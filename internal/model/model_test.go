package model

import "testing"

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
}
