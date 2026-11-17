// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Read checks: an endpoint answers, a read answer has the model's shape.

package verify

import (
	"errors"
	"fmt"
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

// compare the response object with the model node (Read shape)
func compareNode(n *model.Node, v any, path string, out *[]string) {
	obj, ok := v.(map[string]any)
	if arr, isArr := v.([]any); isArr && len(arr) == 0 {
		obj, ok = map[string]any{}, true // PHP encodes an empty object as []
		if len(n.Children) == 0 {
			return
		}
	}
	if !ok {
		*out = append(*out, fmt.Sprintf("%s: expected object, got %T", path, v))
		return
	}
	known := map[string]bool{}
	for _, ch := range n.Children {
		known[ch.Name] = true
		p := strings.TrimPrefix(path+"."+ch.Name, ".")
		val, present := obj[ch.Name]
		if !present {
			*out = append(*out, p+": missing in response")
			continue
		}
		switch ch.Kind {
		case model.KindContainer:
			compareNode(ch, val, p, out)
		case model.KindArray:
			if _, ok := val.(map[string]any); !ok {
				if arr, ok := val.([]any); !ok || len(arr) != 0 {
					*out = append(*out, fmt.Sprintf("%s: expected UUID map, got %T", p, val))
				}
			}
		case model.KindField:
			switch ch.Shape {
			case model.ShapeSelect:
				// map, or a JSON array when PHP keys are 0..n-1
				_, isMap := val.(map[string]any)
				_, isList := val.([]any)
				if !isMap && !isList {
					*out = append(*out, fmt.Sprintf("%s: expected select map, got %T", p, val))
				}
			case model.ShapeString:
				if fieldShapeUncertain(ch) {
					break
				}
				if _, ok := val.(string); !ok {
					*out = append(*out, fmt.Sprintf("%s: expected string, got %T", p, val))
				}
			}
		}
	}
	for k := range obj {
		if !known[k] && !strings.HasPrefix(k, "%") {
			*out = append(*out, strings.TrimPrefix(path+"."+k, ".")+": not in model")
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
		r.HTTP, r.Status, r.Findings = code, StatusMismatch, []string{err.Error()}
		return r // knownGap() turns this into known-gap for overridden actions
	}
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP = code
	c.schema(e, "GET", e.Path, v)
	obj, _ := v.(map[string]any)
	inner, ok := obj[e.Wrapper]
	if !ok {
		var keys []string
		for k := range obj {
			keys = append(keys, k)
		}
		r.Status, r.Findings = StatusMismatch, []string{fmt.Sprintf("root key %q missing, got %v", e.Wrapper, keys)}
		return r
	}
	if m == nil {
		r.Status, r.Findings = StatusSkipped, []string{"model unknown"}
		return r
	}
	compareNode(m.Root, inner, "", &r.Findings)
	r.Status = status(r.Findings)
	return r
}

// knownGap turns a mismatch into "known-gap" when the endpoint was already
// marked as overriding the generic action (the test confirms the gap).
func knownGap(r *Result, e *ir.Endpoint) *Result {
	if r.Status != StatusMismatch {
		return r
	}
	if g, ok := gap.First(e.Gaps, gap.MayDiffer); ok {
		r.Status = StatusKnownGap
		r.Findings = append(r.Findings, "expected ("+string(g.Code)+"): "+g.Detail)
	}
	return r
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
		r.Status, r.Findings = StatusMismatch, []string{fmt.Sprintf("root key %q missing", strings.TrimPrefix(e.Outer+"."+e.Wrapper, "."))}
		return r
	}
	if m != nil {
		if n := m.Find(e.ModelPath); n != nil {
			compareNode(&model.Node{Kind: model.KindContainer, Children: n.Children}, inner, e.ModelPath, &r.Findings)
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
