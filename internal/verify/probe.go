// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Field probes (ADR 0017): every claim the data makes about a field's
// values is tried on the firewall. Each probe is a value with the verdict
// the data predicts; the firewall's verdict must be the same, and a value
// it accepts must read back as sent.

package verify

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// Claim names what a probe tries. The same names count the claims in a
// report (Result.Claims).
type Claim string

const (
	ClaimOption     Claim = "option"     // a listed option is accepted
	ClaimComplete   Claim = "complete"   // a key outside a complete option list is rejected
	ClaimMultiple   Claim = "multiple"   // two options together are accepted
	ClaimSingle     Claim = "single"     // two options together are rejected
	ClaimRequired   Claim = "required"   // the empty value is rejected
	ClaimOptional   Claim = "optional"   // the empty value is accepted
	ClaimKeepsEmpty Claim = "empty-kept" // the empty value is accepted and leaves the value unchanged (empty_ignored)
	ClaimBoolean    Claim = "boolean"    // 0 and 1 accepted, anything else rejected
	ClaimInteger    Claim = "integer"    // a non-number is rejected
	ClaimMinimum    Claim = "minimum"    // MinimumValue accepted, one below rejected
	ClaimMaximum    Claim = "maximum"    // MaximumValue accepted, one above rejected
	ClaimMask       Claim = "mask"       // a matching value accepted, a non-matching one rejected
	ClaimForbidden  Claim = "forbidden"  // a value with a character the field forbids is rejected, one with an allowed character accepted
	// formats (model.Format)
	ClaimFormat         Claim = "format"          // a well-formed value accepted, a malformed one rejected
	ClaimFamily         Claim = "family"          // addresses of the field's families accepted, others rejected
	ClaimNetmask        Claim = "netmask"         // a prefix length forbidden, allowed or required
	ClaimStrict         Claim = "strict"          // address bits after the prefix length rejected when strict
	ClaimWildcard       Claim = "wildcard"        // "any" accepted or rejected
	ClaimIPAllowed      Claim = "ip-allowed"      // a host name field takes an IP address or not
	ClaimDNSName        Claim = "dns-name"        // labels beyond host names accepted or not
	ClaimHostWildcard   Claim = "host-wildcard"   // "*" accepted or not
	ClaimDomainWildcard Claim = "domain-wildcard" // "*.name" accepted or not
	ClaimZoneRoot       Claim = "zone-root"       // "@" accepted or not
	ClaimPortOptional   Claim = "port-optional"   // an address without port accepted or not
	ClaimHostname       Claim = "hostname"        // a host name instead of the address accepted or not
	ClaimService        Claim = "service"         // a port field's service names accepted
	ClaimRange          Claim = "range"           // port ranges accepted or not
)

// Outcome of one probe.
type Outcome string

const (
	OutcomeAgreed       Outcome = "agreed"        // the firewall judged as the data predicts
	OutcomeContradicted Outcome = "contradicted"  // the firewall judged otherwise
	OutcomeOtherFields  Outcome = "other-fields"  // rejected, but the messages name only other fields
	OutcomeNotReadBack  Outcome = "not-read-back" // accepted as predicted, but read back differently
	// judged otherwise, and the field has a rule over several fields
	// (model-validation) and no context of other values was found in
	// which its own values decide: no verdict about the field
	OutcomeExplained Outcome = "explained"
)

// Probe is one value written to one field.
type Probe struct {
	Field   string  `json:"field"`
	Value   string  `json:"value"`
	Claim   Claim   `json:"claim"`
	Accept  bool    `json:"accept"`         // the data's prediction
	Outcome Outcome `json:"outcome"`        // what the firewall did with it
	Read    *string `json:"read,omitempty"` // the value read back, when it differed
	// Context: values of other fields written with it, found because a
	// rule over several fields refused the field's values alone
	Context map[string]string `json:"context,omitempty"`
}

// Accepted is the firewall's verdict, derived from the prediction and the
// outcome (other-fields: no verdict on the value; false).
func (p Probe) Accepted() bool {
	switch p.Outcome {
	case OutcomeAgreed, OutcomeNotReadBack:
		return p.Accept
	case OutcomeContradicted:
		return !p.Accept
	}
	return false
}

var verdictWord = map[bool]string{true: "accepted", false: "rejected"}

