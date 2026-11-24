// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// The HTTP client of the contract test: talks only to the throwaway
// instance, decodes answers, reads its version and packages.

package verify

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
)

// Client talks to the test instance with an API key.
type Client struct {
	BaseURL      string
	Key, Secret  string
	HTTP         *http.Client
	AllowedHosts []string // safety net: refuse anything else
	// pkgHTTP speaks HTTP/2, for the package list only: the contract
	// checks stay on HTTP/1.1 (see NewClient), but the package detection
	// must not fail when the web server cuts the large firmware/info answer
	// over HTTP/1.1 (seen again 2026-11-17 on 26.7.9_3).
	pkgHTTP *http.Client

	// Schema, when set, validates every decoded response against the
	// rendered OpenAPI document (what generated clients will decode).
	Schema        Validator
	schemaResults []*Result

	// retryPause is the pause before the one retry of a read (doWith);
	// retried collects what was retried until the next check takes it
	// (noted).
	retryPause time.Duration
	retried    []string
}

// RequestTimeout is the time limit of one request on an instance with
// hardware acceleration; ScaleTimeouts stretches it for software emulation.
const RequestTimeout = 60 * time.Second

// Validator checks a response body against the document's 200 response
// schema of an operation (path template, e.g. ".../getDnsbl/{uuid}").
type Validator interface {
	Has(method, pathTemplate string) bool
	Validate(method, pathTemplate string, body any) []string
}

// schema records a "schema" check for a decoded response. A mismatch on an
// endpoint the generator already marked (overridden action, custom logic)
// becomes known-gap, as for the shape checks.
func (c *Client) schema(e *ir.Endpoint, method, pathTemplate string, body any) {
	if c.Schema == nil || !c.Schema.Has(method, pathTemplate) {
		return
	}
	r := &Result{Path: pathTemplate, Method: method, Check: CheckSchema, HTTP: 200}
	r.Findings = c.Schema.Validate(method, pathTemplate, body)
	r.Status = status(r.Findings)
	c.schemaResults = append(c.schemaResults, knownGap(r, e))
}

// TestInstance accepts base only when it is https on exactly one of the
// allowed hosts: parsed, not compared as text, so userinfo
// ("https://127.0.0.1:1@192.0.2.1" has the host 192.0.2.1), a path, a
// query or a fragment cannot hide another host (requirement C1: tests run
// only against the throwaway VM).
func TestInstance(base string, allowed []string) error {
	u, err := url.Parse(base)
	if err == nil && u.Scheme == "https" && u.User == nil && u.Opaque == "" && u.RawQuery == "" && u.Fragment == "" &&
		strings.TrimRight(u.Path, "/") == "" && slices.Contains(allowed, u.Hostname()) {
		return nil
	}
	return fmt.Errorf("refusing %s: only https on %v is allowed as a test instance", base, allowed)
}

// NewClient creates a client for a local test instance. The instance uses
// a self-signed certificate; certificate checks are skipped only for hosts
// in allowed (normally 127.0.0.1 port forwards to the throwaway VM).
func NewClient(base, key, secret string, allowed []string) (*Client, error) {
	if err := TestInstance(base, allowed); err != nil {
		return nil, err
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- throwaway local VM only
		// HTTP/1.1 on purpose: Go clients use HTTP/2, so the contract test
		// covers the other protocol. The OPNsense web server sometimes cuts
		// large HTTP/1.1 answers (broken chunked encoding; firmware/info on
		// 26.7.9_3, 2026-11-17); the contract test keeps that visible: a
		// read that is cut is repeated once and noted (doWith; concepts.md,
		// section 7.2).
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	tr2 := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- throwaway local VM only
		ForceAttemptHTTP2: true,
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Key: key, Secret: secret, AllowedHosts: allowed,
		HTTP:       &http.Client{Timeout: RequestTimeout, Transport: tr},
		pkgHTTP:    &http.Client{Timeout: RequestTimeout, Transport: tr2},
		retryPause: 3 * time.Second}, nil
}

// ScaleTimeouts multiplies the request time limits by factor, the
// slowdown of the test instance (tools/testvm.sh: TESTVM_SLOW, 6 under
// software emulation). Factors below 1 count as 1.
func (c *Client) ScaleTimeouts(factor int) {
	if factor < 1 {
		factor = 1
	}
	for _, hc := range []*http.Client{c.HTTP, c.pkgHTTP} {
		if hc != nil {
			hc.Timeout = RequestTimeout * time.Duration(factor)
		}
	}
}

func (c *Client) do(method, path string, body any) (int, any, []byte, error) {
	return c.doWith(c.HTTP, method, path, body)
}

