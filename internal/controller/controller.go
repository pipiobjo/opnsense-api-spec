// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package controller extracts API endpoints from OPNsense MVC controller
// PHP sources using pattern matching (not a full PHP parser). Everything it
// cannot classify is reported, never guessed.
package controller

import (
	"os"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Param is a path parameter taken from the action signature.
type Param struct {
	Name     string `json:"name"`
	Optional bool   `json:"optional,omitempty"`
}

// BaseCall is a call to one of the generic model helpers of
// ApiMutableModelControllerBase.
type BaseCall struct {
	Func string `json:"func"`          // searchBase, getBase, addBase, setBase, delBase, toggleBase
	Key  string `json:"key,omitempty"` // request/response wrapper key (getBase/addBase/setBase)
	Path string `json:"path,omitempty"`
	Raw  string `json:"raw,omitempty"` // arguments as written when not string literals
	// Outer is a key the action wraps the helper result in, as in
	// return array("pac" => $this->getBase(...)); -> {"pac": {"match": ...}}
	Outer string `json:"outer,omitempty"`
	// Direct: the action returns the helper result as is (return $this->xBase(...))
	Direct bool `json:"direct,omitempty"`
	// NoReturn: the action has no return statement at all, so the helper
	// result is discarded and the response is empty.
	NoReturn bool `json:"no_return,omitempty"`
	// Overlay: fields addBase/setBase force regardless of the request, as in
	// addBase('domain', 'domains.domain', ['type' => 'primary']).
	Overlay []Overlay `json:"overlay,omitempty"`
}

// Overlay is one field a helper call forces. Literal values are known;
// otherwise the controller computes the value (a property, a function).
// Field "" means the overlay is not an array literal, so its fields are
// unknown.
type Overlay struct {
	Field   string `json:"field"`
	Value   string `json:"value,omitempty"`
	Literal bool   `json:"literal,omitempty"`
	// FromRequest: the value is derived from the request itself (as in
	// base64_encode($this->request->getPost(...))): the field is
	// writable, but stored in another form.
	FromRequest bool `json:"from_request,omitempty"`
}

// Action is one public xxxAction method.
type Action struct {
	Name        string     `json:"name"` // without the "Action" suffix
	Params      []Param    `json:"params,omitempty"`
	DeclaredIn  string     `json:"declared_in"`
	Overrides   string     `json:"overrides,omitempty"` // parent class whose action is overridden
	Calls       []BaseCall `json:"base_calls,omitempty"`
	CallsParent bool       `json:"calls_parent,omitempty"`
	ChecksPost  bool       `json:"checks_post,omitempty"`
	ChecksGet   bool       `json:"checks_get,omitempty"`
	Lines       int        `json:"lines"`
}

// Class is one parsed PHP class.
type Class struct {
	Name       string             `json:"name"` // fully qualified
	Extends    string             `json:"extends,omitempty"`
	File       string             `json:"file"`
	Abstract   bool               `json:"abstract,omitempty"`
	ModelName  string             `json:"model_name,omitempty"`  // $internalModelName
	ModelClass string             `json:"model_class,omitempty"` // $internalModelClass
	Actions    map[string]*Action `json:"-"`
	// protected hooks of ApiMutableModelControllerBase this class overrides
	Hooks []string `json:"hooks,omitempty"`
}

// Controller is a routable API controller with its effective actions.
type Controller struct {
	Class      string    `json:"class"`
	File       string    `json:"file"`
	Package    string    `json:"package,omitempty"`
	AlsoIn     []string  `json:"also_in,omitempty"` // alternative packages shipping the same file
	Module     string    `json:"module"`            // URL segment 1
	Name       string    `json:"controller"`        // URL segment 2
	Chain      []string  `json:"chain"`             // class, parent, ... (resolved part)
	ModelName  string    `json:"model_name,omitempty"`
	ModelClass string    `json:"model_class,omitempty"`
	Hooks      []string  `json:"hooks,omitempty"` // overridden hooks (getModelNodes, ...)
	Actions    []*Action `json:"actions"`
	Gaps       []gap.Gap `json:"gaps,omitempty"`
}

var (
	nsRe     = regexp.MustCompile(`(?m)^\s*namespace\s+([\\\w]+)\s*;`)
	useRe    = regexp.MustCompile(`(?m)^\s*use\s+([\\\w]+)(?:\s+as\s+(\w+))?\s*;`)
	classRe  = regexp.MustCompile(`(?m)^\s*(abstract\s+|final\s+)?class\s+(\w+)(?:\s+extends\s+([\\\w]+))?`)
	propRe   = regexp.MustCompile(`static\s+\$(internalModelName|internalModelClass)\s*=\s*['"]([^'"]*)['"]`)
	funcRe   = regexp.MustCompile(`public\s+function\s+(\w+)Action\s*\(`)
	callRe   = regexp.MustCompile(`\$this->(searchBase|getBase|addBase|setBase|delBase|toggleBase)\s*\(`)
	returnRe = regexp.MustCompile(`return\s*$`)
	returnKw = regexp.MustCompile(`\breturn\b`)
	outerRe  = regexp.MustCompile(`return\s*(?:array\s*\(|\[)\s*['"](\w+)['"]\s*=>\s*$`)
	hookRe   = regexp.MustCompile(`function\s+(getModelNodes|setActionHook|setBaseHook|__call)\s*\(`)
	parentRe = regexp.MustCompile(`parent::\w+Action\s*\(`)
	litRe    = regexp.MustCompile(`^\s*(['"])([^'"]*)['"]\s*$`)
)

// StripComments removes PHP comments while keeping string literals intact.
func StripComments(src string) string {
	var b strings.Builder
	n := len(src)
	for i := 0; i < n; i++ {
		c := src[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < n && src[j] != c {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			if j >= n {
				j = n - 1
			}
			b.WriteString(src[i : j+1])
			i = j
		case c == '/' && i+1 < n && src[i+1] == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			// keep newlines for line counting
			b.WriteString(strings.Repeat("\n", strings.Count(src[i:i+2+end+2], "\n")))
			i = i + 2 + end + 1
		case (c == '/' && i+1 < n && src[i+1] == '/') || c == '#':
			for i < n && src[i] != '\n' {
				i++
			}
			if i < n {
				b.WriteByte('\n')
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// matching returns the index of the bracket closing the one at open.
func matching(src string, open int, o, c byte) int {
	depth := 0
	for i := open; i < len(src); i++ {
		switch src[i] {
		case '\'', '"':
			q := src[i]
			for i++; i < len(src) && src[i] != q; i++ {
				if src[i] == '\\' {
					i++
				}
			}
		case o:
			depth++
		case c:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitArgs splits a parameter/argument list at top-level commas.
func splitArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case '\'', '"':
			q := s[i]
			for i++; i < len(s) && s[i] != q; i++ {
				if s[i] == '\\' {
					i++
				}
			}
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	if t := strings.TrimSpace(s[start:]); t != "" {
		out = append(out, t)
	}
	return out
}

var paramNameRe = regexp.MustCompile(`\$(\w+)`)

// ParseFile parses one PHP file into classes.
func ParseFile(src, relFile string) []*Class {
	code := StripComments(src)
	ns := ""
	if m := nsRe.FindStringSubmatch(code); m != nil {
		ns = m[1]
	}
	uses := map[string]string{}
	for _, m := range useRe.FindAllStringSubmatch(code, -1) {
		alias := m[2]
		if alias == "" {
			alias = m[1][strings.LastIndex(m[1], `\`)+1:]
		}
		uses[alias] = m[1]
	}
	qualify := func(name string) string {
		switch {
		case name == "":
			return ""
		case strings.HasPrefix(name, `\`):
			return name[1:]
		case uses[name] != "":
			return uses[name]
		case strings.Contains(name, `\`):
			return ns + `\` + name
		default:
			return ns + `\` + name
		}
	}
	var out []*Class
	locs := classRe.FindAllStringSubmatchIndex(code, -1)
	for _, loc := range locs {
		cl := &Class{
			Name:     qualify(code[loc[4]:loc[5]]),
			File:     relFile,
			Abstract: loc[2] >= 0 && strings.HasPrefix(code[loc[2]:loc[3]], "abstract"),
			Actions:  map[string]*Action{},
		}
		if loc[6] >= 0 {
			cl.Extends = qualify(code[loc[6]:loc[7]])
		}
		bodyStart := strings.IndexByte(code[loc[1]:], '{')
		if bodyStart < 0 {
			continue
		}
		bodyStart += loc[1]
		bodyEnd := matching(code, bodyStart, '{', '}')
		if bodyEnd < 0 {
			bodyEnd = len(code) - 1
		}
		body := code[bodyStart : bodyEnd+1]
		for _, m := range hookRe.FindAllStringSubmatch(body, -1) {
			cl.Hooks = append(cl.Hooks, m[1])
		}
		for _, m := range propRe.FindAllStringSubmatch(body, -1) {
			if m[1] == "internalModelName" {
				cl.ModelName = m[2]
			} else {
				// PHP string literal: "\\" is one backslash
				cl.ModelClass = strings.TrimPrefix(strings.ReplaceAll(m[2], `\\`, `\`), `\`)
			}
		}
		for _, fm := range funcRe.FindAllStringSubmatchIndex(body, -1) {
			name := body[fm[2]:fm[3]]
			pOpen := fm[1] - 1
			pClose := matching(body, pOpen, '(', ')')
			if pClose < 0 {
				continue
			}
			a := &Action{Name: name, DeclaredIn: cl.Name}
			for _, p := range splitArgs(body[pOpen+1 : pClose]) {
				pm := paramNameRe.FindStringSubmatch(p)
				if pm == nil {
					continue
				}
				a.Params = append(a.Params, Param{Name: pm[1], Optional: strings.Contains(p, "=")})
			}
			fOpen := strings.IndexByte(body[pClose:], '{')
			if fOpen < 0 {
				cl.Actions[strings.ToLower(name)] = a // abstract/interface method
				continue
			}
			fOpen += pClose
			fClose := matching(body, fOpen, '{', '}')
			if fClose < 0 {
				continue
			}
			fbody := body[fOpen : fClose+1]
			a.Lines = strings.Count(fbody, "\n") + 1
			a.CallsParent = parentRe.MatchString(fbody)
			a.ChecksPost = strings.Contains(fbody, "->isPost()")
			a.ChecksGet = strings.Contains(fbody, "->isGet()")
			for _, cm := range callRe.FindAllStringSubmatchIndex(fbody, -1) {
				aOpen := cm[1] - 1
				aClose := matching(fbody, aOpen, '(', ')')
				if aClose < 0 {
					continue
				}
				bc := baseCall(fbody[cm[2]:cm[3]], splitArgs(fbody[aOpen+1:aClose]), fbody[:cm[0]])
				if om := outerRe.FindStringSubmatch(fbody[:cm[0]]); om != nil {
					bc.Outer, bc.Direct = om[1], true
				} else if returnRe.MatchString(fbody[:cm[0]]) {
					bc.Direct = true
				} else if !returnKw.MatchString(fbody) {
					bc.NoReturn = true
				}
				a.Calls = append(a.Calls, bc)
			}
			cl.Actions[strings.ToLower(name)] = a
		}
		out = append(out, cl)
	}
	return out
}

func lit(s string) (string, bool) {
	if m := litRe.FindStringSubmatch(s); m != nil {
		return m[2], true
	}
	return "", false
}

// baseCall reads one helper call; before is the action's code up to the
// call, where an overlay passed as a variable is assembled.
func baseCall(fn string, args []string, before string) BaseCall {
	bc := BaseCall{Func: fn}
	get := func(i int) (string, bool) {
		if i >= len(args) {
			return "", false
		}
		return lit(args[i])
	}
	ok := true
	switch fn {
	case "getBase", "addBase", "setBase":
		var ok1, ok2 bool
		bc.Key, ok1 = get(0)
		bc.Path, ok2 = get(1)
		ok = ok1 && ok2
	default: // searchBase, delBase, toggleBase: first argument is the path
		bc.Path, ok = get(0)
	}
	if !ok {
		bc.Raw = strings.Join(args, ", ")
	}
	// the overlay argument: addBase($key, $path, $overlay),
	// setBase($key, $path, $uuid, $overlay)
	if i := map[string]int{"addBase": 2, "setBase": 3}[fn]; i > 0 && i < len(args) {
		bc.Overlay = parseOverlay(args[i], before)
	}
	return bc
}

var arrayLitRe = regexp.MustCompile(`(?s)^\s*(?:\[(.*)\]|array\s*\((.*)\))\s*$`)

var varRe = regexp.MustCompile(`^\s*\$([A-Za-z_][A-Za-z0-9_]*)\s*$`)

// parseOverlay reads the overlay argument: a PHP array literal of field =>
// value pairs, null, or a variable assembled in the code before the call
// (overlayVar). Anything else: fields unknown.
func parseOverlay(arg, before string) []Overlay {
	m := arrayLitRe.FindStringSubmatch(arg)
	if m == nil {
		if strings.TrimSpace(arg) == "null" {
			return nil
		}
		if v := varRe.FindStringSubmatch(arg); v != nil {
			return overlayVar(v[1], before)
		}
		return []Overlay{{}} // an expression: fields unknown
	}
	return overlayPairs(m[1] + m[2])
}

// overlayVar derives the fields of an overlay variable from the code before
// the call, the way OPNsense's interface controllers build it:
//
//	$overlay = null;            // or [] / array() / an array literal
//	$overlay['laggif'] = $name; // one field per assignment
//
// Every assignment to the variable must have one of these forms, with a
// literal key; otherwise, or when no field is assigned, the fields are
// unknown, and so are any other uses of the variable before the call
// (".=", passing it on, reading it), and the fields of an overlay assembled
// after the action read the request. Overlays built by a helper method are
// not followed. Conditional assignments count: the controller may overwrite the
// field, so it is not the client's to send.
func overlayVar(name, before string) []Overlay {
	q := regexp.QuoteMeta(name)
	// "$x =" and "$x[...] =", but not "==", "=>" or "$x[...] ==="
	assignRe := regexp.MustCompile(`\$` + q + `\s*(\[[^\]]*\])?\s*=([^=>][^;]*);`)
	// an overlay assembled after reading the request may depend on it
	// (os-dnsmasq clears different fields per option type): not one fixed
	// set of fields
	if strings.Contains(before, "->request->") || strings.Contains(before, "getPost(") {
		return []Overlay{{}}
	}
	assigns := assignRe.FindAllStringSubmatch(before, -1)
	// every use of the variable must be one of these assignments (no
	// ".=", no passing it to a function, no reading it)
	if len(regexp.MustCompile(`\$`+q+`\b`).FindAllStringIndex(before, -1)) != len(assigns) {
		return []Overlay{{}}
	}
	var out []Overlay
	seen := map[string]bool{}
	for _, a := range assigns {
		key, rhs := strings.TrimSpace(a[1]), strings.TrimSpace(a[2])
		if key == "" {
			switch {
			case rhs == "null" || rhs == "[]" || regexp.MustCompile(`^array\s*\(\s*\)$`).MatchString(rhs):
				continue
			case arrayLitRe.MatchString(rhs):
				pairs := overlayPairs(arrayLitRe.FindStringSubmatch(rhs)[1] + arrayLitRe.FindStringSubmatch(rhs)[2])
				if len(pairs) == 1 && pairs[0].Field == "" {
					return pairs
				}
				for _, o := range pairs {
					if !seen[o.Field] {
						seen[o.Field] = true
						out = append(out, o)
					}
				}
				continue
			}
			return []Overlay{{}} // assigned from an expression
		}
		field, ok := lit(strings.TrimSpace(key[1 : len(key)-1]))
		if !ok {
			return []Overlay{{}} // computed key
		}
		if seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, overlayValue(field, rhs))
	}
	if len(out) == 0 {
		return []Overlay{{}}
	}
	return out
}

func overlayValue(field, v string) Overlay {
	o := Overlay{Field: field}
	if val, ok := lit(strings.TrimSpace(v)); ok {
		o.Value, o.Literal = val, true
	} else if strings.Contains(v, "->request->") || strings.Contains(v, "getPost(") {
		o.FromRequest = true
	}
	return o
}

// overlayPairs reads the inside of an array literal of field => value.
func overlayPairs(inner string) []Overlay {
	var out []Overlay
	for _, pair := range splitArgs(inner) {
		k, v, ok := strings.Cut(pair, "=>")
		if !ok {
			continue
		}
		field, ok := lit(strings.TrimSpace(k))
		if !ok {
			return []Overlay{{}} // computed keys: fields unknown
		}
		out = append(out, overlayValue(field, v))
	}
	return out
}

// snake converts CamelCase to the URL form the OPNsense router maps back
// (Router::parsePath: ucwords on "_" then strip "_").
func snake(s string) string {
	var b strings.Builder
	rs := []rune(s)
	for i, r := range rs {
		if unicode.IsUpper(r) && i > 0 {
			prev := rs[i-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
				b.WriteByte('_')
			}
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// LoadAll parses all controllers below appDir/controllers and returns the
// routable API controllers.
func LoadAll(appDir string) ([]*Controller, error) {
	classes := map[string]*Class{}
	root := filepath.Join(appDir, "controllers")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".php") {
			return err
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(appDir, p)
		for _, c := range ParseFile(string(src), filepath.ToSlash(rel)) {
			classes[strings.ToLower(c.Name)] = c
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return Resolve(classes), nil
}

// Resolve builds routable controllers from parsed classes.
func Resolve(classes map[string]*Class) []*Controller {
	var out []*Controller
	for _, c := range classes {
		if c.Abstract || !strings.HasSuffix(c.Name, "Controller") {
			continue
		}
		parts := strings.Split(c.Name, `\`)
		// Vendor\Module\Api\NameController
		if len(parts) != 4 || parts[2] != "Api" {
			continue
		}
		ctl := &Controller{
			Class:  c.Name,
			File:   c.File,
			Module: strings.ToLower(parts[1]),
			Name:   snake(strings.TrimSuffix(parts[3], "Controller")),
		}
		// inheritance chain, most specific first
		var chain []*Class
		for cur := c; cur != nil; {
			chain = append(chain, cur)
			ctl.Chain = append(ctl.Chain, cur.Name)
			if cur.Extends == "" {
				break
			}
			next := classes[strings.ToLower(cur.Extends)]
			if next == nil {
				ctl.Chain = append(ctl.Chain, cur.Extends+" (not found)")
				if !strings.HasPrefix(cur.Extends, `OPNsense\Mvc\`) {
					ctl.Gaps = append(ctl.Gaps, gap.New(gap.ParentMissing, "parent class %s not found; inherited actions missing", cur.Extends))
				}
			}
			cur = next
		}
		actions := map[string]*Action{}
		for i := len(chain) - 1; i >= 0; i-- {
			cl := chain[i]
			if cl.ModelName != "" {
				ctl.ModelName = cl.ModelName
			}
			if cl.ModelClass != "" {
				ctl.ModelClass = cl.ModelClass
			}
			if !strings.HasPrefix(cl.Name, `OPNsense\Base\`) {
				ctl.Hooks = append(ctl.Hooks, cl.Hooks...)
			}
			for k, a := range cl.Actions {
				cp := *a
				if prev, ok := actions[k]; ok {
					cp.Overrides = prev.DeclaredIn
				}
				actions[k] = &cp
			}
		}
		for _, h := range ctl.Hooks {
			if h == "__call" {
				ctl.Gaps = append(ctl.Gaps, gap.New(gap.MagicCall,
					"the controller answers other action names through __call (%s); more endpoints than these may exist", ctl.Class))
			}
		}
		for _, a := range actions {
			ctl.Actions = append(ctl.Actions, a)
		}
		sort.Slice(ctl.Actions, func(i, j int) bool { return ctl.Actions[i].Name < ctl.Actions[j].Name })
		out = append(out, ctl)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Module != out[j].Module {
			return out[i].Module < out[j].Module
		}
		return out[i].Name < out[j].Name
	})
	return out
}
