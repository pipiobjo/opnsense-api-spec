// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// The JSON keys of a settings get or set action a controller writes itself
// instead of inheriting the generic one. The generic actions use
// $internalModelName as the key; an own action may use any key (os-freeradius
// answers {"client": ...} while its model name is "Client"), so the key is
// read from the action's code, or left empty.

package controller

import (
	"regexp"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/php"
)

// ModelNameRef is the key static::$internalModelName (or self::), resolved
// against the controller's model name by the caller.
const ModelNameRef = "$internalModelName"

var (
	modelNameKeyRe = regexp.MustCompile(`^\s*(?:static|self)::\$internalModelName\s*$`)
	returnStmtRe   = regexp.MustCompile(`\breturn\b`)
)

// arrayKey reads an array key: a string literal or static::$internalModelName.
func arrayKey(k string) (string, bool) {
	if v, ok := lit(k); ok {
		return v, true
	}
	if modelNameKeyRe.MatchString(k) {
		return ModelNameRef, true
	}
	return "", false
}

// literalKeys adds the top-level keys of an array literal's inside to keys;
// false when an element has no literal key.
func literalKeys(inner string, keys map[string]bool) bool {
	for _, pair := range php.SplitArgs(inner) {
		k, _, ok := strings.Cut(pair, "=>")
		if !ok {
			return false
		}
		key, ok := arrayKey(k)
		if !ok {
			return false
		}
		keys[key] = true
	}
	return true
}

// responseKey returns the one top-level key of the array a get action
// returns, or "" when the code does not show exactly one. Every return
// statement must return an array literal or a variable; the variable must
// only be assigned array literals or have keys assigned
// ($result['client'] = ...), as in
//
//	$result = array();
//	if ($this->request->isGet()) {
//	    $result['client'] = $mdlClient->getNodes();
//	}
//	return $result;
//
// Anything else (a string, a function result, a variable passed on or
// assigned from an expression) leaves the key unknown.
func responseKey(body string) string {
	keys := map[string]bool{}
	vars := map[string]bool{}
	for _, m := range returnStmtRe.FindAllStringIndex(body, -1) {
		end := php.StatementEnd(body, m[1])
		if end < 0 {
			return ""
		}
		expr := strings.TrimSpace(body[m[1]:end])
		if al := arrayLitRe.FindStringSubmatch(expr); al != nil {
			if !literalKeys(al[1]+al[2], keys) {
				return ""
			}
		} else if v := varRe.FindStringSubmatch(expr); v != nil {
			vars[v[1]] = true
		} else {
			return ""
		}
	}
	for v := range vars {
		if !varKeys(body, v, keys) {
			return ""
		}
	}
	if len(keys) != 1 {
		return ""
	}
	for k := range keys {
		return k
	}
	return ""
}

// varKeys adds the keys variable name receives in body; false when a use
// is neither "return $v;", "$v = <array literal>;" nor "$v[<key>]... = ...;".
func varKeys(body, name string, keys map[string]bool) bool {
	useRe := regexp.MustCompile(`\$` + regexp.QuoteMeta(name) + `\b`)
	for _, m := range useRe.FindAllStringIndex(body, -1) {
		before, rest := body[:m[0]], body[m[1]:]
		if regexp.MustCompile(`\breturn\s*$`).MatchString(before) && regexp.MustCompile(`^\s*;`).MatchString(rest) {
			continue
		}
		i := len(rest) - len(strings.TrimLeft(rest, " \t\r\n"))
		if i < len(rest) && rest[i] == '[' {
			cl := php.Match(rest, i, '[', ']')
			if cl < 0 {
				return false
			}
			key, ok := arrayKey(rest[i+1 : cl])
			if !ok {
				return false
			}
			// further subscripts, then a plain assignment
			after := regexp.MustCompile(`^(?:\s*\[[^\]]*\])*\s*=[^=>]`).FindString(rest[cl+1:])
			if after == "" {
				return false
			}
			keys[key] = true
			continue
		}
		if !regexp.MustCompile(`^\s*=[^=>]`).MatchString(rest) {
			return false
		}
		end := php.StatementEnd(rest, 0)
		if end < 0 {
			return false
		}
		rhs := strings.TrimSpace(rest[strings.IndexByte(rest, '=')+1 : end])
		al := arrayLitRe.FindStringSubmatch(rhs)
		if al == nil || !literalKeys(al[1]+al[2], keys) {
			return false
		}
	}
	return true
}

