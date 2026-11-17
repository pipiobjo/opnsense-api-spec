// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package php

import (
	"reflect"
	"strings"
	"testing"
)

func TestStripCommentsKeepsStrings(t *testing.T) {
	// moved from the controller package; plus a URL in a string
	src := "a // line\nb /* block\n */ c '//not' \"/*not*/\" 'http://x' # hash\nd"
	got := StripComments(src)
	for _, want := range []string{"a ", "b ", " c ", "'//not'", "\"/*not*/\"", "'http://x'", "d"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lost %q", got, want)
		}
	}
	for _, gone := range []string{"line", "block", "hash"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q kept the comment %q", got, gone)
		}
	}
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Error("line count must be preserved")
	}
}

func TestMatchSkipsStrings(t *testing.T) {
	s := `{ $a = "}"; $b = '{'; if (x) { y(); } }`
	if got := Match(s, 0, '{', '}'); got != len(s)-1 {
		t.Errorf("Match = %d, want %d", got, len(s)-1)
	}
	if Match("{ {", 0, '{', '}') != -1 {
		t.Error("an unclosed bracket must give -1")
	}
}

func TestSplitArgs(t *testing.T) {
	got := SplitArgs(`'a', ['x' => 1, 'y' => f(2, 3)], "c,d", $e`)
	want := []string{`'a'`, `['x' => 1, 'y' => f(2, 3)]`, `"c,d"`, `$e`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
}

// PHP's name resolution, where the two former copies disagreed.
func TestQualify(t *testing.T) {
	h := ParseHeader("namespace OPNsense\\Demo;\nuse OPNsense\\Base\\FieldTypes\\BaseField;\nuse OPNsense\\Base as B;\n")
	for in, want := range map[string]string{
		`\OPNsense\X\Y`:   `OPNsense\X\Y`,                       // fully qualified
		`BaseField`:       `OPNsense\Base\FieldTypes\BaseField`, // imported
		`B\FieldTypes\Z`:  `OPNsense\Base\FieldTypes\Z`,         // alias as first segment
		`FieldTypes\Mine`: `OPNsense\Demo\FieldTypes\Mine`,      // relative qualified
		`Local`:           `OPNsense\Demo\Local`,                // unqualified
		``:                ``,
	} {
		if got := h.Qualify(in); got != want {
			t.Errorf("Qualify(%q) = %q, want %q", in, got, want)
		}
	}
}

// Several classes in one file each get their own body; a commented-out
// method is not part of any body once comments are stripped.
func TestClasses(t *testing.T) {
	src := `<?php
namespace OPNsense\Interfaces\FieldTypes;
use OPNsense\Base\FieldTypes\ArrayField;
abstract class ContainerThing extends \OPNsense\Base\FieldTypes\ContainerField
{
    public function toLegacy() { return "}"; }
}
class ThingField extends ArrayField
{
    // public function setValue($v) {}
    public function newContainerField($ref, $tag) { return new ContainerThing($ref, $tag); }
}
`
	code := StripComments(src)
	cs := Classes(code, ParseHeader(code))
	if len(cs) != 2 {
		t.Fatalf("classes: %+v", cs)
	}
	if cs[0].Name != `OPNsense\Interfaces\FieldTypes\ContainerThing` || !cs[0].Abstract || cs[0].Extends != `OPNsense\Base\FieldTypes\ContainerField` {
		t.Errorf("first: %+v", cs[0])
	}
	if cs[1].Extends != `OPNsense\Base\FieldTypes\ArrayField` || strings.Contains(cs[1].Body, "toLegacy") || strings.Contains(cs[1].Body, "setValue") {
		t.Errorf("second: %+v", cs[1])
	}
	if !strings.Contains(cs[0].Body, "toLegacy") || strings.Contains(cs[0].Body, "newContainerField") {
		t.Errorf("first body must hold exactly its own methods: %q", cs[0].Body)
	}
}
