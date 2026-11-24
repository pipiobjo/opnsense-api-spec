// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package ir

import (
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/controller"
	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// The decision table of classify, one row per rule: how an action becomes
// an endpoint kind, source and gaps. The golden hashes check the result on
// real releases; this names each rule.
func TestClassify(t *testing.T) {
	entry := &model.Node{Name: "host", Kind: model.KindArray}
	m := &model.Model{Class: `OPNsense\Demo\Demo`, Root: &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "hosts", Kind: model.KindContainer, Children: []*model.Node{entry}},
		{Name: "general", Kind: model.KindContainer},
	}}}
	call := func(fn, path string, direct bool) controller.BaseCall {
		return controller.BaseCall{Func: fn, Key: "host", Path: path, Direct: direct}
	}
	const own = `OPNsense\Demo\Api\SettingsController`
	for _, c := range []struct {
		name   string
		action controller.Action
		kind   Kind
		source Source
		gaps   []gap.Code
	}{
		{"generic model get", controller.Action{Name: "get", DeclaredIn: baseModelCtl}, KindModelGet, SourceBase, nil},
		{"generic model set", controller.Action{Name: "set", DeclaredIn: baseModelCtl}, KindModelSet, SourceBase, nil},
		{"model get overridden without the parent", controller.Action{Name: "get", DeclaredIn: own, Overrides: baseModelCtl},
			KindModelGet, SourceSignature, []gap.Code{gap.OverrideNotDerived}},
		{"model get overridden, calls the parent", controller.Action{Name: "get", DeclaredIn: own, Overrides: baseModelCtl, CallsParent: true},
			KindModelGet, SourceBase, []gap.Code{gap.OverrideCallsParent}},
		{"service status", controller.Action{Name: "status", DeclaredIn: baseServiceCtl}, KindServiceStatus, SourceBase, nil},
		{"thin add wrapper", controller.Action{Name: "addHost", DeclaredIn: own, Lines: 3,
			Calls: []controller.BaseCall{call("addBase", "hosts.host", true)}}, KindItemAdd, SourceBaseCall, nil},
		{"add with custom logic", controller.Action{Name: "addHost", DeclaredIn: own, Lines: 20,
			Calls: []controller.BaseCall{call("addBase", "hosts.host", true)}}, KindItemAdd, SourceBaseCall, []gap.Code{gap.CustomLogic}},
		{"delete without return", controller.Action{Name: "delHost", DeclaredIn: own, Lines: 3,
			Calls: []controller.BaseCall{{Func: "delBase", Path: "hosts.host", NoReturn: true}}}, KindItemDel, SourceBaseCall, []gap.Code{gap.ResultNotReturned}},
		{"get whose result is wrapped", controller.Action{Name: "getHost", DeclaredIn: own, Lines: 3,
			Calls: []controller.BaseCall{call("getBase", "hosts.host", false)}}, KindItemGet, SourceBaseCall, []gap.Code{gap.PostProcessed}},
		{"add whose result is wrapped", controller.Action{Name: "addHost", DeclaredIn: own, Lines: 3,
			Calls: []controller.BaseCall{call("addBase", "hosts.host", false)}}, KindItemAdd, SourceBaseCall, []gap.Code{gap.PostProcessed}},
		{"helper on a path the model lacks", controller.Action{Name: "addHost", DeclaredIn: own, Lines: 3,
			Calls: []controller.BaseCall{call("addBase", "nothing.here", true)}}, KindItemAdd, SourceBaseCall, []gap.Code{gap.ModelPathMissing}},
		{"custom action", controller.Action{Name: "doSomething", DeclaredIn: own, Lines: 10}, KindCustom, SourceSignature, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := c.action
			ctl := &controller.Controller{Class: own, Module: "demo", Name: "settings", ModelName: "demo",
				ModelClass: m.Class, Actions: []*controller.Action{&a}}
			eps := Build([]*model.Model{m}, []*controller.Controller{ctl})
			if len(eps) != 1 {
				t.Fatalf("endpoints: %d", len(eps))
			}
			e := eps[0]
			if e.Kind != c.kind || e.Source != c.source {
				t.Errorf("kind %s source %s, want %s %s", e.Kind, e.Source, c.kind, c.source)
			}
			got := map[gap.Code]bool{}
			for _, g := range e.Gaps {
				got[g.Code] = true
			}
			for _, w := range c.gaps {
				if !got[w] {
					t.Errorf("gaps %v lack %s", e.Gaps, w)
				}
			}
			if len(c.gaps) == 0 {
				for _, g := range e.Gaps {
					if g.Code != gap.NoApplyEndpoint && g.Code != gap.CustomAction && g.Code != gap.MethodUnknown {
						t.Errorf("unexpected gap %s", g.Code)
					}
				}
			}
		})
	}
}

