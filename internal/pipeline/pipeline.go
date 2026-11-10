// Package pipeline orchestrates fetching, building and the release matrix:
// which releases exist, which need a rebuild, where outputs go. It holds the
// rules of the versioning model (docs/concepts.md, section 5) and is tested
// against a fake mirror.
package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/assemble"
	"github.com/pipiobjo/opnsense-api-spec/internal/diff"
	"github.com/pipiobjo/opnsense-api-spec/internal/fragment"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/openapi"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
	"github.com/pipiobjo/opnsense-api-spec/internal/quality"
	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

// Pipeline holds everything the orchestration depends on.
type Pipeline struct {
	Client  *pkgrepo.Client
	Keys    map[string]pkgrepo.Key // known signing keys (pkgrepo.Keys)
	Arch    string                 // "amd64"
	Dist    string                 // output root, e.g. "dist"
	SrcRoot string                 // extracted sources, e.g. ".cache/src"
	Log     io.Writer
}

func (p *Pipeline) logf(format string, a ...any) {
	if p.Log != nil {
		fmt.Fprintf(p.Log, format, a...)
	}
}

const mvcPrefix = "usr/local/opnsense/mvc/app/"

// Fetched is the result of Fetch: verified, extracted sources of one
// release and their attribution to packages.
type Fetched struct {
	Release  pkgrepo.Release
	SignedBy string
	Core     string // core package version incl. hotfix revision
	Catalog  string // sha256 of the signed packagesite.yaml
	Packages []ir.Package
	AppDir   string // merged MVC app dir (core + plugins)
	Owners   *assemble.Owners
}

// Fetch downloads and verifies the catalog and packages of a release and
// unpacks their MVC sources into one app directory, as on a firewall. With
// plugins, all os-* packages except -devel variants are included. An empty
// dir means <SrcRoot>/<release>/{core,core+plugins}.
func (p *Pipeline) Fetch(version string, plugins bool, dir string) (*Fetched, error) {
	r, err := p.Client.FindRelease(p.Arch, version)
	if err != nil {
		return nil, err
	}
	cat, err := p.Client.FetchCatalog(r)
	if err != nil {
		return nil, err
	}
	k, err := pkgrepo.VerifyCatalog(cat, p.Keys, true)
	if err != nil {
		return nil, err
	}
	key := k.Name
	if k.Status != "trusted" {
		key += " (rotated key of an older series)"
	}
	p.logf("catalog %s: %d packages, signature ok (%s)\n", r.Version, len(cat.Packages), key)
	core, ok := cat.Lookup("opnsense")
	if !ok {
		return nil, fmt.Errorf("no core package in catalog")
	}
	pkgs := []pkgrepo.Package{core}
	if plugins {
		for _, pk := range cat.Plugins() {
			if !strings.HasSuffix(pk.Name, "-devel") {
				pkgs = append(pkgs, pk)
			}
		}
	}
	if dir == "" {
		dir = filepath.Join(p.SrcRoot, r.Version, map[bool]string{true: "core+plugins", false: "core"}[plugins])
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	catSum := sha256.Sum256(cat.YAML)
	f := &Fetched{Release: r, SignedBy: key, AppDir: dir, Core: core.Version, Catalog: fmt.Sprintf("%x", catSum),
		Owners: &assemble.Owners{Owner: map[string]string{}, AlsoIn: map[string][]string{}}}
	digest := map[string][32]byte{}
	for _, pk := range pkgs {
		data, err := p.Client.FetchPackage(r, pk)
		if err != nil {
			return nil, err
		}
		files, err := pkgrepo.ReadArchive(bytes.NewReader(data), func(n string) bool {
			return strings.HasPrefix(n, mvcPrefix) && (strings.HasSuffix(n, ".xml") || strings.HasSuffix(n, ".php"))
		})
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pk.Name, err)
		}
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			content := files[name]
			rel := strings.TrimPrefix(name, mvcPrefix)
			sum := sha256.Sum256(content)
			if prev, ok := f.Owners.Owner[rel]; ok {
				if digest[rel] == sum {
					f.Owners.AlsoIn[rel] = append(f.Owners.AlsoIn[rel], pk.Name) // same API, alternative package
				} else {
					f.Owners.Conflicts = append(f.Owners.Conflicts, rel+": "+prev+" and "+pk.Name)
				}
				continue // first package wins (core is fetched first)
			}
			f.Owners.Owner[rel] = pk.Name
			digest[rel] = sum
			dst := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return nil, err
			}
			if err := os.WriteFile(dst, content, 0o644); err != nil {
				return nil, err
			}
		}
		f.Packages = append(f.Packages, ir.Package{Name: pk.Name, Version: pk.Version, Commit: pk.Annotations["product_hash"], Sum: pk.Sum})
		p.logf("  %-32s %-12s %4d files, checksum ok\n", pk.Name, pk.Version, len(files))
	}
	sort.Strings(f.Owners.Conflicts)
	// the attribution travels with the sources (golden/determinism tests)
	if err := assemble.WriteOwners(dir, f.Owners); err != nil {
		return nil, err
	}
	if err := WriteJSON(filepath.Join(dir, ".release.json"), f.meta()); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *Fetched) meta() ir.Release {
	return ir.Release{Version: f.Release.Version, Series: f.Release.Series, ABI: f.Release.ABI,
		CoreVersion: f.Core, CatalogSHA256: f.Catalog, SignedBy: f.SignedBy, Packages: f.Packages}
}

