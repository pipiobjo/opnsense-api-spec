// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// What a read answer shows of a field's values, against what the data
// claims about them: the options, and in the template of a new entry the
// default. The firewall describes its fields itself (BaseListField's
// getNodeOptions lists every option and marks the selected ones), so these
// claims are checked without writing anything.

package verify

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// compareValues adds a diffValue for every claim about the field n that
// the answer val contradicts. template: val comes from the get of a new
// entry (no uuid), so it holds the model's defaults; a settings get or a
// stored entry holds whatever is configured, and only the options are
// compared.
func compareValues(n *model.Node, val any, p string, template bool, out *[]shapeDiff) {
	if fieldShapeUncertain(n) {
		return // own getNodeData/getNodeOptions: the answer is built by code the generator does not read
	}
	diff := func(format string, a ...any) {
		*out = append(*out, shapeDiff{kind: diffValue, msg: p + ": " + fmt.Sprintf(format, a...)})
	}
	switch n.Shape {
	case model.ShapeSelect:
		offered, selected, ok := selectKeys(val)
		if !ok {
			return // compareNode reports the type
		}
		// options set at run time (options-dynamic): the listed ones are
		// a hint, and a default outside them is not known to be invalid
		runtime := gap.Has(n.Gaps, gap.OptionsDynamic)
		published := map[string]bool{}
		for _, o := range n.Options {
			published[o.Value] = true
			if !offered[o.Value] && !runtime {
				diff("option %q is in the data, the firewall does not offer it", o.Value)
			}
		}
		// BaseListField adds the empty choice to a single select that is
		// not required
		blank := !n.Required && !n.Multiple
		if completeOptions(n) {
			for _, k := range sortedKeys(offered) {
				if !published[k] && (k != "" || !blank) {
					diff("the firewall offers option %q, the data does not list it", k)
				}
			}
			if blank && !offered[""] {
				diff("the data allows an empty value, the firewall offers no empty choice")
			}
		}
		if template && !runtime && !defaultConverted(n) && !gap.Has(n.Gaps, gap.InvalidDefault) {
			want, got := defaultItems(n), sortedKeys(selected)
			// an empty value reads as nothing selected, or as "" selected:
			// the empty choice, an option whose key is "", or the one
			// empty element of an empty list
			empty := len(got) == 0 || slices.Equal(got, []string{""})
			if len(want) == 0 && !empty || len(want) > 0 && !slices.Equal(got, want) {
				diff("a new entry has %s selected, the data's default is %s", quoteList(got), quoteList(want))
			}
		}
	case model.ShapeString:
		if !template || n.ServerSet() || n.Volatile || defaultConverted(n) {
			return // the firewall fills it, it is not stored, or its class computes it
		}
		s, ok := val.(string)
		if !ok {
			return // compareNode reports the type
		}
		want := ""
		if n.Default != nil && !n.WriteOnly() {
			want = *n.Default
		}
		if s != want {
			diff("a new entry has %q, the data's default is %q", s, want)
		}
	}
}

// defaultConverted: the field class's setValue converts values
// (custom-write), and a new entry's default goes through it (applyDefault),
// so the default reads back converted or computed (GidField picks the next
// free group ID).
func defaultConverted(n *model.Node) bool { return gap.Has(n.Gaps, gap.CustomWrite) }

// completeOptions: the data claims its options are every value the field
// accepts: the core OptionField with options that are not decided at run
// time. The capability data's consumers apply the same rule
// (capability.Field.StaticOptions in the Go client). An invalid default
// does not change the options.
func completeOptions(n *model.Node) bool {
	return n.Type == "OptionField" && len(n.Options) > 0 && !gap.Has(n.Gaps, gap.OptionsDynamic)
}

// defaultItems: the default as the list of selected keys, sorted.
func defaultItems(n *model.Node) []string {
	if n.Default == nil || *n.Default == "" {
		return nil
	}
	sep := ","
	if n.List != nil && n.List.Separator != "" {
		sep = n.List.Separator
	}
	items := strings.Split(*n.Default, sep)
	sort.Strings(items)
	return slices.Compact(items)
}

// selectKeys reads a select answer: a {key: {value, selected}} map, or a
// JSON list when PHP's keys are 0..n-1.
func selectKeys(v any) (offered, selected map[string]bool, ok bool) {
	offered, selected = map[string]bool{}, map[string]bool{}
	add := func(k string, o any) bool {
		m, isMap := o.(map[string]any)
		if !isMap {
			return false
		}
		offered[k] = true
		if truthy(m["selected"]) {
			selected[k] = true
		}
		return true
	}
	switch t := v.(type) {
	case map[string]any:
		for k, o := range t {
			if !add(k, o) {
				return nil, nil, false
			}
		}
	case []any:
		for i, o := range t {
			if !add(strconv.Itoa(i), o) {
				return nil, nil, false
			}
		}
	default:
		return nil, nil, false
	}
	return offered, selected, true
}

// truthy: PHP's selected is 1/0, sometimes "1"/"0" or a boolean.
func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t != "" && t != "0"
	}
	return false
}

func quoteList(items []string) string {
	if len(items) == 0 {
		return "nothing"
	}
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}