// doWith sends one request. A read (GET) that fails in transport (no
// answer, a cut answer, 502 or 503 while the web server restarts) is
// repeated once after a short pause; the retry is recorded and ends up as
// a note in the result of the check that made the request (noted), so an
// intermittent fault stays visible without failing the check. A second
// failure is returned as it is. Writes are never repeated: the first
// attempt may have been applied.
func (c *Client) doWith(hc *http.Client, method, path string, body any) (int, any, []byte, error) {
	code, v, raw, err := c.doOnce(hc, method, path, body)
	if method != http.MethodGet || !transportFault(code, err) {
		return code, v, raw, err
	}
	first := err
	if first == nil {
		first = fmt.Errorf("HTTP %d", code)
	}
	time.Sleep(c.retryPause)
	code, v, raw, err = c.doOnce(hc, method, path, body)
	if !transportFault(code, err) {
		c.retried = append(c.retried, fmt.Sprintf("%s %s answered only on the second attempt; the first failed in transport: %v", method, path, first))
	}
	return code, v, raw, err
}

// transportFault: the request got no complete answer from the
// application (as opposed to an answer the check has to judge).
func transportFault(code int, err error) bool {
	if err != nil {
		var nj *NonJSON
		return !errors.As(err, &nj)
	}
	return code == http.StatusBadGateway || code == http.StatusServiceUnavailable
}

// noted moves the recorded retries into r's notes and returns r. Checks
// run one after the other, so what was retried since the last check
// belongs to this one.
func (c *Client) noted(r *Result) *Result {
	if len(c.retried) > 0 {
		r.Notes = append(r.Notes, c.retried...)
		c.retried = nil
	}
	return r
}

func (c *Client) doOnce(hc *http.Client, method, path string, body any) (int, any, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.SetBasicAuth(c.Key, c.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if strings.Contains(err.Error(), "chunked") {
			err = fmt.Errorf("%w (OPNsense web server: broken chunked encoding on large responses over HTTP/1.1)", err)
		}
		return resp.StatusCode, nil, raw, fmt.Errorf("%s %s: reading response: %w", method, path, err)
	}
	var v any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil && resp.StatusCode == 200 {
			// downloads (CSV, PAC, templates) are legitimate non-JSON answers
			return resp.StatusCode, nil, raw, &NonJSON{ContentType: resp.Header.Get("Content-Type")}
		}
	}
	return resp.StatusCode, v, raw, nil
}

// NonJSON is returned for a 200 response that is not JSON.
type NonJSON struct{ ContentType string }

func (e *NonJSON) Error() string { return "response is not JSON (Content-Type " + e.ContentType + ")" }

// InstalledPackages returns the names of the packages installed on the
// instance (core and plugins), from /api/core/firmware/info.
// The firmware reloads its package state after installations and briefly
// returns no lists, so this retries for up to two minutes.
func (c *Client) InstalledPackages() (map[string]bool, error) {
	var lastErr error
	for i := 0; i < 24; i++ {
		pk, err := c.installedOnce()
		if err == nil {
			return pk, nil
		}
		lastErr = err
		time.Sleep(5 * time.Second)
	}
	return nil, lastErr
}

func (c *Client) installedOnce() (map[string]bool, error) {
	hc := c.pkgHTTP
	if hc == nil { // a Client built without NewClient (tests)
		hc = c.HTTP
	}
	code, v, raw, err := c.doWith(hc, "GET", "/api/core/firmware/info", nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("firmware/info: HTTP %d %s", code, trunc(raw))
	}
	out := map[string]bool{}
	m, _ := v.(map[string]any)
	for _, list := range []string{"package", "plugin"} {
		items, _ := m[list].([]any)
		for _, it := range items {
			p, _ := it.(map[string]any)
			name, _ := p["name"].(string)
			if inst, _ := p["installed"].(string); inst == "1" && name != "" {
				out[name] = true
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("firmware/info lists no installed packages")
	}
	return out, nil
}

// InstanceVersion returns product_version of the instance.
func (c *Client) InstanceVersion() (string, error) {
	code, v, raw, err := c.do("GET", "/api/core/firmware/status", nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("firmware/status: HTTP %d %s", code, trunc(raw))
	}
	m, _ := v.(map[string]any)
	if pv, ok := m["product_version"].(string); ok {
		return pv, nil
	}
	if p, ok := m["product"].(map[string]any); ok {
		if pv, ok := p["product_version"].(string); ok {
			return pv, nil
		}
	}
	return "", fmt.Errorf("no product_version in firmware/status")
}

func trunc(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}
