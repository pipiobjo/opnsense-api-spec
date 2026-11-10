# 0002: Explicit derivation levels; never guess

Status: accepted, implemented

## Context

The API behaviour is defined by PHP code. A full PHP parser and interpreter
is out of scope. Most endpoints follow a few framework conventions
(generic base-class actions and `*Base` helpers); the rest is custom code.

## Decision

- Recognise the conventions with patterns over comment-stripped PHP.
- Classify every operation as `base`, `base-call` or `signature-only` and
  record everything unresolved in `gaps`.
- When in doubt, degrade to `signature-only` instead of guessing a shape.
- Make the classification visible in the output (`x-opnsense-derivation`,
  `x-opnsense-coverage`, `coverage.md`).

## Consequences

- Correct where typed, honest where not; about 70 % of operations are
  typed.
- The remaining risk is a convention whose meaning changes while the
  pattern still matches. This is silent and must be caught by contract
  tests, a framework fingerprint and regression gates (see concepts 8.1).

## Evidence

Contract tests found about 15 cases where a pattern matched but the
meaning differed (field types overriding their read shape, post-processed
helper results, an extra wrapper key, overridden hooks); each became a
generator rule or a gap.
