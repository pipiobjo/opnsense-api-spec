# 0005: Follow key rotation through the signed key chain

Status: accepted, implemented 2026-11-11

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

## Implementation notes (2026-11-11)

- `internal/pkgrepo/keys.json` (bootstrap: the 23 keys of core 26.7.5,
  22 rotated and 1 current); `LearnKeys` adds keys with their source and
  only allows trusted -> revoked.
- Every matrix run re-derives the chain (newest release per series,
  ascending), so it does not depend on a committed key file; the file is
  updated as an audit trail (`-keys`).
- Tested against a fake mirror with a series change and a key nobody
  introduced; on real data (25.7 to 26.7.8) no key is learned or changed.

## Addendum 2026-11-18: how long a revoked key is valid

Context: until now every known key, revoked or not, was accepted for every
release (`allowRotated` was always set). A retired key that leaked could
then sign a fake new release on a mirror, and the pipeline would build and
publish data from it. A real firewall would refuse it: its core lists the
key as revoked.

Measured on the mirror (all 64 releases from 24.7 to 26.7.9): every series
is signed by its own key (24.7 20240611, 25.1 20241217, 25.7 20250710,
26.1 20260120, 26.7 20260608), and the newest core of the next series lists
it as revoked (core 26.1.11_10 revokes 20250710, 26.7.9_3 revokes
20260120). Early cores of the next series still trust it (26.7.1, 26.7.2
trust 20260120).

Decision: each key records `revoked_in`, the series whose core first lists
it as revoked in the chain walk (newest release per series, ascending;
the earliest series wins). `VerifyCatalog` takes the series from the
catalog's release and accepts a revoked key only for an older series. A
revoked key without `revoked_in`, or a catalog without a series, is
refused. `LearnKeys` fills the field from the same core packages that
already drive the chain, so a series change still needs no human action.

The values in `keys.json` were derived once by the same walk from 24.7
(the oldest series checked): keys already revoked in core 24.7.12_4 carry
`revoked_in: 24.7` as an upper bound, which only matters below the support
floor (25.7).

Consequences:

- A fake 26.7 or 27.1 release signed with 20250710 or any older key is
  refused (`TestRevokedKeyRefusedForNewSeries`); every published release
  still verifies (`TestEmbeddedKeysCoverPublishedSeries`, and a run over
  all 64 real catalogs on 2026-11-18).
- What stays open by design: a fake 25.7.12 signed with 20250710 is still
  accepted, as on a 25.7 firewall. Between the start of a series and the
  core that revokes the previous key (about two patch releases) the
  previous key is still trusted, as on the firewall.
- No lower bound: a new key may sign an older series. That is not a threat
  (the key is current) and OPNsense does not do it today.
