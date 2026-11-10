package pipeline

import (
	"archive/tar"
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/fragment"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
)

// --- fake mirror -----------------------------------------------------------

// mirror serves files like pkg.opnsense.org, with generated directory
// listings (href="name/") for every directory.
type mirror struct {
	mu    sync.Mutex
	files map[string][]byte
	key   *rsa.PrivateKey
}

func (m *mirror) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if data, ok := m.files[r.URL.Path]; ok {
		_, _ = w.Write(data)
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/") {
		http.NotFound(w, r)
		return
	}
	seen := map[string]bool{}
	for p := range m.files {
		rest, ok := strings.CutPrefix(p, r.URL.Path)
		if !ok {
			continue
		}
		if i := strings.Index(rest, "/"); i > 0 {
			seen[rest[:i]] = true
		}
	}
	if len(seen) == 0 {
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	b.WriteString(`<html><body><a href="?C=N;O=D">Name</a><a href="/">Parent</a>`)
	for d := range seen {
		fmt.Fprintf(&b, `<a href="./%s/">%s/</a>`, d, d)
	}
	b.WriteString("</body></html>")
	_, _ = w.Write([]byte(b.String()))
}

func tarball(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(files[n])
	}
	_ = tw.Close()
	return buf.Bytes()
}

type pkgSpec struct {
	name, version string
	files         map[string][]byte // relative to the mvc app dir
}

const abi, series = "FreeBSD:15:amd64", "26.7"

// publish writes a signed catalog and the package files of one release.
func (m *mirror) publish(t *testing.T, release string, pkgs []pkgSpec) {
	t.Helper()
	base := fmt.Sprintf("/%s/%s/MINT/%s/latest/", abi, series, release)
	var lines bytes.Buffer
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range pkgs {
		files := map[string][]byte{"+MANIFEST": []byte("{}")}
		for n, c := range p.files {
			files["/usr/local/opnsense/mvc/app/"+n] = c
		}
		data := tarball(t, files)
		sum := sha256.Sum256(data)
		repo := "All/" + p.name + "-" + p.version + ".pkg"
		m.files[base+repo] = data
		line, _ := json.Marshal(map[string]any{
			"name": p.name, "version": p.version, "repopath": repo, "pkgsize": len(data),
			"sum": hex.EncodeToString(sum[:]), "annotations": map[string]string{"product_hash": "c0ffee"},
		})
		lines.Write(line)
		lines.WriteByte('\n')
	}
	yaml := lines.Bytes()
	d := sha256.Sum256(yaml)
	msg := sha256.Sum256([]byte(hex.EncodeToString(d[:])))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, msg[:])
	if err != nil {
		t.Fatal(err)
	}
	m.files[base+"packagesite.pkg"] = tarball(t, map[string][]byte{
		"packagesite.yaml": yaml, "packagesite.yaml.sig": sig, "packagesite.yaml.pub": m.pub(),
	})
}

