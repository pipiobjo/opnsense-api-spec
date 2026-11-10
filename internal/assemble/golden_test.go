package assemble

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/fragment"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/openapi"
)

// go test ./internal/assemble -run Golden -update   rewrites testdata/golden
var (
	update = flag.Bool("update", false, "rewrite golden files")
	// final releases only: their catalogs no longer change
	goldenReleases = flag.String("golden-releases", "25.7.10,26.1.10,26.7.7", "releases written by -update")
)

const srcRoot = "../../.cache/src"

// sources returns the extracted core+plugins trees that carry attribution
// (written by `opnsense-api-spec fetch|build -plugins`).
func sources(t *testing.T) map[string]string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(srcRoot, "*", "core+plugins", OwnersFile))
	out := map[string]string{}
	for _, p := range m {
		dir := filepath.Dir(p)
		out[filepath.Base(filepath.Dir(dir))] = dir
	}
	return out
}

func load(t *testing.T, dir string) *ir.Release {
	t.Helper()
	owners, err := ReadOwners(dir)
	if err != nil {
		t.Fatal(err)
	}
	var meta ir.Release
	raw, err := os.ReadFile(filepath.Join(dir, ".release.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	rel, err := Release(dir, owners, meta)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The same sources and generator must give byte-identical outputs.
func TestDeterminism(t *testing.T) {
	src := sources(t)
	if len(src) == 0 {
		t.Skip("no extracted sources with attribution in .cache/src")
	}
	var names []string
	for n := range src {
		names = append(names, n)
	}
	sort.Strings(names)
	dir := src[names[len(names)-1]]
	a, b := load(t, dir), load(t, dir)
	if !bytes.Equal(encode(t, a), encode(t, b)) {
		t.Error("IR differs between two builds")
	}
	da, _ := openapi.Render(a)
	db, _ := openapi.Render(b)
	if !bytes.Equal(encode(t, da), encode(t, db)) {
		t.Error("OpenAPI document differs between two builds")
	}
	fa, fb := fragment.Split(a), fragment.Split(b)
	for i := range fa {
		if fa[i].Hash() != fb[i].Hash() {
			t.Errorf("fragment %s: hash differs between two builds", fa[i].Package)
		}
	}
}

// Golden fragment hashes of final releases: a generator change that alters
// the derived API of an existing release shows up here, per package.
func TestGolden(t *testing.T) {
	src := sources(t)
	files, _ := filepath.Glob("testdata/golden/*.json")
	if *update {
		for _, rel := range strings.Split(*goldenReleases, ",") {
			dir, ok := src[rel]
			if !ok {
				t.Fatalf("no extracted sources for %s; run a build with -plugins first", rel)
			}
			r := load(t, dir)
			hashes := map[string]string{}
			for _, f := range fragment.Split(r) {
				hashes[f.Package] = f.Hash()
			}
			golden := map[string]any{"release": rel, "generator": ir.GeneratorVersion,
				"catalog_sha256": r.CatalogSHA256, "fragments": hashes}
			if err := os.MkdirAll("testdata/golden", 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join("testdata/golden", rel+".json"), encode(t, golden), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		t.Skip("golden files rewritten")
	}
	if len(files) == 0 {
		t.Skip("no golden files")
	}
	for _, gf := range files {
		rel := strings.TrimSuffix(filepath.Base(gf), ".json")
		t.Run(rel, func(t *testing.T) {
			dir, ok := src[rel]
			if !ok {
				t.Skip("sources for " + rel + " not extracted")
			}
			var golden struct {
				Catalog   string            `json:"catalog_sha256"`
				Fragments map[string]string `json:"fragments"`
			}
			raw, _ := os.ReadFile(gf)
			if err := json.Unmarshal(raw, &golden); err != nil {
				t.Fatal(err)
			}
			r := load(t, dir)
			if r.CatalogSHA256 != golden.Catalog {
				t.Skipf("sources are from another catalog state (%s), golden is %s", r.CatalogSHA256[:12], golden.Catalog[:12])
			}
			got := map[string]string{}
			for _, f := range fragment.Split(r) {
				got[f.Package] = f.Hash()
			}
			for pkg, want := range golden.Fragments {
				if got[pkg] != want {
					t.Errorf("%s: fragment hash %s, golden %s (derived API changed)", pkg, got[pkg], want)
				}
			}
			for pkg := range got {
				if _, ok := golden.Fragments[pkg]; !ok {
					t.Errorf("%s: new fragment not in golden file", pkg)
				}
			}
		})
	}
}
