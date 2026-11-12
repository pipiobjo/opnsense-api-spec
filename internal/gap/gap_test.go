// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package gap

import "testing"

func TestMayDiffer(t *testing.T) {
	// the codes that turn a contract-test mismatch into a known gap
	for _, c := range []Code{GetModelNodesOverride, OverrideCallsParent, OverrideNotDerived, ServiceOverride, CustomLogic, PostProcessed} {
		if !MayDiffer(c) {
			t.Errorf("%s must count as may-differ", c)
		}
	}
	for _, c := range []Code{CustomAction, MethodUnknown, NoApplyEndpoint, FieldTypeUnresolved, CustomReadShape, InvalidDefault} {
		if MayDiffer(c) {
			t.Errorf("%s must not count as may-differ", c)
		}
	}
}

func TestHasAndFirst(t *testing.T) {
	gs := []Gap{New(MethodUnknown, "x"), New(PostProcessed, "result of %s", "getBase")}
	if !Has(gs, PostProcessed) || Has(gs, CustomAction) {
		t.Error("Has")
	}
	g, ok := First(gs, MayDiffer)
	if !ok || g.Code != PostProcessed || g.Detail != "result of getBase" {
		t.Errorf("First: %+v %v", g, ok)
	}
	if g.String() != "post-processed: result of getBase" {
		t.Errorf("String: %q", g.String())
	}
}
