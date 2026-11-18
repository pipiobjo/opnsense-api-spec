// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pkgrepo

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Key is an OPNsense repository signing key.
type Key struct {
	Name   string `json:"name"`   // file name in /usr/local/etc/pkg/fingerprints/OPNsense/
	Status string `json:"status"` // "trusted" (current) or "revoked" (rotated, older series)
	// Source says how the key became known: the bootstrap list, or the
	// release and catalog whose (verified) core package introduced it.
	Source string `json:"source,omitempty"`
	// RevokedIn is the series whose core (the newest release of that
	// series, as walked by the key chain) first lists the key as revoked.
	// A revoked key is valid only for catalogs of older series (ADR 0005):
	// OPNsense signs each series with its own key and revokes it in the
	// next series. For keys that were already revoked in the oldest core
	// walked it is an upper bound.
	RevokedIn string `json:"revoked_in,omitempty"`
}

//go:embed keys.json
var embeddedKeys []byte

// Keys is the built-in key list (keys.json, bootstrapped from core 26.7.5
// and extended through the signed chain, see LearnKeys).
var Keys = mustKeys(embeddedKeys)

type keyFile struct {
	Keys map[string]Key `json:"keys"`
}

func mustKeys(b []byte) map[string]Key {
	var kf keyFile
	if err := json.Unmarshal(b, &kf); err != nil {
		panic("keys.json: " + err.Error())
	}
	return kf.Keys
}

// LoadKeys reads a key list file.
func LoadKeys(path string) (map[string]Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var kf keyFile
	if err := json.Unmarshal(b, &kf); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return kf.Keys, nil
}

