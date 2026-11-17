// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package gap

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs/testing.md says which test shows which claim. This keeps it true:
// every Test and Fuzz function in the repository is named there, and every
// name there exists.
func TestEveryTestIsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../docs/testing.md")
	if err != nil {
		t.Fatal(err)
	}
	inDoc := map[string]bool{}
	for _, m := range regexp.MustCompile("`((?:Test|Fuzz)[A-Za-z0-9]+)`").FindAllStringSubmatch(string(doc), -1) {
		inDoc[m[1]] = true
	}
	inCode := map[string]bool{}
	fn := regexp.MustCompile(`(?m)^func ((?:Test|Fuzz)[A-Za-z0-9]+)\(`)
	err = filepath.WalkDir("../..", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != "../.." {
			return filepath.SkipDir
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, m := range fn.FindAllStringSubmatch(string(src), -1) {
			inCode[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inCode) < 50 {
		t.Fatalf("found only %d tests; the walk is broken", len(inCode))
	}
	for n := range inCode {
		if !inDoc[n] {
			t.Errorf("%s is not named in docs/testing.md (add it to the claim it shows)", n)
		}
	}
	for n := range inDoc {
		if !inCode[n] {
			t.Errorf("docs/testing.md names %s, which does not exist", n)
		}
	}
}
