// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"regexp"
	"strconv"
	"strings"
)

// Limits are the limits a field's class enforces on a non-empty value,
// as far as they are derived: an integer range (IntegerField's
// MinMaxValidator), and a pattern whose first match must be the whole
// value (TextField's Mask through OPNsense\Base\Validators\Regex), in Go
// regexp syntax. The contract test probes exactly these (ADR 0017), and
// consumers check them without knowing OPNsense's field classes.
type Limits struct {
	Minimum *int64 `json:"minimum,omitempty"`
	Maximum *int64 `json:"maximum,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}

// UIText: properties that are texts for the web UI, not part of the
// contract. They change often (punctuation, wording) and would split
// fields into variants that differ in nothing a client can rely on; they
// stay in the OpenAPI documents, and are no claim a probe tries.
var UIText = map[string]bool{"ValidationMessage": true, "BlankDesc": true}

// textFields: core classes whose Mask is TextField's regex validator (a
// field's base is the first core class of its chain).
var textFields = map[string]bool{"TextField": true, "StrictTextField": true, "DescriptionField": true}

// Limits of the field; nil when none is derived. A Mask that Go cannot
// run (look-arounds) gives no pattern: the firewall's check stays the
// only one.
func (n *Node) Limits() *Limits {
	var l Limits
	if n.BaseType == "IntegerField" {
		// IntegerField::setMinimumValue takes only is_numeric values
		if v, err := strconv.ParseInt(strings.TrimSpace(n.Props["MinimumValue"]), 10, 64); err == nil {
			l.Minimum = &v
		}
		if v, err := strconv.ParseInt(strings.TrimSpace(n.Props["MaximumValue"]), 10, 64); err == nil {
			l.Maximum = &v
		}
	}
	if mask, ok := n.Props["Mask"]; ok && textFields[n.BaseType] {
		l.Pattern, _ = GoPattern(mask)
	}
	if l == (Limits{}) {
		return nil
	}
	return &l
}

// GoPattern converts a PHP (PCRE) pattern such as "/^.{1,255}$/u" to Go
// regexp syntax, with the flags i, s and m inline. false: Go cannot
// compile it.
func GoPattern(mask string) (string, bool) {
	if len(mask) < 2 {
		return "", false
	}
	d := mask[0]
	end := strings.LastIndexByte(mask, d)
	if end <= 0 {
		return "", false
	}
	expr, flags := mask[1:end], mask[end+1:]
	prefix := ""
	for _, f := range flags {
		switch f {
		case 'i', 's', 'm':
			prefix += string(f)
		}
	}
	if prefix != "" {
		expr = "(?" + prefix + ")" + expr
	}
	if _, err := regexp.Compile(expr); err != nil {
		return "", false
	}
	return expr, true
}

// MaskAccepts is OPNsense's Regex validator: the value passes when the
// first match is the whole value, so an unanchored mask still constrains
// all of it.
func MaskAccepts(re *regexp.Regexp, v string) bool {
	loc := re.FindStringIndex(v)
	return loc != nil && loc[0] == 0 && loc[1] == len(v)
}
