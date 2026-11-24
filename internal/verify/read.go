// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Read checks: an endpoint answers, a read answer has the model's shape.

package verify

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

func checkReachable(c *Client, e *ir.Endpoint, method string) *Result {
	r := &Result{Path: e.Path, Method: method, Check: CheckReachable}
	code, v, raw, err := c.do(method, e.Path, nil)
	var nj *NonJSON
	if errors.As(err, &nj) {
		r.HTTP, r.Status, r.Findings = code, StatusOK, []string{err.Error()}
		return r
	}
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP, r.Status = code, StatusOK
	c.schema(e, method, e.Path, v)
	return r
}

// shapeDiff is one difference between a read answer and the model.
type shapeDiff struct {
	kind diffKind
	// section: the missing node is an entry list or a container, not a
	// field (only for diffMissing)
	section bool
	msg     string
}

type diffKind int

const (
	diffMissing diffKind = iota // a model node is not in the answer
	diffExtra                   // the answer has a key the model does not
	diffType                    // a value has another JSON type
	// diffOtherResource: a missing node that holds no field of the
	// resource read, only nested entry lists, which are resources of their
	// own with their own get and search (the Kea and IPsec settings get
	// leaves them out). Not a difference of this resource; a note.
	diffOtherResource
)

// messages returns the findings of the differences and the notes (nodes
// of other resources left out of the answer).
func messages(ds []shapeDiff) (findings, notes []string) {
	var other []string
	for _, d := range ds {
		if d.kind == diffOtherResource {
			other = append(other, d.msg)
			continue
		}
		findings = append(findings, d.msg)
	}
	if len(other) > 0 {
		notes = []string{"entry lists not in the answer (resources of their own): " + strings.Join(other, ", ")}
	}
	return findings, notes
}

// holdsFields: a field of this resource is below n without passing an
// entry list (the same rule as the fields of a resource in the capability
// data).
func holdsFields(n *model.Node) bool {
	for _, c := range n.Children {
		switch c.Kind {
		case model.KindField:
			return true
		case model.KindContainer:
			if holdsFields(c) {
				return true
			}
		}
	}
	return false
}

// compare the response object with the model node (Read shape)
func compareNode(n *model.Node, v any, path string, out *[]shapeDiff) {
	obj, ok := v.(map[string]any)
	if arr, isArr := v.([]any); isArr && len(arr) == 0 {
		obj, ok = map[string]any{}, true // PHP encodes an empty object as []
		if len(n.Children) == 0 {
			return
		}
	}
	typeErr := func(p, want string, val any) {
		*out = append(*out, shapeDiff{kind: diffType, msg: fmt.Sprintf("%s: expected %s, got %T", p, want, val)})
	}
	if !ok {
		typeErr(path, "object", v)
		return
	}
	known := map[string]bool{}
	for _, ch := range n.Children {
		known[ch.Name] = true
		p := strings.TrimPrefix(path+"."+ch.Name, ".")
		val, present := obj[ch.Name]
		if !present {
			if ch.Kind != model.KindField && !holdsFields(ch) {
				*out = append(*out, shapeDiff{kind: diffOtherResource, msg: p})
				continue
			}
			*out = append(*out, shapeDiff{kind: diffMissing, section: ch.Kind != model.KindField, msg: p + ": missing in response"})
			continue
		}
		switch ch.Kind {
		case model.KindContainer:
			compareNode(ch, val, p, out)
		case model.KindArray:
			if _, ok := val.(map[string]any); !ok {
				if arr, ok := val.([]any); !ok || len(arr) != 0 {
					typeErr(p, "UUID map", val)
				}
			}
		case model.KindField:
			switch ch.Shape {
			case model.ShapeSelect:
				// map, or a JSON array when PHP keys are 0..n-1
				_, isMap := val.(map[string]any)
				_, isList := val.([]any)
				if !isMap && !isList {
					typeErr(p, "select map", val)
				}
			case model.ShapeString:
				if fieldShapeUncertain(ch) {
					break
				}
				if _, ok := val.(string); !ok {
					typeErr(p, "string", val)
				}
			}
		}
	}
	for k := range obj {
		if !known[k] && !strings.HasPrefix(k, "%") {
			*out = append(*out, shapeDiff{kind: diffExtra, msg: strings.TrimPrefix(path+"."+k, ".") + ": not in model"})
		}
	}
}

// fieldShapeUncertain: the generator already marked the read shape as
// unknown (custom getNodeData/getNodeOptions), so no strict comparison.
func fieldShapeUncertain(n *model.Node) bool {
	return gap.Has(n.Gaps, gap.CustomReadShape)
}

