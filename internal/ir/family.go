// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package ir

import (
	"sort"
	"strings"
)

// Family is one resource cut out of the endpoints of one model path: the
// endpoint chosen per operation, and the endpoints that belong to some
// family of the group (so they are not reported again as actions).
type Family struct {
	Name   string // "" when the group is one resource
	Chosen map[string]*Endpoint
	Member map[*Endpoint]bool
}

// Families splits the endpoints of one model path (opOf names the
// operation an endpoint serves: add, get, ...). Usually there is one
// endpoint per operation. When an operation has several:
//
//   - one of them carries the name the single endpoints share (getHost
//     beside addHost/delHost, getClient beside getClientBuilder): that one
//     belongs to the resource, the others are actions;
//   - every such operation has the same set of names, at least two (bind:
//     addPrimaryDomain/addSecondaryDomain/addForwardDomain and the same three
//     search endpoints): one resource per name, "<id>@<family>", each with
//     its own endpoints and the shared ones (getDomain, setDomain,
//     delDomain); the family is the name without the shared part, in lower
//     case ("primary");
//   - otherwise better picks one, the others are actions.
func Families(eps []*Endpoint, opOf func(*Endpoint) string, better func(a, b *Endpoint) bool) []Family {
	byOp := map[string][]*Endpoint{}
	for _, e := range eps {
		byOp[opOf(e)] = append(byOp[opOf(e)], e)
	}
	shared := map[string]int{}
	var multi []string
	for op, es := range byOp {
		if len(es) == 1 {
			shared[stem(op, es[0])]++
		} else {
			multi = append(multi, op)
		}
	}
	sort.Strings(multi)
	base := ""
	for st, n := range shared {
		if n > shared[base] || (n == shared[base] && st < base) {
			base = st
		}
	}
	single := Family{Chosen: map[string]*Endpoint{}, Member: map[*Endpoint]bool{}}
	for op, es := range byOp {
		if len(es) == 1 {
			single.Chosen[op] = es[0]
		}
	}
	if len(multi) == 0 {
		return []Family{single}
	}
	// the shared name decides
	byName := true
	for _, op := range multi {
		var hit *Endpoint
		for _, e := range byOp[op] {
			if base != "" && stem(op, e) == base {
				hit = e
			}
		}
		if hit == nil {
			byName = false
			break
		}
		single.Chosen[op] = hit
	}
	if byName {
		return []Family{single}
	}
	// the same names under every operation with several endpoints
	names := stems(multi[0], byOp[multi[0]])
	sameNames := len(names) >= 2
	for _, op := range multi[1:] {
		if !equalStrings(stems(op, byOp[op]), names) {
			sameNames = false
		}
	}
	if sameNames {
		var out []Family
		member := map[*Endpoint]bool{}
		for _, op := range multi {
			for _, e := range byOp[op] {
				member[e] = true
			}
		}
		for _, n := range names {
			f := Family{Name: familyName(n, base), Chosen: map[string]*Endpoint{}, Member: member}
			for op, es := range byOp {
				if len(es) == 1 {
					f.Chosen[op] = es[0]
					continue
				}
				for _, e := range es {
					if stem(op, e) == n {
						f.Chosen[op] = e
					}
				}
			}
			out = append(out, f)
		}
		return out
	}
	for _, op := range multi {
		var pick *Endpoint
		for _, e := range byOp[op] {
			if pick == nil || better(e, pick) {
				pick = e
			}
		}
		single.Chosen[op] = pick
	}
	return []Family{single}
}

// stem is the action name without the operation verb: addPrimaryDomain
// -> PrimaryDomain, getwake -> wake, searchItem -> Item.
func stem(op string, e *Endpoint) string {
	a := e.Action
	if len(a) >= len(op) && strings.EqualFold(a[:len(op)], op) {
		return a[len(op):]
	}
	return a
}

func stems(op string, es []*Endpoint) []string {
	var out []string
	for _, e := range es {
		out = append(out, stem(op, e))
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// familyName: PrimaryDomain with the shared name Domain -> primary.
func familyName(name, base string) string {
	if base != "" && len(name) > len(base) && strings.HasSuffix(name, base) {
		name = name[:len(name)-len(base)]
	}
	return strings.ToLower(name)
}
