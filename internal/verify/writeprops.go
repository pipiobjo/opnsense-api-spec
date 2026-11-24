// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Checks in the write cycle for what the capability data says about a
// field beyond its value: write_only (get shows it empty) and list
// (elements joined by a separator, in the order written when ordered).

package verify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// writeOnlyValues gives every write-only field of the entry a value in the
// add body, so the cycle can check that get does not return it.
func writeOnlyValues(entry *model.Node, body map[string]any, res *added) {
	if entry == nil {
		return
	}
	for _, ch := range entry.Children {
		if ch.Kind != model.KindField || !ch.WriteOnly() {
			continue
		}
		if cands := candidates(ch, nil); len(cands) > 0 {
			body[ch.Name] = cands[0]
			res.chosen[ch.Name] = true
			res.injected[ch.Name] = true
			res.tried[ch.Name] = 0
		}
	}
}

// checkWriteOnly reads the added entry: a write-only field sent with a
// value must come back empty from get (a finding otherwise: the data says
// write_only). The search answer is only recorded: from OPNsense 26.1.8 on
// it returns the stored value (UIModelGrid reads getValue), which the data
// documents.
func checkWriteOnly(c *Client, f *family, uuid string, a *added) (findings, shown []string) {
	var sent []string
	for _, name := range sortedKeys(a.chosen) {
		if n := node(f.entry, name); n.WriteOnly() && lookup(a.body, name) != "" {
			sent = append(sent, name)
		}
	}
	if len(sent) == 0 {
		return nil, nil
	}
	code, v, raw, err := c.do("GET", f.get.Path+"/"+uuid, nil)
	if err != nil || code != 200 {
		return []string{fmt.Sprintf("get/%s for write-only fields: HTTP %d %s", uuid, code, trunc(raw))}, nil
	}
	root, _ := v.(map[string]any)
	if f.get.Outer != "" {
		root, _ = root[f.get.Outer].(map[string]any)
	}
	got, _ := root[f.get.Wrapper].(map[string]any)
	for _, name := range sent {
		if s, _ := writeValue(lookup(got, name)).(string); s != "" {
			findings = append(findings, fmt.Sprintf("write-only field %s returned by get", name))
		}
	}
	if f.search == nil {
		return findings, nil
	}
	code, v, _, err = c.do("GET", f.search.Path+"?current=1&rowCount=-1", nil)
	if err != nil || code != 200 {
		return findings, nil // the search is checked on its own
	}
	rows, _ := asMap(v)["rows"].([]any)
	for _, row := range rows {
		m, _ := row.(map[string]any)
		if m["uuid"] != uuid {
			continue
		}
		for _, name := range sent {
			if s, _ := m[name].(string); s != "" {
				shown = append(shown, name)
			}
		}
	}
	return findings, shown
}

// listOrder writes two elements of an ordered list field in descending
// order with the set endpoint and checks that get returns them in that
// order (JSON member order of the select map; PHP keeps it). Up to three
// list fields and three pairs of candidates each are tried. Fields whose
// type converts values (gaps) or that are not stored (volatile) are not.
func listOrder(c *Client, f *family, uuid string) (bool, []string, []string) {
	if f.entry == nil {
		return false, nil, nil
	}
	tried := 0
	for _, ch := range f.entry.Children {
		if ch.Kind != model.KindField || ch.List == nil || !ch.List.Ordered || ch.WriteOnly() || ch.Volatile || len(ch.Gaps) > 0 || tried == 3 {
			continue
		}
		tried++
		for _, pair := range pairs(candidates(ch, nil), ch.List.Separator) {
			val := strings.Join(pair, ch.List.Separator)
			code, v, raw, err := c.do("POST", f.set.Path+"/"+uuid, map[string]any{f.set.Wrapper: map[string]any{ch.Name: val}})
			if err != nil || code != 200 {
				return false, []string{fmt.Sprintf("set %s for the list order: HTTP %d %s", ch.Name, code, trunc(raw))}, nil
			}
			if asMap(v)["result"] != "saved" {
				continue // value rejected; next pair
			}
			code, _, raw, err = c.do("GET", f.get.Path+"/"+uuid, nil)
			if err != nil || code != 200 {
				return false, []string{fmt.Sprintf("get/%s after the list write: HTTP %d %s", uuid, code, trunc(raw))}, nil
			}
			path := []string{f.get.Wrapper, ch.Name}
			if f.get.Outer != "" {
				path = append([]string{f.get.Outer}, path...)
			}
			got, err := selectedInOrder(raw, path)
			if err != nil {
				return false, []string{fmt.Sprintf("list order of %s: %v", ch.Name, err)}, nil
			}
			if !slices.Equal(got, pair) {
				return false, []string{fmt.Sprintf("list order of %s: sent %q, read %q (data says ordered, separator %q)", ch.Name, pair, got, ch.List.Separator)}, nil
			}
			return true, nil, nil
		}
	}
	if tried == 0 {
		return false, nil, nil
	}
	return false, nil, []string{"list order not tested (no two values accepted)"}
}

// pairs: up to three pairs of distinct candidates without the separator,
// each in descending order, so the order sent is never the sorted one.
func pairs(cands []string, sep string) [][]string {
	var ok []string
	for _, c := range cands {
		if c != "" && !strings.Contains(c, sep) && !slices.Contains(ok, c) {
			ok = append(ok, c)
		}
	}
	var out [][]string
	for i := 0; i+1 < len(ok) && len(out) < 3; i++ {
		a, b := ok[i], ok[i+1]
		if a < b {
			a, b = b, a
		}
		out = append(out, []string{a, b})
	}
	return out
}

// selectedInOrder returns the selected keys of the select map at path in
// the order of the raw JSON answer (a JSON list stands for keys 0..n-1).
func selectedInOrder(raw []byte, path []string) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	for _, key := range path {
		if err := enterObject(dec, key); err != nil {
			return nil, err
		}
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	var out []string
	switch tok {
	case json.Delim('{'):
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			var opt struct {
				Selected json.Number `json:"selected"`
			}
			if err := dec.Decode(&opt); err != nil {
				return nil, fmt.Errorf("option %v: %w", k, err)
			}
			if opt.Selected == "1" {
				out = append(out, k.(string))
			}
		}
	case json.Delim('['):
		for i := 0; dec.More(); i++ {
			var opt struct {
				Selected json.Number `json:"selected"`
			}
			if err := dec.Decode(&opt); err != nil {
				return nil, err
			}
			if opt.Selected == "1" {
				out = append(out, strconv.Itoa(i))
			}
		}
	default:
		return nil, fmt.Errorf("not a select map but %v", tok)
	}
	return out, nil
}

// enterObject moves the decoder to the value of key in the next object.
func enterObject(dec *json.Decoder, key string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok != json.Delim('{') {
		return fmt.Errorf("%s: not an object", key)
	}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return err
		}
		if k == key {
			return nil
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return err
		}
	}
	return fmt.Errorf("%s: missing", key)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// entrySearch picks the search endpoint of an entry list for the family
// of add: the only one, else the one named like add (bind's
// addPrimaryDomain and searchPrimaryDomain).
func entrySearch(searches []*ir.Endpoint, add *ir.Endpoint) *ir.Endpoint {
	if len(searches) == 1 {
		return searches[0]
	}
	if add == nil {
		return nil
	}
	want := strings.ToLower(strings.TrimPrefix(add.Action, "add"))
	for _, s := range searches {
		if strings.ToLower(strings.TrimPrefix(s.Action, "search")) == want {
			return s
		}
	}
	return nil
}