// notAnOption is a key no option list contains.
const notAnOption = "contract-test-not-an-option"

// probes derives a field's probes from the data. offered: the option keys
// the firewall lists for it (from the read answer), used only for the
// multiple/single claim of a field whose options the data does not list.
// The second result names constraints the field has that no probe tries.
func probes(path string, n *model.Node, offered []string) ([]Probe, []string) {
	var out []Probe
	add := func(c Claim, v string, accept bool) {
		out = append(out, Probe{Field: path, Value: v, Claim: c, Accept: accept})
	}
	switch {
	case n.Required && !n.EmptyIgnored:
		add(ClaimRequired, "", false)
	case n.EmptyIgnored:
		add(ClaimKeepsEmpty, "", true)
	default:
		add(ClaimOptional, "", true)
	}
	if n.Shape == model.ShapeSelect {
		var keys []string
		if !gap.Has(n.Gaps, gap.OptionsDynamic) {
			for _, o := range n.Options {
				if o.Value != "" {
					keys = append(keys, o.Value)
					add(ClaimOption, o.Value, true)
				}
			}
		}
		if completeOptions(n) {
			add(ClaimComplete, notAnOption, false)
		}
		if len(keys) < 2 {
			keys = slices.DeleteFunc(slices.Clone(offered), func(k string) bool { return k == "" })
		}
		if len(keys) >= 2 && n.List == nil || len(keys) >= 2 && n.Multiple {
			if n.Multiple {
				add(ClaimMultiple, keys[0]+","+keys[1], true)
			} else {
				add(ClaimSingle, keys[0]+","+keys[1], false)
			}
		}
	}
	var unprobed []string
	for k := range n.Props {
		if !model.UIText[k] {
			unprobed = append(unprobed, k)
		}
	}
	known := func(keys ...string) {
		unprobed = slices.DeleteFunc(unprobed, func(k string) bool { return slices.Contains(keys, k) })
	}
	if n.BaseType == "BooleanField" {
		add(ClaimBoolean, "0", true)
		add(ClaimBoolean, "1", true)
		add(ClaimBoolean, "2", false)
	}
	if n.BaseType == "IntegerField" {
		add(ClaimInteger, "contract-test", false)
	}
	// the limits the data publishes (model.Limits), and only those
	if l := n.Limits(); l != nil {
		if l.Minimum != nil {
			add(ClaimMinimum, strconv.FormatInt(*l.Minimum, 10), true)
			add(ClaimMinimum, strconv.FormatInt(*l.Minimum-1, 10), false)
			known("MinimumValue")
		}
		if l.Maximum != nil {
			add(ClaimMaximum, strconv.FormatInt(*l.Maximum, 10), true)
			add(ClaimMaximum, strconv.FormatInt(*l.Maximum+1, 10), false)
			known("MaximumValue")
		}
		if l.Pattern != "" {
			match, miss := maskValues(n, l)
			if match != "" && miss != "" {
				add(ClaimMask, match, true)
				add(ClaimMask, miss, false)
				known("Mask")
			}
		}
		if l.Format != nil {
			for _, v := range formatVectors(l.Format, n.List) {
				add(v.claim, v.value, v.accept)
			}
			known(n.Checked...)
			known("AsList", "FieldSeparator")
		}
		if forbidden(n, l, add) {
			known("AllowSpaces", "AllowNewlines", "AllowSpecial")
		}
	}
	sort.Strings(unprobed)
	return out, unprobed
}

// forbidden: for each character group of TextField's Allow* settings, a
// value with such a character; rejected when the field forbids it. Only
// where the pattern lets the value pass, so that the pattern does not
// decide instead.
func forbidden(n *model.Node, l *model.Limits, add func(Claim, string, bool)) bool {
	if !slices.ContainsFunc(n.Checked, func(s string) bool { return strings.HasPrefix(s, "Allow") }) && l.Forbidden == "" {
		return false
	}
	text := *l
	text.Forbidden = ""
	for _, c := range []string{" ", "\n", "\v"} {
		v := "ct" + c + "x"
		if !text.TextAccepts(v) {
			continue // the pattern decides
		}
		if c == "\v" && !strings.Contains(l.Forbidden, c) {
			continue // taken, but the configuration (XML) cannot hold it
		}
		add(ClaimForbidden, v, !strings.Contains(l.Forbidden, c))
	}
	return true
}

