// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// What a field class does with a value, read from its PHP methods:
// whether get shows it (__toString), whether a write stores it (setValue)
// and whether its value is a list (getNodeData, $internalFieldSeparator).
// Each rule recognises one exact form of code; anything else is "not
// recognised" and the field keeps the behaviour of its base class or a gap.

package model

import (
	"regexp"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/php"
)

// setValue behaviour of a class that overrides it
const (
	setOther        = ""              // stores (possibly converted) values
	setDiscard      = "discard"       // stores nothing: `{ return; }` or empty
	setEmptyIgnored = "empty-ignored" // stores only non-empty values
)

var (
	methodRe = regexp.MustCompile(`function\s+(\w+)\s*\([^)]*\)[^{;]*\{`)
	// a property with a literal default: private $separatorchar = "\n";
	propRe     = regexp.MustCompile(`(?:private|protected|public)\s+(?:static\s+)?\$(\w+)\s*=\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')\s*;`)
	boolPropRe = regexp.MustCompile(`(?i:private|protected|public)\s+(?:static\s+)?\$(\w+)\s*=\s*(?i:(true|false))\s*;`)
	// __toString that always returns the empty string
	emptyStringRe = regexp.MustCompile(`^\{\s*return\s*(?:""|''|\(string\)\s*null)\s*;\s*\}$`)
	discardRe     = regexp.MustCompile(`^\{\s*(?:return\s*;)?\s*\}$`)
	writeRe       = regexp.MustCompile(`parent::setValue\s*\(|\$this->serialize\s*\(|\$this->internalValue\s*=[^=]`)
	// conditions under which a write is skipped for "" (and PHP's other
	// empty values), or that only hold for values loaded from config.xml
	guardRe      = regexp.MustCompile(`^(?:\$value\s*!==?\s*(?:""|'')|!\s*empty\s*\(\s*\$value\s*\)|is_a\s*\(\s*\$value\s*,\s*['"]SimpleXMLElement['"]\s*\))$`)
	ifRe         = regexp.MustCompile(`(?:^|[^\w$])(?:else\s*if|elseif|if)\s*$`)
	explodeVarRe = regexp.MustCompile(`\$(\w+)\s*=\s*(explode\s*\([^;]*\))\s*;\s*`)
	// getNodeData that splits the value into {element: {value, selected: 1}}
	splitListRe = regexp.MustCompile(`^\{\s*\$result\s*=\s*(?:\[\s*\]|array\s*\(\s*\))\s*;\s*` +
		`foreach\s*\(\s*explode\s*\(\s*("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|\$this->\w+)\s*,\s*(?:\$this->internalValue|\(string\)\s*\$this)\s*\)\s*as\s*\$(\w+)\s*\)\s*\{\s*` +
		`\$result\s*\[\s*\$(\w+)\s*\]\s*=\s*(?:\[|array\s*\()\s*["']value["']\s*=>\s*\$(\w+)\s*,\s*["']selected["']\s*=>\s*1\s*(?:\]|\))\s*;\s*\}\s*` +
		`return\s+\$result\s*;\s*\}$`)
)

// methods returns the bodies (braces included) of the methods a class
// declares itself, by lower-case name.
func methods(body string) map[string]string {
	out := map[string]string{}
	for _, loc := range methodRe.FindAllStringSubmatchIndex(body, -1) {
		end := php.Match(body, loc[1]-1, '{', '}')
		if end < 0 {
			continue
		}
		out[strings.ToLower(body[loc[2]:loc[3]])] = body[loc[1]-1 : end+1]
	}
	return out
}

// props returns the literal defaults of the properties a class declares.
func props(body string) map[string]string {
	out := map[string]string{}
	for _, m := range propRe.FindAllStringSubmatch(body, -1) {
		if v, ok := phpString(m[2]); ok {
			out[m[1]] = v
		}
	}
	// booleans as "true"/"false" (protected $internalIsRequired = true;)
	for _, m := range boolPropRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = strings.ToLower(m[2])
	}
	return out
}

// phpString decodes a PHP string literal; double quotes know the usual
// escapes, single quotes only \' and \\. Variables in double quotes are
// not resolved (false).
func phpString(lit string) (string, bool) {
	if len(lit) < 2 {
		return "", false
	}
	q, s := lit[0], lit[1:len(lit)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if q == '"' && c == '$' {
			return "", false
		}
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch n := s[i]; {
		case q == '\'' && (n == '\'' || n == '\\'):
			b.WriteByte(n)
		case q == '\'':
			b.WriteByte('\\')
			b.WriteByte(n)
		case n == 'n':
			b.WriteByte('\n')
		case n == 't':
			b.WriteByte('\t')
		case n == 'r':
			b.WriteByte('\r')
		case n == '\\' || n == '"' || n == '$':
			b.WriteByte(n)
		default:
			b.WriteByte('\\')
			b.WriteByte(n)
		}
	}
	return b.String(), true
}

// setValueKind classifies a setValue body.
func setValueKind(body string) string {
	if discardRe.MatchString(body) {
		return setDiscard
	}
	writes := writeRe.FindAllStringIndex(body, -1)
	if len(writes) == 0 {
		return setOther // stores through another method: not recognised
	}
	for _, w := range writes {
		if !guarded(body, w[0]) {
			return setOther
		}
	}
	return setEmptyIgnored
}

// guarded: the position lies in a block (at any depth) opened by an if or
// elseif whose whole condition is one of guardRe.
func guarded(body string, pos int) bool {
	depth := 0
	for i := pos - 1; i > 0; i-- { // body[0] is the function's own brace
		switch body[i] {
		case '}':
			depth++
		case '{':
			if depth > 0 {
				depth--
				continue
			}
			if c, ok := condition(body[:i]); ok && isGuard(c) {
				return true
			}
		}
	}
	return false
}

// condition returns the condition of the if/elseif whose block opens
// right after src (src ends just before the "{").
func condition(src string) (string, bool) {
	src = strings.TrimRight(src, " \t\r\n")
	if !strings.HasSuffix(src, ")") {
		return "", false
	}
	depth := 0
	for i := len(src) - 1; i >= 0; i-- {
		switch src[i] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				if !ifRe.MatchString(src[:i]) {
					return "", false
				}
				return strings.TrimSpace(src[i+1 : len(src)-1]), true
			}
		}
	}
	return "", false
}

// isGuard: the condition holds only if one of its &&-joined parts is a
// guard (a condition with || is never one).
func isGuard(c string) bool {
	if strings.Contains(c, "||") {
		return false
	}
	for _, part := range strings.Split(c, "&&") {
		if guardRe.MatchString(strings.TrimSpace(part)) {
			return true
		}
	}
	return false
}

// splitListSep: the separator expression of a getNodeData that splits the
// value into a list ("" when the body has another form): a decoded string
// literal, or "$this-><property>" to be resolved along the class chain.
func splitListSep(body string) (string, bool) {
	// "$list = explode(...); foreach ($list as $x)" is the same as
	// "foreach (explode(...) as $x)"
	if m := explodeVarRe.FindStringSubmatchIndex(body); m != nil {
		name, expr := body[m[2]:m[3]], body[m[4]:m[5]]
		rest := body[:m[0]] + body[m[1]:]
		if strings.Count(rest, "$"+name) == 1 {
			body = strings.Replace(rest, "$"+name, expr, 1)
		}
	}
	m := splitListRe.FindStringSubmatch(body)
	if m == nil || m[2] != m[3] || m[2] != m[4] {
		return "", false
	}
	if strings.HasPrefix(m[1], "$this->") {
		return m[1], true
	}
	return phpString(m[1])
}
