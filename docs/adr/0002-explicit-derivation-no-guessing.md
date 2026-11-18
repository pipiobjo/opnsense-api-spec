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

## Addendum 2026-11-18: a gap on an operation does not cover its keys

Up to generator 0.9.3 an own settings `get`/`set` action (one that
overrides the generic action without calling it) was marked
`override-not-derived`, yet carried the generic root key
(`$internalModelName`) anyway. For nine settings that key was wrong
(os-freeradius answers `{"client": ...}` where the data said `Client`;
os-siproxd, os-zerotier, os-relayd likewise), and the contract test
accepted the missing key as `known-gap`, because that gap explained any
difference. The Go client, reading these settings with the published
key, failed on every series.

Decision:

- An own action's root key is read from its code (the one key of the
  array its `get` returns; the request key its `set` hands to the
  model's `setNodes`), or left empty. The gap stays: the content is
  still not derived.
- The contract test treats a root key in the data as data: a missing
  key is a `mismatch` whatever gaps the operation has. Gaps explain
  differences inside the key only.
- A settings resource needs a `get` with a key. Operations without one,
  and a lone generic `set` (os-acme-client's `accounts`, `actions`,
  `certificates`, `validations` inherit it next to their own entry
  `get`), are listed as actions in the capability data.

Evidence: contract results of 25.7.11_9, 26.1.11_10 and 26.7.9_3
(2026-11-18) before and after; `TestOwnSettingsKeys`,
`TestOwnActionWrapper`, `TestRootKeyIsData`.
