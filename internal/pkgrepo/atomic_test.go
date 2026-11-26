// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pkgrepo

import (
	"bytes"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Claim: the download cache can be shared by several writers at once
// (parallel test VMs, each with its own package proxy, fetch the same
// catalog): no writer fails because of another, and a reader sees no file
// or the complete one, never a partial one. Both cache paths are written
// concurrently (fresh replaces, cached fills a miss).
func TestCacheWritesAreAtomic(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789abcdef"), 1<<16) // 1 MiB
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer up.Close()
	c := &Client{BaseURL: up.URL, CacheDir: t.TempDir(), HTTP: up.Client()}
	r := Release{ABI: "FreeBSD:15:amd64", Series: "26.7", Version: "26.7.10"}
	path := filepath.Join(c.CacheDir, r.ABI, r.Version, "packagesite.pkg")
	for _, write := range []struct {
		name string
		fn   func() ([]byte, error)
	}{
		{"fresh", func() ([]byte, error) { return c.fresh(r, "packagesite.pkg", up.URL+"/x") }},
		{"cached", func() ([]byte, error) { os.Remove(path); return c.cached(r, "packagesite.pkg", up.URL+"/x") }},
	} {
		t.Run(write.name, func(t *testing.T) {
			stop := make(chan struct{})
			var readers sync.WaitGroup
			var partial int
			var mu sync.Mutex
			for range 4 {
				readers.Add(1)
				go func() {
					defer readers.Done()
					for {
						select {
						case <-stop:
							return
						default:
						}
						got, err := os.ReadFile(path)
						if errors.Is(err, fs.ErrNotExist) {
							continue
						}
						if err == nil && !bytes.Equal(got, body) {
							mu.Lock()
							partial++
							mu.Unlock()
						}
					}
				}()
			}
			var writers sync.WaitGroup
			for range 16 {
				writers.Add(1)
				go func() {
					defer writers.Done()
					for range 5 {
						if _, err := write.fn(); err != nil {
							t.Errorf("concurrent write: %v", err)
						}
					}
				}()
			}
			writers.Wait()
			time.Sleep(10 * time.Millisecond)
			close(stop)
			readers.Wait()
			if partial > 0 {
				t.Errorf("%d reads saw a partial file", partial)
			}
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, body) {
				t.Errorf("final file: %d bytes, %v", len(got), err)
			}
			left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*"))
			if len(left) > 0 {
				t.Errorf("temporary files left: %v", left)
			}
		})
	}
}
