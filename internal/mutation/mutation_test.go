// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package mutation keeps the proof that the most important checks can
// fail (requirement Q1). Each case breaks one rule in the source and runs
// the test that guards it; the test must fail. The source files are not
// touched: the broken copy is given to the compiler with go's -overlay.
package mutation

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type mutant struct {
	name     string // the rule that is broken
	file     string // relative to the module root
	from, to string // exact text, must occur once in file
	pkg      string // package with the guarding test
	test     string // -run pattern
}

// The checks whose silent loss would hurt most: trust in the inputs (S1),
// the contract test's verdicts (S4, S8), the version order (S5) and the
// rule that nothing is guessed (S2).
var mutants = []mutant{
	{"catalog signature not checked", "internal/pkgrepo/verify.go",
		"if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, msg[:], cat.Signature); err != nil {",
		"if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, msg[:], cat.Signature); err != nil && false {",
		"./internal/pkgrepo/", "^TestVerifyCatalog"},
	{"revoked key accepted for any series", "internal/pkgrepo/verify.go",
		`if key.RevokedIn == "" || ser == "" || CompareVersions(ser, key.RevokedIn) >= 0 {`,
		`if false {`,
		"./internal/pkgrepo/", "^TestVerifyCatalogRevokedKeyOnlyForOlderSeries$"},
	{"package checksum not compared", "internal/pkgrepo/verify.go",
		`if got := "2$" + encodePkgBase32(h[:]); got != sum {`,
		`if got := "2$" + encodePkgBase32(h[:]); got != sum && false {`,
		"./internal/pkgrepo/", "^TestVerifyPackage$"},
	{"hotfix revision compared as text", "internal/pkgrepo/pkgrepo.go",
		"return comparePart(reva, revb)",
		"return strings.Compare(reva, revb)",
		"./internal/pkgrepo/", "^TestCompareVersions$"},
	{"release name from the unsigned mirror listing not checked", "internal/pkgrepo/pkgrepo.go",
		"return releaseRe.MatchString(v) && (",
		"return true || (",
		"./internal/pkgrepo/", "^TestValidRelease$"},
	{"string field type not checked", "internal/verify/read.go",
		`typeErr(p, "string", val)`,
		`_ = val`,
		"./internal/verify/", "^TestCompareNodeTypeErrors$"},
	{"missing field explained by a subset gap", "internal/verify/read.go",
		"section: ch.Kind != model.KindField,",
		"section: true,",
		"./internal/verify/", "^TestKnownGapSubsetOnlyExplainsSections$"},
	{"missing root key explained by a gap", "internal/verify/read.go",
		`if r.rootMissing && e.Wrapper != "" {`,
		`if false {`,
		"./internal/verify/", "^TestRootKeyIsData$"},
	{"own settings action gets the generic key (a guess)", "internal/ir/ir.go",
		"e.Wrapper = ownKey(a, c)",
		"e.Wrapper = c.ModelName",
		"./internal/ir/", "^TestOwnActionWrapper$"},
	{"accepted entry ignores its match", "internal/verify/accepted.go",
		`if a.Match == "" {`,
		`if true {`,
		"./internal/verify/", "^TestApplyAcceptedMatchAndUnused$"},
	{"test instance host compared as text (userinfo hides the real host)", "internal/verify/client.go",
		"slices.Contains(allowed, u.Hostname())",
		"slices.ContainsFunc(allowed, func(h string) bool { return strings.HasPrefix(base, \"https://\"+h+\":\") })",
		"./internal/verify/", "^TestNewClientRefusesOtherHosts$"},
	{"settings set writes the current value back (a set that ignores the payload passes)", "internal/verify/settings.go",
		"for _, cand := range otherValues(f.node, f.value) {",
		"for _, cand := range []string{f.value} {",
		"./internal/verify/", "^TestSettingsWriteBack$"},
	{"list order not compared after the write", "internal/verify/writeprops.go",
		"if !slices.Equal(got, pair) {",
		"if false {",
		"./internal/verify/", "^TestWriteCycleListOrderAndWriteOnly$"},
	{"golden hashes change without a generator bump", "internal/assemble/golden_test.go",
		"if pkgrepo.CompareVersions(current, golden) <= 0 {",
		"if pkgrepo.CompareVersions(current, golden) < 0 {",
		"./internal/assemble/", "^TestGeneratorRule$"},
	{"a mismatch does not fail the run", "internal/verify/status.go",
		"func (s Status) Failed() bool { return s == StatusMismatch || s == StatusError }",
		"func (s Status) Failed() bool { return s == StatusError }",
		"./internal/verify/", "^TestFailureRule$"},
	{"a probe the firewall rejects on its own counts as agreed", "internal/verify/probe.go",
		"p.Outcome = map[bool]Outcome{true: OutcomeContradicted, false: OutcomeAgreed}[p.Accept]",
		"p.Outcome = OutcomeAgreed",
		"./internal/verify/", "^TestProbeRunContradictions$"},
	{"a probe predicted rejected but saved counts as agreed", "internal/verify/probe.go",
		"case b.saved:\n\t\t\tp.Outcome = OutcomeContradicted",
		"case b.saved:\n\t\t\tp.Outcome = OutcomeAgreed",
		"./internal/verify/", "^TestProbeRunContradictions$"},
	{"an accepted probe not read back", "internal/verify/probe.go",
		"case !sameValue(p.Value, s):",
		"case false:",
		"./internal/verify/", "^TestProbeRunContradictions$"},
	{"core lists not used for prerequisites when the core is not tested", "internal/verify/write.go",
		"	withCore[ir.CorePackage] = true\n",
		"	_ = ir.CorePackage\n",
		"./internal/verify/", "^TestWriteCyclePrerequisiteFromTheCoreWhenItIsNotTested$"},
	{"package tested on two VMs merged without error", "internal/groups/merge.go",
		"if j, ok := by[pk]; ok {",
		"if j, ok := by[pk]; ok && false {",
		"./internal/groups/", "^TestMerge$"},
	{"plugins that need each other split over VMs", "internal/groups/groups.go",
		"			union(from, to)\n",
		"			_ = union\n",
		"./internal/groups/", "^TestMake$"},
	{"together list of the groups file ignored", "internal/groups/groups.go",
		"			union(have[0], p)\n",
		"			_ = p\n",
		"./internal/groups/", "^TestMakeNamedGroups$"},
	{"inherited controller of another package not a dependency", "internal/groups/groups.go",
		`add(e.Package, o, "controller "`,
		`_, _ = o, ("controller "`,
		"./internal/groups/", "^TestDependencies$"},
}

