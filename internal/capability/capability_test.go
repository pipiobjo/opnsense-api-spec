// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package capability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/controller"
	"github.com/pipiobjo/opnsense-api-spec/internal/fragment"
	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

// testDist writes a small dist/: package os-demo in two versions (the
// field "area" changes from text to select, "enabled" stays), core in one
// version, two releases.
type testDist struct {
	t    *testing.T
	dir  string
	gen  string
	n    int
	frag map[string]*fragment.Fragment
}

func newDist(t *testing.T) *testDist {
	return &testDist{t: t, dir: t.TempDir(), gen: ir.GeneratorVersion, frag: map[string]*fragment.Fragment{}}
}

func str(s string) *string { return &s }

func demoFragment(areaType string, msg string) *fragment.Fragment {
	area := &model.Node{Name: "area", Kind: model.KindField, Type: areaType, BaseType: areaType, Shape: model.ShapeString, Default: str(""),
		Props: map[string]string{"ValidationMessage": msg}}
	if areaType == "OptionField" {
		area.BaseType, area.Shape = "BaseListField", model.ShapeSelect
		area.Options = []model.Option{{Value: "0.0.0.0"}}
	}
	entry := &model.Node{Name: "network", Kind: model.KindArray, Children: []*model.Node{
		area,
		{Name: "enabled", Kind: model.KindField, Type: "BooleanField", BaseType: "BooleanField", Shape: model.ShapeString, Required: true, Default: str("1")},
	}}
	root := &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer, Children: []*model.Node{
			{Name: "enabled", Kind: model.KindField, Type: "BooleanField", BaseType: "BooleanField", Shape: model.ShapeString},
		}},
		{Name: "networks", Kind: model.KindContainer, Children: []*model.Node{entry}},
	}}
	m := &model.Model{Class: "OPNsense\\Demo\\Demo", Package: "os-demo", Root: root}
	ep := func(action string, kind ir.Kind, path string, params ...string) *ir.Endpoint {
		e := &ir.Endpoint{Path: "/api/demo/settings/" + action, Package: "os-demo", Module: "demo", Controller: "settings",
			Action: action, Methods: []string{"POST"}, Kind: kind, Source: ir.SourceBaseCall, Model: m.Class, ModelPath: path,
			Apply: "/api/demo/service/reconfigure"}
		for _, p := range params {
			e.Params = append(e.Params, controllerParam(p))
		}
		return e
	}
	get := ep("get", ir.KindModelGet, "")
	get.Source, get.Wrapper = ir.SourceBase, "demo"
	set := ep("set", ir.KindModelSet, "")
	set.Source, set.Wrapper = ir.SourceBase, "demo"
	add := ep("addNetwork", ir.KindItemAdd, "networks.network")
	add.Wrapper = "network"
	del := ep("delNetwork", ir.KindItemDel, "networks.network", "uuid")
	del.Gaps = []gap.Gap{gap.New(gap.ResultNotReturned, "x")}
	getItem := ep("getNetwork", ir.KindItemGet, "networks.network", "uuid")
	getItem.Wrapper = "network"
	getAll := ep("getNetworks", ir.KindItemGet, "networks.network") // second get, no uuid: an action
	status := &ir.Endpoint{Path: "/api/demo/service/status", Package: "os-demo", Module: "demo", Controller: "service",
		Action: "status", Methods: []string{"GET"}, Kind: ir.KindServiceStatus, Source: ir.SourceBase}
	return &fragment.Fragment{Package: "os-demo", Models: []*model.Model{m},
		Endpoints: []*ir.Endpoint{add, del, get, getAll, getItem, set, status}}
}

func coreFragment() *fragment.Fragment {
	return &fragment.Fragment{Package: "opnsense", Models: []*model.Model{}, Endpoints: []*ir.Endpoint{
		{Path: "/api/core/firmware/status", Package: "opnsense", Module: "core", Controller: "firmware", Action: "status",
			Methods: []string{"GET"}, Kind: ir.KindCustom, Source: ir.SourceSignature},
	}}
}

// compose writes a composition for release with the given fragments and
// package versions; framework is the core framework hash.
func (d *testDist) compose(release, core, framework string, frags map[string]*fragment.Fragment, versions map[string]string) *fragment.Composition {
	d.n++
	c := &fragment.Composition{Release: release, CoreVersion: core, Series: release[:4], ABI: "FreeBSD:15:amd64",
		CatalogSHA256: strings.Repeat(string(rune('a'+d.n)), 64), Status: fragment.Final, FrameworkHash: framework,
		Generator: d.gen}
	for _, pkg := range []string{"opnsense", "os-demo"} {
		f := frags[pkg]
		if f == nil {
			continue
		}
		h := f.Hash()
		d.write(filepath.Join("fragments", pkg, h+".ir.json"), f)
		c.Fragments = append(c.Fragments, fragment.Ref{Package: pkg, Version: versions[pkg], Hash: h})
	}
	d.write(filepath.Join(release, "compositions", c.ID()+".json"), c)
	return c
}