var (
	// $x->...->setNodes(<argument>)
	setNodesRe = regexp.MustCompile(`(\$\w+(?:\s*->\s*\w+(?:\s*\(\s*\))?)*)\s*->\s*setNodes\s*\(`)
	// $this->request->getPost(<key>)
	getPostRe = regexp.MustCompile(`^\$this\s*->\s*request\s*->\s*getPost\s*\(\s*(['"]\w+['"]|(?:static|self)::\$internalModelName)\s*\)`)
	// $mdl = $this->getModel(); or $mdl = new Client();
	modelVarRe = regexp.MustCompile(`^\s*=\s*(?:\$this\s*->\s*getModel\s*\(\s*\)|new\s+\\?([\w\\]+)\s*\(\s*\))\s*;`)
)

// requestKey returns the request key a set action hands to the model, and
// the class it instantiates as the model ("" for $this->getModel()):
// every setNodes call in the action must be on the model (or a node below
// it) and take $this->request->getPost(<key>) directly or through a
// variable assigned from it, all with the same key, as in
//
//	$mdlClient = new Client();
//	$mdlClient->setNodes($this->request->getPost("client"));
//
// "" when the code does not show this; the caller checks the class.
func requestKey(body string) (key, newClass string) {
	ms := setNodesRe.FindAllStringSubmatchIndex(body, -1)
	if len(ms) == 0 {
		return "", ""
	}
	for _, m := range ms {
		open := m[1] - 1
		cl := php.Match(body, open, '(', ')')
		if cl < 0 {
			return "", ""
		}
		k, ok := postKey(body, strings.TrimSpace(body[open+1:cl]))
		if !ok || (key != "" && k != key) {
			return "", ""
		}
		key = k
		recv := strings.Fields(strings.ReplaceAll(body[m[2]:m[3]], "->", " "))
		c, ok := modelVar(body, recv)
		if !ok || (newClass != "" && c != newClass) {
			return "", ""
		}
		newClass = c
	}
	return key, newClass
}

// postKey reads the key of a setNodes argument: getPost(<key>), possibly
// subscripted (getPost('filter')['general']), or a variable assigned once
// from such a call.
func postKey(body, arg string) (string, bool) {
	if v := varRe.FindStringSubmatch(arg); v != nil {
		as := regexp.MustCompile(`\$`+regexp.QuoteMeta(v[1])+`\s*=([^=>][^;]*);`).FindAllStringSubmatch(body, -1)
		if len(as) != 1 {
			return "", false
		}
		arg = strings.TrimSpace(as[0][1])
	}
	m := getPostRe.FindStringSubmatch(arg)
	if m == nil {
		return "", false
	}
	return arrayKey(m[1])
}

// modelVar: the receiver of setNodes ("$this getModel()" or "$mdl general")
// starts with the model: $this->getModel() or a variable assigned once
// from it or from "new <class>()"; returns that class.
func modelVar(body string, recv []string) (string, bool) {
	if len(recv) == 0 {
		return "", false
	}
	if recv[0] == "$this" {
		return "", len(recv) > 1 && regexp.MustCompile(`^getModel\s*\(\s*\)$`).MatchString(recv[1])
	}
	re := regexp.MustCompile(regexp.QuoteMeta(recv[0]) + `\b\s*=[^=>][^;]*;`)
	as := re.FindAllStringIndex(body, -1)
	if len(as) != 1 {
		return "", false
	}
	m := modelVarRe.FindStringSubmatch(body[as[0][0]+len(recv[0]) : as[0][1]])
	if m == nil {
		return "", false
	}
	return m[1], true
}
