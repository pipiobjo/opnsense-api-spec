// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package php is the one place where PHP source is scanned: comments,
// brackets, argument lists, namespaces, use imports, class declarations and
// their bodies. It is not a parser; it relies on the code style of OPNsense
// (one statement per line for namespace, use and class) and is tested on
// real OPNsense files through the packages that use it. Controllers and
// field types are both read through it, so a rule (for example: comments
// never count) holds everywhere.
package php

import (
	"regexp"
	"strings"
)

// StripComments removes PHP comments while keeping string literals intact.
func StripComments(src string) string {
	var b strings.Builder
	n := len(src)
	for i := 0; i < n; i++ {
		c := src[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < n && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= n {
				j = n - 1
			}
			b.WriteString(src[i : j+1])
			i = j
		case c == '/' && i+1 < n && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			// keep newlines for line counting
			b.WriteString(strings.Repeat("\n", strings.Count(src[i:i+2+end+2], "\n")))
			i = i + 2 + end + 1
		case (c == '/' && i+1 < n && src[i+1] == '/') || c == '#':
			for i < n && src[i] != '\n' {
				i++
			}
			if i < n {
				b.WriteByte('\n')
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Match returns the index of the bracket c closing the bracket o at
// index open, skipping string literals; -1 if it is not closed.
func Match(src string, open int, o, c byte) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '\'', '"':
			q := src[i]
			for i++; i < len(src) && src[i] != q; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case o:
			depth++
		case c:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// SplitArgs splits a parameter/argument list at top-level commas.
func SplitArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '\'', '"':
			q := s[i]
			for i++; i < len(s) && s[i] != q; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if t := strings.TrimSpace(s[start:]); t != "" {
		out = append(out, t)
	}
	return out
}

var (
	nsRe    = regexp.MustCompile(`(?m)^\s*namespace\s+([\\\w]+)\s*;`)
	useRe   = regexp.MustCompile(`(?m)^\s*use\s+([\\\w]+)(?:\s+as\s+(\w+))?\s*;`)
	classRe = regexp.MustCompile(`(?m)^\s*(abstract\s+|final\s+)?class\s+(\w+)(?:\s+extends\s+([\\\w]+))?`)
)

// Header is the namespace and the use imports of a file.
type Header struct {
	Namespace string
	Uses      map[string]string // alias -> fully qualified name
}

// ParseHeader reads namespace and use statements of comment-free code.
func ParseHeader(code string) Header {
	h := Header{Uses: map[string]string{}}
	if m := nsRe.FindStringSubmatch(code); m != nil {
		h.Namespace = m[1]
	}
	for _, m := range useRe.FindAllStringSubmatch(code, -1) {
		alias := m[2]
		if alias == "" {
			alias = m[1][strings.LastIndex(m[1], `\`)+1:]
		}
		h.Uses[alias] = m[1]
	}
	return h
}

// Qualify resolves a class name as PHP does: "\A\B" is fully qualified;
// otherwise the first segment is looked up among the use imports ("A\B"
// with "use X\A" is "X\A\B"); else the namespace is prepended. The result
// has no leading backslash.
func (h Header) Qualify(name string) string {
	switch {
	case name == "":
		return ""
	case strings.HasPrefix(name, `\`):
		return name[1:]
	}
	first, rest, qualified := strings.Cut(name, `\`)
	if imp, ok := h.Uses[first]; ok {
		if qualified {
			return imp + `\` + rest
		}
		return imp
	}
	if h.Namespace == "" {
		return name
	}
	return h.Namespace + `\` + name
}

// Class is one class declaration with its body.
type Class struct {
	Name     string // fully qualified
	Extends  string // fully qualified, "" for a root class
	Abstract bool
	Body     string // from the opening to the closing brace, inclusive
}

// Classes returns the classes of comment-free code, each with its own body
// (a file can hold several, e.g. a container class and its array field).
func Classes(code string, h Header) []Class {
	var out []Class
	for _, loc := range classRe.FindAllStringSubmatchIndex(code, -1) {
		c := Class{Name: h.Qualify(code[loc[4]:loc[5]]),
			Abstract: loc[2] >= 0 && strings.HasPrefix(code[loc[2]:loc[3]], "abstract")}
		if loc[6] >= 0 {
			c.Extends = h.Qualify(code[loc[6]:loc[7]])
		}
		open := strings.IndexByte(code[loc[1]:], '{')
		if open < 0 {
			continue
		}
		open += loc[1]
		end := Match(code, open, '{', '}')
		if end < 0 {
			end = len(code) - 1
		}
		c.Body = code[open : end+1]
		out = append(out, c)
	}
	return out
}
