// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pkgrepo

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
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
		_, err := VerifyCatalog(c.cat, c.keys, true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", c.name, err, c.want)
		}
	}
}
