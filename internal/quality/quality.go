// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package quality turns silent breakage into findings: invariants of a
// release (build gates), regressions against the previous release, and a
// fingerprint of the framework files the derivation relies on.
package quality

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
	"github.com/pipiobjo/opnsense-api-spec/internal/php"
)

// FrameworkFiles are core files (relative to the MVC app dir) whose
// semantics the derivation assumes: generic actions and helpers, field
// base classes, routing. A change means the derivation rules may no longer
// hold and needs a contract test before publishing.
var FrameworkFiles = []string{
	"controllers/OPNsense/Base/ApiControllerBase.php",
	"controllers/OPNsense/Base/ApiMutableModelControllerBase.php",
	"controllers/OPNsense/Base/ApiMutableServiceControllerBase.php",
	"models/OPNsense/Base/FieldTypes/BaseField.php",
	"models/OPNsense/Base/FieldTypes/BaseListField.php",
	"models/OPNsense/Base/FieldTypes/BaseSetField.php",
	"models/OPNsense/Base/FieldTypes/ArrayField.php",
	"models/OPNsense/Base/FieldTypes/ContainerField.php",
	"library/OPNsense/Mvc/Router.php",
	"library/OPNsense/Mvc/Dispatcher.php",
	"library/OPNsense/Firewall/Util.php",
}

// FrameworkDirs: every file in them counts as a framework file. The field
// classes and validators define what a field takes (ADR 0018), and a
// plugin's field inherits from them: two releases whose field classes
// differ must not share a framework hash, or a plugin version would map
// to two fragments on one framework.
var FrameworkDirs = []string{
	"models/OPNsense/Base/FieldTypes",
	"models/OPNsense/Base/Validators",
}

