// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package quality

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

func TestFingerprintIgnoresCommentsAndWhitespace(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, FrameworkFiles[1])
	_ = os.MkdirAll(filepath.Dir(f), 0o755)
	write := func(s string) string {
		t.Helper()
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		_, h, err := Fingerprint(dir)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	a := write("<?php\n/* Copyright 2025 */\nclass A { function getAction() { return 1; } }\n")
	b := write("<?php\n/* Copyright 2026 */\n\nclass A {\n  function getAction() { return 1; }\n}\n")
	c := write("<?php\nclass A { function getAction() { return 2; } }\n")
	if a != b {
		t.Error("comments and whitespace must not change the fingerprint")
	}
	if a == c {
		t.Error("a code change must change the fingerprint")
	}
	files, _, _ := Fingerprint(dir)
	if files[FrameworkFiles[0]] != "missing" {
		t.Errorf("missing files must be recorded: %v", files)
	}
}

func rel(version string, eps []*ir.Endpoint, fields []*model.Node, fw map[string]string) *ir.Release {
	return &ir.Release{
		Version:   version,
		Packages:  []ir.Package{{Name: "opnsense"}},
		Models:    []*model.Model{{Class: "M", Package: "opnsense", Root: &model.Node{Kind: model.KindContainer, Children: fields}}},
		Endpoints: eps,
		Framework: fw,
	}
}

func TestCompare(t *testing.T) {
	prev := rel("1", []*ir.Endpoint{
		{Path: "/a", Source: ir.SourceBase},
		{Path: "/b", Source: ir.SourceBaseCall},
	}, []*model.Node{{Name: "f", Kind: model.KindField, Shape: model.ShapeString}}, map[string]string{"x.php": "1"})
	cur := rel("2", []*ir.Endpoint{
		{Path: "/a", Source: ir.SourceSignature},                                               // regression
		{Path: "/b", Source: ir.SourceBaseCall, Gaps: []gap.Gap{gap.New(gap.CustomLogic, "")}}, // new gap
		{Path: "/c", Source: ir.SourceSignature},                                               // new endpoint: not a regression
	}, []*model.Node{{Name: "f", Kind: model.KindField, Shape: model.ShapeString, Gaps: []gap.Gap{gap.New(gap.CustomReadShape, "")}}},
		map[string]string{"x.php": "2"})
	r := Compare(prev, cur)
	if len(r.Regressions) != 2 || !strings.Contains(strings.Join(r.Regressions, " "), "/a: typed") ||
		!strings.Contains(strings.Join(r.Regressions, " "), "field M f") {
		t.Errorf("regressions: %v", r.Regressions)
	}
	if r.NewGaps[gap.CustomLogic] != 1 {
		t.Errorf("new gaps: %v", r.NewGaps)
	}
	if len(r.FrameworkChanged) != 1 || !r.Notable() {
		t.Errorf("framework: %v", r.FrameworkChanged)
	}
	if same := Compare(prev, prev); same.Notable() || len(same.NewGaps) != 0 {
		t.Errorf("identical releases: %+v", same)
	}
}

func TestInvariants(t *testing.T) {
	r := rel("1", []*ir.Endpoint{
		{Path: "/a", Methods: []string{"GET"}, Package: "opnsense", Model: "M"},
		{Path: "/a", Methods: []string{"GET"}, Package: "opnsense"},
		{Path: "/b", Package: "os-missing", Model: "Nope"},
	}, nil, nil)
	v := Invariants(r, []string{"#/components/schemas/Known", "#/components/schemas/Gone", "../common/x.json#/components/schemas/Y"},
		map[string]bool{"Known": true})
	joined := strings.Join(v, "\n")
	for _, want := range []string{"duplicate endpoint path /a", "/b has no HTTP method", "unknown model Nope", `package "os-missing"`, "unresolved $ref #/components/schemas/Gone"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing violation %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "common") {
		t.Error("external refs are not checked here")
	}
}
