// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoadAllFixture(t *testing.T) {
	ms, _, err := LoadAll("testdata")
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 {
		t.Fatalf("models = %d", len(ms))
	}
	m := ms[0]
	if m.Class != `OPNsense\Demo\Demo` || m.Mount != "//OPNsense/demo" || m.Version != "1.0.3" {
		t.Errorf("model header: %+v", m)
	}
	check := func(path string, kind Kind, base string, shape Shape) *Node {
		t.Helper()
		n := m.Find(path)
		if n == nil {
			t.Fatalf("%s missing", path)
		}
		if n.Kind != kind || n.BaseType != base || n.Shape != shape {
			t.Errorf("%s: kind=%s base=%s shape=%s", path, n.Kind, n.BaseType, n.Shape)
		}
		return n
	}
	en := check("general.enabled", KindField, "BooleanField", ShapeString)
	if !en.Required || en.Default == nil || *en.Default != "0" {
		t.Errorf("enabled: %+v", en)
	}
	if n := check("general.safesearch", KindField, "BooleanField", ShapeString); n.Default != nil {
		t.Error("safesearch has no default")
	}
	if n := check("general.iface", KindField, "BaseListField", ShapeSelect); !n.Multiple || len(n.Gaps) != 0 {
		t.Errorf("custom field type via FieldTypes: %+v", n)
	}
	check("hosts.host", KindArray, "ArrayField", "")
	if z := m.Find("hosts.host.zone"); z == nil || len(z.Relations) != 1 || z.Relations[0] != (Relation{Source: "OPNsense.Demo.Zones", Items: "zones.zone"}) {
		t.Errorf("relation target not parsed: %+v", z)
	}
	check("hosts.host.hostname", KindField, "BaseSetField", ShapeString)
	mode := check("hosts.host.mode", KindField, "BaseListField", ShapeSelect)
	if len(mode.Options) != 2 || mode.Options[1] != (Option{Value: "b-1", Label: "Mode B", Group: "Grouped"}) {
		t.Errorf("options incl. optgroup: %+v", mode.Options)
	}
	if ttl := check("hosts.host.ttl", KindField, "IntegerField", ShapeString); ttl.Props["MinimumValue"] != "1" {
		t.Errorf("props: %+v", ttl.Props)
	}
	if odd := check("hosts.host.odd", KindField, "", ShapeUnknown); len(odd.Gaps) != 1 {
		t.Errorf("unknown type must be a gap: %+v", odd)
	}
	// a typed child is a field even if its name equals a setting tag
	check("hosts.host.filters", KindField, "TextField", ShapeString)
	// an unknown untyped leaf element is reported on its parent, not a field
	host := m.Find("hosts.host")
	if m.Find("hosts.host.NewUpstreamKeyword") != nil || len(host.Gaps) != 1 || host.Gaps[0].Code != "unknown-setting" {
		t.Errorf("unknown setting: %+v", host.Gaps)
	}
}

// Write-side conversion is read from the PHP classes: a field type that
// overrides setValue (also a core one, but not BaseField itself), an entry
// list whose container class has toLegacy/fromLegacy (two classes in one
// file), a model class that overrides serializeToConfig.
func TestWriteSideGaps(t *testing.T) {
	ms, _, err := LoadAll("testdata")
	if err != nil {
		t.Fatal(err)
	}
	m := ms[0]
	codes := func(n *Node) []string {
		var out []string
		for _, g := range n.Gaps {
			out = append(out, string(g.Code))
		}
		return out
	}
	want := map[string][]string{
		"general.stored":    {"custom-write"}, // plugin field type with setValue
		"general.mac":       {"custom-write"}, // core field type with setValue
		"general.commented": nil,              // setValue only in comments
		"general.enabled":   nil,              // BooleanField: only BaseField's own setValue
		"general.port":      nil,
		"legacy.entry":      {"custom-save"}, // container class with toLegacy/fromLegacy
		"plain.entry":       nil,             // ArrayField subclass without storage code
	}
	for path, w := range want {
		n := m.Find(path)
		if n == nil {
			t.Fatalf("%s missing", path)
		}
		if got := codes(n); !reflect.DeepEqual(got, w) {
			t.Errorf("%s: gaps %v, want %v", path, got, w)
		}
	}
	if len(m.Gaps) != 2 || m.Gaps[0].Code != "custom-save" || !strings.Contains(m.Gaps[0].Detail, "serializeToConfig") ||
		m.Gaps[1].Code != "custom-save" || !strings.Contains(m.Gaps[1].Detail, "init") {
		t.Errorf("model gaps %+v", m.Gaps)
	}
}

