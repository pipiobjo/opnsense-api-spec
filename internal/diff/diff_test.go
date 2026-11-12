// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package diff

import (
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

func field(name, typ string) *model.Node {
	return &model.Node{Name: name, Kind: model.KindField, Type: typ, Shape: model.ShapeString}
}

func container(name string, cs ...*model.Node) *model.Node {
	return &model.Node{Name: name, Kind: model.KindContainer, Children: cs}
}

// The safesearch move between OPNsense 25.7 and 26.1, in miniature.
func TestCompareSafesearchMove(t *testing.T) {
	a := &ir.Release{Version: "25.7",
		Packages: []ir.Package{{Name: "opnsense", Version: "25.7"}},
		Models: []*model.Model{{Class: `OPNsense\Unbound\Unbound`, Version: "1.0.12", Package: "opnsense",
			Root: container("", container("general"), container("dnsbl", field("safesearch", "BooleanField")))}},
		Endpoints: []*ir.Endpoint{{Path: "/api/unbound/settings/get", Kind: ir.KindModelGet, Package: "opnsense"}},
	}
	b := &ir.Release{Version: "26.1",
		Packages: []ir.Package{{Name: "opnsense", Version: "26.1"}},
		Models: []*model.Model{{Class: `OPNsense\Unbound\Unbound`, Version: "1.0.13", Package: "opnsense",
			Root: container("", container("general", field("safesearch", "BooleanField")), container("dnsbl"))}},
		Endpoints: []*ir.Endpoint{
			{Path: "/api/unbound/settings/get", Kind: ir.KindModelGet, Package: "opnsense"},
			{Path: "/api/unbound/settings/addDnsbl", Kind: ir.KindItemAdd, Package: "opnsense"},
		},
	}
	r := Compare(a, b)
	want := map[string]string{
		`OPNsense\Unbound\Unbound dnsbl.safesearch`:   "removed",
		`OPNsense\Unbound\Unbound general.safesearch`: "added",
	}
	for _, c := range r.Fields {
		if w, ok := want[c.Subject]; ok && w == c.Kind {
			delete(want, c.Subject)
		}
	}
	if len(want) != 0 {
		t.Errorf("missing field changes %v in %+v", want, r.Fields)
	}
	if len(r.Models) != 1 || r.Models[0].Old != "1.0.12" || r.Models[0].New != "1.0.13" {
		t.Errorf("model version: %+v", r.Models)
	}
	if len(r.Endpoints) != 1 || r.Endpoints[0].Kind != "added" {
		t.Errorf("endpoints: %+v", r.Endpoints)
	}
	if len(r.Packages) != 1 || r.Packages[0].Kind != "changed" {
		t.Errorf("packages: %+v", r.Packages)
	}
	md := r.Markdown()
	if !strings.Contains(md, "general.safesearch") || !strings.Contains(md, "| fields | 1 | 1 | 0 |") {
		t.Errorf("markdown:\n%s", md)
	}
	same := Compare(a, a)
	if !same.Empty() || same.Fields == nil || same.Endpoints == nil {
		t.Errorf("same release must give empty, non-nil lists: %+v", same)
	}
}
