// Package verify runs contract tests of the IR against a running OPNsense
// instance. It must only ever be pointed at a throwaway test instance: the
// write cycle adds and deletes entries.
package verify

import (
	"bytes"

	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// Result of one check against one endpoint.
type Result struct {
	Path     string   `json:"path"`
	Method   string   `json:"method"`
	Check    string   `json:"check"`  // reachable, shape, write-cycle
	Status   string   `json:"status"` // ok, mismatch, error, skipped
	HTTP     int      `json:"http,omitempty"`
	Findings []string `json:"findings,omitempty"`
	// RuntimeRequired: fields the instance rejected as missing when adding
	// an entry with server defaults (write-cycle only), without wrapper
	RuntimeRequired []string `json:"runtime_required,omitempty"`
}

// Report is the outcome of a run.
type Report struct {
	Release string `json:"release"`
	// Composition is the id of the catalog state the IR was built from
	// (set by the caller), so a later catalog change is visible as
	// "not verified".
	Composition string    `json:"composition,omitempty"`
	Instance    string    `json:"instance_version"`
	Generator   string    `json:"generator"`
	Date        string    `json:"date"`
	Results     []*Result `json:"results"`
}

// Client talks to the test instance with an API key.
type Client struct {
	BaseURL      string
	Key, Secret  string
	HTTP         *http.Client
	AllowedHosts []string // safety net: refuse anything else

	// Schema, when set, validates every decoded response against the
	// rendered OpenAPI document (what generated clients will decode).
	Schema        Validator
	schemaResults []*Result
}

// Validator checks a response body against the document's 200 response
// schema of an operation (path template, e.g. ".../getDnsbl/{uuid}").
type Validator interface {
	Has(method, pathTemplate string) bool
	Validate(method, pathTemplate string, body any) []string
}

// schema records a "schema" check for a decoded response. A mismatch on an
// endpoint the generator already marked (overridden action, custom logic)
// becomes known-gap, as for the shape checks.
func (c *Client) schema(e *ir.Endpoint, method, pathTemplate string, body any) {
	if c.Schema == nil || !c.Schema.Has(method, pathTemplate) {
		return
	}
	r := &Result{Path: pathTemplate, Method: method, Check: "schema", HTTP: 200}
	r.Findings = c.Schema.Validate(method, pathTemplate, body)
	r.Status = status(r.Findings)
	c.schemaResults = append(c.schemaResults, knownGap(r, e))
}

// NewClient creates a client for a local test instance. The instance uses
// a self-signed certificate; certificate checks are skipped only for hosts
// in allowed (normally 127.0.0.1 port forwards to the throwaway VM).
func NewClient(base, key, secret string, allowed []string) (*Client, error) {
	ok := false
	for _, h := range allowed {
		if strings.HasPrefix(base, "https://"+h+":") || base == "https://"+h {
			ok = true
		}
	}
	if !ok {
		return nil, fmt.Errorf("refusing %s: only %v are allowed as test instances", base, allowed)
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // #nosec G402 -- throwaway local VM only
		// HTTP/1.1 only: over HTTP/2 large responses from the OPNsense web
		// server arrived truncated at 64 KiB (firmware/info, 26.7)
		TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &Client{BaseURL: strings.TrimRight(base, "/"), Key: key, Secret: secret, AllowedHosts: allowed,
		HTTP: &http.Client{Timeout: 60 * time.Second, Transport: tr}}, nil
}

func (c *Client) do(method, path string, body any) (int, any, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, rd)
	if err != nil {
		return 0, nil, nil, err
	}
	req.SetBasicAuth(c.Key, c.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		if strings.Contains(err.Error(), "chunked") {
			err = fmt.Errorf("%w (OPNsense web server: broken chunked encoding on large responses over HTTP/1.1)", err)
		}
		return resp.StatusCode, nil, raw, fmt.Errorf("%s %s: reading response: %w", method, path, err)
	}
	var v any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &v); err != nil && resp.StatusCode == 200 {
			// downloads (CSV, PAC, templates) are legitimate non-JSON answers
			return resp.StatusCode, nil, raw, &NonJSON{ContentType: resp.Header.Get("Content-Type")}
		}
	}
	return resp.StatusCode, v, raw, nil
}

