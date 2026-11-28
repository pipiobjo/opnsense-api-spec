// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package model

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// a golden release: tools/ci/golden-fetch.sh extracts it before the tests
const liveApp = "../../.cache/src/26.7.7/core+plugins"

func TestLiveSummary(t *testing.T) {
	if _, err := os.Stat(liveApp); err != nil {
		if os.Getenv("GOLDEN_REQUIRED") == "1" {
			t.Fatalf("%s not extracted (GOLDEN_REQUIRED=1; run tools/ci/golden-fetch.sh)", liveApp)
		}
		t.Skip("no fetched release in .cache")
	}
	ms, _, err := LoadAll(liveApp)
	if err != nil {
		t.Fatal(err)
	}
	fields, unknown, arrays := 0, 0, 0
	var walk func(n *Node)
	walk = func(n *Node) {
		switch n.Kind {
		case KindField:
			fields++
			if n.Shape == ShapeUnknown {
				unknown++
				t.Logf("unknown: %s %s", n.Name, n.Type)
			}
		case KindArray:
			arrays++
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	for _, m := range ms {
		walk(m.Root)
	}
	t.Logf("models=%d fields=%d arrays=%d unknown=%d", len(ms), fields, arrays, unknown)
	for _, m := range ms {
		if m.Class == `OPNsense\Unbound\Unbound` {
			for _, p := range []string{"general.safesearch", "dnsbl.blocklist", "dnsbl.blocklist.type", "general.active_interface"} {
				n := m.Find(p)
				if n == nil {
					t.Errorf("missing %s", p)
					continue
				}
				t.Logf("%s kind=%s type=%s base=%s shape=%s req=%v opts=%d", p, n.Kind, n.Type, n.BaseType, n.Shape, n.Required, len(n.Options))
			}
		}
	}
}

// TestLiveChecks: on a real release, every described class has a reviewed
// form, and the checks of fields a firewall configuration depends on are
// derived as the PHP code has them.
func TestLiveChecks(t *testing.T) {
	if _, err := os.Stat(liveApp); err != nil {
		if os.Getenv("GOLDEN_REQUIRED") == "1" {
			t.Fatalf("%s not extracted", liveApp)
		}
		t.Skip("no fetched release in .cache")
	}
	ms, types, err := LoadAll(liveApp)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range types.Forms() {
		if strings.HasSuffix(f, "NOT REVIEWED") {
			t.Errorf("form not reviewed: %s", f)
		}
	}
	find := func(class, path string) *Node {
		for _, m := range ms {
			if m.Class == class {
				if n := m.Find(path); n != nil {
					return n
				}
			}
		}
		t.Fatalf("%s %s not found", class, path)
		return nil
	}
	port := find(`OPNsense\Firewall\Filter`, "rules.rule.destination_port").Limits()
	if port == nil || port.Format == nil || port.Format.Kind != FormatPort || !port.Format.Ranges || !port.Format.Aliases ||
		!port.Format.Any || !slices.Contains(port.Format.Services, "https") || !slices.Contains(port.Format.Services, "afs3-fileserver") {
		// up to 26.7.5 ['any'] + array_keys(...) lost the first name
		t.Errorf("firewall rule port: %+v", port)
	}
	dns := find(`OPNsense\IPsec\IPsec`, "charon.plugins.attr.dns").Limits()
	if dns == nil || dns.Format == nil || dns.Format.Kind != FormatNetwork || dns.Format.Netmask != NetmaskForbidden {
		t.Errorf("IPsec dns (NetmaskAllowed, lower-case m, reaches setNetMaskAllowed): %+v", dns)
	}
}
