// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pipeline

import (
	"path/filepath"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/jsonfile"
)

// first_seen is set once, when a composition is built for the first time;
// a rebuild keeps it; a composition from before the field existed stays
// without one ("unknown" must never read as "new").
func TestFirstSeen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.json")
	if got := firstSeen(path, "2026-11-17"); got != "2026-11-17" {
		t.Errorf("new composition: %q", got)
	}
	if err := jsonfile.Write(path, map[string]any{"release": "26.7.9", "first_seen": "2026-11-10"}); err != nil {
		t.Fatal(err)
	}
	if got := firstSeen(path, "2026-11-17"); got != "2026-11-10" {
		t.Errorf("rebuilt composition must keep its date: %q", got)
	}
	if err := jsonfile.Write(path, map[string]any{"release": "26.7.9"}); err != nil {
		t.Fatal(err)
	}
	if got := firstSeen(path, "2026-11-17"); got != "" {
		t.Errorf("a composition from before the field has no date: %q", got)
	}
}
