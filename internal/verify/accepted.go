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
	Path     string `json:"path"`     // endpoint path or path template
	Check    string `json:"check"`    // reachable, shape, schema, write-cycle
	Releases string `json:"releases"` // glob on the release, e.g. "26.1.*" or "*"
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
		if f.Path == "" || f.Check == "" || f.Reason == "" || f.Evidence == "" {
			return nil, fmt.Errorf("%s: entry %d needs path, check, reason and evidence", file, i)
		}
	}
	return a.Findings, nil
}

// ApplyAccepted turns mismatches and errors that match an accepted entry
// into status "accepted" (the reason is added to the findings).
func ApplyAccepted(rep *Report, list []Accepted) int {
	n := 0
	for _, r := range rep.Results {
		if r.Status != "mismatch" && r.Status != "error" {
			continue
		}
		for _, a := range list {
			rel := a.Releases
			if rel == "" {
				rel = "*"
			}
			if ok, _ := path.Match(rel, rep.Release); !ok {
				continue
			}
			if a.Path == r.Path && a.Check == r.Check {
				r.Status = "accepted"
				r.Findings = append(r.Findings, "accepted: "+strings.TrimSpace(a.Reason))
				n++
				break
			}
		}
	}
	return n
}
