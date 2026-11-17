// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package model parses OPNsense MVC model XML files and field type classes
// into a language-neutral description.
package model

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"github.com/pipiobjo/opnsense-api-spec/internal/php"
)

// Kind is the structural role of a node.
type Kind string

const (
	KindContainer Kind = "container" // plain nesting element
	KindArray     Kind = "array"     // ArrayField: entries keyed by UUID
	KindField     Kind = "field"     // leaf value
)

// Shape is how a leaf field is represented on read (GET).
type Shape string

const (
	ShapeString  Shape = "string"  // plain string value
	ShapeSelect  Shape = "select"  // {key: {value, selected}} map (list fields)
	ShapeUnknown Shape = "unknown" // field type could not be resolved
)

// Option is one allowed value of a list field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label,omitempty"`
	Group string `json:"group,omitempty"`
}

// Node is one element of a model tree.
type Node struct {
	Name     string            `json:"name"`
	Kind     Kind              `json:"kind"`
	Type     string            `json:"type,omitempty"`      // as written in the XML, e.g. "OptionField", ".\\KeaPoolsField"
	BaseType string            `json:"base_type,omitempty"` // resolved core base class, e.g. "BaseListField"
	Shape    Shape             `json:"read_shape,omitempty"`
	Multiple bool              `json:"multiple,omitempty"`
	Required bool              `json:"required,omitempty"`
	Volatile bool              `json:"volatile,omitempty"`
	Default  *string           `json:"default,omitempty"`
	Options  []Option          `json:"options,omitempty"`
	Props    map[string]string `json:"props,omitempty"` // other scalar settings (Mask, MinimumValue, Separator, ...)
	// Relations: the entry lists a ModelRelationField refers to (its keys
	// are UUIDs of entries there).
	Relations []Relation `json:"relations,omitempty"`
	Children  []*Node    `json:"children,omitempty"`
	Gaps      []gap.Gap  `json:"gaps,omitempty"`
}

// Relation is one target of a ModelRelationField: model class (dotted, as
// in the XML: "OPNsense.Bind.Domain") and the entry list path within it.
type Relation struct {
	Source string `json:"source"`
	Items  string `json:"items"`
}

