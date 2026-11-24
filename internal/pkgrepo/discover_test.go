// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pkgrepo

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Claim (S1, SEC-5): a directory name from the unsigned mirror listing
// becomes a release only in the shape of a release of its series; it
// ends up in file paths, tags and workflow matrices.
func TestValidRelease(t *testing.T) {
	for _, v := range []string{"26.7", "26.7.5", "26.7.10", "26.7.9_1", "26.7_4"} {
		if !ValidRelease("26.7", v) {
			t.Errorf("%q must be valid in 26.7", v)
		}
	}
	for _, v := range []string{"..", ".", "26.1.5", "26.70", "26.7.5-rc1", "26.7.5/x", "26.7.5 ", "26.7.a", "$(id)", "26.7.5_", "26.7..5", ""} {
		if ValidRelease("26.7", v) {
			t.Errorf("%q must not be valid in 26.7", v)
		}
	}
}

func TestCoreMatchesRelease(t *testing.T) {
	for _, c := range [][2]string{{"26.7.9", "26.7.9"}, {"26.7.9_1", "26.7.9"}, {"26.1_4", "26.1"}} {
		if !CoreMatchesRelease(c[0], c[1]) {
			t.Errorf("core %s belongs to %s", c[0], c[1])
		}
	}
	for _, c := range [][2]string{{"26.7.9", "26.7.8"}, {"26.7.10", "26.7.1"}, {"26.7.1", "26.7"}} {
		if CoreMatchesRelease(c[0], c[1]) {
			t.Errorf("core %s does not belong to %s", c[0], c[1])
		}
	}
}

func TestDiscoverIgnoresBadNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<a href="FreeBSD:15:amd64/">x</a>`)
		case "/FreeBSD:15:amd64/":
			fmt.Fprint(w, `<a href="21.1/">x</a><a href="26.7/">x</a>`)
		case "/FreeBSD:15:amd64/21.1/MINT/":
			fmt.Fprint(w, `<a href="21.1.9_dev/">x</a>`)
		case "/FreeBSD:15:amd64/26.7/MINT/":
			fmt.Fprint(w, `<a href="26.7.5/">x</a><a href="../">x</a><a href="26.7.5-rc1/">x</a><a href="26.1.3/">x</a>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var warn bytes.Buffer
	c := NewClient(srv.URL, "")
	c.Warn = &warn
	c.WarnFrom = "25.7" // 21.1.9_dev is skipped without a word
	got, err := c.Discover("amd64")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Version != "26.7.5" {
		t.Errorf("releases %+v, want only 26.7.5", got)
	}
	if n := strings.Count(warn.String(), "warning: mirror lists"); n != 2 {
		t.Errorf("want 2 warnings (26.7.5-rc1, 26.1.3; .. is no listing entry), got:\n%s", warn.String())
	}
}

// Claim (REL-8): a server error or a cut connection is retried; a refusal
// (403/404/410) and other answers are not.
func TestGetRetriesServerErrors(t *testing.T) {
	var calls atomic.Int32
	status := []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		if r.URL.Path == "/gone" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status[min(n, len(status)-1)])
		fmt.Fprint(w, "data")
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	c.Backoff = time.Millisecond
	data, err := c.get(srv.URL + "/ok")
	if err != nil || string(data) != "data" || calls.Load() != 3 {
		t.Errorf("two 5xx then 200: data %q err %v after %d calls", data, err, calls.Load())
	}
	calls.Store(0)
	if _, err := c.get(srv.URL + "/gone"); !errors.Is(err, ErrUnavailable) || calls.Load() != 1 {
		t.Errorf("404 must not be retried: err %v after %d calls", err, calls.Load())
	}
	calls.Store(0)
	status = []int{http.StatusInternalServerError}
	if _, err := c.get(srv.URL + "/ok"); err == nil || calls.Load() != int32(c.Retries+1) {
		t.Errorf("lasting 500: err %v after %d calls, want %d", err, calls.Load(), c.Retries+1)
	}
}
