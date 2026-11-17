// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Write cycles of entry lists: add with generated values and
// prerequisite entries, read back, update, delete.

package verify

import (
	"fmt"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// writeValue turns a read value into its write form: strings stay, select
// maps become comma-separated selected keys, containers recurse.
func writeValue(v any) any {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var keys []string
		for i, o := range t {
			if sel, _ := o.(map[string]any)["selected"].(float64); sel == 1 {
				keys = append(keys, fmt.Sprint(i))
			}
		}
		return strings.Join(keys, ",")
	case map[string]any:
		if isSelect(t) {
			var keys []string
			for k, o := range t {
				if sel, _ := o.(map[string]any)["selected"].(float64); sel == 1 {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			return strings.Join(keys, ",")
		}
		out := map[string]any{}
		for k, c := range t {
			if !strings.HasPrefix(k, "%") {
				out[k] = writeValue(c)
			}
		}
		return out
	}
	return ""
}

// writeCycles: for every entry list with get/add/del, add an entry, read
// it back, change one field, read it back again and delete it.
//
// The entry starts from the server defaults (the empty-entry template),
// with a description where the model has one. When the instance rejects
// fields, each rejected field gets a generated value (values.go) and the
// add is retried. A rejected relation without anything to point at gets a
// prerequisite entry in the target list (created the same way, deleted
// afterwards). A field whose candidates are exhausted makes the cycle
// "skipped" with the reason. Nothing is applied (no reconfigure), so the
// running test instance is not changed beyond its stored configuration.
func writeCycles(c *Client, rel *ir.Release, models map[string]*model.Model, packages map[string]bool) []*Result {
	w := &writer{c: c, byList: map[string]*family{}}
	fams, keys := cycleFamilies(rel, models, packages)
	// prerequisites: the first family of a list (in key order) serves
	for _, k := range keys {
		f := fams[k]
		if lk := listKey(f.add.Model, f.add.ModelPath); w.byList[lk] == nil {
			w.byList[lk] = f
		}
	}
	var out []*Result
	for _, k := range keys {
		out = append(out, w.cycle(fams[k]))
	}
	return out
}

// cycleFamilies returns the entry lists to write-test, keyed
// "<module>/<controller> <model path>[@<family>]", and the keys in order.
func cycleFamilies(rel *ir.Release, models map[string]*model.Model, packages map[string]bool) (map[string]*family, []string) {
	// endpoints per entry list; several families of endpoints on one list
	// (bind primary/secondary/forward domains) get one cycle each, split by
	// the same rule as the capability data (ir.Families)
	groups := map[string][]*ir.Endpoint{}
	gets := map[string][]*ir.Endpoint{}
	for _, e := range rel.Endpoints {
		if e.ModelPath == "" || e.Source != ir.SourceBaseCall || !packages[e.Package] {
			continue
		}
		k := e.Module + "/" + e.Controller + " " + e.ModelPath
		switch e.Kind {
		case ir.KindItemGet:
			gets[k] = append(gets[k], e)
			groups[k] = append(groups[k], e)
		case ir.KindItemAdd, ir.KindItemDel, ir.KindItemSet:
			groups[k] = append(groups[k], e)
		}
	}
	fams := map[string]*family{}
	opOf := func(e *ir.Endpoint) string { return strings.TrimPrefix(string(e.Kind), "item.") }
	byPath := func(a, b *ir.Endpoint) bool { return a.Path < b.Path }
	for k, eps := range groups {
		for _, fm := range ir.Families(eps, opOf, byPath) {
			f := &family{gets: gets[k], add: fm.Chosen["add"], del: fm.Chosen["del"], set: fm.Chosen["set"]}
			key := k
			if fm.Name != "" {
				key += "@" + fm.Name
			}
			fams[key] = f
		}
	}
	var keys []string
	for k, f := range fams {
		if f.add == nil || f.del == nil {
			continue
		}
		if f.get = entryGet(f.gets, f.add); f.get == nil {
			continue
		}
		if m := models[f.add.Model]; m != nil {
			f.entry = m.Find(f.add.ModelPath)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return fams, keys
}

type family struct {
	gets               []*ir.Endpoint
	get, add, del, set *ir.Endpoint
	entry              *model.Node
}

type writer struct {
	c      *Client
	byList map[string]*family // model class + "#" + entry list path
}

// maxRounds bounds the add retries of one entry list; maxDepth bounds
// prerequisite chains (a record needs a zone, a zone needs nothing).
const (
	maxRounds = 8
	maxDepth  = 2
)

// added is the outcome of adding one entry.
type added struct {
	uuid     string
	body     map[string]any
	chosen   map[string]bool // fields set on purpose (compared on read-back)
	tried    map[string]int  // generated fields -> candidate index
	runtime  []string        // fields rejected with server defaults
	notes    []string
	cleanups []func() []string // delete prerequisites, newest first
	err      *Result           // set when the add failed or was skipped
}

func (w *writer) add(f *family, depth int) *added {
	c := w.c
	res := &added{chosen: map[string]bool{}, tried: map[string]int{}}
	r := &Result{Path: f.add.Path, Method: "POST", Check: CheckWriteCycle}
	code, v, raw, err := c.do("GET", f.get.Path, nil)
	if err != nil || code != 200 {
		res.err = fail(r, code, raw, err)
		return res
	}
	root, _ := v.(map[string]any)
	if f.get.Outer != "" {
		root, _ = root[f.get.Outer].(map[string]any)
	}
	tmpl, _ := root[f.get.Wrapper].(map[string]any)
	body, _ := writeValue(tmpl).(map[string]any)
	if body == nil {
		body = map[string]any{}
	}
	if f.entry != nil {
		for _, ch := range f.entry.Children {
			if ch.Kind == model.KindField && (ch.Type == "DescriptionField" || ch.Name == "description" || ch.Name == "descr") {
				if cands := candidates(ch, nil); len(cands) > 0 {
					body[ch.Name] = cands[0]
					res.chosen[ch.Name] = true
				}
			}
		}
	}
	prefix := f.add.Wrapper + "."
	related := map[string]bool{}
	for round := 0; ; round++ {
		code, v, raw, err = c.do("POST", f.add.Path, map[string]any{f.add.Wrapper: body})
		if err != nil || code != 200 {
			res.err = fail(r, code, raw, err)
			return res
		}
		if round == 0 && depth == 0 {
			c.schema(f.add, "POST", f.add.Path, v)
		}
		out, _ := v.(map[string]any)
		if id, _ := out["uuid"].(string); out["result"] == "saved" && id != "" {
			res.uuid, res.body = id, body
			return res
		}
		vals, _ := out["validations"].(map[string]any)
		if len(vals) == 0 {
			r.Status, r.Findings = StatusError, []string{"add failed without validation messages: " + trunc(raw)}
			res.err = r
			return res
		}
		var fields []string
		for k := range vals {
			fields = append(fields, strings.TrimPrefix(k, prefix))
		}
		sort.Strings(fields)
		if round == 0 {
			res.runtime = fields
		}
		var stuck []string
		for _, fld := range fields {
			n := node(f.entry, fld)
			cands := candidates(n, lookup(tmpl, fld))
			i, seen := res.tried[fld]
			if seen {
				i++
			}
			if i < len(cands) {
				res.tried[fld] = i
				res.chosen[fld] = true
				assign(body, fld, cands[i])
				continue
			}
			if !related[fld] && depth < maxDepth {
				if id, note, cleanup := w.prerequisite(n, depth+1); id != "" {
					related[fld] = true
					res.tried[fld] = len(cands)
					res.chosen[fld] = true
					res.notes = append(res.notes, note)
					res.cleanups = append([]func() []string{cleanup}, res.cleanups...)
					assign(body, fld, id)
					continue
				}
			}
			stuck = append(stuck, fmt.Sprintf("%s (%v)", fld, vals[prefix+fld]))
		}
		if len(stuck) > 0 || round+1 >= maxRounds {
			r.Status = StatusSkipped
			reason := "no accepted value for: " + strings.Join(stuck, "; ")
			if len(stuck) == 0 {
				reason = fmt.Sprintf("not accepted after %d rounds: %s", maxRounds, strings.Join(fields, ", "))
			}
			r.Findings = append(r.Findings, "add rejected; runtime-required: "+strings.Join(res.runtime, ", "), reason)
			r.RuntimeRequired = res.runtime
			res.err = r
			return res
		}
	}
}

// prerequisite creates an entry in the list a relation field points to and
// returns its uuid, a note and the function that deletes it again.
// listKey identifies an entry list by model class and item path. Relations
// name the class with dots and in any case ("OPNsense.quagga.ospf" for
// OPNsense\Quagga\OSPF); PHP class names are case-insensitive.
func listKey(class, items string) string {
	return strings.ToLower(strings.ReplaceAll(class, ".", `\`)) + "#" + items
}

func (w *writer) prerequisite(n *model.Node, depth int) (string, string, func() []string) {
	if n == nil {
		return "", "", nil
	}
	for _, rel := range n.Relations {
		f := w.byList[listKey(rel.Source, rel.Items)]
		if f == nil {
			continue
		}
		a := w.add(f, depth)
		if a.uuid == "" {
			for _, cl := range a.cleanups {
				cl()
			}
			continue
		}
		cleanup := func() []string {
			var fs []string
			if code, _, raw, err := w.c.do("POST", f.del.Path+"/"+a.uuid, map[string]any{}); err != nil || code != 200 {
				fs = append(fs, fmt.Sprintf("deleting prerequisite %s/%s: HTTP %d %s", f.del.Path, a.uuid, code, trunc(raw)))
			}
			for _, cl := range a.cleanups {
				fs = append(fs, cl()...)
			}
			return fs
		}
		return a.uuid, "prerequisite entry created via " + f.add.Path, cleanup
	}
	return "", "", nil
}

func (w *writer) cycle(f *family) *Result {
	c := w.c
	a := w.add(f, 0)
	defer func() {
		for _, cl := range a.cleanups {
			cl()
		}
	}()
	if a.err != nil {
		if a.err.Status == StatusSkipped {
			if nm := notRequiredInModel(f.entry, a.runtime); len(nm) > 0 {
				a.err.Notes = append(a.err.Notes, "required at runtime but not marked Required in the model: "+strings.Join(nm, ", "))
			}
		}
		return a.err
	}
	r := &Result{Path: f.add.Path, Method: "POST", Check: CheckWriteCycle, HTTP: 200, RuntimeRequired: a.runtime}
	if nm := notRequiredInModel(f.entry, a.runtime); len(nm) > 0 {
		r.Notes = append(r.Notes, "required at runtime but not marked Required in the model: "+strings.Join(nm, ", "))
	}
	r.Notes = append(r.Notes, a.notes...)
	for fld := range a.tried {
		r.Generated = append(r.Generated, fld)
	}
	sort.Strings(r.Generated)
	r.Sample = a.body
	r.Steps = append(r.Steps, "add")
	uuid, body := a.uuid, a.body

	// read back what we chose (values the server changes itself, such as
	// ids, audit fields or serials, are not writes of ours)
	sent := map[string]any{}
	for fld := range a.chosen {
		if !writeOnly(node(f.entry, fld)) {
			assign(sent, fld, lookup(body, fld))
		}
	}
	if fs := readBack(c, f.get, uuid, sent, "add"); len(fs) > 0 {
		r.Findings = append(r.Findings, fs...)
	} else {
		r.Steps = append(r.Steps, "read-back")
	}

	// change one field and read it back
	if f.set == nil {
		r.Notes = append(r.Notes, "update not tested (no set endpoint)")
	} else if fld, val := changeable(f.entry, body, a.chosen); fld == "" {
		r.Notes = append(r.Notes, "update not tested (no field to change)")
	} else {
		code, v, raw, err := c.do("POST", f.set.Path+"/"+uuid, map[string]any{f.set.Wrapper: map[string]any{fld: val}})
		res, _ := v.(map[string]any)
		switch {
		case err != nil || code != 200:
			r.Findings = append(r.Findings, fmt.Sprintf("set: HTTP %d %s", code, trunc(raw)))
		case res["result"] != "saved" && res["validations"] != nil:
			// the changed value broke a rule involving other fields; this
			// says nothing about the endpoint, but update is not verified
			r.Notes = append(r.Notes, fmt.Sprintf("update not tested (%s=%q rejected: %s)", fld, val, trunc(raw)))
		case res["result"] != "saved":
			r.Findings = append(r.Findings, "set "+fld+": "+trunc(raw))
		default:
			c.schema(f.set, "POST", f.set.Path+"/{uuid}", v)
			r.Steps = append(r.Steps, "set")
			if fs := readBack(c, f.get, uuid, map[string]any{fld: val}, "set"); len(fs) > 0 {
				r.Findings = append(r.Findings, fs...)
			} else {
				r.Steps = append(r.Steps, "read-back-set")
			}
		}
	}

	code, v, raw, err := c.do("POST", f.del.Path+"/"+uuid, map[string]any{})
	if code == 500 && strings.Contains(string(raw), "locked") && lookup(body, "lock") == "1" && f.set != nil {
		// e.g. interface assignments: "Interface locked, unset lock first
		// before removal"; lock defaults to 1. Clients must unlock first.
		if c2, v2, _, _ := c.do("POST", f.set.Path+"/"+uuid, map[string]any{f.set.Wrapper: map[string]any{"lock": "0"}}); c2 == 200 {
			if res2, _ := v2.(map[string]any); res2["result"] == "saved" {
				r.Notes = append(r.Notes, "delete refused while lock=1 (the default); deleted after setting lock=0")
				code, v, raw, err = c.do("POST", f.del.Path+"/"+uuid, map[string]any{})
			}
		}
	}
	res, _ := v.(map[string]any)
	switch {
	case err != nil || code != 200:
		r.Findings = append(r.Findings, fmt.Sprintf("del: HTTP %d %s", code, trunc(raw)))
	case gap.Has(f.del.Gaps, gap.ResultNotReturned) && len(raw) == 0:
		r.Steps = append(r.Steps, "del") // empty answer, as the spec says
	case res["result"] != "deleted":
		r.Findings = append(r.Findings, "del: "+trunc(raw))
	default:
		c.schema(f.del, "POST", f.del.Path+"/{uuid}", v)
		r.Steps = append(r.Steps, "del")
	}
	for _, cl := range a.cleanups {
		r.Findings = append(r.Findings, cl()...)
	}
	a.cleanups = nil
	r.Status = status(r.Findings)
	if r.Status == StatusMismatch {
		r = knownGap(r, f.get)
	}
	return r
}

// writeOnly: fields the server never returns (passwords and the like).
func writeOnly(n *model.Node) bool {
	return n != nil && (n.BaseType == "UpdateOnlyTextField" || n.Type == "UpdateOnlyTextField")
}

// readBack gets the entry and compares every sent field (write form).
// Differences are findings prefixed "read-back".
func readBack(c *Client, get *ir.Endpoint, uuid string, sent map[string]any, step string) []string {
	code, v, raw, err := c.do("GET", get.Path+"/"+uuid, nil)
	if err != nil || code != 200 {
		return []string{fmt.Sprintf("get/%s after %s: HTTP %d %s", uuid, step, code, trunc(raw))}
	}
	c.schema(get, "GET", get.Path+"/{uuid}", v)
	root, _ := v.(map[string]any)
	if get.Outer != "" {
		root, _ = root[get.Outer].(map[string]any)
	}
	got, ok := root[get.Wrapper].(map[string]any)
	if !ok {
		return []string{"get by uuid: root key " + get.Wrapper + " missing"}
	}
	var out []string
	var walk func(prefix string, s map[string]any)
	walk = func(prefix string, s map[string]any) {
		var ks []string
		for k := range s {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			p := strings.TrimPrefix(prefix+"."+k, ".")
			if sub, ok := s[k].(map[string]any); ok {
				walk(p, sub)
				continue
			}
			want, _ := s[k].(string)
			have := writeValue(lookup(got, p))
			if hs, ok := have.(string); !ok || !sameValue(want, hs) {
				out = append(out, fmt.Sprintf("read-back after %s: %s sent %q, read %v", step, p, want, have))
			}
		}
	}
	walk("", sent)
	return out
}

// sameValue compares written and read values; comma-separated lists are
// compared as sets (the server may reorder them).
func sameValue(a, b string) bool {
	if a == b {
		return true
	}
	if !strings.Contains(a, ",") && !strings.Contains(b, ",") {
		return false
	}
	as, bs := strings.Split(a, ","), strings.Split(b, ",")
	sort.Strings(as)
	sort.Strings(bs)
	return strings.Join(as, ",") == strings.Join(bs, ",")
}

// changeable picks a field for the set step and a new value that satisfies
// the field's mask: the description first, else another field we chose.
func changeable(entry *model.Node, body map[string]any, chosen map[string]bool) (string, string) {
	var order []string
	for _, name := range []string{"description", "descr"} {
		if chosen[name] {
			order = append(order, name)
		}
	}
	var rest []string
	for f := range chosen {
		if f != "description" && f != "descr" && !strings.Contains(f, ".") {
			rest = append(rest, f)
		}
	}
	sort.Strings(rest)
	for _, f := range append(order, rest...) {
		n := node(entry, f)
		if writeOnly(n) {
			continue
		}
		cur, _ := lookup(body, f).(string)
		for _, cand := range candidates(n, nil) {
			if cand != cur {
				return f, cand
			}
		}
	}
	return "", ""
}

func notRequiredInModel(entry *model.Node, fields []string) []string {
	var out []string
	for _, f := range fields {
		n := node(entry, f)
		if n == nil || !(n.Required && n.Default == nil) {
			out = append(out, f)
		}
	}
	return out
}

// entryGet picks the get endpoint of an entry list: the one taking a uuid
// with the add endpoint's wrapper (os-wol has getHost and getwake for the
// same list; only getHost reads an entry).
func entryGet(gets []*ir.Endpoint, add *ir.Endpoint) *ir.Endpoint {
	var best *ir.Endpoint
	for _, g := range gets {
		takesUUID := len(g.Params) > 0
		switch {
		case g.Wrapper == add.Wrapper && takesUUID:
			return g
		case best == nil || (g.Wrapper == add.Wrapper && best.Wrapper != add.Wrapper):
			best = g
		}
	}
	return best
}