// Claim (Q1): every check in the table above fails when its rule is
// removed. A mutant that survives means the test lost its power; an
// anchor that no longer matches means the code changed and the table must
// follow it (the failure says which).
func TestMutantsAreKilled(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test once per mutant")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	gobin := filepath.Join(os.Getenv("GOROOT"), "bin", "go")
	if _, err := os.Stat(gobin); err != nil {
		gobin = "go"
	}
	for _, m := range mutants {
		t.Run(m.name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(root, m.file))
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(string(src), m.from); n != 1 {
				t.Fatalf("anchor found %d times in %s (want 1); update the mutant to the current code: %q", n, m.file, m.from)
			}
			dir := t.TempDir()
			mutated := filepath.Join(dir, filepath.Base(m.file))
			if err := os.WriteFile(mutated, []byte(strings.Replace(string(src), m.from, m.to, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			overlay, _ := json.Marshal(map[string]map[string]string{"Replace": {filepath.Join(root, m.file): mutated}})
			ov := filepath.Join(dir, "overlay.json")
			if err := os.WriteFile(ov, overlay, 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(gobin, "test", "-count=1", "-overlay", ov, "-run", m.test, m.pkg)
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOFLAGS=")
			out, err := cmd.CombinedOutput()
			switch {
			case err == nil:
				t.Errorf("mutant survived: %s still passes with %q", m.test, m.to)
			case strings.Contains(string(out), "[build failed]") || strings.Contains(string(out), "[setup failed]"):
				t.Errorf("mutant does not compile; break the rule, not the build:\n%s", out)
			case !strings.Contains(string(out), "--- FAIL"):
				t.Errorf("go test failed without a failing test:\n%s", out)
			}
		})
	}
}
