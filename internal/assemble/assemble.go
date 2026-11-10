// Package assemble builds the IR of a release from extracted MVC sources and
// their package attribution. It is shared by the CLI and by the golden and
// determinism tests, so that both derive the IR the same way.
package assemble

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/pipiobjo/opnsense-api-spec/internal/controller"
	"github.com/pipiobjo/opnsense-api-spec/internal/ir"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
	"github.com/pipiobjo/opnsense-api-spec/internal/quality"
)

// OwnersFile is written next to the extracted sources by fetch.
const OwnersFile = ".owners.json"

// Owners maps files (relative to the app dir) to the package that ships
// them, plus alternative packages shipping an identical file and files with
// conflicting content.
type Owners struct {
	Owner     map[string]string   `json:"owner"`
	AlsoIn    map[string][]string `json:"also_in,omitempty"`
	Conflicts []string            `json:"conflicts,omitempty"`
}

// WriteOwners stores the attribution in appDir.
func WriteOwners(appDir string, o *Owners) error {
	b, err := json.MarshalIndent(o, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(appDir, OwnersFile), b, 0o644)
}

// ReadOwners loads the attribution written by fetch.
func ReadOwners(appDir string) (*Owners, error) {
	b, err := os.ReadFile(filepath.Join(appDir, OwnersFile))
	if err != nil {
		return nil, err
	}
	var o Owners
	return &o, json.Unmarshal(b, &o)
}

// Release derives models and endpoints from appDir. meta carries the
// release metadata (version, packages, catalog, ...); its Models,
// Endpoints, Conflicts and Generator fields are filled in.
func Release(appDir string, owners *Owners, meta ir.Release) (*ir.Release, error) {
	models, _, err := model.LoadAll(appDir)
	if err != nil {
		return nil, err
	}
	ctls, err := controller.LoadAll(appDir)
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		m.Package = owners.Owner[m.File]
	}
	for _, c := range ctls {
		c.Package = owners.Owner[c.File]
		c.AlsoIn = owners.AlsoIn[c.File]
	}
	rel := meta
	rel.Models = models
	rel.Conflicts = append([]string(nil), owners.Conflicts...)
	sort.Strings(rel.Conflicts)
	rel.Generator.Name, rel.Generator.Version = ir.GeneratorName, ir.GeneratorVersion
	rel.Endpoints = ir.Build(models, ctls)
	if rel.Framework, rel.FrameworkHash, err = quality.Fingerprint(appDir); err != nil {
		return nil, err
	}
	return &rel, nil
}
