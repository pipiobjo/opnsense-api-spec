package model

import (
	"os"
	"testing"
)

const liveApp = "../../.cache/src/26.7.5/core"

func TestLiveSummary(t *testing.T) {
	if _, err := os.Stat(liveApp); err != nil {
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
