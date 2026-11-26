// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package groups

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

// Claim: a contract test split over several VMs (one plugin group each,
// the core on exactly one) merges into the report one run over all
// packages writes: same packages, results in the same order, plugins not
// installable on any VM, accepted entries used on any VM count as used.
// Parts of another release, instance version or generator, and a package
// tested twice, are refused.
func TestMerge(t *testing.T) {
	acc := []verify.Accepted{
		{Path: "/api/a", Check: verify.CheckShape, Releases: "*", Match: "x", Reason: "r", Evidence: "e"},
		{Path: "/api/b", Check: verify.CheckShape, Releases: "*", Match: "x", Reason: "r", Evidence: "e"},
		{Path: "/api/c", Check: verify.CheckShape, Releases: "*", Match: "x", Reason: "r", Evidence: "e"},
	}
	res := func(p string, c verify.Check, s verify.Status) *verify.Result {
		return &verify.Result{Path: p, Method: "GET", Check: c, Status: s}
	}
	part := func(pk []string, date string, rs ...*verify.Result) *verify.Report {
		return &verify.Report{Release: "26.7.10", Composition: "26.7.10~x", Instance: "26.7.10_1", Generator: "0.10.0",
			Date: date, Packages: pk, Results: rs}
	}
	// what one run over everything wrote (results sorted by path, check)
	serial := part([]string{"opnsense", "os-a", "os-b"}, "2026-11-26T10:05:00Z",
		res("/api/a/x", verify.CheckSchema, verify.StatusOK), res("/api/a/x", verify.CheckShape, verify.StatusAccepted),
		res("/api/core/y", verify.CheckShape, verify.StatusOK), res("/api/core/y", verify.CheckWriteCycle, verify.StatusSkipped),
		res("/api/osb/z", verify.CheckReachable, verify.StatusOK))
	serial.NotInstallable = []string{"os-c", "os-d"}
	serial.UsedAccepted = []string{acc[0].ID(), acc[2].ID()}
	serial.UnusedAccepted = []string{acc[1].ID()}

	core := part([]string{"opnsense"}, "2026-11-26T10:01:00Z", serial.Results[2], serial.Results[3])
	core.UsedAccepted, core.UnusedAccepted = []string{acc[2].ID()}, []string{acc[0].ID(), acc[1].ID()}
	a := part([]string{"os-a"}, "2026-11-26T10:05:00Z", serial.Results[0], serial.Results[1])
	a.NotInstallable = []string{"os-d"}
	a.UsedAccepted, a.UnusedAccepted = []string{acc[0].ID()}, []string{acc[1].ID(), acc[2].ID()}
	b := part([]string{"os-b"}, "2026-11-26T10:03:00Z", serial.Results[4])
	b.NotInstallable = []string{"os-c", "os-d"}
	b.UnusedAccepted = []string{acc[0].ID(), acc[1].ID(), acc[2].ID()}

	for _, order := range [][]*verify.Report{{core, a, b}, {b, a, core}} {
		got, err := MergeReports(order, acc)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, serial) {
			t.Errorf("merged %+v\nwant   %+v", got, serial)
		}
	}

	for name, c := range map[string]struct {
		mod  func(*verify.Report)
		want string
	}{
		"core twice":       {func(r *verify.Report) { r.Packages = []string{"opnsense", "os-b"} }, "package opnsense"},
		"other instance":   {func(r *verify.Report) { r.Instance = "26.7.10_2" }, "instance version"},
		"other release":    {func(r *verify.Report) { r.Release = "26.7.9" }, "release"},
		"other generator":  {func(r *verify.Report) { r.Generator = "0.9.0" }, "generator"},
		"other data state": {func(r *verify.Report) { r.Composition = "26.7.10~y" }, "composition"},
	} {
		bad := *b
		c.mod(&bad)
		if _, err := MergeReports([]*verify.Report{core, a, &bad}, acc); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error naming %q", name, err, c.want)
		}
	}
	if _, err := MergeReports(nil, acc); err == nil {
		t.Error("nothing to merge must be an error")
	}
}