// frameworkPaths: FrameworkFiles, then the files of FrameworkDirs in
// name order.
func frameworkPaths(appDir string) ([]string, error) {
	out := append([]string(nil), FrameworkFiles...)
	seen := map[string]bool{}
	for _, f := range out {
		seen[f] = true
	}
	for _, d := range FrameworkDirs {
		ents, err := os.ReadDir(filepath.Join(appDir, d))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		for _, e := range ents {
			f := d + "/" + e.Name()
			if !e.IsDir() && strings.HasSuffix(f, ".php") && !seen[f] {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	return out, nil
}

var spaceRe = regexp.MustCompile(`\s+`)

// Fingerprint hashes the framework files without comments and whitespace,
// so copyright years and formatting do not count. A missing file is
// recorded as "missing". The combined hash covers all files.
func Fingerprint(appDir string) (map[string]string, string, error) {
	files := map[string]string{}
	all := sha256.New()
	paths, err := frameworkPaths(appDir)
	if err != nil {
		return nil, "", err
	}
	for _, f := range paths {
		src, err := os.ReadFile(filepath.Join(appDir, f))
		switch {
		case os.IsNotExist(err):
			files[f] = "missing"
		case err != nil:
			return nil, "", err
		default:
			norm := spaceRe.ReplaceAllString(php.StripComments(string(src)), " ")
			sum := sha256.Sum256([]byte(norm))
			files[f] = fmt.Sprintf("%x", sum)[:16]
		}
		fmt.Fprintf(all, "%s=%s\n", f, files[f])
	}
	return files, fmt.Sprintf("%x", all.Sum(nil))[:16], nil
}

// Invariants returns violations that make a release unusable; the build
// must stop on any of them. refs are the $ref targets used in the rendered
// document, defined the schema names it defines.
func Invariants(rel *ir.Release, refs []string, defined map[string]bool) []string {
	var out []string
	pkgs := map[string]bool{}
	for _, p := range rel.Packages {
		pkgs[p.Name] = true
	}
	models := map[string]bool{}
	for _, m := range rel.Models {
		models[m.Class] = true
		if !pkgs[m.Package] {
			out = append(out, fmt.Sprintf("model %s: package %q not in the catalog selection", m.Class, m.Package))
		}
	}
	seen := map[string]bool{}
	for _, e := range rel.Endpoints {
		if seen[e.Path] {
			out = append(out, "duplicate endpoint path "+e.Path)
		}
		seen[e.Path] = true
		if len(e.Methods) == 0 {
			out = append(out, "endpoint "+e.Path+" has no HTTP method")
		}
		if e.Model != "" && !models[e.Model] {
			out = append(out, fmt.Sprintf("endpoint %s refers to unknown model %s", e.Path, e.Model))
		}
		if !pkgs[e.Package] {
			out = append(out, fmt.Sprintf("endpoint %s: package %q not in the catalog selection", e.Path, e.Package))
		}
	}
	const local = "#/components/schemas/"
	for _, r := range refs {
		if name, ok := strings.CutPrefix(r, local); ok && !defined[name] {
			out = append(out, "unresolved $ref "+r)
		}
	}
	sort.Strings(out)
	return out
}

// Report compares a release with its predecessor.
type Report struct {
	From string `json:"from"`
	To   string `json:"to"`
	// Regressions: things that were derived before and are not anymore.
	Regressions []string `json:"regressions"`
	// NewGaps: gap codes that appeared on operations existing in both.
	NewGaps map[gap.Code]int `json:"new_gaps"`
	// FrameworkChanged: framework files whose normalized content changed.
	FrameworkChanged []string `json:"framework_changed"`
}

// Notable is true when the report has something to explain.
func (r *Report) Notable() bool {
	return len(r.Regressions) > 0 || len(r.FrameworkChanged) > 0
}

func fieldShapes(rel *ir.Release) map[string]model.Shape {
	out := map[string]model.Shape{}
	for _, m := range rel.Models {
		var walk func(n *model.Node, path string)
		walk = func(n *model.Node, path string) {
			for _, c := range n.Children {
				p := strings.TrimPrefix(path+"."+c.Name, ".")
				if c.Kind == model.KindField {
					s := c.Shape
					if gap.Has(c.Gaps, gap.CustomReadShape) {
						s = model.ShapeUnknown
					}
					out[m.Class+" "+p] = s
				}
				walk(c, p)
			}
		}
		walk(m.Root, "")
	}
	return out
}

// Compare finds regressions from prev to cur.
func Compare(prev, cur *ir.Release) *Report {
	r := &Report{From: prev.Version, To: cur.Version, Regressions: []string{}, NewGaps: map[gap.Code]int{}, FrameworkChanged: []string{}}
	before := map[string]*ir.Endpoint{}
	for _, e := range prev.Endpoints {
		before[e.Path] = e
	}
	for _, e := range cur.Endpoints {
		p, ok := before[e.Path]
		if !ok {
			continue // new endpoint: a diff, not a regression
		}
		if p.Source != ir.SourceSignature && e.Source == ir.SourceSignature {
			r.Regressions = append(r.Regressions, fmt.Sprintf("%s: typed (%s) -> untyped", e.Path, p.Source))
		}
		had := map[gap.Code]bool{}
		for _, g := range p.Gaps {
			had[g.Code] = true
		}
		for _, g := range e.Gaps {
			if !had[g.Code] {
				r.NewGaps[g.Code]++
			}
		}
	}
	ps, cs := fieldShapes(prev), fieldShapes(cur)
	for k, s := range cs {
		if old, ok := ps[k]; ok && old != model.ShapeUnknown && s == model.ShapeUnknown {
			r.Regressions = append(r.Regressions, fmt.Sprintf("field %s: read shape %s -> unknown", k, old))
		}
	}
	for f, h := range cur.Framework {
		if old, ok := prev.Framework[f]; ok && old != h {
			r.FrameworkChanged = append(r.FrameworkChanged, f)
		}
	}
	sort.Strings(r.Regressions)
	sort.Strings(r.FrameworkChanged)
	return r
}