// maskValues: a value the pattern accepts (from the generated
// candidates) and one it refuses, both free of spaces and control
// characters, so that the TextField's own checks do not decide instead of
// the pattern; the refused one is the longest when only the length limits
// it.
func maskValues(n *model.Node, l *model.Limits) (string, string) {
	var yes, no string
	for _, v := range append(candidates(n, nil), texts()...) {
		if v == "" || strings.ContainsAny(v, " \t\r\n") {
			continue
		}
		if l.TextAccepts(v) && yes == "" {
			yes = v
		}
	}
	long := 256
	if l.MaxLength != nil {
		long = int(*l.MaxLength) + 1
	}
	for _, v := range []string{"!", "contract-test!", "x", "_", "0", "-", "ct1.example.com", "contract-test", strings.Repeat("x", long)} {
		if !l.TextAccepts(v) {
			no = v
			break
		}
	}
	return yes, no
}

// probeField is a field of the probed object.
type probeField struct {
	path string
	node *model.Node
}

// probeFields: the fields of an object that take values: below
// containers, not in entry lists, not filled by the firewall, stored.
func probeFields(n *model.Node, prefix string) []probeField {
	var out []probeField
	for _, ch := range n.Children {
		p := ch.Name
		if prefix != "" {
			p = prefix + "." + ch.Name
		}
		switch ch.Kind {
		case model.KindContainer:
			out = append(out, probeFields(ch, p)...)
		case model.KindField:
			if ch.ServerSet() || ch.Volatile || fieldShapeUncertain(ch) {
				continue
			}
			out = append(out, probeField{p, ch})
		}
	}
	return out
}

// answer is what a write of fields answered.
type answer struct {
	saved   bool
	own     map[string]bool // written fields the validation messages name
	others  []string        // other fields they name
	err     string          // neither a save nor a validation: the probing stops
	code    int
	raw     []byte
	nonJSON *NonJSON
	httpErr error
}

// postFields writes fields (dotted paths below the wrapper) to path.
func postFields(c *Client, path, wrapper string, vals map[string]string) answer {
	body := map[string]any{}
	for p, v := range vals {
		assign(body, p, v)
	}
	code, v, raw, err := c.do("POST", path, map[string]any{wrapper: body})
	a := answer{code: code, raw: raw}
	if errors.As(err, &a.nonJSON) {
		a.err = "answers with " + err.Error()
		return a
	}
	if err != nil || code != 200 {
		a.httpErr, a.err = err, fmt.Sprintf("HTTP %d %s", code, trunc(raw))
		return a
	}
	out, _ := v.(map[string]any)
	if res, _ := out["result"].(string); res == "saved" {
		a.saved = true
		return a
	}
	msgs := asMap(out["validations"])
	if len(msgs) == 0 {
		a.err = "answers " + trunc(raw)
		return a
	}
	a.own = map[string]bool{}
	for k := range msgs {
		f := strings.TrimPrefix(k, wrapper+".")
		if _, written := vals[f]; written {
			a.own[f] = true
		} else {
			a.others = append(a.others, f)
		}
	}
	sort.Strings(a.others)
	return a
}

// target is the object probed: a settings model or one entry.
type target struct {
	write func(vals map[string]string) answer
	read  func() (map[string]any, error)
	// opaqueRead: the get is the controller's own code (its shape not
	// derived, override-not-derived): what it shows is not the stored
	// value, so accepted values are not compared on read
	opaqueRead bool
}

func settingsTarget(c *Client, get, set *ir.Endpoint) target {
	return target{
		opaqueRead: gap.Has(get.Gaps, gap.OverrideNotDerived),
		write:      func(vals map[string]string) answer { return postFields(c, set.Path, set.Wrapper, vals) },
		read: func() (map[string]any, error) {
			code, v, raw, err := c.do("GET", get.Path, nil)
			if err != nil || code != 200 {
				return nil, fmt.Errorf("HTTP %d %s", code, trunc(raw))
			}
			obj, ok := settingsObject(v, get.Wrapper)
			if !ok {
				return nil, fmt.Errorf("root key %s missing", get.Wrapper)
			}
			return obj, nil
		},
	}
}

