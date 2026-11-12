// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package controller

import (
	"os"
	"strings"
	"testing"
)

func load(t *testing.T) map[string]*Controller {
	t.Helper()
	classes := map[string]*Class{}
	for _, f := range []string{"Base.php", "SettingsController.php", "ServiceController.php"} {
		src, err := os.ReadFile("testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range ParseFile(string(src), f) {
			classes[strings.ToLower(c.Name)] = c
		}
	}
	out := map[string]*Controller{}
	for _, c := range Resolve(classes) {
		out[c.Module+"/"+c.Name] = c
	}
	return out
}

func action(c *Controller, name string) *Action {
	for _, a := range c.Actions {
		if a.Name == name {
			return a
		}
	}
	return nil
}

func TestResolveSettings(t *testing.T) {
	cs := load(t)
	c := cs["demo/settings"]
	if c == nil {
		t.Fatalf("demo/settings not found, have %v", cs)
	}
	if c.ModelName != "demo" || c.ModelClass != `OPNsense\Demo\Demo` {
		t.Errorf("model: %q %q", c.ModelName, c.ModelClass)
	}
	if len(c.Gaps) != 0 {
		t.Errorf("unexpected gaps: %v", c.Gaps)
	}
	if action(c, "hidden") != nil {
		t.Error("action inside a # comment must be ignored")
	}
	if a := action(c, "set"); a == nil || a.DeclaredIn != `OPNsense\Base\ApiMutableModelControllerBase` || !a.ChecksPost {
		t.Errorf("inherited set: %+v", a)
	}
	g := action(c, "get")
	if g == nil || !g.CallsParent || g.Overrides != `OPNsense\Base\ApiMutableModelControllerBase` {
		t.Errorf("overridden get: %+v", g)
	}
	gh := action(c, "getHost")
	if gh == nil || len(gh.Params) != 1 || gh.Params[0] != (Param{Name: "uuid", Optional: true}) {
		t.Errorf("getHost params: %+v", gh)
	}
	if gh.Calls[0] != (BaseCall{Func: "getBase", Key: "host", Path: "hosts.host", Direct: true}) {
		t.Errorf("getHost call: %+v", gh.Calls)
	}
	if a := action(c, "addHost"); a.Calls[0].Key != "host" || a.Calls[0].Path != "hosts.host" {
		t.Errorf("double-quoted literals: %+v", a.Calls)
	}
	if a := action(c, "delHost"); len(a.Calls) != 2 || a.Calls[1].Path != "hosts.host" {
		t.Errorf("delHost calls: %+v", a.Calls)
	}
	if a := action(c, "getPac"); a.Calls[0].Outer != "pac" || a.Calls[0].Key != "match" {
		t.Errorf("outer wrapper: %+v", a.Calls)
	}
	if a := action(c, "getHost"); a.Calls[0].Outer != "" || !a.Calls[0].Direct {
		t.Errorf("no outer wrapper expected: %+v", a.Calls)
	}
	if a := action(c, "dyn"); a.Calls[0].Raw == "" {
		t.Errorf("non-literal argument must be kept raw: %+v", a.Calls)
	}
	if a := action(c, "export"); !a.ChecksGet || a.ChecksPost || len(a.Calls) != 0 {
		t.Errorf("export: %+v", a)
	}
}

func TestResolveService(t *testing.T) {
	c := load(t)["demo/service"]
	if c == nil || action(c, "reconfigure") == nil || action(c, "status") == nil {
		t.Fatalf("service actions missing: %+v", c)
	}
}

func TestSnake(t *testing.T) {
	for in, want := range map[string]string{
		"CtrlAgent": "ctrl_agent", "Dhcpv4": "dhcpv4", "Leases4": "leases4",
		"DNat": "d_nat", "HasyncStatus": "hasync_status", "IDS": "ids", "Settings": "settings",
	} {
		if got := snake(in); got != want {
			t.Errorf("snake(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripCommentsKeepsStrings(t *testing.T) {
	in := "a = 'http://x'; // c\nb = \"# no\"; /* x\ny */ c"
	got := StripComments(in)
	if !strings.Contains(got, "'http://x'") || !strings.Contains(got, `"# no"`) || strings.Contains(got, "// c") || strings.Contains(got, "x\ny") {
		t.Errorf("StripComments = %q", got)
	}
	if strings.Count(got, "\n") != strings.Count(in, "\n") {
		t.Error("line count must be preserved")
	}
}
