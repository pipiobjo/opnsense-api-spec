// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package groups splits the plugins of a release into groups that test VMs
// install and test in parallel (tools/parallel.sh). Every plugin with an
// API is in exactly one group, the core in exactly one, and plugins that
// need each other stay together. Groups named in a groups file (scenarios)
// come first; every plugin they do not name goes into automatically
// balanced groups. The split is deterministic: the same input gives the
// same groups. MergeReports combines the groups' contract test reports
// into the report of one run. (Neither runs against a VM, so neither is
// part of the contract test's coverage profile; unit tests cover both.)
package groups

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/controller"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
)

// Core is the package name of the OPNsense core.
const Core = ir.CorePackage

// File is a repository's groups file (verify/groups.json in this
// repository, test/integration/groups.json in the client).
type File struct {
	// About: what the file is for (JSON has no comments)
	About string `json:"about"`
	// AutoGroups: how many balanced groups take the plugins no named group
	// lists (default 3, at least 1)
	AutoGroups int     `json:"auto_groups"`
	Groups     []Named `json:"groups"`
	// Together: packages that must share a VM for a reason neither the
	// package catalog nor the IR shows (a plugin's install code creating
	// configuration another plugin's test uses), each with the evidence;
	// "opnsense" puts plugins on the VM that tests the core. They stay in
	// balanced groups, unlike a named group.
	Together []Together `json:"together"`
}

// Together is a set of plugins observed to need each other.
type Together struct {
	Packages []string `json:"packages"`
	Why      string   `json:"why"`
}

// Named is a scenario group: plugins that are tested together on one VM
// on purpose, with the reason.
type Named struct {
	Name     string   `json:"name"`
	Why      string   `json:"why"`
	Packages []string `json:"packages"`
	// Core: this group also tests the core (at most one group may)
	Core bool `json:"core,omitempty"`
}

// ReadFile reads and checks a groups file.
func ReadFile(path string) (File, error) {
	var f File
	b, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("%s: %w", path, err)
	}
	if f.AutoGroups == 0 {
		f.AutoGroups = 3
	}
	if f.AutoGroups < 1 {
		return f, fmt.Errorf("%s: auto_groups must be at least 1", path)
	}
	names, pkgs, core := map[string]bool{}, map[string]string{}, ""
	for _, g := range f.Groups {
		switch {
		case g.Name == "" || g.Why == "" || len(g.Packages) == 0:
			return f, fmt.Errorf("%s: every group needs a name, why and packages", path)
		case names[g.Name] || strings.HasPrefix(g.Name, "auto-") || g.Name == "conflicts":
			return f, fmt.Errorf("%s: group name %q is used twice or reserved", path, g.Name)
		case g.Core && core != "":
			return f, fmt.Errorf("%s: groups %s and %s both claim the core", path, core, g.Name)
		}
		names[g.Name] = true
		if g.Core {
			core = g.Name
		}
		for _, p := range g.Packages {
			if !strings.HasPrefix(p, "os-") {
				return f, fmt.Errorf("%s: group %s: %q is no plugin name (the core is named with \"core\": true)", path, g.Name, p)
			}
			if o, ok := pkgs[p]; ok {
				return f, fmt.Errorf("%s: %s is in groups %s and %s", path, p, o, g.Name)
			}
			pkgs[p] = g.Name
		}
	}
	for _, t := range f.Together {
		if len(t.Packages) < 2 || t.Why == "" {
			return f, fmt.Errorf("%s: every together entry needs at least two packages and why", path)
		}
		for _, p := range t.Packages {
			if !strings.HasPrefix(p, "os-") && p != Core {
				return f, fmt.Errorf("%s: together: %q is neither a plugin nor %q", path, p, Core)
			}
		}
	}
	return f, nil
}

// Group is one VM's share of a release.
type Group struct {
	Name     string   `json:"name"`
	Explicit bool     `json:"explicit"`
	Core     bool     `json:"core"`
	Plugins  []string `json:"plugins"`
	// Weight: the estimated seconds (Input.Weights) or, without
	// measurements, the number of packages
	Weight float64 `json:"weight"`
}