func (d *testDist) write(rel string, v any) {
	p := filepath.Join(d.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		d.t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		d.t.Fatal(err)
	}
}

const fw1, fw2 = "1111111111111111", "2222222222222222"

func buildDemo(t *testing.T) (*testDist, *fragment.Fragment, *fragment.Fragment, *fragment.Composition) {
	d := newDist(t)
	v1, v2 := demoFragment("TextField", "old text"), demoFragment("OptionField", "new text.")
	core := coreFragment()
	d.compose("26.1.1", "26.1.1", fw1, map[string]*fragment.Fragment{"opnsense": core, "os-demo": v1}, map[string]string{"opnsense": "26.1.1", "os-demo": "1.0"})
	c2 := d.compose("26.1.2", "26.1.2_1", fw2, map[string]*fragment.Fragment{"opnsense": core, "os-demo": v2}, map[string]string{"opnsense": "26.1.2_1", "os-demo": "1.1"})
	return d, v1, v2, c2
}

func TestBuildVariantsAndSelect(t *testing.T) {
	d, v1, v2, _ := buildDemo(t)
	cat, docs, err := Build(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cat, docs); err != nil {
		t.Fatal(err)
	}
	if cat.Cores["26.1.2_1"].Framework != fw2 || cat.Cores["26.1.1"].Release != "26.1.1" {
		t.Errorf("cores: %+v", cat.Cores)
	}
	doc := docs["os-demo"]
	h1, h2 := v1.Hash(), v2.Hash()
	if doc.Select[fw1]["1.0"] != h1 || doc.Select[fw2]["1.1"] != h2 {
		t.Errorf("select: %v", doc.Select)
	}
	if doc.Fragments[h1].Order != 0 || doc.Fragments[h2].Order != 1 {
		t.Errorf("order: %+v %+v", doc.Fragments[h1], doc.Fragments[h2])
	}
	r := doc.Resources["demo/settings#networks.network"]
	if r == nil || r.Kind != "entries" {
		t.Fatalf("entry resource missing: %v", keys(doc.Resources))
	}
	area := r.Fields["area"]
	if len(area) != 2 || area[0].Type != "TextField" || area[1].ReadShape != "select" {
		t.Fatalf("area variants: %s", js(area))
	}
	if in, ok := area[1].In.([]string); !ok || len(in) != 1 || in[0] != h2 {
		t.Errorf("area[1].in = %v", area[1].In)
	}
	if area[0].Props["ValidationMessage"] != "" {
		t.Error("UI text must not be part of the field")
	}
	if area[0].Default != nil {
		t.Error("an empty default is no default")
	}
	if en := r.Fields["enabled"]; len(en) != 1 || en[0].In != All || !en[0].Required {
		t.Errorf("enabled should be one variant in all fragments: %s", js(en))
	}
	ops := r.API[0].Operations
	if ops["get"] == nil || ops["get"].Path != "/api/demo/settings/getNetwork" || ops["add"] == nil || ops["del"].Gaps[0] != "result-not-returned" {
		t.Errorf("operations: %s", js(ops))
	}
	if r.API[0].Wrapper != "network" || r.API[0].Apply != "/api/demo/service/reconfigure" || r.API[0].In != All {
		t.Errorf("api: %s", js(r.API))
	}
	s := doc.Resources["demo/settings"]
	if s == nil || s.Kind != "settings" || s.Fields["general.enabled"] == nil || s.Fields["networks.network.area"] != nil {
		t.Errorf("settings resource: fields of entry lists belong to the entry resource: %s", js(s))
	}
	if doc.Actions["/api/demo/settings/getNetworks"] == nil || doc.Actions["/api/demo/service/status"] == nil {
		t.Errorf("second get and service action must be actions: %v", keys(doc.Actions))
	}
}