func entryTarget(c *Client, get, set *ir.Endpoint, uuid string) target {
	return target{
		opaqueRead: gap.Has(get.Gaps, gap.OverrideNotDerived),
		write:      func(vals map[string]string) answer { return postFields(c, set.Path+"/"+uuid, set.Wrapper, vals) },
		read:       func() (map[string]any, error) { return getEntry(c, get, uuid) },
	}
}

// probeResult probes the fields of root on t: one field-probe result on
// path. Before the first probe the current values are written as they
// are; an object that does not take them back is not probed.
func probeResult(set *ir.Endpoint, t target, root *model.Node, gaps ...[]gap.Gap) *Result {
	path := set.Path
	r := &Result{Path: path, Method: "POST", Check: CheckFieldProbe}
	if root == nil {
		r.Status, r.Findings = StatusSkipped, []string{"model unknown"}
		return r
	}
	// fields the endpoint sets itself: a value sent has no effect
	fields := slices.DeleteFunc(probeFields(root, ""), func(f probeField) bool {
		_, forced := set.Forced[f.path]
		return forced || slices.Contains(set.Computed, f.path)
	})
	cur, err := t.read()
	if err != nil {
		r.Status, r.Findings = StatusSkipped, []string{"not probed: read: " + err.Error()}
		return r
	}
	same := map[string]string{}
	for _, f := range fields {
		if !f.node.WriteOnly() { // reads empty; writing that back would clear it
			same[f.path] = wireString(lookup(cur, f.path))
		}
	}
	switch a := t.write(same); {
	case a.err != "":
		r.Status, r.Findings = StatusSkipped, []string{"not probed: set " + a.err}
		return r
	case !a.saved:
		r.Status, r.Findings = StatusSkipped, []string{"not probed: the current values are rejected for: " + strings.Join(append(slices.Sorted(maps.Keys(a.own)), a.others...), ", ")}
		return r
	}
	r.HTTP = 200
	var explained []string
	r.Probes, r.Findings, explained, r.Notes, r.Unprobed = probeRun(t, fields)
	r.Status = status(r.Findings)
	if r.Status == StatusOK && len(explained) > 0 {
		r.Status, r.Findings = StatusKnownGap, explained
	} else {
		r.Notes = append(r.Notes, explained...)
	}
	if len(r.Probes) == 0 && r.Status == StatusOK {
		r.Status, r.Findings = StatusSkipped, []string{"no field to probe"}
	}
	if r.Status == StatusMismatch {
		for _, gs := range gaps {
			if g, ok := gap.First(gs, func(c gap.Code) bool { return c == gap.CustomSave }); ok {
				r.Status = StatusKnownGap
				r.Findings = append(r.Findings, "expected ("+string(g.Code)+"): "+g.Detail)
				break
			}
		}
	}
	return r
}

