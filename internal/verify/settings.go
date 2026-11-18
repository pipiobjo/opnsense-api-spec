// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Settings write-back: one field of a settings model written back and
// read back.

package verify

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// settingsWriteBacks checks the set endpoint of every settings model by
// writing one field back with its current value (nothing changes):
//
//   - "saved" and the value reads back unchanged: ok;
//   - rejected because of the written field itself: mismatch;
//   - rejected because of other fields: skipped, the model is not valid in
//     its default state (a plugin that needs required settings first);
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
		inner, _ := obj[get.Wrapper].(map[string]any)
		return inner, nil
	}
	cur, bad := read()
	if bad != nil {
		return bad
	}
	path, val := plainField(m.Root, cur, "")
	if path == "" {
		r.Status, r.Findings = StatusSkipped, []string{"no plain field to write back (the model holds only entry lists or option fields)"}
		return r
	}
	code, v, raw, err := c.do("POST", set.Path, map[string]any{set.Wrapper: nested(path, val)})
	var nj *NonJSON
	if errors.As(err, &nj) {
		r.HTTP, r.Status, r.Findings = code, StatusSkipped, []string{"set does not save configuration, it answers with " + err.Error()}
		return r
	}
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP = 200
	out, _ := v.(map[string]any)
	switch res, _ := out["result"].(string); {
	case res == "saved":
	case len(asMap(out["validations"])) > 0:
		var others []string
		for k := range asMap(out["validations"]) {
			f := strings.TrimPrefix(k, set.Wrapper+".")
			if f == path {
				r.Status, r.Findings = StatusMismatch, []string{fmt.Sprintf("its own current value of %s was rejected: %v", path, out["validations"])}
				return r
			}
			others = append(others, f)
		}
		sort.Strings(others)
		r.Status, r.Findings = StatusSkipped, []string{"model not valid in its default state, set rejected for: " + strings.Join(others, ", ")}
		return r
	default:
		r.Status, r.Findings = StatusSkipped, []string{"set does not save configuration, it answers " + trunc(raw)}
		return r
	}
	after, bad := read()
	if bad != nil {
		return bad
	}
	if got := leaf(after, path); fmt.Sprint(got) != val {
		r.Status, r.Findings = StatusMismatch, []string{fmt.Sprintf("read-back after set: %s sent %q, read %v", path, val, got)}
		return r
	}
	r.Status, r.Steps = StatusOK, []string{"set", "read-back"}
	return r
}

// plainField: a field (model order) that the object carries as a plain
// string, with its value: booleans and integers first, otherwise text.
// Fields the server sets itself or never returns are left out.
func plainField(n *model.Node, obj map[string]any, prefix string) (string, string) {
	if p, v := pickField(n, obj, prefix, true); p != "" {
		return p, v
	}
	return pickField(n, obj, prefix, false)
}

func pickField(n *model.Node, obj map[string]any, prefix string, numeric bool) (string, string) {
	for _, ch := range n.Children {
		p := ch.Name
		if prefix != "" {
			p = prefix + "." + ch.Name
		}
		switch ch.Kind {
		case model.KindContainer:
			if sub, ok := obj[ch.Name].(map[string]any); ok {
				if path, v := pickField(ch, sub, p, numeric); path != "" {
					return path, v
				}
			}
		case model.KindField:
			if ch.Volatile || ch.ServerSet() || ch.WriteOnly() || len(ch.Gaps) > 0 {
				continue
			}
			if isNum := ch.BaseType == "BooleanField" || ch.BaseType == "IntegerField"; isNum != numeric {
				continue
			}
			if s, ok := obj[ch.Name].(string); ok {
				return p, s
			}
		}
	}
	return "", ""
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
