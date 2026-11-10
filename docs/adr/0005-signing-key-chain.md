# 0005: Follow key rotation through the signed key chain

Status: accepted, planned (today: static key list from core 26.7.5)

## Context

OPNsense rotates the repository signing key per series. A static list
breaks at the next series (27.1) and needs a manual update, which invites
either outages or careless trust decisions.

Measured: each series' core lists its own and the previous key as trusted
(26.7: 20260120, 20260608), and the last patch release of a series already
lists the next key (25.7.11 trusts 20260120, 26.1.11 trusts 20260608).

## Decision

Start from one pinned anchor key. Accept a new key only if a core package
lists it as trusted, and that package comes from a catalog signed by an
already accepted key and matches its checksum. Store the resulting chain
(key, introducing release, catalog hash) in the repository.

## Consequences

- Series changes need no human action as long as OPNsense keeps this
  practice.
- An emergency rotation without the chain stops the pipeline and asks for
  a reviewed manual entry; this is intended.
