// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/controller"
	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

func TestCandidates(t *testing.T) {
	sel := map[string]any{
		"":    map[string]any{"value": "none", "selected": float64(1)},
		"opt": map[string]any{"value": "OPT", "selected": float64(0)},
		"lan": map[string]any{"value": "LAN", "selected": float64(0)},
	}
	tests := []struct {
		name string
		n    *model.Node
		tmpl any
		want []string
	}{
		{"options offered by the instance", &model.Node{Type: "InterfaceField", BaseType: "BaseListField"}, sel, []string{"lan", "opt"}},
		{"list without offered options uses static options", &model.Node{Type: "OptionField", BaseType: "BaseListField", Options: []model.Option{{Value: ""}, {Value: "a"}}}, "", []string{"a"}},
		{"relation without objects has no value", &model.Node{Type: "ModelRelationField", BaseType: "BaseListField"}, "", nil},
		{"integer within bounds", &model.Node{BaseType: "IntegerField", Props: map[string]string{"MinimumValue": "5", "MaximumValue": "50"}}, "", []string{"5", "10", "50"}},
		{"network with mask required", &model.Node{Type: "NetworkField", BaseType: "BaseSetField", Props: map[string]string{"NetMaskRequired": "Y", "AddressFamily": "ipv4"}}, "", []string{"192.0.2.0/24"}},
		{"address without mask, v6", &model.Node{Type: "NetworkField", BaseType: "BaseSetField", Props: map[string]string{"NetMaskAllowed": "N", "AddressFamily": "ipv6"}}, "", []string{"2001:db8::10"}},
		{"text filtered by mask", &model.Node{Type: "TextField", BaseType: "TextField", Props: map[string]string{"Mask": `/^([\-\+\~\=]){1,2}$/`}}, "", []string{"=", "-"}},
		{"hostname without IP", &model.Node{Type: "HostnameField", BaseType: "BaseSetField", Props: map[string]string{"IpAllowed": "N"}}, "", []string{"ct1.example.com", "ct1"}},
	}
	for _, tc := range tests {
		if got := candidates(tc.n, tc.tmpl); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestCandidatesUseDocumentationAddressesOnly(t *testing.T) {
	ip := regexp.MustCompile(`\b\d+\.\d+\.\d+\.\d+\b`)
	for _, typ := range []string{"TextField", "NetworkField", "HostnameField", "CSVListField"} {
		for _, c := range candidates(&model.Node{Type: typ, BaseType: "BaseSetField"}, nil) {
			for _, a := range ip.FindAllString(c, -1) {
				if !strings.HasPrefix(a, "192.0.2.") {
					t.Errorf("%s candidate %q uses %s outside 192.0.2.0/24", typ, c, a)
				}
			}
			if strings.Contains(c, ":") && strings.Count(c, ":") > 1 && !strings.HasPrefix(c, "2001:db8:") && !strings.HasPrefix(c, "02:00:") {
				t.Errorf("%s candidate %q: IPv6 outside 2001:db8::/32", typ, c)
			}
		}
	}
}

func TestPHPRegex(t *testing.T) {
	if re := phpRegex(`/^.{1,255}$/u`); re == nil || !re.MatchString("x") {
		t.Error("simple PCRE pattern must compile")
	}
	if re := phpRegex(`/^https?:\/\/.*$/i`); re == nil || !re.MatchString("HTTPS://example.com") {
		t.Error("case-insensitive flag must be kept")
	}
	if re := phpRegex(`/^(?!any$)[a-z]{1,32}$/u`); re != nil {
		t.Error("look-ahead cannot be expressed in Go and must yield no filter")
	}
	if got := filterMask([]string{"a", "b"}, `/^(?!any$)[a-z]+$/`); len(got) != 2 {
		t.Error("an uncompilable mask must not filter anything out")
	}
}

func TestSameValue(t *testing.T) {
	if !sameValue("b,a", "a,b") || sameValue("a", "b") || !sameValue("", "") {
		t.Error("lists compare as sets, scalars exactly")
	}
}

// fakeInstance behaves like an OPNsense entry controller for one entry
// list ("host"): required fields are validated, unknown keys are ignored
// silently, and list fields are read back as select maps.
type fakeInstance struct {
	mu        sync.Mutex
	entries   map[string]map[string]string
	options   map[string]string // offered interface options; empty = none
	dropOnSet string            // field the server silently ignores on set
	rejectSet bool              // every set fails with a cross-field validation
	needZone  bool              // hosts must refer to an existing zone
	lockable  bool              // entries have lock=1 by default and refuse deletion while locked
	zones     map[string]string // uuid -> name
}

func (f *fakeInstance) template() map[string]any {
	iface := map[string]any{"": map[string]any{"value": "none", "selected": float64(1)}}
	for k, v := range f.options {
		iface[k] = map[string]any{"value": v, "selected": float64(0)}
	}
	t := map[string]any{"enabled": "1", "hostname": "", "server": "", "iface": iface, "description": ""}
	if f.lockable {
		t["lock"] = "1"
	}
	if f.needZone {
		zone := map[string]any{"": map[string]any{"value": "none", "selected": float64(1)}}
		for k, v := range f.zones {
			zone[k] = map[string]any{"value": v, "selected": float64(0)}
		}
		t["zone"] = zone
	}
	return t
}

func (f *fakeInstance) read(e map[string]string) map[string]any {
	t := f.template()
	for k, v := range e {
		if k == "iface" || k == "zone" {
			m := t[k].(map[string]any)
			for key, o := range m {
				o.(map[string]any)["selected"] = float64(0)
				if key == v {
					o.(map[string]any)["selected"] = float64(1)
				}
			}
			continue
		}
		t[k] = v
	}
	return t
}

func (f *fakeInstance) validate(e map[string]string) map[string]any {
	v := map[string]any{}
	if !regexp.MustCompile(`^[a-z0-9]{1,32}$`).MatchString(e["hostname"]) {
		v["host.hostname"] = "invalid hostname"
	}
	if !strings.HasPrefix(e["server"], "192.0.2.") || strings.Contains(e["server"], "/") {
		v["host.server"] = "need an address"
	}
	if _, ok := f.options[e["iface"]]; !ok {
		v["host.iface"] = "pick an interface"
	}
	if _, ok := f.zones[e["zone"]]; f.needZone && !ok {
		v["host.zone"] = "A value is required."
	}
	return v
}

func (f *fakeInstance) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	body := func() map[string]string {
		var in map[string]map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		out := map[string]string{}
		for k, v := range in["host"] {
			if s, ok := v.(string); ok && k != "unknown" {
				out[k] = s
			}
		}
		return out
	}
	p := r.URL.Path
	zone := func() map[string]any {
		var in map[string]map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		return in["zone"]
	}
	switch {
	case p == "/api/x/zones/getZone":
		reply(map[string]any{"zone": map[string]any{"name": ""}})
	case p == "/api/x/zones/addZone":
		z := zone()
		name, _ := z["name"].(string)
		if name == "" {
			reply(map[string]any{"result": "failed", "validations": map[string]any{"zone.name": "required"}})
			return
		}
		f.zones["z1"] = name
		reply(map[string]any{"result": "saved", "uuid": "z1"})
	case strings.HasPrefix(p, "/api/x/zones/delZone/"):
		delete(f.zones, strings.TrimPrefix(p, "/api/x/zones/delZone/"))
		reply(map[string]any{"result": "deleted"})
	case p == "/api/x/settings/getHost":
		reply(map[string]any{"host": f.template()})
	case p == "/api/x/settings/addHost":
		e := body()
		if v := f.validate(e); len(v) > 0 {
			reply(map[string]any{"result": "failed", "validations": v})
			return
		}
		f.entries["u1"] = e
		reply(map[string]any{"result": "saved", "uuid": "u1"})
	case strings.HasPrefix(p, "/api/x/settings/getHost/"):
		reply(map[string]any{"host": f.read(f.entries[strings.TrimPrefix(p, "/api/x/settings/getHost/")])})
	case strings.HasPrefix(p, "/api/x/settings/setHost/") && f.rejectSet:
		reply(map[string]any{"result": "failed", "validations": map[string]any{"host.server": "does not match the other field"}})
	case strings.HasPrefix(p, "/api/x/settings/setHost/"):
		e := f.entries[strings.TrimPrefix(p, "/api/x/settings/setHost/")]
		for k, v := range body() {
			if k != f.dropOnSet {
				e[k] = v
			}
		}
		reply(map[string]any{"result": "saved"})
	case strings.HasPrefix(p, "/api/x/settings/delHost/") && f.lockable && f.entries[strings.TrimPrefix(p, "/api/x/settings/delHost/")]["lock"] == "1":
		w.WriteHeader(500)
		reply(map[string]any{"errorMessage": "Interface locked, unset lock first before removal", "errorTitle": "locked"})
	case strings.HasPrefix(p, "/api/x/settings/delHost/"):
		delete(f.entries, strings.TrimPrefix(p, "/api/x/settings/delHost/"))
		reply(map[string]any{"result": "deleted"})
	default:
		http.NotFound(w, r)
	}
}

func hostCycle(t *testing.T, f *fakeInstance) *Result {
	t.Helper()
	srv := httptest.NewTLSServer(f)
	defer srv.Close()
	c, err := NewClient(srv.URL, "k", "s", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = srv.Client()
	ep := func(action string, kind ir.Kind) *ir.Endpoint {
		return &ir.Endpoint{Path: "/api/x/settings/" + action, Kind: kind, Wrapper: "host", ModelPath: "hosts.host"}
	}
	entry := &model.Node{Kind: model.KindArray, Children: []*model.Node{
		{Name: "enabled", Kind: model.KindField, Type: "BooleanField", BaseType: "BooleanField"},
		{Name: "hostname", Kind: model.KindField, Type: "HostnameField", BaseType: "BaseSetField", Props: map[string]string{"IpAllowed": "N"}},
		{Name: "server", Kind: model.KindField, Type: "NetworkField", BaseType: "BaseSetField", Required: true},
		{Name: "iface", Kind: model.KindField, Type: "InterfaceField", BaseType: "BaseListField", Shape: model.ShapeSelect},
		{Name: "description", Kind: model.KindField, Type: "DescriptionField", BaseType: "DescriptionField"},
		{Name: "zone", Kind: model.KindField, Type: "ModelRelationField", BaseType: "BaseListField", Shape: model.ShapeSelect,
			Relations: []model.Relation{{Source: "test.zones", Items: "zones.zone"}}}, // case as in real models
	}}
	f2 := &family{get: ep("getHost", ir.KindItemGet), add: ep("addHost", ir.KindItemAdd), set: ep("setHost", ir.KindItemSet), del: ep("delHost", ir.KindItemDel), entry: entry}
	zep := func(action string, kind ir.Kind) *ir.Endpoint {
		return &ir.Endpoint{Path: "/api/x/zones/" + action, Kind: kind, Wrapper: "zone", ModelPath: "zones.zone"}
	}
	zones := &family{get: zep("getZone", ir.KindItemGet), add: zep("addZone", ir.KindItemAdd), del: zep("delZone", ir.KindItemDel),
		entry: &model.Node{Kind: model.KindArray, Children: []*model.Node{{Name: "name", Kind: model.KindField, Type: "TextField", BaseType: "TextField"}}}}
	w := &writer{c: c, byList: map[string]*family{listKey(`Test\Zones`, "zones.zone"): zones}}
	return w.cycle(f2)
}

func TestWriteCycleGeneratesValues(t *testing.T) {
	f := &fakeInstance{entries: map[string]map[string]string{}, zones: map[string]string{}, options: map[string]string{"lan": "LAN", "opt1": "OPT1"}}
	r := hostCycle(t, f)
	if r.Status != "ok" {
		t.Fatalf("status %s, findings %v", r.Status, r.Findings)
	}
	if want := []string{"hostname", "iface", "server"}; !reflect.DeepEqual(r.Generated, want) {
		t.Errorf("generated %v, want %v", r.Generated, want)
	}
	if want := []string{"hostname", "iface", "server"}; !reflect.DeepEqual(r.RuntimeRequired, want) {
		t.Errorf("runtime-required %v, want %v", r.RuntimeRequired, want)
	}
	if r.Sample["hostname"] != "ct1" || r.Sample["server"] != "192.0.2.10" || r.Sample["iface"] != "lan" {
		t.Errorf("sample %v", r.Sample)
	}
	if len(f.entries) != 0 {
		t.Errorf("entry not deleted: %v", f.entries)
	}
	if want := []string{"add", "read-back", "set", "read-back-set", "del"}; !reflect.DeepEqual(r.Steps, want) {
		t.Errorf("steps %v, want %v", r.Steps, want)
	}
	// hostname and iface are not marked Required in the model: a note,
	// which must not fail the check
	if len(r.Findings) != 0 || len(r.Notes) != 1 || !strings.Contains(r.Notes[0], "hostname, iface") {
		t.Errorf("findings %v, notes %v", r.Findings, r.Notes)
	}
}

func TestWriteCycleDetectsIgnoredField(t *testing.T) {
	f := &fakeInstance{entries: map[string]map[string]string{}, zones: map[string]string{}, options: map[string]string{"lan": "LAN"}, dropOnSet: "description"}
	r := hostCycle(t, f)
	if r.Status != "mismatch" {
		t.Fatalf("a field the server ignored must be a mismatch, got %s %v", r.Status, r.Findings)
	}
	found := false
	for _, s := range r.Findings {
		found = found || strings.HasPrefix(s, "read-back after set: description")
	}
	if !found {
		t.Errorf("findings %v", r.Findings)
	}
}

func TestWriteCycleWithoutPrerequisiteIsSkipped(t *testing.T) {
	f := &fakeInstance{entries: map[string]map[string]string{}, zones: map[string]string{}, options: map[string]string{}}
	r := hostCycle(t, f)
	if r.Status != "skipped" || !strings.Contains(strings.Join(r.Findings, " "), "no accepted value for: iface") {
		t.Errorf("status %s, findings %v", r.Status, r.Findings)
	}
	if r.Sample != nil {
		t.Error("a skipped cycle has no sample")
	}
}

func TestWriteCycleUpdateRejectedIsNotVerified(t *testing.T) {
	f := &fakeInstance{entries: map[string]map[string]string{}, zones: map[string]string{}, options: map[string]string{"lan": "LAN"}, rejectSet: true}
	r := hostCycle(t, f)
	if r.Status != "ok" {
		t.Fatalf("a rejected changed value is not a failure of the endpoint: %s %v", r.Status, r.Findings)
	}
	for _, s := range r.Steps {
		if s == "set" {
			t.Fatalf("update must not count as tested: steps %v", r.Steps)
		}
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "update not tested") {
		t.Errorf("notes %v", r.Notes)
	}
}

func TestEntryGet(t *testing.T) {
	add := &ir.Endpoint{Path: "/api/wol/wol/addHost", Wrapper: "host"}
	wake := &ir.Endpoint{Path: "/api/wol/wol/getwake", Wrapper: "wake"}
	host := &ir.Endpoint{Path: "/api/wol/wol/getHost", Wrapper: "host", Params: []controller.Param{{Name: "uuid", Optional: true}}}
	if got := entryGet([]*ir.Endpoint{host, wake}, add); got != host {
		t.Errorf("got %v", got.Path)
	}
	if got := entryGet([]*ir.Endpoint{wake, host}, add); got != host {
		t.Errorf("order must not matter, got %v", got.Path)
	}
}

func TestWriteCycleCreatesPrerequisite(t *testing.T) {
	f := &fakeInstance{entries: map[string]map[string]string{}, zones: map[string]string{}, options: map[string]string{"lan": "LAN"}, needZone: true}
	r := hostCycle(t, f)
	if r.Status != "ok" {
		t.Fatalf("status %s, findings %v", r.Status, r.Findings)
	}
	if r.Sample["zone"] != "z1" {
		t.Errorf("relation not filled with the prerequisite: %v", r.Sample)
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "prerequisite entry created via /api/x/zones/addZone") {
		t.Errorf("prerequisite must be visible: %v", r.Notes)
	}
	if len(f.zones) != 0 || len(f.entries) != 0 {
		t.Errorf("left behind: zones %v, entries %v", f.zones, f.entries)
	}
}

func TestWriteCycleUnlocksBeforeDelete(t *testing.T) {
	f := &fakeInstance{entries: map[string]map[string]string{}, zones: map[string]string{}, options: map[string]string{"lan": "LAN"}, lockable: true}
	r := hostCycle(t, f)
	if r.Status != "ok" || len(f.entries) != 0 {
		t.Fatalf("status %s, left %v, findings %v", r.Status, f.entries, r.Findings)
	}
	if !strings.Contains(strings.Join(r.Notes, " "), "delete refused while lock=1") {
		t.Errorf("the unlock must be visible: %v", r.Notes)
	}
}

// The four outcomes of writing a settings field back unchanged.
func TestSettingsWriteBack(t *testing.T) {
	m := &model.Model{Class: `OPNsense\X\X`, Root: &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer, Children: []*model.Node{
			{Name: "name", Kind: model.KindField, Type: "TextField", BaseType: "TextField"},
			{Name: "enabled", Kind: model.KindField, Type: "BooleanField", BaseType: "BooleanField"},
		}},
	}}}
	get := &ir.Endpoint{Path: "/api/x/settings/get", Kind: ir.KindModelGet, Wrapper: "x"}
	set := &ir.Endpoint{Path: "/api/x/settings/set", Kind: ir.KindModelSet, Wrapper: "x"}
	for _, c := range []struct {
		name    string
		answer  map[string]any
		stores  bool
		status  Status
		finding string
	}{
		{"saved and unchanged", map[string]any{"result": "saved"}, true, "ok", ""},
		{"saved but not stored", map[string]any{"result": "saved"}, false, "mismatch", "read-back after set: general.enabled"},
		{"own value rejected", map[string]any{"result": "failed", "validations": map[string]any{"x.general.enabled": "no"}}, true, "mismatch", "its own current value"},
		{"model invalid by default", map[string]any{"result": "failed", "validations": map[string]any{"x.general.name": "A value is required."}}, true, "skipped", "not valid in its default state, set rejected for: general.name"},
		{"not a configuration save", map[string]any{"status": "ok"}, true, "skipped", "does not save configuration"},
		{"answers a file", nil, true, "skipped", "does not save configuration"},
	} {
		t.Run(c.name, func(t *testing.T) {
			enabled := "1"
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case get.Path:
					_ = json.NewEncoder(w).Encode(map[string]any{"x": map[string]any{"general": map[string]any{"name": "", "enabled": enabled}}})
				case set.Path:
					if !c.stores {
						enabled = "0"
					}
					if c.answer == nil {
						w.Header().Set("Content-Type", "application/octet-stream")
						_, _ = w.Write([]byte("PK\x03\x04"))
						return
					}
					_ = json.NewEncoder(w).Encode(c.answer)
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			cl, err := NewClient(srv.URL, "k", "s", []string{"127.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			cl.HTTP = srv.Client()
			r := settingsWriteBack(cl, get, set, m)
			if r.Status != c.status || (c.finding != "" && !strings.Contains(strings.Join(r.Findings, " "), c.finding)) {
				t.Errorf("status %s, findings %v", r.Status, r.Findings)
			}
			if r.Check != "write-cycle" || r.Path != set.Path {
				t.Errorf("result recorded as %s on %s", r.Check, r.Path)
			}
		})
	}
}

// checkModelGet compares a settings answer with the model: a field
// missing, a field in another shape (a list returned as a plain string)
// and a key the model does not have are findings; a matching answer is ok.
func TestCheckModelGetShape(t *testing.T) {
	m := &model.Model{Class: `OPNsense\X\X`, Root: &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer, Children: []*model.Node{
			{Name: "enabled", Kind: model.KindField, Type: "BooleanField", BaseType: "BooleanField", Shape: model.ShapeString},
			{Name: "iface", Kind: model.KindField, Type: "InterfaceField", BaseType: "BaseListField", Shape: model.ShapeSelect},
		}},
	}}}
	get := &ir.Endpoint{Path: "/api/x/settings/get", Kind: ir.KindModelGet, Wrapper: "x"}
	good := map[string]any{"enabled": "1", "iface": map[string]any{"lan": map[string]any{"value": "LAN", "selected": 1}}}
	for _, c := range []struct {
		name    string
		general map[string]any
		status  Status
		finding string
	}{
		{"matches", good, "ok", ""},
		{"field missing", map[string]any{"iface": good["iface"]}, "mismatch", "general.enabled: missing"},
		{"list as plain string", map[string]any{"enabled": "1", "iface": "lan"}, "mismatch", "general.iface"},
		{"unknown key", map[string]any{"enabled": "1", "iface": good["iface"], "extra": "x"}, "mismatch", "general.extra"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"x": map[string]any{"general": c.general}})
			}))
			defer srv.Close()
			cl, err := NewClient(srv.URL, "k", "s", []string{"127.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			cl.HTTP = srv.Client()
			r := checkModelGet(cl, get, m)
			if r.Status != c.status || (c.finding != "" && !strings.Contains(strings.Join(r.Findings, " "), c.finding)) {
				t.Errorf("status %s, findings %v", r.Status, r.Findings)
			}
		})
	}
}

