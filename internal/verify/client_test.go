// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
)

// flaky answers path with the given faults first, then normally; it counts
// the requests per path. Faults: "cut" (a chunked answer that breaks off,
// as the OPNsense web server does over HTTP/1.1), "reset" (connection
// closed without an answer), or an HTTP status.
type flaky struct {
	mu     sync.Mutex
	path   string
	faults []string
	n      map[string]int
}

func (f *flaky) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.n[r.URL.Path]++
	var fault string
	if r.URL.Path == f.path && len(f.faults) > 0 {
		fault, f.faults = f.faults[0], f.faults[1:]
	}
	f.mu.Unlock()
	switch fault {
	case "":
		_, _ = w.Write([]byte(`{"status":"ok","product_version":"26.7.9"}`))
	case "cut", "reset":
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			panic(err)
		}
		if fault == "cut" {
			_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n10\r\n{\"status\":\"ok\",")
			_ = buf.Flush()
		}
		_ = conn.Close()
	case "502":
		w.WriteHeader(http.StatusBadGateway)
	case "503":
		w.WriteHeader(http.StatusServiceUnavailable)
	default:
		panic("unknown fault " + fault)
	}
}

func flakyClient(t *testing.T, path string, faults ...string) (*Client, *flaky) {
	t.Helper()
	f := &flaky{path: path, faults: faults, n: map[string]int{}}
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, "k", "s", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	c.retryPause = time.Millisecond
	return c, f
}

// A read that fails in transport once is repeated once; the check passes
// and its result carries a note naming the first failure.
func TestReadRetriedOnceAfterTransportFault(t *testing.T) {
	const p = "/api/x/service/status"
	for _, fault := range []string{"cut", "reset", "502", "503"} {
		t.Run(fault, func(t *testing.T) {
			c, f := flakyClient(t, p, fault)
			// a fresh connection per request: net/http itself repeats a GET
			// once when a reused connection was closed, which would hide
			// the case
			c.HTTP.Transport.(*http.Transport).DisableKeepAlives = true
			rel := &ir.Release{Version: "26.7.9", Endpoints: []*ir.Endpoint{{Path: p, Kind: ir.KindServiceStatus, Package: "opnsense"}}}
			rep := Run(c, rel, map[string]bool{"opnsense": true}, false)
			if len(rep.Results) != 1 {
				t.Fatalf("results: %+v", rep.Results)
			}
			r := rep.Results[0]
			if r.Status != StatusOK {
				t.Errorf("status %s %v, want ok after the retry", r.Status, r.Findings)
			}
			if len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "second attempt") {
				t.Errorf("notes %q, want one note about the retry", r.Notes)
			}
			if f.n[p] != 2 {
				t.Errorf("%d requests, want 2", f.n[p])
			}
		})
	}
}

// A second transport failure is an error, as without retry; nothing is
// noted as a successful retry.
func TestReadFailingTwiceIsAnError(t *testing.T) {
	const p = "/api/x/service/status"
	c, f := flakyClient(t, p, "cut", "503")
	rel := &ir.Release{Version: "26.7.9", Endpoints: []*ir.Endpoint{{Path: p, Kind: ir.KindServiceStatus, Package: "opnsense"}}}
	rep := Run(c, rel, map[string]bool{"opnsense": true}, false)
	if r := rep.Results[0]; r.Status != StatusError || len(r.Notes) != 0 {
		t.Errorf("status %s notes %q, want error without note", r.Status, r.Notes)
	}
	if f.n[p] != 2 {
		t.Errorf("%d requests, want 2 (one retry, not more)", f.n[p])
	}
}

// Writes are never repeated: the first attempt may have been applied.
func TestWriteIsNotRetried(t *testing.T) {
	const p = "/api/x/settings/addHost"
	c, f := flakyClient(t, p, "503")
	code, _, _, _ := c.do("POST", p, map[string]any{"host": map[string]any{}})
	if code != http.StatusServiceUnavailable || f.n[p] != 1 {
		t.Errorf("HTTP %d after %d requests, want 503 after 1", code, f.n[p])
	}
	if len(c.retried) != 0 {
		t.Errorf("retried %q", c.retried)
	}
}

// Answers the check has to judge (a 404, a 500 with an error page) are
// not transport faults and are not repeated.
func TestApplicationAnswerIsNotRetried(t *testing.T) {
	const p = "/api/x/service/status"
	c, f := flakyClient(t, p)
	f.path = "" // every path answers normally; /api/none is a 404 below
	srv404 := httptest.NewTLSServer(http.NotFoundHandler())
	defer srv404.Close()
	c.BaseURL = srv404.URL
	c.HTTP = srv404.Client()
	if code, _, _, _ := c.do("GET", "/api/none", nil); code != http.StatusNotFound || len(c.retried) != 0 {
		t.Errorf("HTTP %d, retried %q", code, c.retried)
	}
}

// Request time limits stretch with the slowdown of the test instance.
func TestScaleTimeouts(t *testing.T) {
	c, err := NewClient("https://127.0.0.1:1", "k", "s", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	c.ScaleTimeouts(6)
	if c.HTTP.Timeout != 6*RequestTimeout || c.pkgHTTP.Timeout != 6*RequestTimeout {
		t.Errorf("timeouts %s %s, want %s", c.HTTP.Timeout, c.pkgHTTP.Timeout, 6*RequestTimeout)
	}
	c.ScaleTimeouts(0)
	if c.HTTP.Timeout != RequestTimeout {
		t.Errorf("factor 0: %s, want %s", c.HTTP.Timeout, RequestTimeout)
	}
}
