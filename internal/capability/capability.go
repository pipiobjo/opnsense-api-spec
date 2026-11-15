// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package capability builds the capability data: one language-neutral
// document per package that describes every resource (settings and entry
// lists), its fields and actions over all known versions, with
// availability by fragment and the verification results. Clients and
// providers read this instead of the per-release OpenAPI documents.
// The format is described in docs/capabilities.md and
// schema/capability.schema.json.
package capability

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/fragment"
	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

// Format is the major version of the document format. Additions (new
// keys) do not change it; consumers ignore keys they do not know.
const Format = 1

// All is the shorthand for "in every fragment of the enclosing resource".
const All = "*"

// Catalog is catalog.json: which core versions exist and which framework
// each one has. Together with Doc.Select it maps what a firewall reports
// (core version, installed package versions) to fragments.
type Catalog struct {
	Format    int                  `json:"format"`
	Generator string               `json:"generator"`
	Cores     map[string]*Core     `json:"cores"`    // core version (26.7.9_1) -> framework
	Releases  []*Release           `json:"releases"` // in version order
	Packages  map[string]*PkgEntry `json:"packages"` // package -> document file
}

// Core is one core package version.
type Core struct {
	Release   string `json:"release"`
	Series    string `json:"series"`
	Framework string `json:"framework"`
}

// Release is one OPNsense release with its catalog states.
type Release struct {
	Release      string         `json:"release"`
	Series       string         `json:"series"`
	Status       string         `json:"status"` // provisional, final
	Current      string         `json:"current"`
	Compositions []*Composition `json:"compositions"`
}

// Composition is one catalog state: package -> fragment.
type Composition struct {
	ID        string            `json:"id"`
	Core      string            `json:"core"`
	Framework string            `json:"framework"`
	Generator string            `json:"generator"` // generator the fragments were built with
	Fragments map[string]string `json:"fragments"`
	Verified  *Verification     `json:"verified,omitempty"`
}

// Verification summarises a contract test run of a composition.
type Verification struct {
	Date      string         `json:"date"`
	Generator string         `json:"generator"`
	Instance  string         `json:"instance"`
	Packages  []string       `json:"packages"` // installed during the run
	Results   map[string]int `json:"results"`  // check/status -> count
}

// PkgEntry points to a package document.
type PkgEntry struct {
	File      string   `json:"file"`
	Fragments []string `json:"fragments"`
}

// Doc is the capability document of one package.
type Doc struct {
	Format    int                  `json:"format"`
	Package   string               `json:"package"`
	Generator string               `json:"generator"`
	Fragments map[string]*Fragment `json:"fragments"`
	// Select maps framework -> package version -> fragment.
	Select    map[string]map[string]string `json:"select"`
	Resources map[string]*Resource         `json:"resources"`
	Actions   map[string][]*Action         `json:"actions,omitempty"`

	rawRes map[string]*rawResource
	rawAct map[string]*rawAction
}

// Fragment is one API state of the package.
type Fragment struct {
	Versions []string `json:"versions"`
	Cores    []string `json:"cores"`    // core versions it was seen with
	Releases []string `json:"releases"` // OPNsense releases it was seen in
	Order    int      `json:"order"`    // 0 = oldest first appearance
}

// Resource is a settings model or an entry list.
type Resource struct {
	Kind   string                `json:"kind"` // settings, entries
	In     []string              `json:"in"`
	API    []*API                `json:"api"`
	Fields map[string][]*Field   `json:"fields"`
	Verify map[string]*ResVerify `json:"verified,omitempty"` // fragment -> result
	// SharedWith lists other resources of this package that address the
	// same model data (same model class and model path in some fragment).
	SharedWith []string `json:"shared_with,omitempty"`
}

// API is how the resource is addressed in some fragments.
type API struct {
	In         any                   `json:"in"`
	Model      string                `json:"model"`
	ModelPath  string                `json:"model_path,omitempty"`
	Wrapper    string                `json:"wrapper,omitempty"`
	Apply      string                `json:"apply,omitempty"`
	Operations map[string]*Operation `json:"operations"`
}