// Plan is the split of one release.
type Plan struct {
	Release string  `json:"release"`
	Groups  []Group `json:"groups"`
	// All: every plugin, for the conflict check (one VM installs all of
	// them together and runs no test)
	All []string `json:"all"`
	// Together: the sets of plugins that must share a VM, with the reason
	Together []Unit `json:"together,omitempty"`
	// WeightsFrom: "measured" (all packages had a measured time),
	// "partly measured" (unknown ones took the median), or "count"
	WeightsFrom string `json:"weights_from"`
	// Notes: what could not be derived (dependencies unknown, a named
	// plugin not in this release), one line each
	Notes []string `json:"notes,omitempty"`
}

// Unit is a set of plugins that must be tested on one VM.
type Unit struct {
	Packages []string `json:"packages"`
	Because  []string `json:"because"`
}

// Input is what a split needs to know about a release.
type Input struct {
	Release string
	// Plugins: the plugins to test (with API endpoints)
	Plugins []string
	// Needs: package -> packages it needs, with the reason
	// (Dependencies)
	Needs map[string]map[string]string
	// Weights: measured seconds per package ("opnsense" for the core);
	// may be empty
	Weights map[string]float64
}

// Dependencies derives which packages need which, from the signed package
// catalog (a plugin's package dependencies on other plugins, which the
// firmware installs with it) and the IR: a model field that refers to
// entries of another package's model (a ModelRelationField; the contract
// test's write cycle creates such entries first), and an API action
// declared in another package's controller (the controller extends it).
// The IR does not record a controller's parent class itself, so a
// controller that extends another package's controller without
// inheriting any action from it is not seen. Needs on
// the core are left out: the core is on every VM. What cannot be decided
// is returned as notes, never guessed: a plugin without a catalog entry,
// a relation to a model class the IR does not have.
func Dependencies(rel *ir.Release, catalog []pkgrepo.Package, plugins []string) (map[string]map[string]string, []string) {
	needs := map[string]map[string]string{}
	var notes []string
	add := func(from, to, why string) {
		if from == to || to == Core {
			return
		}
		if needs[from] == nil {
			needs[from] = map[string]string{}
		}
		if _, ok := needs[from][to]; !ok {
			needs[from][to] = why
		}
	}
	byName := map[string]pkgrepo.Package{}
	for _, p := range catalog {
		byName[p.Name] = p
	}
	for _, pl := range plugins {
		p, ok := byName[pl]
		if !ok {
			notes = append(notes, fmt.Sprintf("%s: not in the package catalog, its package dependencies are unknown", pl))
			continue
		}
		for _, d := range slices.Sorted(maps.Keys(p.Deps)) {
			if strings.HasPrefix(d, "os-") {
				add(pl, d, "package dependency")
			}
		}
	}
	owner := map[string]string{}
	for _, m := range rel.Models {
		owner[model.ClassKey(m.Class)] = m.Package
	}
	for _, m := range rel.Models {
		var walk func(n *model.Node)
		walk = func(n *model.Node) {
			if n == nil {
				return
			}
			for _, r := range n.Relations {
				to, ok := owner[model.ClassKey(r.Source)]
				if !ok {
					notes = append(notes, fmt.Sprintf("%s: model %s refers to %s, a model the IR does not have (dependency unknown)", m.Package, m.Class, r.Source))
					continue
				}
				add(m.Package, to, "model relation "+m.Class+" -> "+r.Source)
			}
			for _, c := range n.Children {
				walk(c)
			}
		}
		walk(m.Root)
	}
	// an action a controller inherits from another package's API
	// controller (os-web-proxy-sso's service controller extends
	// os-squid's): without that package the class is missing and every
	// action of the controller fails
	routes := map[string]map[string]bool{} // module/controller -> packages
	for _, e := range rel.Endpoints {
		k := e.Module + "/" + e.Controller
		if routes[k] == nil {
			routes[k] = map[string]bool{}
		}
		routes[k][e.Package] = true
	}
	for _, e := range rel.Endpoints {
		module, name, ok := controller.Route(e.DeclaredIn)
		if !ok || (module == e.Module && name == e.Controller) {
			continue
		}
		owners := routes[module+"/"+name]
		if len(owners) == 0 {
			// an abstract controller: which package has it is not in the IR
			notes = append(notes, fmt.Sprintf("%s: controllers inherit actions from %s, which has no endpoint of its own (abstract); if another package has it, that dependency is unknown", e.Package, e.DeclaredIn))
			continue
		}
		for _, o := range slices.Sorted(maps.Keys(owners)) {
			add(e.Package, o, "controller "+e.Module+"/"+e.Controller+" inherits from "+e.DeclaredIn)
		}
	}
	slices.Sort(notes)
	return needs, slices.Compact(notes)
}