// probeRun tries every probe of the fields on t and returns them with
// their outcomes, plus findings and notes for the result.
func probeRun(t target, fields []probeField) (done []Probe, findings, explained, notes []string, unprobed map[string]int) {
	orig, err := t.read()
	if err != nil {
		return nil, []string{"read before probing: " + err.Error()}, nil, nil, nil
	}
	unprobed = map[string]int{}
	nodes := map[string]*model.Node{}
	queue := map[string][]Probe{}
	var order []string
	for _, f := range fields {
		ps, up := probes(f.path, f.node, optionKeys(lookup(orig, f.path)))
		for _, k := range up {
			unprobed[k]++
		}
		if len(ps) == 0 {
			continue
		}
		nodes[f.path] = f.node
		queue[f.path] = ps
		order = append(order, f.path)
	}
	stop := ""
	// rounds: the n-th probe of each field, predicted accepted ones and
	// predicted rejected ones apart (a rejection spoils a save)
	for i := 0; stop == ""; i++ {
		round := map[bool][]Probe{}
		for _, p := range order {
			if i < len(queue[p]) {
				pr := queue[p][i]
				round[pr.Accept] = append(round[pr.Accept], pr)
			}
		}
		if len(round[true]) == 0 && len(round[false]) == 0 {
			break
		}
		for _, accept := range []bool{true, false} {
			if stop != "" || len(round[accept]) == 0 {
				continue
			}
			var res []Probe
			res, stop = t.judge(round[accept], nodes)
			done = append(done, res...)
		}
	}
	// a field with a rule over several fields that refused a value
	// predicted to be accepted: probe it again in a context of other
	// values
	ctxFields := map[string]bool{}
	if stop == "" {
		var tried []map[string]string
		for _, p := range order {
			if !gap.Has(nodes[p].Gaps, gap.ModelValidation) {
				continue
			}
			refusedValue, ok := firstRefused(done, p)
			if !ok {
				continue
			}
			ctx, msg := t.findContext(p, refusedValue, fields, orig, &tried)
			if msg != "" {
				stop = msg
				break
			}
			if ctx == nil {
				notes = append(notes, fmt.Sprintf("%s: no context of other values found in which its values decide", p))
				continue
			}
			for k := range ctx {
				ctxFields[k] = true
			}
			done = slices.DeleteFunc(done, func(d Probe) bool { return d.Field == p })
			for _, pr := range queue[p] {
				res, msg := t.judgeIn(pr, ctx, nodes[p])
				if msg != "" {
					stop = msg
					break
				}
				done = append(done, res)
			}
			// the field and its context back as they were, so that the
			// next field's search starts from the same object (an ICMP
			// type left set refuses a port with protocol TCP)
			back := map[string]string{p: wireString(lookup(orig, p))}
			for k := range ctx {
				back[k] = wireString(lookup(orig, k))
			}
			if stop == "" && !nodes[p].WriteOnly() {
				if a := t.write(back); !a.saved {
					notes = append(notes, fmt.Sprintf("%s: original values not written back after probing in a context", p))
				}
			}
		}
	}
	if stop != "" {
		findings = append(findings, stop)
	}
	// write the original values back, all at once, else one by one
	back := map[string]string{}
	for _, p := range order {
		if !nodes[p].WriteOnly() {
			back[p] = wireString(lookup(orig, p))
		}
	}
	for p := range ctxFields {
		back[p] = wireString(lookup(orig, p))
	}
	if len(back) > 0 && stop == "" {
		if a := t.write(back); !a.saved {
			for _, p := range slices.Sorted(maps.Keys(back)) {
				if a := t.write(map[string]string{p: back[p]}); !a.saved {
					notes = append(notes, fmt.Sprintf("original value of %s not written back (%q)", p, back[p]))
				}
			}
		}
	}
	reasons := map[string]bool{}
	for i, p := range done {
		// a rule of the model over several fields decides, and the data
		// says so (model-validation): the difference is explained
		to := &findings
		if g, ok := gap.First(nodes[p.Field].Gaps, func(c gap.Code) bool { return c == gap.ModelValidation }); ok {
			to = &explained
			if p.Outcome == OutcomeContradicted || p.Outcome == OutcomeNotReadBack {
				reasons["expected ("+string(g.Code)+"): "+g.Detail] = true
				done[i].Outcome = OutcomeExplained
			}
		}
		switch p.Outcome {
		case OutcomeContradicted:
			*to = append(*to, fmt.Sprintf("%s=%q: the data predicts %s (%s), the firewall %s it", p.Field, p.Value, verdictWord[p.Accept], p.Claim, verdictWord[!p.Accept]))
		case OutcomeNotReadBack:
			*to = append(*to, fmt.Sprintf("%s=%q (%s) was accepted but reads back as %q", p.Field, p.Value, p.Claim, *p.Read))
		case OutcomeOtherFields:
			notes = append(notes, fmt.Sprintf("%s=%q (%s): rejected for other fields; a rule over several fields", p.Field, p.Value, p.Claim))
		}
	}
	explained = append(explained, slices.Sorted(maps.Keys(reasons))...)
	return done, findings, explained, notes, unprobed
}

// firstRefused: the first value of the field predicted to be accepted
// that the firewall refused.
func firstRefused(done []Probe, field string) (string, bool) {
	for _, p := range done {
		if p.Field == field && p.Accept && p.Outcome == OutcomeContradicted {
			return p.Value, true
		}
	}
	return "", false
}

// maxContextTries bounds the search for a context per field (a protocol
// field offers some 140 options).
const maxContextTries = 300

