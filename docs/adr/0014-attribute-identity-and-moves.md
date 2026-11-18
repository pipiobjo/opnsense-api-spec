# 0014: Provider attribute names are fixed once; field moves are mapped, not generated

Status: accepted 2026-11-18 (before the first provider slice), requirement P8

## Context

OPNsense sometimes moves a field inside a model. Between 25.7.7 and 25.7.8
the Unbound model moved `dnsbl.safesearch` to `general.safesearch` and
turned the `dnsbl` section into an entry list `dnsbl.blocklist`. The
server ignores keys it does not know, so a client that sends the old path
to a newer firewall gets "saved" and no change (ADR 0006). The capability
data shows such a move as one field removed and another added.

A provider attribute is part of its users' state. If attribute names
follow the raw field path, a later move would turn one attribute into two
and force a state upgrade per resource, with notes for every user. The
earlier plan was to generate `moved_from` from the diffs (capabilities.md,
"Not in format 1 yet").

Measured on all 34 releases from 25.7 to 26.7.9 (diffs between
consecutive releases, a removed and an added field with the same last path
segment in the same model):

- Unbound 25.7.7 -> 25.7.8: `safesearch` is a real move (migration
  `M1_0_13.php`: "move safesearch toggle to General container"); the seven
  other matches (`dnsbl.enabled` -> `dnsbl.blocklist.enabled` and so on)
  are a section turned into an entry list, and the migration splits one
  old section into several entries by network size.
- os-frr 26.7.6 -> 26.7.7: six matches (`networks.network.area` ->
  `areas.area`, ...); the OSPF6 model replaced `networks` with a new
  `areas` list of different fields. None is a move.

So the name match finds one move in fourteen candidates. OPNsense states
moves only in migration code, which is arbitrary PHP that often reshapes
data on the way.

## Decision

1. **Attribute names.** A provider attribute is named from the field path
   relative to the resource (snake case, nested containers as nested
   attributes) in the newest data that exists when the attribute is first
   released. The provider's checked-in name table (generated, append-only;
   provider plan, section 3.2) records it. After its first release an
   attribute name never changes; the build fails if it would.
2. **Mapping.** The name table maps each attribute to one or more field
   paths, each with the releases (variants) in which it is valid. The
   provider sends and reads the path that exists in the resolved variant.
   A move found later adds a path to the existing entry: same attribute
   name, same state, no state upgrade, no user action.
3. **No generated `moved_from` for now.** The generator does not write
   `moved_from`, because the only reliable source is migration code it
   cannot read without guessing (ADR 0002), and the name heuristic is
   wrong in 13 of 14 measured cases. The data already gives what the
   mapping needs: per field the variants in which it exists. Adding a
   path to the name table is a reviewed, one-time step per move, triggered
   by the provider build (below), not by a schedule.
4. **Detection.** The provider build compares the name table with the
   data it embeds: an attribute whose paths exist in no variant of the
   newest release of a series fails the build with the candidates (fields
   added in the same model in the same release). This happens only when a
   provider release is built with new data; a newer data state loaded at
   run time (P3) cannot rename anything, it can only make the attribute
   unavailable on that version, which the client refuses before sending
   (F6) with a message that names the version.
5. **Pilot resources (first slice).** Unbound settings names `safesearch`
   after its current path (`general.safesearch`); on 25.7.0 to 25.7.7 the
   attribute is not available and plan says so. The DNSBL resource is the
   entry list `dnsbl.blocklist`, available from 25.7.8; on older
   releases the resource is not available. Host overrides and Kea
   reservations have no move in the data. The slice therefore needs no
   mapping; mapping `dnsbl.safesearch` for 25.7.0 to 25.7.7 can be added
   later without changing the attribute.

## Consequences

- No state upgraders for moves; an attribute keeps its name even if
  OPNsense later moves the field (the name then shows the older location).
- Users of releases before a move cannot manage the moved field until a
  mapping is added; they get a plan-time message, never a silent no-op.
- `moved_from` stays listed as possible in capabilities.md. If OPNsense
  ever declares moves in data (not code), the generator can emit it as an
  optional key without a new format number.
