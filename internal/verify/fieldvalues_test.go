// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

func str(s string) *string { return &s }

func opt(sel int) map[string]any { return map[string]any{"value": "x", "selected": float64(sel)} }

func TestCompareValues(t *testing.T) {
	option := func(mod func(*model.Node)) *model.Node {
		n := &model.Node{Name: "f", Kind: model.KindField, Type: "OptionField", Shape: model.ShapeSelect,
			Default: str("a"), Options: []model.Option{{Value: "a"}, {Value: "b"}}}
		if mod != nil {
			mod(n)
		}
		return n
	}
	text := &model.Node{Name: "f", Kind: model.KindField, Type: "TextField", Shape: model.ShapeString, Default: str("x")}
	cases := []struct {
		name     string
		n        *model.Node
		val      any
		template bool
		want     []string // substrings of the findings, in order; none: no finding
	}{
		{"matches", option(nil), map[string]any{"": opt(0), "a": opt(1), "b": opt(0)}, true, nil},
		{"option not offered", option(nil), map[string]any{"": opt(0), "a": opt(1)}, true,
			[]string{`option "b" is in the data`}},
		{"option offered, not in the data", option(nil), map[string]any{"": opt(0), "a": opt(1), "b": opt(0), "c": opt(0)}, true,
			[]string{`offers option "c"`}},
		{"empty choice missing", option(nil), map[string]any{"a": opt(1), "b": opt(0)}, true,
			[]string{"offers no empty choice"}},
		{"required: no empty choice", option(func(n *model.Node) { n.Required = true }), map[string]any{"a": opt(1), "b": opt(0)}, true, nil},
		{"required: empty choice is extra", option(func(n *model.Node) { n.Required = true }), map[string]any{"": opt(0), "a": opt(1), "b": opt(0)}, true,
			[]string{`offers option ""`}},
		{"an option with the key \"\"", option(func(n *model.Node) {
			n.Required, n.Default, n.Options = true, str(""), []model.Option{{Value: ""}, {Value: "a"}}
		}), map[string]any{"": opt(1), "a": opt(0)}, true, nil},
		{"wrong default", option(nil), map[string]any{"": opt(0), "a": opt(0), "b": opt(1)}, true,
			[]string{`has "b" selected, the data's default is "a"`}},
		{"no default: empty selected", option(func(n *model.Node) { n.Default = nil }), map[string]any{"": opt(1), "a": opt(0), "b": opt(0)}, true, nil},
		{"multiple default", option(func(n *model.Node) { n.Multiple, n.Default = true, str("b,a") }), map[string]any{"a": opt(1), "b": opt(1)}, true, nil},
		{"multiple default with another separator", option(func(n *model.Node) {
			n.Multiple, n.Default, n.List = true, str("b\na"), &model.List{Separator: "\n"}
		}), map[string]any{"a": opt(1), "b": opt(1)}, true, nil},
		{"empty list reads as \"\" selected", &model.Node{Name: "f", Kind: model.KindField, Type: "CSVListField", Shape: model.ShapeSelect, Multiple: true},
			map[string]any{"": opt(1)}, true, nil},
		{"empty list: an element selected", &model.Node{Name: "f", Kind: model.KindField, Type: "CSVListField", Shape: model.ShapeSelect, Multiple: true},
			map[string]any{"x": opt(1)}, true, []string{`has "x" selected, the data's default is nothing`}},
		{"default, nothing selected", option(nil), map[string]any{"": opt(0), "a": opt(0), "b": opt(0)}, true,
			[]string{`has nothing selected, the data's default is "a"`}},
		{"options set at run time: listed ones and default not compared", option(func(n *model.Node) {
			n.Type, n.Gaps = ".\\ServiceField", []gap.Gap{gap.New(gap.OptionsDynamic, "x")}
		}), map[string]any{"z": opt(0)}, true, nil},
		{"converting class: default not compared", option(func(n *model.Node) { n.Gaps = []gap.Gap{gap.New(gap.CustomWrite, "x")} }),
			map[string]any{"": opt(0), "a": opt(0), "b": opt(1)}, true, nil},
		{"converting class: options still compared", option(func(n *model.Node) { n.Gaps = []gap.Gap{gap.New(gap.CustomWrite, "x")} }),
			map[string]any{"": opt(0), "a": opt(1)}, true, []string{`option "b" is in the data`}},
		{"computed text default not compared", &model.Node{Name: "f", Kind: model.KindField, Shape: model.ShapeString,
			Gaps: []gap.Gap{gap.New(gap.CustomWrite, "x")}}, "2000", true, nil},
		{"stored entry: selection not compared", option(nil), map[string]any{"": opt(0), "a": opt(0), "b": opt(1)}, false, nil},
		{"stored entry: options still compared", option(nil), map[string]any{"": opt(0), "a": opt(0)}, false,
			[]string{`option "b" is in the data`}},
		{"invalid default: selection not compared", option(func(n *model.Node) { n.Gaps = []gap.Gap{gap.New(gap.InvalidDefault, "x")} }),
			map[string]any{"": opt(1), "a": opt(0), "b": opt(0)}, true, nil},
		{"subclass: may offer more", option(func(n *model.Node) { n.Type = ".\\MyField" }),
			map[string]any{"": opt(0), "a": opt(1), "b": opt(0), "c": opt(0)}, true, nil},
		{"subclass: published option still offered", option(func(n *model.Node) { n.Type = ".\\MyField" }),
			map[string]any{"": opt(0), "a": opt(1)}, true, []string{`option "b" is in the data`}},
		{"own read shape: nothing compared", option(func(n *model.Node) { n.Gaps = []gap.Gap{gap.New(gap.CustomReadShape, "x")} }),
			map[string]any{"z": opt(1)}, true, nil},
		{"list answer (keys 0..n-1)", option(func(n *model.Node) {
			n.Required, n.Default, n.Options = true, str("1"), []model.Option{{Value: "0"}, {Value: "1"}}
		}), []any{opt(0), opt(1)}, true, nil},
		{"text default", text, "x", true, nil},
		{"text wrong default", text, "y", true, []string{`has "y", the data's default is "x"`}},
		{"text: stored value not compared", text, "y", false, nil},
		{"text without default", &model.Node{Name: "f", Kind: model.KindField, Shape: model.ShapeString}, "y", true,
			[]string{`has "y", the data's default is ""`}},
		{"server-set: not compared", &model.Node{Name: "f", Kind: model.KindField, BaseType: "AutoNumberField", Shape: model.ShapeString}, "7", true, nil},
		{"volatile: not compared", &model.Node{Name: "f", Kind: model.KindField, Shape: model.ShapeString, Volatile: true}, "7", true, nil},
		{"write-only: shown empty", &model.Node{Name: "f", Kind: model.KindField, Shape: model.ShapeString, GetEmpty: true, Default: str("k")}, "", true, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out []shapeDiff
			compareValues(c.n, c.val, "f", c.template, &out)
			if len(out) != len(c.want) {
				t.Fatalf("findings %v, want %d (%v)", out, len(c.want), c.want)
			}
			for i, w := range c.want {
				if out[i].kind != diffValue || !strings.Contains(out[i].msg, w) {
					t.Errorf("finding %d = %+v, want a value difference with %q", i, out[i], w)
				}
			}
		})
	}
}

// A value difference on an endpoint with a Subset gap (it may leave out
// sections) stays a mismatch: the gap does not explain a wrong option.
func TestValueDiffNotExplainedBySubset(t *testing.T) {
	r := &Result{Status: StatusMismatch, Check: CheckShape, Findings: []string{"f: x"},
		diffs: []shapeDiff{{kind: diffValue, msg: "f: x"}}}
	if bad := unexplainedBySubset(r); len(bad) != 1 {
		t.Fatalf("unexplained = %v, want the value difference", bad)
	}
}
