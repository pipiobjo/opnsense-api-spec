// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package gap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Every gap code is explained in the table of docs/concepts.md. The codes
// are read from this package's source, so a new code without a row fails
// here instead of drifting.
func TestEveryCodeIsDocumented(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "gap.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.CONST {
			continue
		}
		for _, s := range g.Specs {
			v := s.(*ast.ValueSpec)
			if id, ok := v.Type.(*ast.Ident); !ok || id.Name != "Code" {
				continue
			}
			for _, val := range v.Values {
				if lit, ok := val.(*ast.BasicLit); ok {
					code, _ := strconv.Unquote(lit.Value)
					codes = append(codes, code)
				}
			}
		}
	}
	if len(codes) < 10 {
		t.Fatalf("found only %d codes in gap.go", len(codes))
	}
	doc, err := os.ReadFile("../../docs/concepts.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range codes {
		if !strings.Contains(string(doc), "| `"+c+"` |") {
			t.Errorf("gap code %q has no row in the table of docs/concepts.md", c)
		}
	}
}