// Claim: an own settings get/set action (no parent call) carries the key
// its code shows, never the generic $internalModelName guess; without such
// a key, or for an action taking parameters, the wrapper stays empty.
func TestOwnActionWrapper(t *testing.T) {
	m := &model.Model{Class: `OPNsense\Freeradius\Client`, Root: &model.Node{Kind: model.KindContainer}}
	const own = `OPNsense\Freeradius\Api\ClientController`
	for _, c := range []struct {
		name   string
		action controller.Action
		want   string
	}{
		{"get with its own key", controller.Action{Name: "get", RespKey: "client"}, "client"},
		{"get with static::$internalModelName", controller.Action{Name: "get", RespKey: controller.ModelNameRef}, "Client"},
		{"get whose key is not shown", controller.Action{Name: "get"}, ""},
		{"get taking a uuid", controller.Action{Name: "get", RespKey: "network", Params: []controller.Param{{Name: "uuid", Optional: true}}}, ""},
		{"set on new Client()", controller.Action{Name: "set", ReqKey: "client", ReqNew: "Client"}, "client"},
		{"set on getModel()", controller.Action{Name: "set", ReqKey: "client"}, "client"},
		{"set on another model", controller.Action{Name: "set", ReqKey: "client", ReqNew: `\OPNsense\Other\Thing`}, ""},
		{"set whose key is not shown", controller.Action{Name: "set"}, ""},
		{"generic get calling the parent keeps the generic key", controller.Action{Name: "get", CallsParent: true, RespKey: "x"}, "Client"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := c.action
			a.DeclaredIn, a.Overrides = own, baseModelCtl
			ctl := &controller.Controller{Class: own, Module: "freeradius", Name: "client", ModelName: "Client",
				ModelClass: m.Class, Actions: []*controller.Action{&a}}
			e := Build([]*model.Model{m}, []*controller.Controller{ctl})[0]
			if e.Wrapper != c.want {
				t.Errorf("wrapper %q, want %q", e.Wrapper, c.want)
			}
			if !c.action.CallsParent && !gap.Has(e.Gaps, gap.OverrideNotDerived) {
				t.Errorf("gaps %v lack %s", e.Gaps, gap.OverrideNotDerived)
			}
		})
	}
}

// Claim: a controller that overrides save (os-stunnel computes
// general.enabled from its services there) marks every write it serves
// custom-save; its reads are not marked. Found by the settings write
// check, which changes a value (generator 0.10.0).
func TestControllerSaveIsCustomSave(t *testing.T) {
	m := &model.Model{Class: `OPNsense\Demo\Demo`, Root: &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer},
	}}}
	for _, hooks := range [][]string{{"save"}, nil} {
		get := controller.Action{Name: "get", DeclaredIn: baseModelCtl}
		set := controller.Action{Name: "set", DeclaredIn: baseModelCtl}
		ctl := &controller.Controller{Class: `OPNsense\Demo\Api\ServicesController`, Module: "demo", Name: "services", ModelName: "demo",
			ModelClass: m.Class, Hooks: hooks, Actions: []*controller.Action{&get, &set}}
		for _, e := range Build([]*model.Model{m}, []*controller.Controller{ctl}) {
			want := e.Kind == KindModelSet && hooks != nil
			if got := gap.Has(e.Gaps, gap.CustomSave); got != want {
				t.Errorf("hooks %v, %s: custom-save %v, want %v", hooks, e.Kind, got, want)
			}
		}
	}
}