// findContext looks for values of other fields of the object (single
// selects with model-validation; the options the firewall offers, else the
// data's) with which
// value is saved: first the contexts that worked for other fields, then
// one other field at a time, each of its options. The second result stops
// the probing.
func (t target) findContext(field, value string, fields []probeField, orig map[string]any, tried *[]map[string]string) (map[string]string, string) {
	try := func(ctx map[string]string) (bool, string) {
		vals := maps.Clone(ctx)
		vals[field] = value
		a := t.write(vals)
		return a.saved, a.err
	}
	for _, ctx := range *tried {
		if ok, msg := try(ctx); msg != "" || ok {
			return ctx, msg
		}
	}
	// one more field on top of nothing, or of a context that worked for
	// another field (a redirect port needs protocol TCP and no "no rdr")
	bases := append([]map[string]string{{}}, *tried...)
	n := 0
	for _, base := range bases {
		for _, f := range fields {
			o := f.node
			if f.path == field || base[f.path] != "" || !contextField(o) {
				continue
			}
			// the original value too: earlier probes may have left the
			// field at another one
			for _, opt := range contextValues(o, lookup(orig, f.path)) {
				if opt == "" {
					continue
				}
				if n++; n > maxContextTries {
					return nil, ""
				}
				ctx := maps.Clone(base)
				ctx[f.path] = opt
				ok, msg := try(ctx)
				if msg != "" {
					return nil, msg
				}
				if ok {
					*tried = append(*tried, ctx)
					return ctx, ""
				}
			}
		}
	}
	return nil, ""
}

// contextField: a field that can make a context: one the rule over
// several fields names (it carries model-validation itself), a single
// select or a boolean.
func contextField(o *model.Node) bool {
	return gap.Has(o.Gaps, gap.ModelValidation) && !o.Multiple &&
		(o.Shape == model.ShapeSelect || o.BaseType == "BooleanField")
}

// contextValues: the values a context field can take: a boolean's, the
// options the firewall offers, else the data's.
func contextValues(o *model.Node, read any) []string {
	if o.BaseType == "BooleanField" {
		return []string{"0", "1"}
	}
	opts := optionKeys(read)
	if len(opts) == 0 {
		for _, op := range o.Options {
			opts = append(opts, op.Value)
		}
	}
	return opts
}

// refused: the outcome of a probe whose value the firewall refused for
// its own field.
func refused(p Probe) Outcome {
	return map[bool]Outcome{true: OutcomeContradicted, false: OutcomeAgreed}[p.Accept]
}

// judgeIn writes one probe together with its context.
func (t target) judgeIn(p Probe, ctx map[string]string, n *model.Node) (Probe, string) {
	vals := maps.Clone(ctx)
	vals[p.Field] = p.Value
	p.Context = ctx
	a := t.write(vals)
	switch {
	case a.err != "":
		return p, a.err
	case a.saved && p.Accept:
		got, err := t.read()
		if err != nil {
			return p, "read-back: " + err.Error()
		}
		return t.readBack(p, n, got), ""
	case a.saved:
		p.Outcome = OutcomeContradicted
	case a.own[p.Field]:
		p.Outcome = refused(p)
	default:
		p.Outcome = OutcomeOtherFields
	}
	return p, ""
}

// judge writes one round of probes with the same prediction, and every
// probe whose verdict a whole round cannot show alone. The second result
// stops the probing (an answer that is not a save or a validation).
func (t target) judge(round []Probe, nodes map[string]*model.Node) ([]Probe, string) {
	accept := round[0].Accept
	vals := map[string]string{}
	for _, p := range round {
		vals[p.Field] = p.Value
	}
	a := t.write(vals)
	if a.err != "" {
		return nil, a.err
	}
	var alone []Probe
	var out []Probe
	switch {
	case accept && a.saved:
		got, err := t.read()
		if err != nil {
			return nil, "read-back: " + err.Error()
		}
		for _, p := range round {
			out = append(out, t.readBack(p, nodes[p.Field], got))
		}
		return out, ""
	case accept:
		// some value spoiled the save: each again on its own
		alone = round
	case a.saved:
		// predicted rejections all accepted: confirm each on its own
		alone = round
	default:
		for _, p := range round {
			if a.own[p.Field] {
				p.Outcome = OutcomeAgreed
				out = append(out, p)
			} else {
				alone = append(alone, p)
			}
		}
	}
	for _, p := range alone {
		b := t.write(map[string]string{p.Field: p.Value})
		switch {
		case b.err != "":
			return out, b.err
		case b.saved && p.Accept:
			got, err := t.read()
			if err != nil {
				return out, "read-back: " + err.Error()
			}
			p = t.readBack(p, nodes[p.Field], got)
		case b.saved:
			p.Outcome = OutcomeContradicted
		case b.own[p.Field]:
			p.Outcome = refused(p)
		default:
			p.Outcome = OutcomeOtherFields
		}
		out = append(out, p)
	}
	return out, ""
}