// Operation is one endpoint of a resource.
type Operation struct {
	Path    string   `json:"path"`
	Methods []string `json:"methods"`
	Params  []string `json:"params,omitempty"`
	// Wrapper is the key around the object in request and response bodies;
	// Outer an additional key around the wrapper in the response (rare).
	Wrapper string `json:"wrapper,omitempty"`
	Outer   string `json:"outer,omitempty"`
	// Forced: fields this endpoint sets to a fixed value; Computed: fields
	// it fills itself. A value sent for them is overwritten.
	Forced      map[string]string `json:"forced,omitempty"`
	Computed    []string          `json:"computed,omitempty"`
	Transformed []string          `json:"transformed,omitempty"` // writable, stored in another form
	Source      string            `json:"source"`
	Gaps        []string          `json:"gaps,omitempty"`
}

// Field is one variant of a field.
type Field struct {
	In        any               `json:"in"`
	Type      string            `json:"type"`
	Base      string            `json:"base,omitempty"`
	ReadShape string            `json:"read_shape,omitempty"`
	Multiple  bool              `json:"multiple,omitempty"`
	Required  bool              `json:"required,omitempty"`
	Volatile  bool              `json:"volatile,omitempty"`
	Default   *string           `json:"default,omitempty"`
	Options   []model.Option    `json:"options,omitempty"`
	Relations []model.Relation  `json:"relations,omitempty"`
	Props     map[string]string `json:"constraints,omitempty"`
	Gaps      []string          `json:"gaps,omitempty"`
}

// Action is an endpoint that is not a resource operation (service
// actions, custom actions), in some fragments.
type Action struct {
	In      []string `json:"in"`
	Kind    string   `json:"kind"`
	Methods []string `json:"methods"`
	Params  []string `json:"params,omitempty"`
	Model   string   `json:"model,omitempty"`
	Source  string   `json:"source"`
	Gaps    []string `json:"gaps,omitempty"`
}

// ResVerify is the contract test result of a resource in one fragment.
type ResVerify struct {
	Release     string `json:"release"`
	Composition string `json:"composition"`
	Generator   string `json:"generator"`
	Date        string `json:"date"`
	// Read: worst status of the read checks (shape, schema) of the get and
	// search operations; absent when none ran.
	Read string `json:"read,omitempty"`
	// Write: status of the write cycle (entries only); absent when none ran.
	Write           string         `json:"write,omitempty"`
	Steps           []string       `json:"steps,omitempty"`
	Sample          map[string]any `json:"sample,omitempty"`
	Generated       []string       `json:"generated,omitempty"`
	RuntimeRequired []string       `json:"runtime_required,omitempty"`
	Findings        []string       `json:"findings,omitempty"`
}

