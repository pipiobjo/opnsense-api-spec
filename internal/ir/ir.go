// Package ir builds the language-neutral intermediate representation of one
// OPNsense release: models, endpoints and how each endpoint was derived.
package ir

import (
	"sort"

	"github.com/pipiobjo/opnsense-api-spec/internal/gap"
	"strings"

	"github.com/pipiobjo/opnsense-api-spec/internal/controller"
	"github.com/pipiobjo/opnsense-api-spec/internal/model"
)

// GeneratorName and GeneratorVersion identify this generator in all output.
const (
	GeneratorName    = "opnsense-api-spec"
	GeneratorVersion = "0.4.0-dev"
)

// Source says how an endpoint description was derived.
type Source string

const (
	// SourceBase: behaviour of a generic base-class action (getAction,
	// setAction, service actions) applied to the bound model.
	SourceBase Source = "base"
	// SourceBaseCall: a thin wrapper around searchBase/getBase/addBase/
	// setBase/delBase/toggleBase with literal arguments.
	SourceBaseCall Source = "base-call"
	// SourceSignature: only the action name and parameters are known.
	SourceSignature Source = "signature-only"
)

// Kind classifies an endpoint.
type Kind string

const (
	KindModelGet      Kind = "model.get"
	KindModelSet      Kind = "model.set"
	KindItemSearch    Kind = "item.search"
	KindItemGet       Kind = "item.get"
	KindItemAdd       Kind = "item.add"
	KindItemSet       Kind = "item.set"
	KindItemDel       Kind = "item.del"
	KindItemToggle    Kind = "item.toggle"
	KindServiceAction Kind = "service.action"
	KindServiceStatus Kind = "service.status"
	KindCustom        Kind = "custom"
)

// Endpoint is one API action with its classification.
type Endpoint struct {
	Path       string             `json:"path"` // /api/module/controller/action
	Package    string             `json:"package,omitempty"`
	AlsoIn     []string           `json:"also_in,omitempty"`
	Module     string             `json:"module"`
	Controller string             `json:"controller"`
	Action     string             `json:"action"`
	Params     []controller.Param `json:"params,omitempty"`
	Methods    []string           `json:"methods"`
	Kind       Kind               `json:"kind"`
	Source     Source             `json:"source"`
	Model      string             `json:"model,omitempty"`      // model class
	Wrapper    string             `json:"wrapper,omitempty"`    // JSON root key of request/response
	Outer      string             `json:"outer,omitempty"`      // extra key around the wrapper in the response
	ModelPath  string             `json:"model_path,omitempty"` // item path for item.* kinds
	Apply      string             `json:"apply,omitempty"`      // endpoint that activates changes
	DeclaredIn string             `json:"declared_in"`
	Gaps       []gap.Gap          `json:"gaps,omitempty"`
}

// Package is provenance of one input package.
type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Sum     string `json:"sum"`
}