func TestSelectConflict(t *testing.T) {
	d := newDist(t)
	core := coreFragment()
	// same version, same framework, different API, same generator: the
	// selection key would be ambiguous
	d.compose("26.1.1", "26.1.1", fw1, map[string]*fragment.Fragment{"opnsense": core, "os-demo": demoFragment("TextField", "")}, map[string]string{"opnsense": "26.1.1", "os-demo": "1.0"})
	d.compose("26.1.2", "26.1.2", fw1, map[string]*fragment.Fragment{"opnsense": core, "os-demo": demoFragment("OptionField", "")}, map[string]string{"opnsense": "26.1.2", "os-demo": "1.0"})
	if _, _, err := Build(d.dir); err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("want ambiguity error, got %v", err)
	}
	// the same with an older generator for the first state: newer wins
	d2 := newDist(t)
	d2.gen = "0.1.0"
	d2.compose("26.1.1", "26.1.1", fw1, map[string]*fragment.Fragment{"opnsense": core, "os-demo": demoFragment("TextField", "")}, map[string]string{"opnsense": "26.1.1", "os-demo": "1.0"})
	d2.gen = ir.GeneratorVersion
	newer := demoFragment("OptionField", "")
	d2.compose("26.1.2", "26.1.2", fw1, map[string]*fragment.Fragment{"opnsense": core, "os-demo": newer}, map[string]string{"opnsense": "26.1.2", "os-demo": "1.0"})
	_, docs, err := Build(d2.dir)
	if err != nil {
		t.Fatal(err)
	}
	if docs["os-demo"].Select[fw1]["1.0"] != newer.Hash() {
		t.Errorf("newer generator must win: %v", docs["os-demo"].Select)
	}
}

func TestVerification(t *testing.T) {
	d, _, v2, c2 := buildDemo(t)
	rep := &verify.Report{Release: "26.1.2", Composition: c2.ID(), Instance: "26.1.2_1", Generator: ir.GeneratorVersion,
		Date: "2026-11-15T10:00:00Z", Packages: []string{"opnsense", "os-demo"}, Results: []*verify.Result{
			{Path: "/api/demo/settings/getNetwork", Check: "shape", Status: "ok"},
			{Path: "/api/demo/settings/getNetwork", Check: "schema", Status: "known-gap", Findings: []string{"area differs"}},
			{Path: "/api/demo/settings/addNetwork", Check: "write-cycle", Status: "ok",
				Steps: []string{"add", "read-back", "set", "read-back-set", "del"}, Sample: map[string]any{"area": "0.0.0.0"}},
			{Path: "/api/demo/settings/get", Check: "shape", Status: "ok"},
		}}
	d.write(filepath.Join("26.1.2", "opnsense-26.1.2.verified.json"), rep)
	// a report without package list says nothing
	c1, _ := filepath.Glob(filepath.Join(d.dir, "26.1.1", "compositions", "*.json"))
	d.write(filepath.Join("26.1.1", "opnsense-26.1.1.verified.json"), &verify.Report{
		Composition: strings.TrimSuffix(filepath.Base(c1[0]), ".json"), Generator: ir.GeneratorVersion})
	cat, docs, err := Build(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cat, docs); err != nil {
		t.Fatal(err)
	}
	r := docs["os-demo"].Resources["demo/settings#networks.network"]
	v := r.Verify[v2.Hash()]
	if v == nil || v.Read != "known-gap" || v.Write != "ok" || len(v.Steps) != 5 || v.Sample["area"] != "0.0.0.0" || v.Composition != c2.ID() {
		t.Fatalf("entry verification: %s", js(r.Verify))
	}
	if len(r.Verify) != 1 {
		t.Errorf("the older fragment was not tested: %s", js(r.Verify))
	}
	s := docs["os-demo"].Resources["demo/settings"].Verify[v2.Hash()]
	if s == nil || s.Read != "ok" || s.Write != "" {
		t.Errorf("settings: read verified, write not tested: %s", js(s))
	}
	if first := cat.Releases[0].Compositions[0].Verified; first != nil {
		t.Errorf("a report without package list must not count: %s", js(first))
	}
	last := cat.Releases[len(cat.Releases)-1].Compositions[0].Verified
	if last == nil || last.Results["write-cycle/ok"] != 1 {
		t.Errorf("catalog verification: %s", js(last))
	}
}

// The schema must reject what it is there to reject.
func TestSchemaRejects(t *testing.T) {
	d, _, _, _ := buildDemo(t)
	cat, docs, err := Build(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	doc := docs["os-demo"]
	r := doc.Resources["demo/settings#networks.network"]
	r.Fields["area"][0].In = []string{"not-a-hash"}
	if err := Validate(cat, docs); err == nil {
		t.Error("invalid fragment reference accepted")
	}
	r.Fields["area"][0].In = All
	r.API = append(r.API, r.API[0]) // every fragment covered twice
	if err := Validate(cat, docs); err == nil || !strings.Contains(err.Error(), "covered by 2") {
		t.Errorf("double coverage accepted: %v", err)
	}
}

func TestWrite(t *testing.T) {
	d, _, _, _ := buildDemo(t)
	cat, docs, err := Build(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	if err := Write(out, cat, docs); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"catalog.json", "capability.schema.json", "capabilities/os-demo.json", "capabilities/opnsense.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func js(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func controllerParam(name string) controller.Param { return controller.Param{Name: name} }
