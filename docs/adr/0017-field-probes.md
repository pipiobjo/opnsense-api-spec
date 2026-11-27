# 0017: Field probes: every claim about a field is tried on the firewall

Status: accepted 2026-11-27, implemented (generator 0.12.0)

## Context

The capability data says, for every field of every resource, which values
the firewall takes: its options, whether several may be selected, whether
it may be empty, its default, its limits (`MinimumValue`, `MaximumValue`,
`Mask`, ...). Clients check values against these claims before they send
anything (the Go client's `Check`), and a provider shows the result at
plan time. A wrong claim therefore turns into a plan that passes and an
apply that fails, or into a value refused that the firewall would take.

Up to now the contract test wrote one sample per entry list and one field
per settings model (ADR 0009). Each new check that looked at a further
corner found an error there (an option `default` listed instead of `""`,
class defaults not read, options decided at run time listed as fixed). A
read-only comparison of options and defaults with the firewall's own
description of its fields came first (generator 0.11.0). What the
firewall accepts can only be seen by writing.

The aim (requirement Q8): when the firewall rejects a configuration, the
cause is a wrong configuration, a published gap, or a release that was
never tested, and never an undetected error of the data or the client.

## Decision

1. **Probes.** The contract test writes, for every field of every
   writable resource (settings models through `set`; entry lists through
   `set` on the entry the write cycle created), values derived from the
   data's claims, each with the verdict the data predicts:

   | Claim | Probe: accepted | Probe: rejected |
   |---|---|---|
   | options (not `options-dynamic`) | every option | a key that is not an option, where the list is complete |
   | `multiple` | two options together | two options together, when not multiple |
   | `required` | | the empty value (unless `empty_ignored`) |
   | not required | the empty value | |
   | boolean | `0`, `1` | `2` |
   | integer, `MinimumValue`, `MaximumValue` | the limits | one below, one above, a non-number |
   | `Mask` | a value matching it | a value not matching it |

   An accepted value must read back as sent (the existing read-back rule:
   not for `custom-write`, `write_only`, `server_set`).

2. **Verdicts.** A probe the firewall judges as predicted confirms its
   claim. A probe judged otherwise is a finding: the data is wrong, or a
   gap is missing. A rejection that names only other fields says nothing
   about the probed claim (a rule over several fields, step 3 of Q8); it
   is recorded as such and the claim stays unconfirmed.

3. **Rounds.** A model's probes are sent in rounds, one value per field
   per round: a round of values predicted to be accepted, read back
   together, and a round of values predicted to be rejected, whose
   validation messages must name each field. A field that spoils a round
   is retried alone, so a verdict is always about one field. After the
   last round the original values are written back and read back.

4. **Every run, every field.** The probes run in every contract test,
   with no selection by changed models (decision of 2026-11-27,
   requirements Q8): the contract test runs only when a release is new,
   and a selection would be one more way for an older release's result
   to count for a newer one.

5. **Claims gate.** The verifier counts each claim of each field as
   confirmed, contradicted, covered by a gap, blocked (with the reason:
   the model is not valid in its default state, no entry could be
   created, a rule over several fields) or not probed (a constraint the
   prober does not know yet). The counts go into the report; a
   contradicted claim fails the run like any other finding. Claims that
   are not probed are listed by constraint name, so what remains is
   visible instead of silently passing.

6. **Publication.** The probes and the firewall's verdicts are part of a
   resource's verification in the capability data (additive; format 1
   readers ignore it). Consumers replay them: the Go client's test runs
   every published probe through `Check` and requires the firewall's
   verdict. The data and the client then agree with the firewall on the
   same values without the client running a firewall.

## Consequences

- A contract test barely takes longer: one value per field per round,
  so a model needs as many rounds as its longest option list. The core of
  26.7.10 on one VM: 141 s for the whole contract test. All three series
  with every plugin, parallel on 12 VMs: 536 s wall time, as before.
- The data grows by the probe values: 7.3 MB to 13 MB as JSON, 0.21 MB
  compressed with zstd (34 releases, 75 packages).
- Claims the prober cannot try yet stay visible in the counts, and
  remaining work is measurable.
- The probes try exactly the limits the data publishes as `limits`
  (`model.Limits`: an integer field's range, a text field's mask in Go
  syntax with OPNsense's "first match is the whole value" rule), so a
  consumer checks the same limits without a copy of the PHP knowledge.

## Evidence

The first runs (2026-11-27, newest release of each series, every plugin)
contradicted the data in these places, each now derived:

- Rules over several fields: `performValidation` of the model (and the
  methods it calls, os-caddy), and `<Constraints>` in the model XML
  (`SetIfConstraint`, unbound host overrides). Firewall rules, NPT, Kea
  reservations, dnsmasq ranges, monit tests (its validation sets a test's
  type). Now the gap `model-validation` on 539 fields of 26.7.10.
- A field class that makes its fields required (`$internalIsRequired` in
  IPsec's `CharonLogLevelField`): 19 fields were published as optional.
- A mask matched in part: OPNsense's Regex validator requires the first
  match to be the whole value; unbound's unanchored cache size masks.
- Fields a controller's `setBaseHook` sets (26.1 port forwarding audit
  fields), now `computed`.
- OPNsense 25.7 marks the empty choice of a single select also for the
  value "0" (`empty()` in `BaseListField::getNodeData`): the reading rule
  in docs/capabilities.md, applied by the contract test and the Go client.

Result of the second full run: 11,925 / 13,651 / 14,139 probes on
25.7.11 / 26.1.11 / 26.7.10, no unexplained contradiction; 97 / 101 / 108
objects not probed (their current values are refused or no entry could
be created), listed with their reason. Replayed through the Go client's
value check: 27,888 probes agree, after one fix in the client (a single
select whose options are decided at run time took a list of keys).
