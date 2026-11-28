// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"sort"
	"strconv"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// Test values for write cycles. The instance tells us which fields it
// rejects (validations); for each of them we try candidates derived from
// the field type and its constraints, in order, until the instance accepts
// the entry. Option lists take their values from what the instance itself
// offers (the empty-entry template lists static and dynamic options), so
// relations to existing objects (interfaces, aliases, certificates) work
// when such objects exist and are reported when they do not.
//
// All addresses are from the documentation ranges (RFC 5737, RFC 3849,
// RFC 2606); nothing here may point at a real network.

// candidates returns test values for a field, best first. tmpl is the
// field's value in the instance's empty-entry template (read shape), used
// for option lists. An empty result means no value can be generated.
func candidates(n *model.Node, tmpl any) []string {
	if n == nil {
		return nil
	}
	if opts := optionKeys(tmpl); len(opts) > 0 {
		return opts
	}
	if n.BaseType == "BaseListField" {
		// a list field without offered options: static options from the
		// model, else nothing (needs a prerequisite object)
		var out []string
		for _, o := range n.Options {
			if o.Value != "" {
				out = append(out, o.Value)
			}
		}
		if len(out) > 0 || !strings.HasSuffix(n.Type, "PortField") {
			return out
		}
	}
	var c []string
	t := strings.TrimPrefix(n.Type, ".\\")
	p := n.Props
	switch {
	case n.BaseType == "BooleanField" || t == "BooleanField":
		c = []string{"1", "0"}
	case n.BaseType == "IntegerField" || n.BaseType == "NumericField" || t == "AutoNumberField":
		c = integers(p["MinimumValue"], p["MaximumValue"])
	case strings.HasSuffix(t, "PortField"):
		c = []string{"8443", "443", "1024"}
	case t == "EmailField":
		c = []string{"contract-test@example.com"}
	case t == "UrlField":
		c = []string{"https://example.com/", "http://example.com/"}
	case t == "MacAddressField":
		c = []string{"02:00:00:00:00:01"}
	case t == "Base64Field":
		c = []string{"Y29udHJhY3QtdGVzdA=="} // "contract-test"
	case strings.HasSuffix(t, "NetworkField") || strings.HasSuffix(t, "AddressField") || t == "IPPortField":
		c = networks(p)
	case strings.HasSuffix(t, "HostnameField"):
		c = []string{"ct1.example.com", "ct1", "192.0.2.10"}
		if strings.EqualFold(p["IpAllowed"], "N") {
			c = c[:2]
		}
	default:
		c = texts()
	}
	return filterRules(c, n.Limits())
}

func texts() []string {
	return []string{
		"contracttest1", "ct1", "contract-test-1", "contract_test_1", "Contract test 1",
		"1", "ct", "=", "-", "192.0.2.10", "192.0.2.0/24", "ct1.example.com",
		"02:00:00:00:00:01", "https://example.com/", "contract-test@example.com",
	}
}

func networks(p map[string]string) []string {
	v4 := []string{"192.0.2.10", "192.0.2.0/24"}
	v6 := []string{"2001:db8::10", "2001:db8::/64"}
	if strings.EqualFold(p["NetMaskRequired"], "Y") {
		v4, v6 = []string{"192.0.2.0/24"}, []string{"2001:db8::/64"}
	} else if strings.EqualFold(p["NetMaskAllowed"], "N") {
		v4, v6 = v4[:1], v6[:1]
	}
	switch strings.ToLower(p["AddressFamily"]) {
	case "ipv6":
		return v6
	case "ipv4":
		return v4
	}
	return append(v4, v6...)
}

func integers(minS, maxS string) []string {
	lo, okLo := atoi(minS)
	hi, okHi := atoi(maxS)
	var out []string
	add := func(v int64) {
		if (okLo && v < lo) || (okHi && v > hi) {
			return
		}
		s := strconv.FormatInt(v, 10)
		for _, o := range out {
			if o == s {
				return
			}
		}
		out = append(out, s)
	}
	if okLo {
		add(lo)
	}
	for _, v := range []int64{1, 10, 100, 1000, 0} {
		add(v)
	}
	if okHi {
		add(hi)
	}
	return out
}

func atoi(s string) (int64, bool) {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return v, err == nil
}

// optionKeys: the selectable keys of a select map or list as read, without
// the empty "none" entry.
func optionKeys(v any) []string {
	var keys []string
	switch t := v.(type) {
	case map[string]any:
		if !isSelect(t) {
			return nil
		}
		for k := range t {
			if k != "" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
	case []any:
		if len(t) == 0 || !isSelect(t) {
			return nil
		}
		for i := range t {
			keys = append(keys, strconv.Itoa(i))
		}
	}
	return keys
}

// filterRules keeps the candidates the field's pattern, length limits and
// forbidden characters let pass (formats: the candidates are chosen per
// class).
func filterRules(c []string, l *model.Limits) []string {
	if l == nil {
		return c
	}
	var out []string
	for _, v := range c {
		if l.TextAccepts(v) {
			out = append(out, v)
		}
	}
	return out
}

// lookup returns the value at a dotted path in a decoded object.
func lookup(v any, path string) any {
	for _, step := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[step]
	}
	return v
}

// assign sets a value at a dotted path, creating objects on the way.
func assign(m map[string]any, path string, val any) {
	steps := strings.Split(path, ".")
	for _, s := range steps[:len(steps)-1] {
		next, ok := m[s].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[s] = next
		}
		m = next
	}
	m[steps[len(steps)-1]] = val
}

// node finds the model node at a dotted path below n.
func node(n *model.Node, path string) *model.Node {
	if n == nil {
		return nil
	}
	for _, step := range strings.Split(path, ".") {
		var next *model.Node
		for _, c := range n.Children {
			if c.Name == step {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}
