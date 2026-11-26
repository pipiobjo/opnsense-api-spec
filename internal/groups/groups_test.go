// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package groups

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
)

// where returns the group of every package ("opnsense" for the core) and
// fails when one is in no group or in two.
func where(t *testing.T, p *Plan, plugins []string) map[string]string {
	t.Helper()
	at := map[string]string{}
	cores := 0
	for _, g := range p.Groups {
		if g.Core {
			cores++
			at[Core] = g.Name
		}
		for _, pl := range g.Plugins {
			if o, ok := at[pl]; ok {
				t.Fatalf("%s in groups %s and %s", pl, o, g.Name)
			}
			at[pl] = g.Name
		}
	}
	if cores != 1 {
		t.Fatalf("the core is in %d groups, want exactly 1", cores)
	}
	for _, pl := range plugins {
		if at[pl] == "" {
			t.Fatalf("%s in no group", pl)
		}
	}
	if len(at) != len(plugins)+1 {
		t.Fatalf("groups hold %d packages, want %d plugins and the core", len(at), len(plugins))
	}
	return at
}

func names(n int) []string {
	var out []string
	for i := range n {
		out = append(out, fmt.Sprintf("os-p%02d", i))
	}
	return out
}

// Claim: every plugin is in exactly one group and the core in exactly
// one; plugins that need each other share a group; the same input gives
// the same split; groups are balanced by measured time where it is known,
// else by count.
func TestMake(t *testing.T) {
	plugins := names(10)
	needs := map[string]map[string]string{
		"os-p01": {"os-p07": "package dependency"},
		"os-p07": {"os-p03": "model relation", Core: "relation to the core is not a constraint"},
	}
	f := File{AutoGroups: 3}
	p, err := Make(f, Input{Release: "26.7.10", Plugins: plugins, Needs: needs}, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := where(t, p, plugins)
	if at["os-p01"] != at["os-p07"] || at["os-p07"] != at["os-p03"] {
		t.Errorf("dependent plugins split: %v", at)
	}
	if p.WeightsFrom != "count" || len(p.Groups) != 3 {
		t.Errorf("weights from %s, %d groups", p.WeightsFrom, len(p.Groups))
	}
	// 11 packages by count over 3 groups: 4/4/3 (the unit of 3 counts 3)
	var sizes []float64
	for _, g := range p.Groups {
		sizes = append(sizes, g.Weight)
	}
	slices.Sort(sizes)
	if !reflect.DeepEqual(sizes, []float64{3, 4, 4}) {
		t.Errorf("count balance %v, want [3 4 4]", sizes)
	}
	if !reflect.DeepEqual(p.All, plugins) {
		t.Errorf("conflict check list %v", p.All)
	}
	if len(p.Together) != 1 || !reflect.DeepEqual(p.Together[0].Packages, []string{"os-p01", "os-p03", "os-p07"}) {
		t.Errorf("together: %+v", p.Together)
	}
	for range 20 {
		again, _ := Make(f, Input{Release: "26.7.10", Plugins: slices.Clone(plugins), Needs: needs}, 0)
		if !reflect.DeepEqual(again, p) {
			t.Fatalf("not deterministic:\n%+v\n%+v", again, p)
		}
	}

	// measured: the core is heavy, so it gets a group (nearly) to itself
	w := map[string]float64{Core: 100}
	for _, pl := range plugins {
		w[pl] = 10
	}
	p, err = Make(f, Input{Release: "26.7.10", Plugins: plugins, Needs: needs, Weights: w}, 0)
	if err != nil {
		t.Fatal(err)
	}
	at = where(t, p, plugins)
	for _, g := range p.Groups {
		if g.Core && len(g.Plugins) != 0 {
			t.Errorf("core group %+v: with the core weighing 100 and 10 plugins of 10, it should hold only the core", g)
		}
	}
	if p.WeightsFrom != "measured" {
		t.Errorf("weights from %s", p.WeightsFrom)
	}
	// partly measured: unknown ones take the median of the known ones
	delete(w, "os-p09")
	p, _ = Make(f, Input{Release: "26.7.10", Plugins: plugins, Weights: w}, 0)
	if p.WeightsFrom != "partly measured" || !strings.Contains(strings.Join(p.Notes, "\n"), "os-p09") {
		t.Errorf("partly measured: %s %v", p.WeightsFrom, p.Notes)
	}
	// auto overrides the file; never more groups than units
	if p, _ = Make(f, Input{Release: "26.7.10", Plugins: plugins}, 2); len(p.Groups) != 2 {
		t.Errorf("auto 2: %d groups", len(p.Groups))
	}
	if p, _ = Make(f, Input{Release: "26.7.10", Plugins: []string{"os-a"}}, 3); len(p.Groups) != 2 {
		t.Errorf("core and one plugin over 3 groups: %d groups, want 2", len(p.Groups))
	}
}

// Claim: a named group keeps its plugins (and what they need) together,
// may take the core, leaves the rest to the balanced groups, is left out
// in a release that has none of its plugins, and cannot share a unit with
// another named group.
func TestMakeNamedGroups(t *testing.T) {
	plugins := names(8)
	needs := map[string]map[string]string{"os-p02": {"os-p06": "package dependency"}}
	f := File{AutoGroups: 2, Groups: []Named{
		{Name: "dns-dhcp", Why: "w", Packages: []string{"os-p02", "os-gone"}, Core: true},
		{Name: "absent", Why: "w", Packages: []string{"os-gone2"}},
	}}
	p, err := Make(f, Input{Release: "26.7.10", Plugins: plugins, Needs: needs}, 0)
	if err != nil {
		t.Fatal(err)
	}
	at := where(t, p, plugins)
	if at["os-p02"] != "dns-dhcp" || at["os-p06"] != "dns-dhcp" || at[Core] != "dns-dhcp" {
		t.Errorf("named group: %v", at)
	}
	// a together set from the file joins balanced plugins, with its reason
	f2 := File{AutoGroups: 3, Together: []Together{{Packages: []string{"os-p00", "os-p05", "os-gone"}, Why: "seen"}}}
	p2, err := Make(f2, Input{Release: "26.7.10", Plugins: plugins}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if at2 := where(t, p2, plugins); at2["os-p00"] != at2["os-p05"] || !strings.Contains(fmt.Sprint(p2.Together), "(groups file: seen)") {
		t.Errorf("together from the file: %v %+v", at2, p2.Together)
	}
	// with the core: the plugin goes where the core is (here a named group)
	f.Together = []Together{{Packages: []string{Core, "os-p07"}, Why: "core sample uses its action"}}
	p3, err := Make(f, Input{Release: "26.7.10", Plugins: plugins, Needs: needs}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if at3 := where(t, p3, plugins); at3["os-p07"] != "dns-dhcp" {
		t.Errorf("together with the core: %v", at3)
	}
	f.Together = nil
	if len(p.Groups) != 3 || p.Groups[0].Name != "dns-dhcp" || !p.Groups[0].Explicit {
		t.Errorf("groups %+v", p.Groups)
	}
	notes := strings.Join(p.Notes, "\n")
	if !strings.Contains(notes, "os-gone") || !strings.Contains(notes, "group absent has nothing to test") {
		t.Errorf("notes %q", notes)
	}
	f.Groups[1] = Named{Name: "other", Why: "w", Packages: []string{"os-p06"}}
	if _, err := Make(f, Input{Release: "26.7.10", Plugins: plugins, Needs: needs}, 0); err == nil || !strings.Contains(err.Error(), "must share a VM") {
		t.Errorf("a unit in two named groups: %v", err)
	}
}

// Claim: dependencies come from the signed catalog (os-* package
// dependencies) and the IR (model relations into another package's
// model, matched as PHP matches class names; actions inherited from
// another package's API controller); needs on the core are no
// constraint; what cannot be decided is a note, not a guess.
func TestDependencies(t *testing.T) {
	rel := &ir.Release{Models: []*model.Model{
		{Class: `OPNsense\HAProxy\HAProxy`, Package: "os-haproxy", Root: &model.Node{}},
		{Class: `OPNsense\Firewall\Alias`, Package: Core, Root: &model.Node{}},
		{Class: `OPNsense\AcmeClient\AcmeClient`, Package: "os-acme-client", Root: &model.Node{Children: []*model.Node{
			{Name: "a", Relations: []model.Relation{{Source: "OPNsense.haproxy.HAPROXY", Items: "x"}}},
			{Name: "b", Children: []*model.Node{{Name: "c", Relations: []model.Relation{
				{Source: "OPNsense.Firewall.Alias", Items: "aliases.alias"},
				{Source: "OPNsense.Gone.Model", Items: "y"}}}}},
		}}},
	}, Endpoints: []*ir.Endpoint{
		// os-web-proxy-sso's service controller extends os-squid's (seen
		// on 25.7.11 to 26.7.10: without os-squid every action answers 500)
		{Path: "/api/proxy/service/fetchacls", Package: "os-squid", Module: "proxy", Controller: "service", DeclaredIn: `OPNsense\Proxy\Api\ServiceController`},
		{Path: "/api/proxysso/service/fetchacls", Package: "os-web-proxy-sso", Module: "proxysso", Controller: "service", DeclaredIn: `OPNsense\Proxy\Api\ServiceController`},
		{Path: "/api/proxysso/service/status", Package: "os-web-proxy-sso", Module: "proxysso", Controller: "service", DeclaredIn: `OPNsense\Base\ApiMutableServiceControllerBase`},
		{Path: "/api/proxysso/service/showkeytab", Package: "os-web-proxy-sso", Module: "proxysso", Controller: "service", DeclaredIn: `OPNsense\ProxySSO\Api\ServiceController`},
		{Path: "/api/firewall/filter/apply", Package: Core, Module: "firewall", Controller: "filter", DeclaredIn: `OPNsense\Firewall\Api\FilterBaseController`},
	}}
	cat := []pkgrepo.Package{
		{Name: "os-OPNProxy", Deps: map[string]pkgrepo.Dep{"os-squid": {}, "os-redis": {}, "php": {}}},
		{Name: "os-acme-client"}, {Name: "os-haproxy"}, {Name: "os-squid"}, {Name: "os-redis"}, {Name: "os-web-proxy-sso"},
	}
	needs, notes := Dependencies(rel, cat, []string{"os-OPNProxy", "os-acme-client", "os-haproxy", "os-squid", "os-web-proxy-sso", "os-new"})
	want := map[string]map[string]string{
		"os-OPNProxy":      {"os-squid": "package dependency", "os-redis": "package dependency"},
		"os-acme-client":   {"os-haproxy": `model relation OPNsense\AcmeClient\AcmeClient -> OPNsense.haproxy.HAPROXY`},
		"os-web-proxy-sso": {"os-squid": `controller proxysso/service inherits from OPNsense\Proxy\Api\ServiceController`},
	}
	if !reflect.DeepEqual(needs, want) {
		t.Errorf("needs %v\nwant  %v", needs, want)
	}
	n := strings.Join(notes, "\n")
	if len(notes) != 3 || !strings.Contains(n, "os-new: not in the package catalog") || !strings.Contains(n, "OPNsense.Gone.Model") ||
		!strings.Contains(n, `FilterBaseController, which has no endpoint of its own`) {
		t.Errorf("notes %q", notes)
	}
	// os-redis is no plugin under test: noted, not a group constraint
	p, err := Make(File{AutoGroups: 3}, Input{Release: "r", Plugins: []string{"os-OPNProxy", "os-acme-client", "os-haproxy", "os-squid"}, Needs: needs}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(p.Notes, "\n"), "os-OPNProxy needs os-redis (package dependency), which is not a plugin under test") {
		t.Errorf("notes %q", p.Notes)
	}
}

// Claim: a package's estimated time is its installation plus its share of
// its group's test time by endpoints; the core has no installation.
func TestWeights(t *testing.T) {
	rel := &ir.Release{}
	for pk, n := range map[string]int{Core: 6, "os-a": 3, "os-b": 1} {
		for range n {
			rel.Endpoints = append(rel.Endpoints, &ir.Endpoint{Package: pk})
		}
	}
	got := Weights(rel, []Run{
		{Core: true, Plugins: []string{"os-a"}, Install: map[string]float64{"os-a": 5}, Test: 90},
		{Plugins: []string{"os-b"}, Install: map[string]float64{"os-b": 4}, Test: 10},
	})
	want := map[string]float64{Core: 60, "os-a": 35, "os-b": 14}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("weights %v, want %v", got, want)
	}
}

// Claim: a groups file that would test a package twice, name the core
// twice, use a reserved name or carry an unknown key is refused.
func TestReadFile(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"ok":              {`{"about":"x","groups":[{"name":"g","why":"w","packages":["os-a"]}]}`, ""},
		"plugin twice":    {`{"groups":[{"name":"g","why":"w","packages":["os-a"]},{"name":"h","why":"w","packages":["os-a"]}]}`, "os-a is in groups g and h"},
		"core twice":      {`{"groups":[{"name":"g","why":"w","packages":["os-a"],"core":true},{"name":"h","why":"w","packages":["os-b"],"core":true}]}`, "both claim the core"},
		"reserved":        {`{"groups":[{"name":"auto-1","why":"w","packages":["os-a"]}]}`, "reserved"},
		"no reason":       {`{"groups":[{"name":"g","packages":["os-a"]}]}`, "needs a name, why"},
		"core by name":    {`{"groups":[{"name":"g","why":"w","packages":["opnsense"]}]}`, "no plugin name"},
		"unknown key":     {`{"groups":[],"group_count":2}`, "unknown field"},
		"no auto group":   {`{"auto_groups":-1}`, "at least 1"},
		"together alone":  {`{"together":[{"packages":["os-a"],"why":"w"}]}`, "at least two packages"},
		"together no why": {`{"together":[{"packages":["os-a","os-b"]}]}`, "at least two packages and why"},
	} {
		path := filepath.Join(t.TempDir(), "groups.json")
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := ReadFile(path)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case c.want == "" && f.AutoGroups != 3:
			t.Errorf("%s: default auto_groups %d, want 3", name, f.AutoGroups)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
		}
	}
	// the repository's own file is valid
	if _, err := ReadFile("../../verify/groups.json"); err != nil {
		t.Error(err)
	}
}