// Release is the complete IR of one release.
type Release struct {
	Generator struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"generator"`
	Version string `json:"version"`
	Series  string `json:"series"`
	// CoreVersion is the core package version including the hotfix
	// revision (26.7.8_2); CatalogSHA256 identifies the catalog state
	// (SHA-256 of the signed packagesite.yaml).
	CoreVersion   string         `json:"core_version,omitempty"`
	CatalogSHA256 string         `json:"catalog_sha256,omitempty"`
	ABI           string         `json:"abi"`
	SignedBy      string         `json:"catalog_signed_by"`
	Packages      []Package      `json:"packages"`
	Conflicts     []string       `json:"file_conflicts,omitempty"`
	Models        []*model.Model `json:"models"`
	Endpoints     []*Endpoint    `json:"endpoints"`
}

const (
	baseModelCtl   = `OPNsense\Base\ApiMutableModelControllerBase`
	baseServiceCtl = `OPNsense\Base\ApiMutableServiceControllerBase`
)

var itemKinds = map[string]Kind{
	"searchBase": KindItemSearch, "getBase": KindItemGet, "addBase": KindItemAdd,
	"setBase": KindItemSet, "delBase": KindItemDel, "toggleBase": KindItemToggle,
}

// thin wrappers: a base call plus at most a few lines of glue
const thinLines = 4

// Build classifies all endpoints of the given controllers against models.
func Build(models []*model.Model, ctls []*controller.Controller) []*Endpoint {
	byClass := map[string]*model.Model{}
	for _, m := range models {
		byClass[strings.ToLower(m.Class)] = m
	}
	// apply endpoints: the controller's own apply/reconfigure action wins,
	// otherwise the module's service/reconfigure
	apply := map[string]string{}    // module -> service reconfigure
	ownApply := map[string]string{} // controller class -> own endpoint
	for _, c := range ctls {
		for _, a := range c.Actions {
			switch {
			case c.Name == "service" && a.Name == "reconfigure":
				apply[c.Module] = "/api/" + c.Module + "/service/reconfigure"
			case a.Name == "apply":
				ownApply[c.Class] = "/api/" + c.Module + "/" + c.Name + "/apply"
			case a.Name == "reconfigure" && ownApply[c.Class] == "":
				ownApply[c.Class] = "/api/" + c.Module + "/" + c.Name + "/reconfigure"
			}
		}
	}
	var out []*Endpoint
	for _, c := range ctls {
		var mdl *model.Model
		if c.ModelClass != "" {
			mdl = byClass[strings.ToLower(c.ModelClass)]
		}
		// primary path per action suffix (e.g. "HostOverride" -> hosts.host),
		// voted by the non-delete item actions
		primary := map[string]string{}
		for _, a := range c.Actions {
			if sfx, ok := itemSuffix(a.Name); ok {
				for _, bc := range a.Calls {
					if bc.Func != "delBase" && bc.Path != "" && primary[sfx] == "" {
						primary[sfx] = bc.Path
					}
				}
			}
		}
		for _, a := range c.Actions {
			e := &Endpoint{
				Path:       "/api/" + c.Module + "/" + c.Name + "/" + a.Name,
				Module:     c.Module,
				Controller: c.Name,
				Action:     a.Name,
				Params:     a.Params,
				DeclaredIn: a.DeclaredIn,
				Package:    c.Package,
				AlsoIn:     c.AlsoIn,
			}
			e.Gaps = append(e.Gaps, c.Gaps...)
			classify(e, c, a, mdl, primary)
			if e.Model != "" && isMutation(e.Kind) {
				e.Apply = ownApply[c.Class]
				if e.Apply == "" {
					e.Apply = apply[c.Module]
				}
				if e.Apply == "" {
					e.Gaps = append(e.Gaps, gap.New(gap.NoApplyEndpoint, "no apply endpoint found (controller apply/reconfigure or %s/service/reconfigure); how changes take effect is unknown", c.Module))
				}
			}
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func isMutation(k Kind) bool {
	switch k {
	case KindModelSet, KindItemAdd, KindItemSet, KindItemDel, KindItemToggle:
		return true
	}
	return false
}

var itemPrefixes = []string{"search", "get", "add", "set", "del", "toggle"}

func itemSuffix(name string) (string, bool) {
	for _, p := range itemPrefixes {
		if strings.HasPrefix(name, p) && len(name) > len(p) {
			return name[len(p):], true
		}
	}
	return "", false
}

func classify(e *Endpoint, c *controller.Controller, a *controller.Action, mdl *model.Model, primary map[string]string) {
	modelGap := func() {
		if mdl == nil {
			if c.ModelClass == "" {
				e.Gaps = append(e.Gaps, gap.New(gap.ModelClassMissing, "controller has no $internalModelClass"))
			} else {
				e.Gaps = append(e.Gaps, gap.New(gap.ModelMissing, "model %s not found", c.ModelClass))
			}
		}
	}
	setModel := func() {
		if mdl != nil {
			e.Model = mdl.Class
		}
	}

	// 1. generic model get/set from ApiMutableModelControllerBase
	// (an override that only wraps a *Base helper, e.g. getAction($uuid)
	// calling getBase, is an entry action and handled in step 3)
	wrapsHelper := a.DeclaredIn != baseModelCtl && len(a.Calls) > 0 && !a.CallsParent
	if (a.Name == "get" || a.Name == "set") && (a.DeclaredIn == baseModelCtl || a.Overrides == baseModelCtl) && !wrapsHelper {
		e.Kind, e.Source, e.Wrapper = KindModelGet, SourceBase, c.ModelName
		e.Methods = []string{"GET"}
		if a.Name == "set" {
			e.Kind, e.Methods = KindModelSet, []string{"POST"}
		}
		setModel()
		modelGap()
		for _, h := range c.Hooks {
			if h == "getModelNodes" && a.Name == "get" {
				e.Gaps = append(e.Gaps, gap.New(gap.GetModelNodesOverride, "overrides getModelNodes; response may be a subset of the model"))
			}
		}
		if a.DeclaredIn != baseModelCtl {
			if a.CallsParent {
				e.Gaps = append(e.Gaps, gap.New(gap.OverrideCallsParent, "overrides %sAction and calls the parent; request/response may differ from the full model (subset or extra keys)", a.Name))
			} else {
				e.Source = SourceSignature
				e.Gaps = append(e.Gaps, gap.New(gap.OverrideNotDerived, "overrides %sAction without calling the parent; shape not derived", a.Name))
			}
		}
		return
	}
	// 2. service actions from ApiMutableServiceControllerBase
	if a.DeclaredIn == baseServiceCtl || a.Overrides == baseServiceCtl {
		e.Source = SourceBase
		if a.Name == "status" {
			e.Kind, e.Methods = KindServiceStatus, []string{"GET"}
		} else {
			e.Kind, e.Methods = KindServiceAction, []string{"POST"}
		}
		if a.DeclaredIn != baseServiceCtl {
			e.Gaps = append(e.Gaps, gap.New(gap.ServiceOverride, "overrides the base service action; response may differ"))
		}
		return
	}
	// 3. thin wrappers around *Base helpers
	if len(a.Calls) > 0 {
		sfx, hasPrefix := itemSuffix(a.Name)
		prefix := strings.TrimSuffix(a.Name, sfx)
		var bc *controller.BaseCall
		// prefer the helper matching the action prefix (setX -> setBase),
		// then the family's primary path
		for i := range a.Calls {
			if hasPrefix && a.Calls[i].Func == prefix+"Base" && (primary[sfx] == "" || a.Calls[i].Path == primary[sfx]) {
				bc = &a.Calls[i]
				break
			}
		}
		for i := range a.Calls {
			if bc == nil && a.Calls[i].Path == primary[sfx] {
				bc = &a.Calls[i]
			}
		}
		if bc == nil {
			bc = &a.Calls[0]
		}
		if len(a.Calls) > 1 {
			e.Gaps = append(e.Gaps, gap.New(gap.MultipleHelpers, "action calls several base helpers; documented the one on %s", bc.Path))
		}
		if bc.Raw != "" {
			e.Kind, e.Source = KindCustom, SourceSignature
			e.Methods = []string{"GET", "POST"}
			e.Gaps = append(e.Gaps, gap.New(gap.HelperNonLiteral, "base helper %s called with non-literal arguments: %s", bc.Func, bc.Raw))
			return
		}
		// a helper used internally by an action of another kind with own
		// logic (e.g. rawDump calling getBase) says nothing about its I/O
		nameFits := hasPrefix && bc.Func == prefix+"Base" || a.Name == "get" || a.Name == "set" ||
			bc.Func == "searchBase" && strings.HasPrefix(a.Name, "search")
		if !nameFits && a.Lines > thinLines {
			e.Kind, e.Source = KindCustom, SourceSignature
			e.Methods = []string{"GET", "POST"}
			e.Gaps = append(e.Gaps, gap.New(gap.HelperUsedInternally, "custom action using %s internally; request and response not derived", bc.Func))
			return
		}
		e.Kind, e.Source = itemKinds[bc.Func], SourceBaseCall
		e.ModelPath, e.Wrapper, e.Outer = bc.Path, bc.Key, bc.Outer
		setModel()
		modelGap()
		switch e.Kind {
		case KindItemSearch:
			e.Methods = []string{"GET", "POST"}
		case KindItemGet:
			e.Methods = []string{"GET"}
		default:
			e.Methods = []string{"POST"}
		}
		if mdl != nil {
			if n := mdl.Find(bc.Path); n == nil {
				e.Gaps = append(e.Gaps, gap.New(gap.ModelPathMissing, "path %s not found in model", bc.Path))
			} else if n.Kind != model.KindArray {
				e.Gaps = append(e.Gaps, gap.New(gap.ModelPathNotArray, "path %s is not an ArrayField", bc.Path))
			}
		}
		if a.Lines > thinLines {
			e.Gaps = append(e.Gaps, gap.New(gap.CustomLogic, "action adds custom logic around %s; request/response may differ", bc.Func))
		} else if !bc.Direct && (e.Kind == KindItemGet || e.Kind == KindItemSearch) {
			e.Gaps = append(e.Gaps, gap.New(gap.PostProcessed, "action post-processes the result of %s; response may differ", bc.Func))
		}
		return
	}
	// 4. anything else: only the signature is known
	e.Kind, e.Source = KindCustom, SourceSignature
	switch {
	case a.ChecksPost && !a.ChecksGet:
		e.Methods = []string{"POST"}
	case a.ChecksGet && !a.ChecksPost:
		e.Methods = []string{"GET"}
	default:
		e.Methods = []string{"GET", "POST"}
		e.Gaps = append(e.Gaps, gap.New(gap.MethodUnknown, "HTTP method not derived"))
	}
	e.Gaps = append(e.Gaps, gap.New(gap.CustomAction, "custom action; request and response not derived"))
}
