// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
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
	// props: the entries also have a write-only "secret" and an ordered
	// list "servers" (separator ","); the flags break what the data says
	props       bool
	leakGet     bool // get returns the secret
	searchShows bool // search returns the secret (OPNsense 26.1.8 and later)
	sortList    bool // the list is read back sorted
}

// orderedSelect encodes a select map with its members in this order
// (encoding/json sorts map keys).
type orderedSelect []string

func (o orderedSelect) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteString("{")
	for i, k := range o {
		if i > 0 {
			b.WriteString(",")
		}
		kj, _ := json.Marshal(k)
		fmt.Fprintf(&b, `%s:{"value":%s,"selected":1}`, kj, kj)
	}
	b.WriteString("}")
	return []byte(b.String()), nil
}

func (f *fakeInstance) template() map[string]any {
	iface := map[string]any{"": map[string]any{"value": "none", "selected": float64(1)}}
	for k, v := range f.options {
		iface[k] = map[string]any{"value": v, "selected": float64(0)}
	}
	t := map[string]any{"enabled": "1", "hostname": "", "server": "", "iface": iface, "description": ""}
	if f.props {
		t["secret"], t["servers"] = "", orderedSelect{""}
	}
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
		if k == "servers" {
			l := orderedSelect(strings.Split(v, ","))
			if f.sortList {
				slices.Sort(l)
			}
			t[k] = l
			continue
		}
		if k == "secret" && !f.leakGet {
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
	case p == "/api/x/settings/searchHost":
		var rows []any
		for id, e := range f.entries {
			row := map[string]any{"uuid": id, "hostname": e["hostname"]}
			if f.searchShows {
				row["secret"] = e["secret"]
			}
			rows = append(rows, row)
		}
		reply(map[string]any{"rows": rows, "total": len(rows), "rowCount": len(rows), "current": 1})
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
	if f.props {
		entry.Children = append(entry.Children,
			&model.Node{Name: "secret", Kind: model.KindField, Type: "UpdateOnlyTextField", BaseType: "TextField", GetEmpty: true},
			&model.Node{Name: "servers", Kind: model.KindField, Type: "NetworkField", BaseType: "BaseSetField", Shape: model.ShapeSelect,
				List: &model.List{Separator: ",", Ordered: true}})
	}
	f2 := &family{get: ep("getHost", ir.KindItemGet), add: ep("addHost", ir.KindItemAdd), set: ep("setHost", ir.KindItemSet), del: ep("delHost", ir.KindItemDel),
		search: ep("searchHost", ir.KindItemSearch), entry: entry}
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

// Claim (DEV-1, GEN-3 of the review of 2026-11-24): the write cycle
// writes an ordered list field with two elements out of sorted order and
// reads them back in that order, gives a write-only field a value and
// checks that get returns it empty, and records whether search shows it.
// Breaking each makes the cycle say so.
func TestWriteCycleListOrderAndWriteOnly(t *testing.T) {
	newF := func() *fakeInstance {
		return &fakeInstance{entries: map[string]map[string]string{}, zones: map[string]string{}, options: map[string]string{"lan": "LAN"}, props: true}
	}
	f := newF()
	r := hostCycle(t, f)
	if r.Status != "ok" || !slices.Contains(r.Steps, "list-order") || len(r.ReturnedBySearch) != 0 {
		t.Fatalf("status %s, steps %v, findings %v, search %v", r.Status, r.Steps, r.Findings, r.ReturnedBySearch)
	}
	if slices.Contains(r.Generated, "secret") || slices.Contains(r.RuntimeRequired, "secret") {
		t.Errorf("the write-only value is ours, not a generated or required one: %v %v", r.Generated, r.RuntimeRequired)
	}
	f = newF()
	f.sortList = true
	if r := hostCycle(t, f); r.Status != "mismatch" || !strings.Contains(strings.Join(r.Findings, " "), "list order of servers") {
		t.Errorf("sorted list: status %s, findings %v", r.Status, r.Findings)
	}
	f = newF()
	f.leakGet = true
	if r := hostCycle(t, f); r.Status != "mismatch" || !strings.Contains(strings.Join(r.Findings, " "), "write-only field secret returned by get") {
		t.Errorf("get shows the secret: status %s, findings %v", r.Status, r.Findings)
	}
	f = newF()
	f.searchShows = true
	if r := hostCycle(t, f); r.Status != "ok" || !reflect.DeepEqual(r.ReturnedBySearch, []string{"secret"}) {
		t.Errorf("search shows the secret: status %s, returned by search %v", r.Status, r.ReturnedBySearch)
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
	type store = map[string]string
	// handlers: what the set endpoint does with the stored values and the
	// request; nil answer = store and say "saved"
	for _, c := range []struct {
		name    string
		handle  func(st store, sent store) map[string]any
		status  Status
		finding string
		steps   int
	}{
		{"stores", nil, "ok", "", 4},
		// the case writing the current value back could not detect
		{"saved but payload ignored", func(st, sent store) map[string]any { return map[string]any{"result": "saved"} }, "mismatch", "read-back after set: general.enabled", 0},
		{"saved but another value stored", func(st, sent store) map[string]any {
			for k := range sent {
				st[k] = "x"
			}
			return map[string]any{"result": "saved"}
		}, "mismatch", "read-back after set", 0},
		{"own field refused, next field written", func(st, sent store) map[string]any {
			if v, ok := sent["enabled"]; ok && v != st["enabled"] {
				return map[string]any{"result": "failed", "validations": map[string]any{"x.general.enabled": "no"}}
			}
			maps.Copy(st, sent)
			return nil
		}, "ok", "", 4},
		{"model invalid by default", func(st, sent store) map[string]any {
			return map[string]any{"result": "failed", "validations": map[string]any{"x.general.other": "A value is required."}}
		}, "skipped", "not valid in its default state, set rejected for: general.other", 0},
		{"restore refused", func(st, sent store) map[string]any {
			if sent["enabled"] == "1" && st["enabled"] == "0" {
				return map[string]any{"result": "failed", "validations": map[string]any{"x.general.enabled": "no"}}
			}
			maps.Copy(st, sent)
			return nil
		}, "mismatch", "original value of general.enabled was rejected", 0},
		// cloudflared: the token is required; setting it would make the
		// model valid and the restore of "" fail, so nothing is changed
		{"model valid only with a value we would set", func(st, sent store) map[string]any {
			if v, ok := sent["name"]; !ok || v == "" {
				if st["name"] == "" && v == "" {
					return map[string]any{"result": "failed", "validations": map[string]any{"x.general.name": "A value is required."}}
				}
			}
			maps.Copy(st, sent)
			return nil
		}, "skipped", "not valid in its default state, set rejected for: general.name", 0},
		{"not a configuration save", func(st, sent store) map[string]any { return map[string]any{"status": "ok"} }, "skipped", "does not save configuration", 0},
		{"answers a file", func(st, sent store) map[string]any { return map[string]any{"file": true} }, "skipped", "does not save configuration", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store{"name": "", "enabled": "1"}
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case get.Path:
					_ = json.NewEncoder(w).Encode(map[string]any{"x": map[string]any{"general": map[string]any{"name": st["name"], "enabled": st["enabled"]}}})
				case set.Path:
					var body map[string]map[string]store
					_ = json.NewDecoder(r.Body).Decode(&body)
					sent := body["x"]["general"]
					var ans map[string]any
					if c.handle != nil {
						ans = c.handle(st, sent)
					} else {
						maps.Copy(st, sent)
					}
					if ans == nil {
						ans = map[string]any{"result": "saved"}
					}
					if ans["file"] == true {
						w.Header().Set("Content-Type", "application/octet-stream")
						_, _ = w.Write([]byte("PK\x03\x04"))
						return
					}
					_ = json.NewEncoder(w).Encode(ans)
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
			if c.steps > 0 && (len(r.Steps) != c.steps || st["enabled"] != "1" || st["name"] != "") {
				t.Errorf("steps %v, store after the check %v (want the original values back)", r.Steps, st)
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
		// Kea on 26.x: the entry lists are resources of their own, so
		// leaving them out is no difference of the settings resource
		{"only entry lists left out", map[string]any{"dhcpv4": map[string]any{"general": map[string]any{"enabled": "1"}}}, subset, StatusOK},
		{"only entry lists left out, no mark", map[string]any{"dhcpv4": map[string]any{"general": map[string]any{"enabled": "1"}}}, nil, StatusOK},
		{"section with fields left out", map[string]any{"dhcpv4": map[string]any{"subnets": map[string]any{}}}, nil, StatusMismatch},
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

// Claim: the root key the data names is checked as data. No gap explains
// an answer without it (os-freeradius client/get answered {"client": ...}
// while the data said "Client", and override-not-derived turned that into
// known-gap on 25.7.11, 26.1.11 and 26.7.9), while a gap still explains
// differences inside it. Without a derived key nothing is compared
// (skipped), and a settings write-back whose get lacks the key is a
// mismatch, not "no plain field to write back".
func TestRootKeyIsData(t *testing.T) {
	m := &model.Model{Class: `OPNsense\Freeradius\Client`, Root: &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer, Children: []*model.Node{
			{Name: "enabled", Kind: model.KindField, Type: "BooleanField", BaseType: "BooleanField", Shape: model.ShapeString},
		}},
	}}}
	broad := []gap.Gap{gap.New(gap.OverrideNotDerived, "own get action")}
	serve := func(t *testing.T, answer any) *Client {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if answer == nil {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write([]byte("PK\x03\x04"))
				return
			}
			_ = json.NewEncoder(w).Encode(answer)
		}))
		t.Cleanup(srv.Close)
		cl, err := NewClient(srv.URL, "k", "s", []string{"127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		cl.HTTP = srv.Client()
		return cl
	}
	good := map[string]any{"client": map[string]any{"general": map[string]any{"enabled": "1"}}}
	for _, c := range []struct {
		name    string
		wrapper string
		answer  any
		status  Status
	}{
		{"named key missing, broad gap", "Client", good, StatusMismatch},
		{"named key, answer not JSON, broad gap", "Client", nil, StatusMismatch},
		{"named key present, content differs, broad gap", "client", map[string]any{"client": map[string]any{}}, StatusKnownGap},
		{"named key present, content matches", "client", good, StatusOK},
		{"no key derived", "", good, StatusSkipped},
		{"no key derived, answer not JSON", "", nil, StatusKnownGap},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := &ir.Endpoint{Path: "/api/freeradius/client/get", Kind: ir.KindModelGet, Wrapper: c.wrapper, Gaps: broad}
			r := knownGap(checkModelGet(serve(t, c.answer), e, m), e)
			if r.Status != c.status {
				t.Errorf("status %s, want %s; findings %v", r.Status, c.status, r.Findings)
			}
		})
	}
	for _, c := range []struct {
		name   string
		answer any
		status Status
	}{
		{"get lacks the key", good, StatusMismatch},
		{"model without fields arrives as []", map[string]any{"Client": []any{}}, StatusSkipped},
	} {
		t.Run("write-back: "+c.name, func(t *testing.T) {
			get := &ir.Endpoint{Path: "/api/freeradius/client/get", Kind: ir.KindModelGet, Wrapper: "Client"}
			set := &ir.Endpoint{Path: "/api/freeradius/client/set", Kind: ir.KindModelSet, Wrapper: "Client"}
			r := settingsWriteBack(serve(t, c.answer), get, set, m)
			if r.Status != c.status {
				t.Errorf("status %s, want %s; findings %v", r.Status, c.status, r.Findings)
			}
		})
	}
}

// Claim: a settings write that does not read back is known-gap, not ok
// and not a failure, when the data already says the writes go through own
// code (custom-save on the set endpoint, as os-stunnel's save override, or
// on the model, as the setup wizard's init); otherwise a mismatch.
func TestSettingsWriteCustomSaveIsKnownGap(t *testing.T) {
	m := &model.Model{Class: `OPNsense\X\X`, Root: &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer, Children: []*model.Node{
			{Name: "enabled", Kind: model.KindField, Type: "BooleanField", BaseType: "BooleanField"},
		}},
	}}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/x/settings/get" {
			_ = json.NewEncoder(w).Encode(map[string]any{"x": map[string]any{"general": map[string]any{"enabled": "0"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "saved"}) // computes enabled itself
	}))
	defer srv.Close()
	cl, err := NewClient(srv.URL, "k", "s", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	cl.HTTP = srv.Client()
	get := &ir.Endpoint{Path: "/api/x/settings/get", Kind: ir.KindModelGet, Wrapper: "x"}
	plain := &ir.Endpoint{Path: "/api/x/settings/set", Kind: ir.KindModelSet, Wrapper: "x"}
	own := *plain
	own.Gaps = []gap.Gap{gap.New(gap.CustomSave, "overrides save")}
	if r := settingsWriteBack(cl, get, plain, m); r.Status != StatusMismatch {
		t.Errorf("without a gap: %s %v", r.Status, r.Findings)
	}
	if r := settingsWriteBack(cl, get, &own, m); r.Status != StatusKnownGap {
		t.Errorf("custom-save on set: %s %v", r.Status, r.Findings)
	}
	mg := *m
	mg.Gaps = []gap.Gap{gap.New(gap.CustomSave, "overrides init")}
	if r := settingsWriteBack(cl, get, plain, &mg); r.Status != StatusKnownGap {
		t.Errorf("custom-save on the model: %s %v", r.Status, r.Findings)
	}
}
