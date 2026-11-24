// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package controller

import (
	"os"
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
	cs, err := LoadAll(liveApp)
	if err != nil {
		t.Fatal(err)
	}
	acts, calls, raw := 0, 0, 0
	for _, c := range cs {
		acts += len(c.Actions)
		for _, g := range c.Gaps {
			t.Logf("gap %s/%s: %s", c.Module, c.Name, g)
		}
		for _, a := range c.Actions {
			calls += len(a.Calls)
			for _, bc := range a.Calls {
				if bc.Raw != "" {
					raw++
				}
			}
		}
	}
	t.Logf("controllers=%d actions=%d basecalls=%d unresolved-args=%d", len(cs), acts, calls, raw)
	for _, c := range cs {
		if (c.Module == "unbound" && c.Name == "settings") || (c.Module == "kea" && (c.Name == "dhcpv4" || c.Name == "ctrl_agent" || c.Name == "service")) {
			t.Logf("== /api/%s/%s model=%s class=%s chain=%v", c.Module, c.Name, c.ModelName, c.ModelClass, c.Chain)
			for _, a := range c.Actions {
				t.Logf("   %-22s params=%v calls=%v parent=%v post=%v get=%v overrides=%q", a.Name, a.Params, a.Calls, a.CallsParent, a.ChecksPost, a.ChecksGet, a.Overrides)
			}
		}
	}
}
