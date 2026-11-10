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
	"encoding/pem"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"golang.org/x/crypto/blake2b"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"26.7", "26.7.1", -1}, {"26.7.10", "26.7.9", 1}, {"26.1.11", "26.7", -1}, {"26.7.5", "26.7.5", 0}} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%s, %s) = %d", c.a, c.b, got)
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
	if _, err := VerifyCatalog(cat, map[string]Key{}, true); err == nil {
		t.Error("unknown key must fail")
	}
	trusted := map[string]Key{fp: {Name: "test-key", Status: "trusted"}}
	k, err := VerifyCatalog(cat, trusted, false)
	if err != nil || k.Name != "test-key" {
		t.Errorf("trusted: %v %+v", err, k)
	}
	rotated := map[string]Key{fp: {Name: "old-key", Status: "revoked"}}
	if _, err := VerifyCatalog(cat, rotated, false); err == nil {
		t.Error("rotated key must fail unless allowed")
	}
	if k, err := VerifyCatalog(cat, rotated, true); err != nil || k.Status != "revoked" {
		t.Errorf("rotated allowed: %v %+v", err, k)
	}
	cat.YAML = []byte(`{"name":"evil"}`)
	if _, err := VerifyCatalog(cat, trusted, false); err == nil {
		t.Error("modified catalog must fail")
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
	keys := map[string]Key{a: {Name: "k.a", Status: "revoked", Source: "bootstrap"}}
	changed := LearnKeys(keys, map[string][]byte{
		"x/fingerprints/OPNsense/trusted/k.a": file(a), // older core: outdated view
		"x/fingerprints/OPNsense/trusted/k.b": file(b), // new key
		"x/fingerprints/OPNsense/README":      []byte("ignored"),
	}, "core 1")
	if keys[a].Status != "revoked" {
		t.Error("a revoked key must never become trusted again")
	}
	if keys[b].Status != "trusted" || keys[b].Source != "core 1" || len(changed) != 1 {
		t.Errorf("new key: %+v changed=%v", keys[b], changed)
	}
	changed = LearnKeys(keys, map[string][]byte{"x/fingerprints/OPNsense/revoked/k.b": file(b)}, "core 2")
	if keys[b].Status != "revoked" || len(changed) != 1 || !strings.Contains(keys[b].Source, "core 2") {
		t.Errorf("revocation: %+v", keys[b])
	}
}
