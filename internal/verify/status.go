// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package verify

// Status is the outcome of one check. The order of severity, and which
// statuses fail a run, are defined here and nowhere else (scripts ask
// "opnsense-api-spec failures").
type Status string

const (
	StatusOK       Status = "ok"
	StatusAccepted Status = "accepted"  // a failure declared expected in verify/accepted.json
	StatusSkipped  Status = "skipped"   // not tested; the findings say why
	StatusKnownGap Status = "known-gap" // differs where the derivation already marked a gap
	StatusMismatch Status = "mismatch"
	StatusError    Status = "error"
)

// Failed: the check found something nobody explained; it blocks
// publishing.
func (s Status) Failed() bool { return s == StatusMismatch || s == StatusError }

var severity = map[Status]int{"": 0, StatusOK: 1, StatusAccepted: 2, StatusSkipped: 3, StatusKnownGap: 4, StatusMismatch: 5, StatusError: 6}

// Worse returns the more severe of two statuses.
func Worse(a, b Status) Status {
	if severity[b] > severity[a] {
		return b
	}
	return a
}

// Check names what was checked.
type Check string

const (
	CheckReachable  Check = "reachable"   // the endpoint answers
	CheckShape      Check = "shape"       // a read answer matches the model
	CheckSchema     Check = "schema"      // an answer matches the derived OpenAPI schema
	CheckWriteCycle Check = "write-cycle" // written, read back, (updated,) deleted; for settings written back
	CheckFieldProbe Check = "field-probe" // every field set to values the data predicts accepted or rejected (ADR 0017)
)

// status: findings make a mismatch, no findings are ok. Non-failing
// remarks belong in Result.Notes, so they never decide the status.
func status(findings []string) Status {
	if len(findings) == 0 {
		return StatusOK
	}
	return StatusMismatch
}

// Key is the result's "<check>/<status>", the unit the index counts.
func (r *Result) Key() string { return string(r.Check) + "/" + string(r.Status) }

// Failures are the results that fail the run.
func (r *Report) Failures() []*Result {
	var out []*Result
	for _, x := range r.Results {
		if x.Status.Failed() {
			out = append(out, x)
		}
	}
	return out
}
