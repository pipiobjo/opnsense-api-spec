# 0007: OpenAPI 3.1 as the only output format

Status: accepted (gates planned)

## Context

OpenAPI 3.1 is based on full JSON Schema (2020-12) and can describe what the
OPNsense API does, e.g. `patternProperties` for the `%field` display values.
OpenAPI 3.0 cannot, so a 3.0 output would need workarounds. The concern was
tool support for 3.1.

Our own consumer is a Go client (for a Terraform/OpenTofu provider),
generated with oapi-codegen. Other consumers of the public spec are unknown;
openapi-generator is the most widely used generator for other languages.

Measured on 2026-11-10 with the full 26.7 spec (core and all plugins):

| Tool | Generate | Compiles | Notes |
|---|---|---|---|
| oapi-codegen v2.8.2 (Go) | ok, no warnings | yes, 5094 client methods | `patternProperties` is ignored (display values dropped, harmless); needs `compatibility.always-prefix-enum-values` |
| openapi-generator v7.27.0, Python | ok | yes | 3.1 support labelled "beta" by the tool |
| openapi-generator v7.27.0, TypeScript | ok | no, 4 errors | enum values `~*` and `^~` (nginx) become empty identifiers; independent of the OpenAPI version |

oapi-codegen gained 3.1 support in v2.8.0 (2026-07-17, closing
oapi-codegen#373). An earlier test with v2.4.1 had suggested missing
support; that version was simply outdated.

## Decision

- Publish OpenAPI 3.1 only. No 3.0 variant until a concrete consumer needs
  one.
- The IR stays the source of truth; a 3.0 renderer would be a variant of the
  existing renderer, so this decision is cheap to revisit.
- Compatibility is checked on every build:
  - **blocking:** validation against the OpenAPI 3.1 specification
    (language-neutral; tool to be chosen), and oapi-codegen generate +
    compile (our consumer);
  - **informational:** openapi-generator for other languages. Failures are
    reported, not blocking, because they are often generator issues rather
    than spec errors.
- No `x-enum-varnames` for now: oapi-codegen names special-character enum
  values itself (`^~` -> `CaretTilde`, `~*` -> `TildeAsterisk`); only
  openapi-generator's TypeScript output would benefit.

## Re-evaluate when

- a consumer asks for OpenAPI 3.0, or
- a blocking compatibility check fails for a reason that is not a spec
  error, or an informational check keeps failing for a widely used
  language and a consumer is affected.

## Consequences

- One format, one set of tests, full expressiveness.
- Third-party clients generated with less mature 3.1 tooling may need
  tool-specific settings; the README lists the known ones.
