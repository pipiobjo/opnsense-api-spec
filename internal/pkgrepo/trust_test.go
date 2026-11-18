// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pkgrepo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// Claim (concepts, section 3): a catalog is used only when it is signed by
// a known key with a valid signature. Every way the key material can be
// wrong is refused, even when its fingerprint is trusted, so a mistake in
// keys.json cannot turn into accepting an unverifiable catalog.
func TestVerifyCatalogRefusesBadKeyMaterial(t *testing.T) {
	good, _ := signCatalog(t, []byte(`{"name":"opnsense"}`))
	trust := func(pub []byte) map[string]Key {
		return map[string]Key{Fingerprint(pub): {Name: "k", Status: "trusted"}}
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, _ := x509.MarshalPKIXPublicKey(&ecKey.PublicKey)
	ecPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: ecDER})
	notDER := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("not a key")})

	cases := []struct {
		name string
		cat  *Catalog
		keys map[string]Key
		want string
	}{
		{"no signature", &Catalog{YAML: good.YAML, PublicKey: good.PublicKey}, trust(good.PublicKey), "not signed"},
		{"no public key", &Catalog{YAML: good.YAML, Signature: good.Signature}, trust(nil), "not signed"},
		{"key is not PEM", &Catalog{YAML: good.YAML, Signature: good.Signature, PublicKey: []byte("garbage")}, trust([]byte("garbage")), "not PEM"},
		{"PEM without a key", &Catalog{YAML: good.YAML, Signature: good.Signature, PublicKey: notDER}, trust(notDER), "parse public key"},
		{"key is not RSA", &Catalog{YAML: good.YAML, Signature: good.Signature, PublicKey: ecPEM}, trust(ecPEM), "not RSA"},
		{"signature of another catalog", &Catalog{YAML: []byte(`{"name":"other"}`), Signature: good.Signature, PublicKey: good.PublicKey}, trust(good.PublicKey), "signature invalid"},
	}
	for _, c := range cases {
		_, err := VerifyCatalog(c.cat, c.keys)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", c.name, err, c.want)
		}
	}
}

// SumHash turns pkg's checksum into a standard hex digest; checked against
// the real BLAKE2b-512 of data, so a wrong decoder cannot pass.
func TestSumHash(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("x"), []byte(strings.Repeat("opnsense package ", 100))} {
		h := blake2b.Sum512(data)
		sum := "2$" + encodePkgBase32(h[:])
		alg, got, err := SumHash(sum)
		if err != nil || alg != "BLAKE2b-512" || got != hex.EncodeToString(h[:]) {
			t.Errorf("SumHash(%s) = %s %s %v", sum, alg, got, err)
		}
	}
	s := sha256.Sum256([]byte("x"))
	if alg, got, err := SumHash(hex.EncodeToString(s[:])); err != nil || alg != "SHA-256" || got != hex.EncodeToString(s[:]) {
		t.Errorf("sha256: %s %s %v", alg, got, err)
	}
	for _, bad := range []string{"2$", "2$ab!", "2$" + strings.Repeat("y", 102), "3$abc", strings.Repeat("z", 64)} {
		if _, _, err := SumHash(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
