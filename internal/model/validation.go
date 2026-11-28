// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

package model

// How a field's class checks a non-empty value (ADR 0018). Every class of
// a field's chain that declares getValidators or isValidInput is matched
// against the code forms this file knows: a hash over the class's code and
// the code it relies on, comments and layout ignored. A known form adds
// the rules it enforces to the field's Limits; a class in any other form
// gives the gap validation-unknown, and only the firewall checks what that
// class adds. A setting in the model XML reaches a field only through a
// setter of its class (BaseModel calls set<Name> when the class has it);
// any other setting has no effect and is no claim.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
)

// Format is the form of value a field class accepts beyond options,
// integer ranges and patterns. docs/capabilities.md section "Formats"
// defines each kind exactly; consumers implement those definitions, and
// the field probes try them on the firewall.
type Format struct {
	Kind           string `json:"kind"`                      // one of the Format* kinds
	Family         string `json:"family,omitempty"`          // network, ip-port: "ipv4" or "ipv6"; empty: both
	Netmask        string `json:"netmask,omitempty"`         // network: "forbidden", "allowed" or "required"
	Any            bool   `json:"any,omitempty"`             // network: the whole value "any" is accepted
	Strict         bool   `json:"strict,omitempty"`          // network: no address bits set after the prefix length
	IP             bool   `json:"ip,omitempty"`              // hostname: an IP address is accepted
	HostWildcard   bool   `json:"host_wildcard,omitempty"`   // hostname: "*" is accepted
	DomainWildcard bool   `json:"domain_wildcard,omitempty"` // hostname: "*." before a name is accepted
	ZoneRoot       bool   `json:"zone_root,omitempty"`       // hostname: "@", and "@." before a name, are accepted
	DNSName        bool   `json:"dns_name,omitempty"`        // hostname: labels of any characters (a DNS name), not only host names
	PortOptional   bool   `json:"port_optional,omitempty"`   // ip-port: the address alone is accepted
	Hostname       bool   `json:"hostname,omitempty"`        // ip-port: a host name instead of an IPv4 address
	// port: Any (with Services) is "any"; Services the service names
	// accepted; Ranges "a-b"; Aliases: the names of port aliases on the
	// firewall (not known here); RangeSpaces: spaces around a range's
	// numbers are accepted
	Services    []string `json:"services,omitempty"`
	Ranges      bool     `json:"ranges,omitempty"`
	Aliases     bool     `json:"aliases,omitempty"`
	RangeSpaces bool     `json:"range_spaces,omitempty"`
}

// Format kinds
const (
	FormatNetwork  = "network"
	FormatHostname = "hostname"
	FormatMAC      = "mac"
	FormatIPPort   = "ip-port"
	FormatURL      = "url"
	FormatPort     = "port"
)

// Netmask values of a network format
const (
	NetmaskForbidden = "forbidden"
	NetmaskAllowed   = "allowed"
	NetmaskRequired  = "required"
)

// checker: a core class whose checks the data describes.
type checker struct {
	// the code a form is computed over: methods of the class itself (nil:
	// the whole class) and code it relies on ("class" or "class::method",
	// lower case)
	methods []string
	deps    []string
	// known forms: hash -> where it was reviewed (for the reader)
	forms map[string]string
	// apply adds what the class checks to l; an error: a setting of this
	// field the description cannot express
	apply func(s settings, l *Limits) error
	// ignore: code left out of the form because apply reads it itself
	// (PortField's list of service names)
	ignore *regexp.Regexp
}

