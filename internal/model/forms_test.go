// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"os"
	"path/filepath"
	"testing"
)

// TestForms lists the forms of the described classes in the releases
// below OPNSENSE_FORMS (a directory of fetched sources, .cache/src): the
// review aid behind the form tables in validation.go.
func TestForms(t *testing.T) {
	dir := os.Getenv("OPNSENSE_FORMS")
	if dir == "" {
		t.Skip("OPNSENSE_FORMS not set")
	}
	rels, _ := os.ReadDir(dir)
	for _, r := range rels {
		models := filepath.Join(dir, r.Name(), "core+plugins", "models")
		idx, err := BuildTypeIndex(models)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range idx.Forms() {
			t.Logf("%s %s", r.Name(), f)
		}
	}
}