// Build fetches a release and writes the release view (IR, OpenAPI,
// coverage) to out (default <Dist>/<release>). With plugins it also writes
// fragments, the composition and keeps earlier contract test results. It
// returns the path of the IR file.
func (p *Pipeline) Build(version string, plugins bool, out string) (string, error) {
	f, err := p.Fetch(version, plugins, "")
	if err != nil {
		return "", err
	}
	rel, err := assemble.Release(f.AppDir, f.Owners, f.meta())
	if err != nil {
		return "", err
	}
	if out == "" {
		out = filepath.Join(p.Dist, f.Release.Version)
	}
	var ver *verify.Report
	if plugins {
		if raw, err := os.ReadFile(filepath.Join(out, "opnsense-"+f.Release.Version+".verified.json")); err == nil {
			ver = &verify.Report{}
			if err := json.Unmarshal(raw, ver); err != nil {
				return "", err
			}
			p.logf("including contract test results from %s (instance %s)\n", ver.Date, ver.Instance)
		}
	}
	doc, cov := openapi.RenderVerified(rel, ver)
	refs, defined := openapi.Refs(doc)
	if v := quality.Invariants(rel, refs, defined); len(v) > 0 {
		return "", fmt.Errorf("%s: %d invariant violation(s), not writing outputs:\n  %s", version, len(v), strings.Join(v, "\n  "))
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return "", err
	}
	name := "opnsense-" + f.Release.Version
	if !plugins {
		name += "-core"
	}
	irPath := filepath.Join(out, name+".ir.json")
	if err := WriteJSON(irPath, rel); err != nil {
		return "", err
	}
	if err := WriteJSON(filepath.Join(out, name+".openapi.json"), doc); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(out, name+".coverage.md"), []byte(CoverageMD(rel, cov)), 0o644); err != nil {
		return "", err
	}
	if plugins {
		// fragments and composition only for complete catalog states
		all, err := p.Client.Discover(p.Arch)
		if err != nil {
			return "", err
		}
		comp, err := writeFragments(rel, filepath.Dir(out), out, statusOf(all, f.Release))
		if err != nil {
			return "", err
		}
		p.logf("composition %s (%s): %d fragments\n", comp.ID(), comp.Status, len(comp.Fragments))
	}
	p.logf("models=%d fields=%d operations=%d (base=%d base-call=%d signature-only=%d) with-gaps=%d -> %s\n",
		cov.Models, cov.Fields, cov.Operations, cov.BySource["base"], cov.BySource["base-call"],
		cov.BySource["signature-only"], cov.WithGaps, out)
	return irPath, nil
}

// Action is what the matrix does with a release.
type Action string

const (
	ActionBuild      Action = "build"
	ActionStatusOnly Action = "status"
	ActionUpToDate   Action = "up-to-date"
)

// Decide is the rebuild rule: build when forced, when nothing usable
// exists, when the catalog changed (hotfix revision, plugin update) or when
// the generator changed; otherwise only the status may move.
func Decide(comp *fragment.Composition, haveIR bool, catalogSHA, generator string, status fragment.Status, force bool) (Action, string) {
	switch {
	case force:
		return ActionBuild, "forced"
	case comp == nil || !haveIR:
		return ActionBuild, "no previous build"
	case comp.CatalogSHA256 != catalogSHA:
		return ActionBuild, "catalog changed"
	case comp.Generator != generator:
		return ActionBuild, "generator " + comp.Generator + " -> " + generator
	case comp.Status != status:
		return ActionStatusOnly, string(comp.Status) + " -> " + string(status)
	default:
		return ActionUpToDate, ""
	}
}

// Outcome reports what Matrix did with one release.
type Outcome struct {
	Release     string
	Action      Action
	Reason      string
	Composition string // id after the run
	Status      fragment.Status
	// Quality compares with the previous release of the run (nil for the
	// first one): regressions and framework changes. It explains contract
	// test failures; it is not a gate (framework files change in about two
	// of three patch releases, measured 25.7 to 26.7.8).
	Quality *quality.Report
}

