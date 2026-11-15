// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package capability

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

//go:embed capability.schema.json
var schemaJSON []byte

// Schema returns the compiled JSON Schema of the format.
func Schema() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	const url = "capability.schema.json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

// Validate checks the catalog and every document against the schema and
// the rules the schema cannot express.
func Validate(cat *Catalog, docs map[string]*Doc) error {
	s, err := Schema()
	if err != nil {
		return fmt.Errorf("capability schema: %w", err)
	}
	check := func(name string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			return err
		}
		if err := s.Validate(inst); err != nil {
			return fmt.Errorf("%s does not match the capability schema: %w", name, err)
		}
		return nil
	}
	if err := check("catalog.json", cat); err != nil {
		return err
	}
	names := make([]string, 0, len(docs))
	for k := range docs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, pkg := range names {
		d := docs[pkg]
		if err := check(pkg+".json", d); err != nil {
			return err
		}
		if err := consistent(d); err != nil {
			return fmt.Errorf("%s.json: %w", pkg, err)
		}
	}
	return nil
}

// consistent: every fragment a document refers to is in its fragment
// table, and every fragment of a resource is covered by exactly one api
// variant.
func consistent(d *Doc) error {
	known := func(h string) error {
		if d.Fragments[h] == nil {
			return fmt.Errorf("unknown fragment %s", h)
		}
		return nil
	}
	for _, sel := range d.Select {
		for _, h := range sel {
			if err := known(h); err != nil {
				return err
			}
		}
	}
	for id, r := range d.Resources {
		cover := map[string]int{}
		for _, a := range r.API {
			if a.In == All {
				for _, h := range r.In {
					cover[h]++
				}
				continue
			}
			for _, h := range a.In.([]string) {
				cover[h]++
			}
		}
		for _, h := range r.In {
			if err := known(h); err != nil {
				return fmt.Errorf("%s: %w", id, err)
			}
			if cover[h] != 1 {
				return fmt.Errorf("%s: fragment %s is covered by %d api variants", id, h, cover[h])
			}
		}
		for h := range r.Verify {
			if cover[h] != 1 {
				return fmt.Errorf("%s: verification for fragment %s the resource does not exist in", id, h)
			}
		}
	}
	return nil
}
