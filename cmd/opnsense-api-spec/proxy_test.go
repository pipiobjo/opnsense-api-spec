// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
)

// TestProxyRefreshesCatalog: a hotfix revision changes the catalog inside
// the same snapshot. A restarted proxy must serve the new catalog, while
// versioned package files stay cached.
func TestProxyRefreshesCatalog(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	catalog := "core 26.7.9"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits[r.URL.Path]++
		switch r.URL.Path {
		case "/FreeBSD:15:amd64/26.7/MINT/26.7.9/latest/meta.conf":
			io.WriteString(w, catalog)
		case "/FreeBSD:15:amd64/26.7/MINT/26.7.9/latest/All/os-frr-2.1.pkg":
			io.WriteString(w, "package")
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	c := &pkgrepo.Client{BaseURL: up.URL, HTTP: up.Client()}
	dir := t.TempDir()
	get := func(h http.Handler, path string) string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Body.String()
	}
	const meta, pkg = "/FreeBSD:15:amd64/26.7/latest/meta.conf", "/FreeBSD:15:amd64/26.7/latest/All/os-frr-2.1.pkg"

	h := proxyHandler(c, "FreeBSD:15:amd64", "26.7", "26.7.9", dir)
	if got := get(h, meta); got != "core 26.7.9" {
		t.Fatalf("meta: %q", got)
	}
	get(h, meta)
	get(h, pkg)
	get(h, pkg)
	if n := hits["/FreeBSD:15:amd64/26.7/MINT/26.7.9/latest/meta.conf"]; n != 1 {
		t.Errorf("catalog fetched %d times within one proxy run, want 1", n)
	}

	catalog = "core 26.7.9_1" // hotfix published into the same snapshot
	h2 := proxyHandler(c, "FreeBSD:15:amd64", "26.7", "26.7.9", dir)
	if got := get(h2, meta); got != "core 26.7.9_1" {
		t.Errorf("restarted proxy served a stale catalog: %q", got)
	}
	get(h2, pkg)
	if n := hits["/FreeBSD:15:amd64/26.7/MINT/26.7.9/latest/All/os-frr-2.1.pkg"]; n != 1 {
		t.Errorf("versioned package fetched %d times, want 1 (cached)", n)
	}
	if got := get(h2, "/FreeBSD:15:amd64/26.7/latest/../../etc/passwd"); got != "404 page not found\n" {
		t.Errorf("path traversal not denied: %q", got)
	}
}
