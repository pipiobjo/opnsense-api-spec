// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package sbom

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/crypto/blake2b"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
)

// pkgSum is pkg's catalog checksum of data (BLAKE2b-512 in pkg's base32).
func pkgSum(data string) (sum, hexDigest string) {
	h := blake2b.Sum512([]byte(data))
	const alphabet = "ybndrfg8ejkmcpqxot1uwisza345h769"
	var sb strings.Builder
	var acc uint32
	bits := 0
	for _, b := range h {
		acc |= uint32(b) << bits
		bits += 8
		for bits >= 5 {
			sb.WriteByte(alphabet[acc&31])
			acc >>= 5
			bits -= 5
		}
	}
	if bits > 0 {
		sb.WriteByte(alphabet[acc&31])
	}
	return "2$" + sb.String(), hex.EncodeToString(h[:])
}

func release(t *testing.T) (*ir.Release, map[string]string) {
	t.Helper()
	r := &ir.Release{Version: "26.7.9", Series: "26.7", CoreVersion: "26.7.9_3", ABI: "FreeBSD:15:amd64",
		CatalogSHA256: strings.Repeat("ab", 32), SignedBy: "opnsense-26.7"}
	r.Generator.Name, r.Generator.Version = "opnsense-api-spec", "0.9.3"
	want := map[string]string{}
	for _, p := range [][2]string{{"os-frr", "1.48_1"}, {"opnsense", "26.7.9_3"}, {"os-c+c", "1.0+b"}} {
		sum, digest := pkgSum(p[0] + p[1])
		r.Packages = append(r.Packages, ir.Package{Name: p[0], Version: p[1], Commit: "9e942da1c", Sum: sum})
		want[p[0]] = digest
	}
	return r, want
}

// Claim (testing.md, S6): the SBOM of a release is valid CycloneDX 1.6,
// lists every package the description was derived from with its real
// hash, and is byte-identical for the same input.
func TestForRelease(t *testing.T) {
	r, want := release(t)
	out, err := ForRelease(r)
	if err != nil {
		t.Fatal(err)
	}
	validate(t, out)

	var b bom
	if err := json.Unmarshal(out, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Components) != len(r.Packages) || len(b.Dependencies) != 1 || len(b.Dependencies[0].DependsOn) != len(r.Packages) {
		t.Errorf("components %d, dependencies %+v", len(b.Components), b.Dependencies)
	}
	for _, c := range b.Components {
		if len(c.Hashes) != 1 || c.Hashes[0].Alg != "BLAKE2b-512" || c.Hashes[0].Content != want[c.Name] {
			t.Errorf("%s: hashes %+v, want BLAKE2b-512 %s", c.Name, c.Hashes, want[c.Name])
		}
	}
	if c := b.Components[0]; c.Name != "opnsense" && c.Name != "os-c+c" {
		t.Errorf("components not sorted by name: first is %s", c.Name)
	}
	if !strings.Contains(string(out), `"pkg:generic/opnsense/os-c+c@1.0+b"`) && !strings.Contains(string(out), `"pkg:generic/opnsense/os-c%2Bc@1.0%2Bb"`) {
		t.Errorf("purl of a name with + not found")
	}
	if b.Metadata.Component.Version != "26.7.9" || b.Metadata.Tools.Components[0].Version != "0.9.3" {
		t.Errorf("metadata: %+v", b.Metadata)
	}
	again, _ := ForRelease(r)
	if !bytes.Equal(out, again) {
		t.Error("not deterministic")
	}
	r.Packages[0].Version = "1.48_2"
	changed, _ := ForRelease(r)
	var b2 bom
	_ = json.Unmarshal(changed, &b2)
	if b2.SerialNumber == b.SerialNumber {
		t.Error("the serial number must change with the content")
	}
}

func TestForReleaseRefusesBadInput(t *testing.T) {
	r, _ := release(t)
	r.Packages[1].Sum = "2$not-base32!"
	if _, err := ForRelease(r); err == nil {
		t.Error("a checksum that does not decode must be refused, not written")
	}
	if _, err := ForRelease(&ir.Release{Version: "26.7.9"}); err == nil {
		t.Error("an IR without packages must be refused")
	}
}

// validate checks out against the official CycloneDX 1.6 JSON Schema
// (testdata, from github.com/CycloneDX/specification, tag 1.6).
func validate(t *testing.T, out []byte) {
	t.Helper()
	c := jsonschema.NewCompiler()
	for _, f := range []string{"bom-1.6.schema.json", "spdx.schema.json", "jsf-0.82.schema.json"} {
		raw, err := os.ReadFile("testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := c.AddResource("http://cyclonedx.org/schema/"+f, doc); err != nil {
			t.Fatal(err)
		}
	}
	sch, err := c.Compile("http://cyclonedx.org/schema/bom-1.6.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(inst); err != nil {
		t.Errorf("not valid CycloneDX 1.6: %v", err)
	}
}
