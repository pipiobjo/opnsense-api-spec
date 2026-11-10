// Command opnsense-api-spec builds a versioned API definition for an
// OPNsense release from the official package repository.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/assemble"
	"github.com/pipiobjo/opnsense-api-spec/internal/diff"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/openapi"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
	"github.com/pipiobjo/opnsense-api-spec/internal/schemacheck"
	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: opnsense-api-spec <command> [flags]

commands:
  releases                 list release snapshots on the mirror
  fetch  -release X        download, verify and unpack MVC sources
  build  -release X        fetch + build IR, OpenAPI and coverage into -out
  matrix -series A,B       build every release of the series (with plugins),
                           diffs between consecutive releases, dist/index.json
  diff   -from F -to T     compare two IR files (*.ir.json)
  proxy  -release X [-listen A]
                           serve the package snapshot of release X to an
                           isolated test VM (default listen 127.0.0.1:18080)
  verify -ir F -url U -keyfile K [-write] [-packages a,b|installed]
                           contract tests against a THROWAWAY test instance
                           (only https://127.0.0.1:<port> or localhost);
                           writes *.verified.json and re-renders the OpenAPI

flags:
  -mirror URL   package mirror (default https://pkg.opnsense.org)
  -arch A       architecture (default amd64)
  -cache DIR    download cache (default .cache/pkg)
  -out DIR      output directory (fetch: .cache/src/X, build: dist/X)
  -plugins      include all os-* plugins (default: core only)
  -latest       matrix: only the newest release of each series
  -force        matrix: rebuild releases that already exist in dist/`)
	os.Exit(2)
}

type opts struct {
	base, arch, cache, release, out string
	series, from, to                string
	irFile, url, keyfile, packages  string
	plugins, latest, force, write   bool
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd, args := os.Args[1], os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	var o opts
	fs.StringVar(&o.base, "mirror", pkgrepo.DefaultBaseURL, "")
	fs.StringVar(&o.arch, "arch", "amd64", "")
	fs.StringVar(&o.cache, "cache", ".cache/pkg", "")
	fs.StringVar(&o.release, "release", "", "")
	fs.StringVar(&o.out, "out", "", "")
	fs.BoolVar(&o.plugins, "plugins", false, "")
	fs.StringVar(&o.series, "series", "", "")
	fs.StringVar(&o.from, "from", "", "")
	fs.StringVar(&o.to, "to", "", "")
	fs.BoolVar(&o.latest, "latest", false, "")
	fs.BoolVar(&o.force, "force", false, "")
	fs.StringVar(&o.irFile, "ir", "", "")
	fs.StringVar(&o.url, "url", "", "")
	fs.StringVar(&o.keyfile, "keyfile", "", "")
	fs.StringVar(&o.packages, "packages", "opnsense", "")
	fs.BoolVar(&o.write, "write", false, "")
	offline := fs.Bool("offline", false, "")
	listen := fs.String("listen", "127.0.0.1:18080", "")
	fs.Usage = usage
	_ = fs.Parse(args)
	c := pkgrepo.NewClient(o.base, o.cache)
	c.Offline = *offline

	var err error
	switch cmd {
	case "releases":
		err = cmdReleases(c, o.arch)
	case "fetch":
		if o.release == "" {
			usage()
		}
		_, err = fetch(c, o)
	case "build":
		if o.release == "" {
			usage()
		}
		_, err = build(c, o)
	case "matrix":
		if o.series == "" {
			usage()
		}
		err = matrix(c, o)
	case "diff":
		if o.from == "" || o.to == "" {
			usage()
		}
		err = cmdDiff(o)
	case "verify":
		if o.irFile == "" || o.url == "" || o.keyfile == "" {
			usage()
		}
		err = cmdVerify(o)
	case "proxy":
		if o.release == "" {
			usage()
		}
		err = proxy(c, o, *listen)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdReleases(c *pkgrepo.Client, arch string) error {
	rs, err := c.Discover(arch)
	if err != nil {
		return err
	}
	for _, r := range rs {
		fmt.Printf("%-10s %-6s %s\n", r.Version, r.Series, r.ABI)
	}
	return nil
}

const mvcPrefix = "usr/local/opnsense/mvc/app/"

type fetched struct {
	release  pkgrepo.Release
	signedBy string
	core     string // core package version incl. hotfix revision
	catalog  string // sha256 of the signed packagesite.yaml
	packages []ir.Package
	appDir   string              // merged MVC app dir (core + plugins)
	owner    map[string]string   // file relative to appDir -> package name
	alsoIn   map[string][]string // identical file also shipped by these packages
	digest   map[string][32]byte // file -> sha256 of its content
	conflict []string            // files shipped with different content by several packages
}

// fetch downloads and verifies the packages and unpacks their MVC sources
// into one merged app directory, as they are laid out on a firewall.
func fetch(c *pkgrepo.Client, o opts) (*fetched, error) {
	r, err := c.FindRelease(o.arch, o.release)
	if err != nil {
		return nil, err
	}
	cat, err := c.FetchCatalog(r)
	if err != nil {
		return nil, err
	}
	k, err := pkgrepo.VerifyCatalog(cat, pkgrepo.Keys, true)
	if err != nil {
		return nil, err
	}
	key := k.Name
	if k.Status != "trusted" {
		key += " (rotated key of an older series)"
	}
	fmt.Fprintf(os.Stderr, "catalog %s: %d packages, signature ok (%s)\n", r.Version, len(cat.Packages), key)
	core, ok := cat.Lookup("opnsense")
	if !ok {
		return nil, fmt.Errorf("no core package in catalog")
	}
	pkgs := []pkgrepo.Package{core}
	if o.plugins {
		for _, p := range cat.Plugins() {
			if !strings.HasSuffix(p.Name, "-devel") {
				pkgs = append(pkgs, p)
			}
		}
	}
	dir := filepath.Join(".cache", "src", r.Version, map[bool]string{true: "core+plugins", false: "core"}[o.plugins])
	if o.out != "" && len(os.Args) > 1 && os.Args[1] == "fetch" {
		dir = o.out
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	catSum := sha256.Sum256(cat.YAML)
	f := &fetched{release: r, signedBy: key, appDir: dir, owner: map[string]string{},
		core: core.Version, catalog: fmt.Sprintf("%x", catSum),
		alsoIn: map[string][]string{}, digest: map[string][32]byte{}}
	for _, p := range pkgs {
		data, err := c.FetchPackage(r, p)
		if err != nil {
			return nil, err
		}
		files, err := pkgrepo.ReadArchive(bytes.NewReader(data), func(n string) bool {
			return strings.HasPrefix(n, mvcPrefix) && (strings.HasSuffix(n, ".xml") || strings.HasSuffix(n, ".php"))
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Name, err)
		}
		for name, content := range files {
			rel := strings.TrimPrefix(name, mvcPrefix)
			sum := sha256.Sum256(content)
			if prev, ok := f.owner[rel]; ok {
				if f.digest[rel] == sum {
					f.alsoIn[rel] = append(f.alsoIn[rel], p.Name) // same API, alternative package
				} else {
					f.conflict = append(f.conflict, rel+": "+prev+" and "+p.Name)
				}
				continue // first package wins (core is fetched first)
			}
			f.owner[rel] = p.Name
			f.digest[rel] = sum
			dst := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(dst, content, 0o644); err != nil {
				return nil, err
			}
		}
		commit := p.Annotations["product_hash"]
		f.packages = append(f.packages, ir.Package{Name: p.Name, Version: p.Version, Commit: commit, Sum: p.Sum})
		fmt.Fprintf(os.Stderr, "  %-32s %-12s %4d files, checksum ok\n", p.Name, p.Version, len(files))
	}
	// the attribution travels with the sources (golden/determinism tests)
	if err := assemble.WriteOwners(dir, &assemble.Owners{Owner: f.owner, AlsoIn: f.alsoIn, Conflicts: f.conflict}); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(dir, ".release.json"), ir.Release{
		Version: r.Version, Series: r.Series, ABI: r.ABI, CoreVersion: f.core,
		CatalogSHA256: f.catalog, SignedBy: key, Packages: f.packages,
	}); err != nil {
		return nil, err
	}
	return f, nil
}

// build returns the path of the written IR file.
func build(c *pkgrepo.Client, o opts) (string, error) {
	f, err := fetch(c, o)
	if err != nil {
		return "", err
	}
	owners := &assemble.Owners{Owner: f.owner, AlsoIn: f.alsoIn, Conflicts: f.conflict}
	rel, err := assemble.Release(f.appDir, owners, ir.Release{
		Version: f.release.Version, Series: f.release.Series, ABI: f.release.ABI,
		CoreVersion: f.core, CatalogSHA256: f.catalog, SignedBy: f.signedBy, Packages: f.packages,
	})
	if err != nil {
		return "", err
	}

	out := o.out
	if out == "" {
		out = filepath.Join("dist", f.release.Version)
	}
	// keep results of an earlier contract test run (verify) in the output
	var ver *verify.Report
	if o.plugins {
		if raw, err := os.ReadFile(filepath.Join(out, "opnsense-"+f.release.Version+".verified.json")); err == nil {
			ver = &verify.Report{}
			if err := json.Unmarshal(raw, ver); err != nil {
				return "", err
			}
			fmt.Fprintf(os.Stderr, "including contract test results from %s (instance %s)\n", ver.Date, ver.Instance)
		}
	}
	doc, cov := openapi.RenderVerified(rel, ver)
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	name := "opnsense-" + f.release.Version
	if !o.plugins {
		name += "-core"
	}
	irPath := filepath.Join(out, name+".ir.json")
	if err := writeJSON(irPath, rel); err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(out, name+".openapi.json"), doc); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(out, name+".coverage.md"), []byte(coverageMD(rel, cov)), 0o644); err != nil {
		return "", err
	}
	if o.plugins {
		// fragments and composition only for complete catalog states
		all, err := c.Discover(o.arch)
		if err != nil {
			return "", err
		}
		comp, err := writeFragments(rel, filepath.Dir(out), out, statusOf(all, f.release))
		if err != nil {
			return "", err
		}
		fmt.Fprintf(os.Stderr, "composition %s (%s): %d fragments\n", comp.ID(), comp.Status, len(comp.Fragments))
	}
	fmt.Fprintf(os.Stderr, "models=%d fields=%d operations=%d (base=%d base-call=%d signature-only=%d) with-gaps=%d -> %s\n",
		cov.Models, cov.Fields, cov.Operations, cov.BySource["base"], cov.BySource["base-call"],
		cov.BySource["signature-only"], cov.WithGaps, out)
	return irPath, nil
}

func readIR(path string) (*ir.Release, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rel ir.Release
	if err := json.Unmarshal(data, &rel); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &rel, nil
}

func writeDiff(a, b *ir.Release, dir string) (*diff.Report, error) {
	r := diff.Compare(a, b)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	base := filepath.Join(dir, a.Version+".."+b.Version)
	if err := writeJSON(base+".json", r); err != nil {
		return nil, err
	}
	return r, os.WriteFile(base+".md", []byte(r.Markdown()), 0o644)
}

func cmdVerify(o opts) error {
	rel, err := readIR(o.irFile)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(o.keyfile)
	if err != nil {
		return err
	}
	var key struct{ Key, Secret string }
	if err := json.Unmarshal(raw, &key); err != nil || key.Key == "" {
		return fmt.Errorf("%s: expected {\"key\": ..., \"secret\": ...}", o.keyfile)
	}
	c, err := verify.NewClient(o.url, key.Key, key.Secret, []string{"127.0.0.1", "localhost"})
	if err != nil {
		return err
	}
	// validate responses against the document generated clients use
	plain, _ := openapi.Render(rel)
	if c.Schema, err = schemacheck.New(plain); err != nil {
		return fmt.Errorf("schema checker: %w", err)
	}
	pk := map[string]bool{}
	if o.packages == "installed" {
		if pk, err = c.InstalledPackages(); err != nil {
			return err
		}
	} else {
		for _, p := range strings.Split(o.packages, ",") {
			pk[strings.TrimSpace(p)] = true
		}
	}
	fmt.Fprintf(os.Stderr, "testing endpoints of %d installed/selected packages\n", len(pk))
	rep := verify.Run(c, rel, pk, o.write)
	if !strings.HasPrefix(rep.Instance, rel.Version) {
		fmt.Fprintf(os.Stderr, "warning: instance runs %q, IR is %s\n", rep.Instance, rel.Version)
	}
	base := strings.TrimSuffix(o.irFile, ".ir.json")
	if err := writeJSON(base+".verified.json", rep); err != nil {
		return err
	}
	counts := map[string]int{}
	for _, r := range rep.Results {
		counts[r.Check+"/"+r.Status]++
	}
	for _, k := range sortedKeys(counts) {
		fmt.Fprintf(os.Stderr, "%-24s %d\n", k, counts[k])
	}
	doc, _ := openapi.RenderVerified(rel, rep)
	return writeJSON(base+".openapi.json", doc)
}

func cmdDiff(o opts) error {
	a, err := readIR(o.from)
	if err != nil {
		return err
	}
	b, err := readIR(o.to)
	if err != nil {
		return err
	}
	dir := o.out
	if dir == "" {
		dir = filepath.Join("dist", "diffs")
	}
	r, err := writeDiff(a, b, dir)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s -> %s: %d package, %d model, %d field, %d endpoint changes\n",
		a.Version, b.Version, len(r.Packages), len(r.Models), len(r.Fields), len(r.Endpoints))
	return nil
}

func matrix(c *pkgrepo.Client, o opts) error {
	all, err := c.Discover(o.arch)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, s := range strings.Split(o.series, ",") {
		want[strings.TrimSpace(s)] = true
	}
	var rels []pkgrepo.Release
	for _, r := range all {
		if want[r.Series] {
			rels = append(rels, r)
		}
	}
	if o.latest {
		newest := map[string]pkgrepo.Release{}
		for _, r := range rels {
			newest[r.Series] = r // Discover returns ascending order
		}
		rels = rels[:0]
		for _, r := range newest {
			rels = append(rels, r)
		}
		sort.Slice(rels, func(i, j int) bool { return pkgrepo.CompareVersions(rels[i].Version, rels[j].Version) < 0 })
	}
	if len(rels) == 0 {
		return fmt.Errorf("no releases for series %s", o.series)
	}
	var prev *ir.Release
	for _, r := range rels {
		ro := o
		ro.release, ro.plugins, ro.out = r.Version, true, filepath.Join("dist", r.Version)
		irPath := filepath.Join(ro.out, "opnsense-"+r.Version+".ir.json")
		catSum, coreV, err := currentCatalog(c, r)
		if err != nil {
			return fmt.Errorf("%s: %w", r.Version, err)
		}
		comp, cerr := readComposition(filepath.Join(ro.out, "composition.json"))
		cur, ierr := readIR(irPath)
		switch {
		case o.force || cerr != nil || ierr != nil:
		case comp.CatalogSHA256 != catSum:
			fmt.Fprintf(os.Stderr, "%s: catalog changed (%s -> %s), rebuilding\n", r.Version, comp.CoreVersion, coreV)
		case comp.Generator != ir.GeneratorVersion:
			fmt.Fprintf(os.Stderr, "%s: generator %s -> %s, rebuilding\n", r.Version, comp.Generator, ir.GeneratorVersion)
		default:
			// unchanged input: only the status can move (provisional -> final)
			if st := statusOf(all, r); st != comp.Status {
				comp.Status = st
				if err := writeJSON(filepath.Join(ro.out, "composition.json"), comp); err != nil {
					return err
				}
				if err := writeJSON(filepath.Join(ro.out, "compositions", comp.ID()+".json"), comp); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "%s: status -> %s\n", r.Version, st)
			}
			fmt.Fprintf(os.Stderr, "%s: up to date (%s)\n", r.Version, comp.ID())
			if prev != nil {
				if _, err := writeDiff(prev, cur, ro.out); err != nil {
					return err
				}
			}
			prev = cur
			continue
		}
		if irPath, err = build(c, ro); err != nil {
			return fmt.Errorf("%s: %w", r.Version, err)
		}
		if cur, err = readIR(irPath); err != nil {
			return err
		}
		if prev != nil {
			if _, err := writeDiff(prev, cur, ro.out); err != nil {
				return err
			}
		}
		prev = cur
	}
	return buildIndex("dist")
}

func writeJSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func coverageMD(rel *ir.Release, cov openapi.Coverage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Coverage: OPNsense %s\n\n", rel.Version)
	fmt.Fprintf(&b, "Generator %s %s. Catalog signed by `%s`.\n\n", rel.Generator.Name, rel.Generator.Version, rel.SignedBy)
	fmt.Fprintf(&b, "| | count |\n|---|---|\n| models | %d |\n| fields | %d |\n| fields with gaps | %d |\n| operations | %d |\n| operations with gaps | %d |\n\n",
		cov.Models, cov.Fields, cov.FieldGaps, cov.Operations, cov.WithGaps)
	b.WriteString("## Operations by derivation source\n\n| source | count | meaning |\n|---|---|---|\n")
	meaning := map[string]string{
		"base":           "generic base-class action applied to the bound model (typed)",
		"base-call":      "thin wrapper around a generic model helper (typed)",
		"signature-only": "only path and parameters known (untyped)",
	}
	for _, s := range sortedKeys(cov.BySource) {
		fmt.Fprintf(&b, "| %s | %d | %s |\n", s, cov.BySource[s], meaning[s])
	}
	b.WriteString("\n## Gaps by reason\n\n| reason | operations |\n|---|---|\n")
	for _, s := range sortedKeys(cov.GapsByReason) {
		fmt.Fprintf(&b, "| %s | %d |\n", s, cov.GapsByReason[s])
	}
	b.WriteString("\n## Per module\n\n| module | operations | typed | with gaps |\n|---|---|---|---|\n")
	type agg struct{ ops, typed, gaps int }
	mods := map[string]*agg{}
	for _, e := range rel.Endpoints {
		a := mods[e.Module]
		if a == nil {
			a = &agg{}
			mods[e.Module] = a
		}
		a.ops++
		if e.Source != ir.SourceSignature {
			a.typed++
		}
		if len(e.Gaps) > 0 {
			a.gaps++
		}
	}
	for _, m := range sortedKeys(mods) {
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", m, mods[m].ops, mods[m].typed, mods[m].gaps)
	}
	pk := map[string]*agg{}
	for _, e := range rel.Endpoints {
		a := pk[e.Package]
		if a == nil {
			a = &agg{}
			pk[e.Package] = a
		}
		a.ops++
		if e.Source != ir.SourceSignature {
			a.typed++
		}
		if len(e.Gaps) > 0 {
			a.gaps++
		}
	}
	b.WriteString("\n## Packages\n\n| package | version | commit | operations | typed | with gaps |\n|---|---|---|---|---|---|\n")
	for _, p := range rel.Packages {
		a := pk[p.Name]
		if a == nil {
			a = &agg{}
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d |\n", p.Name, p.Version, p.Commit, a.ops, a.typed, a.gaps)
	}
	if len(rel.Conflicts) > 0 {
		b.WriteString("\n## File conflicts between packages\n\nThe first package wins (core first, then plugins by name).\n\n")
		for _, c := range rel.Conflicts {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
