// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package pipeline

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/fragment"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/openapi"
	"github.com/pipiobjo/opnsense-api-spec/internal/pkgrepo"
	"github.com/pipiobjo/opnsense-api-spec/internal/schemacheck"
	"github.com/pipiobjo/opnsense-api-spec/internal/verify"
)

// Layout below the dist root (see docs/concepts.md, section 5.3):
//
//	fragments/common/<hash>.openapi.json        shared schemas
//	fragments/<package>/<hash>.{ir,openapi}.json one fragment, written once
//	<release>/compositions/<id>.json            every catalog state seen
//	<release>/composition.json                  the current one
//	<release>/opnsense-<release>.*              release view (current state)
//	index.json                                  compositions and fragment availability

func writeIfAbsent(path string, v any) error {
	if _, err := os.Stat(path); err == nil {
		return nil // content-addressed: same name, same content
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return WriteJSON(path, v)
}

func shortHash(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%x", sum)[:16], nil
}

// statusOf derives provisional/final from the releases on the mirror.
func statusOf(all []pkgrepo.Release, r pkgrepo.Release) fragment.Status {
	var same []string
	for _, x := range all {
		if x.Series == r.Series {
			same = append(same, x.Version)
		}
	}
	return fragment.StatusFor(r.Version, same, pkgrepo.CompareVersions)
}

// writeFragments stores the fragments of rel and the common document, and
// writes the composition into the release directory.
func writeFragments(rel *ir.Release, dist, releaseDir string, status fragment.Status) (*fragment.Composition, error) {
	common := openapi.RenderCommon(ir.GeneratorVersion)
	commonHash, err := shortHash(common)
	if err != nil {
		return nil, err
	}
	if err := writeIfAbsent(filepath.Join(dist, "fragments", "common", commonHash+".openapi.json"), common); err != nil {
		return nil, err
	}
	commonURL := "file:///fragments/common/" + commonHash + ".openapi.json"
	frags := fragment.Split(rel)
	for _, f := range frags {
		h := f.Hash()
		dir := filepath.Join(dist, "fragments", f.Package)
		sub := *rel
		sub.Models, sub.Endpoints = f.Models, f.Endpoints
		// the IR fragment is content-addressed and never changes ...
		if err := writeIfAbsent(filepath.Join(dir, h+".ir.json"), f); err != nil {
			return nil, err
		}
		// ... its OpenAPI rendering follows the current generator
		// (x-generator), so it is written on every build
		doc, _ := openapi.RenderFragment(&sub, f.Package, h, "../common/"+commonHash+".openapi.json")
		url := "file:///fragments/" + f.Package + "/" + h + ".openapi.json"
		if v := schemacheck.ValidateDocumentAt(doc, url, map[string]any{commonURL: common}); len(v) > 0 {
			return nil, fmt.Errorf("fragment %s %s is not a valid OpenAPI 3.1 document:\n  %s", f.Package, h, strings.Join(v, "\n  "))
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if err := WriteJSON(filepath.Join(dir, h+".openapi.json"), doc); err != nil {
			return nil, err
		}
	}
	comp := fragment.Compose(rel, frags, status)
	if err := os.MkdirAll(filepath.Join(releaseDir, "compositions"), 0o755); err != nil {
		return nil, err
	}
	if err := WriteJSON(filepath.Join(releaseDir, "compositions", comp.ID()+".json"), comp); err != nil {
		return nil, err
	}
	return comp, WriteJSON(filepath.Join(releaseDir, "composition.json"), comp)
}

// ReadComposition loads a composition file.
func ReadComposition(path string) (*fragment.Composition, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c fragment.Composition
	return &c, json.Unmarshal(raw, &c)
}

// currentCatalog returns the SHA-256 of the signed packagesite.yaml as it is
// on the mirror now (always downloaded, unless offline).
func (p *Pipeline) currentCatalog(r pkgrepo.Release) (string, string, error) {
	cat, err := p.Client.FetchCatalog(r)
	if err != nil {
		return "", "", err
	}
	if _, err := pkgrepo.VerifyCatalog(cat, p.Keys, true); err != nil {
		return "", "", err
	}
	core, _ := cat.Lookup("opnsense")
	sum := sha256.Sum256(cat.YAML)
	return fmt.Sprintf("%x", sum), core.Version, nil
}

// Index describes everything in dist/.
type Index struct {
	Generator string                                  `json:"generator"`
	Releases  []IndexRelease                          `json:"releases"`
	Fragments map[string]map[string]*IndexFragmentUse `json:"fragments"` // package -> hash -> use
}

// IndexRelease is one release with all catalog states seen.
type IndexRelease struct {
	Release      string            `json:"release"`
	Series       string            `json:"series"`
	Current      string            `json:"current"` // composition id
	Status       fragment.Status   `json:"status"`
	Compositions []string          `json:"compositions"`
	Files        map[string]string `json:"files"` // release view file -> sha256
	// Verified is the last contract test run; NeedsVerification is true
	// when the current composition has not been contract-tested (new
	// release, hotfix, plugin update or new generator).
	Verified          *IndexVerification `json:"verified,omitempty"`
	NeedsVerification bool               `json:"needs_verification"`
}

// IndexVerification summarizes a contract test run.
type IndexVerification struct {
	Composition string         `json:"composition"`
	Instance    string         `json:"instance"`
	Date        string         `json:"date"`
	Generator   string         `json:"generator"`
	Results     map[string]int `json:"results"` // check/status -> count
	// Failed counts mismatches and errors outside known gaps.
	Failed int `json:"failed"`
}

// IndexFragmentUse tells where a fragment occurs.
type IndexFragmentUse struct {
	Versions     []string `json:"versions"`
	Compositions []string `json:"compositions"`
}

func addUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// BuildIndex scans dist/<release>/compositions and writes dist/index.json.
func BuildIndex(dist string) error {
	idx := Index{Generator: ir.GeneratorVersion, Fragments: map[string]map[string]*IndexFragmentUse{}}
	dirs, err := os.ReadDir(dist)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		cur, err := ReadComposition(filepath.Join(dist, d.Name(), "composition.json"))
		if err != nil {
			continue // not a release directory
		}
		ent := IndexRelease{Release: cur.Release, Series: cur.Series, Current: cur.ID(), Status: cur.Status, Files: map[string]string{}}
		comps, _ := filepath.Glob(filepath.Join(dist, d.Name(), "compositions", "*.json"))
		sort.Strings(comps)
		for _, p := range comps {
			c, err := ReadComposition(p)
			if err != nil {
				return err
			}
			ent.Compositions = append(ent.Compositions, c.ID())
			for _, f := range c.Fragments {
				if idx.Fragments[f.Package] == nil {
					idx.Fragments[f.Package] = map[string]*IndexFragmentUse{}
				}
				u := idx.Fragments[f.Package][f.Hash]
				if u == nil {
					u = &IndexFragmentUse{}
					idx.Fragments[f.Package][f.Hash] = u
				}
				u.Versions = addUnique(u.Versions, f.Version)
				u.Compositions = addUnique(u.Compositions, c.ID())
			}
		}
		files, _ := os.ReadDir(filepath.Join(dist, d.Name()))
		for _, f := range files {
			if f.IsDir() || !strings.HasPrefix(f.Name(), "opnsense-") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dist, d.Name(), f.Name()))
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			ent.Files[f.Name()] = fmt.Sprintf("%x", sum)
		}
		if raw, err := os.ReadFile(filepath.Join(dist, d.Name(), "opnsense-"+cur.Release+".verified.json")); err == nil {
			var rep verify.Report
			if err := json.Unmarshal(raw, &rep); err == nil {
				v := &IndexVerification{Composition: rep.Composition, Instance: rep.Instance, Date: rep.Date,
					Generator: rep.Generator, Results: map[string]int{}}
				for _, r := range rep.Results {
					v.Results[r.Check+"/"+r.Status]++
					if r.Status == "mismatch" || r.Status == "error" {
						v.Failed++
					}
				}
				ent.Verified = v
			}
		}
		ent.NeedsVerification = ent.Verified == nil || ent.Verified.Composition != ent.Current ||
			ent.Verified.Generator != ir.GeneratorVersion
		idx.Releases = append(idx.Releases, ent)
	}
	sort.Slice(idx.Releases, func(i, j int) bool {
		return pkgrepo.CompareVersions(idx.Releases[i].Release, idx.Releases[j].Release) < 0
	})
	for _, byHash := range idx.Fragments {
		for _, u := range byHash {
			sort.Strings(u.Versions)
			sort.Strings(u.Compositions)
		}
	}
	if err := pruneFragments(dist, idx.Fragments); err != nil {
		return err
	}
	return WriteJSON(filepath.Join(dist, "index.json"), idx)
}

// pruneFragments deletes fragment files no composition refers to (left
// over from older generator versions). Common documents are kept.
func pruneFragments(dist string, used map[string]map[string]*IndexFragmentUse) error {
	files, _ := filepath.Glob(filepath.Join(dist, "fragments", "*", "*.json"))
	for _, f := range files {
		pkg := filepath.Base(filepath.Dir(f))
		if pkg == "common" {
			continue
		}
		hash := strings.SplitN(filepath.Base(f), ".", 2)[0]
		if used[pkg][hash] == nil {
			if err := os.Remove(f); err != nil {
				return err
			}
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(dist, "fragments", "*"))
	for _, d := range dirs {
		if rest, _ := os.ReadDir(d); len(rest) == 0 {
			_ = os.Remove(d)
		}
	}
	return nil
}
