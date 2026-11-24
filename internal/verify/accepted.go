// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"
)

// Accepted is a reviewed finding that must not block publishing: an
// upstream issue or a limitation of the test environment. Every entry
// needs a reason and evidence.
type Accepted struct {
	Path     string `json:"path"` // endpoint path or path template
	Check    Check  `json:"check"`
	Releases string `json:"releases"` // glob on the release, e.g. "26.1.*" or "*"
	// Match, when set, is a substring one of the check's findings must
	// contain (for example "HTTP 500"), so a different failure on the same
	// path and check is not hidden by an entry that was reviewed for
	// another one.
	Match    string `json:"match,omitempty"`
	Reason   string `json:"reason"`
	Evidence string `json:"evidence"`
	Upstream string `json:"upstream,omitempty"` // issue link, when reported
}

// LoadAccepted reads the accepted findings list.
func LoadAccepted(file string) ([]Accepted, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var a struct {
		Findings []Accepted `json:"findings"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	for i, f := range a.Findings {
		// match is required: an entry for a path and check alone would hide
		// any other failure there (finding REQ-9)
		if f.Path == "" || f.Check == "" || f.Match == "" || f.Reason == "" || f.Evidence == "" {
			return nil, fmt.Errorf("%s: entry %d needs path, check, match, reason and evidence", file, i)
		}
	}
	return a.Findings, nil
}

// ID names an entry in messages: path, check, releases and match.
func (a Accepted) ID() string {
	id := fmt.Sprintf("%s %s %s", a.Path, a.Check, a.releases())
	if a.Match != "" {
		id += fmt.Sprintf(" match %q", a.Match)
	}
	return id
}

func (a Accepted) releases() string {
	if a.Releases == "" {
		return "*"
	}
	return a.Releases
}

// covers: the entry applies to this failed result of release rel.
func (a Accepted) covers(rel string, r *Result) bool {
	if ok, _ := path.Match(a.releases(), rel); !ok || a.Path != r.Path || a.Check != r.Check {
		return false
	}
	if a.Match == "" {
		return true
	}
	for _, f := range r.Findings {
		if strings.Contains(f, a.Match) {
			return true
		}
	}
	return false
}

// ApplyAccepted turns mismatches and errors that match an accepted entry
// into status "accepted" (the reason is added to the findings). Entries
// whose release glob matches the run but that covered no result are listed
// in rep.UnusedAccepted: the failure they were written for is gone (or the
// plugin was not installed), so they are candidates for removal.
func ApplyAccepted(rep *Report, list []Accepted) int {
	n := 0
	used := make([]bool, len(list))
	for _, r := range rep.Results {
		if !r.Status.Failed() {
			continue
		}
		for i, a := range list {
			if a.covers(rep.Release, r) {
				r.Status = StatusAccepted
				r.Findings = append(r.Findings, "accepted: "+strings.TrimSpace(a.Reason))
				used[i] = true
				n++
				break
			}
		}
	}
	rep.UnusedAccepted, rep.UsedAccepted = nil, nil
	for i, a := range list {
		switch ok, _ := path.Match(a.releases(), rep.Release); {
		case used[i]:
			rep.UsedAccepted = append(rep.UsedAccepted, a.ID())
		case ok:
			rep.UnusedAccepted = append(rep.UnusedAccepted, a.ID())
		}
	}
	return n
}