const (
	validatorsNS = `opnsense\base\validators\`
	utilClass    = `opnsense\firewall\util`
)

// checkers by class. The forms are the hashes of the code reviewed for
// the releases named; tools/ci/formats.sh lists the forms of a release.
var checkers = map[string]*checker{
	baseNS + "basefield": {methods: []string{"getvalidators"}, forms: forms(
		"c8e4477f22a24032", "25.7", "9dfece09d8f64044", "26.1, 26.7"),
		apply: func(settings, *Limits) error { return nil }}, // required, Constraints: described elsewhere
	baseNS + "baselistfield": {methods: []string{"getvalidators"}, forms: forms(
		"cfd9dc555a0e2b2b", "25.7", "7494b7240fef7052", "26.1, 26.7"),
		apply: func(settings, *Limits) error { return nil }}, // options, multiple
	baseNS + "booleanfield": {forms: forms("dcd305cab28cdfff", "25.7-26.7"),
		apply: func(settings, *Limits) error { return nil }}, // 0 or 1
	baseNS + "integerfield": {deps: []string{validatorsNS + "minmaxvalidator", validatorsNS + "integervalidator"}, forms: forms(
		"35f35e037ccf7d85", "25.7-25.7.10", "193c38ebf7c9148d", "25.7.11", "0b2cb7ef5d05bf07", "26.1, 26.7"),
		apply: integerLimits},
	baseNS + "textfield": {deps: []string{validatorsNS + "regex"}, forms: forms(
		"0155ad4c810da1cf", "25.7-26.1.9", "1569d050d1102ab1", "26.1.10-26.7 (AllowSpaces, AllowNewlines, AllowSpecial)"),
		apply: textLimits},
	baseNS + "descriptionfield": {forms: forms(
		"a10863ba253e36c0", "25.7-26.1.10", "ac1243e30b74047c", "26.1.11-26.7 (no newlines or control characters by default)"),
		apply: classPattern},
	baseNS + "emailfield": {forms: forms(
		"3ff7cc8e818b7aef", notDescribed+"PHP's FILTER_VALIDATE_EMAIL (25.7-26.1.8)", "4c41aebc053b8860", "26.1.9-26.7"),
		apply: classPattern},
	baseNS + "networkfield": {deps: []string{baseNS + "basesetfield::iterateinput", utilClass + "::issubnetstrict", utilClass + "::issubnet"}, forms: forms(
		"fde8364893b9d1be", "25.7, 26.1", "745ef3e1267a4c3f", "26.7"),
		apply: networkFormat},
	baseNS + "hostnamefield": {deps: []string{baseNS + "basesetfield::iterateinput"}, forms: forms(
		"df6a44ded55114a0", "25.7-26.1.5", "e565b72a3ace9c6f", "26.1.6-26.7"),
		apply: hostnameFormat},
	baseNS + "macaddressfield": {deps: []string{baseNS + "basesetfield::iterateinput"}, forms: forms("659dabc5d8f851a5", "25.7-26.7"),
		apply: func(_ settings, l *Limits) error { l.Format = &Format{Kind: FormatMAC}; return nil }},
	baseNS + "ipportfield": {deps: []string{baseNS + "basesetfield::iterateinput", utilClass + "::isport", utilClass + "::getservbyname",
		utilClass + "::isipv4address", utilClass + "::isipv6address"}, forms: forms(
		"54bda2a23d15b6be", "25.7-26.7.5", "c7a39cf7cafc172c", "26.7.6-26.7 (isPort: ranges ordered; not used for one port)"),
		apply: ipPortFormat},
	baseNS + "urlfield": {deps: []string{validatorsNS + "url"}, forms: forms("db6b8e42e0ee5093", "25.7-26.7"),
		apply: func(_ settings, l *Limits) error { l.Format = &Format{Kind: FormatURL}; return nil }},
	baseNS + "csvlistfield": {deps: []string{baseNS + "basesetfield::iterateinput"}, forms: forms("ac2aebd80b2f70f9", "25.7-26.7"),
		apply: csvLimits},
	// the forms differ in the read side, the alias type test, the
	// getWellKnown helper and normalizedPort, none of them a check; the
	// service names, the ['any'] + union and the range spaces are read
	// from the code by portFormat
	baseNS + "portfield": {ignore: servicesRe, forms: forms(
		"73f8bfc219b0e763", "25.7-25.7.1", "b06ceecc8fd96b3a", "25.7.2-25.7.10", "579c2f0c8a6ef9e8", "25.7.11-26.1.1",
		"438600d2bca7b650", "26.1.2", "69591b4751f64a72", "26.1.3-26.1.11", "55c9176066679bf4", "26.7-26.7.2",
		"a4ddb2242e658f23", "26.7.3-26.7.5", "e53aa9fd2d46a18b", "26.7.6-26.7.7", "59af5176511492cc", "26.7.8-26.7"),
		apply: portFormat},
}

// notDescribed starts the note of a reviewed form whose checks the data
// cannot express
const notDescribed = "not described: "

// forms builds a form table from hash, note pairs.
func forms(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

// settings: what a field's model XML and its class chain say about one
// setting.
type settings struct {
	t     *TypeIndex
	chain []string
	n     *Node
	class string    // the class being applied
	used  *[]string // the XML settings read
}

// xml is the value of the XML setting name when the class has its setter
// (set<name>), else ok is false: the setting has no effect.
func (s settings) xml(name string) (string, bool) {
	// BaseModel calls "set" . <element name>, and PHP method names are
	// case-insensitive: NetmaskAllowed reaches setNetMaskAllowed
	for k, v := range s.n.Props {
		if strings.EqualFold(k, name) && s.t.hasSetter(s.chain, k) {
			*s.used = append(*s.used, k)
			return v, true
		}
	}
	return "", false
}

// flag: the class's setter stores trim(strtoupper(v)) == "Y"; without the
// setting, the default of property prop.
func (s settings) flag(name, prop string, def bool) bool {
	if v, ok := s.xml(name); ok {
		return strings.ToUpper(strings.TrimSpace(v)) == "Y"
	}
	if v, ok := s.t.prop(s.chain, prop); ok {
		return v == "true"
	}
	return def
}

// family: trim(strtolower(AddressFamily)); "" when not set.
func (s settings) family() string {
	v, _ := s.xml("AddressFamily")
	return strings.ToLower(strings.TrimSpace(v))
}

func integerLimits(s settings, l *Limits) error {
	// IntegerField::setMinimumValue takes only is_numeric values
	if v, ok := s.xml("MinimumValue"); ok {
		if i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			l.Minimum = &i
		}
	}
	if v, ok := s.xml("MaximumValue"); ok {
		if i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			l.Maximum = &i
		}
	}
	return nil
}

// textLimits: TextField's Mask (OPNsense\Base\Validators\Regex: the first
// match must be the whole value) and, from 26.1.10, the characters
// AllowSpaces, AllowNewlines and AllowSpecial forbid.
func textLimits(s settings, l *Limits) error {
	if mask, ok := s.xml("Mask"); ok && strings.TrimSpace(mask) != "" {
		p, ok := GoPattern(strings.TrimSpace(mask), false)
		if !ok {
			return fmt.Errorf("its Mask %s has no Go form", mask)
		}
		if err := l.addPattern(p); err != nil {
			return err
		}
	}
	var forbid string
	if s.t.hasSetter(s.chain, "AllowSpaces") && !s.flag("AllowSpaces", "internalAllowSpaces", true) {
		forbid += " \t"
	}
	if s.t.hasSetter(s.chain, "AllowNewlines") && !s.flag("AllowNewlines", "internalAllowNewlines", true) {
		forbid += "\n\r"
	}
	if s.t.hasSetter(s.chain, "AllowSpecial") && !s.flag("AllowSpecial", "internalAllowSpecial", true) {
		forbid += "\x00\v\f"
	}
	l.Forbidden = forbid
	return nil
}

// pregRe: a callback that refuses the value unless preg_match finds the
// literal pattern (DescriptionField, EmailField from 26.1.9)
var pregRe = regexp.MustCompile(`if\s*\(\s*!\s*preg_match\s*\(\s*('(?:[^'\\]|\\.)*')\s*,\s*\$data\s*\)\s*\)`)

// classPattern: the pattern of the class's own preg_match. preg_match
// accepts any match; for a pattern anchored at both ends that is a match of
// the whole value, apart from PCRE's "$" also matching before a final
// newline (unless the D modifier is set).
func classPattern(s settings, l *Limits) error {
	m := pregRe.FindStringSubmatch(s.t.code[s.class])
	if m == nil {
		return fmt.Errorf("checks other than one preg_match")
	}
	re, ok := phpString(m[1])
	if !ok {
		return fmt.Errorf("pattern %s not a literal", m[1])
	}
	p, ok := GoPattern(re, true)
	if !ok {
		return fmt.Errorf("its pattern %s has no Go form", re)
	}
	return l.addPattern(p)
}

// csvLimits: CSVListField's Mask, matched like TextField's (the first
// match must be the whole value), against the whole value or, with
// MaskPerItem, each element.
func csvLimits(s settings, l *Limits) error {
	mask, ok := s.xml("Mask")
	if !ok || strings.TrimSpace(mask) == "" {
		return nil
	}
	p, ok := GoPattern(strings.TrimSpace(mask), false)
	if !ok {
		return fmt.Errorf("its Mask %s has no Go form", mask)
	}
	if err := l.addPattern(p); err != nil {
		return err
	}
	l.PerElement = s.flag("MaskPerItem", "internalMaskPerItem", false)
	return nil
}

var (
	// servicesRe: PortField's service names, a list of names or of
	// name => port
	servicesRe = regexp.MustCompile(`(?s)\$wellknownservices\s*=\s*\[(.*?)\]\s*;`)
	quotedRe   = regexp.MustCompile(`'([^'\\]*)'\s*(=>)?`)
	// anyUnionRe: ['any'] + list: PHP's array union keeps 'any' at index
	// 0 and drops the list's first element
	anyUnionRe = regexp.MustCompile(`\[\s*["']any["']\s*\]\s*\+`)
)

// portFormat: PortField's options are the numbers 1 to 65535, with
// EnableWellKnown also "any" and the service names, with EnableAlias the
// names of the firewall's port aliases, and with EnableRanges every
// "a-b" range of the value.
func portFormat(s settings, l *Limits) error {
	f := &Format{Kind: FormatPort,
		Ranges:  s.flag("EnableRanges", "enableRanges", false),
		Aliases: s.flag("EnableAlias", "enableAlias", false)}
	if s.flag("EnableWellKnown", "enableWellKnown", false) {
		code := s.t.code[s.class]
		m := servicesRe.FindStringSubmatch(code)
		if m == nil {
			return fmt.Errorf("service names not found")
		}
		var keys, all []string
		for _, q := range quotedRe.FindAllStringSubmatch(m[1], -1) {
			all = append(all, q[1])
			if q[2] != "" {
				keys = append(keys, q[1])
			}
		}
		names := all
		if len(keys) > 0 {
			names = keys
		}
		if anyUnionRe.MatchString(code) && len(names) > 0 {
			names = names[1:]
		}
		f.Any, f.Services = true, names
	}
	// the range check rejects spaces around the numbers from 26.7 on;
	// before, FILTER_VALIDATE_INT trims them
	f.RangeSpaces = f.Ranges && !strings.Contains(s.t.code[s.class], "trim($tmp[0])")
	l.Format = f
	return nil
}

func networkFormat(s settings, l *Limits) error {
	f := &Format{Kind: FormatNetwork, Netmask: NetmaskAllowed,
		Any:    s.flag("WildcardEnabled", "internalWildcardEnabled", true),
		Strict: s.flag("Strict", "internalStrict", false)}
	switch fam := s.family(); fam {
	case "ipv4", "ipv6":
		f.Family = fam
	}
	allowed := s.flag("NetMaskAllowed", "internalNetMaskAllowed", true)
	required := s.flag("NetMaskRequired", "internalNetMaskRequired", false)
	switch {
	case !allowed && required:
		return fmt.Errorf("NetMaskRequired and not NetMaskAllowed: no value is accepted")
	case !allowed:
		f.Netmask = NetmaskForbidden
	case required:
		f.Netmask = NetmaskRequired
	}
	l.Format = f
	return nil
}

func hostnameFormat(s settings, l *Limits) error {
	l.Format = &Format{Kind: FormatHostname,
		IP:             s.flag("IpAllowed", "internalIpAllowed", true),
		HostWildcard:   s.flag("HostWildcardAllowed", "internalHostWildcardAllowed", false),
		DomainWildcard: s.flag("FqdnWildcardAllowed", "internalFqdnWildcardAllowed", false),
		ZoneRoot:       s.flag("ZoneRootAllowed", "internalZoneRootAllowed", false),
		DNSName:        s.flag("IsDNSName", "internalIsDNSName", false)}
	return nil
}

func ipPortFormat(s settings, l *Limits) error {
	f := &Format{Kind: FormatIPPort,
		PortOptional: s.flag("PortOptional", "internalPortOptional", false),
		Hostname:     s.flag("HostnameAllowed", "internalHostnameAllowed", false)}
	switch fam := s.family(); fam {
	case "", "ipv4", "ipv6":
		f.Family = fam
	default:
		// neither address branch runs for another value
		return fmt.Errorf("AddressFamily %q", fam)
	}
	l.Format = f
	return nil
}

// addPattern sets the one pattern a field has (with its length limits).
func (l *Limits) addPattern(p Limits) error {
	if l.Pattern != "" && l.Pattern != p.Pattern {
		return fmt.Errorf("two patterns")
	}
	l.Pattern, l.Bytes, l.MinLength, l.MaxLength = p.Pattern, p.Bytes, p.MinLength, p.MaxLength
	return nil
}

// validation derives the field's Limits from its class chain, and the gap
// validation-unknown for classes whose checks are not described. It also
// drops the settings the class has no setter for (they have no effect).
func (t *TypeIndex) validation(chain []string, n *Node) {
	var l Limits
	var unknown, used []string
	parentRules := true // the classes so far call parent::getValidators
	for _, c := range chain {
		if !t.validates[c] {
			continue
		}
		ck, ok := checkers[c]
		form := t.form(c, ck)
		switch {
		case !ok:
			unknown = append(unknown, fmt.Sprintf("%s (own checks)", t.baseName(c)))
		case ck.forms[form] == "":
			unknown = append(unknown, fmt.Sprintf("%s (form %s not reviewed)", t.baseName(c), form))
		case strings.HasPrefix(ck.forms[form], notDescribed):
			unknown = append(unknown, fmt.Sprintf("%s (%s)", t.baseName(c), strings.TrimPrefix(ck.forms[form], notDescribed)))
		case parentRules:
			if err := ck.apply(settings{t, chain, n, c, &used}, &l); err != nil {
				unknown = append(unknown, fmt.Sprintf("%s (%v)", t.baseName(c), err))
			}
		}
		if !t.callsParent[c] {
			// a class that does not call parent::getValidators replaces
			// the checks of the classes above it: theirs are no claim
			parentRules = false
		}
	}
	if len(unknown) > 0 {
		n.Gaps = append(n.Gaps, gap.New(gap.ValidationUnknown, "the value is also checked by %s; only the firewall knows those checks", strings.Join(unknown, ", ")))
	}
	if !l.empty() {
		n.Rules = &l
	}
	if len(unknown) == 0 || parentRules {
		slices.Sort(used)
		n.Checked = slices.Compact(used)
	}
	// a setting without a setter in the chain never reaches the field
	for k := range n.Props {
		if !t.hasSetter(chain, k) {
			delete(n.Props, k)
		}
	}
	if len(n.Props) == 0 {
		n.Props = nil
	}
}

// hasSetter: some class of the chain declares set<name> (PHP method names
// are case-insensitive).
func (t *TypeIndex) hasSetter(chain []string, name string) bool {
	m := "set" + strings.ToLower(name)
	for _, c := range chain {
		if t.methodCode[c][m] != "" {
			return true
		}
	}
	return false
}

// form is the hash of the code a checker reads for class c (comments
// and layout ignored), 16 hex digits.
func (t *TypeIndex) form(c string, ck *checker) string {
	if ck == nil {
		return ""
	}
	h := sha256.New()
	unit := func(name, code string) {
		fmt.Fprintf(h, "%s\n%s\n", name, normalize(code))
	}
	if ck.methods == nil {
		code := t.code[c]
		if ck.ignore != nil {
			code = ck.ignore.ReplaceAllString(code, "")
		}
		unit(c, code)
	}
	for _, m := range ck.methods {
		unit(c+"::"+m, t.methodCode[c][m])
	}
	for _, d := range ck.deps {
		cls, m, ok := strings.Cut(d, "::")
		if ok {
			unit(d, t.methodCode[cls][m])
		} else {
			unit(d, t.code[cls])
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Forms lists, for each described class present in this release, its
// form and whether it is known (tools/ci/formats.sh).
func (t *TypeIndex) Forms() []string {
	var out []string
	for c, ck := range checkers {
		if t.code[c] == "" {
			continue
		}
		f := t.form(c, ck)
		note := ck.forms[f]
		if note == "" {
			note = "NOT REVIEWED"
		}
		out = append(out, fmt.Sprintf("%s %s %s", t.baseName(c), f, note))
	}
	slices.Sort(out)
	return out
}

// normalize: layout does not count.
func normalize(code string) string {
	return strings.Join(strings.Fields(code), " ")
}
