// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package pkgrepo reads the OPNsense package repository (pkg.opnsense.org):
// release discovery, the signed package catalog and package archives.
package pkgrepo

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
)

// DefaultBaseURL is the official OPNsense package mirror.
const DefaultBaseURL = "https://pkg.opnsense.org"

// Package is one entry of packagesite.yaml (one JSON object per line).
type Package struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Origin      string            `json:"origin"`
	Comment     string            `json:"comment"`
	RepoPath    string            `json:"repopath"`
	PkgSize     int64             `json:"pkgsize"`
	Sum         string            `json:"sum"`
	Deps        map[string]Dep    `json:"deps"`
	Annotations map[string]string `json:"annotations"`
}

// Dep is a package dependency as listed in the catalog.
type Dep struct {
	Origin  string `json:"origin"`
	Version string `json:"version"`
}

// Release identifies one OPNsense release snapshot in the repository.
type Release struct {
	ABI     string // FreeBSD ABI directory, e.g. "FreeBSD:15:amd64"
	Series  string // e.g. "26.7"
	Version string // e.g. "26.7.5"
}

// Client talks to a package mirror and caches downloads on disk.
type Client struct {
	BaseURL  string
	CacheDir string
	HTTP     *http.Client
	// Offline uses cached catalogs instead of downloading them. Without it,
	// catalogs are always fetched: a snapshot can change under the same
	// release number (hotfix revisions, plugin updates).
	Offline bool
	// Warn receives warnings ("warning: ..." lines); nil: none.
	Warn io.Writer
	// WarnFrom: names that are no release are reported only in this series
	// and newer (old series on the mirror have test directories such as
	// "21.1.9_dev" that would be noise in every run); empty: all.
	WarnFrom string
	// Retries of a GET after a server error (5xx) or a connection fault,
	// with a growing pause (Backoff, Backoff*2, ...). Safe: every package
	// and catalog is checked against the signed catalog afterwards.
	Retries int
	Backoff time.Duration
}

// NewClient returns a client for baseURL with a download cache in cacheDir.
func NewClient(baseURL, cacheDir string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), CacheDir: cacheDir,
		HTTP: &http.Client{Timeout: 5 * time.Minute}, Retries: 3, Backoff: 5 * time.Second}
}

// SnapshotURL is the repository root of a release snapshot.
func (c *Client) SnapshotURL(r Release) string {
	return fmt.Sprintf("%s/%s/%s/MINT/%s/latest", c.BaseURL, r.ABI, r.Series, r.Version)
}

var hrefRe = regexp.MustCompile(`href="([^"?/][^"]*)/"`)

func (c *Client) listDirs(url string) ([]string, error) {
	body, err := c.get(url + "/")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, m := range hrefRe.FindAllSubmatch(body, -1) {
		d := strings.TrimPrefix(string(m[1]), "./")
		if d == ".." || d == "." {
			continue // the parent link of a directory listing
		}
		out = append(out, d)
	}
	return out, nil
}

var seriesRe = regexp.MustCompile(`^\d+\.\d+$`)

// releaseRe is the only shape of a release name the project accepts: the
// directory names under MINT/ come from an unsigned listing and end up in
// file paths, tags and workflow matrices. tools/ci/publish.sh checks tags
// against the same shape (TAG_RE) in the job that runs no Go.
var releaseRe = regexp.MustCompile(`^\d+\.\d+(\.\d+)?(_\d+)?$`)

// ValidRelease reports whether v is a release name of series: the shape
// of releaseRe, and the series itself or below it ("26.7", "26.7.5").
func ValidRelease(series, v string) bool {
	return releaseRe.MatchString(v) && (v == series || strings.HasPrefix(v, series+".") || strings.HasPrefix(v, series+"_"))
}

// CoreMatchesRelease reports whether the signed core package version
// belongs to the release name the (unsigned) directory listing gave:
// equal, or the same release with a hotfix revision ("26.7.9_1").
func CoreMatchesRelease(core, release string) bool {
	return core == release || strings.HasPrefix(core, release+"_")
}

