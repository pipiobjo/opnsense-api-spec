// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

func ptr(s string) *string { return &s }

func i64(v int64) *int64 { return &v }

func probeList(ps []Probe) []string {
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s %q %v", p.Claim, p.Value, p.Accept))
	}
	return out
}

func TestProbesFromClaims(t *testing.T) {
	opts := []model.Option{{Value: ""}, {Value: "a"}, {Value: "b"}}
	cases := []struct {
		name     string
		n        *model.Node
		offered  []string
		want     []string
		unprobed []string
	}{
		{"boolean, required",
			&model.Node{Type: "BooleanField", BaseType: "BooleanField", Required: true},
			nil, []string{`required "" false`, `boolean "0" true`, `boolean "1" true`, `boolean "2" false`}, nil},
		{"integer with limits",
			&model.Node{Type: "IntegerField", BaseType: "IntegerField", Props: map[string]string{"MinimumValue": "1", "MaximumValue": "65535", "ValidationMessage": "x"},
				Rules: &model.Limits{Minimum: i64(1), Maximum: i64(65535)}},
			nil, []string{`optional "" true`, `integer "contract-test" false`, `minimum "1" true`, `minimum "0" false`, `maximum "65535" true`, `maximum "65536" false`},
			nil}, // a text for the web UI is no claim
		{"single option field, complete list",
			&model.Node{Type: "OptionField", BaseType: "BaseListField", Shape: model.ShapeSelect, Options: opts},
			nil, []string{`optional "" true`, `option "a" true`, `option "b" true`, `complete "` + notAnOption + `" false`, `single "a,b" false`}, nil},
		{"multiple option field",
			&model.Node{Type: "OptionField", BaseType: "BaseListField", Shape: model.ShapeSelect, Options: opts, Multiple: true, List: &model.List{Separator: ","}},
			nil, []string{`optional "" true`, `option "a" true`, `option "b" true`, `complete "` + notAnOption + `" false`, `multiple "a,b" true`}, nil},
		{"options decided at run time: only the offered ones for single/multiple",
			&model.Node{Type: ".\\ServiceField", BaseType: "BaseListField", Shape: model.ShapeSelect, Options: opts,
				Gaps: []gap.Gap{{Code: gap.OptionsDynamic}}},
			[]string{"", "x", "y"}, []string{`optional "" true`, `single "x,y" false`}, nil},
		{"subclass with options: listed ones, but no completeness",
			&model.Node{Type: ".\\OwnField", BaseType: "BaseListField", Shape: model.ShapeSelect, Options: opts},
			nil, []string{`optional "" true`, `option "a" true`, `option "b" true`, `single "a,b" false`}, nil},
		{"mask on a text field",
			&model.Node{Type: "TextField", BaseType: "TextField", Props: map[string]string{"Mask": `/^[a-z0-9]{1,8}$/`},
				Rules: &model.Limits{Pattern: `^[a-z0-9]{1,8}$`, Bytes: true}},
			nil, []string{`optional "" true`, `mask "ct1" true`, `mask "!" false`}, nil},
		{"a field's own pattern: a value too long is the miss",
			&model.Node{Type: "DescriptionField", BaseType: "TextField", Rules: &model.Limits{Pattern: `^(.){1,255}\n?$`, Bytes: true}},
			nil, []string{`optional "" true`, `mask "contracttest1" true`, `mask "` + strings.Repeat("x", 256) + `" false`}, nil},
		{"mask Go cannot run stays unprobed",
			&model.Node{Type: "TextField", BaseType: "TextField", Props: map[string]string{"Mask": `/^(?!any$)[a-z]+$/`}},
			nil, []string{`optional "" true`}, []string{"Mask"}},
		{"forbidden characters, the allowed group accepted",
			&model.Node{Type: "TextField", BaseType: "TextField", Props: map[string]string{"AllowSpaces": "N", "AllowNewlines": "Y"},
				Checked: []string{"AllowNewlines", "AllowSpaces"}, Rules: &model.Limits{Forbidden: " \t\x00\v\f"}},
			nil, []string{`optional "" true`, `forbidden "ct x" false`, `forbidden "ct\nx" true`, `forbidden "ct\vx" false`}, nil},
		{"network format: every rule, settings counted as probed",
			&model.Node{Type: "NetworkField", BaseType: "BaseSetField", Props: map[string]string{"NetMaskAllowed": "N", "AddressFamily": "ipv4"},
				Checked: []string{"AddressFamily", "NetMaskAllowed"},
				Rules:   &model.Limits{Format: &model.Format{Kind: model.FormatNetwork, Family: "ipv4", Netmask: model.NetmaskForbidden}}},
			nil, []string{`optional "" true`, `family "192.0.2.10" true`, `family "2001:db8::10" false`, `netmask "192.0.2.0/24" false`,
				`wildcard "any" false`, `format "192.0.2.256" false`, `format "192.0.2.010" false`, `format "contract-test" false`}, nil},
		{"empty value ignored",
			&model.Node{Type: ".\\SecretField", BaseType: "TextField", Required: true, EmptyIgnored: true},
			nil, []string{`empty-kept "" true`}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.n.Type == "TextField" && tc.n.Props["Mask"] == `/^[a-z0-9]{1,8}$/` {
				// the match comes from the generated candidates: the first
				// one without spaces the mask takes
				yes, _ := maskValues(tc.n, tc.n.Rules)
				tc.want[1] = fmt.Sprintf("mask %q true", yes)
			}
			got, unprobed := probes("f", tc.n, tc.offered)
			if !slices.Equal(probeList(got), tc.want) {
				t.Errorf("probes\n got %q\nwant %q", probeList(got), tc.want)
			}
			if !slices.Equal(unprobed, tc.unprobed) {
				t.Errorf("unprobed %q, want %q", unprobed, tc.unprobed)
			}
		})
	}
}

