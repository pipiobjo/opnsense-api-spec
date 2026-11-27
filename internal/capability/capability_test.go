// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package capability

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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
	getItem.Wrapper, getItem.Outer = "network", "pac"
	getAll := ep("getNetworks", ir.KindItemGet, "networks.network") // second get, no uuid: an action
	status := &ir.Endpoint{Path: "/api/demo/service/status", Package: "os-demo", Module: "demo", Controller: "service",
		Action: "status", Methods: []string{"GET"}, Kind: ir.KindServiceStatus, Source: ir.SourceBase}
	// a second settings controller on the same model; the class is written
	// in another case
	other := &ir.Endpoint{Path: "/api/demo/general/set", Package: "os-demo", Module: "demo", Controller: "general",
		Action: "set", Methods: []string{"POST"}, Kind: ir.KindModelSet, Source: ir.SourceBase, Model: "OPNsense\\demo\\demo", Wrapper: "demo"}
	otherGet := *other
	otherGet.Path, otherGet.Action, otherGet.Kind, otherGet.Methods = "/api/demo/general/get", "get", ir.KindModelGet, []string{"GET"}
	// the generic set inherited next to an own entry get (acmeclient/accounts):
	// no settings get, so an action
	setOnly := *other
	setOnly.Path, setOnly.Controller = "/api/demo/accounts/set", "accounts"
	// an own get/set whose root key the code does not show (relayd/settings):
	// actions
	noKey := *other
	noKey.Path, noKey.Controller, noKey.Action, noKey.Kind, noKey.Wrapper = "/api/demo/relay/get", "relay", "get", ir.KindModelGet, ""
	noKey.Source, noKey.Gaps = ir.SourceSignature, []gap.Gap{gap.New(gap.OverrideNotDerived, "x")}
	noKeySet := noKey
	noKeySet.Path, noKeySet.Action, noKeySet.Kind = "/api/demo/relay/set", "set", ir.KindModelSet
	return &fragment.Fragment{Package: "os-demo", Models: []*model.Model{m},
		Endpoints: []*ir.Endpoint{add, del, get, getAll, getItem, other, &otherGet, &setOnly, &noKey, &noKeySet, set, status}}
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
	if s := doc.Resources["demo/settings"].SharedWith; len(s) != 1 || s[0] != "demo/general" {
		t.Errorf("demo/settings shared_with = %v", s)
	}
	if s := doc.Resources["demo/general"].SharedWith; len(s) != 1 || s[0] != "demo/settings" {
		t.Errorf("demo/general shared_with = %v", s)
	}
	if s := r.SharedWith; len(s) != 0 {
		t.Errorf("entry list must not share with the settings: %v", s)
	}
	ops := r.API[0].Operations
	if ops["get"] == nil || ops["get"].Path != "/api/demo/settings/getNetwork" || ops["add"] == nil || ops["del"].Gaps[0] != "result-not-returned" {
		t.Errorf("operations: %s", js(ops))
	}
	if g := ops["get"]; g.Wrapper != "network" || g.Outer != "pac" || ops["add"].Wrapper != "network" || ops["del"].Wrapper != "" {
		t.Errorf("per-operation wrapper/outer: %s", js(ops))
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
	// Claim: a settings resource has a get with a root key; a lone set, or
	// a get/set whose key is not derived, is listed as an action
	for _, id := range []string{"demo/accounts", "demo/relay"} {
		if doc.Resources[id] != nil {
			t.Errorf("%s must not be a settings resource: %s", id, js(doc.Resources[id]))
		}
	}
	for _, p := range []string{"/api/demo/accounts/set", "/api/demo/relay/get", "/api/demo/relay/set"} {
		if doc.Actions[p] == nil {
			t.Errorf("%s must be an action: %v", p, keys(doc.Actions))
		}
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
				Steps: []string{"add", "read-back", "set", "read-back-set", "del"}, Sample: map[string]any{"area": "0.0.0.0"},
				ReturnedBySearch: []string{"area"}},
			{Path: "/api/demo/settings/get", Check: "shape", Status: "ok"},
			{Path: "/api/demo/settings/set", Check: "field-probe", Status: "ok", Probes: []verify.Probe{
				{Field: "general.port", Value: "1", Accept: true, Outcome: verify.OutcomeAgreed},
				{Field: "general.port", Value: "0", Accept: false, Outcome: verify.OutcomeAgreed},
				{Field: "general.port", Value: "70000", Accept: false, Outcome: verify.OutcomeContradicted},
				{Field: "general.mode", Value: "x", Accept: true, Outcome: verify.OutcomeOtherFields},
			}},
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
	if v == nil || v.Read != "known-gap" || v.Write != "ok" || len(v.Steps) != 5 || v.Sample["area"] != "0.0.0.0" || v.Composition != c2.ID() ||
		len(v.ReturnedBySearch) != 1 {
		t.Fatalf("entry verification: %s", js(r.Verify))
	}
	if len(r.Verify) != 1 {
		t.Errorf("the older fragment was not tested: %s", js(r.Verify))
	}
	s := docs["os-demo"].Resources["demo/settings"].Verify[v2.Hash()]
	if s == nil || s.Read != "ok" || s.Write != "" {
		t.Errorf("settings: read verified, write not tested: %s", js(s))
	}
	// the firewall's verdicts per field; a rejection for other fields is
	// no verdict on the value
	if s == nil || s.Probe != "ok" || len(s.Probes) != 1 || s.Probes["general.port"] == nil ||
		!slices.Equal(s.Probes["general.port"].Accepted, []string{"1", "70000"}) || !slices.Equal(s.Probes["general.port"].Rejected, []string{"0"}) {
		t.Errorf("settings probes: %s", js(s))
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
	for _, f := range []string{"catalog.json", "capability.schema.json", VersionOrderFile, "capabilities/os-demo.json", "capabilities/opnsense.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Error(err)
		}
	}
}

