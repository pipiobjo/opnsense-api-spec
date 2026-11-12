// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package openapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/controller"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// fixture app: one Demo module with settings + service controllers
func build(t *testing.T) (*ir.Release, M) {
	t.Helper()
	ms, _, err := model.LoadAll("testdata/app")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := controller.LoadAll("testdata/app")
	if err != nil {
		t.Fatal(err)
	}
	rel := &ir.Release{Version: "0.0.1", Models: ms, Endpoints: ir.Build(ms, cs)}
	doc, _ := Render(rel)
	// round-trip through JSON to work on plain maps
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var out M
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return rel, out
}

func dig(t *testing.T, v any, keys ...string) any {
	t.Helper()
	for _, k := range keys {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("not an object at %q", k)
		}
		v, ok = m[k]
		if !ok {
			t.Fatalf("missing key %q", k)
		}
	}
	return v
}

func endpoint(rel *ir.Release, path string) *ir.Endpoint {
	for _, e := range rel.Endpoints {
		if e.Path == path {
			return e
		}
	}
	return nil
}

func TestClassification(t *testing.T) {
	rel, _ := build(t)
	for path, want := range map[string]ir.Kind{
		"/api/demo/settings/get":        ir.KindModelGet,
		"/api/demo/settings/set":        ir.KindModelSet,
		"/api/demo/settings/searchHost": ir.KindItemSearch,
		"/api/demo/settings/addHost":    ir.KindItemAdd,
		"/api/demo/settings/delHost":    ir.KindItemDel,
		"/api/demo/settings/export":     ir.KindCustom,
		"/api/demo/settings/dyn":        ir.KindCustom,
		"/api/demo/service/reconfigure": ir.KindServiceAction,
		"/api/demo/service/status":      ir.KindServiceStatus,
		"/api/demo/accounts/get":        ir.KindItemGet, // getAction($uuid) wrapping getBase
		"/api/demo/accounts/update":     ir.KindItemSet,
		"/api/demo/accounts/set":        ir.KindModelSet, // inherited, untouched
	} {
		e := endpoint(rel, path)
		if e == nil {
			t.Errorf("%s missing", path)
			continue
		}
		if e.Kind != want {
			t.Errorf("%s: kind %s, want %s", path, e.Kind, want)
		}
	}
	if e := endpoint(rel, "/api/demo/settings/delHost"); e.ModelPath != "hosts.host" || len(e.Gaps) != 1 {
		t.Errorf("delHost must pick the primary path and report the extra call: %+v", e)
	}
	if e := endpoint(rel, "/api/demo/settings/get"); len(e.Gaps) != 1 || e.Source != ir.SourceBase {
		t.Errorf("overridden get must stay typed but carry a gap: %+v", e)
	}
	if e := endpoint(rel, "/api/demo/settings/export"); len(e.Methods) != 1 || e.Methods[0] != "GET" {
		t.Errorf("export method from isGet(): %+v", e.Methods)
	}
	if e := endpoint(rel, "/api/demo/settings/addHost"); e.Apply != "/api/demo/service/reconfigure" {
		t.Errorf("apply endpoint: %q", e.Apply)
	}
}

