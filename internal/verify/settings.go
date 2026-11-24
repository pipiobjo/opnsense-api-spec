// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Settings write check: one field of a settings model changed, read back,
// restored and read back.

package verify

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// settingsWriteBacks checks the set endpoint of every settings model by
// changing one field and setting it back:
//
//   - a value other than the current one is written ("saved"), read back,
//     the original value is written again and read back: ok. A set that
//     answers "saved" but stores nothing is a mismatch, which writing the
//     current value back could not show;
//   - a candidate rejected for the field itself is replaced by the next
//     one, then the next field is tried (bounded by maxSettingsTries);
//   - rejected because of other fields: skipped, the model is not valid in
//     its default state (a plugin that needs required settings first);
//   - the original value rejected on restore: mismatch;
//   - any other answer: skipped, the endpoint does not save configuration
//     (a job or action under the name set).
//
// The result is a "write-cycle" on the set path, so the capability data
// records it as the resource's write verification.
func settingsWriteBacks(c *Client, rel *ir.Release, models map[string]*model.Model, packages map[string]bool) []*Result {
	gets := map[string]*ir.Endpoint{}
	for _, e := range rel.Endpoints {
		if e.Kind == ir.KindModelGet && packages[e.Package] {
			gets[e.Module+"/"+e.Controller] = e
		}
	}
	var out []*Result
	for _, e := range rel.Endpoints {
		if e.Kind != ir.KindModelSet || !packages[e.Package] || e.Wrapper == "" {
			continue
		}
		g := gets[e.Module+"/"+e.Controller]
		if g == nil || g.Wrapper != e.Wrapper {
			continue
		}
		out = append(out, c.noted(settingsWriteBack(c, g, e, models[e.Model])))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// maxSettingsTries bounds the writes of one settings check (fields times
// candidates).
const maxSettingsTries = 8

// setAnswer is what a settings set answered.
type setAnswer int

const (
	setSaved    setAnswer = iota
	setOwnField           // rejected because of the written field
	setOthers             // rejected because of other fields
	setNoSave             // not a configuration save (other answer)
	setFailed             // HTTP or transport error (r is filled in)
)

func settingsWriteBack(c *Client, get, set *ir.Endpoint, m *model.Model) *Result {
	r := &Result{Path: set.Path, Method: "POST", Check: CheckWriteCycle}
	if m == nil {
		r.Status, r.Findings = StatusSkipped, []string{"model unknown"}
		return r
	}
	read := func() (map[string]any, *Result) {
		code, v, raw, err := c.do("GET", get.Path, nil)
		var nj *NonJSON
		if errors.As(err, &nj) {
			r.HTTP, r.Status, r.Findings = code, StatusSkipped, []string{"not a configuration resource: get answers with " + err.Error()}
			return nil, r
		}
		if err != nil || code != 200 {
			return nil, fail(r, code, raw, err)
		}
		obj, _ := v.(map[string]any)
		inner, ok := obj[get.Wrapper].(map[string]any)
		if l, isList := obj[get.Wrapper].([]any); isList && len(l) == 0 {
			inner, ok = map[string]any{}, true // PHP encodes an empty object as []
		}
		if !ok {
			// otherwise it would pass as "no plain field to write back"
			r.HTTP, r.Status, r.Findings = code, StatusMismatch, []string{fmt.Sprintf("root key %q missing or not an object in the answer of %s", get.Wrapper, get.Path)}
			return nil, r
		}
		return inner, nil
	}
	// write sends one field; others are the fields of the validation
	// messages that are not the written one
	write := func(path, val string) (setAnswer, []string) {
		code, v, raw, err := c.do("POST", set.Path, map[string]any{set.Wrapper: nested(path, val)})
		var nj *NonJSON
		if errors.As(err, &nj) {
			r.HTTP, r.Status, r.Findings = code, StatusSkipped, []string{"set does not save configuration, it answers with " + err.Error()}
			return setNoSave, nil
		}
		if err != nil || code != 200 {
			fail(r, code, raw, err)
			return setFailed, nil
		}
		r.HTTP = 200
		out, _ := v.(map[string]any)
		if res, _ := out["result"].(string); res == "saved" {
			return setSaved, nil
		}
		if vals := asMap(out["validations"]); len(vals) > 0 {
			var others []string
			for k := range vals {
				f := strings.TrimPrefix(k, set.Wrapper+".")
				if f == path {
					return setOwnField, nil
				}
				others = append(others, f)
			}
			return setOthers, others
		}
		r.Status, r.Findings = StatusSkipped, []string{"set does not save configuration, it answers " + trunc(raw)}
		return setNoSave, nil
	}
	cur, bad := read()
	if bad != nil {
		return bad
	}
	fields := plainFields(m.Root, cur, "")
	if len(fields) == 0 {
		r.Status, r.Findings = StatusSkipped, []string{"no plain field to write back (the model holds only entry lists or option fields)"}
		return r
	}
	// the current value first: a model that is not valid as it is (a
	// plugin that needs required settings) rejects every change, and
	// changing exactly the missing field would make the restore fail
	switch ans, oth := write(fields[0].path, fields[0].value); ans {
	case setSaved:
	case setOwnField:
		r.Status, r.Findings = StatusMismatch, []string{fmt.Sprintf("its own current value of %s was rejected", fields[0].path)}
		return r
	case setOthers:
		sort.Strings(oth)
		r.Status, r.Findings = StatusSkipped, []string{"model not valid in its default state, set rejected for: " + strings.Join(oth, ", ")}
		return r
	default:
		return r
	}
	var path, orig, val string
	others := map[string]bool{}
	var refused []string
	tries := 0
search:
	for _, f := range fields {
		for _, cand := range otherValues(f.node, f.value) {
			if tries == maxSettingsTries {
				break search
			}
			tries++
			switch ans, oth := write(f.path, cand); ans {
			case setSaved:
				path, orig, val = f.path, f.value, cand
				break search
			case setOwnField:
				refused = append(refused, f.path+"="+cand)
				continue
			case setOthers:
				for _, o := range oth {
					others[o] = true
				}
				continue search // the model needs other fields first
			default:
				return r
			}
		}
	}
	if path == "" {
		r.Status = StatusSkipped
		if len(others) > 0 {
			l := slices.Sorted(maps.Keys(others))
			r.Findings = []string{"model not valid in its default state, set rejected for: " + strings.Join(l, ", ")}
		} else {
			r.Findings = []string{"no other value accepted: " + strings.Join(refused, ", ")}
		}
		return r
	}
	r.Steps = []string{"set"}
	after, bad := read()
	if bad != nil {
		return bad
	}
	if got := leaf(after, path); fmt.Sprint(got) != val {
		r.Findings = append(r.Findings, fmt.Sprintf("read-back after set: %s sent %q, read %v", path, val, got))
	} else {
		r.Steps = append(r.Steps, "read-back")
	}
	switch ans, _ := write(path, orig); ans {
	case setSaved:
		r.Steps = append(r.Steps, "restore")
	case setOwnField, setOthers:
		r.Findings = append(r.Findings, fmt.Sprintf("its own original value of %s was rejected on restore", path))
	default:
		return r
	}
	if slices.Contains(r.Steps, "restore") {
		final, bad := read()
		if bad != nil {
			return bad
		}
		if got := leaf(final, path); fmt.Sprint(got) != orig {
			r.Findings = append(r.Findings, fmt.Sprintf("read-back after restore: %s sent %q, read %v", path, orig, got))
		} else {
			r.Steps = append(r.Steps, "read-back-restore")
		}
	}
	r.Status = status(r.Findings)
	if r.Status == StatusMismatch {
		// the data already says that writes go through own code
		for _, gs := range [][]gap.Gap{set.Gaps, m.Gaps} {
			if g, ok := gap.First(gs, func(c gap.Code) bool { return c == gap.CustomSave }); ok {
				r.Status = StatusKnownGap
				r.Findings = append(r.Findings, "expected ("+string(g.Code)+"): "+g.Detail)
				break
			}
		}
	}
	return r
}

// plainValue is a field the object carries as a plain string.
type plainValue struct {
	path, value string
	node        *model.Node
}

// plainFields: the fields (model order) that the object carries as plain
// strings, booleans and integers first, then text. Fields the server sets
// itself, never returns, holds as a list or marks with a gap are left out.
func plainFields(n *model.Node, obj map[string]any, prefix string) []plainValue {
	return append(pickFields(n, obj, prefix, true), pickFields(n, obj, prefix, false)...)
}

func pickFields(n *model.Node, obj map[string]any, prefix string, numeric bool) []plainValue {
	var out []plainValue
	for _, ch := range n.Children {
		p := ch.Name
		if prefix != "" {
			p = prefix + "." + ch.Name
		}
		switch ch.Kind {
		case model.KindContainer:
			if sub, ok := obj[ch.Name].(map[string]any); ok {
				out = append(out, pickFields(ch, sub, p, numeric)...)
			}
		case model.KindField:
			if ch.Volatile || ch.ServerSet() || ch.WriteOnly() || ch.List != nil || len(ch.Gaps) > 0 {
				continue
			}
			if isNum := ch.BaseType == "BooleanField" || ch.BaseType == "IntegerField"; isNum != numeric {
				continue
			}
			if s, ok := obj[ch.Name].(string); ok {
				out = append(out, plainValue{p, s, ch})
			}
		}
	}
	return out
}

// otherValues: up to three valid-looking values other than cur (a boolean
// is flipped).
func otherValues(n *model.Node, cur string) []string {
	var out []string
	for _, v := range candidates(n, nil) {
		if v != cur && len(out) < 3 {
			out = append(out, v)
		}
	}
	return out
}

// nested turns "general.enabled" and a value into {"general": {"enabled": v}}.
func nested(path, v string) map[string]any {
	parts := strings.Split(path, ".")
	out := map[string]any{parts[len(parts)-1]: v}
	for i := len(parts) - 2; i >= 0; i-- {
		out = map[string]any{parts[i]: out}
	}
	return out
}

func leaf(obj map[string]any, path string) any {
	var cur any = obj
	for _, p := range strings.Split(path, ".") {
		m, _ := cur.(map[string]any)
		cur = m[p]
	}
	return cur
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