// Matrix builds every release of the given series (all if latestOnly is
// false, else the newest per series), writes diffs between consecutive
// releases and the index.
func (p *Pipeline) Matrix(series []string, latestOnly, force bool) ([]Outcome, error) {
	all, err := p.Client.Discover(p.Arch)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, s := range series {
		want[strings.TrimSpace(s)] = true
	}
	var rels []pkgrepo.Release
	for _, r := range all {
		if want[r.Series] {
			rels = append(rels, r)
		}
	}
	if latestOnly {
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
		return nil, fmt.Errorf("no releases for series %s", strings.Join(series, ","))
	}
	var out []Outcome
	var prev *ir.Release
	for _, r := range rels {
		dir := filepath.Join(p.Dist, r.Version)
		irPath := filepath.Join(dir, "opnsense-"+r.Version+".ir.json")
		catSum, _, err := p.currentCatalog(r)
		if err != nil {
			return out, fmt.Errorf("%s: %w", r.Version, err)
		}
		comp, cerr := ReadComposition(filepath.Join(dir, "composition.json"))
		if cerr != nil {
			comp = nil
		}
		cur, ierr := ReadIR(irPath)
		st := statusOf(all, r)
		action, reason := Decide(comp, ierr == nil, catSum, ir.GeneratorVersion, st, force)
		switch action {
		case ActionBuild:
			p.logf("%s: %s, building\n", r.Version, reason)
			if irPath, err = p.Build(r.Version, true, dir); err != nil {
				return out, fmt.Errorf("%s: %w", r.Version, err)
			}
			if cur, err = ReadIR(irPath); err != nil {
				return out, err
			}
			if comp, err = ReadComposition(filepath.Join(dir, "composition.json")); err != nil {
				return out, err
			}
		case ActionStatusOnly:
			comp.Status = st
			if err := WriteJSON(filepath.Join(dir, "composition.json"), comp); err != nil {
				return out, err
			}
			if err := WriteJSON(filepath.Join(dir, "compositions", comp.ID()+".json"), comp); err != nil {
				return out, err
			}
			p.logf("%s: status %s\n", r.Version, reason)
		default:
			p.logf("%s: up to date (%s)\n", r.Version, comp.ID())
		}
		oc := Outcome{Release: r.Version, Action: action, Reason: reason, Composition: comp.ID(), Status: comp.Status}
		if prev != nil {
			if _, err := WriteDiff(prev, cur, dir); err != nil {
				return out, err
			}
			oc.Quality = quality.Compare(prev, cur)
			if err := WriteJSON(filepath.Join(dir, prev.Version+".."+cur.Version+".quality.json"), oc.Quality); err != nil {
				return out, err
			}
		}
		prev = cur
		out = append(out, oc)
	}
	return out, BuildIndex(p.Dist)
}

// ReadIR loads an IR file.
func ReadIR(path string) (*ir.Release, error) {
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

// WriteDiff writes <a>..<b>.{json,md} into dir.
func WriteDiff(a, b *ir.Release, dir string) (*diff.Report, error) {
	r := diff.Compare(a, b)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	base := filepath.Join(dir, a.Version+".."+b.Version)
	if err := WriteJSON(base+".json", r); err != nil {
		return nil, err
	}
	return r, os.WriteFile(base+".md", []byte(r.Markdown()), 0o644)
}

// WriteJSON writes indented JSON without HTML escaping.
func WriteJSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// CoverageMD renders the human-readable coverage report.
func CoverageMD(rel *ir.Release, cov openapi.Coverage) string {
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
	for _, s := range SortedKeys(cov.BySource) {
		fmt.Fprintf(&b, "| %s | %d | %s |\n", s, cov.BySource[s], meaning[s])
	}
	b.WriteString("\n## Gaps by reason\n\n| reason | operations |\n|---|---|\n")
	for _, s := range SortedKeys(cov.GapsByReason) {
		fmt.Fprintf(&b, "| %s | %d |\n", s, cov.GapsByReason[s])
	}
	type agg struct{ ops, typed, gaps int }
	count := func(key func(*ir.Endpoint) string) map[string]*agg {
		m := map[string]*agg{}
		for _, e := range rel.Endpoints {
			a := m[key(e)]
			if a == nil {
				a = &agg{}
				m[key(e)] = a
			}
			a.ops++
			if e.Source != ir.SourceSignature {
				a.typed++
			}
			if len(e.Gaps) > 0 {
				a.gaps++
			}
		}
		return m
	}
	mods := count(func(e *ir.Endpoint) string { return e.Module })
	b.WriteString("\n## Per module\n\n| module | operations | typed | with gaps |\n|---|---|---|---|\n")
	for _, m := range SortedKeys(mods) {
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", m, mods[m].ops, mods[m].typed, mods[m].gaps)
	}
	pk := count(func(e *ir.Endpoint) string { return e.Package })
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

// SortedKeys returns the keys of m in order.
func SortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
