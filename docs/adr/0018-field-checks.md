# 0018: Field checks: what a field class checks, as data, for reviewed code only

Status: accepted 2026-11-27, implemented (generator 0.12.0)

## Context

Field probes ([ADR 0017](0017-field-probes.md)) try every claim the data
makes about a field. Up to generator 0.12.0 the data claimed only options,
empty or required, booleans, integer ranges and the masks of text fields.
The classes that matter most for a firewall check far more, and the data
said nothing about it: a `NetworkField` takes an address or a network of a
family, with or without prefix length, maybe `any`; a `HostnameField`
takes host names, maybe IP addresses or wildcards; a `PortField` takes
numbers, service names, ranges and port aliases. A client could not tell
`192.0.2.300` from `192.0.2.30`, and a provider's plan passed what the
apply then failed.

These checks are PHP code in each class, and the code changes between
releases: `Util::isPort` changed in 26.7.6, `TextField` gained
`AllowSpaces`/`AllowNewlines`/`AllowSpecial` in 26.1.10, `EmailField`
switched from PHP's `FILTER_VALIDATE_EMAIL` to a regular expression in
26.1.9, and `PortField` exists in nine forms across 25.7 to 26.7.10. A
description written once for "the" class would be wrong for some
releases, silently.

## Decision

1. **Described classes, by code form.** For each core class whose checks
   the data describes, the generator computes a form: a hash over the
   class's code and the code it relies on (validators, `Util` functions,
   `BaseSetField::iterateInput`), comments and layout ignored. A table in
   `internal/model/validation.go` lists the forms that were reviewed, with
   the releases they come from. Only a reviewed form yields a description.

2. **Every check is described or a gap.** Each class of a field's chain
   that declares `getValidators` or `isValidInput` is either described
   (a reviewed form) or makes the field carry the gap
   `validation-unknown`, naming the class and why: no description
   (relation fields, plugin classes with own checks), a form nobody
   reviewed (a new release changed the code), or a setting the
   description cannot express. A class that does not call its parent's
   checks voids the parents' rules.

3. **What is published.** `limits` gains `format` (kinds `network`,
   `hostname`, `mac`, `ip-port`, `url`, `port`, defined exactly in
   docs/capabilities.md, section Formats, quirks of the PHP code
   included), the pattern details `bytes` (PCRE without `u` counts bytes),
   `min_length`/`max_length` (bounds Go's regexp does not take) and
   `per_element` (CSVListField's `MaskPerItem`), and `forbidden`
   characters. A class's own `preg_match` pattern (DescriptionField,
   EmailField) is published as a pattern with PCRE's `$` translated.

4. **Settings without setter are no claim.** OPNsense applies a setting
   of the model XML only through `set<Name>` of the field's class (PHP
   method names are case-insensitive, so `NetmaskAllowed` works). A
   setting without a setter has no effect; it is dropped from
   `constraints` and is no claim a probe tries.

5. **Probes for every rule, in a context where needed.** The contract test
   probes each rule of a format with a value it decides, on the edges
   where PHP and common libraries differ (zone indices, leading zeros,
   MAC addresses of other lengths, port numbers with leading zeros). A
   field whose every value a rule over several fields refuses is probed
   again with values of other fields (one single select at a time) that
   let its own values decide, so that, for example, the ports of firewall
   rules are tried with protocol TCP. Verdicts reached without such a
   context are not published as verdicts about the field (`explained`).

6. **Field classes are framework.** The framework hash (the key under
   which a plugin version selects its fragment) covers every file of the
   core's `FieldTypes` and `Validators` directories and the firewall's
   `Util.php`, not only the base classes: a plugin's field inherits its
   checks from them, so two releases with different field classes give
   the same plugin version different fragments.

7. **Clients implement the definitions, the probes test them.** The Go
   client implements each format following the PHP code; every published
   probe, including those on fields with `model-validation`, is replayed
   through its check.

## Consequences

- A new release that changes a described class makes its fields carry
  `validation-unknown` until the new form is reviewed and added to the
  table (`OPNSENSE_FORMS=.cache/src go test ./internal/model -run
  TestForms -v` lists the forms per release). Nothing fails: the data stays
  correct and says less, and the claim counts of the contract test show
  it.
- Descriptions of older releases' forms are reviewed, not probed: the
  contract test runs only on the newest release of each series.
- What only the firewall can know stays a gap: whether a referenced entry,
  alias, interface or certificate exists (step 3 of requirement Q8), and
  the names in `/etc/services`.

## Evidence

Contract test of generator 0.12.0, all plugins, on the newest release of
each series (`tools/parallel.sh`, 12 VMs, 594 s wall time, 2026-11-28):

| Release | Probes | In a context | Explained | Contradicted | Field-probe checks ok / known gap / skipped |
|---|---|---|---|---|---|
| 25.7.11 | 15,617 | 414 | 82 | 0 | 234 / 19 / 97 |
| 26.1.11 | 17,836 | 601 | 88 | 0 | 237 / 20 / 101 |
| 26.7.10 | 18,605 | 646 | 110 | 0 | 241 / 21 / 108 |

- Formats in 26.7.10: 100 hostname, 5 ip-port, 8 MAC, 209 network, 70
  port and 12 URL fields; 299 fields carry `validation-unknown`, most of
  them relation fields (182).
- Contexts found: firewall rule ports with `protocol=tcp`; destination
  NAT `local-port` only with two fields (`nordr=0` and `protocol=tcp`),
  hence contexts are extended by one field at a time. Restricting the
  search to fields with model validation of their own brought the run
  from 1410 s to about 600 s.
- Explained probes on format claims that remain (no context found, per
  release): dnsmasq host `ip` (a list), Kea reservation `ip_address`
  (`any`), Quagga static route `gateway` (IPv6), syslog destination
  `hostname` (IPv6). The firewall refuses them for reasons of other
  fields; they are not published as verdicts.
- Upstream quirks the definitions keep, found while reviewing the forms:
  `PortField` dropped the first service name from its options up to
  26.7.5 (the `['any'] +` array union), accepted spaces in ranges before
  26.7, and `HostnameField` does not reset its result per list element,
  so after a valid element a later one fails only in the domain branch.
- The Go client replays all 41,855 published probes of the three
  releases through `Check`; all agree.
- Building the data first failed: `os-acme-client 4.16_1` mapped to two
  fragments on one framework (26.1.8 to 26.1.10 changed field classes,
  not the base classes). Hence decision 6. With field classes counted,
  the framework changes in 29 of 30 transitions from 25.7 to 26.7.10
  (21 before); the framework change is a report, not a gate.