func (m *mirror) pub() []byte {
	der, _ := x509.MarshalPKIXPublicKey(&m.key.PublicKey)
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// --- fixture packages ----------------------------------------------------

// core: the Base classes and the Demo module of the openapi fixtures
func coreFiles(t *testing.T, extraField bool) map[string][]byte {
	t.Helper()
	root := "../openapi/testdata/app"
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if extraField { // the hotfix adds a field to the Demo model
		xml := string(out["models/OPNsense/Demo/Demo.xml"])
		out["models/OPNsense/Demo/Demo.xml"] = []byte(strings.Replace(xml,
			`<safesearch type="BooleanField"/>`, `<safesearch type="BooleanField"/><hotfix type="BooleanField"/>`, 1))
	}
	return out
}

func pluginFiles() map[string][]byte {
	return map[string][]byte{
		"models/OPNsense/Extra/Extra.xml": []byte(`<model><mount>//OPNsense/extra</mount><version>1.0.0</version>
<items><general><enabled type="BooleanField"><Default>0</Default><Required>Y</Required></enabled></general></items></model>`),
		"controllers/OPNsense/Extra/Api/SettingsController.php": []byte(`<?php
namespace OPNsense\Extra\Api;
use OPNsense\Base\ApiMutableModelControllerBase;
class SettingsController extends ApiMutableModelControllerBase
{
    protected static $internalModelName = 'extra';
    protected static $internalModelClass = 'OPNsense\Extra\Extra';
}
`),
	}
}

func setup(t *testing.T) (*mirror, *Pipeline, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	m := &mirror{files: map[string][]byte{}, key: key}
	srv := httptest.NewServer(m)
	t.Cleanup(srv.Close)
	tmp := t.TempDir()
	p := &Pipeline{
		Client:  pkgrepo.NewClient(srv.URL, filepath.Join(tmp, "cache")),
		Keys:    map[string]pkgrepo.Key{pkgrepo.Fingerprint(m.pub()): {Name: "test-key", Status: "trusted"}},
		Arch:    "amd64",
		Dist:    filepath.Join(tmp, "dist"),
		SrcRoot: filepath.Join(tmp, "src"),
	}
	return m, p, tmp
}

func outcome(t *testing.T, outs []Outcome, release string) Outcome {
	t.Helper()
	for _, o := range outs {
		if o.Release == release {
			return o
		}
	}
	t.Fatalf("no outcome for %s in %+v", release, outs)
	return Outcome{}
}

func fragHash(t *testing.T, p *Pipeline, release, pkg string) string {
	t.Helper()
	c, err := ReadComposition(filepath.Join(p.Dist, release, "composition.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range c.Fragments {
		if f.Package == pkg {
			return f.Hash
		}
	}
	t.Fatalf("%s: no fragment for %s", release, pkg)
	return ""
}

// --- tests ------------------------------------------------------------------

// The versioning model end to end: build, no-op, hotfix under the same
// release number, a new release making the previous one final.
func TestMatrixLifecycle(t *testing.T) {
	m, p, _ := setup(t)
	m.publish(t, "26.7", []pkgSpec{{"opnsense", "26.7", coreFiles(t, false)}, {"os-extra", "1.0", pluginFiles()}})
	m.publish(t, "26.7.1", []pkgSpec{{"opnsense", "26.7.1", coreFiles(t, false)}, {"os-extra", "1.0", pluginFiles()}})

	// 1. first run builds everything
	outs, err := p.Matrix([]string{series}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, outs, "26.7"); o.Action != ActionBuild || o.Status != fragment.Final {
		t.Errorf("26.7: %+v", o)
	}
	o1 := outcome(t, outs, "26.7.1")
	if o1.Action != ActionBuild || o1.Status != fragment.Provisional || !strings.HasPrefix(o1.Composition, "26.7.1~") {
		t.Errorf("26.7.1: %+v", o1)
	}
	coreBefore, pluginBefore := fragHash(t, p, "26.7.1", "opnsense"), fragHash(t, p, "26.7.1", "os-extra")
	if fragHash(t, p, "26.7", "os-extra") != pluginBefore {
		t.Error("identical plugin API must be one fragment across releases")
	}

	// 2. nothing changed: no rebuild
	outs, err = p.Matrix([]string{series}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range outs {
		if o.Action != ActionUpToDate {
			t.Errorf("second run must not rebuild: %+v", o)
		}
	}

	// 3. hotfix under the same release number
	m.publish(t, "26.7.1", []pkgSpec{{"opnsense", "26.7.1_1", coreFiles(t, true)}, {"os-extra", "1.0", pluginFiles()}})
	outs, err = p.Matrix([]string{series}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	o1h := outcome(t, outs, "26.7.1")
	if o1h.Action != ActionBuild || o1h.Reason != "catalog changed" || !strings.HasPrefix(o1h.Composition, "26.7.1_1~") {
		t.Errorf("hotfix must rebuild with a new composition id: %+v", o1h)
	}
	if o := outcome(t, outs, "26.7"); o.Action != ActionUpToDate {
		t.Errorf("26.7 must stay untouched: %+v", o)
	}
	if fragHash(t, p, "26.7.1", "opnsense") == coreBefore {
		t.Error("changed core API must give a new core fragment")
	}
	if fragHash(t, p, "26.7.1", "os-extra") != pluginBefore {
		t.Error("unchanged plugin must keep its fragment")
	}
	comps, _ := filepath.Glob(filepath.Join(p.Dist, "26.7.1", "compositions", "*.json"))
	if len(comps) != 2 {
		t.Errorf("both catalog states must be kept, got %v", comps)
	}
	rel, err := ReadIR(filepath.Join(p.Dist, "26.7.1", "opnsense-26.7.1.ir.json"))
	if err != nil || rel.CoreVersion != "26.7.1_1" {
		t.Errorf("release view must show the hotfix revision: %v %v", err, rel)
	}

	// 4. a new release makes 26.7.1 final without rebuilding it
	m.publish(t, "26.7.2", []pkgSpec{{"opnsense", "26.7.2", coreFiles(t, true)}, {"os-extra", "1.0", pluginFiles()}})
	outs, err = p.Matrix([]string{series}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(t, outs, "26.7.1"); o.Action != ActionStatusOnly || o.Status != fragment.Final {
		t.Errorf("26.7.1 must only change status: %+v", o)
	}
	if o := outcome(t, outs, "26.7.2"); o.Action != ActionBuild || o.Status != fragment.Provisional {
		t.Errorf("26.7.2: %+v", o)
	}
	if fragHash(t, p, "26.7.2", "opnsense") != fragHash(t, p, "26.7.1", "opnsense") {
		t.Error("same core API in 26.7.2 must reuse the fragment")
	}

	// index reflects everything
	var idx Index
	raw, _ := os.ReadFile(filepath.Join(p.Dist, "index.json"))
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Releases) != 3 || len(idx.Releases[1].Compositions) != 2 {
		t.Errorf("index releases: %+v", idx.Releases)
	}
	if u := idx.Fragments["os-extra"][pluginBefore]; u == nil || len(u.Compositions) != 4 {
		t.Errorf("plugin fragment availability: %+v", idx.Fragments["os-extra"])
	}
	if _, err := os.Stat(filepath.Join(p.Dist, "26.7.2", "26.7.1..26.7.2.md")); err != nil {
		t.Errorf("diff to the previous release missing: %v", err)
	}
}

// A cached package that no longer matches the catalog is fetched again.
func TestStaleCachedPackageIsRefetched(t *testing.T) {
	m, p, tmp := setup(t)
	m.publish(t, "26.7", []pkgSpec{{"opnsense", "26.7", coreFiles(t, false)}})
	if _, err := p.Build("26.7", true, ""); err != nil {
		t.Fatal(err)
	}
	cached := filepath.Join(tmp, "cache", abi, "26.7", "opnsense-26.7.pkg")
	if err := os.WriteFile(cached, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Build("26.7", true, ""); err != nil {
		t.Fatalf("stale cache must be refetched: %v", err)
	}
	if data, _ := os.ReadFile(cached); string(data) == "stale" {
		t.Error("cache was not replaced")
	}
}

// A catalog signed with an unknown key stops the run.
func TestUnknownKeyFailsLoudly(t *testing.T) {
	m, p, _ := setup(t)
	m.publish(t, "26.7", []pkgSpec{{"opnsense", "26.7", coreFiles(t, false)}})
	p.Keys = map[string]pkgrepo.Key{}
	if _, err := p.Matrix([]string{series}, false, false); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Errorf("want unknown key error, got %v", err)
	}
}

// Plugin files are attributed to their package; core first.
func TestFetchAttribution(t *testing.T) {
	m, p, _ := setup(t)
	m.publish(t, "26.7", []pkgSpec{{"opnsense", "26.7", coreFiles(t, false)}, {"os-extra", "1.0", pluginFiles()}})
	f, err := p.Fetch("26.7", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Owners.Owner["models/OPNsense/Extra/Extra.xml"] != "os-extra" || f.Owners.Owner["models/OPNsense/Demo/Demo.xml"] != "opnsense" {
		t.Errorf("attribution: %+v", f.Owners.Owner)
	}
	if f.Core != "26.7" || len(f.Catalog) != 64 {
		t.Errorf("metadata: core=%s catalog=%s", f.Core, f.Catalog)
	}
}

func TestDecide(t *testing.T) {
	comp := &fragment.Composition{CatalogSHA256: "a", Generator: "g1", Status: fragment.Provisional}
	for _, c := range []struct {
		name   string
		comp   *fragment.Composition
		haveIR bool
		cat, g string
		st     fragment.Status
		force  bool
		want   Action
	}{
		{"forced", comp, true, "a", "g1", fragment.Provisional, true, ActionBuild},
		{"nothing built", nil, false, "a", "g1", fragment.Provisional, false, ActionBuild},
		{"ir missing", comp, false, "a", "g1", fragment.Provisional, false, ActionBuild},
		{"catalog changed", comp, true, "b", "g1", fragment.Provisional, false, ActionBuild},
		{"generator changed", comp, true, "a", "g2", fragment.Provisional, false, ActionBuild},
		{"status moved", comp, true, "a", "g1", fragment.Final, false, ActionStatusOnly},
		{"unchanged", comp, true, "a", "g1", fragment.Provisional, false, ActionUpToDate},
	} {
		if got, _ := Decide(c.comp, c.haveIR, c.cat, c.g, c.st, c.force); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	_ = ir.GeneratorVersion
}
