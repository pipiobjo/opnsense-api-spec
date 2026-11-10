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
}

// NewClient returns a client for baseURL with a download cache in cacheDir.
func NewClient(baseURL, cacheDir string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), CacheDir: cacheDir,
		HTTP: &http.Client{Timeout: 5 * time.Minute}}
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
		out = append(out, strings.TrimPrefix(string(m[1]), "./"))
	}
	return out, nil
}

var seriesRe = regexp.MustCompile(`^\d+\.\d+$`)

// Discover lists all release snapshots for the given architecture
// (e.g. "amd64"), across all FreeBSD ABIs present on the mirror.
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

func (c *Client) get(url string) ([]byte, error) {
	resp, err := c.HTTP.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

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

// CompareVersions compares OPNsense release versions like "26.7" and
// "26.7.10" numerically. Non-numeric parts compare as strings.
func CompareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		var nx, ny int
		_, ex := fmt.Sscanf(x, "%d", &nx)
		_, ey := fmt.Sscanf(y, "%d", &ny)
		switch {
		case x == y:
			continue
		case x == "":
			return -1
		case y == "":
			return 1
		case ex == nil && ey == nil && nx != ny:
			if nx < ny {
				return -1
			}
			return 1
		default:
			return strings.Compare(x, y)
		}
	}
	return 0
}
