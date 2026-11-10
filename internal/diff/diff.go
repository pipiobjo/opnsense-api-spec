// Package diff compares the IR of two releases: models, fields, endpoints
// and packages.
package diff

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// Change is one difference. Old/New are empty for additions/removals.
type Change struct {
	Kind    string `json:"kind"` // added, removed, changed
	Subject string `json:"subject"`
	Package string `json:"package,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Old     string `json:"old,omitempty"`
	New     string `json:"new,omitempty"`
}

// Report is the difference between two releases.
type Report struct {
	From      string   `json:"from"`
	To        string   `json:"to"`
	Packages  []Change `json:"packages"`
	Models    []Change `json:"models"`
	Fields    []Change `json:"fields"`
	Endpoints []Change `json:"endpoints"`
}

// Empty reports whether nothing changed.
func (r *Report) Empty() bool {
	return len(r.Packages)+len(r.Models)+len(r.Fields)+len(r.Endpoints) == 0
}

type fieldInfo struct {
	pkg, desc string
}

// describe a node so that any API-relevant change alters the string
func describe(n *model.Node) string {
	parts := []string{string(n.Kind)}
	if n.Type != "" {
		parts = append(parts, "type="+n.Type)
	}
	if n.Shape != "" {
		parts = append(parts, "read="+string(n.Shape))
	}
	if n.Required {
		parts = append(parts, "required")
	}
	if n.Multiple {
		parts = append(parts, "multiple")
	}
	if n.Default != nil {
		parts = append(parts, fmt.Sprintf("default=%q", *n.Default))
	}
	if len(n.Options) > 0 {
		var o []string
		for _, x := range n.Options {
			o = append(o, x.Value)
		}
		parts = append(parts, "options="+strings.Join(o, "|"))
	}
	return strings.Join(parts, " ")
}

func fields(rel *ir.Release) map[string]fieldInfo {
	out := map[string]fieldInfo{}
	for _, m := range rel.Models {
		var walk func(n *model.Node, path string)
		walk = func(n *model.Node, path string) {
			for _, c := range n.Children {
				p := c.Name
				if path != "" {
					p = path + "." + c.Name
				}
				out[m.Class+" "+p] = fieldInfo{pkg: m.Package, desc: describe(c)}
				walk(c, p)
			}
		}
		walk(m.Root, "")
	}
	return out
}

func endpointDesc(e *ir.Endpoint) string {
	return fmt.Sprintf("%s %s methods=%s wrapper=%s path=%s params=%s",
		e.Kind, e.Source, strings.Join(e.Methods, ","), e.Wrapper, e.ModelPath, params(e))
}

func params(e *ir.Endpoint) string {
	var ps []string
	for _, p := range e.Params {
		s := p.Name
		if p.Optional {
			s += "?"
		}
		ps = append(ps, s)
	}
	return strings.Join(ps, ",")
}

// Compare builds the report from a to b.
func Compare(a, b *ir.Release) *Report {
	r := &Report{From: a.Version, To: b.Version}

	// packages
	pa, pb := map[string]string{}, map[string]string{}
	for _, p := range a.Packages {
		pa[p.Name] = p.Version
	}
	for _, p := range b.Packages {
		pb[p.Name] = p.Version
	}
	r.Packages = compareMaps(pa, pb, func(k string) string { return k }, nil)

	// models (version)
	ma, mb := map[string]string{}, map[string]string{}
	pkgOf := map[string]string{}
	for _, m := range a.Models {
		ma[m.Class] = m.Version
		pkgOf[m.Class] = m.Package
	}
	for _, m := range b.Models {
		mb[m.Class] = m.Version
		pkgOf[m.Class] = m.Package
	}
	r.Models = compareMaps(ma, mb, func(k string) string { return k }, pkgOf)
	for i := range r.Models {
		if r.Models[i].Kind == "changed" {
			r.Models[i].Detail = "model version"
		}
	}

	// fields
	fa, fb := fields(a), fields(b)
	da, db := map[string]string{}, map[string]string{}
	fpkg := map[string]string{}
	for k, v := range fa {
		da[k] = v.desc
		fpkg[k] = v.pkg
	}
	for k, v := range fb {
		db[k] = v.desc
		fpkg[k] = v.pkg
	}
	// skip fields below a model that was added/removed as a whole
	whole := map[string]bool{}
	for _, c := range r.Models {
		if c.Kind != "changed" {
			whole[c.Subject] = true
		}
	}
	r.Fields = []Change{}
	for _, c := range compareMaps(da, db, func(k string) string { return k }, fpkg) {
		if whole[strings.SplitN(c.Subject, " ", 2)[0]] {
			continue
		}
		r.Fields = append(r.Fields, c)
	}

	// endpoints
	ea, eb := map[string]string{}, map[string]string{}
	epkg := map[string]string{}
	for _, e := range a.Endpoints {
		ea[e.Path] = endpointDesc(e)
		epkg[e.Path] = e.Package
	}
	for _, e := range b.Endpoints {
		eb[e.Path] = endpointDesc(e)
		epkg[e.Path] = e.Package
	}
	r.Endpoints = compareMaps(ea, eb, func(k string) string { return k }, epkg)
	return r
}

func compareMaps(a, b map[string]string, subj func(string) string, pkg map[string]string) []Change {
	out := []Change{} // [] rather than null in JSON
	for k, va := range a {
		vb, ok := b[k]
		switch {
		case !ok:
			out = append(out, Change{Kind: "removed", Subject: subj(k), Old: va})
		case va != vb:
			out = append(out, Change{Kind: "changed", Subject: subj(k), Old: va, New: vb})
		}
	}
	for k, vb := range b {
		if _, ok := a[k]; !ok {
			out = append(out, Change{Kind: "added", Subject: subj(k), New: vb})
		}
	}
	if pkg != nil {
		for i := range out {
			out[i].Package = pkg[out[i].Subject]
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Subject != out[j].Subject {
			return out[i].Subject < out[j].Subject
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// Markdown renders the report for humans.
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# API changes: OPNsense %s -> %s\n\n", r.From, r.To)
	if r.Empty() {
		b.WriteString("No API-relevant changes.\n")
		return b.String()
	}
	count := func(cs []Change, k string) int {
		n := 0
		for _, c := range cs {
			if c.Kind == k {
				n++
			}
		}
		return n
	}
	b.WriteString("| | added | removed | changed |\n|---|---|---|---|\n")
	for _, s := range []struct {
		name string
		cs   []Change
	}{{"packages", r.Packages}, {"models", r.Models}, {"fields", r.Fields}, {"endpoints", r.Endpoints}} {
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", s.name, count(s.cs, "added"), count(s.cs, "removed"), count(s.cs, "changed"))
	}
	section := func(title string, cs []Change) {
		if len(cs) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n## %s\n\n| change | subject | package | old | new |\n|---|---|---|---|---|\n", title)
		for _, c := range cs {
			fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s |\n", c.Kind, c.Subject, c.Package, esc(c.Old), esc(c.New))
		}
	}
	section("Packages", r.Packages)
	section("Models", r.Models)
	section("Fields", r.Fields)
	section("Endpoints", r.Endpoints)
	return b.String()
}

func esc(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}
