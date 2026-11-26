// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package groups

import (
	"fmt"
	"slices"
	"sort"

	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

// MergeReports combines the reports of one release whose contract test was split
// over several VMs (tools/parallel.sh: each VM tests a group of plugins,
// exactly one of them the core) into the report of one run. The parts must
// describe the same release, generator, catalog state and instance
// version, and no package may have been tested twice: a package in two
// parts means the groups overlapped (the core in two groups, for one), and
// its results would count twice. Accepted entries count as used when any
// part used them; they are listed in the order of the accepted list.
func MergeReports(parts []*verify.Report, accepted []verify.Accepted) (*verify.Report, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("no reports to merge")
	}
	first := parts[0]
	out := &verify.Report{Release: first.Release, Composition: first.Composition, Instance: first.Instance,
		Generator: first.Generator, Date: first.Date, Packages: []string{}, Results: []*verify.Result{}}
	by := map[string]int{} // package -> part that tested it
	used, unused := map[string]bool{}, map[string]bool{}
	for i, p := range parts {
		for _, d := range []struct{ what, a, b string }{
			{"release", first.Release, p.Release},
			{"generator", first.Generator, p.Generator},
			{"composition", first.Composition, p.Composition},
			{"instance version", first.Instance, p.Instance},
		} {
			if d.a != d.b {
				return nil, fmt.Errorf("part %d has %s %q, part 1 %q", i+1, d.what, d.b, d.a)
			}
		}
		for _, pk := range p.Packages {
			if j, ok := by[pk]; ok {
				return nil, fmt.Errorf("package %s was tested in part %d and part %d", pk, j+1, i+1)
			}
			by[pk] = i
			out.Packages = append(out.Packages, pk)
		}
		for _, n := range p.NotInstallable {
			if !slices.Contains(out.NotInstallable, n) {
				out.NotInstallable = append(out.NotInstallable, n)
			}
		}
		if p.Date > out.Date {
			out.Date = p.Date
		}
		out.Results = append(out.Results, p.Results...)
		for _, id := range p.UsedAccepted {
			used[id] = true
		}
		for _, id := range p.UnusedAccepted {
			unused[id] = true
		}
	}
	sort.Strings(out.Packages)
	sort.Strings(out.NotInstallable)
	verify.SortResults(out.Results)
	order := map[string]int{}
	for i, a := range accepted {
		order[a.ID()] = i
	}
	list := func(set map[string]bool, keep func(string) bool) []string {
		var ids []string
		for id := range set {
			if keep(id) {
				ids = append(ids, id)
			}
		}
		// the accepted list's order; an ID it no longer has after them
		sort.Slice(ids, func(i, j int) bool {
			oi, iok := order[ids[i]]
			oj, jok := order[ids[j]]
			if iok != jok {
				return iok
			}
			if iok {
				return oi < oj
			}
			return ids[i] < ids[j]
		})
		return ids
	}
	out.UsedAccepted = list(used, func(string) bool { return true })
	out.UnusedAccepted = list(unused, func(id string) bool { return !used[id] })
	return out, nil
}
