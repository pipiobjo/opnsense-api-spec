// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package schemacheck

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed oas-3.1-schema-2022-10-07.json
var oasSchema []byte

const oasURL = "https://spec.openapis.org/oas/3.1/schema/2022-10-07"

// ValidateDocument checks a self-contained rendered document: its structure
// against the official OpenAPI 3.1 schema, and every component schema by
// compiling it as JSON Schema 2020-12. It returns findings, empty when valid.
func ValidateDocument(doc any) []string { return ValidateDocumentAt(doc, docURL, nil) }

// ValidateDocumentAt validates a document that lives at url and may refer
// to other documents (externals: url -> document), e.g. a fragment and its
// common document. Relative references resolve against url.
func ValidateDocumentAt(doc any, url string, externals map[string]any) []string {
	raw, err := json.Marshal(doc)
	if err != nil {
		return []string{err.Error()}
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return []string{err.Error()}
	}
	var out []string

	meta, err := jsonschema.UnmarshalJSON(bytes.NewReader(oasSchema))
	if err != nil {
		return []string{"embedded OpenAPI schema: " + err.Error()}
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(oasURL, meta); err != nil {
		return []string{err.Error()}
	}
	sch, err := c.Compile(oasURL)
	if err != nil {
		return []string{"compile OpenAPI schema: " + err.Error()}
	}
	if err := sch.Validate(inst); err != nil {
		if ve, ok := err.(*jsonschema.ValidationError); ok {
			collect(ve, &out)
		} else {
			out = append(out, err.Error())
		}
	}

	// every component schema must be valid JSON Schema 2020-12
	dc := jsonschema.NewCompiler()
	dc.DefaultDraft(jsonschema.Draft2020)
	if err := dc.AddResource(url, inst); err != nil {
		return append(out, err.Error())
	}
	for u, ext := range externals {
		b, err := json.Marshal(ext)
		if err != nil {
			return append(out, err.Error())
		}
		v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return append(out, err.Error())
		}
		if err := dc.AddResource(u, v); err != nil {
			return append(out, err.Error())
		}
	}
	m, _ := inst.(map[string]any)
	comps, _ := m["components"].(map[string]any)
	schemas, _ := comps["schemas"].(map[string]any)
	names := make([]string, 0, len(schemas))
	for n := range schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if _, err := dc.Compile(url + "#/components/schemas/" + pointerEscape(n)); err != nil {
			out = append(out, fmt.Sprintf("schema %s: %v", n, err))
		}
	}
	if len(out) > 50 {
		out = append(out[:50], fmt.Sprintf("... %d more", len(out)-50))
	}
	return out
}