func checkModelGet(c *Client, e *ir.Endpoint, m *model.Model) *Result {
	r := &Result{Path: e.Path, Method: "GET", Check: CheckShape}
	code, v, raw, err := c.do("GET", e.Path, nil)
	var nj *NonJSON
	if errors.As(err, &nj) {
		r.HTTP, r.Status, r.Findings, r.rootMissing = code, StatusMismatch, []string{err.Error()}, true
		return r // knownGap() decides whether a gap explains it
	}
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP = code
	c.schema(e, "GET", e.Path, v)
	if e.Wrapper == "" {
		// an own get action whose code does not show its key: nothing to
		// compare against (the endpoint is no resource in the capability
		// data)
		r.Status, r.Findings = StatusSkipped, []string{"root key not derived; shape not compared"}
		return r
	}
	obj, _ := v.(map[string]any)
	inner, ok := obj[e.Wrapper]
	if !ok {
		keys := slices.Sorted(maps.Keys(obj))
		r.Status, r.Findings, r.rootMissing = StatusMismatch, []string{fmt.Sprintf("root key %q missing, got %v", e.Wrapper, keys)}, true
		return r
	}
	if m == nil {
		r.Status, r.Findings = StatusSkipped, []string{"model unknown"}
		return r
	}
	compareNode(m.Root, inner, "", &r.diffs)
	r.Findings, r.Notes = messages(r.diffs)
	r.Status = status(r.Findings)
	return r
}

// knownGap turns a mismatch into "known-gap" when the endpoint was already
// marked as overriding the generic action (the test confirms the gap).
// When every such mark is a Subset gap, a shape check is known-gap only if
// each difference is one the gap explains (a whole section left out, a key
// added); a missing field, a type error, a missing root key or an answer
// that is not JSON stays a mismatch, so a new deviation on such an endpoint
// is not swallowed. Other marks (custom logic, post-processing, overrides
// that are not derived) explain any difference in the content: the source
// says the shape is built by code the generator does not read. No mark
// explains a missing root key (or an answer that is not JSON) when the data
// names one: the key is published as derived, and a consumer relies on it.
func knownGap(r *Result, e *ir.Endpoint) *Result {
	if r.Status != StatusMismatch {
		return r
	}
	g, ok := gap.First(e.Gaps, gap.MayDiffer)
	if !ok {
		return r
	}
	if r.rootMissing && e.Wrapper != "" {
		// the root key is derived (from the generic action or the
		// action's own code) and published as such; a gap on the
		// content does not explain an answer without it
		r.Findings = append(r.Findings, fmt.Sprintf("not explained by %s: the root key is part of the data, not of the gap", g.Code))
		return r
	}
	_, broad := gap.First(e.Gaps, func(c gap.Code) bool { return gap.MayDiffer(c) && !gap.Subset(c) })
	if !broad && r.Check == CheckShape {
		if bad := unexplainedBySubset(r); len(bad) > 0 {
			r.Findings = append(r.Findings, fmt.Sprintf("not explained by %s (only whole sections left out or keys added): %s",
				g.Code, strings.Join(bad, "; ")))
			return r
		}
	}
	r.Status = StatusKnownGap
	r.Findings = append(r.Findings, "expected ("+string(g.Code)+"): "+g.Detail)
	return r
}

// unexplainedBySubset returns the findings of a shape mismatch that a
// Subset gap does not explain.
func unexplainedBySubset(r *Result) []string {
	if len(r.diffs) == 0 {
		return r.Findings // root key missing, not JSON
	}
	var bad []string
	for _, d := range r.diffs {
		if d.kind == diffType || (d.kind == diffMissing && !d.section) {
			bad = append(bad, d.msg)
		}
	}
	return bad
}

func checkSearch(c *Client, e *ir.Endpoint) *Result {
	r := &Result{Path: e.Path, Method: "GET", Check: CheckShape}
	code, v, raw, err := c.do("GET", e.Path+"?current=1&rowCount=-1", nil)
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP = code
	c.schema(e, "GET", e.Path, v)
	obj, _ := v.(map[string]any)
	for _, k := range []string{"rows", "total", "rowCount", "current"} {
		if _, ok := obj[k]; !ok {
			r.Findings = append(r.Findings, "search result has no "+k)
		}
	}
	r.Status = status(r.Findings)
	return r
}

// item get without uuid returns a new entry with defaults
func checkItemGet(c *Client, e *ir.Endpoint, m *model.Model) *Result {
	r := &Result{Path: e.Path, Method: "GET", Check: CheckShape}
	code, v, raw, err := c.do("GET", e.Path, nil)
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP = code
	c.schema(e, "GET", e.Path, v)
	obj, _ := v.(map[string]any)
	if e.Outer != "" {
		obj, _ = obj[e.Outer].(map[string]any)
	}
	inner, ok := obj[e.Wrapper]
	if !ok {
		r.Status, r.Findings, r.rootMissing = StatusMismatch, []string{fmt.Sprintf("root key %q missing", strings.TrimPrefix(e.Outer+"."+e.Wrapper, "."))}, true
		return r
	}
	if m != nil {
		if n := m.Find(e.ModelPath); n != nil {
			compareNode(&model.Node{Kind: model.KindContainer, Children: n.Children}, inner, e.ModelPath, &r.diffs)
			r.Findings, r.Notes = messages(r.diffs)
		}
	}
	r.Status = status(r.Findings)
	return r
}

func isOption(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, hasSel := m["selected"]
	return hasSel
}

// isSelect: a select map/list as read (all members are {value, selected}).
func isSelect(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			return false
		}
		for _, o := range t {
			if !isOption(o) {
				return false
			}
		}
		return true
	case []any:
		for _, o := range t {
			if !isOption(o) {
				return false
			}
		}
		return true
	}
	return false
}