// Discover lists all release snapshots for the given architecture
// (e.g. "amd64"), across all FreeBSD ABIs present on the mirror. A
// directory under MINT/ that is not a release name of its series
// (ValidRelease) is skipped with a warning to Warn, never used.
func (c *Client) Discover(arch string) ([]Release, error) {
	abis, err := c.listDirs(c.BaseURL)
	if err != nil {
		return nil, err
	}
	var out []Release
	for _, abi := range abis {
		if !strings.HasPrefix(abi, "FreeBSD:") || !strings.HasSuffix(abi, ":"+arch) {
			continue
		}
		series, err := c.listDirs(c.BaseURL + "/" + abi)
		if err != nil {
			return nil, err
		}
		for _, s := range series {
			if !seriesRe.MatchString(s) {
				continue
			}
			mints, err := c.listDirs(fmt.Sprintf("%s/%s/%s/MINT", c.BaseURL, abi, s))
			if err != nil {
				continue // series without snapshots
			}
			for _, v := range mints {
				if !ValidRelease(s, v) {
					if c.Warn != nil && (c.WarnFrom == "" || CompareVersions(s, c.WarnFrom) >= 0) {
						fmt.Fprintf(c.Warn, "warning: mirror lists %q under %s/%s/MINT, not a release name of the series; ignored\n", v, abi, s)
					}
					continue
				}
				out = append(out, Release{ABI: abi, Series: s, Version: v})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return CompareVersions(out[i].Version, out[j].Version) < 0 })
	return out, nil
}

// FindRelease locates a release version (e.g. "26.7.5") on the mirror.
func (c *Client) FindRelease(arch, version string) (Release, error) {
	all, err := c.Discover(arch)
	if err != nil {
		return Release{}, err
	}
	for _, r := range all {
		if r.Version == version {
			return r, nil
		}
	}
	return Release{}, fmt.Errorf("release %s not found on %s", version, c.BaseURL)
}

// Catalog is the parsed packagesite.yaml of a snapshot plus the raw signed
// material needed for verification.
type Catalog struct {
	Release   Release
	Packages  []Package
	YAML      []byte // packagesite.yaml as signed
	Signature []byte // packagesite.yaml.sig
	PublicKey []byte // packagesite.yaml.pub
}

// Lookup returns the package with the given name.
func (cat *Catalog) Lookup(name string) (Package, bool) {
	for _, p := range cat.Packages {
		if p.Name == name {
			return p, true
		}
	}
	return Package{}, false
}

// Plugins returns all os-* packages, sorted by name.
func (cat *Catalog) Plugins() []Package {
	var out []Package
	for _, p := range cat.Packages {
		if strings.HasPrefix(p.Name, "os-") {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// FetchCatalog downloads and unpacks packagesite.pkg of a release snapshot.
func (c *Client) FetchCatalog(r Release) (*Catalog, error) {
	var raw []byte
	var err error
	if c.Offline {
		raw, err = c.cached(r, "packagesite.pkg", c.SnapshotURL(r)+"/packagesite.pkg")
	} else {
		raw, err = c.fresh(r, "packagesite.pkg", c.SnapshotURL(r)+"/packagesite.pkg")
	}
	if err != nil {
		return nil, err
	}
	files, err := ReadArchive(bytes.NewReader(raw), func(name string) bool {
		return strings.HasPrefix(filepath.Base(name), "packagesite.yaml")
	})
	if err != nil {
		return nil, fmt.Errorf("unpack catalog: %w", err)
	}
	cat := &Catalog{Release: r}
	for name, data := range files {
		switch filepath.Base(name) {
		case "packagesite.yaml":
			cat.YAML = data
		case "packagesite.yaml.sig":
			cat.Signature = data
		case "packagesite.yaml.pub":
			cat.PublicKey = data
		}
	}
	if cat.YAML == nil {
		return nil, errors.New("catalog has no packagesite.yaml")
	}
	pkgs, err := ParseCatalog(cat.YAML)
	if err != nil {
		return nil, err
	}
	cat.Packages = pkgs
	return cat, nil
}

// ParseCatalog parses packagesite.yaml (JSON lines).
func ParseCatalog(data []byte) ([]Package, error) {
	var out []Package
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var p Package
		if err := json.Unmarshal(line, &p); err != nil {
			return nil, fmt.Errorf("catalog line: %w", err)
		}
		out = append(out, p)
	}
	return out, sc.Err()
}

// FetchPackage downloads a package archive (cached) and returns its bytes.
func (c *Client) FetchPackage(r Release, p Package) ([]byte, error) {
	url := c.SnapshotURL(r) + "/" + p.RepoPath
	name := filepath.Base(p.RepoPath)
	check := func(data []byte) error {
		if p.PkgSize > 0 && int64(len(data)) != p.PkgSize {
			return fmt.Errorf("%s: size %d, catalog says %d", p.RepoPath, len(data), p.PkgSize)
		}
		if err := VerifyPackage(data, p.Sum); err != nil {
			return fmt.Errorf("%s: %w", p.RepoPath, err)
		}
		return nil
	}
	data, err := c.cached(r, name, url)
	if err != nil {
		return nil, err
	}
	if err := check(data); err == nil {
		return data, nil
	} else if c.Offline {
		return nil, err
	}
	// a cached file that does not match the current catalog (rebuilt
	// package under the same name) is fetched again, once
	if data, err = c.fresh(r, name, url); err != nil {
		return nil, err
	}
	return data, check(data)
}

// fresh downloads url and replaces the cached copy.
func (c *Client) fresh(r Release, name, url string) ([]byte, error) {
	data, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if c.CacheDir != "" {
		path := filepath.Join(c.CacheDir, r.ABI, r.Version, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path+".tmp", data, 0o644); err != nil {
			return nil, err
		}
		if err := os.Rename(path+".tmp", path); err != nil {
			return nil, err
		}
	}
	return data, nil
}

func (c *Client) cached(r Release, name, url string) ([]byte, error) {
	var path string
	if c.CacheDir != "" {
		path = filepath.Join(c.CacheDir, r.ABI, r.Version, name)
		if data, err := os.ReadFile(path); err == nil {
			return data, nil
		}
	}
	data, err := c.get(url)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
	}
	return data, nil
}

// get fetches url. A server error (5xx) or a connection fault is retried
// (Retries, Backoff); 403/404/410 are ErrUnavailable at once, any other
// status is an error at once.
func (c *Client) get(url string) ([]byte, error) {
	var err error
	for attempt := 0; ; attempt++ {
		var data []byte
		var retry bool
		data, retry, err = c.getOnce(url)
		if err == nil || !retry || attempt >= c.Retries {
			return data, err
		}
		if c.Warn != nil {
			fmt.Fprintf(c.Warn, "retrying (%d of %d): %v\n", attempt+1, c.Retries, err)
		}
		time.Sleep(c.Backoff << attempt)
	}
}

func (c *Client) getOnce(url string) (data []byte, retry bool, err error) {
	resp, err := c.HTTP.Get(url)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		data, err = io.ReadAll(resp.Body)
		return data, err != nil, err
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusGone:
		return nil, false, fmt.Errorf("GET %s: %s: %w", url, resp.Status, ErrUnavailable)
	case resp.StatusCode >= 500:
		return nil, true, fmt.Errorf("GET %s: %s", url, resp.Status)
	default:
		return nil, false, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
}

// ErrUnavailable: the mirror refuses or no longer has a file (HTTP 403,
// 404, 410), e.g. the packages of a withdrawn snapshot whose catalog is
// still served. Callers decide whether that is fatal.
var ErrUnavailable = errors.New("not available on the mirror")

// ReadArchive reads a (possibly zstd-, xz- or gzip-compressed) tar archive and
// returns the regular files whose name matches keep, keyed by their path
// without a leading slash.
func ReadArchive(r io.Reader, keep func(name string) bool) (map[string][]byte, error) {
	br := bufio.NewReader(r)
	magic, _ := br.Peek(4)
	var src io.Reader = br
	switch {
	case bytes.Equal(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		dec, err := zstd.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer dec.Close()
		src = dec
	case bytes.Equal(magic, []byte{0xfd, 0x37, 0x7a, 0x58}):
		x, err := xz.NewReader(br)
		if err != nil {
			return nil, err
		}
		src = x
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, err
		}
		defer gz.Close()
		src = gz
	}
	tr := tar.NewReader(src)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(h.Name, "/")
		if keep != nil && !keep(name) {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		out[name] = data
	}
	return out, nil
}

// CompareVersions compares package versions such as 26.7.9_1, 1.0_6 or
// 2.1a: release parts numerically (a part's letter suffix after its
// number, "2.1" < "2.1a" < "2.2"), then the hotfix revision numerically
// ("_2" < "_10"). 26.7 < 26.7_1 < 26.7.1; missing parts count as 0.
//
// The same algorithm is capability.CompareVersions in opnsense-api-go;
// both test against internal/capability/version-order.json, which is
// published with the capability data. Change the table first.
func CompareVersions(a, b string) int {
	ra, reva, _ := strings.Cut(a, "_")
	rb, revb, _ := strings.Cut(b, "_")
	pa, pb := strings.Split(ra, "."), strings.Split(rb, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		x, y := "0", "0"
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if c := comparePart(x, y); c != 0 {
			return c
		}
	}
	if reva == "" {
		reva = "0"
	}
	if revb == "" {
		revb = "0"
	}
	return comparePart(reva, revb)
}

// comparePart compares "12" < "12a" < "13" numerically, then by suffix.
func comparePart(a, b string) int {
	na, sa := splitNum(a)
	nb, sb := splitNum(b)
	switch {
	case na < nb:
		return -1
	case na > nb:
		return 1
	}
	return strings.Compare(sa, sb)
}

func splitNum(s string) (int, string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i]) // empty -> 0
	return n, s[i:]
}