// NonJSON is returned for a 200 response that is not JSON.
type NonJSON struct{ ContentType string }

func (e *NonJSON) Error() string { return "response is not JSON (Content-Type " + e.ContentType + ")" }

// InstalledPackages returns the names of the packages installed on the
// instance (core and plugins), from /api/core/firmware/info.
// The firmware reloads its package state after installations and briefly
// returns no lists, so this retries for up to two minutes.
func (c *Client) InstalledPackages() (map[string]bool, error) {
	var lastErr error
	for i := 0; i < 24; i++ {
		pk, err := c.installedOnce()
		if err == nil {
			return pk, nil
		}
		lastErr = err
		time.Sleep(5 * time.Second)
	}
	return nil, lastErr
}

func (c *Client) installedOnce() (map[string]bool, error) {
	code, v, raw, err := c.do("GET", "/api/core/firmware/info", nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, fmt.Errorf("firmware/info: HTTP %d %s", code, trunc(raw))
	}
	out := map[string]bool{}
	m, _ := v.(map[string]any)
	for _, list := range []string{"package", "plugin"} {
		items, _ := m[list].([]any)
		for _, it := range items {
			p, _ := it.(map[string]any)
			name, _ := p["name"].(string)
			if inst, _ := p["installed"].(string); inst == "1" && name != "" {
				out[name] = true
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("firmware/info lists no installed packages")
	}
	return out, nil
}

// InstanceVersion returns product_version of the instance.
func (c *Client) InstanceVersion() (string, error) {
	code, v, raw, err := c.do("GET", "/api/core/firmware/status", nil)
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("firmware/status: HTTP %d %s", code, trunc(raw))
	}
	m, _ := v.(map[string]any)
	if pv, ok := m["product_version"].(string); ok {
		return pv, nil
	}
	if p, ok := m["product"].(map[string]any); ok {
		if pv, ok := p["product_version"].(string); ok {
			return pv, nil
		}
	}
	return "", fmt.Errorf("no product_version in firmware/status")
}

func trunc(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// Run executes all checks for endpoints of the given packages (installed
// on the instance). write enables the add/get/del cycle.
func Run(c *Client, rel *ir.Release, packages map[string]bool, write bool) *Report {
	rep := &Report{Release: rel.Version, Generator: rel.Generator.Version, Date: time.Now().UTC().Format(time.RFC3339)}
	rep.Instance, _ = c.InstanceVersion()
	models := map[string]*model.Model{}
	for _, m := range rel.Models {
		models[m.Class] = m
	}
	for _, e := range rel.Endpoints {
		if !packages[e.Package] {
			continue
		}
		switch {
		case e.Kind == ir.KindModelGet:
			rep.Results = append(rep.Results, knownGap(checkModelGet(c, e, models[e.Model]), e))
		case e.Kind == ir.KindItemSearch:
			rep.Results = append(rep.Results, checkSearch(c, e))
		case e.Kind == ir.KindItemGet && (len(e.Params) == 0 || e.Params[0].Optional):
			rep.Results = append(rep.Results, knownGap(checkItemGet(c, e, models[e.Model]), e))
		case e.Kind == ir.KindServiceStatus:
			rep.Results = append(rep.Results, checkReachable(c, e, "GET"))
		case e.Kind == ir.KindCustom && len(e.Params) == 0 && len(e.Methods) == 1 && e.Methods[0] == "GET":
			rep.Results = append(rep.Results, checkReachable(c, e, "GET"))
		}
	}
	if write {
		rep.Results = append(rep.Results, writeCycles(c, rel, models, packages)...)
	}
	rep.Results = append(rep.Results, c.schemaResults...)
	sort.Slice(rep.Results, func(i, j int) bool {
		if rep.Results[i].Path != rep.Results[j].Path {
			return rep.Results[i].Path < rep.Results[j].Path
		}
		return rep.Results[i].Check < rep.Results[j].Check
	})
	return rep
}

func fail(r *Result, code int, raw []byte, err error) *Result {
	r.HTTP = code
	if err != nil {
		r.Status, r.Findings = "error", []string{err.Error()}
		return r
	}
	r.Status, r.Findings = "error", []string{fmt.Sprintf("HTTP %d: %s", code, trunc(raw))}
	return r
}

func checkReachable(c *Client, e *ir.Endpoint, method string) *Result {
	r := &Result{Path: e.Path, Method: method, Check: "reachable"}
	code, v, raw, err := c.do(method, e.Path, nil)
	var nj *NonJSON
	if errors.As(err, &nj) {
		r.HTTP, r.Status, r.Findings = code, "ok", []string{err.Error()}
		return r
	}
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP, r.Status = code, "ok"
	c.schema(e, method, e.Path, v)
	return r
}

// compare the response object with the model node (Read shape)
func compareNode(n *model.Node, v any, path string, out *[]string) {
	obj, ok := v.(map[string]any)
	if arr, isArr := v.([]any); isArr && len(arr) == 0 {
		obj, ok = map[string]any{}, true // PHP encodes an empty object as []
		if len(n.Children) == 0 {
			return
		}
	}
	if !ok {
		*out = append(*out, fmt.Sprintf("%s: expected object, got %T", path, v))
		return
	}
	known := map[string]bool{}
	for _, ch := range n.Children {
		known[ch.Name] = true
		p := strings.TrimPrefix(path+"."+ch.Name, ".")
		val, present := obj[ch.Name]
		if !present {
			*out = append(*out, p+": missing in response")
			continue
		}
		switch ch.Kind {
		case model.KindContainer:
			compareNode(ch, val, p, out)
		case model.KindArray:
			if _, ok := val.(map[string]any); !ok {
				if arr, ok := val.([]any); !ok || len(arr) != 0 {
					*out = append(*out, fmt.Sprintf("%s: expected UUID map, got %T", p, val))
				}
			}
		case model.KindField:
			switch ch.Shape {
			case model.ShapeSelect:
				// map, or a JSON array when PHP keys are 0..n-1
				_, isMap := val.(map[string]any)
				_, isList := val.([]any)
				if !isMap && !isList {
					*out = append(*out, fmt.Sprintf("%s: expected select map, got %T", p, val))
				}
			case model.ShapeString:
				if fieldShapeUncertain(ch) {
					break
				}
				if _, ok := val.(string); !ok {
					*out = append(*out, fmt.Sprintf("%s: expected string, got %T", p, val))
				}
			}
		}
	}
	for k := range obj {
		if !known[k] && !strings.HasPrefix(k, "%") {
			*out = append(*out, strings.TrimPrefix(path+"."+k, ".")+": not in model")
		}
	}
}

// fieldShapeUncertain: the generator already marked the read shape as
// unknown (custom getNodeData/getNodeOptions), so no strict comparison.
func fieldShapeUncertain(n *model.Node) bool {
	return gap.Has(n.Gaps, gap.CustomReadShape)
}

func checkModelGet(c *Client, e *ir.Endpoint, m *model.Model) *Result {
	r := &Result{Path: e.Path, Method: "GET", Check: "shape"}
	code, v, raw, err := c.do("GET", e.Path, nil)
	var nj *NonJSON
	if errors.As(err, &nj) {
		r.HTTP, r.Status, r.Findings = code, "mismatch", []string{err.Error()}
		return r // knownGap() turns this into known-gap for overridden actions
	}
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP = code
	c.schema(e, "GET", e.Path, v)
	obj, _ := v.(map[string]any)
	inner, ok := obj[e.Wrapper]
	if !ok {
		var keys []string
		for k := range obj {
			keys = append(keys, k)
		}
		r.Status, r.Findings = "mismatch", []string{fmt.Sprintf("root key %q missing, got %v", e.Wrapper, keys)}
		return r
	}
	if m == nil {
		r.Status, r.Findings = "skipped", []string{"model unknown"}
		return r
	}
	compareNode(m.Root, inner, "", &r.Findings)
	r.Status = status(r.Findings)
	return r
}

func status(f []string) string {
	if len(f) == 0 {
		return "ok"
	}
	return "mismatch"
}

// knownGap turns a mismatch into "known-gap" when the endpoint was already
// marked as overriding the generic action (the test confirms the gap).
func knownGap(r *Result, e *ir.Endpoint) *Result {
	if r.Status != "mismatch" {
		return r
	}
	if g, ok := gap.First(e.Gaps, gap.MayDiffer); ok {
		r.Status = "known-gap"
		r.Findings = append(r.Findings, "expected ("+string(g.Code)+"): "+g.Detail)
	}
	return r
}

func checkSearch(c *Client, e *ir.Endpoint) *Result {
	r := &Result{Path: e.Path, Method: "GET", Check: "shape"}
	code, v, raw, err := c.do("GET", e.Path+"?current=1&rowCount=-1", nil)
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP = code
	c.schema(e, "GET", e.Path, v)
	obj, _ := v.(map[string]any)
	for _, k := range []string{"rows", "total", "rowCount", "current"} {
		if _, ok := obj[k]; !ok {
			r.Findings = append(r.Findings, "search result has no "+k)
		}
	}
	r.Status = status(r.Findings)
	return r
}

// item get without uuid returns a new entry with defaults
func checkItemGet(c *Client, e *ir.Endpoint, m *model.Model) *Result {
	r := &Result{Path: e.Path, Method: "GET", Check: "shape"}
	code, v, raw, err := c.do("GET", e.Path, nil)
	if err != nil || code != 200 {
		return fail(r, code, raw, err)
	}
	r.HTTP = code
	c.schema(e, "GET", e.Path, v)
	obj, _ := v.(map[string]any)
	if e.Outer != "" {
		obj, _ = obj[e.Outer].(map[string]any)
	}
	inner, ok := obj[e.Wrapper]
	if !ok {
		r.Status, r.Findings = "mismatch", []string{fmt.Sprintf("root key %q missing", strings.TrimPrefix(e.Outer+"."+e.Wrapper, "."))}
		return r
	}
	if m != nil {
		if n := m.Find(e.ModelPath); n != nil {
			compareNode(&model.Node{Kind: model.KindContainer, Children: n.Children}, inner, e.ModelPath, &r.Findings)
		}
	}
	r.Status = status(r.Findings)
	return r
}

func isOption(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	_, hasSel := m["selected"]
	return hasSel
}

// isSelect: a select map/list as read (all members are {value, selected}).
func isSelect(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			return false
		}
		for _, o := range t {
			if !isOption(o) {
				return false
			}
		}
		return true
	case []any:
		for _, o := range t {
			if !isOption(o) {
				return false
			}
		}
		return true
	}
	return false
}

