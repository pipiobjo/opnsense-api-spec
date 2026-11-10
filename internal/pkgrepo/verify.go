package pkgrepo

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Key is an OPNsense repository signing key.
type Key struct {
	Name   string // file name in /usr/local/etc/pkg/fingerprints/OPNsense/
	Status string // "trusted" (current) or "revoked" (rotated, older series)
}

// Keys are the SHA-256 fingerprints (over the PEM public key file) of all
// OPNsense repository signing keys, copied from core 26.7.5
// (/usr/local/etc/pkg/fingerprints/OPNsense/{trusted,revoked}/). OPNsense
// rotates the key per release series; the "revoked" ones signed older
// series. Regenerate from the newest core when a new key appears.
var Keys = map[string]Key{
	"598b5faf596474e7fe97280b0527edaefd978f16ba941799126030dc55c1f8b8": {Name: "pkg.opnsense.org.20150402", Status: "revoked"},
	"c805fcaf29443b08ae86c3ea95a14a19b06db85db91ee518788f01e5c003d54f": {Name: "pkg.opnsense.org.20160104", Status: "revoked"},
	"ef982a2a829f62ebca3f6beaced8296dbb4e0182b27b916806cf7a9b9003061d": {Name: "pkg.opnsense.org.20160630", Status: "revoked"},
	"1436d53cd170457e43ef66a36e11b8cf25593cd24ae4e7222d4091c710d12b3a": {Name: "pkg.opnsense.org.20161210", Status: "revoked"},
	"296005724e9b270b56c442ef753ccc8aa05253d8b45eaaf68b1aa7b5d24ef942": {Name: "pkg.opnsense.org.20170625", Status: "revoked"},
	"9a2c4fd9fa68b91c0dc5564a31b8b3d96f9aaf2ca07075f83433c96741bf681a": {Name: "pkg.opnsense.org.20171219", Status: "revoked"},
	"61bc2d1d457f4d072346dce5615d998a6780f65903b5ca0a615fd84cd093a4fa": {Name: "pkg.opnsense.org.20180614", Status: "revoked"},
	"8a00fc70efa3b4cb645eda29891244a18da27622fcc0e67662c418f0a650b184": {Name: "pkg.opnsense.org.20181218", Status: "revoked"},
	"d7ef31add16ef0d5d340e3383a020dd8c40946848a8cb1f5398dfb4203f2fd7a": {Name: "pkg.opnsense.org.20190702", Status: "revoked"},
	"555d1ebe9741c7e1489e41d57c9c08eef060ad5604fbbbb9d5955dd658522e26": {Name: "pkg.opnsense.org.20200119", Status: "revoked"},
	"77c5163e08b84927b1780245247439e9f9e5d8862d393d877abfb1576175f51f": {Name: "pkg.opnsense.org.20200313", Status: "revoked"},
	"1d07655993aebd8c5222aac22c74551f1208d2c55d22c6278dc7dfaeaf77aa8d": {Name: "pkg.opnsense.org.20210104", Status: "revoked"},
	"57006cebacef60f2fe48e078dbaf68743308aef3533516c3da0027f43bef2df9": {Name: "pkg.opnsense.org.20210629", Status: "revoked"},
	"056f8ab802466e90299616885478e3436c2eb19bc97f553ac6836b2611a7c65f": {Name: "pkg.opnsense.org.20210903", Status: "revoked"},
	"7d511a58b3d7974190d50895e04a1cf6fc30773b446e7a0fb599c1da44452c47": {Name: "pkg.opnsense.org.20220701", Status: "revoked"},
	"0bd31937980a923a3deaebcfef1962c306f325c6f750816702f81598bb0e62f6": {Name: "pkg.opnsense.org.20221213", Status: "revoked"},
	"cb771fe840893a47874299ec94552320fefb23baf8f98c737e4b5ad2f70c368e": {Name: "pkg.opnsense.org.20230717", Status: "revoked"},
	"fa43aaebad78989234f7623b3853aff95829b72308a102acc0657561cf350b13": {Name: "pkg.opnsense.org.20240105", Status: "revoked"},
	"64a33f49ca717d8e8f5924832b97125bff660235d8ef08f0457bf5c3a7b5114c": {Name: "pkg.opnsense.org.20240611", Status: "revoked"},
	"9d104dda4c0ec16316503d47438d7b5dd008871d842c61c259bb89c56af41e88": {Name: "pkg.opnsense.org.20241217", Status: "revoked"},
	"ab6411c236dd1d03bf23de635f68e4628753b05ae877bf1486d40e63b778f001": {Name: "pkg.opnsense.org.20250710", Status: "revoked"},
	"d8828c0658ffb5e900a31269d7091b402497d5706b9be179e01ed8c88a0d49d8": {Name: "pkg.opnsense.org.20260120", Status: "revoked"},
	"9f3b76ffef38fb6405595038b324b61043c9a2c375ae5c151d628508e9a845ce": {Name: "pkg.opnsense.org.20260608", Status: "trusted"},
}

// Fingerprint returns the pkg-style fingerprint of a PEM public key file.
func Fingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])
}

// VerifyCatalog checks the catalog signature the way FreeBSD pkg does for
// signature_type "fingerprints": the public key must be a known OPNsense
// key, and the signature is RSA PKCS#1 v1.5 (SHA-256) over the hex SHA-256
// digest of packagesite.yaml. Rotated keys are accepted only if
// allowRotated is set; the returned Key tells which one signed.
func VerifyCatalog(cat *Catalog, keys map[string]Key, allowRotated bool) (Key, error) {
	if len(cat.Signature) == 0 || len(cat.PublicKey) == 0 {
		return Key{}, errors.New("catalog is not signed (no .sig/.pub)")
	}
	fp := Fingerprint(cat.PublicKey)
	key, ok := keys[fp]
	if !ok {
		return Key{}, fmt.Errorf("catalog signed by unknown key %s", fp)
	}
	if key.Status != "trusted" && !allowRotated {
		return Key{}, fmt.Errorf("catalog signed by rotated key %s (%s)", key.Name, fp)
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