// fakeObject is a settings model in memory with OPNsense's validation:
// only the written fields are validated, all messages are collected, and
// nothing is saved if there is one.
type fakeObject struct {
	vals     map[string]string
	validate map[string]func(v string, all map[string]string) string // field -> message ("" = valid)
	// crossField: a message for "other" whenever the field is written with
	// a value (a rule over several fields, reported on the other field)
	crossField string
	// readAs: the value a field reads back as, whatever was written
	readAs map[string]string
	writes int
}

func (o *fakeObject) target() target {
	return target{
		write: func(vals map[string]string) answer {
			o.writes++
			next := maps.Clone(o.vals)
			maps.Copy(next, vals)
			a := answer{own: map[string]bool{}}
			for f, v := range vals {
				if fn := o.validate[f]; fn != nil && v != o.vals[f] {
					if msg := fn(v, next); msg != "" {
						a.own[f] = true
					}
				}
				if f == o.crossField && v != "" && v != o.vals[f] {
					a.others = append(a.others, "other")
				}
			}
			if len(a.own) == 0 && len(a.others) == 0 {
				a.saved = true
				o.vals = next
			}
			return a
		},
		read: func() (map[string]any, error) {
			out := map[string]any{}
			for k, v := range o.vals {
				if r, ok := o.readAs[k]; ok {
					v = r
				}
				assign(out, k, v)
			}
			return out, nil
		},
	}
}

