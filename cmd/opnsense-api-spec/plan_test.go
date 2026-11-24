// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/pipeline"
)

// stdout runs f and returns what it printed.
func stdout(t *testing.T, f func() error) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	ferr := f()
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	if ferr != nil {
		t.Fatal(ferr)
	}
	return strings.TrimSpace(string(out))
}

// The commands the pipeline calls in prepare and build: index records the
// unused accepted entries, plan prints the list or the matrix.
func TestIndexAndPlanCommands(t *testing.T) {
	dist := t.TempDir()
	acc := filepath.Join(t.TempDir(), "accepted.json")
	if err := os.WriteFile(acc, []byte(`{"findings":[{"path":"/a","check":"shape","releases":"*","match":"HTTP 500","reason":"r","evidence":"e"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cmdIndex(dist, acc); err != nil {
		t.Fatal(err)
	}
	idx, err := pipeline.ReadIndex(filepath.Join(dist, "index.json"))
	if err != nil || len(idx.Releases) != 0 || len(idx.UnusedAccepted) != 0 {
		t.Fatalf("empty dist: %+v %v", idx, err)
	}
	if err := cmdIndex(dist, filepath.Join(dist, "missing.json")); err != nil {
		t.Errorf("no accepted list is fine: %v", err)
	}
	if got := stdout(t, func() error { return cmdPlan(dist, "", false, false) }); got != "[]" {
		t.Errorf("plan %s", got)
	}
	if got := stdout(t, func() error { return cmdPlan(dist, " ", true, true) }); got != "[]" {
		t.Errorf("matrix %s", got)
	}
	if err := cmdPlan(dist, "26.7.5", true, false); err == nil {
		t.Error("a release that is not built must be refused")
	}
}
