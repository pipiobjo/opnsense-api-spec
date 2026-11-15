# 0009: Generated write values, read-back and prerequisite entries

Status: accepted, implemented 2026-11-15

## Context

Clients and providers mostly write. The contract test only wrote entries
with the server's default values: an add that the server rejected was
reported as "skipped", so on 26.7.9 only 20 of 188 entry lists were ever
written, and nothing checked that a written value was actually stored.
Since the server silently ignores unknown keys (concepts 7.1), "the add
returned saved" proves little.

Measured on 26.7.9 (core and 70 plugins): the rejected fields were text
(111), networks (42), option lists and relations (60), integers (21) and a
long tail of custom types; 69 of them had a PHP regex mask, some with
look-aheads that Go's regexp cannot compile.

## Decision

The write cycle per entry list is: add, read back, update one field, read
back, delete.

- **Values come from the instance's answer.** The first add uses the
  server defaults. Each field the server rejects gets candidates derived
  from its type and constraints (`MinimumValue`, `NetMaskRequired`,
  `AddressFamily`, `Mask`, ...), filtered by the mask where Go can compile
  it; a field rejected again gets the next candidate (at most 8 rounds).
  Option and relation fields take what the instance offers in its
  empty-entry template, so they work with the instance's own interfaces,
  certificates and aliases.
- **Only documentation values**: 192.0.2.0/24, 2001:db8::/32, example.com,
  02:00:00:00:00:01. A unit test enforces this.
- **Relations get prerequisites.** A rejected relation with nothing to
  point at creates an entry in the target list (model and list path are
  now parsed from the model XML, `x-opnsense-relation`), up to two levels
  deep, and deletes it afterwards. This is noted in the result.
- **Read-back compares what we chose** (generated values, description,
  the updated field). Values the server sets itself (ids, audit fields,
  serials) are not writes of ours; write-only fields are not compared.
  A difference is a mismatch, or a known gap when the get endpoint is
  already marked as differing.
- **Steps are recorded** (`add`, `read-back`, `set`, `read-back-set`,
  `del`). An update rejected because the new value broke a rule involving
  other fields is a visible note, and `set` is then missing from the
  steps: it is not verified, and it is not counted as verified.
- **Samples**: the accepted entry is stored in the result and rendered as
  the request example of the add operation, for client authors and for
  the test suites of client and provider.
- Nothing is applied (no reconfigure); the instance only stores the
  entries.

## Consequences

- 26.7.9 with all plugins: 162 of 188 entry lists pass (150 with update,
  17 with an automatically created prerequisite), 23 skipped with the
  reason, 2 known gaps (was: 20 passing). The CI run on all three series
  (generator 0.7.0) found no mismatch: 25.7.11 160 ok of 180, 26.1.11 162
  of 184, 26.7.9 163 of 188.
- The stronger check found two generator errors that had been published:
  an action that calls a helper without returning its result
  (`os-wol` `delHost`, new gap code `result-not-returned`), and a wrapped
  helper result treated as direct (`interfaces/vip_settings/addItem`,
  now `post-processed`). It also found that the cycle had picked the wrong
  get endpoint where a controller has two (`getHost`, `getwake`).
- Interface assignments refuse deletion while `lock=1` (the default); the
  cycle unlocks and notes it. Consumers must do the same.
- Samples contain values that only exist on the test instance (interface
  keys, UUIDs of prerequisites); the example's summary says so.
- Skipped entry lists are mostly relations to lists the cycle cannot
  create (a target without an add endpoint, or one needing values from
  outside the model) and cross-field rules. They stay visible per list.