// Make splits a release. auto overrides the file's number of balanced
// groups when above 0.
func Make(f File, in Input, auto int) (*Plan, error) {
	if auto <= 0 {
		auto = f.AutoGroups
	}
	if auto <= 0 {
		auto = 3
	}
	plan := &Plan{Release: in.Release, All: slices.Sorted(slices.Values(in.Plugins))}
	isPlugin := map[string]bool{}
	for _, p := range in.Plugins {
		isPlugin[p] = true
	}
	// units: plugins (and the core) that must share a VM, by union-find
	parent := map[string]string{}
	var find func(string) string
	find = func(x string) string {
		if parent[x] == "" || parent[x] == x {
			parent[x] = x
			return x
		}
		parent[x] = find(parent[x])
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if rb < ra { // the smaller name is the root: deterministic
			ra, rb = rb, ra
		}
		parent[rb] = ra
	}
	nodes := append([]string{Core}, plan.All...)
	for _, n := range nodes {
		find(n)
	}
	because := map[string][]string{}
	for _, from := range slices.Sorted(maps.Keys(in.Needs)) {
		if from != Core && !isPlugin[from] {
			continue
		}
		for _, to := range slices.Sorted(maps.Keys(in.Needs[from])) {
			if !isPlugin[to] {
				// installed with it by the firmware, not tested on its own
				plan.Notes = append(plan.Notes, fmt.Sprintf("%s needs %s (%s), which is not a plugin under test here", from, to, in.Needs[from][to]))
				continue
			}
			union(from, to)
			because[from] = append(because[from], fmt.Sprintf("%s needs %s (%s)", from, to, in.Needs[from][to]))
		}
	}
	// sets from the groups file (the core may be one of them: its plugins
	// then go where the core is tested)
	for _, t := range f.Together {
		var have []string
		for _, p := range t.Packages {
			if isPlugin[p] || p == Core {
				have = append(have, p)
			}
		}
		for _, p := range have[min(1, len(have)):] {
			union(have[0], p)
			because[have[0]] = append(because[have[0]], fmt.Sprintf("%s and %s together (groups file: %s)", have[0], p, t.Why))
		}
	}
	members := map[string][]string{}
	for _, n := range nodes {
		members[find(n)] = append(members[find(n)], n)
	}
	for _, root := range slices.Sorted(maps.Keys(members)) {
		ms := members[root]
		if len(ms) < 2 {
			continue
		}
		u := Unit{Packages: ms}
		for _, m := range ms {
			u.Because = append(u.Because, because[m]...)
		}
		plan.Together = append(plan.Together, u)
	}

	weight, from, unknown := weights(nodes, in.Weights)
	plan.WeightsFrom = from
	if len(unknown) > 0 && from != "count" {
		plan.Notes = append(plan.Notes, "no measured time for "+strings.Join(unknown, ", ")+": the median of the measured ones is used")
	}

	// named groups take their plugins and, through the units, what those
	// need; a unit spread over two named groups cannot be split
	taken := map[string]int{} // unit root -> group index
	for i, g := range f.Groups {
		grp := Group{Name: g.Name, Explicit: true}
		roots := []string{}
		if g.Core {
			roots = append(roots, find(Core))
		}
		for _, p := range g.Packages {
			if !isPlugin[p] {
				plan.Notes = append(plan.Notes, fmt.Sprintf("group %s names %s, which is no plugin with an API in %s", g.Name, p, in.Release))
				continue
			}
			roots = append(roots, find(p))
		}
		for _, r := range roots {
			if j, ok := taken[r]; ok && j != i {
				return nil, fmt.Errorf("%s: groups %s and %s both need %s (they must share a VM)",
					in.Release, f.Groups[j].Name, g.Name, strings.Join(members[r], ", "))
			}
			taken[r] = i
		}
		plan.Groups = append(plan.Groups, grp)
	}
	// the rest: units by weight, heaviest first, each into the lightest
	// balanced group (ties: the lower number); names break weight ties
	var rest []string
	for _, root := range slices.Sorted(maps.Keys(members)) {
		if _, ok := taken[root]; !ok {
			rest = append(rest, root)
		}
	}
	unitWeight := func(root string) float64 {
		w := 0.0
		for _, m := range members[root] {
			w += weight[m]
		}
		return w
	}
	sort.SliceStable(rest, func(i, j int) bool { return unitWeight(rest[i]) > unitWeight(rest[j]) })
	first := len(plan.Groups)
	if len(rest) > 0 {
		for k := 1; k <= auto && k <= len(rest); k++ {
			plan.Groups = append(plan.Groups, Group{Name: fmt.Sprintf("auto-%d", k)})
		}
	}
	load := make([]float64, len(plan.Groups))
	for _, root := range slices.Sorted(maps.Keys(taken)) {
		load[taken[root]] += unitWeight(root)
	}
	for _, root := range rest {
		best := first
		for i := first + 1; i < len(plan.Groups); i++ {
			if load[i] < load[best] {
				best = i
			}
		}
		taken[root] = best
		load[best] += unitWeight(root)
	}
	for root, i := range taken {
		for _, m := range members[root] {
			if m == Core {
				plan.Groups[i].Core = true
			} else {
				plan.Groups[i].Plugins = append(plan.Groups[i].Plugins, m)
			}
		}
	}
	var keep []Group
	for i, g := range plan.Groups {
		if len(g.Plugins) == 0 && !g.Core {
			// a named group none of whose plugins this release has
			plan.Notes = append(plan.Notes, fmt.Sprintf("group %s has nothing to test in %s and is left out", g.Name, in.Release))
			continue
		}
		slices.Sort(g.Plugins)
		if g.Plugins == nil {
			g.Plugins = []string{}
		}
		g.Weight = load[i]
		keep = append(keep, g)
	}
	plan.Groups = keep
	slices.Sort(plan.Notes)
	return plan, nil
}

