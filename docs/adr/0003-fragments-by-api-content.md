# 0003: Fragments identified by API content

Status: accepted, implemented 2026-11-10 (replaces "one spec per release number"); daily pipeline implemented 2026-11-12 (ADR 0008); superset view planned

## Context

Outputs are currently keyed by release number. Measured on the mirror:

- the snapshot of the newest release is byte-identical to the live
  `latest` repository and keeps changing until the next patch release
  (hotfix revisions such as `26.7.8_2`, plugin updates);
- the same plugin version has an identical API across releases and series
  (`os-haproxy` 5.1 from 26.1.6 to 26.7.8), package revisions often do not
  change the API, but an identical version number does not guarantee an
  identical API (`os-crowdsec` 1.0.12 in 25.7.11 vs. 26.1.6).

So a release number neither identifies content nor is it the right unit
for plugins.

## Decision

- The unit is a **fragment**: the derived API of the core or of one plugin,
  identified by a hash over the derived API (after resolving core field
  types), not by package version or raw files.
- A **composition** is the set of fragments of one catalog state,
  identified by the catalog hash and named after OPNsense (`26.7.8_2`) plus
  the catalog revision.
- Status `provisional` while a snapshot is live, `final` afterwards; final
  fragments are immutable (content addresses). Generator versions add
  fragments, never overwrite them.
- Clarified 2026-11-18 (architecture review): the per-release GitHub
  assets (OpenAPI views) are re-rendered and replaced when the generator
  changes, also for final releases; what identifies one version of them is
  `SHA256SUMS`, the generator version in the release notes and the
  attestation. Consumers that need a fixed state use the capability
  data's never-replaced releases (ADR 0010).
- Daily change detection on catalog hashes; rebuild only changed fragments.
- Views derived from fragments: per release, per fragment, superset per
  package with availability ranges.

## Consequences

- Correct identity for hotfixes and plugin updates.
- Fragments are deduplicated across releases (less storage than ~10 MB per
  release today).
- Consumers can compose exactly what an instance runs.
- More moving parts: an index of fragments and their availability.

## Evidence

oapi-codegen generates one Go package per fragment with shared types from a
`common` package (`import-mapping`, `skip-prune`); openapi-generator
(TypeScript, Python) resolves the external references and copies shared
types. Both compile/generate without errors (probe, 2026-11-10).

## Implementation notes (2026-11-10)

- Catalogs are always downloaded; a cached catalog would have hidden
  hotfix revisions.
- 33 releases (25.7 to 26.7.8): 2,379 fragment references, 209 distinct
  fragments; e.g. `os-haproxy` 5.1 is one fragment in 18 releases.
- Composition ids carry the hotfix revision (`26.7.3_11~86d63fb41411`).
- Golden hashes of final releases and a determinism test guard the
  identity (`internal/assemble`).
