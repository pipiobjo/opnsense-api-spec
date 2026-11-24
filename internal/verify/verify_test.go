// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

func TestNewClientRefusesOtherHosts(t *testing.T) {
	allowed := []string{"127.0.0.1", "localhost"}
	for _, u := range []string{"https://192.0.2.1", "https://192.0.2.1:443", "https://127.0.0.1.evil:1", "http://127.0.0.1:18443",
		// userinfo that looks like an allowed host: the real host is 192.0.2.1
		"https://127.0.0.1:18443@192.0.2.1", "https://127.0.0.1:x@192.0.2.1/", "https://localhost@fw.example.com:443",
		"https://127.0.0.1:18443/@192.0.2.1", "https://127.0.0.1:18443?x=@192.0.2.1", "https://127.0.0.1:18443#@192.0.2.1"} {
		if _, err := NewClient(u, "k", "s", allowed); err == nil {
			t.Errorf("%s must be refused", u)
		}
	}
	for _, u := range []string{"https://127.0.0.1:18443", "https://127.0.0.1:18443/", "https://localhost", "https://localhost:8443"} {
		if _, err := NewClient(u, "k", "s", allowed); err != nil {
			t.Error(err)
		}
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
	var diffs []shapeDiff
	compareNode(n, v, "", &diffs)
	out := messages(diffs)
	want := []string{"missing: missing in response", "extra: not in model"}
	if !reflect.DeepEqual(out, want) {
		t.Errorf("findings %v, want %v", out, want)
	}
}

// Claim: the shape check reports a value of the wrong JSON type, once, at
// its path: a string field that comes as a number, a UUID map (entry list)
// that comes as a string, a container that comes as a string. The valid
// answer, including the empty forms PHP sends for an empty list, has no
// finding, so each case differs from it in exactly one value.
func TestCompareNodeTypeErrors(t *testing.T) {
	n := &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer, Children: []*model.Node{
			{Name: "name", Kind: model.KindField, Shape: model.ShapeString},
		}},
		{Name: "hosts", Kind: model.KindArray},
		{Name: "empty", Kind: model.KindArray},
	}}
	valid := `{"general": {"name": "a"}, "hosts": {"0b5e...": {}}, "empty": []}`
	for _, c := range []struct {
		name, doc, want string
	}{
		{"valid", valid, ""},
		{"string field with a number", `{"general": {"name": 1}, "hosts": {}, "empty": []}`, "general.name: expected string, got float64"},
		{"UUID map with a string", `{"general": {"name": "a"}, "hosts": "x", "empty": []}`, "hosts: expected UUID map, got string"},
		{"container with a string", `{"general": "x", "hosts": {}, "empty": []}`, "general: expected object, got string"},
	} {
		var v any
		if err := json.Unmarshal([]byte(c.doc), &v); err != nil {
			t.Fatal(err)
		}
		var diffs []shapeDiff
		compareNode(n, v, "", &diffs)
		out := messages(diffs)
		var want []string
		if c.want != "" {
			want = []string{c.want}
		}
		if !reflect.DeepEqual(out, want) {
			t.Errorf("%s: findings %q, want %q", c.name, out, want)
		}
	}
}

