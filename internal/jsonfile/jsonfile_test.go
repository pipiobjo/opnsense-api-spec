// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package jsonfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.json")
	in := map[string]any{"b": "<a&b>", "a": []int{1}}
	if err := Write(p, in); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if string(raw) != "{\n  \"a\": [\n    1\n  ],\n  \"b\": \"<a&b>\"\n}\n" {
		t.Errorf("format: %q", raw)
	}
	var out map[string]any
	if err := Read(p, &out); err != nil || out["b"] != "<a&b>" {
		t.Errorf("read: %v %v", out, err)
	}
	if err := os.WriteFile(p, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Read(p, &out); err == nil || !strings.Contains(err.Error(), "x.json") {
		t.Errorf("a decode error must name the file: %v", err)
	}
}