// The data state has an identity derived from its content: the same data
// gives the same ID (deterministic), catalog.json names it, and a change
// in any file gives another ID.
func TestDataID(t *testing.T) {
	d, _, _, _ := buildDemo(t)
	write := func() (string, string) {
		cat, docs, err := Build(d.dir)
		if err != nil {
			t.Fatal(err)
		}
		out := t.TempDir()
		if err := Write(out, cat, docs); err != nil {
			t.Fatal(err)
		}
		return out, cat.DataID
	}
	out, id := write()
	if ok, _ := regexp.MatchString(`^[0-9a-f]{16}$`, id); !ok {
		t.Fatalf("DataID %q", id)
	}
	if _, id2 := write(); id2 != id {
		t.Errorf("same data, different IDs: %s %s", id, id2)
	}
	var onDisk Catalog
	b, _ := os.ReadFile(filepath.Join(out, "catalog.json"))
	if err := json.Unmarshal(b, &onDisk); err != nil || onDisk.DataID != id {
		t.Errorf("catalog.json data_id %q, want %s (%v)", onDisk.DataID, id, err)
	}
	if got, err := DataIDOf(out); err != nil || got != id {
		t.Errorf("DataIDOf = %q %v, want %s", got, err, id)
	}
	doc := filepath.Join(out, "capabilities", "os-demo.json")
	b, _ = os.ReadFile(doc)
	b[len(b)/2] ^= 1 // same length, one bit different
	if err := os.WriteFile(doc, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := DataIDOf(out); got == id {
		t.Error("a changed document kept the data ID")
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

func TestFamilies(t *testing.T) {
	ep := func(action string) *ir.Endpoint { return &ir.Endpoint{Action: action, Path: "/api/m/c/" + action} }
	pick := func(fs []ir.Family) map[string]map[string]string {
		out := map[string]map[string]string{}
		for _, f := range fs {
			m := map[string]string{}
			for op, e := range f.Chosen {
				m[op] = e.Action
			}
			out[f.Name] = m
		}
		return out
	}
	// bind: three families with their own add and search, shared get/set/del
	var bind []cand
	for _, f := range []string{"Primary", "Secondary", "Forward"} {
		bind = append(bind, cand{ep("add" + f + "Domain"), "add"}, cand{ep("search" + f + "Domain"), "search"})
	}
	bind = append(bind, cand{ep("getDomain"), "get"}, cand{ep("setDomain"), "set"}, cand{ep("delDomain"), "del"})
	got := pick(families(bind))
	want := map[string]map[string]string{
		"primary":   {"add": "addPrimaryDomain", "search": "searchPrimaryDomain", "get": "getDomain", "set": "setDomain", "del": "delDomain"},
		"secondary": {"add": "addSecondaryDomain", "search": "searchSecondaryDomain", "get": "getDomain", "set": "setDomain", "del": "delDomain"},
		"forward":   {"add": "addForwardDomain", "search": "searchForwardDomain", "get": "getDomain", "set": "setDomain", "del": "delDomain"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("bind families:\n got %v\nwant %v", got, want)
	}
	// wol: the shared name decides, the other get is an action
	wol := []cand{{ep("getwake"), "get"}, {ep("getHost"), "get"}, {ep("addHost"), "add"}, {ep("delHost"), "del"}, {ep("setHost"), "set"}}
	if got := pick(families(wol)); got[""]["get"] != "getHost" || len(got) != 1 {
		t.Errorf("wol: %v", got)
	}
	// wireguard: getClientBuilder is an action beside getClient
	wg := []cand{{ep("getClientBuilder"), "get"}, {ep("getClient"), "get"}, {ep("addClient"), "add"}, {ep("setClient"), "set"}}
	if got := pick(families(wg)); got[""]["get"] != "getClient" {
		t.Errorf("wireguard: %v", got)
	}
	// different names per operation: no families, better() picks
	odd := []cand{{ep("addA"), "add"}, {ep("addB"), "add"}, {ep("searchC"), "search"}, {ep("searchD"), "search"}, {ep("getX"), "get"}}
	if got := families(odd); len(got) != 1 || got[0].Name != "" {
		t.Errorf("unrelated names must not become families: %v", pick(got))
	}
	// one endpoint per operation: unchanged
	plain := []cand{{ep("addItem"), "add"}, {ep("getItem"), "get"}}
	if got := pick(families(plain)); !reflect.DeepEqual(got, map[string]map[string]string{"": {"add": "addItem", "get": "getItem"}}) {
		t.Errorf("plain: %v", got)
	}
}

// Write-side marks reach the data: a model that saves through its own code
// and an entry list stored through its own conversion mark the API variant
// (only in the fragment that has them); a converting field type marks the
// field variant. The result validates against the schema.
func TestWriteSideGapsInData(t *testing.T) {
	d := newDist(t)
	v1, v2 := demoFragment("TextField", "old text"), demoFragment("OptionField", "new text.")
	m := v2.Models[0]
	m.Gaps = []gap.Gap{gap.New(gap.CustomSave, "overrides serializeToConfig")}
	entry := m.Find("networks.network")
	entry.Gaps = []gap.Gap{gap.New(gap.CustomSave, "toLegacy")}
	entry.Children[0].Gaps = []gap.Gap{gap.New(gap.CustomWrite, "setValue")}
	core := coreFragment()
	d.compose("26.1.1", "26.1.1", fw1, map[string]*fragment.Fragment{"opnsense": core, "os-demo": v1}, map[string]string{"opnsense": "26.1.1", "os-demo": "1.0"})
	d.compose("26.1.2", "26.1.2_1", fw2, map[string]*fragment.Fragment{"opnsense": core, "os-demo": v2}, map[string]string{"opnsense": "26.1.2_1", "os-demo": "1.1"})
	cat, docs, err := Build(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cat, docs); err != nil {
		t.Fatal(err)
	}
	h1, h2 := v1.Hash(), v2.Hash()
	apiGaps := func(id, hash string) []string {
		for _, a := range docs["os-demo"].Resources[id].API {
			if a.In == All || contains(a.In.([]string), hash) {
				return a.Gaps
			}
		}
		t.Fatalf("%s: no API variant for %s", id, hash)
		return nil
	}
	if g := apiGaps("demo/settings", h2); !reflect.DeepEqual(g, []string{"custom-save"}) {
		t.Errorf("settings in v2: %v", g)
	}
	if g := apiGaps("demo/settings", h1); len(g) != 0 {
		t.Errorf("settings in v1 must have no gaps: %v", g)
	}
	if g := apiGaps("demo/settings#networks.network", h2); !reflect.DeepEqual(g, []string{"custom-save"}) {
		t.Errorf("entries in v2 (model and entry list, once): %v", g)
	}
	var marked bool
	for _, f := range docs["os-demo"].Resources["demo/settings#networks.network"].Fields["area"] {
		if contains(f.Gaps, "custom-write") {
			marked = true
			if f.In == All || contains(f.In.([]string), h1) {
				t.Errorf("custom-write leaked into v1: %+v", f)
			}
		}
	}
	if !marked {
		t.Error("field area not marked custom-write in v2")
	}
}

// The flags reach the data, so consumers need no list of field types.
func TestServerSetWriteOnlyInData(t *testing.T) {
	uid := &model.Node{Name: "uid", Kind: model.KindField, Type: "UniqueIdField", BaseType: "UniqueIdField"}
	pw := &model.Node{Name: "pw", Kind: model.KindField, Type: "UpdateOnlyTextField", BaseType: "TextField", GetEmpty: true, EmptyIgnored: true}
	link := &model.Node{Name: "l", Kind: model.KindField, Type: "LegacyLinkField", BaseType: "LegacyLinkField", Discards: true}
	list := &model.Node{Name: "c", Kind: model.KindField, Type: "AliasContentField", BaseType: "BaseField", List: &model.List{Separator: "\n", Ordered: true}}
	plain := &model.Node{Name: "x", Kind: model.KindField, Type: "TextField", BaseType: "TextField"}
	if f := field(uid); !f.ServerSet || f.WriteOnly {
		t.Errorf("uid: %+v", f)
	}
	if f := field(pw); f.ServerSet || !f.WriteOnly || !f.EmptyIgnored {
		t.Errorf("pw: %+v", f)
	}
	if f := field(link); !f.ServerSet {
		t.Errorf("link: %+v", f)
	}
	if f := field(list); f.List == nil || f.List.Separator != "\n" || !f.List.Ordered {
		t.Errorf("list: %+v", f)
	}
	if f := field(plain); f.ServerSet || f.WriteOnly || f.EmptyIgnored || f.List != nil {
		t.Errorf("plain: %+v", f)
	}
}

// GEN-2 of the review of 2026-11-24: a hotfix revision that was built by an
// older generator is left out when its release has a state built by this
// one, so no client selects a fragment of an older derivation for it. A
// release that has only older states keeps them.
func TestDropStaleCompositions(t *testing.T) {
	d := newDist(t)
	core := coreFragment()
	old, cur := demoFragment("TextField", ""), demoFragment("OptionField", "")
	d.gen = "0.1.0"
	d.compose("26.1.1", "26.1.1", fw1, map[string]*fragment.Fragment{"opnsense": core, "os-demo": old}, map[string]string{"opnsense": "26.1.1", "os-demo": "0.9"})
	stale := d.compose("26.1.2", "26.1.2_1", fw2, map[string]*fragment.Fragment{"opnsense": core, "os-demo": old}, map[string]string{"opnsense": "26.1.2_1", "os-demo": "1.0"})
	d.gen = ir.GeneratorVersion
	d.compose("26.1.2", "26.1.2_3", fw2, map[string]*fragment.Fragment{"opnsense": core, "os-demo": cur}, map[string]string{"opnsense": "26.1.2_3", "os-demo": "1.1"})
	cat, docs, err := Build(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	if cat.Cores["26.1.2_1"] != nil {
		t.Errorf("stale state %s still selectable: %s", stale.ID(), js(cat.Cores))
	}
	if cat.Cores["26.1.1"] == nil {
		t.Error("a release with only older states must keep them")
	}
	for _, r := range cat.Releases {
		for _, c := range r.Compositions {
			if r.Release == "26.1.2" && c.Generator != ir.GeneratorVersion {
				t.Errorf("26.1.2 keeps composition %s of generator %s", c.ID, c.Generator)
			}
		}
	}
	if h := docs["os-demo"].Select[fw2]["1.0"]; h != "" {
		t.Errorf("select still maps the stale package version to %s", h)
	}
	if fr := docs["os-demo"].Fragments[old.Hash()]; fr == nil || len(fr.Releases) != 1 || fr.Releases[0] != "26.1.1" {
		t.Errorf("old fragment must only be seen in 26.1.1: %s", js(fr))
	}
}

// DEV-2/REQ-4 of the review of 2026-11-24: a fragment that was not tested
// gets the result of a tested fragment of the same series where the
// resource is defined identically, marked same_definition_as; a resource
// that differs, or a fragment of another series, does not.
func TestCarryVerification(t *testing.T) {
	d, v1, v2, c2 := buildDemo(t)
	v3 := demoFragment("OptionField", "other UI text") // other hash, same data
	core := coreFragment()
	d.compose("26.7.1", "26.7.1", fw2, map[string]*fragment.Fragment{"opnsense": core, "os-demo": v3}, map[string]string{"opnsense": "26.7.1", "os-demo": "1.2"})
	d.write(filepath.Join("26.1.2", "opnsense-26.1.2.verified.json"), &verify.Report{Release: "26.1.2", Composition: c2.ID(),
		Instance: "26.1.2_1", Generator: ir.GeneratorVersion, Date: "2026-11-15T10:00:00Z", Packages: []string{"opnsense", "os-demo"},
		Results: []*verify.Result{
			{Path: "/api/demo/settings/get", Check: "shape", Status: "ok"},
			{Path: "/api/demo/settings/getNetwork", Check: "shape", Status: "ok"},
		}})
	cat, docs, err := Build(d.dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cat, docs); err != nil {
		t.Fatal(err)
	}
	h1, h2, h3 := v1.Hash(), v2.Hash(), v3.Hash()
	if h3 == h2 {
		t.Fatal("fixture: v3 must be another fragment")
	}
	set := docs["os-demo"].Resources["demo/settings"].Verify
	if v := set[h1]; v == nil || v.SameDefinitionAs != h2 || v.Release != "26.1.2" || v.Read != "ok" {
		t.Errorf("settings in 26.1.1 (same definition, same series): %s", js(set))
	}
	if v := set[h2]; v == nil || v.SameDefinitionAs != "" {
		t.Errorf("the tested fragment keeps its own result: %s", js(set[h2]))
	}
	if set[h3] != nil {
		t.Errorf("another series must not inherit: %s", js(set[h3]))
	}
	ent := docs["os-demo"].Resources["demo/settings#networks.network"].Verify
	if ent[h1] != nil {
		t.Errorf("entries differ in 26.1.1 (area is text there): %s", js(ent[h1]))
	}
}