// Claim (S8): mismatch and error fail a run, every other status does
// not; Failures, which the CLI, the job summary and the report use, lists
// exactly those results. Found by TestMutantsAreKilled: before this test,
// "a mismatch does not fail the run" survived the whole suite.
func TestFailureRule(t *testing.T) {
	rep := &Report{}
	for _, s := range []Status{StatusOK, StatusAccepted, StatusSkipped, StatusKnownGap, StatusMismatch, StatusError} {
		rep.Results = append(rep.Results, &Result{Path: "/api/" + string(s), Status: s})
	}
	var failed []string
	for _, r := range rep.Failures() {
		failed = append(failed, string(r.Status))
	}
	if want := []string{"mismatch", "error"}; !reflect.DeepEqual(failed, want) {
		t.Errorf("failures %v, want %v", failed, want)
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
	if len(rep.UnusedAccepted) != 0 {
		t.Errorf("an entry for another series is not unused on this one: %v", rep.UnusedAccepted)
	}
}

// Claim (TST-6): an entry with "match" accepts only a failure whose
// findings contain that text; another failure on the same path and check
// stays a failure. Entries that apply to the release but covered nothing
// are reported as unused.
func TestApplyAcceptedMatchAndUnused(t *testing.T) {
	rep := &Report{Release: "25.7.11", Results: []*Result{
		{Path: "/api/u/get", Check: "shape", Status: "error", Findings: []string{"HTTP 500: {}"}},
		{Path: "/api/v/get", Check: "shape", Status: "mismatch", Findings: []string{"general.x: missing in response"}},
		{Path: "/api/w/get", Check: "shape", Status: "ok"},
	}}
	n := ApplyAccepted(rep, []Accepted{
		{Path: "/api/u/get", Check: "shape", Releases: "25.7.*", Match: "HTTP 500", Reason: "r", Evidence: "e"},
		{Path: "/api/v/get", Check: "shape", Releases: "25.7.*", Match: "HTTP 500", Reason: "r", Evidence: "e"},
		{Path: "/api/w/get", Check: "shape", Releases: "*", Reason: "r", Evidence: "e"},
		{Path: "/api/u/get", Check: "shape", Releases: "26.1.*", Reason: "r", Evidence: "e"},
	})
	if n != 1 || rep.Results[0].Status != StatusAccepted || rep.Results[1].Status != StatusMismatch {
		t.Errorf("match: n=%d %s %s", n, rep.Results[0].Status, rep.Results[1].Status)
	}
	want := []string{`/api/v/get shape 25.7.* match "HTTP 500"`, "/api/w/get shape *"}
	if !reflect.DeepEqual(rep.UnusedAccepted, want) {
		t.Errorf("unused %q, want %q", rep.UnusedAccepted, want)
	}
}

// The committed list stays narrow: every entry names the failure it was
// reviewed for.
func TestAcceptedEntriesHaveMatch(t *testing.T) {
	list, err := LoadAccepted("../../verify/accepted.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.Match == "" {
			t.Errorf("%s: no match", a.ID())
		}
	}
}

func TestCompareNodeEmptyModel(t *testing.T) {
	var out []shapeDiff
	compareNode(&model.Node{Kind: model.KindContainer}, []any{}, "", &out)
	if len(out) != 0 {
		t.Errorf("empty model as [] must be fine: %v", out)
	}
}

// Each family of endpoints on one entry list gets its own write cycle
// (bind: primary, secondary and forward domains share get/set/del).
func TestCycleFamilies(t *testing.T) {
	var eps []*ir.Endpoint
	ep := func(action string, kind ir.Kind) {
		eps = append(eps, &ir.Endpoint{Module: "bind", Controller: "domain", Action: action, Path: "/api/bind/domain/" + action,
			Kind: kind, Source: ir.SourceBaseCall, Package: "os-bind", Model: `OPNsense\Bind\Domain`, ModelPath: "domains.domain", Wrapper: "domain"})
	}
	for _, f := range []string{"Primary", "Secondary", "Forward"} {
		ep("add"+f+"Domain", ir.KindItemAdd)
		ep("search"+f+"Domain", ir.KindItemSearch)
	}
	ep("getDomain", ir.KindItemGet)
	ep("setDomain", ir.KindItemSet)
	ep("delDomain", ir.KindItemDel)
	fams, keys := cycleFamilies(&ir.Release{Endpoints: eps}, nil, map[string]bool{"os-bind": true})
	want := []string{"bind/domain domains.domain@forward", "bind/domain domains.domain@primary", "bind/domain domains.domain@secondary"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys %v, want %v", keys, want)
	}
	if a := fams[want[1]].add.Action; a != "addPrimaryDomain" || fams[want[1]].del.Action != "delDomain" || fams[want[1]].get.Action != "getDomain" {
		t.Errorf("primary: add %s del %s get %s", a, fams[want[1]].del.Action, fams[want[1]].get.Action)
	}
}

// The package list is read over HTTP/2, because the OPNsense web server
// sometimes cuts the large firmware/info answer over HTTP/1.1; every
// contract check stays on HTTP/1.1, so that problem stays visible there.
func TestPackageListUsesHTTP2ChecksUseHTTP1(t *testing.T) {
	var mu sync.Mutex
	proto := map[string]int{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proto[r.URL.Path] = r.ProtoMajor
		mu.Unlock()
		if r.URL.Path == "/api/core/firmware/info" {
			_, _ = w.Write([]byte(`{"package":[{"name":"opnsense","installed":"1"}],"plugin":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"product":{"product_version":"26.7.9"}}`))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	c, err := NewClient(srv.URL, "k", "s", []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if pk, err := c.InstalledPackages(); err != nil || !pk["opnsense"] {
		t.Fatalf("InstalledPackages: %v %v", pk, err)
	}
	if _, err := c.InstanceVersion(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if proto["/api/core/firmware/info"] != 2 {
		t.Errorf("firmware/info over HTTP/%d, want 2", proto["/api/core/firmware/info"])
	}
	if proto["/api/core/firmware/status"] != 1 {
		t.Errorf("a contract-test request over HTTP/%d, want 1", proto["/api/core/firmware/status"])
	}
}
