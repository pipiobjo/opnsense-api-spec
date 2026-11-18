// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package verify runs contract tests of the IR against a running OPNsense
// instance. It must only ever be pointed at a throwaway test instance: the
// write cycle adds and deletes entries.
package verify

import (
	"fmt"
	"sort"
	"time"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// Result of one check against one endpoint.
type Result struct {
	Path     string   `json:"path"`
	Method   string   `json:"method"`
	Check    Check    `json:"check"`
	Status   Status   `json:"status"`
	HTTP     int      `json:"http,omitempty"`
	Findings []string `json:"findings,omitempty"` // what is wrong; decides the status
	Notes    []string `json:"notes,omitempty"`    // remarks that do not fail the check
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
	// diffs (shape checks only): the structured differences behind
	// Findings, so knownGap decides by kind, never by message text
	diffs []shapeDiff
	// rootMissing (read checks only): the answer lacks the root key the
	// data names, or is not JSON; never explained by a gap when the data
	// names a key (knownGap)
	rootMissing bool
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
	// UnusedAccepted: entries of verify/accepted.json that apply to this
	// release but covered no failed check in this run (see ApplyAccepted).
	UnusedAccepted []string `json:"unused_accepted,omitempty"`
}

// Run executes all checks for endpoints of the given packages (installed
// on the instance). write enables the add/get/del cycle.
func Run(c *Client, rel *ir.Release, packages map[string]bool, write bool) *Report {
	rep := &Report{Release: rel.Version, Generator: rel.Generator.Version, Date: time.Now().UTC().Format(time.RFC3339)}
	rep.Instance, _ = c.InstanceVersion()
	c.retried = nil // before the first check; the version is not a check
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
			rep.Results = append(rep.Results, c.noted(knownGap(checkModelGet(c, e, models[e.Model]), e)))
		case e.Kind == ir.KindItemSearch:
			rep.Results = append(rep.Results, c.noted(checkSearch(c, e)))
		case e.Kind == ir.KindItemGet && (len(e.Params) == 0 || e.Params[0].Optional):
			rep.Results = append(rep.Results, c.noted(knownGap(checkItemGet(c, e, models[e.Model]), e)))
		case e.Kind == ir.KindServiceStatus:
			rep.Results = append(rep.Results, c.noted(checkReachable(c, e, "GET")))
		case e.Kind == ir.KindCustom && len(e.Params) == 0 && len(e.Methods) == 1 && e.Methods[0] == "GET":
			rep.Results = append(rep.Results, c.noted(checkReachable(c, e, "GET")))
		}
	}
	if write {
		rep.Results = append(rep.Results, writeCycles(c, rel, models, packages)...)
		rep.Results = append(rep.Results, settingsWriteBacks(c, rel, models, packages)...)
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
		r.Status, r.Findings = StatusError, []string{err.Error()}
		return r
	}
	r.Status, r.Findings = StatusError, []string{fmt.Sprintf("HTTP %d: %s", code, trunc(raw))}
	return r
}
