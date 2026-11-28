// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// Limits are the rules a field's classes enforce on a non-empty value, as
// far as they are derived (validation.go): an integer range, a pattern
// whose first match must be the whole value (in Go regexp syntax),
// characters the value may not contain, and a format. The contract test
// probes exactly these (ADR 0017), and consumers check them without
// knowing OPNsense's field classes.
type Limits struct {
	Minimum *int64 `json:"minimum,omitempty"`
	Maximum *int64 `json:"maximum,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	// Bytes: the pattern applies to the value's bytes, each one character
	// (a PHP pattern without the u modifier)
	Bytes bool `json:"bytes,omitempty"`
	// MinLength, MaxLength: the value's length in characters (bytes when
	// Bytes); a pattern "^X{m,n}$" whose bound Go's regexp does not take
	// (over 1000) is published as "^X*$" with these
	MinLength *int64 `json:"min_length,omitempty"`
	MaxLength *int64 `json:"max_length,omitempty"`
	// PerElement: the pattern applies to each element of the list
	PerElement bool    `json:"per_element,omitempty"`
	Forbidden  string  `json:"forbidden,omitempty"` // characters the value may not contain
	Format     *Format `json:"format,omitempty"`
}

func (l *Limits) empty() bool {
	return l.Minimum == nil && l.Maximum == nil && l.Pattern == "" && l.MinLength == nil && l.MaxLength == nil &&
		l.Forbidden == "" && l.Format == nil
}

// UIText: properties that are texts for the web UI, not part of the
// contract. They change often (punctuation, wording) and would split
// fields into variants that differ in nothing a client can rely on; they
// stay in the OpenAPI documents, and are no claim a probe tries.
var UIText = map[string]bool{"ValidationMessage": true, "BlankDesc": true}

// Limits of the field; nil when none is derived.
func (n *Node) Limits() *Limits { return n.Rules }

// pcreFlags: modifiers GoPattern can express (x, U, A and others change
// the meaning in ways it does not translate)
const pcreFlags = "imsuD"

// GoPattern converts a PHP (PCRE) pattern such as "/^.{1,255}$/u" to Go
// regexp syntax, with the flags i, s and m inline; the result has Pattern,
// Bytes and, for a long bounded repeat, MinLength and MaxLength set. preg:
// the pattern is used by preg_match, where any match counts; it must then
// be anchored at both ends, and PCRE's "$" (also before a final newline,
// unless D) is translated. Bytes: no u modifier, the pattern applies to
// bytes. ok is false when Go cannot express the pattern.
func GoPattern(mask string, preg bool) (Limits, bool) {
	var l Limits
	if len(mask) < 2 {
		return l, false
	}
	d := mask[0]
	end := strings.LastIndexByte(mask, d)
	if end <= 0 {
		return l, false
	}
	expr, flags := mask[1:end], mask[end+1:]
	prefix := ""
	for _, f := range flags {
		if !strings.ContainsRune(pcreFlags, f) {
			return l, false
		}
		switch f {
		case 'i', 's', 'm':
			prefix += string(f)
		}
	}
	l.Bytes = !strings.ContainsRune(flags, 'u')
	if l.Bytes && !isASCII(expr) {
		return l, false // literal UTF-8 sequences matched byte by byte
	}
	if preg {
		if strings.ContainsRune(flags, 'm') || !strings.HasPrefix(expr, "^") || !strings.HasSuffix(expr, "$") || strings.HasSuffix(expr, `\$`) {
			return l, false
		}
		if !strings.ContainsRune(flags, 'D') {
			expr = strings.TrimSuffix(expr, "$") + `\n?$`
		}
	} else if m := longRepeatRe.FindStringSubmatch(expr); m != nil && !strings.ContainsRune(flags, 'm') {
		// ^X{m,n}$ with one character X: every character is an X, and
		// the length is m to n
		atom := m[1] + m[4]
		min, _ := strconv.ParseInt(m[2]+m[5], 10, 64)
		max, err := strconv.ParseInt(m[3]+m[6], 10, 64)
		if err == nil && max > 1000 {
			expr = "^" + atom + "*$"
			if min > 0 {
				l.MinLength = &min
			}
			l.MaxLength = &max
		}
	}
	if prefix != "" {
		expr = "(?" + prefix + ")" + expr
	}
	if _, err := regexp.Compile(expr); err != nil {
		return Limits{}, false
	}
	l.Pattern = expr
	return l, true
}

// longRepeatRe: "^X{m,n}$" or "^(X{m,n})$" where X matches one character
// (".", a character class, or one of them in a group)
var longRepeatRe = regexp.MustCompile(`^\^(` + oneChar + `)\{(\d+),(\d+)\}\$$|^\^\((` + oneChar + `)\{(\d+),(\d+)\}\)\$$`)

const oneChar = `\.|\[(?:\\.|[^\]\\])+\]|\((?:\.|\[(?:\\.|[^\]\\])+\])\)`

var compiled sync.Map // pattern -> *regexp.Regexp

// TextAccepts: the value passes the pattern (OPNsense's Regex validator:
// the first match is the whole value, so an unanchored mask still
// constrains all of it), the length limits and the forbidden characters.
func (l *Limits) TextAccepts(v string) bool {
	if l == nil {
		return true
	}
	if strings.ContainsAny(v, l.Forbidden) {
		return false
	}
	n := int64(len(v))
	if !l.Bytes {
		n = int64(utf8.RuneCountInString(v))
	}
	if l.MinLength != nil && n < *l.MinLength || l.MaxLength != nil && n > *l.MaxLength {
		return false
	}
	if l.Pattern == "" {
		return true
	}
	re, ok := compiled.Load(l.Pattern)
	if !ok {
		re, _ = compiled.LoadOrStore(l.Pattern, regexp.MustCompile(l.Pattern))
	}
	if l.Bytes {
		v = Latin1(v)
	}
	loc := re.(*regexp.Regexp).FindStringIndex(v)
	return loc != nil && loc[0] == 0 && loc[1] == len(v)
}

// Latin1 maps each byte to the character of the same number, so that a
// Go pattern sees bytes as PCRE does without the u modifier.
func Latin1(v string) string {
	if isASCII(v) {
		return v
	}
	r := make([]rune, len(v))
	for i := 0; i < len(v); i++ {
		r[i] = rune(v[i])
	}
	return string(r)
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}