// Model is one parsed model XML.
type Model struct {
	Class       string `json:"class"` // e.g. "OPNsense\\Kea\\KeaDhcpv4"
	File        string `json:"file"`  // relative to the mvc app dir
	Package     string `json:"package,omitempty"`
	Mount       string `json:"mount"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Root        *Node  `json:"root"`
	// Gaps of the whole model: custom-save when its class overrides
	// serializeToConfig (bind domains, os-frr general settings, ...)
	Gaps []gap.Gap `json:"gaps,omitempty"`
}

// Find returns the node at a dotted path below the root ("dnsbl.blocklist").
func (m *Model) Find(path string) *Node {
	n := m.Root
	for _, step := range strings.Split(path, ".") {
		var next *Node
		for _, c := range n.Children {
			if c.Name == step {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		n = next
	}
	return n
}

// TypeIndex resolves field type names to their core base class.
type TypeIndex struct {
	parent map[string]string // lower-case class -> lower-case parent
	names  map[string]string // lower-case class -> class as declared
	// read-shape overrides found in getNodeData/getNodeOptions:
	// "string", "string-unless-multiple" or "custom"
	readOverride map[string]string
	asList       map[string]bool // class default of $internalAsList
	// write-side overrides: class -> methods among setValue, toLegacy,
	// fromLegacy declared in the class itself
	writeOverride map[string]map[string]bool
	// ArrayField subclasses that create their entries as another class
	// (newContainerField): array class -> container class
	container map[string]string
}

var (
	returnStringRe = regexp.MustCompile(`return\s*\(string\)\s*\$this\s*;`)
	returnRe       = regexp.MustCompile(`\breturn\b`)
	nodeFnRe       = regexp.MustCompile(`function\s+(getNodeData|getNodeOptions)\s*\([^)]*\)[^{]*\{`)
	asListRe       = regexp.MustCompile(`\$internalAsList\s*=\s*(true|false)`)
	writeFnRe      = regexp.MustCompile(`function\s+(setValue|toLegacy|fromLegacy)\s*\(`)
	newContainerRe = regexp.MustCompile(`function\s+newContainerField\s*\([^)]*\)[^{]*\{[^}]*\bnew\s+([\\\w]+)\s*\(`)
	saveFnRe       = regexp.MustCompile(`function\s+serializeToConfig\s*\(`)
	initFnRe       = regexp.MustCompile(`function\s+init\s*\(`)
)

// the core defaults; overrides below them change the read shape
var defaultShapeClasses = map[string]bool{
	baseNS + "basefield": true, baseNS + "baselistfield": true, baseNS + "basesetfield": true,
}

// readOverrideOf classifies the getNodeData/getNodeOptions bodies of a class.
func readOverrideOf(src string) string {
	kind := ""
	for _, loc := range nodeFnRe.FindAllStringIndex(src, -1) {
		end := php.Match(src, loc[1]-1, '{', '}')
		if end < 0 {
			end = len(src) - 1
		}
		body := src[loc[1]-1 : end+1]
		// only a literal `return (string)$this;` proves a string; using
		// (string)$this elsewhere (e.g. inside explode) proves nothing
		strReturns := len(returnStringRe.FindAllStringIndex(body, -1))
		allReturns := len(returnRe.FindAllStringIndex(body, -1))
		switch {
		case strReturns > 0 && strings.Contains(body, "internalMultiSelect"):
			kind = "string-unless-multiple"
		case strReturns > 0 && strReturns == allReturns:
			if kind == "" {
				kind = "string"
			}
		default:
			if kind == "" {
				kind = "custom"
			}
		}
	}
	return kind
}

// BuildTypeIndex scans all FieldTypes/*.php below modelsDir.
func BuildTypeIndex(modelsDir string) (*TypeIndex, error) {
	idx := &TypeIndex{parent: map[string]string{}, names: map[string]string{},
		readOverride: map[string]string{}, asList: map[string]bool{},
		writeOverride: map[string]map[string]bool{}, container: map[string]string{}}
	err := filepath.WalkDir(modelsDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".php") || filepath.Base(filepath.Dir(p)) != "FieldTypes" {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		// comments never count, and every rule reads the body of its own
		// class (a file can hold several, e.g. NetworkInterfaceField.php)
		code := php.StripComments(string(src))
		h := php.ParseHeader(code)
		for _, c := range php.Classes(code, h) {
			key := strings.ToLower(c.Name)
			idx.names[key] = c.Name
			for _, w := range writeFnRe.FindAllStringSubmatch(c.Body, -1) {
				if idx.writeOverride[key] == nil {
					idx.writeOverride[key] = map[string]bool{}
				}
				idx.writeOverride[key][w[1]] = true
			}
			if m := newContainerRe.FindStringSubmatch(c.Body); m != nil {
				idx.container[key] = strings.ToLower(h.Qualify(m[1]))
			}
			if c.Extends == "" {
				continue // root class (BaseField)
			}
			idx.parent[key] = strings.ToLower(c.Extends)
			if ov := readOverrideOf(c.Body); ov != "" && !defaultShapeClasses[key] {
				idx.readOverride[key] = ov
			}
			if m := asListRe.FindStringSubmatch(c.Body); m != nil {
				idx.asList[key] = m[1] == "true"
			}
		}
		return nil
	})
	return idx, err
}

const baseNS = `opnsense\base\fieldtypes\`

// Resolve maps a type attribute (relative to the model's namespace) to the
// chain of classes up to a core base class.
func (t *TypeIndex) Resolve(typeAttr, modelNS string) []string {
	var cls string
	switch {
	case strings.HasPrefix(typeAttr, `.\`):
		cls = modelNS + `\FieldTypes\` + typeAttr[2:]
	case strings.HasPrefix(typeAttr, `\`):
		cls = typeAttr[1:]
	case strings.Contains(typeAttr, `\`):
		cls = typeAttr
	default:
		cls = `OPNsense\Base\FieldTypes\` + typeAttr
	}
	key := strings.ToLower(cls)
	chain := []string{key}
	for i := 0; i < 20; i++ {
		p, ok := t.parent[key]
		if !ok {
			break
		}
		chain = append(chain, p)
		key = p
	}
	return chain
}

// writeGap: the field's class chain converts values on write (setValue
// below BaseField). Field types the client already treats specially are
// included; the gap only says that the stored value may differ.
func (t *TypeIndex) writeGap(chain []string) *gap.Gap {
	for _, c := range chain {
		if c == baseNS+"basefield" {
			break
		}
		if t.writeOverride[c]["setValue"] {
			g := gap.New(gap.CustomWrite, "setValue in %s converts or normalizes the value; the stored value may differ from the request", t.baseName(c))
			return &g
		}
	}
	return nil
}

// saveGap: an ArrayField whose entries are stored through their own
// conversion (toLegacy/fromLegacy in the array class or the container class
// it creates), as the interface assignments are.
func (t *TypeIndex) saveGap(chain []string) *gap.Gap {
	for _, c := range chain {
		if c == baseNS+"arrayfield" {
			break
		}
		for _, cls := range []string{c, t.container[c]} {
			if ov := t.writeOverride[cls]; ov["toLegacy"] || ov["fromLegacy"] {
				g := gap.New(gap.CustomSave, "entries are stored through toLegacy/fromLegacy in %s; values may be changed or dropped depending on other fields", t.baseName(cls))
				return &g
			}
		}
	}
	return nil
}

func (t *TypeIndex) baseName(key string) string {
	if n, ok := t.names[key]; ok {
		key = n
	}
	return key[strings.LastIndex(key, `\`)+1:]
}

// readShape applies class overrides (getNodeData/getNodeOptions) and the
// AsList default to the shape derived from the base class.
func (t *TypeIndex) readShape(chain []string, base string, shape Shape, multiple bool, xmlAsList string) (Shape, *gap.Gap) {
	for _, c := range chain {
		if defaultShapeClasses[c] {
			break
		}
		switch t.readOverride[c] {
		case "string":
			return ShapeString, nil
		case "string-unless-multiple":
			if multiple {
				return ShapeSelect, nil
			}
			return ShapeString, nil
		case "custom":
			g := gap.New(gap.CustomReadShape, "custom getNodeData/getNodeOptions in %s; read shape may differ", t.baseName(c))
			return shape, &g
		}
	}
	if base == "BaseSetField" {
		asList := false
		for _, c := range chain {
			if v, ok := t.asList[c]; ok {
				asList = v
				break
			}
		}
		switch strings.ToUpper(xmlAsList) {
		case "Y":
			asList = true
		case "N":
			asList = false
		}
		if asList {
			return ShapeSelect, nil
		}
		return ShapeString, nil
	}
	return shape, nil
}

// classify picks base type and read shape from a resolved chain.
func (t *TypeIndex) classify(chain []string) (base string, shape Shape, isArray bool, known bool) {
	last := chain[len(chain)-1]
	known = strings.HasPrefix(last, baseNS) && strings.EqualFold(t.baseName(last), "basefield")
	for _, c := range chain {
		switch c {
		case baseNS + "arrayfield":
			return "ArrayField", "", true, true
		case baseNS + "baselistfield":
			return "BaseListField", ShapeSelect, false, true
		case baseNS + "basesetfield":
			return "BaseSetField", ShapeString, false, true
		}
	}
	if known {
		// first core class in the chain is the most specific known type
		for _, c := range chain {
			if strings.HasPrefix(c, baseNS) {
				return t.baseName(c), ShapeString, false, true
			}
		}
	}
	return "", ShapeUnknown, false, false
}

// xml tree with attributes and children
type xnode struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []xnode    `xml:",any"`
}

func (x *xnode) attr(name string) string {
	for _, a := range x.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func (x *xnode) child(name string) *xnode {
	for i := range x.Children {
		if x.Children[i].XMLName.Local == name {
			return &x.Children[i]
		}
	}
	return nil
}

// leaf-level settings elements of a field (not child nodes)
var fieldSettings = map[string]bool{}

func init() {
	for _, s := range []string{"Default", "Required", "OptionValues", "Mask", "ValidationMessage",
		"Multiple", "MinimumValue", "MaximumValue", "BlankDesc", "Constraints", "AsList",
		"FieldSeparator", "Model", "Source", "Items", "NetMaskAllowed", "NetMaskRequired",
		"AddressFamily", "WildcardEnabled", "IsDNSName", "IpAllowed", "HostWildcardAllowed",
		"FqdnWildcardAllowed", "ZoneRootAllowed", "Strict", "EnableWellKnown", "Lowercase",
		"ChangeCase", "Type", "Filters", "OptionsFromConfig", "ConfigdPopulateAct",
		"ConfigdPopulateTTL", "SourceField", "SourceFile", "SelectAll", "AllowDynamic",
		"AddParentInterfaces", "Sorted", "AllowNonAscii", "LocalOnly", "Protocols",
		"Initial", "ValidationMessages", "AllowRange", "Separator", "EnableRanges",
		"PrefixLengthMin", "PrefixLengthMax", "Strict"} {
		fieldSettings[s] = true
	}
}

// Parse reads one model XML file. relFile is used for provenance only.
func Parse(r io.Reader, relFile string, types *TypeIndex) (*Model, error) {
	var root xnode
	if err := xml.NewDecoder(r).Decode(&root); err != nil {
		return nil, fmt.Errorf("%s: %w", relFile, err)
	}
	if root.XMLName.Local != "model" {
		return nil, nil // not a model (ACL, Menu, ...)
	}
	m := &Model{File: relFile}
	if c := root.child("mount"); c != nil {
		m.Mount = strings.TrimSpace(c.Text)
	}
	if c := root.child("version"); c != nil {
		m.Version = strings.TrimSpace(c.Text)
	}
	if c := root.child("description"); c != nil {
		m.Description = strings.TrimSpace(c.Text)
	}
	// models/OPNsense/Kea/KeaDhcpv4.xml -> OPNsense\Kea\KeaDhcpv4
	rel := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(relFile), "models/"), ".xml")
	m.Class = strings.ReplaceAll(rel, "/", `\`)
	ns := m.Class[:strings.LastIndex(m.Class, `\`)]
	m.Root = &Node{Name: "", Kind: KindContainer}
	if items := root.child("items"); items != nil {
		for i := range items.Children {
			m.Root.Children = append(m.Root.Children, convert(&items.Children[i], ns, types))
		}
	}
	return m, nil
}

// convertChildren adds the structural children of a container or array.
// The rule is structural, not list-based: an element with a type attribute
// is always a field; an element without type and without children is
// never a field or section but a setting of its parent (e.g. <Default> of
// an ArrayField). A setting that is not known is reported, so a new
// upstream keyword cannot silently become a field or vanish.
func convertChildren(n *Node, x *xnode, ns string, types *TypeIndex) {
	for i := range x.Children {
		c := &x.Children[i]
		name := c.XMLName.Local
		if c.attr("type") == "" && len(c.Children) == 0 {
			if !fieldSettings[name] {
				n.Gaps = append(n.Gaps, gap.New(gap.UnknownSetting, "element <%s> without type and children is not a known setting", name))
			}
			continue
		}
		if c.attr("type") == "" && fieldSettings[name] && n.Kind == KindArray {
			continue // structured setting of the array (e.g. OptionValues-like)
		}
		n.Children = append(n.Children, convert(c, ns, types))
	}
}

func convert(x *xnode, ns string, types *TypeIndex) *Node {
	n := &Node{Name: x.XMLName.Local}
	typ := x.attr("type")
	if x.attr("volatile") == "true" {
		n.Volatile = true
	}
	if typ == "" {
		n.Kind = KindContainer
		convertChildren(n, x, ns, types)
		return n
	}
	n.Type = typ
	chain := types.Resolve(typ, ns)
	base, shape, isArray, known := types.classify(chain)
	n.BaseType = base
	if isArray {
		n.Kind = KindArray
		if g := types.saveGap(chain); g != nil {
			n.Gaps = append(n.Gaps, *g)
		}
		convertChildren(n, x, ns, types)
		return n
	}
	if typ == "ContainerField" || base == "ContainerField" {
		n.Kind = KindContainer
		convertChildren(n, x, ns, types)
		return n
	}
	n.Kind = KindField
	n.Shape = shape
	if !known {
		n.Gaps = append(n.Gaps, gap.New(gap.FieldTypeUnresolved, "field type %s not resolved to a core base class", typ))
	}
	for i := range x.Children {
		c := &x.Children[i]
		name := c.XMLName.Local
		val := strings.TrimSpace(c.Text)
		switch name {
		case "Required":
			n.Required = strings.EqualFold(val, "Y")
		case "Multiple":
			n.Multiple = strings.EqualFold(val, "Y")
		case "Default":
			v := val
			n.Default = &v
		case "OptionValues":
			n.Options = options(c)
		case "AsList":
			setProp(n, name, val)
		case "Model":
			setProp(n, name, "(structured)")
			for j := range c.Children {
				t := &c.Children[j]
				src, items := t.child("source"), t.child("items")
				if src != nil && items != nil {
					n.Relations = append(n.Relations, Relation{Source: strings.TrimSpace(src.Text), Items: strings.TrimSpace(items.Text)})
				}
			}
		default:
			if len(c.Children) == 0 {
				setProp(n, name, val)
			} else {
				setProp(n, name, "(structured)")
			}
		}
	}
	if known {
		var g *gap.Gap
		n.Shape, g = types.readShape(chain, base, n.Shape, n.Multiple, n.Props["AsList"])
		if g != nil {
			n.Gaps = append(n.Gaps, *g)
		}
	}
	if known {
		if g := types.writeGap(chain); g != nil {
			n.Gaps = append(n.Gaps, *g)
		}
	}
	if base == "BaseListField" && n.Options == nil && n.Type == "OptionField" {
		n.Gaps = append(n.Gaps, gap.New(gap.OptionsDynamic, "OptionField without static OptionValues"))
	}
	return n
}

func setProp(n *Node, k, v string) {
	if n.Props == nil {
		n.Props = map[string]string{}
	}
	n.Props[k] = v
}

func options(x *xnode) []Option {
	var out []Option
	var walk func(x *xnode, group string)
	walk = func(x *xnode, group string) {
		for i := range x.Children {
			c := &x.Children[i]
			if len(c.Children) > 0 {
				g := c.attr("label")
				if g == "" {
					g = c.XMLName.Local
				}
				walk(c, g)
				continue
			}
			v := c.attr("value")
			if v == "" {
				v = c.XMLName.Local
			}
			out = append(out, Option{Value: v, Label: strings.TrimSpace(c.Text), Group: group})
		}
	}
	walk(x, "")
	return out
}

// LoadAll parses every model XML below appDir/models.
func LoadAll(appDir string) ([]*Model, *TypeIndex, error) {
	modelsDir := filepath.Join(appDir, "models")
	types, err := BuildTypeIndex(modelsDir)
	if err != nil {
		return nil, nil, err
	}
	var out []*Model
	err = filepath.WalkDir(modelsDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".xml") {
			return err
		}
		rel, _ := filepath.Rel(appDir, p)
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		m, err := Parse(f, rel, types)
		if err != nil {
			return err
		}
		if m != nil {
			// the model class lives next to its XML (Domain.xml, Domain.php)
			if raw, err := os.ReadFile(strings.TrimSuffix(p, ".xml") + ".php"); err == nil {
				src := php.StripComments(string(raw))
				if saveFnRe.MatchString(src) {
					m.Gaps = append(m.Gaps, gap.New(gap.CustomSave, "%s overrides serializeToConfig; saving runs own code (values may be changed, computed or written elsewhere)", m.Class))
				}
				if initFnRe.MatchString(src) {
					// e.g. OPNsense\Core\InitialSetup loads every value from
					// the rest of the configuration on each read
					m.Gaps = append(m.Gaps, gap.New(gap.CustomSave, "%s overrides init; values read may be set by its own code (loaded from elsewhere or derived), not the ones stored", m.Class))
				}
			}
			out = append(out, m)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out, types, err
}