// Claim (TST-7): on an endpoint whose only mark is a Subset gap (the
// action calls the generic get and leaves sections out, as kea/dhcpv4/get
// does with subnets and reservations), a shape difference is known-gap only
// when the gap explains it: a whole entry list or container missing, a key
// added. A missing field (a rename), a type error or a missing root key is
// a mismatch. Marks that explain any difference (custom-logic) keep the
// broad tolerance, also when combined with a Subset gap.
func TestKnownGapSubsetOnlyExplainsSections(t *testing.T) {
	m := &model.Model{Class: `OPNsense\Kea\KeaDhcpv4`, Root: &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer, Children: []*model.Node{
			{Name: "enabled", Kind: model.KindField, Shape: model.ShapeString},
		}},
		{Name: "subnets", Kind: model.KindContainer, Children: []*model.Node{{Name: "subnet4", Kind: model.KindArray}}},
		{Name: "reservations", Kind: model.KindArray},
	}}}
	subset := []gap.Gap{gap.New(gap.OverrideCallsParent, "overrides getAction and calls the parent")}
	broad := []gap.Gap{gap.New(gap.CustomLogic, "custom logic")}
	for _, c := range []struct {
		name   string
		answer any
		gaps   []gap.Gap
		status Status
	}{
		{"sections left out, key added", map[string]any{"dhcpv4": map[string]any{"general": map[string]any{"enabled": "1", "this_hostname": "fw"}}}, subset, StatusKnownGap},
		{"field missing", map[string]any{"dhcpv4": map[string]any{"general": map[string]any{"enable": "1"}}}, subset, StatusMismatch},
		{"type error", map[string]any{"dhcpv4": map[string]any{"general": map[string]any{"enabled": 1}}}, subset, StatusMismatch},
		{"section with another type", map[string]any{"dhcpv4": map[string]any{"general": map[string]any{"enabled": "1"}, "reservations": "x"}}, subset, StatusMismatch},
		{"root key missing", map[string]any{"result": "x"}, subset, StatusMismatch},
		{"field missing, broad mark", map[string]any{"dhcpv4": map[string]any{"general": map[string]any{}}}, broad, StatusKnownGap},
		{"field missing, both marks", map[string]any{"dhcpv4": map[string]any{"general": map[string]any{}}}, append(append([]gap.Gap{}, subset...), broad...), StatusKnownGap},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(c.answer)
			}))
			defer srv.Close()
			cl, err := NewClient(srv.URL, "k", "s", []string{"127.0.0.1"})
			if err != nil {
				t.Fatal(err)
			}
			cl.HTTP = srv.Client()
			e := &ir.Endpoint{Path: "/api/kea/dhcpv4/get", Kind: ir.KindModelGet, Wrapper: "dhcpv4", Gaps: c.gaps}
			r := knownGap(checkModelGet(cl, e, m), e)
			if r.Status != c.status {
				t.Errorf("status %s, want %s; findings %v", r.Status, c.status, r.Findings)
			}
		})
	}
}
