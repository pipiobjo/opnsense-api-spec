// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// The HTTP client of the contract test: talks only to the throwaway
// instance, decodes answers, reads its version and packages.

package verify

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	// Schema, when set, validates every decoded response against the
	// rendered OpenAPI document (what generated clients will decode).
	Schema        Validator
	schemaResults []*Result
}

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

// NewClient creates a client for a local test instance. The instance uses
// a self-signed certificate; certificate checks are skipped only for hosts
// in allowed (normally 127.0.0.1 port forwards to the throwaway VM).
func NewClient(base, key, secret string, allowed []string) (*Client, error) {
	ok := false
	for _, h := range allowed {
		if strings.HasPrefix(base, "https://"+h+":") || base == "https://"+h {
			ok = true
		}
	}
	if !ok {
		return nil, fmt.Errorf("refusing %s: only %v are allowed as test instances", base, allowed)
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- throwaway local VM only
		// HTTP/1.1 on purpose: Go clients use HTTP/2, so the contract test
		// covers the other protocol. Large HTTP/1.1 answers were once cut
		// (broken chunked encoding, 26.7 test VMs, 2026-11-11); a recurrence
		// shows as a failure here (concepts.md, section 7.2).
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Key: key, Secret: secret, AllowedHosts: allowed,
		HTTP: &http.Client{Timeout: 60 * time.Second, Transport: tr}}, nil
}

func (c *Client) do(method, path string, body any) (int, any, []byte, error) {
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
	resp, err := c.HTTP.Do(req)
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
	code, v, raw, err := c.do("GET", "/api/core/firmware/info", nil)
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