// Build reads dist/ (compositions, fragments, verification reports) and
// returns the catalog and one document per package.
func Build(dist string) (*Catalog, map[string]*Doc, error) {
	comps, reports, err := readCompositions(dist)
	if err != nil {
		return nil, nil, err
	}
	cat := &Catalog{Format: Format, Generator: ir.GeneratorVersion, Cores: map[string]*Core{}, Packages: map[string]*PkgEntry{}}
	docs := map[string]*Doc{}
	byRelease := map[string]*Release{}
	firstSeen := map[string]map[string]int{} // package -> hash -> index of first composition
	selGen := map[string]string{}            // select key -> generator of the chosen fragment
	for i, c := range comps {
		cat.Cores[c.CoreVersion] = &Core{Release: c.Release, Series: c.Series, Framework: c.FrameworkHash}
		r := byRelease[c.Release]
		if r == nil {
			r = &Release{Release: c.Release, Series: c.Series}
			byRelease[c.Release] = r
			cat.Releases = append(cat.Releases, r)
		}
		r.Status, r.Current = string(c.Status), c.ID() // comps are in order: last wins
		cc := &Composition{ID: c.ID(), Core: c.CoreVersion, Framework: c.FrameworkHash, Generator: c.Generator, Fragments: map[string]string{}}
		if rep := reports[c.ID()]; rep != nil {
			v := &Verification{Date: rep.Date, Generator: rep.Generator, Instance: rep.Instance, Packages: rep.Packages, Results: map[string]int{}}
			for _, res := range rep.Results {
				v.Results[res.Check+"/"+res.Status]++
			}
			cc.Verified = v
		}
		r.Compositions = append(r.Compositions, cc)
		for _, f := range c.Fragments {
			cc.Fragments[f.Package] = f.Hash
			d := docs[f.Package]
			if d == nil {
				d = &Doc{Format: Format, Package: f.Package, Generator: ir.GeneratorVersion,
					Fragments: map[string]*Fragment{}, Select: map[string]map[string]string{}, Resources: map[string]*Resource{}}
				docs[f.Package] = d
				firstSeen[f.Package] = map[string]int{}
			}
			fr := d.Fragments[f.Hash]
			if fr == nil {
				fr = &Fragment{}
				d.Fragments[f.Hash] = fr
				firstSeen[f.Package][f.Hash] = i
			}
			fr.Versions = addUnique(fr.Versions, f.Version)
			fr.Cores = addUnique(fr.Cores, c.CoreVersion)
			fr.Releases = addUnique(fr.Releases, c.Release)
			sel := d.Select[c.FrameworkHash]
			if sel == nil {
				sel = map[string]string{}
				d.Select[c.FrameworkHash] = sel
			}
			// the same package version on the same framework has the same
			// sources; two fragments can only come from two generator
			// versions (older catalog states are not rebuilt). The newer
			// generator wins; anything else means the key is not unique.
			key := f.Package + " " + c.FrameworkHash + " " + f.Version
			if prev, ok := sel[f.Version]; ok && prev != f.Hash {
				switch cmp := pkgrepo.CompareVersions(c.Generator, selGen[key]); {
				case cmp == 0:
					return nil, nil, fmt.Errorf("%s %s with framework %s maps to two fragments (%s, %s); the selection key is not unique",
						f.Package, f.Version, c.FrameworkHash, prev, f.Hash)
				case cmp < 0:
					continue
				}
			}
			sel[f.Version] = f.Hash
			selGen[key] = c.Generator
		}
	}
	for pkg, d := range docs {
		order := sortedByFirstSeen(firstSeen[pkg])
		for i, h := range order {
			d.Fragments[h].Order = i
		}
		for _, h := range order {
			frag, err := readFragment(dist, pkg, h)
			if err != nil {
				return nil, nil, err
			}
			d.add(h, frag)
		}
		d.finish(order)
		for _, c := range comps {
			if rep := reports[c.ID()]; rep != nil {
				for _, f := range c.Fragments {
					if f.Package == pkg && contains(rep.Packages, pkg) {
						d.addVerification(f.Hash, c, rep)
					}
				}
			}
		}
		cat.Packages[pkg] = &PkgEntry{File: "capabilities/" + pkg + ".json", Fragments: order}
	}
	return cat, docs, nil
}

// readCompositions returns all compositions in version order (by core
// version, then catalog id) and the verification reports by composition.
func readCompositions(dist string) ([]*fragment.Composition, map[string]*verify.Report, error) {
	files, err := filepath.Glob(filepath.Join(dist, "*", "compositions", "*.json"))
	if err != nil {
		return nil, nil, err
	}
	var comps []*fragment.Composition
	for _, p := range files {
		var c fragment.Composition
		if err := readJSON(p, &c); err != nil {
			return nil, nil, err
		}
		comps = append(comps, &c)
	}
	if len(comps) == 0 {
		return nil, nil, fmt.Errorf("%s: no compositions found", dist)
	}
	sort.SliceStable(comps, func(i, j int) bool {
		if c := pkgrepo.CompareVersions(comps[i].CoreVersion, comps[j].CoreVersion); c != 0 {
			return c < 0
		}
		return comps[i].ID() < comps[j].ID()
	})
	reports := map[string]*verify.Report{}
	vfiles, _ := filepath.Glob(filepath.Join(dist, "*", "opnsense-*.verified.json"))
	for _, p := range vfiles {
		var rep verify.Report
		if err := readJSON(p, &rep); err != nil {
			return nil, nil, err
		}
		// a report without composition or package list (older generators)
		// cannot say what it tested; it does not count
		if rep.Composition != "" && len(rep.Packages) > 0 {
			reports[rep.Composition] = &rep
		}
	}
	return comps, reports, nil
}

