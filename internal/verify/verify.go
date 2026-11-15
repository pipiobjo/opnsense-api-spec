// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

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
	// Sample (write-cycle only): the entry the instance accepted, in write
	// form without wrapper. Values of option fields come from this
	// instance's options (interfaces, relations) and may not exist elsewhere.
	Sample map[string]any `json:"sample,omitempty"`
	// Generated (write-cycle only): fields that needed a generated value
	// because the server default was rejected.
	Generated []string `json:"generated,omitempty"`
	// Steps (write-cycle only): the steps that completed: add, read-back,
	// set, read-back-set, del. A missing "set" means the update was not
	// tested (no set endpoint, or the changed value was rejected).
	Steps []string `json:"steps,omitempty"`
}

// Report is the outcome of a run.
type Report struct {
	Release string `json:"release"`
	// Composition is the id of the catalog state the IR was built from
	// (set by the caller), so a later catalog change is visible as
	// "not verified".
	Composition string `json:"composition,omitempty"`
	Instance    string `json:"instance_version"`
	Generator   string `json:"generator"`
	Date        string `json:"date"`
	// Packages is the package selection the run tested (core "opnsense"
	// and the plugins that were installed). Endpoints of other packages
	// were not tested; the index reports them as untested.
	Packages []string  `json:"packages"`
	Results  []*Result `json:"results"`
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
	for p, ok := range packages {
		if ok {
			rep.Packages = append(rep.Packages, p)
		}
	}
	sort.Strings(rep.Packages)
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

// writeCycles: for every entry list with get/add/del, add an entry, read
// it back, change one field, read it back again and delete it.
//
// The entry starts from the server defaults (the empty-entry template),
// with a description where the model has one. When the instance rejects
// fields, each rejected field gets a generated value (values.go) and the
// add is retried. A rejected relation without anything to point at gets a
// prerequisite entry in the target list (created the same way, deleted
// afterwards). A field whose candidates are exhausted makes the cycle
// "skipped" with the reason. Nothing is applied (no reconfigure), so the
// running test instance is not changed beyond its stored configuration.
func writeCycles(c *Client, rel *ir.Release, models map[string]*model.Model, packages map[string]bool) []*Result {
	w := &writer{c: c, byList: map[string]*family{}}
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
			f.gets = append(f.gets, e)
		case ir.KindItemAdd:
			f.add = e
		case ir.KindItemDel:
			f.del = e
		case ir.KindItemSet:
			f.set = e
		}
	}
	var keys []string
	for k, f := range fams {
		if f.add == nil || f.del == nil {
			continue
		}
		if f.get = entryGet(f.gets, f.add); f.get == nil {
			continue
		}
		if m := models[f.add.Model]; m != nil {
			f.entry = m.Find(f.add.ModelPath)
		}
		w.byList[listKey(f.add.Model, f.add.ModelPath)] = f
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []*Result
	for _, k := range keys {
		out = append(out, w.cycle(fams[k]))
	}
	return out
}

type family struct {
	gets               []*ir.Endpoint
	get, add, del, set *ir.Endpoint
	entry              *model.Node
}

type writer struct {
	c      *Client
	byList map[string]*family // model class + "#" + entry list path
}

// maxRounds bounds the add retries of one entry list; maxDepth bounds
// prerequisite chains (a record needs a zone, a zone needs nothing).
const (
	maxRounds = 8
	maxDepth  = 2
)

// added is the outcome of adding one entry.
type added struct {
	uuid     string
	body     map[string]any
	chosen   map[string]bool // fields set on purpose (compared on read-back)
	tried    map[string]int  // generated fields -> candidate index
	runtime  []string        // fields rejected with server defaults
	notes    []string
	cleanups []func() []string // delete prerequisites, newest first
	err      *Result           // set when the add failed or was skipped
}

