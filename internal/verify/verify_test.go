package verify

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

func TestNewClientRefusesOtherHosts(t *testing.T) {
	allowed := []string{"127.0.0.1", "localhost"}
	for _, u := range []string{"https://192.0.2.1", "https://192.0.2.1:443", "https://127.0.0.1.evil:1", "http://127.0.0.1:18443"} {
		if _, err := NewClient(u, "k", "s", allowed); err == nil {
			t.Errorf("%s must be refused", u)
		}
	}
	if _, err := NewClient("https://127.0.0.1:18443", "k", "s", allowed); err != nil {
		t.Error(err)
	}
}

func TestWriteValue(t *testing.T) {
	var read any
	_ = json.Unmarshal([]byte(`{
		"enabled": "1",
		"iface": {"lan": {"value": "LAN", "selected": 1}, "wan": {"value": "WAN", "selected": 0}, "opt1": {"value": "OPT1", "selected": 1}},
		"verb": [{"value": "0", "selected": 0}, {"value": "3", "selected": 1}],
		"option_data": {"domain_name": "", "routers": {"": {"value": "None", "selected": 1}}},
		"%iface": "LAN, OPT1"
	}`), &read)
	got := writeValue(read)
	want := map[string]any{
		"enabled":     "1",
		"iface":       "lan,opt1",
		"verb":        "1",
		"option_data": map[string]any{"domain_name": "", "routers": ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v", got)
	}
}

func TestCompareNode(t *testing.T) {
	n := &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "enabled", Kind: model.KindField, Shape: model.ShapeString},
		{Name: "mode", Kind: model.KindField, Shape: model.ShapeSelect},
		{Name: "missing", Kind: model.KindField, Shape: model.ShapeString},
	}}
	var v any
	_ = json.Unmarshal([]byte(`{"enabled": "1", "mode": [{"value": "a", "selected": 1}], "extra": "x", "%enabled": "Yes"}`), &v)
	var out []string
	compareNode(n, v, "", &out)
	want := []string{"missing: missing in response", "extra: not in model"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("findings %v, want %v", out, want)
	}
}

func TestApplyAccepted(t *testing.T) {
	rep := &Report{Release: "26.1.6", Results: []*Result{
		{Path: "/api/x/get", Check: "reachable", Status: "error"},
		{Path: "/api/x/get", Check: "schema", Status: "mismatch"},
		{Path: "/api/y/get", Check: "reachable", Status: "error"},
	}}
	n := ApplyAccepted(rep, []Accepted{
		{Path: "/api/x/get", Check: "reachable", Releases: "26.1.*", Reason: "upstream 500", Evidence: "VM run"},
		{Path: "/api/y/get", Check: "reachable", Releases: "26.7*", Reason: "other series", Evidence: "x"},
	})
	if n != 1 || rep.Results[0].Status != "accepted" || rep.Results[1].Status != "mismatch" || rep.Results[2].Status != "error" {
		t.Errorf("accepted: n=%d %+v %+v %+v", n, rep.Results[0], rep.Results[1], rep.Results[2])
	}
}

func TestCompareNodeEmptyModel(t *testing.T) {
	var out []string
	compareNode(&model.Node{Kind: model.KindContainer}, []any{}, "", &out)
	if len(out) != 0 {
		t.Errorf("empty model as [] must be fine: %v", out)
	}
}