func readFragment(dist, pkg, hash string) (*fragment.Fragment, error) {
	var f fragment.Fragment
	return &f, readJSON(filepath.Join(dist, "fragments", pkg, hash+".ir.json"), &f)
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// per-fragment raw values, merged into variants by finish
type rawResource struct {
	kind   string
	api    map[string]*API
	fields map[string]map[string]*Field // field -> hash -> descriptor
}

type rawAction struct {
	byHash map[string]*Action
}

func (d *Doc) raw() (map[string]*rawResource, map[string]*rawAction) {
	if d.rawRes == nil {
		d.rawRes, d.rawAct = map[string]*rawResource{}, map[string]*rawAction{}
	}
	return d.rawRes, d.rawAct
}

// ResourceID returns the stable identifier of the resource an endpoint
// belongs to: "<module>/<controller>" for settings,
// "<module>/<controller>#<model path>" for entry lists; "" for actions.
func ResourceID(e *ir.Endpoint) (id, kind, op string) {
	k := string(e.Kind)
	switch {
	case e.Kind == ir.KindModelGet || e.Kind == ir.KindModelSet:
		if e.Model == "" {
			return "", "", ""
		}
		return e.Module + "/" + e.Controller, "settings", strings.TrimPrefix(k, "model.")
	case strings.HasPrefix(k, "item.") && e.ModelPath != "" && e.Model != "":
		return e.Module + "/" + e.Controller + "#" + e.ModelPath, "entries", strings.TrimPrefix(k, "item.")
	}
	return "", "", ""
}

func (d *Doc) add(hash string, f *fragment.Fragment) {
	res, acts := d.raw()
	models := map[string]*model.Model{}
	for _, m := range f.Models {
		models[m.Class] = m
	}
	// group endpoints per resource; prefer one endpoint per operation
	type cand struct {
		e  *ir.Endpoint
		op string
	}
	groups := map[string][]cand{}
	kinds := map[string]string{}
	var loose []*ir.Endpoint
	for _, e := range f.Endpoints {
		id, kind, op := ResourceID(e)
		if id == "" {
			loose = append(loose, e)
			continue
		}
		groups[id] = append(groups[id], cand{e, op})
		kinds[id] = kind
	}
	for id, cs := range groups {
		chosen := map[string]*ir.Endpoint{}
		for _, c := range cs {
			if prev := chosen[c.op]; prev == nil || better(c.e, prev) {
				chosen[c.op] = c.e
			}
		}
		var first *ir.Endpoint
		ops := map[string]*Operation{}
		for _, c := range cs {
			if chosen[c.op] != c.e {
				loose = append(loose, c.e) // second endpoint of the same kind: an action
				continue
			}
			ops[c.op] = operation(c.e)
			if first == nil || c.e.Path < first.Path {
				first = c.e
			}
		}
		r := res[id]
		if r == nil {
			r = &rawResource{kind: kinds[id], api: map[string]*API{}, fields: map[string]map[string]*Field{}}
			res[id] = r
		}
		a := &API{Model: first.Model, ModelPath: first.ModelPath, Operations: ops}
		for _, c := range cs {
			if chosen[c.op] == c.e {
				if a.Wrapper == "" {
					a.Wrapper = c.e.Wrapper
				}
				if a.Apply == "" {
					a.Apply = c.e.Apply
				}
			}
		}
		r.api[hash] = a
		m := models[first.Model]
		if m == nil {
			continue
		}
		var root *model.Node
		if r.kind == "settings" {
			root = m.Root
		} else {
			root = m.Find(first.ModelPath)
		}
		if root == nil {
			continue
		}
		walk(root, "", func(path string, n *model.Node) {
			if r.fields[path] == nil {
				r.fields[path] = map[string]*Field{}
			}
			r.fields[path][hash] = field(n)
		})
	}
	for _, e := range loose {
		a := acts[e.Path]
		if a == nil {
			a = &rawAction{byHash: map[string]*Action{}}
			acts[e.Path] = a
		}
		a.byHash[hash] = &Action{Kind: string(e.Kind), Methods: e.Methods, Params: params(e), Model: e.Model,
			Source: string(e.Source), Gaps: gapCodes(e.Gaps)}
	}
}

// better: base-call endpoints over others, endpoints taking a uuid over
// those that do not (get), then the shorter path, then lexical order.
func better(a, b *ir.Endpoint) bool {
	if (a.Source == ir.SourceBaseCall) != (b.Source == ir.SourceBaseCall) {
		return a.Source == ir.SourceBaseCall
	}
	if (len(a.Params) > 0) != (len(b.Params) > 0) {
		return len(a.Params) > 0
	}
	if len(a.Path) != len(b.Path) {
		return len(a.Path) < len(b.Path)
	}
	return a.Path < b.Path
}

func operation(e *ir.Endpoint) *Operation {
	return &Operation{Path: e.Path, Methods: e.Methods, Params: params(e), Wrapper: e.Wrapper, Outer: e.Outer,
		Forced: e.Forced, Computed: e.Computed, Transformed: e.Transformed,
		Source: string(e.Source), Gaps: gapCodes(e.Gaps)}
}

func params(e *ir.Endpoint) []string {
	var out []string
	for _, p := range e.Params {
		n := p.Name
		if p.Optional {
			n += "?"
		}
		out = append(out, n)
	}
	return out
}

func gapCodes(gs []gap.Gap) []string {
	var out []string
	for _, g := range gs {
		if c := string(g.Code); !contains(out, c) {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// walk visits the leaf fields below n with dotted paths; nested entry
// lists (array nodes) are separate resources and are not descended into.
func walk(n *model.Node, prefix string, visit func(string, *model.Node)) {
	for _, c := range n.Children {
		p := c.Name
		if prefix != "" {
			p = prefix + "." + c.Name
		}
		switch c.Kind {
		case model.KindField:
			visit(p, c)
		case model.KindContainer:
			walk(c, p, visit)
		}
	}
}

// uiText: properties that are texts for the web UI, not part of the
// contract. They change often (punctuation, wording) and would split
// fields into variants that differ in nothing a client can rely on; they
// stay in the OpenAPI documents.
var uiText = map[string]bool{"ValidationMessage": true, "BlankDesc": true}

func field(n *model.Node) *Field {
	f := &Field{Type: strings.TrimPrefix(n.Type, ".\\"), Base: n.BaseType, ReadShape: string(n.Shape),
		Multiple: n.Multiple, Required: n.Required, Volatile: n.Volatile, Default: n.Default,
		Options: n.Options, Relations: n.Relations, Gaps: gapCodes(n.Gaps)}
	// an empty default and no default both leave the field empty
	if f.Default != nil && *f.Default == "" {
		f.Default = nil
	}
	for k, v := range n.Props {
		if uiText[k] {
			continue
		}
		if f.Props == nil {
			f.Props = map[string]string{}
		}
		f.Props[k] = v
	}
	return f
}

// finish merges the per-fragment values into variants.
func (d *Doc) finish(order []string) {
	res, acts := d.raw()
	for id, r := range res {
		out := &Resource{Kind: r.kind, Fields: map[string][]*Field{}}
		for _, h := range order {
			if r.api[h] != nil {
				out.In = append(out.In, h)
			}
		}
		for _, g := range group(out.In, func(h string) any { return r.api[h] }) {
			a := *r.api[g.hashes[0]]
			a.In = inValue(g.hashes, out.In)
			out.API = append(out.API, &a)
		}
		for name, byHash := range r.fields {
			var present []string
			for _, h := range out.In {
				if byHash[h] != nil {
					present = append(present, h)
				}
			}
			for _, g := range group(present, func(h string) any { return byHash[h] }) {
				f := *byHash[g.hashes[0]]
				f.In = inValue(g.hashes, out.In)
				out.Fields[name] = append(out.Fields[name], &f)
			}
		}
		d.Resources[id] = out
	}
	d.markShared()
	if len(acts) > 0 {
		d.Actions = map[string][]*Action{}
	}
	for path, a := range acts {
		var present []string
		for _, h := range order {
			if a.byHash[h] != nil {
				present = append(present, h)
			}
		}
		for _, g := range group(present, func(h string) any { return a.byHash[h] }) {
			x := *a.byHash[g.hashes[0]]
			x.In = g.hashes
			d.Actions[path] = append(d.Actions[path], &x)
		}
	}
	d.rawRes, d.rawAct = nil, nil
}

// markShared sets SharedWith: resources whose API variants name the same
// model class (case-insensitive, as PHP) and model path. Several
// controllers can expose one model; writing through two of them means two
// owners of the same data.
func (d *Doc) markShared() {
	byData := map[string][]string{}
	for id, r := range d.Resources {
		seen := map[string]bool{}
		for _, a := range r.API {
			k := strings.ToLower(a.Model) + "#" + a.ModelPath
			if !seen[k] {
				seen[k] = true
				byData[k] = append(byData[k], id)
			}
		}
	}
	for _, ids := range byData {
		for _, id := range ids {
			for _, other := range ids {
				if other != id {
					d.Resources[id].SharedWith = addUnique(d.Resources[id].SharedWith, other)
				}
			}
		}
	}
	for _, r := range d.Resources {
		sort.Strings(r.SharedWith)
	}
}

type variant struct{ hashes []string }

// group partitions hashes (in order) by equal JSON value, keeping the
// order of first appearance.
func group(hashes []string, val func(string) any) []*variant {
	var out []*variant
	idx := map[string]*variant{}
	for _, h := range hashes {
		b, _ := json.Marshal(val(h))
		k := string(b)
		v := idx[k]
		if v == nil {
			v = &variant{}
			idx[k] = v
			out = append(out, v)
		}
		v.hashes = append(v.hashes, h)
	}
	return out
}

func inValue(hashes, all []string) any {
	if len(hashes) == len(all) {
		return All
	}
	return hashes
}

// addVerification attaches the results of rep (run on composition c) to
// the resources of fragment hash.
func (d *Doc) addVerification(hash string, c *fragment.Composition, rep *verify.Report) {
	byPath := map[string][]*verify.Result{}
	for _, r := range rep.Results {
		byPath[r.Path] = append(byPath[r.Path], r)
	}
	for _, res := range d.Resources {
		if !contains(res.In, hash) {
			continue
		}
		var api *API
		for _, a := range res.API {
			if a.In == All || contains(a.In.([]string), hash) {
				api = a
			}
		}
		if api == nil {
			continue
		}
		v := &ResVerify{Release: c.Release, Composition: c.ID(), Generator: rep.Generator, Date: rep.Date}
		for op, o := range api.Operations {
			for _, r := range byPath[o.Path] {
				switch {
				case r.Check == "write-cycle":
					v.Write, v.Steps, v.Sample, v.Generated, v.RuntimeRequired = r.Status, r.Steps, r.Sample, r.Generated, r.RuntimeRequired
					if r.Status != "ok" {
						v.Findings = append(v.Findings, r.Findings...)
					}
				case (op == "get" || op == "search") && (r.Check == "shape" || r.Check == "schema"):
					v.Read = worse(v.Read, r.Status)
					if r.Status != "ok" && r.Status != "accepted" {
						v.Findings = append(v.Findings, r.Findings...)
					}
				}
			}
		}
		if v.Read == "" && v.Write == "" {
			continue
		}
		if res.Verify == nil {
			res.Verify = map[string]*ResVerify{}
		}
		// a fragment can be tested in several compositions: the newest
		// generator counts, then the newest run
		if old := res.Verify[hash]; old != nil {
			if c := pkgrepo.CompareVersions(old.Generator, v.Generator); c > 0 || (c == 0 && old.Date >= v.Date) {
				continue
			}
		}
		res.Verify[hash] = v
	}
}

// worse returns the more severe of two check statuses.
func worse(a, b string) string {
	rank := map[string]int{"": 0, "ok": 1, "accepted": 2, "skipped": 3, "known-gap": 4, "mismatch": 5, "error": 6}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func sortedByFirstSeen(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for h := range m {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		if m[out[i]] != m[out[j]] {
			return m[out[i]] < m[out[j]]
		}
		return out[i] < out[j]
	})
	return out
}

func addUnique(list []string, v string) []string {
	if contains(list, v) {
		return list
	}
	return append(list, v)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// Write stores catalog.json and capabilities/<package>.json below dir,
// after validating every document against the JSON Schema.
func Write(dir string, cat *Catalog, docs map[string]*Doc) error {
	if err := os.MkdirAll(filepath.Join(dir, "capabilities"), 0o755); err != nil {
		return err
	}
	if err := Validate(cat, docs); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(dir, "catalog.json"), cat); err != nil {
		return err
	}
	for pkg, d := range docs {
		if err := writeJSON(filepath.Join(dir, "capabilities", pkg+".json"), d); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "capability.schema.json"), schemaJSON, 0o644)
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(v); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
