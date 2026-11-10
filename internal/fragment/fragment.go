// Package fragment splits the IR of a release into per-package fragments
// identified by their API content, and describes compositions (the
// fragments of one catalog state). See docs/adr/0003.
package fragment

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// Fragment is the derived API of one package: its models and endpoints.
type Fragment struct {
	Package   string         `json:"package"`
	Models    []*model.Model `json:"models"`
	Endpoints []*ir.Endpoint `json:"endpoints"`
}

// Split groups a release's models and endpoints by package. Every model and
// endpoint belongs to exactly one fragment (no endpoint uses a model of
// another package; verified on 26.7.8). Fragments are sorted by package.
func Split(rel *ir.Release) []*Fragment {
	by := map[string]*Fragment{}
	get := func(pkg string) *Fragment {
		f := by[pkg]
		if f == nil {
			f = &Fragment{Package: pkg, Models: []*model.Model{}, Endpoints: []*ir.Endpoint{}}
			by[pkg] = f
		}
		return f
	}
	for _, m := range rel.Models {
		f := get(m.Package)
		f.Models = append(f.Models, m)
	}
	for _, e := range rel.Endpoints {
		f := get(e.Package)
		f.Endpoints = append(f.Endpoints, e)
	}
	out := make([]*Fragment, 0, len(by))
	for _, f := range by {
		sort.Slice(f.Models, func(i, j int) bool { return f.Models[i].Class < f.Models[j].Class })
		sort.Slice(f.Endpoints, func(i, j int) bool { return f.Endpoints[i].Path < f.Endpoints[j].Path })
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package < out[j].Package })
	return out
}

// Hash identifies the fragment by its API content. Provenance that can vary
// for the same API is excluded: alternative packages (they depend on which
// variants a catalog contains). Package versions and checksums are not part
// of the IR fragment at all. The result is the first 16 hex digits of the
// SHA-256 over the canonical JSON.
func (f *Fragment) Hash() string {
	eps := make([]ir.Endpoint, len(f.Endpoints))
	for i, e := range f.Endpoints {
		eps[i] = *e
		eps[i].AlsoIn = nil
	}
	canon := struct {
		Package   string         `json:"package"`
		Models    []*model.Model `json:"models"`
		Endpoints []ir.Endpoint  `json:"endpoints"`
	}{f.Package, f.Models, eps}
	sum := sha256.Sum256(mustCanonical(canon))
	return fmt.Sprintf("%x", sum)[:16]
}

// mustCanonical encodes deterministically: encoding/json sorts map keys,
// slices are sorted by Split and by the IR builders.
func mustCanonical(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err) // only plain data types
	}
	return buf.Bytes()
}

// Status of a composition.
type Status string

const (
	// Provisional: the snapshot is still live (newest release of its
	// series) and may change under the same release number.
	Provisional Status = "provisional"
	// Final: a newer patch release exists; the snapshot no longer changes.
	Final Status = "final"
)

// Ref points from a composition to a fragment.
type Ref struct {
	Package string   `json:"package"`
	Version string   `json:"version"`          // package version in this catalog
	Commit  string   `json:"commit,omitempty"` // build commit from the catalog
	Hash    string   `json:"hash"`             // fragment identity
	AlsoIn  []string `json:"also_in,omitempty"`
}

// Composition is the set of fragments of one catalog state.
type Composition struct {
	Release       string `json:"release"`      // 26.7.8
	CoreVersion   string `json:"core_version"` // 26.7.8_2
	Series        string `json:"series"`
	ABI           string `json:"abi"`
	CatalogSHA256 string `json:"catalog_sha256"`
	SignedBy      string `json:"catalog_signed_by"`
	FrameworkHash string `json:"framework_hash,omitempty"`
	Status        Status `json:"status"`
	Generator     string `json:"generator"`
	Fragments     []Ref  `json:"fragments"`
}

// ID is the stable identifier of a composition: core version plus the
// first 12 hex digits of the catalog hash.
func (c *Composition) ID() string {
	return c.CoreVersion + "~" + c.CatalogSHA256[:12]
}

// Compose builds the composition of a release IR.
func Compose(rel *ir.Release, frags []*Fragment, status Status) *Composition {
	pk := map[string]ir.Package{}
	for _, p := range rel.Packages {
		pk[p.Name] = p
	}
	c := &Composition{
		Release: rel.Version, CoreVersion: rel.CoreVersion, Series: rel.Series, ABI: rel.ABI,
		CatalogSHA256: rel.CatalogSHA256, SignedBy: rel.SignedBy, Status: status, FrameworkHash: rel.FrameworkHash,
		Generator: rel.Generator.Version, Fragments: []Ref{},
	}
	for _, f := range frags {
		ref := Ref{Package: f.Package, Hash: f.Hash(), Version: pk[f.Package].Version, Commit: pk[f.Package].Commit}
		for _, e := range f.Endpoints {
			if len(e.AlsoIn) > 0 {
				ref.AlsoIn = e.AlsoIn
				break
			}
		}
		c.Fragments = append(c.Fragments, ref)
	}
	return c
}

// StatusFor returns Final if sameSeries contains a release newer than
// release (cmp as pkgrepo.CompareVersions), Provisional otherwise.
func StatusFor(release string, sameSeries []string, cmp func(a, b string) int) Status {
	for _, v := range sameSeries {
		if cmp(v, release) > 0 {
			return Final
		}
	}
	return Provisional
}
