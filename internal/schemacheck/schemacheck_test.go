package schemacheck

import (
	"encoding/json"
	"strings"
	"testing"
)

const doc = `{
 "openapi": "3.1.0", "info": {"title": "t", "version": "1"},
 "paths": {"/api/demo/settings/get": {"get": {"responses": {"200": {"description": "OK",
   "content": {"application/json": {"schema": {"type": "object", "required": ["demo"],
     "properties": {"demo": {"$ref": "#/components/schemas/Demo.Read"}}}}}}}}},
   "/api/demo/settings/getHost/{uuid}": {"get": {"responses": {"200": {"description": "OK",
     "content": {"application/json": {"schema": {"type": "object"}}}}}}}},
 "components": {"schemas": {
   "Demo.Read": {"type": "object", "properties": {
     "enabled": {"type": "string", "enum": ["0", "1"], "x-opnsense-type": "BooleanField"},
     "hosts": {"type": "object", "properties": {"host": {"oneOf": [
       {"type": "object", "additionalProperties": {"type": "object"}},
       {"type": "array", "maxItems": 0}]}}}}}}}}`

func load(t *testing.T) *Checker {
	t.Helper()
	var d any
	if err := json.Unmarshal([]byte(doc), &d); err != nil {
		t.Fatal(err)
	}
	c, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func body(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestValidate(t *testing.T) {
	c := load(t)
	for _, ok := range []string{
		`{"demo": {"enabled": "1", "hosts": {"host": {"uuid-1": {}}}}}`,
		`{"demo": {"enabled": "0", "hosts": {"host": []}}}`, // empty list as [] (PHP)
	} {
		if f := c.Validate("GET", "/api/demo/settings/get", body(t, ok)); len(f) != 0 {
			t.Errorf("%s: unexpected findings %v", ok, f)
		}
	}
	for bad, want := range map[string]string{
		`{"demo": {"enabled": "yes"}}`:              "/demo/enabled",
		`{"demo": {"hosts": {"host": [{"x": 1}]}}}`: "/demo/hosts/host",
		`{"other": {}}`:                             "demo",
	} {
		f := c.Validate("GET", "/api/demo/settings/get", body(t, bad))
		if len(f) == 0 || !strings.Contains(strings.Join(f, " "), want) {
			t.Errorf("%s: want a finding mentioning %q, got %v", bad, want, f)
		}
	}
	if !c.Has("GET", "/api/demo/settings/getHost/{uuid}") || c.Has("POST", "/api/demo/settings/get") {
		t.Error("Has() mismatch")
	}
	if f := c.Validate("GET", "/api/unknown", body(t, `{}`)); len(f) != 1 {
		t.Errorf("unknown operation must be reported: %v", f)
	}
}
