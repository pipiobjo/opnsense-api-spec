// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
)

func ptr(v int64) *int64 { return &v }

func str(p *int64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatInt(*p, 10)
}

func TestTextAccepts(t *testing.T) {
	l, _ := GoPattern(`/[0-9]+[kKmMgG]?/`, false) // unanchored, as unbound's cache sizes
	for v, want := range map[string]bool{"4m": true, "100": true, "contracttest1": false, "1x": false, "": false} {
		if got := l.TextAccepts(v); got != want {
			t.Errorf("%q: %v, want %v (the first match must be the whole value)", v, got, want)
		}
	}
	f := &Limits{Forbidden: " \t"}
	if f.TextAccepts("a b") || !f.TextAccepts("ab") {
		t.Error("forbidden characters")
	}
}

func TestGoPattern(t *testing.T) {
	cases := []struct {
		in, want    string
		preg, bytes bool
		min, max    string
		ok          bool
	}{
		{`/^.{1,255}$/u`, `^.{1,255}$`, false, false, "nil", "nil", true},
		{`/^[a-z]+$/i`, `(?i)^[a-z]+$`, false, true, "nil", "nil", true},
		{`/^(?!any$)[a-z]+$/`, "", false, false, "nil", "nil", false}, // look-ahead
		{`/^[a-z]+$/x`, "", false, false, "nil", "nil", false},        // a modifier that changes the meaning
		{`/^ä$/`, "", false, false, "nil", "nil", false},              // a UTF-8 literal matched as bytes
		{`/^(.){1,255}$/`, `^(.){1,255}\n?$`, true, true, "nil", "nil", true},
		{`/^[a-z]+$/D`, `^[a-z]+$`, true, true, "nil", "nil", true},
		{`/[a-z]+/`, "", true, false, "nil", "nil", false}, // preg_match unanchored: any match counts
		// bounds over Go's 1000: a repeat of one character becomes a length
		{`/^.{1,4096}$/u`, `^.*$`, false, false, "1", "4096", true},
		{`/^([0-9a-z\.,_\-:]){0,1024}$/u`, `^([0-9a-z\.,_\-:])*$`, false, false, "nil", "1024", true},
		{`/^([^"]{0,1024})$/u`, `^[^"]*$`, false, false, "nil", "1024", true},
		{`/^(ab){1,2000}$/u`, "", false, false, "nil", "nil", false}, // not one character
	}
	for _, c := range cases {
		got, ok := GoPattern(c.in, c.preg)
		if got.Pattern != c.want || ok != c.ok || ok && (got.Bytes != c.bytes || str(got.MinLength) != c.min || str(got.MaxLength) != c.max) {
			t.Errorf("GoPattern(%q, %v) = %+v %v, want %q bytes=%v %s..%s %v", c.in, c.preg, got, ok, c.want, c.bytes, c.min, c.max, c.ok)
		}
	}
	// a byte pattern counts bytes, as PCRE without u
	l, _ := GoPattern(`/^.{1,3}$/`, false)
	if l.TextAccepts("ää") || !l.TextAccepts("äa") {
		t.Error("a pattern without u must count bytes")
	}
	// a length limit counts characters, or bytes without u
	l, _ = GoPattern(`/^.{1,1001}$/u`, false)
	if !l.TextAccepts(strings.Repeat("ä", 1001)) || l.TextAccepts(strings.Repeat("x", 1002)) {
		t.Error("length in characters")
	}
	// preg_match: "$" also before a final newline
	l, _ = GoPattern(`/^(.){1,3}$/`, true)
	if !l.TextAccepts("ab\n") {
		t.Error(`preg_match's "$" matches before a final newline`)
	}
}

// TestValidation: a described class adds its rules; a class without a
// description makes the gap validation-unknown; a class that does not call
// its parent's checks voids them; a setting without a setter has no effect;
// setting names are matched as PHP matches method names.
func TestValidation(t *testing.T) {
	const cls = baseNS + "demorangefield"
	idx, err := BuildTypeIndex("testdata/models")
	if err != nil {
		t.Fatal(err)
	}
	ck := &checker{apply: integerLimits}
	ck.forms = forms(idx.form(cls, ck), "test")
	checkers[cls] = ck
	defer delete(checkers, cls)
	ms, _, err := LoadAll("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var demo *Model
	for _, m := range ms {
		if m.Class == `OPNsense\Demo\Demo` {
			demo = m
		}
	}
	field := func(name string) *Node {
		n := demo.Find("general." + name)
		if n == nil {
			t.Fatalf("no field %s", name)
		}
		return n
	}
	ranged := field("ranged")
	if l := ranged.Limits(); l == nil || str(l.Minimum) != "5" || gap.Has(ranged.Gaps, gap.ValidationUnknown) {
		t.Errorf("described class: %+v %+v", l, ranged.Gaps)
	}
	if _, ok := ranged.Props["Mask"]; ok || !slices.Equal(ranged.Checked, []string{"MinimumValue"}) {
		t.Errorf("a Mask without setter must be dropped: %v, checked %v", ranged.Props, ranged.Checked)
	}
	if l := field("cased").Limits(); l == nil || str(l.Minimum) != "7" {
		t.Errorf("minimumvalue reaches setMinimumValue: %+v", l)
	}
	adding := field("adding")
	if l := adding.Limits(); l == nil || str(l.Minimum) != "5" || !gap.Has(adding.Gaps, gap.ValidationUnknown) {
		t.Errorf("own checks on top: %+v %+v", l, adding.Gaps)
	}
	replacing := field("replacing")
	if replacing.Limits() != nil || !gap.Has(replacing.Gaps, gap.ValidationUnknown) {
		t.Errorf("checks replaced: %+v %+v", replacing.Limits(), replacing.Gaps)
	}
	// a form nobody reviewed: no rules, the gap names it
	ck.forms = forms()
	ms, _, _ = LoadAll("testdata")
	for _, m := range ms {
		if n := m.Find("general.ranged"); n != nil {
			if n.Limits() != nil || !gap.Has(n.Gaps, gap.ValidationUnknown) {
				t.Errorf("unreviewed form: %+v %+v", n.Limits(), n.Gaps)
			}
		}
	}
}
