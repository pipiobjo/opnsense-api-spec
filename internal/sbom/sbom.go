// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package sbom writes the software bill of materials of one published
// release: which OPNsense packages its API description was derived from
// (CycloneDX 1.6, JSON). The data is what the IR records about its inputs:
// name, version, source commit and the checksum from the signed package
// catalog, plus the catalog's own checksum and signing key. Licences are
// not in the IR and are left out rather than guessed.
//
// The output is deterministic (no timestamp; the serial number is derived
// from the content), so an unchanged release keeps an unchanged file and
// is not republished.
package sbom

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
)

// FileName is the asset name of the SBOM of a release.
func FileName(release string) string { return "opnsense-" + release + ".sbom.cdx.json" }

type bom struct {
	BOMFormat    string       `json:"bomFormat"`
	SpecVersion  string       `json:"specVersion"`
	SerialNumber string       `json:"serialNumber,omitempty"`
	Version      int          `json:"version"`
	Metadata     metadata     `json:"metadata"`
	Components   []component  `json:"components"`
	Dependencies []dependency `json:"dependencies"`
}

type metadata struct {
	Tools      tools      `json:"tools"`
	Component  component  `json:"component"`
	Properties []property `json:"properties"`
}

type tools struct {
	Components []component `json:"components"`
}

type component struct {
	Type        string     `json:"type"`
	BOMRef      string     `json:"bom-ref,omitempty"`
	Name        string     `json:"name"`
	Version     string     `json:"version,omitempty"`
	Description string     `json:"description,omitempty"`
	Publisher   string     `json:"publisher,omitempty"`
	PURL        string     `json:"purl,omitempty"`
	Hashes      []hash     `json:"hashes,omitempty"`
	Properties  []property `json:"properties,omitempty"`
}

type hash struct {
	Alg     string `json:"alg"`
	Content string `json:"content"`
}

type property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type dependency struct {
	Ref       string   `json:"ref"`
	DependsOn []string `json:"dependsOn"`
}

// ForRelease returns the SBOM of the release described by rel.
func ForRelease(rel *ir.Release) ([]byte, error) {
	if rel.Version == "" || len(rel.Packages) == 0 {
		return nil, fmt.Errorf("sbom: IR without version or packages")
	}
	main := component{
		Type:    "data",
		BOMRef:  "opnsense-api-description@" + rel.Version,
		Name:    "opnsense-api-description",
		Version: rel.Version,
		Description: "OpenAPI description and capability data of OPNsense " + rel.Version +
			", derived from the source of the packages listed here",
	}
	b := bom{
		BOMFormat:   "CycloneDX",
		SpecVersion: "1.6",
		Version:     1,
		Metadata: metadata{
			Tools:     tools{Components: []component{{Type: "application", Name: rel.Generator.Name, Version: rel.Generator.Version}}},
			Component: main,
		},
	}
	for _, p := range []property{
		{"opnsense:series", rel.Series},
		{"opnsense:core_version", rel.CoreVersion},
		{"opnsense:abi", rel.ABI},
		{"opnsense:catalog_sha256", rel.CatalogSHA256},
		{"opnsense:catalog_signed_by", rel.SignedBy},
	} {
		if p.Value != "" {
			b.Metadata.Properties = append(b.Metadata.Properties, p)
		}
	}
	pkgs := append([]ir.Package(nil), rel.Packages...)
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	dep := dependency{Ref: main.BOMRef}
	for _, p := range pkgs {
		alg, digest, err := pkgrepo.SumHash(p.Sum)
		if err != nil {
			return nil, fmt.Errorf("sbom: package %s: %w", p.Name, err)
		}
		ref := "pkg:" + p.Name + "@" + p.Version
		c := component{
			Type:      "application",
			BOMRef:    ref,
			Name:      p.Name,
			Version:   p.Version,
			Publisher: "OPNsense",
			PURL:      "pkg:generic/opnsense/" + url.PathEscape(p.Name) + "@" + url.PathEscape(p.Version),
			Hashes:    []hash{{Alg: alg, Content: digest}},
			Properties: []property{
				{"opnsense:pkg_sum", p.Sum},
			},
		}
		if p.Commit != "" {
			c.Properties = append(c.Properties, property{"opnsense:source_commit", p.Commit})
		}
		b.Components = append(b.Components, c)
		dep.DependsOn = append(dep.DependsOn, ref)
	}
	b.Dependencies = []dependency{dep}

	// serial number: a UUID (version 8, RFC 9562) from the content
	body, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(body)
	h[6] = h[6]&0x0f | 0x80
	h[8] = h[8]&0x3f | 0x80
	b.SerialNumber = fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
