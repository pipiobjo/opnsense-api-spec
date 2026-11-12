// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package schemacheck validates JSON responses against the response schemas
// of a rendered OpenAPI 3.1 document (JSON Schema 2020-12), so that
// contract tests check what generated clients will decode, not only the
// model.
package schemacheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

var printer = message.NewPrinter(language.English)

const docURL = "urn:opnsense-api-spec:openapi.json"

// Checker validates responses of one OpenAPI document.
type Checker struct {
	compiler *jsonschema.Compiler
	paths    map[string]any
	mu       sync.Mutex
	cache    map[string]*jsonschema.Schema
}

// New prepares a checker for an OpenAPI document (as produced by the
// renderer, any JSON-compatible value).
func New(doc any) (*Checker, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	parsed, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	if err := c.AddResource(docURL, parsed); err != nil {
		return nil, err
	}
	m, _ := parsed.(map[string]any)
	paths, _ := m["paths"].(map[string]any)
	return &Checker{compiler: c, paths: paths, cache: map[string]*jsonschema.Schema{}}, nil
}

func pointerEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// Has reports whether the document describes a JSON 200 response for the
// operation.
func (c *Checker) Has(method, pathTemplate string) bool {
	item, _ := c.paths[pathTemplate].(map[string]any)
	op, _ := item[strings.ToLower(method)].(map[string]any)
	resp, _ := op["responses"].(map[string]any)
	ok, _ := resp["200"].(map[string]any)
	content, _ := ok["content"].(map[string]any)
	_, has := content["application/json"]
	return has
}

// Validate checks a decoded JSON response body against the 200 response
// schema of method + pathTemplate (e.g. "/api/unbound/settings/getDnsbl/{uuid}").
// It returns one finding per violation, empty when valid.
func (c *Checker) Validate(method, pathTemplate string, body any) []string {
	if !c.Has(method, pathTemplate) {
		return []string{"schema: operation " + method + " " + pathTemplate + " not in the document"}
	}
	loc := docURL + "#/paths/" + pointerEscape(pathTemplate) + "/" + strings.ToLower(method) +
		"/responses/200/content/application~1json/schema"
	c.mu.Lock()
	sch, ok := c.cache[loc]
	if !ok {
		var err error
		sch, err = c.compiler.Compile(loc)
		if err != nil {
			c.mu.Unlock()
			return []string{"schema: cannot compile response schema: " + err.Error()}
		}
		c.cache[loc] = sch
	}
	c.mu.Unlock()
	err := sch.Validate(body)
	if err == nil {
		return nil
	}
	ve, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return []string{"schema: " + err.Error()}
	}
	var out []string
	collect(ve, &out)
	sort.Strings(out)
	if len(out) > 20 {
		out = append(out[:20], fmt.Sprintf("schema: ... %d more", len(out)-20))
	}
	return out
}

// collect flattens the error tree to its leaves ("instance path: message").
func collect(ve *jsonschema.ValidationError, out *[]string) {
	if len(ve.Causes) == 0 {
		*out = append(*out, fmt.Sprintf("schema: /%s: %s", strings.Join(ve.InstanceLocation, "/"), ve.ErrorKind.LocalizedString(printer)))
		return
	}
	for _, c := range ve.Causes {
		collect(c, out)
	}
}