// readBack: readBackProbe, unless the get does not show stored values.
func (t target) readBack(p Probe, n *model.Node, got map[string]any) Probe {
	if t.opaqueRead {
		p.Outcome = OutcomeAgreed
		return p
	}
	return readBackProbe(p, n, got)
}

// readBackProbe: an accepted probe read back. Fields whose class converts
// values, or that read back empty, are not compared; an empty_ignored
// field must keep its value (which the probe cannot know, so only "not
// emptied" is checked).
func readBackProbe(p Probe, n *model.Node, got map[string]any) Probe {
	p.Outcome = OutcomeAgreed
	if !p.Accept || n == nil || n.WriteOnly() || defaultConverted(n) {
		return p
	}
	s := fieldWire(n, lookup(got, p.Field))
	switch {
	case p.Claim == ClaimKeepsEmpty:
		if s == "" {
			p.Outcome, p.Read = OutcomeNotReadBack, &s
		}
	case !sameValue(p.Value, s):
		p.Outcome, p.Read = OutcomeNotReadBack, &s
	}
	return p
}

// fieldWire: a read value of field n in write form. A single select that
// marks the empty choice and one key holds that key: OPNsense 25.7 marks
// the empty choice for every value PHP's empty() calls empty, "0"
// included (BaseListField::getNodeData; 26.1 asks isSet()).
func fieldWire(n *model.Node, v any) string {
	s := wireString(v)
	if n != nil && n.Shape == model.ShapeSelect && !n.Multiple && n.List == nil && strings.HasPrefix(s, ",") && strings.Count(s, ",") == 1 {
		return s[1:]
	}
	return s
}

// wireString: a read value in write form (selected keys joined by ",").
func wireString(v any) string {
	s, _ := writeValue(v).(string)
	return s
}

// ClaimSummary counts the field probes of a report (ADR 0017): the probes
// of each claim by outcome, the constraints no probe tries (with the
// number of fields that have them), and the objects whose fields were not
// probed at all (field-probe results that were skipped).
type ClaimSummary struct {
	Probes    map[Claim]map[Outcome]int `json:"probes"`
	Unprobed  map[string]int            `json:"unprobed,omitempty"`
	NotProbed int                       `json:"not_probed"`
}

// Summarize counts the field-probe results.
func Summarize(results []*Result) ClaimSummary {
	s := ClaimSummary{Probes: map[Claim]map[Outcome]int{}, Unprobed: map[string]int{}}
	for _, r := range results {
		if r.Check != CheckFieldProbe {
			continue
		}
		if r.Status == StatusSkipped {
			s.NotProbed++
		}
		for _, p := range r.Probes {
			if s.Probes[p.Claim] == nil {
				s.Probes[p.Claim] = map[Outcome]int{}
			}
			s.Probes[p.Claim][p.Outcome]++
		}
		for k, n := range r.Unprobed {
			s.Unprobed[k] += n
		}
	}
	return s
}

// Lines renders the summary for a log, one claim per line.
func (s ClaimSummary) Lines() []string {
	var out []string
	for _, c := range slices.Sorted(maps.Keys(s.Probes)) {
		var parts []string
		for _, o := range slices.Sorted(maps.Keys(s.Probes[c])) {
			parts = append(parts, fmt.Sprintf("%d %s", s.Probes[c][o], o))
		}
		out = append(out, fmt.Sprintf("claim %-12s %s", c, strings.Join(parts, ", ")))
	}
	out = append(out, fmt.Sprintf("objects not probed: %d", s.NotProbed))
	if len(s.Unprobed) > 0 {
		var parts []string
		for _, k := range slices.Sorted(maps.Keys(s.Unprobed)) {
			parts = append(parts, fmt.Sprintf("%s %d", k, s.Unprobed[k]))
		}
		out = append(out, "constraints no probe tries (fields): "+strings.Join(parts, ", "))
	}
	return out
}
