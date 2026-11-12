// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
)

// proxy serves the package snapshot of exactly one release to an isolated
// test VM: /<ABI>/<series>/{latest,aux}/<file> on the VM side maps to
// <mirror>/<ABI>/<series>/MINT/<release>/{latest,aux}/<file> upstream.
// Pinning the snapshot keeps the repository's core version equal to the
// installed one, which the firmware requires before installing plugins.
// Files are passed through unchanged (pkg in the VM verifies signatures
// and checksums itself) and cached on disk.
func proxy(c *pkgrepo.Client, arch, release, listen string) error {
	r, err := c.FindRelease(arch, release)
	if err != nil {
		return err
	}
	re := regexp.MustCompile(`^/` + regexp.QuoteMeta(r.ABI) + `/` + regexp.QuoteMeta(r.Series) +
		`/(latest|aux)/([A-Za-z0-9._+,/-]+)$`)
	// FreeBSD base and kernel sets (signed, checked by opnsense-update) live
	// per series, not per snapshot: /<ABI>/<series>/sets/{base,kernel}-*.txz[.sig]
	setsRe := regexp.MustCompile(`^/` + regexp.QuoteMeta(r.ABI) + `/` + regexp.QuoteMeta(r.Series) +
		`/sets/((?:base|kernel)-[A-Za-z0-9._+-]+\.txz(?:\.sig)?)$`)
	cacheDir := filepath.Join(".cache", "proxy", r.ABI, r.Version)
	var mu sync.Mutex // one upstream fetch at a time per file is enough here
	h := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		m := re.FindStringSubmatch(req.URL.Path)
		if sm := setsRe.FindStringSubmatch(req.URL.Path); m == nil && sm != nil {
			// sets: same path upstream, cached per series
			m = []string{req.URL.Path, "sets", sm[1]}
		}
		if m == nil || strings.Contains(m[2], "..") || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
			fmt.Fprintf(os.Stderr, "deny %s %s\n", req.Method, req.URL.Path)
			http.NotFound(w, req)
			return
		}
		local := filepath.Join(cacheDir, m[1], filepath.FromSlash(m[2]))
		mu.Lock()
		if _, err := os.Stat(local); err != nil {
			up := fmt.Sprintf("%s/%s/%s/MINT/%s/%s/%s", c.BaseURL, r.ABI, r.Series, r.Version, m[1], m[2])
			if m[1] == "sets" {
				up = fmt.Sprintf("%s/%s/%s/sets/%s", c.BaseURL, r.ABI, r.Series, m[2])
			}
			if err := download(c.HTTP, up, local); err != nil {
				mu.Unlock()
				fmt.Fprintf(os.Stderr, "miss %s: %v\n", req.URL.Path, err)
				http.NotFound(w, req)
				return
			}
		}
		mu.Unlock()
		fmt.Fprintf(os.Stderr, "serve %s\n", req.URL.Path)
		http.ServeFile(w, req, local)
	})
	fmt.Fprintf(os.Stderr, "serving %s snapshot %s (%s) on http://%s\n", r.Series, r.Version, r.ABI, listen)
	return http.ListenAndServe(listen, h)
}

func download(hc *http.Client, url, dst string) error {
	resp, err := hc.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
