// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"regexp"
	"strconv"
	"testing"
)

func TestLimits(t *testing.T) {
	cases := []struct {
		n    Node
		want *Limits
	}{
		{Node{BaseType: "IntegerField", Props: map[string]string{"MinimumValue": "1", "MaximumValue": " 65535 "}}, &Limits{Minimum: ptr(1), Maximum: ptr(65535)}},
		{Node{BaseType: "IntegerField", Props: map[string]string{"MinimumValue": "x"}}, nil}, // not is_numeric: ignored by the class
		{Node{BaseType: "BaseSetField", Props: map[string]string{"MinimumValue": "1", "Mask": "/a/"}}, nil},
		{Node{BaseType: "DescriptionField", Props: map[string]string{"Mask": "/^[a-z]+$/i"}}, &Limits{Pattern: "(?i)^[a-z]+$"}},
		{Node{BaseType: "TextField", Props: map[string]string{"Mask": "/^(?!any$)[a-z]+$/"}}, nil}, // Go cannot run it
		{Node{BaseType: "TextField"}, nil},
	}
	for _, c := range cases {
		got := c.n.Limits()
		if (got == nil) != (c.want == nil) || got != nil && (str(got.Minimum) != str(c.want.Minimum) || str(got.Maximum) != str(c.want.Maximum) || got.Pattern != c.want.Pattern) {
			t.Errorf("%+v: %+v, want %+v", c.n, got, c.want)
		}
	}
}

func ptr(v int64) *int64 { return &v }

func str(p *int64) string {
	if p == nil {
		return "nil"
	}
	return strconv.FormatInt(*p, 10)
}

func TestMaskAccepts(t *testing.T) {
	p, _ := GoPattern(`/[0-9]+[kKmMgG]?/`) // unanchored, as unbound's cache sizes
	re := regexp.MustCompile(p)
	for v, want := range map[string]bool{"4m": true, "100": true, "contracttest1": false, "1x": false, "": false} {
		if got := MaskAccepts(re, v); got != want {
			t.Errorf("%q: %v, want %v (the first match must be the whole value)", v, got, want)
		}
	}
}