// weights: the weight of every node. With no measurement at all, every
// package counts 1; otherwise a package without one takes the median of
// the measured ones.
func weights(nodes []string, measured map[string]float64) (map[string]float64, string, []string) {
	w := map[string]float64{}
	var known []float64
	var unknown []string
	for _, n := range nodes {
		if v, ok := measured[n]; ok && v > 0 {
			w[n] = v
			known = append(known, v)
		} else {
			unknown = append(unknown, n)
		}
	}
	if len(known) == 0 {
		for _, n := range nodes {
			w[n] = 1
		}
		return w, "count", unknown
	}
	slices.Sort(known)
	median := known[len(known)/2]
	if len(known)%2 == 0 {
		median = (known[len(known)/2-1] + known[len(known)/2]) / 2
	}
	for _, n := range unknown {
		w[n] = median
	}
	if len(unknown) == 0 {
		return w, "measured", nil
	}
	return w, "partly measured", unknown
}

// Run is the measurement of one group's VM in a run, as tools/parallel.sh
// records it: the seconds each plugin's installation took and the seconds
// of the group's tests.
type Run struct {
	Core    bool               `json:"core"`
	Plugins []string           `json:"plugins"`
	Install map[string]float64 `json:"install"`
	Test    float64            `json:"test"`
}

// Weights estimates each package's seconds from the runs of the last
// split: its own installation time plus its share of its group's test
// time, by the number of endpoints it has in the IR (the tests go through
// endpoints). The core has no installation time.
func Weights(rel *ir.Release, runs []Run) map[string]float64 {
	eps := map[string]float64{}
	for _, e := range rel.Endpoints {
		eps[e.Package]++
	}
	out := map[string]float64{}
	for _, r := range runs {
		pk := slices.Clone(r.Plugins)
		if r.Core {
			pk = append(pk, Core)
		}
		total := 0.0
		for _, p := range pk {
			total += eps[p]
		}
		for _, p := range pk {
			share := 0.0
			if total > 0 {
				share = r.Test * eps[p] / total
			}
			out[p] = r.Install[p] + share
		}
	}
	return out
}

// Plugins returns the plugins of a release that have API endpoints (what
// a test VM installs), sorted.
func Plugins(rel *ir.Release) []string {
	set := map[string]bool{}
	for _, e := range rel.Endpoints {
		if e.Package != Core && e.Package != "" {
			set[e.Package] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}
