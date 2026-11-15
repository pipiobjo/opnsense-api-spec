# 0010: Capability data per package instead of an OpenAPI superset

Status: proposed (format under review), implemented behind a switch 2026-11-15

## Context

ADR 0006 says consumers need to know, per firewall, which operations,
fields and values exist. Concepts 9.1 planned this as an OpenAPI superset
with `x-opnsense-availability` extensions. Generated OpenAPI clients ignore
extensions, so the only readers of that information would be our own
capability layers, which would have to parse OpenAPI plus extensions to get
it.

Measured on the published data (34 releases, core and 75 packages):

- The same plugin version has different fragments on different core
  frameworks (os-c-icap 1.9 on 25.7 and 26.1; os-web-proxy-sso 2.2_3 and
  os-udpbroadcastrelay 1.0_6 even within one series). `(package, version,
  framework)` identifies a fragment uniquely in all 34 catalog states.
- 361 of 4,924 fields change between versions (defaults, options,
  validation masks, a few types; OSPF `area` changes from text to a
  relation in os-frr 2.x). Validation texts for the UI changed in another
  173 fields without any change to the contract.

## Decision

- **A separate, plain JSON format** (docs/capabilities.md, JSON Schema in
  `internal/capability/capability.schema.json`), not an OpenAPI superset:
  one document per package plus `catalog.json`. Resources are settings
  models and entry lists with stable identifiers
  (`<module>/<controller>[#<model path>]`); every value that can change is
  a list of variants with the fragments it applies to.
- **Selection is exact**: core version -> framework (`catalog.json`), then
  `(framework, package version)` -> fragment (`select`). The build fails if
  that key is ever ambiguous. A key collision caused only by an older
  generator (older catalog states are not rebuilt) resolves to the newer
  generator.
- **Verification results travel with the data**, per resource and fragment
  (read, write, steps, accepted sample), so consumers can show how well
  something is tested. A report that does not say which packages it tested
  does not count.
- UI texts (`ValidationMessage`, `BlankDesc`) are not part of the format;
  an empty default equals no default.
- Published as the rolling release `catalog` (`catalog.json`,
  `capabilities.tar.zst`, `SHA256SUMS`, build provenance attestations),
  rebuilt by the daily pipeline and replaced only when the checksums
  change. The archive is reproducible (same input, same bytes).
- Until the format is reviewed, publishing is off (repository variable
  `CATALOG_PUBLISH`); every run builds and validates the data and keeps it
  as a workflow artifact.

## Consequences

- Size: 3.8 MB of JSON, 0.13 MB compressed; one download for a consumer.
- Concepts 9.1 "superset view" is this format; the per-release and
  per-fragment OpenAPI documents stay as they are for generated clients.
- Not yet in format 1: `moved_from` for moved fields, write tests of
  settings resources, per-field verification. They can be added without a
  new format number.
- Consumers pin the hash of `SHA256SUMS` and verify the attestation; the
  release body says how.