func intRange(lo, hi int64) func(string, map[string]string) string {
	return func(v string, _ map[string]string) string {
		if v == "" {
			return ""
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < lo || n > hi {
			return "Invalid integer value."
		}
		return ""
	}
}

func oneOf(keys ...string) func(string, map[string]string) string {
	return func(v string, _ map[string]string) string {
		if v == "" || slices.Contains(keys, v) {
			return ""
		}
		return "Option not in list."
	}
}

func boolean(v string, _ map[string]string) string {
	if v == "" || v == "0" || v == "1" {
		return ""
	}
	return "Value should be a boolean (0,1)."
}

func probeModel() *model.Node {
	return &model.Node{Kind: model.KindContainer, Children: []*model.Node{
		{Name: "general", Kind: model.KindContainer, Children: []*model.Node{
			{Name: "enabled", Kind: model.KindField, Type: "BooleanField", BaseType: "BooleanField", Default: ptr("0")},
			{Name: "port", Kind: model.KindField, Type: "IntegerField", BaseType: "IntegerField",
				Props: map[string]string{"MinimumValue": "1", "MaximumValue": "65535"}, Rules: &model.Limits{Minimum: i64(1), Maximum: i64(65535)}},
			{Name: "mode", Kind: model.KindField, Type: "OptionField", BaseType: "BaseListField", Shape: model.ShapeSelect,
				Options: []model.Option{{Value: "a"}, {Value: "b"}}},
		}},
		{Name: "uid", Kind: model.KindField, Type: "UniqueIdField", BaseType: "UniqueIdField"},
	}}
}

// honest: a firewall that does what probeModel says.
func honest() *fakeObject {
	return &fakeObject{
		vals: map[string]string{"general.enabled": "0", "general.port": "53", "general.mode": "a"},
		validate: map[string]func(string, map[string]string) string{
			"general.enabled": boolean,
			"general.port":    intRange(1, 65535),
			"general.mode":    oneOf("a", "b"),
		},
	}
}

var setEP = &ir.Endpoint{Path: "/api/x/settings/set", Wrapper: "x"}

func run(t *testing.T, o *fakeObject, root *model.Node) *Result {
	t.Helper()
	return probeResult(setEP, o.target(), root)
}

func TestProbeRunAgrees(t *testing.T) {
	o := honest()
	r := run(t, o, probeModel())
	if r.Status != StatusOK || len(r.Findings) > 0 || len(r.Notes) > 0 {
		t.Fatalf("status %s, findings %q, notes %q", r.Status, r.Findings, r.Notes)
	}
	claims := map[Claim]int{}
	for _, p := range r.Probes {
		if p.Outcome != OutcomeAgreed {
			t.Errorf("%+v: outcome %s", p, p.Outcome)
		}
		claims[p.Claim]++
	}
	// the server-set uid is not probed
	for _, p := range r.Probes {
		if p.Field == "uid" {
			t.Errorf("server-set field probed: %+v", p)
		}
	}
	want := map[Claim]int{ClaimOptional: 3, ClaimBoolean: 3, ClaimInteger: 1, ClaimMinimum: 2, ClaimMaximum: 2, ClaimOption: 2, ClaimComplete: 1, ClaimSingle: 1}
	if !maps.Equal(claims, want) {
		t.Errorf("claims %v, want %v", claims, want)
	}
	if o.vals["general.port"] != "53" || o.vals["general.mode"] != "a" || o.vals["general.enabled"] != "0" {
		t.Errorf("original values not written back: %v", o.vals)
	}
	// rounds: one write per round and verdict, not one per probe
	if o.writes >= len(r.Probes) {
		t.Errorf("%d writes for %d probes: the rounds do not batch fields", o.writes, len(r.Probes))
	}
}

func TestProbeRunContradictions(t *testing.T) {
	t.Run("a listed option the firewall rejects", func(t *testing.T) {
		o := honest()
		o.validate["general.mode"] = oneOf("a")
		r := run(t, o, probeModel())
		if r.Status != StatusMismatch || !slices.ContainsFunc(r.Findings, func(f string) bool {
			return strings.Contains(f, `general.mode="b"`) && strings.Contains(f, "the data predicts accepted (option), the firewall rejected it")
		}) {
			t.Errorf("status %s, findings %q", r.Status, r.Findings)
		}
		// the other fields of the spoiled round still have their verdicts
		for _, p := range r.Probes {
			if p.Field != "general.mode" && p.Outcome != OutcomeAgreed {
				t.Errorf("%+v: a rejected field must not spoil the others", p)
			}
		}
	})
	t.Run("a maximum one too low in the data", func(t *testing.T) {
		o := honest()
		o.validate["general.port"] = intRange(1, 65536)
		r := run(t, o, probeModel())
		if r.Status != StatusMismatch || len(r.Findings) != 1 || !strings.Contains(r.Findings[0], `general.port="65536": the data predicts rejected (maximum), the firewall accepted it`) {
			t.Errorf("status %s, findings %q", r.Status, r.Findings)
		}
		if o.vals["general.port"] != "53" {
			t.Errorf("port not written back after an accepted rejection probe: %v", o.vals)
		}
	})
	t.Run("a value that reads back differently", func(t *testing.T) {
		o := honest()
		o.readAs = map[string]string{"general.mode": "a"}
		r := run(t, o, probeModel())
		if r.Status != StatusMismatch || !slices.ContainsFunc(r.Findings, func(f string) bool {
			return strings.Contains(f, `general.mode="b" (option) was accepted but reads back as "a"`)
		}) {
			t.Errorf("status %s, findings %q", r.Status, r.Findings)
		}
	})
	t.Run("a rule over several fields is a note, not a verdict", func(t *testing.T) {
		o := honest()
		o.crossField = "general.port"
		r := run(t, o, probeModel())
		if r.Status != StatusOK {
			t.Errorf("status %s, findings %q", r.Status, r.Findings)
		}
		var other int
		for _, p := range r.Probes {
			if p.Outcome == OutcomeOtherFields {
				other++
				if p.Field != "general.port" {
					t.Errorf("%+v: only the port's probes meet the rule", p)
				}
			}
		}
		rules := slices.DeleteFunc(slices.Clone(r.Notes), func(n string) bool { return !strings.Contains(n, "a rule over several fields") })
		if other == 0 || len(rules) != other {
			t.Errorf("%d other-fields probes, notes %q", other, r.Notes)
		}
	})
	t.Run("a custom save explains the difference", func(t *testing.T) {
		o := honest()
		o.validate["general.mode"] = oneOf("a")
		r := probeResult(setEP, o.target(), probeModel(), []gap.Gap{{Code: gap.CustomSave, Detail: "toLegacy"}})
		if r.Status != StatusKnownGap {
			t.Errorf("status %s, findings %q", r.Status, r.Findings)
		}
	})
}

func TestProbeRunNotProbed(t *testing.T) {
	// a model the firewall does not take back as it is (a plugin that
	// needs settings first): nothing is probed, and the result says why
	o := honest()
	tg := o.target()
	tg.write = func(vals map[string]string) answer {
		return answer{own: map[string]bool{"general.port": true}}
	}
	r := probeResult(setEP, tg, probeModel())
	if r.Status != StatusSkipped || len(r.Probes) > 0 || !strings.Contains(r.Findings[0], "current values are rejected for: general.port") {
		t.Errorf("status %s, findings %q, %d probes", r.Status, r.Findings, len(r.Probes))
	}
	if r := probeResult(setEP, o.target(), nil); r.Status != StatusSkipped {
		t.Errorf("unknown model: status %s", r.Status)
	}
}

func TestSummarize(t *testing.T) {
	rs := []*Result{
		{Check: CheckFieldProbe, Status: StatusOK, Probes: []Probe{
			{Claim: ClaimOption, Outcome: OutcomeAgreed}, {Claim: ClaimOption, Outcome: OutcomeContradicted}, {Claim: ClaimMask, Outcome: OutcomeOtherFields}},
			Unprobed: map[string]int{"AddressFamily": 2}},
		{Check: CheckFieldProbe, Status: StatusSkipped, Unprobed: map[string]int{"AddressFamily": 1}},
		{Check: CheckWriteCycle, Status: StatusSkipped, Probes: []Probe{{Claim: ClaimOption, Outcome: OutcomeAgreed}}},
	}
	s := Summarize(rs)
	if s.Probes[ClaimOption][OutcomeAgreed] != 1 || s.Probes[ClaimOption][OutcomeContradicted] != 1 || s.Probes[ClaimMask][OutcomeOtherFields] != 1 {
		t.Errorf("probes %v", s.Probes)
	}
	if s.NotProbed != 1 || s.Unprobed["AddressFamily"] != 3 {
		t.Errorf("not probed %d (a skipped write cycle is not a probe result), unprobed %v", s.NotProbed, s.Unprobed)
	}
	if l := s.Lines(); len(l) != 4 || l[3] != "constraints no probe tries (fields): AddressFamily 3" {
		t.Errorf("lines %q", l)
	}
}

func TestProbeRunModelValidationAndOverlay(t *testing.T) {
	t.Run("a rule of the model explains its fields only", func(t *testing.T) {
		o := honest()
		o.validate["general.mode"] = oneOf("a") // refused by a rule over several fields
		root := probeModel()
		mode := root.Children[0].Children[2]
		mode.Gaps = []gap.Gap{{Code: gap.ModelValidation, Detail: "performValidation of X"}}
		r := run(t, o, root)
		if r.Status != StatusKnownGap || len(r.Findings) != 2 || !strings.Contains(r.Findings[1], "expected (model-validation)") {
			t.Errorf("status %s, findings %q", r.Status, r.Findings)
		}
		o = honest()
		o.validate["general.mode"] = oneOf("a")
		o.validate["general.port"] = intRange(1, 100) // not explained
		r = run(t, o, root)
		if r.Status != StatusMismatch || !slices.ContainsFunc(r.Findings, func(f string) bool { return strings.HasPrefix(f, "general.port=") }) ||
			slices.ContainsFunc(r.Findings, func(f string) bool { return strings.HasPrefix(f, "general.mode=") }) {
			t.Errorf("status %s, findings %q", r.Status, r.Findings)
		}
	})
	t.Run("fields the endpoint sets itself are not probed", func(t *testing.T) {
		o := honest()
		o.readAs = map[string]string{"general.mode": "a"}
		set := &ir.Endpoint{Path: "/api/x/settings/set", Computed: []string{"general.mode"}, Forced: map[string]string{"general.port": "53"}}
		r := probeResult(set, o.target(), probeModel())
		if r.Status != StatusOK {
			t.Errorf("status %s, findings %q", r.Status, r.Findings)
		}
		for _, p := range r.Probes {
			if p.Field == "general.mode" || p.Field == "general.port" {
				t.Errorf("probed %+v", p)
			}
		}
	})
}

// a field that a rule over several fields refuses in the object's current
// state (a firewall rule's port with protocol "any") is probed again with
// another value of a select that lets its own values decide
func TestProbeRunContext(t *testing.T) {
	o := honest()
	o.validate["general.port"] = func(v string, all map[string]string) string {
		if v != "" && v != "53" && all["general.mode"] != "b" { // the stored value is valid as it is
			return "a port only with mode b"
		}
		return intRange(1, 65535)(v, all)
	}
	root := probeModel()
	port := root.Children[0].Children[1]
	port.Gaps = []gap.Gap{{Code: gap.ModelValidation, Detail: "performValidation of X"}}
	// options decided at run time: mode's own probes do not set "b", the
	// search takes the listed options
	mode := root.Children[0].Children[2]
	mode.Gaps = []gap.Gap{{Code: gap.OptionsDynamic}, {Code: gap.ModelValidation}}
	r := run(t, o, root)
	if r.Status != StatusOK || len(r.Findings) > 0 {
		t.Fatalf("status %s, findings %q, notes %q", r.Status, r.Findings, r.Notes)
	}
	n := 0
	for _, p := range r.Probes {
		if p.Field != "general.port" {
			continue
		}
		n++
		if p.Context["general.mode"] != "b" || p.Outcome != OutcomeAgreed {
			t.Errorf("%+v: want agreed in the context mode=b", p)
		}
	}
	if n == 0 {
		t.Fatal("port not probed")
	}
	if o.vals["general.port"] != "53" || o.vals["general.mode"] != "a" {
		t.Errorf("not restored: %v", o.vals)
	}
	// no context helps: the verdicts are no verdicts about the field
	o = honest()
	o.validate["general.port"] = func(v string, _ map[string]string) string {
		if v != "" {
			return "never"
		}
		return ""
	}
	r = run(t, o, root)
	if r.Status != StatusKnownGap || !slices.ContainsFunc(r.Notes, func(s string) bool { return strings.Contains(s, "no context") }) {
		t.Errorf("status %s, notes %q", r.Status, r.Notes)
	}
	for _, p := range r.Probes {
		if p.Field == "general.port" && p.Accept && p.Value != "" && p.Outcome != OutcomeExplained {
			t.Errorf("%+v: want explained", p)
		}
	}
}

// a context that worked for one field, extended by a boolean, lets a
// second field decide (a redirect port: protocol TCP and no "no rdr")
func TestProbeRunContextOnTopOfAnother(t *testing.T) {
	o := honest()
	o.vals["general.other"] = ""
	o.vals["general.enabled"] = "1" // its probes leave it at 1; the port needs 0
	o.validate["general.other"] = func(v string, all map[string]string) string {
		if v != "" && all["general.mode"] != "b" {
			return "only with mode b"
		}
		return ""
	}
	o.validate["general.port"] = func(v string, all map[string]string) string {
		if v != "" && v != "53" && (all["general.mode"] != "b" || all["general.enabled"] != "0") {
			return "only with mode b and enabled"
		}
		return intRange(1, 65535)(v, all)
	}
	root := probeModel()
	g := root.Children[0]
	mv := gap.Gap{Code: gap.ModelValidation, Detail: "performValidation of X"}
	g.Children[0].Gaps = []gap.Gap{mv}                             // enabled
	g.Children[1].Gaps = []gap.Gap{mv}                             // port
	g.Children[2].Gaps = []gap.Gap{{Code: gap.OptionsDynamic}, mv} // mode
	g.Children = append([]*model.Node{{Name: "other", Kind: model.KindField, Type: "TextField", BaseType: "TextField", Gaps: []gap.Gap{mv},
		Rules: &model.Limits{Pattern: `^[a-z0-9]+$`}}}, g.Children...)
	r := run(t, o, root)
	var port map[string]string
	for _, p := range r.Probes {
		if p.Field == "general.port" && p.Value == "1" {
			port = p.Context
			if p.Outcome != OutcomeAgreed {
				t.Errorf("%+v", p)
			}
		}
	}
	if port["general.mode"] != "b" || port["general.enabled"] != "0" {
		t.Errorf("context %v, notes %q", port, r.Notes)
	}
}

// a get that is the controller's own code (netbird 25.7 masks the setup
// key): verdicts count, what it shows is not compared
func TestProbeRunOpaqueRead(t *testing.T) {
	o := honest()
	o.readAs = map[string]string{"general.mode": "b"} // valid, but not what was stored
	tg := o.target()
	tg.opaqueRead = true
	if r := probeResult(setEP, tg, probeModel()); r.Status != StatusOK {
		t.Errorf("status %s, findings %q", r.Status, r.Findings)
	}
	if r := probeResult(setEP, o.target(), probeModel()); r.Status != StatusMismatch {
		t.Errorf("a derived get is compared: status %s", r.Status)
	}
}

func TestFieldWire(t *testing.T) {
	sel := func(keys ...string) map[string]any {
		m := map[string]any{"": map[string]any{"value": "none", "selected": float64(0)}, "0": map[string]any{"value": "0", "selected": float64(0)}, "1": map[string]any{"value": "1", "selected": float64(0)}}
		for _, k := range keys {
			m[k] = map[string]any{"value": k, "selected": float64(1)}
		}
		return m
	}
	single := &model.Node{Shape: model.ShapeSelect}
	multiple := &model.Node{Shape: model.ShapeSelect, Multiple: true}
	for _, c := range []struct {
		n    *model.Node
		v    any
		want string
	}{
		{single, sel("", "0"), "0"}, // 25.7: "0" also marks the empty choice
		{single, sel(""), ""},
		{single, sel("1"), "1"},
		{multiple, sel("0", "1"), "0,1"},
		{multiple, sel("", "0"), ",0"}, // a list keeps what it reads
		{single, "x", "x"},
	} {
		if got := fieldWire(c.n, c.v); got != c.want {
			t.Errorf("%+v: %q, want %q", c.v, got, c.want)
		}
	}
}
