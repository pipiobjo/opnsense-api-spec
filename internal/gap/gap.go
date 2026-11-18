// SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
// SPDX-License-Identifier: BSD-2-Clause

// Package gap defines the things the generator noticed but could not
// derive. Behaviour depends only on the Code; Detail is for humans and may
// be reworded freely.
package gap

import "fmt"

// Code identifies a kind of gap. Codes are part of the output format
// (x-opnsense-derivation.gaps, x-opnsense-gaps, coverage) and must stay
// stable.
type Code string

// Operation-level codes.
const (
	ParentMissing         Code = "parent-missing"           // a parent controller class was not found
	ModelClassMissing     Code = "model-class-missing"      // controller has no $internalModelClass
	ModelMissing          Code = "model-missing"            // the bound model was not found
	ModelPathMissing      Code = "model-path-missing"       // helper path not in the model
	ModelPathNotArray     Code = "model-path-not-array"     // helper path is not an ArrayField
	GetModelNodesOverride Code = "get-model-nodes-override" // response may be a subset of the model
	OverrideCallsParent   Code = "override-calls-parent"    // overrides a generic action, calls the parent
	OverrideNotDerived    Code = "override-not-derived"     // overrides a generic action without the parent
	ServiceOverride       Code = "service-override"         // overrides a generic service action
	MultipleHelpers       Code = "multiple-helpers"         // several helper calls; one is documented
	HelperNonLiteral      Code = "helper-non-literal"       // helper called with non-literal arguments
	HelperUsedInternally  Code = "helper-used-internally"   // custom action using a helper internally
	CustomLogic           Code = "custom-logic"             // own code around a helper call
	PostProcessed         Code = "post-processed"           // the helper result is transformed before returning
	ResultNotReturned     Code = "result-not-returned"      // the helper is called but its result is not returned: empty response
	OverlayUnknown        Code = "overlay-unknown"          // the helper forces fields, but which ones is not derivable (helper method, request-dependent, expression)
	MagicCall             Code = "magic-call"               // the controller serves undeclared actions through __call
	NoApplyEndpoint       Code = "no-apply-endpoint"        // how changes take effect is unknown
	MethodUnknown         Code = "http-method-unknown"      // HTTP method not derivable
	CustomAction          Code = "custom-action"            // request and response not derived
)

// Field-level codes.
const (
	FieldTypeUnresolved Code = "field-type-unresolved" // type not resolved to a core base class
	CustomReadShape     Code = "custom-read-shape"     // custom getNodeData/getNodeOptions
	OptionsDynamic      Code = "options-dynamic"       // OptionField without static options
	InvalidDefault      Code = "invalid-default"       // default outside the allowed values (upstream model error)
	UnknownSetting      Code = "unknown-setting"       // untyped leaf element that is not a known setting
	CustomWrite         Code = "custom-write"          // the field type converts or normalizes values on write (setValue)
	CustomSave          Code = "custom-save"           // own code between request and stored data (toLegacy/fromLegacy, serializeToConfig)
)

// Gap is one finding of the generator.
type Gap struct {
	Code   Code   `json:"code"`
	Detail string `json:"detail"`
}

// New creates a gap with a formatted detail text.
func New(c Code, format string, a ...any) Gap {
	return Gap{Code: c, Detail: fmt.Sprintf(format, a...)}
}

func (g Gap) String() string { return string(g.Code) + ": " + g.Detail }

// Has reports whether any of the codes occurs in gs.
func Has(gs []Gap, codes ...Code) bool {
	for _, g := range gs {
		for _, c := range codes {
			if g.Code == c {
				return true
			}
		}
	}
	return false
}

// MayDiffer reports whether a gap means that the instance may answer with a
// different shape than the derived one (overridden or extended actions).
// Contract-test mismatches on such operations confirm a known gap.
func MayDiffer(c Code) bool {
	switch c {
	case GetModelNodesOverride, OverrideCallsParent, OverrideNotDerived, ServiceOverride,
		CustomLogic, PostProcessed:
		return true
	}
	return false
}

// Subset reports whether a MayDiffer gap still promises the model's shape
// with whole sections (entry lists, containers) left out and keys added:
// the action calls the generic implementation and removes or adds parts
// of its result. A missing field or a value of another type is not
// explained by such a gap.
func Subset(c Code) bool {
	return c == GetModelNodesOverride || c == OverrideCallsParent
}

// First returns the first gap of gs whose code satisfies pred.
func First(gs []Gap, pred func(Code) bool) (Gap, bool) {
	for _, g := range gs {
		if pred(g.Code) {
			return g, true
		}
	}
	return Gap{}, false
}
