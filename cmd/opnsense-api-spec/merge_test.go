// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pipiobjo/opnsense-api-spec/internal/jsonfile"
	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

// Claim: merge-verified writes the merged report and its OpenAPI document
// where verify writes them, fails like verify when a merged check failed,
// and refuses reports of another release than the IR.
func TestMergeVerifiedCommand(t *testing.T) {
	dir := t.TempDir()
	irf := filepath.Join(dir, "opnsense-26.7.10.ir.json")
	if err := os.WriteFile(irf, []byte(`{"generator":{"name":"opnsense-api-spec","version":"0.10.0"},"version":"26.7.10","series":"26.7","abi":"FreeBSD:15:amd64","catalog_signed_by":"k","packages":[],"models":[],"endpoints":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	part := func(name, pk string, st verify.Status) string {
		f := filepath.Join(dir, name)
		rep := &verify.Report{Release: "26.7.10", Instance: "26.7.10", Generator: "0.10.0", Packages: []string{pk},
			Results: []*verify.Result{{Path: "/api/" + pk, Method: "GET", Check: verify.CheckReachable, Status: st}}}
		if err := jsonfile.Write(f, rep); err != nil {
			t.Fatal(err)
		}
		return f
	}
	a, b := part("a.json", "opnsense", verify.StatusOK), part("b.json", "os-a", verify.StatusOK)
	o := opts{irFile: irf}
	if err := cmdMergeVerified(o, filepath.Join(dir, "none.json"), []string{a, b}); err != nil {
		t.Fatal(err)
	}
	var got verify.Report
	if err := jsonfile.Read(filepath.Join(dir, "opnsense-26.7.10.verified.json"), &got); err != nil || len(got.Results) != 2 {
		t.Fatalf("merged report next to the IR: %+v %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "opnsense-26.7.10.openapi.json")); err != nil {
		t.Errorf("OpenAPI document: %v", err)
	}
	o.out = filepath.Join(dir, "elsewhere.verified.json")
	bad := part("c.json", "os-b", verify.StatusMismatch)
	if err := cmdMergeVerified(o, "", []string{a, bad}); err == nil || !strings.Contains(err.Error(), "1 contract check(s) failed") {
		t.Errorf("a failed check in a part must fail the merge: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "elsewhere.openapi.json")); err != nil {
		t.Errorf("-out: %v", err)
	}
	other := filepath.Join(dir, "d.json")
	if err := jsonfile.Write(other, &verify.Report{Release: "26.7.9", Generator: "0.10.0", Packages: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if err := cmdMergeVerified(opts{irFile: irf}, "", []string{other}); err == nil || !strings.Contains(err.Error(), "release 26.7.9") {
		t.Errorf("reports of another release: %v", err)
	}
}