func TestRenderWrappersAndSchemas(t *testing.T) {
	_, doc := build(t)
	paths := dig(t, doc, "paths").(map[string]any)

	set := dig(t, paths, "/api/demo/settings/set", "post", "requestBody", "content", "application/json", "schema", "properties")
	if _, ok := set.(map[string]any)["demo"]; !ok {
		t.Errorf("set must wrap with $internalModelName 'demo', got %v", set)
	}
	add := dig(t, paths, "/api/demo/settings/addHost", "post", "requestBody", "content", "application/json", "schema", "properties", "host", "$ref")
	if add != "#/components/schemas/Demo.Demo.hosts.host.Write" {
		t.Errorf("addHost body: %v", add)
	}
	if dig(t, paths, "/api/demo/settings/addHost", "post", "x-opnsense-apply") != "/api/demo/service/reconfigure" {
		t.Error("x-opnsense-apply missing")
	}
	for _, p := range []string{"/api/demo/settings/getHost", "/api/demo/settings/getHost/{uuid}"} {
		if _, ok := paths[p]; !ok {
			t.Errorf("path variant %s missing", p)
		}
	}
	search := paths["/api/demo/settings/searchHost"].(map[string]any)
	if _, ok := search["get"]; !ok {
		t.Error("search must allow GET")
	}
	if _, ok := search["post"]; !ok {
		t.Error("search must allow POST")
	}

	schemas := dig(t, doc, "components", "schemas").(map[string]any)
	w := schemas["Demo.Demo.hosts.host.Write"].(map[string]any)
	if req := w["required"].([]any); len(req) != 1 || req[0] != "hostname" {
		t.Errorf("required on add: %v", w["required"])
	}
	mode := dig(t, w, "properties", "mode").(map[string]any)
	if enum := mode["enum"].([]any); len(enum) != 3 || enum[0] != "" || enum[2] != "b-1" {
		t.Errorf("write enum incl. empty for optional: %v", enum)
	}
	r := dig(t, schemas, "Demo.Demo.hosts.host.Read", "properties", "mode", "allOf").([]any)
	if r[0].(map[string]any)["$ref"] != "#/components/schemas/SelectMap" {
		t.Errorf("list field read shape: %v", r)
	}
	// nested sections are named components
	if ref := dig(t, schemas, "Demo.Demo.Write", "properties", "general", "$ref"); ref != "#/components/schemas/Demo.Demo.general.Write" {
		t.Errorf("nested section must be a named component: %v", ref)
	}
	safe := dig(t, schemas, "Demo.Demo.general.Write", "properties", "safesearch").(map[string]any)
	// optional boolean: "" when never set
	if e := safe["enum"].([]any); len(e) != 3 || e[0] != "" || e[1] != "0" {
		t.Errorf("boolean enum: %v", e)
	}
	en := dig(t, schemas, "Demo.Demo.general.Write", "properties", "enabled").(map[string]any)
	if e := en["enum"].([]any); len(e) != 2 || e[0] != "0" {
		t.Errorf("required boolean enum: %v", e)
	}
	derivation := dig(t, paths, "/api/demo/settings/dyn", "get", "x-opnsense-derivation").(map[string]any)
	if derivation["source"] != "signature-only" || len(derivation["gaps"].([]any)) == 0 {
		t.Errorf("untyped action must be marked: %v", derivation)
	}
	for name, goName := range map[string]any{"agentPort": nil, "agent_port": "agent_port2"} {
		p := dig(t, schemas, "Demo.Demo.hosts.host.Write", "properties", name).(map[string]any)
		if p["x-go-name"] != goName || p["x-opnsense-name-collision"] == nil {
			t.Errorf("collision %s not marked: %v", name, p)
		}
	}
	if _, ok := dig(t, schemas, "Demo.Demo.hosts.host.Read").(map[string]any)["patternProperties"]; !ok {
		t.Error("display values must be described by patternProperties")
	}
	// entry list in a model read: named union members
	hosts := dig(t, schemas, "Demo.Demo.hosts.Read", "properties", "host", "oneOf").([]any)
	if hosts[0].(map[string]any)["$ref"] != "#/components/schemas/Demo.Demo.hosts.host.Entries" ||
		hosts[1].(map[string]any)["$ref"] != "#/components/schemas/EmptyList" {
		t.Errorf("entry list union members: %v", hosts)
	}
	// signature-only must stay untyped even when the kind is known
	dyn := dig(t, paths, "/api/demo/settings/dyn", "get", "responses", "200", "content", "application/json", "schema").(map[string]any)
	if len(dyn) != 0 {
		t.Errorf("signature-only response must be untyped: %v", dyn)
	}
	if dig(t, doc, "info", "license", "identifier") != "BSD-2-Clause" ||
		!strings.Contains(dig(t, doc, "x-opnsense-notice").(string), "Deciso B.V.") {
		t.Error("licence and OPNsense notice must travel with the document")
	}
	if dig(t, doc, "openapi") != "3.1.0" {
		t.Error("openapi version")
	}
}

func TestRenderFragmentUsesCommon(t *testing.T) {
	rel, _ := build(t)
	frag, _ := RenderFragment(rel, "os-demo", "0123456789abcdef", "../common/c1.openapi.json")
	b, _ := json.Marshal(frag)
	var doc M
	_ = json.Unmarshal(b, &doc)
	schemas := dig(t, doc, "components", "schemas").(map[string]any)
	if _, ok := schemas["MutationResult"]; ok {
		t.Error("shared schemas must not be defined in a fragment")
	}
	ref := dig(t, doc, "paths", "/api/demo/settings/set", "post", "responses", "200", "content", "application/json", "schema", "$ref")
	if ref != "../common/c1.openapi.json#/components/schemas/MutationResult" {
		t.Errorf("shared schema ref: %v", ref)
	}
	if dig(t, doc, "x-opnsense-fragment", "hash") != "0123456789abcdef" {
		t.Error("fragment identity missing")
	}
	common := RenderCommon("test")
	cs := common["components"].(M)["schemas"].(M)
	for _, n := range CommonSchemas {
		if _, ok := cs[n]; !ok {
			t.Errorf("common document lacks %s", n)
		}
	}
}
