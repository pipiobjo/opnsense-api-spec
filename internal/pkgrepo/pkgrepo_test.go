// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pkgrepo

import (
	"archive/tar"
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"golang.org/x/crypto/blake2b"
)

// Claim: CompareVersions follows the version order of the capability
// data. The table is the file published with the data
// (internal/capability/version-order.json), which the Go client tests
// against as well, so both implementations are checked on the same cases.
func TestCompareVersions(t *testing.T) {
	b, err := os.ReadFile("../capability/version-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Ordered []string    `json:"ordered"`
		Equal   [][2]string `json:"equal"`
	}
	if err := json.Unmarshal(b, &table); err != nil || len(table.Ordered) < 2 || len(table.Equal) == 0 {
		t.Fatalf("version-order.json: %v %+v", err, table)
	}
	ordered := table.Ordered
	for i := 0; i < len(ordered); i++ {
		for j := 0; j < len(ordered); j++ {
			got := CompareVersions(ordered[i], ordered[j])
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got != want {
				t.Errorf("CompareVersions(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	for _, eq := range table.Equal {
		if CompareVersions(eq[0], eq[1]) != 0 || CompareVersions(eq[1], eq[0]) != 0 {
			t.Errorf("CompareVersions(%s, %s) must be 0", eq[0], eq[1])
		}
	}
}

func TestVerifyPackage(t *testing.T) {
	data := []byte("package bytes")
	h := blake2b.Sum512(data)
	sum := "2$" + encodePkgBase32(h[:])
	if len(sum) != 2+103 {
		t.Errorf("unexpected length %d", len(sum))
	}
	if err := VerifyPackage(data, sum); err != nil {
		t.Error(err)
	}
	if err := VerifyPackage([]byte("tampered"), sum); err == nil {
		t.Error("tampered data must fail")
	}
	s := sha256.Sum256(data)
	if err := VerifyPackage(data, hex.EncodeToString(s[:])); err != nil {
		t.Error("hex sha256:", err)
	}
	// tampered data against a SHA-256 checksum (older catalogs) must fail
	// just as with BLAKE2b
	if err := VerifyPackage([]byte("tampered"), hex.EncodeToString(s[:])); err == nil {
		t.Error("tampered data must fail against a sha256 checksum")
	}
	if err := VerifyPackage(data, "1$abc"); err == nil {
		t.Error("unknown format must fail")
	}
}

// The encoder reproduced the real "sum" of os-acme-client-4.17.pkg in the
// 26.7.5 catalog. This vector is derived by hand: 0x00 0xff is read LSB
// first in 5-bit groups -> 0, 24, 31, 1 -> "y", "a", "9", "b".
func TestEncodePkgBase32(t *testing.T) {
	if got := encodePkgBase32([]byte{0x00, 0xff}); got != "ya9b" {
		t.Errorf("got %q", got)
	}
}

func signCatalog(t *testing.T, yaml []byte) (*Catalog, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKIXPublicKey(&key.PublicKey)
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	d := sha256.Sum256(yaml)
	msg := sha256.Sum256([]byte(hex.EncodeToString(d[:])))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, msg[:])
	if err != nil {
		t.Fatal(err)
	}
	return &Catalog{YAML: yaml, Signature: sig, PublicKey: pub}, Fingerprint(pub)
}

func TestVerifyCatalog(t *testing.T) {
	cat, fp := signCatalog(t, []byte(`{"name":"opnsense"}`))
	cat.Release.Series = "26.7"
	if _, err := VerifyCatalog(cat, map[string]Key{}); err == nil {
		t.Error("unknown key must fail")
	}
	trusted := map[string]Key{fp: {Name: "test-key", Status: "trusted"}}
	k, err := VerifyCatalog(cat, trusted)
	if err != nil || k.Name != "test-key" {
		t.Errorf("trusted: %v %+v", err, k)
	}
	cat.YAML = []byte(`{"name":"evil"}`)
	if _, err := VerifyCatalog(cat, trusted); err == nil {
		t.Error("modified catalog must fail")
	}
}

// Claim (SEC-4, ADR 0005): a revoked key signs only catalogs of series
// older than the one whose core revoked it. Real case: 20250710 signs
// every 25.7 release and is revoked by core 26.1.11; a new 26.1, 26.7 or
// 27.1 release signed with it is refused, as is any revoked key whose
// revocation series is unknown or a catalog whose series is unknown.
func TestVerifyCatalogRevokedKeyOnlyForOlderSeries(t *testing.T) {
	cat, fp := signCatalog(t, []byte(`{"name":"opnsense"}`))
	revoked := map[string]Key{fp: {Name: "pkg.opnsense.org.20250710", Status: "revoked", RevokedIn: "26.1"}}
	for _, c := range []struct {
		series string
		ok     bool
	}{{"24.7", true}, {"25.7", true}, {"26.1", false}, {"26.7", false}, {"27.1", false}, {"", false}} {
		cat.Release.Series = c.series
		_, err := VerifyCatalog(cat, revoked)
		if (err == nil) != c.ok {
			t.Errorf("series %q: err=%v, want accepted=%v", c.series, err, c.ok)
		}
	}
	cat.Release.Series = "25.7"
	if _, err := VerifyCatalog(cat, map[string]Key{fp: {Name: "k", Status: "revoked"}}); err == nil {
		t.Error("a revoked key without revocation series must fail")
	}
}

// Every release that is published today was signed by a key valid for its
// series under keys.json (measured 2026-11-18 on the mirror: each series is
// signed by its own key, 24.7 20240611, 25.1 20241217, 25.7 20250710,
// 26.1 20260120, 26.7 20260608), and the next series is refused for each.
func TestEmbeddedKeysCoverPublishedSeries(t *testing.T) {
	signer := map[string]string{"24.7": "20240611", "25.1": "20241217", "25.7": "20250710",
		"26.1": "20260120", "26.7": "20260608"}
	next := map[string]string{"24.7": "25.1", "25.1": "25.7", "25.7": "26.1", "26.1": "26.7", "26.7": ""}
	for ser, date := range signer {
		var key Key
		for _, k := range Keys {
			if k.Name == "pkg.opnsense.org."+date {
				key = k
			}
		}
		if key.Name == "" {
			t.Fatalf("%s: key %s missing", ser, date)
		}
		valid := func(s string) bool {
			return key.Status == "trusted" || (key.RevokedIn != "" && CompareVersions(s, key.RevokedIn) < 0)
		}
		if !valid(ser) {
			t.Errorf("%s: %s (%+v) would refuse its own series", ser, key.Name, key)
		}
		if n := next[ser]; n != "" && valid(n) {
			t.Errorf("%s: %s (%+v) would still sign series %s", ser, key.Name, key, n)
		}
	}
	for fp, k := range Keys {
		if k.Status == "revoked" && k.RevokedIn == "" {
			t.Errorf("%s (%s): revoked without revoked_in", k.Name, fp[:12])
		}
	}
}

func TestReadArchiveZstd(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	for name, body := range map[string]string{
		"/usr/local/opnsense/mvc/app/models/A.xml": "<model/>",
		"/+MANIFEST": "{}",
	} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	var z bytes.Buffer
	enc, _ := zstd.NewWriter(&z)
	_, _ = enc.Write(tarBuf.Bytes())
	_ = enc.Close()
	files, err := ReadArchive(&z, func(n string) bool { return n != "+MANIFEST" })
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || string(files["usr/local/opnsense/mvc/app/models/A.xml"]) != "<model/>" {
		t.Errorf("files = %v", files)
	}
}

func TestReadArchiveXZ(t *testing.T) {
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{Name: "packagesite.yaml", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("{}"))
	_ = tw.Close()
	var x bytes.Buffer
	w, _ := xz.NewWriter(&x)
	_, _ = w.Write(tarBuf.Bytes())
	_ = w.Close()
	files, err := ReadArchive(&x, nil)
	if err != nil || string(files["packagesite.yaml"]) != "{}" {
		t.Fatalf("%v %v", err, files)
	}
}

func TestKeysHaveOneTrusted(t *testing.T) {
	n := 0
	for _, k := range Keys {
		if k.Status == "trusted" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("expected exactly one trusted key, got %d", n)
	}
}

func TestParseCatalog(t *testing.T) {
	pkgs, err := ParseCatalog([]byte(`{"name":"opnsense","version":"26.7.5","annotations":{"CORE_COMMIT":"26.7.5 0 791286d4d"}}
{"name":"os-acme-client","version":"4.17","repopath":"All/os-acme-client-4.17.pkg"}
`))
	if err != nil || len(pkgs) != 2 || pkgs[0].Annotations["CORE_COMMIT"] == "" {
		t.Fatalf("%v %+v", err, pkgs)
	}
	cat := &Catalog{Packages: pkgs}
	if p := cat.Plugins(); len(p) != 1 || p[0].Name != "os-acme-client" {
		t.Errorf("plugins: %+v", p)
	}
}

func TestLearnKeysOneWay(t *testing.T) {
	file := func(fp string) []byte { return []byte("function: \"sha256\"\nfingerprint: \"" + fp + "\"\n") }
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	keys := map[string]Key{a: {Name: "k.a", Status: "revoked", Source: "bootstrap", RevokedIn: "26.1"}}
	changed := LearnKeys(keys, map[string][]byte{
		"x/fingerprints/OPNsense/trusted/k.a": file(a), // older core: outdated view
		"x/fingerprints/OPNsense/trusted/k.b": file(b), // new key
		"x/fingerprints/OPNsense/README":      []byte("ignored"),
	}, "core 1", "26.7")
	if keys[a].Status != "revoked" || keys[a].RevokedIn != "26.1" {
		t.Error("a revoked key must never become trusted again")
	}
	if keys[b].Status != "trusted" || keys[b].Source != "core 1" || keys[b].RevokedIn != "" || len(changed) != 1 {
		t.Errorf("new key: %+v changed=%v", keys[b], changed)
	}
	changed = LearnKeys(keys, map[string][]byte{"x/fingerprints/OPNsense/revoked/k.b": file(b)}, "core 2", "27.1")
	if keys[b].Status != "revoked" || keys[b].RevokedIn != "27.1" || len(changed) != 1 || !strings.Contains(keys[b].Source, "core 2") {
		t.Errorf("revocation: %+v", keys[b])
	}
	// a later series that also lists it revoked does not move the series
	if changed = LearnKeys(keys, map[string][]byte{"x/fingerprints/OPNsense/revoked/k.b": file(b)}, "core 3", "27.7"); len(changed) != 0 || keys[b].RevokedIn != "27.1" {
		t.Errorf("later revocation: %+v %v", keys[b], changed)
	}
	// an earlier series that lists it revoked moves it down (earliest wins)
	if changed = LearnKeys(keys, map[string][]byte{"x/fingerprints/OPNsense/revoked/k.a": file(a)}, "core 0", "25.7"); len(changed) != 1 || keys[a].RevokedIn != "25.7" {
		t.Errorf("earlier revocation: %+v %v", keys[a], changed)
	}
}