// writeValue turns a read value into its write form: strings stay, select
// maps become comma-separated selected keys, containers recurse.
func writeValue(v any) any {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var keys []string
		for i, o := range t {
			if sel, _ := o.(map[string]any)["selected"].(float64); sel == 1 {
				keys = append(keys, fmt.Sprint(i))
			}
		}
		return strings.Join(keys, ",")
	case map[string]any:
		if isSelect(t) {
			var keys []string
			for k, o := range t {
				if sel, _ := o.(map[string]any)["selected"].(float64); sel == 1 {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			return strings.Join(keys, ",")
		}
		out := map[string]any{}
		for k, c := range t {
			if !strings.HasPrefix(k, "%") {
				out[k] = writeValue(c)
			}
		}
		return out
	}
	return ""
}

// writeCycles: for every entry list with get/add/del, add an entry built
// from the server defaults (plus a description/name where required), read
// it back and delete it.
func writeCycles(c *Client, rel *ir.Release, models map[string]*model.Model, packages map[string]bool) []*Result {
	type family struct{ get, add, del, set *ir.Endpoint }
	fams := map[string]*family{}
	for _, e := range rel.Endpoints {
		if e.ModelPath == "" || e.Source != ir.SourceBaseCall || !packages[e.Package] {
			continue
		}
		k := e.Module + "/" + e.Controller + " " + e.ModelPath
		f := fams[k]
		if f == nil {
			f = &family{}
			fams[k] = f
		}
		switch e.Kind {
		case ir.KindItemGet:
			f.get = e
		case ir.KindItemAdd:
			f.add = e
		case ir.KindItemDel:
			f.del = e
		case ir.KindItemSet:
			f.set = e
		}
	}
	var keys []string
	for k := range fams {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []*Result
	for _, k := range keys {
		f := fams[k]
		if f.get == nil || f.add == nil || f.del == nil {
			continue
		}
		r := &Result{Path: f.add.Path, Method: "POST", Check: "write-cycle"}
		out = append(out, r)
		code, v, raw, err := c.do("GET", f.get.Path, nil)
		if err != nil || code != 200 {
			fail(r, code, raw, err)
			continue
		}
		root, _ := v.(map[string]any)
		if f.get.Outer != "" {
			root, _ = root[f.get.Outer].(map[string]any)
		}
		tmpl, _ := root[f.get.Wrapper].(map[string]any)
		body, _ := writeValue(tmpl).(map[string]any)
		if body == nil {
			body = map[string]any{}
		}
		m := models[f.add.Model]
		if m != nil {
			if n := m.Find(f.add.ModelPath); n != nil {
				for _, ch := range n.Children {
					if ch.Kind == model.KindField && (ch.Type == "DescriptionField" || ch.Name == "description" || ch.Name == "descr") {
						body[ch.Name] = "contract-test"
					}
				}
			}
		}
		code, v, raw, err = c.do("POST", f.add.Path, map[string]any{f.add.Wrapper: body})
		if err != nil || code != 200 {
			fail(r, code, raw, err)
			continue
		}
		r.HTTP = code
		c.schema(f.add, "POST", f.add.Path, v)
		res, _ := v.(map[string]any)
		uuid, _ := res["uuid"].(string)
		if res["result"] != "saved" || uuid == "" {
			r.Status = "skipped"
			// fields OPNsense requires at runtime vs. required in the model
			var runtime, notInModel []string
			if vals, ok := res["validations"].(map[string]any); ok {
				for k := range vals {
					runtime = append(runtime, k)
				}
			}
			sort.Strings(runtime)
			req := map[string]bool{}
			if m != nil {
				if n := m.Find(f.add.ModelPath); n != nil {
					for _, ch := range n.Children {
						if ch.Required && ch.Default == nil {
							req[f.add.Wrapper+"."+ch.Name] = true
						}
					}
				}
			}
			for _, k := range runtime {
				if !req[k] {
					notInModel = append(notInModel, k)
				}
			}
			r.Findings = append(r.Findings, "add with server defaults rejected; runtime-required: "+strings.Join(runtime, ", "))
			for _, k := range runtime {
				r.RuntimeRequired = append(r.RuntimeRequired, strings.TrimPrefix(k, f.add.Wrapper+"."))
			}
			if len(notInModel) > 0 {
				r.Findings = append(r.Findings, "required at runtime but not marked Required in the model: "+strings.Join(notInModel, ", "))
			}
			continue
		}
		code, v, raw, err = c.do("GET", f.get.Path+"/"+uuid, nil)
		if err != nil || code != 200 {
			r.Findings = append(r.Findings, fmt.Sprintf("get/%s: HTTP %d %s", uuid, code, trunc(raw)))
		} else {
			c.schema(f.get, "GET", f.get.Path+"/{uuid}", v)
			if _, ok := v.(map[string]any)[f.get.Wrapper]; !ok {
				r.Findings = append(r.Findings, "get by uuid: root key "+f.get.Wrapper+" missing")
			}
		}
		code, v, raw, err = c.do("POST", f.del.Path+"/"+uuid, map[string]any{})
		if err != nil || code != 200 {
			r.Findings = append(r.Findings, fmt.Sprintf("del: HTTP %d %s", code, trunc(raw)))
		} else {
			c.schema(f.del, "POST", f.del.Path+"/{uuid}", v)
			if res, _ := v.(map[string]any); res["result"] != "deleted" {
				r.Findings = append(r.Findings, "del: "+trunc(raw))
			}
		}
		r.Status = status(r.Findings)
	}
	return out
}