func (w *writer) add(f *family, depth int) *added {
	c := w.c
	res := &added{chosen: map[string]bool{}, tried: map[string]int{}}
	r := &Result{Path: f.add.Path, Method: "POST", Check: "write-cycle"}
	code, v, raw, err := c.do("GET", f.get.Path, nil)
	if err != nil || code != 200 {
		res.err = fail(r, code, raw, err)
		return res
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
	if f.entry != nil {
		for _, ch := range f.entry.Children {
			if ch.Kind == model.KindField && (ch.Type == "DescriptionField" || ch.Name == "description" || ch.Name == "descr") {
				if cands := candidates(ch, nil); len(cands) > 0 {
					body[ch.Name] = cands[0]
					res.chosen[ch.Name] = true
				}
			}
		}
	}
	prefix := f.add.Wrapper + "."
	related := map[string]bool{}
	for round := 0; ; round++ {
		code, v, raw, err = c.do("POST", f.add.Path, map[string]any{f.add.Wrapper: body})
		if err != nil || code != 200 {
			res.err = fail(r, code, raw, err)
			return res
		}
		if round == 0 && depth == 0 {
			c.schema(f.add, "POST", f.add.Path, v)
		}
		out, _ := v.(map[string]any)
		if id, _ := out["uuid"].(string); out["result"] == "saved" && id != "" {
			res.uuid, res.body = id, body
			return res
		}
		vals, _ := out["validations"].(map[string]any)
		if len(vals) == 0 {
			r.Status, r.Findings = "error", []string{"add failed without validation messages: " + trunc(raw)}
			res.err = r
			return res
		}
		var fields []string
		for k := range vals {
			fields = append(fields, strings.TrimPrefix(k, prefix))
		}
		sort.Strings(fields)
		if round == 0 {
			res.runtime = fields
		}
		var stuck []string
		for _, fld := range fields {
			n := node(f.entry, fld)
			cands := candidates(n, lookup(tmpl, fld))
			i, seen := res.tried[fld]
			if seen {
				i++
			}
			if i < len(cands) {
				res.tried[fld] = i
				res.chosen[fld] = true
				assign(body, fld, cands[i])
				continue
			}
			if !related[fld] && depth < maxDepth {
				if id, note, cleanup := w.prerequisite(n, depth+1); id != "" {
					related[fld] = true
					res.tried[fld] = len(cands)
					res.chosen[fld] = true
					res.notes = append(res.notes, note)
					res.cleanups = append([]func() []string{cleanup}, res.cleanups...)
					assign(body, fld, id)
					continue
				}
			}
			stuck = append(stuck, fmt.Sprintf("%s (%v)", fld, vals[prefix+fld]))
		}
		if len(stuck) > 0 || round+1 >= maxRounds {
			r.Status = "skipped"
			reason := "no accepted value for: " + strings.Join(stuck, "; ")
			if len(stuck) == 0 {
				reason = fmt.Sprintf("not accepted after %d rounds: %s", maxRounds, strings.Join(fields, ", "))
			}
			r.Findings = append(r.Findings, "add rejected; runtime-required: "+strings.Join(res.runtime, ", "), reason)
			r.RuntimeRequired = res.runtime
			res.err = r
			return res
		}
	}
}

// prerequisite creates an entry in the list a relation field points to and
// returns its uuid, a note and the function that deletes it again.
// listKey identifies an entry list by model class and item path. Relations
// name the class with dots and in any case ("OPNsense.quagga.ospf" for
// OPNsense\Quagga\OSPF); PHP class names are case-insensitive.
func listKey(class, items string) string {
	return strings.ToLower(strings.ReplaceAll(class, ".", `\`)) + "#" + items
}

func (w *writer) prerequisite(n *model.Node, depth int) (string, string, func() []string) {
	if n == nil {
		return "", "", nil
	}
	for _, rel := range n.Relations {
		f := w.byList[listKey(rel.Source, rel.Items)]
		if f == nil {
			continue
		}
		a := w.add(f, depth)
		if a.uuid == "" {
			for _, cl := range a.cleanups {
				cl()
			}
			continue
		}
		cleanup := func() []string {
			var fs []string
			if code, _, raw, err := w.c.do("POST", f.del.Path+"/"+a.uuid, map[string]any{}); err != nil || code != 200 {
				fs = append(fs, fmt.Sprintf("deleting prerequisite %s/%s: HTTP %d %s", f.del.Path, a.uuid, code, trunc(raw)))
			}
			for _, cl := range a.cleanups {
				fs = append(fs, cl()...)
			}
			return fs
		}
		return a.uuid, "note: prerequisite entry created via " + f.add.Path, cleanup
	}
	return "", "", nil
}

func (w *writer) cycle(f *family) *Result {
	c := w.c
	a := w.add(f, 0)
	defer func() {
		for _, cl := range a.cleanups {
			cl()
		}
	}()
	if a.err != nil {
		if a.err.Status == "skipped" {
			if nm := notRequiredInModel(f.entry, a.runtime); len(nm) > 0 {
				a.err.Findings = append([]string{"required at runtime but not marked Required in the model: " + strings.Join(nm, ", ")}, a.err.Findings...)
			}
		}
		return a.err
	}
	r := &Result{Path: f.add.Path, Method: "POST", Check: "write-cycle", HTTP: 200, RuntimeRequired: a.runtime}
	if nm := notRequiredInModel(f.entry, a.runtime); len(nm) > 0 {
		r.Findings = append(r.Findings, "required at runtime but not marked Required in the model: "+strings.Join(nm, ", "))
	}
	r.Findings = append(r.Findings, a.notes...)
	for fld := range a.tried {
		r.Generated = append(r.Generated, fld)
	}
	sort.Strings(r.Generated)
	r.Sample = a.body
	r.Steps = append(r.Steps, "add")
	uuid, body := a.uuid, a.body

	// read back what we chose (values the server changes itself, such as
	// ids, audit fields or serials, are not writes of ours)
	sent := map[string]any{}
	for fld := range a.chosen {
		if !writeOnly(node(f.entry, fld)) {
			assign(sent, fld, lookup(body, fld))
		}
	}
	if fs := readBack(c, f.get, uuid, sent, "add"); len(fs) > 0 {
		r.Findings = append(r.Findings, fs...)
	} else {
		r.Steps = append(r.Steps, "read-back")
	}

	// change one field and read it back
	if f.set == nil {
		r.Findings = append(r.Findings, "note: update not tested (no set endpoint)")
	} else if fld, val := changeable(f.entry, body, a.chosen); fld == "" {
		r.Findings = append(r.Findings, "note: update not tested (no field to change)")
	} else {
		code, v, raw, err := c.do("POST", f.set.Path+"/"+uuid, map[string]any{f.set.Wrapper: map[string]any{fld: val}})
		res, _ := v.(map[string]any)
		switch {
		case err != nil || code != 200:
			r.Findings = append(r.Findings, fmt.Sprintf("set: HTTP %d %s", code, trunc(raw)))
		case res["result"] != "saved" && res["validations"] != nil:
			// the changed value broke a rule involving other fields; this
			// says nothing about the endpoint, but update is not verified
			r.Findings = append(r.Findings, fmt.Sprintf("note: update not tested (%s=%q rejected: %s)", fld, val, trunc(raw)))
		case res["result"] != "saved":
			r.Findings = append(r.Findings, "set "+fld+": "+trunc(raw))
		default:
			c.schema(f.set, "POST", f.set.Path+"/{uuid}", v)
			r.Steps = append(r.Steps, "set")
			if fs := readBack(c, f.get, uuid, map[string]any{fld: val}, "set"); len(fs) > 0 {
				r.Findings = append(r.Findings, fs...)
			} else {
				r.Steps = append(r.Steps, "read-back-set")
			}
		}
	}

	code, v, raw, err := c.do("POST", f.del.Path+"/"+uuid, map[string]any{})
	if code == 500 && strings.Contains(string(raw), "locked") && lookup(body, "lock") == "1" && f.set != nil {
		// e.g. interface assignments: "Interface locked, unset lock first
		// before removal"; lock defaults to 1. Clients must unlock first.
		if c2, v2, _, _ := c.do("POST", f.set.Path+"/"+uuid, map[string]any{f.set.Wrapper: map[string]any{"lock": "0"}}); c2 == 200 {
			if res2, _ := v2.(map[string]any); res2["result"] == "saved" {
				r.Findings = append(r.Findings, "note: delete refused while lock=1 (the default); deleted after setting lock=0")
				code, v, raw, err = c.do("POST", f.del.Path+"/"+uuid, map[string]any{})
			}
		}
	}
	res, _ := v.(map[string]any)
	switch {
	case err != nil || code != 200:
		r.Findings = append(r.Findings, fmt.Sprintf("del: HTTP %d %s", code, trunc(raw)))
	case gap.Has(f.del.Gaps, gap.ResultNotReturned) && len(raw) == 0:
		r.Steps = append(r.Steps, "del") // empty answer, as the spec says
	case res["result"] != "deleted":
		r.Findings = append(r.Findings, "del: "+trunc(raw))
	default:
		c.schema(f.del, "POST", f.del.Path+"/{uuid}", v)
		r.Steps = append(r.Steps, "del")
	}
	for _, cl := range a.cleanups {
		r.Findings = append(r.Findings, cl()...)
	}
	a.cleanups = nil
	r.Status = writeStatus(r.Findings)
	if r.Status == "mismatch" {
		r = knownGap(r, f.get)
	}
	return r
}

// writeOnly: fields the server never returns (passwords and the like).
func writeOnly(n *model.Node) bool {
	return n != nil && (n.BaseType == "UpdateOnlyTextField" || n.Type == "UpdateOnlyTextField")
}

// readBack gets the entry and compares every sent field (write form).
// Differences are findings prefixed "read-back".
func readBack(c *Client, get *ir.Endpoint, uuid string, sent map[string]any, step string) []string {
	code, v, raw, err := c.do("GET", get.Path+"/"+uuid, nil)
	if err != nil || code != 200 {
		return []string{fmt.Sprintf("get/%s after %s: HTTP %d %s", uuid, step, code, trunc(raw))}
	}
	c.schema(get, "GET", get.Path+"/{uuid}", v)
	root, _ := v.(map[string]any)
	if get.Outer != "" {
		root, _ = root[get.Outer].(map[string]any)
	}
	got, ok := root[get.Wrapper].(map[string]any)
	if !ok {
		return []string{"get by uuid: root key " + get.Wrapper + " missing"}
	}
	var out []string
	var walk func(prefix string, s map[string]any)
	walk = func(prefix string, s map[string]any) {
		var ks []string
		for k := range s {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			p := strings.TrimPrefix(prefix+"."+k, ".")
			if sub, ok := s[k].(map[string]any); ok {
				walk(p, sub)
				continue
			}
			want, _ := s[k].(string)
			have := writeValue(lookup(got, p))
			if hs, ok := have.(string); !ok || !sameValue(want, hs) {
				out = append(out, fmt.Sprintf("read-back after %s: %s sent %q, read %v", step, p, want, have))
			}
		}
	}
	walk("", sent)
	return out
}

// sameValue compares written and read values; comma-separated lists are
// compared as sets (the server may reorder them).
func sameValue(a, b string) bool {
	if a == b {
		return true
	}
	if !strings.Contains(a, ",") && !strings.Contains(b, ",") {
		return false
	}
	as, bs := strings.Split(a, ","), strings.Split(b, ",")
	sort.Strings(as)
	sort.Strings(bs)
	return strings.Join(as, ",") == strings.Join(bs, ",")
}

// changeable picks a field for the set step and a new value that satisfies
// the field's mask: the description first, else another field we chose.
func changeable(entry *model.Node, body map[string]any, chosen map[string]bool) (string, string) {
	var order []string
	for _, name := range []string{"description", "descr"} {
		if chosen[name] {
			order = append(order, name)
		}
	}
	var rest []string
	for f := range chosen {
		if f != "description" && f != "descr" && !strings.Contains(f, ".") {
			rest = append(rest, f)
		}
	}
	sort.Strings(rest)
	for _, f := range append(order, rest...) {
		n := node(entry, f)
		if writeOnly(n) {
			continue
		}
		cur, _ := lookup(body, f).(string)
		for _, cand := range candidates(n, nil) {
			if cand != cur {
				return f, cand
			}
		}
	}
	return "", ""
}

func notRequiredInModel(entry *model.Node, fields []string) []string {
	var out []string
	for _, f := range fields {
		n := node(entry, f)
		if n == nil || !(n.Required && n.Default == nil) {
			out = append(out, f)
		}
	}
	return out
}

// writeStatus: read-back differences and failed steps are mismatches;
// notes (update not tested) and the remark about runtime-required fields
// are not. Whether the update was tested is in Result.Steps.
func writeStatus(f []string) string {
	for _, s := range f {
		if !strings.HasPrefix(s, "required at runtime but not marked Required") && !strings.HasPrefix(s, "note: ") {
			return "mismatch"
		}
	}
	return "ok"
}

// entryGet picks the get endpoint of an entry list: the one taking a uuid
// with the add endpoint's wrapper (os-wol has getHost and getwake for the
// same list; only getHost reads an entry).
func entryGet(gets []*ir.Endpoint, add *ir.Endpoint) *ir.Endpoint {
	var best *ir.Endpoint
	for _, g := range gets {
		takesUUID := len(g.Params) > 0
		switch {
		case g.Wrapper == add.Wrapper && takesUUID:
			return g
		case best == nil || (g.Wrapper == add.Wrapper && best.Wrapper != add.Wrapper):
			best = g
		}
	}
	return best
}