// Which fields the firewall sets itself, shows empty on get or does not
// clear with "" is derived from the field classes (fixtures copied from
// 26.7.9: UpdateOnlyTextField, ApiKeyField, StoreB64Field, LegacyLinkField),
// not from a list of type names, and published in the capability data.
func TestServerSetAndWriteOnly(t *testing.T) {
	ms, _, err := LoadAll("testdata")
	if err != nil {
		t.Fatal(err)
	}
	m := ms[0]
	for path, want := range map[string][3]bool{ // server-set, write-only, empty-ignored
		"general.uid":     {true, false, false},
		"general.secret":  {false, true, true},   // __toString "", setValue only if != ""
		"general.apikeys": {false, true, true},   // plugin class, guards: SimpleXMLElement && ..., !empty
		"general.b64":     {false, false, false}, // __toString decodes; setValue('') at the end clears
		"general.link":    {true, false, false},  // setValue { return; }: every write discarded
		"general.mac":     {false, false, false},
		"general.enabled": {false, false, false},
	} {
		n := m.Find(path)
		if n == nil {
			t.Fatalf("%s missing", path)
		}
		if got := [3]bool{n.ServerSet(), n.WriteOnly(), n.EmptyIgnored}; got != want {
			t.Errorf("%s: server-set, write-only, empty-ignored = %v, want %v", path, got, want)
		}
	}
	if g := m.Find("general.link").Gaps; len(g) != 0 {
		t.Errorf("a discarding setValue is no conversion: gaps %v", g)
	}
	var none *Node
	if none.ServerSet() || none.WriteOnly() {
		t.Error("a missing node is neither")
	}
}

// Which fields hold a list, with which separator, and whether the firewall
// keeps the order written (DEV-1 of the review of 2026-11-24).
func TestListFields(t *testing.T) {
	ms, _, err := LoadAll("testdata")
	if err != nil {
		t.Fatal(err)
	}
	m := ms[0]
	for path, want := range map[string]*List{
		"general.iface":   {Separator: ",", Ordered: false}, // multiple option field: option order
		"general.peers":   {Separator: ",", Ordered: true},  // multiple relation with Sorted
		"general.csv":     {Separator: ",", Ordered: true},  // $internalAsList = true in the class
		"general.servers": {Separator: ";", Ordered: true},  // AsList Y, FieldSeparator from the model
		"general.content": {Separator: "\n", Ordered: true}, // getNodeData splits on $this->separatorchar
		"general.nets":    {Separator: ",", Ordered: true},  // getNodeData splits on a literal
		"general.single":  nil,                              // set field without AsList: one value
		"general.secret":  nil,
		"hosts.host.mode": nil,
	} {
		n := m.Find(path)
		if n == nil {
			t.Fatalf("%s missing", path)
		}
		if !reflect.DeepEqual(n.List, want) {
			t.Errorf("%s: list %+v, want %+v", path, n.List, want)
		}
		if want != nil && path != "general.iface" && path != "general.peers" && (n.Shape != ShapeSelect || len(n.Gaps) != 0) {
			t.Errorf("%s: read shape %s gaps %v, want select without gaps", path, n.Shape, n.Gaps)
		}
	}
}