// SaveKeys writes a key list file (sorted, stable).
func SaveKeys(path string, keys map[string]Key) error {
	b, err := json.MarshalIndent(keyFile{Keys: keys}, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

var fingerprintRe = regexp.MustCompile(`fingerprint:\s*"([0-9a-f]{64})"`)

// LearnKeys extends keys with the fingerprints a verified core package
// ships (files below .../fingerprints/OPNsense/{trusted,revoked}/). This is
// the signed key chain: the last patch release of a series already lists
// the key of the next series. Unknown keys are added; a known key may only
// move from trusted to revoked, never back, because older cores still list
// keys as trusted that newer ones revoked. A revocation records series (the
// core's series) as RevokedIn; the earliest series wins. It returns the
// fingerprints that were added or changed.
func LearnKeys(keys map[string]Key, files map[string][]byte, source, series string) []string {
	var changed []string
	for path, data := range files {
		dir := filepath.Base(filepath.Dir(path))
		if dir != "trusted" && dir != "revoked" {
			continue
		}
		m := fingerprintRe.FindSubmatch(data)
		if m == nil {
			continue
		}
		fp := string(m[1])
		old, known := keys[fp]
		switch {
		case !known:
			k := Key{Name: filepath.Base(path), Status: dir, Source: source}
			if dir == "revoked" {
				k.RevokedIn = series
			}
			keys[fp] = k
		case dir == "trusted":
			continue // unchanged, or an older core's outdated view
		case old.Status == "trusted":
			old.Source += "; status trusted -> revoked per " + source
			old.Status, old.RevokedIn = dir, series
			keys[fp] = old
		case old.RevokedIn == "" || CompareVersions(series, old.RevokedIn) < 0:
			old.Source += "; revoked in " + series + " per " + source
			old.RevokedIn = series
			keys[fp] = old
		default:
			continue // already known as revoked in this or an earlier series
		}
		changed = append(changed, fp)
	}
	sort.Strings(changed)
	return changed
}

// Fingerprint returns the pkg-style fingerprint of a PEM public key file.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// VerifyCatalog checks the catalog signature the way FreeBSD pkg does for
// signature_type "fingerprints": the public key must be a known OPNsense
// key, and the signature is RSA PKCS#1 v1.5 (SHA-256) over the hex SHA-256
// digest of packagesite.yaml. A revoked key is accepted only for a catalog
// of a series older than the one that revoked it (cat.Release.Series before
// Key.RevokedIn), so a retired key cannot sign a new release; a revoked key
// without RevokedIn is refused. The returned Key tells which one signed.
func VerifyCatalog(cat *Catalog, keys map[string]Key) (Key, error) {
	if len(cat.Signature) == 0 || len(cat.PublicKey) == 0 {
		return Key{}, errors.New("catalog is not signed (no .sig/.pub)")
	}
	fp := Fingerprint(cat.PublicKey)
	key, ok := keys[fp]
	if !ok {
		return Key{}, fmt.Errorf("catalog signed by unknown key %s", fp)
	}
	if key.Status != "trusted" {
		ser := cat.Release.Series
		if key.RevokedIn == "" || ser == "" || CompareVersions(ser, key.RevokedIn) >= 0 {
			return Key{}, fmt.Errorf("catalog of series %q signed by key %s (%s), revoked in series %q", ser, key.Name, fp, key.RevokedIn)
		}
	}
	blk, _ := pem.Decode(cat.PublicKey)
	if blk == nil {
		return Key{}, errors.New("catalog public key is not PEM")
	}
	pk, err := x509.ParsePKIXPublicKey(blk.Bytes)
	if err != nil {
		return Key{}, fmt.Errorf("parse public key: %w", err)
	}
	pub, ok := pk.(*rsa.PublicKey)
	if !ok {
		return Key{}, errors.New("catalog public key is not RSA")
	}
	d := sha256.Sum256(cat.YAML)
	msg := sha256.Sum256([]byte(hex.EncodeToString(d[:])))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, msg[:], cat.Signature); err != nil {
		return Key{}, fmt.Errorf("catalog signature invalid: %w", err)
	}
	return key, nil
}

// pkg's base32 alphabet (libpkg/pkg_checksum.c).
const pkgBase32 = "ybndrfg8ejkmcpqxot1uwisza345h769"

func encodePkgBase32(in []byte) string {
	var sb strings.Builder
	var acc uint32
	bits := 0
	for _, b := range in {
		acc |= uint32(b) << bits
		bits += 8
		for bits >= 5 {
			sb.WriteByte(pkgBase32[acc&31])
			acc >>= 5
			bits -= 5
		}
	}
	if bits > 0 {
		sb.WriteByte(pkgBase32[acc&31])
	}
	return sb.String()
}

// decodePkgBase32 is the inverse of encodePkgBase32 for n output bytes.
func decodePkgBase32(s string, n int) ([]byte, error) {
	out := make([]byte, 0, n)
	var acc uint32
	bits := 0
	for i := 0; i < len(s); i++ {
		v := strings.IndexByte(pkgBase32, s[i])
		if v < 0 {
			return nil, fmt.Errorf("invalid character %q in pkg checksum", s[i])
		}
		acc |= uint32(v) << bits
		bits += 5
		if bits >= 8 && len(out) < n {
			out = append(out, byte(acc))
			acc >>= 8
			bits -= 8
		}
	}
	if len(out) != n || encodePkgBase32(out) != s {
		return nil, fmt.Errorf("pkg checksum %q is not %d bytes", s, n)
	}
	return out, nil
}

// SumHash converts a catalog checksum into a standard algorithm name
// (CycloneDX spelling) and hex digest: "2$<base32>" is BLAKE2b-512, 64 hex
// characters are SHA-256.
func SumHash(sum string) (alg, hexDigest string, err error) {
	switch {
	case strings.HasPrefix(sum, "2$"):
		b, err := decodePkgBase32(sum[2:], blake2b.Size)
		if err != nil {
			return "", "", err
		}
		return "BLAKE2b-512", hex.EncodeToString(b), nil
	case len(sum) == 64:
		if _, err := hex.DecodeString(sum); err != nil {
			return "", "", fmt.Errorf("checksum %q: %w", sum, err)
		}
		return "SHA-256", strings.ToLower(sum), nil
	}
	return "", "", fmt.Errorf("unsupported checksum format %q", sum)
}

// VerifyPackage checks a package file against its catalog checksum.
// Supported: "2$<base32>" (BLAKE2b-512, pkg checksum type 2) and plain
// 64-character hex SHA-256 (older catalogs).
func VerifyPackage(data []byte, sum string) error {
	switch {
	case strings.HasPrefix(sum, "2$"):
		h := blake2b.Sum512(data)
		if got := "2$" + encodePkgBase32(h[:]); got != sum {
			return fmt.Errorf("checksum mismatch: got %s, want %s", got, sum)
		}
		return nil
	case len(sum) == 64:
		h := sha256.Sum256(data)
		if got := hex.EncodeToString(h[:]); got != sum {
			return fmt.Errorf("checksum mismatch: got %s, want %s", got, sum)
		}
		return nil
	default:
		return fmt.Errorf("unsupported checksum format %q", sum)
	}
}
