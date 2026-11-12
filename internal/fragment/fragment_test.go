// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package fragment

import (
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
)

func release() *ir.Release {
	field := func(name string) *model.Node {
		return &model.Node{Name: name, Kind: model.KindField, Type: "TextField", Shape: model.ShapeString}
	}
	return &ir.Release{
		Version: "26.7.8", CoreVersion: "26.7.8_2", Series: "26.7", ABI: "FreeBSD:15:amd64",
		CatalogSHA256: strings.Repeat("ab", 32),
		Packages:      []ir.Package{{Name: "opnsense", Version: "26.7.8_2"}, {Name: "os-demo", Version: "1.2"}},
		Models: []*model.Model{
			{Class: `OPNsense\Demo\Demo`, Package: "os-demo", Root: &model.Node{Kind: model.KindContainer, Children: []*model.Node{field("a")}}},
			{Class: `OPNsense\Core\Core`, Package: "opnsense", Root: &model.Node{Kind: model.KindContainer}},
		},
		Endpoints: []*ir.Endpoint{
			{Path: "/api/demo/settings/get", Package: "os-demo", AlsoIn: []string{"os-demo2"}},
			{Path: "/api/core/x/get", Package: "opnsense"},
			{Path: "/api/demo/settings/set", Package: "os-demo"},
		},
	}
}

func TestSplitCoversEverythingOnce(t *testing.T) {
	rel := release()
	fs := Split(rel)
	if len(fs) != 2 || fs[0].Package != "opnsense" || fs[1].Package != "os-demo" {
		t.Fatalf("fragments: %+v", fs)
	}
	models, eps := 0, 0
	for _, f := range fs {
		models += len(f.Models)
		eps += len(f.Endpoints)
		for _, e := range f.Endpoints {
			if e.Package != f.Package {
				t.Errorf("%s in fragment %s", e.Path, f.Package)
			}
		}
	}
	if models != len(rel.Models) || eps != len(rel.Endpoints) {
		t.Errorf("lost or duplicated items: models %d/%d endpoints %d/%d", models, len(rel.Models), eps, len(rel.Endpoints))
	}
	if fs[1].Endpoints[0].Path != "/api/demo/settings/get" {
		t.Error("endpoints must be sorted")
	}
}

func TestHash(t *testing.T) {
	a, b := Split(release()), Split(release())
	if a[1].Hash() != b[1].Hash() || len(a[1].Hash()) != 16 {
		t.Fatalf("hash not stable: %s %s", a[1].Hash(), b[1].Hash())
	}
	base := a[1].Hash()
	// alternatives are provenance, not API
	b[1].Endpoints[0].AlsoIn = nil
	if b[1].Hash() != base {
		t.Error("AlsoIn must not change the hash")
	}
	// any API change changes the hash
	c := Split(release())
	c[1].Models[0].Root.Children[0].Type = "StrictTextField"
	if c[1].Hash() == base {
		t.Error("a field type change must change the hash")
	}
	d := Split(release())
	d[1].Endpoints[1].Wrapper = "x"
	if d[1].Hash() == base {
		t.Error("an endpoint change must change the hash")
	}
	if a[0].Hash() == base {
		t.Error("different packages must have different hashes")
	}
}

func TestComposeAndStatus(t *testing.T) {
	rel := release()
	c := Compose(rel, Split(rel), Provisional)
	if c.ID() != "26.7.8_2~abababababab" || len(c.Fragments) != 2 {
		t.Fatalf("composition: %+v", c)
	}
	if c.Fragments[1].Version != "1.2" || len(c.Fragments[1].AlsoIn) != 1 {
		t.Errorf("ref: %+v", c.Fragments[1])
	}
	if StatusFor("26.7.8", []string{"26.7", "26.7.8"}, pkgrepo.CompareVersions) != Provisional {
		t.Error("newest release must be provisional")
	}
	if StatusFor("26.7.7", []string{"26.7.7", "26.7.8"}, pkgrepo.CompareVersions) != Final {
		t.Error("older release must be final")
	}
}
