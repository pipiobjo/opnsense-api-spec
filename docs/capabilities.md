# Capability data (format 1)

Status: accepted (2026-11-15). The format is the contract between this
repository and its consumers (the Go client, the OpenTofu/Terraform
provider, and anyone else); it changes only with a new `format` number.

## What it is for

The OpenAPI documents describe one release, or one package in one state
(a fragment). A client that talks to many firewalls needs the opposite
view: for one package, every resource over all versions, and for a given
firewall, which of them apply. OpenAPI has no notion of "this field exists
from version X" (concepts, section 9), and OPNsense silently ignores keys
it does not know (concepts, section 7.1), so a client without this
knowledge sends fields that do nothing.

The capability data is that view: plain JSON with a JSON Schema, readable
without any code from this repository.

## Files

| File | Content |
|---|---|
| `catalog.json` | core versions with their framework, releases with their catalog states (compositions) and test results, list of packages |
| `capabilities/<package>.json` | one document per package (`opnsense` is the core) |
| `capability.schema.json` | JSON Schema (draft 2020-12) for both kinds of file |
| `version-order.json` | test table of the version order (see Compatibility rules): `ordered` ascending, `equal` pairs; every implementation of the order must pass it. Added 2026-11-18; format 1 readers ignore it |

Size over 34 releases, core and 75 packages: 3.8 MB as JSON, 0.13 MB
compressed with zstd. The core document is the largest (1.8 MB).

## Identity and releases

Every data state has an ID, `data_id` in `catalog.json`: the first 16
hex digits of a SHA-256 over all files of the state (each as name, NUL,
length, NUL, content, in name order; `catalog.json` taken with `data_id`
removed). The same data gives the same ID; any change in any file gives a
new one. The build is deterministic and checks that itself: it builds
twice and fails if the results differ.

Each state is published as its own release, and two rolling releases
point to the newest one:

| Release | Changes | Use it for |
|---|---|---|
| `catalog-v1-<data_id>` | never: created as a draft, filled, then published once; an existing one is only compared with a new build, never changed | anything that must reproduce a state: a provider that embeds the data, a bug report |
| `catalog-v1` | only its release notes: the line `data_id: <data_id>` of the newest state in format 1; no files, the tag never moves | finding the newest state, then downloading `catalog-v1-<data_id>` |
| `catalog` | its files are replaced when the data changes | older consumers; kept until they read `catalog-v1`, then frozen |

The pipeline publishes the fixed release first and moves the pointer only
when it is complete, so a consumer that follows the pointer never sees a
mixed set of files (the files of `catalog` are replaced one by one and
can). After every run, `tools/ci/check-published.sh catalog` downloads the
state the pointer names as an anonymous consumer and checks it: file list,
`SHA256SUMS`, attestation, and the `data_id` recomputed from the content
(`opnsense-api-spec data-id DIR`). "Never changed" rests on that check and
on the attestation, not on GitHub alone: while GitHub's Immutable Releases
setting is off, the owner of the repository could still replace files;
the attestation of the original files would then no longer match.

Reading the pointer, then the state (anonymous; the API allows 60 such
reads per hour and address):

```sh
id=$(curl -fsSL https://api.github.com/repos/pipiobjo/opnsense-api-spec/releases/tags/catalog-v1 \
  | jq -r .body | sed -n 's/^data_id: \([0-9a-f]\{16\}\)\r\{0,1\}$/\1/p')
gh release download "catalog-v1-$id" -R pipiobjo/opnsense-api-spec
```

A new format (a breaking change, see Compatibility rules) gets a new
pointer, `catalog-v2`, and new fixed releases `catalog-v2-<data_id>`. The
format-1 pointer and its fixed releases stay as they are: a reader of
format 1 keeps working with the last format-1 state; it does not receive
newer data until it reads format 2. No format is produced twice in
parallel.

`catalog-v1-386ea34c45d263ba` (2026-11-18) is defective: its
`catalog.json` names a `data_id` over a file the archive lacks
(`version-order.json`); its content is that of
`catalog-v1-f79a4c510ffefa42`. The build has since packed every file it
hashes, and the check above recomputes the `data_id` of what is published.

All of them are GitHub pre-releases: format 1 may still change before the
first provider release that uses it. A consumer verifies `SHA256SUMS`, then the
attestation (`gh attestation verify <file> -R pipiobjo/opnsense-api-spec
--signer-workflow pipiobjo/opnsense-api-spec/.github/workflows/pipeline.yml
--source-ref refs/heads/main`: only the publishing pipeline on `main`
counts, not a dry run from another branch),
and reports the `data_id` it uses; a consumer that embeds the data also
recomputes the `data_id` from the content (the rule above).

## Identifiers

- **Fragment**: 16 hex digits, the hash of a package's derived API
  (concepts, section 5.2). Equal API, equal hash, regardless of release.
- **Framework**: 16 hex digits, the hash of the core's MVC base classes,
  field classes and validators (what a field takes depends on them).
  Plugins are derived against it, so the same plugin version can have a
  different fragment on a different framework.
- **Resource**: `<module>/<controller>` for a settings model (one object,
  `get`/`set`), `<module>/<controller>#<model path>` for an entry list
  (entries keyed by UUID: `search`, `get`, `add`, `set`, `del`, `toggle`).
  Example: `quagga/ospfsettings#networks.network`. Where one model path is
  served by several families of endpoints, each family is its own
  resource, `<module>/<controller>#<model path>@<family>`, with `family`
  set (bind: `bind/domain#domains.domain@primary`, `@secondary`,
  `@forward`, each with its own `add` and `search`, sharing `get`, `set`
  and `del`). An identifier is never reused for something else.
- **Field**: dotted path below the resource (`general.enabled` in settings,
  `area` in an entry). Fields of nested entry lists belong to their own
  resource, not to the settings.

## Selecting the fragments for a firewall

A firewall reports its core version (`26.7.9_1`, including the hotfix
revision) and the versions of installed packages.

1. `catalog.json` `cores["26.7.9_1"]` gives the framework, here
   `fb3734b94bf5416f`, and the release.
2. For each installed package, its document's `select[framework][version]`
   gives the fragment. This key is unique; the build fails otherwise.
3. Everything in a document with that fragment in its `in` applies to this
   firewall; everything else does not.

A combination that was never seen (a core hotfix newer than the data, a
plugin version built after the last run) has no entry. What to do then is
the consumer's policy. The Go client's, which we recommend:

1. Another hotfix revision of the same release (`26.7.9` against data of
   `26.7.9_1`): the nearest older revision, otherwise the nearest newer
   one, with a note. Revisions of one release do not change the API in
   practice, and every write is read back. The same package version seen
   on another core of the series is used the same way.
2. A package version the data has never seen: the newest fragment of the
   package in that series is used, with a warning that the combination is
   unverified.
3. An installed version older than all data of its series: read only,
   writes are refused (the data may describe fields it lacks).
4. A series newer than the data: no fragment. Writes are refused, because
   a new series can change the framework the API is derived from.

Apart from hotfix revisions of the same release, never take data newer
than what is installed for a write, and never fall back silently: a
consumer must show the note or warning.

## Variants and `in`

Everything that can change between versions is a list of variants. Each
variant has `in`: either `"*"` (all fragments the resource exists in) or
the list of fragments it applies to. Exactly one variant applies per
fragment. From a real document (os-frr, OSPF networks, shortened):

```json
"quagga/ospfsettings#networks.network": {
  "kind": "entries",
  "in": ["70ea32d17d684e96", "...13 fragments..."],
  "api": [{
    "in": "*",
    "model": "OPNsense\\Quagga\\OSPF",
    "model_path": "networks.network",
    "wrapper": "network",
    "apply": "/api/quagga/service/reconfigure",
    "operations": {
      "add":    {"path": "/api/quagga/ospfsettings/addNetwork", "methods": ["POST"], "source": "base-call"},
      "get":    {"path": "/api/quagga/ospfsettings/getNetwork", "methods": ["GET"], "params": ["uuid?"], "source": "base-call"},
      "del":    {"path": "/api/quagga/ospfsettings/delNetwork", "methods": ["POST"], "params": ["uuid"], "source": "base-call"}
    }
  }],
  "fields": {
    "enabled": [{"in": "*", "type": "BooleanField", "base": "BooleanField", "read_shape": "string", "required": true, "default": "1"}],
    "area": [
      {"in": ["70ea32d17d684e96", "...11..."], "type": "TextField", "read_shape": "string", "required": true,
       "constraints": {"Mask": "/^\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}\\.\\d{1,3}$/"}},
      {"in": ["48ee87c87938d808", "b04bde19061e2dec"], "type": "ModelRelationField", "base": "BaseListField",
       "read_shape": "select", "required": true, "gaps": ["custom-read-shape"]}
    ]
  }
}
```

In os-frr 2.x the OSPF `area` changed from a free-text field to a
reference to an area entry: a client that sends `"0.0.0.0"` to the new
version gets a validation error, and one that reads it gets an option map
instead of a string. The data says so per fragment.

Over 34 releases, 361 of 4,924 fields have more than one variant: changed
defaults, option lists, validation masks, and a few changes of type. Texts
for the web UI (`ValidationMessage`, `BlankDesc`) are left out on purpose:
they change wording often and are not part of the contract; the OpenAPI
documents keep them.

### Field attributes

| Key | Meaning |
|---|---|
| `type` | field class as written in the model XML (`OptionField`, `NetworkField`, a plugin's own class) |
| `base` | the core base class it resolves to; decides validation and shape |
| `read_shape` | `string`, or `select` (a read returns `{key: {value, selected}}`, a write takes the keys joined by the list separator); `unknown` when the class could not be resolved |
| `multiple`, `required`, `volatile` | as in the model; `volatile` fields are not stored |
| `list` | the value is a list of elements (see [List fields](#list-fields)): `separator` joins the elements on the wire, `ordered` says whether the firewall keeps the order written. Absent for single values |
| `server_set` | the firewall fills the field itself (base `UniqueIdField`, `AutoNumberField`, `JsonAuditField`) or its type discards every value written (`LegacyLinkField`, whose value comes from the legacy configuration); a value sent has no effect |
| `write_only` | `get` returns it empty, because the type's `__toString` returns an empty string (`UpdateOnlyTextField`, `ApiKeyField`: passwords, API keys). Not a secrecy guarantee: from OPNsense 26.1.8 on, `search` returns the stored value (a password hash, the API key blob), and a raw API call sees it. A consumer that must hide it drops it from search rows itself |
| `empty_ignored` | a write of the empty value leaves the stored value unchanged (the type's `setValue` stores only non-empty values; with PHP's `empty()`, `"0"` counts as empty too). Such a field cannot be cleared through the API; a consumer should refuse the attempt instead of reporting success |
| `default` | the model default; absent means empty |
| `options` | static options from the model; dynamic options (interfaces, certificates) only exist on the instance |
| `relations` | entry lists a relation field points to: `source` is the model class as written in the model, with dots and in any case (`OPNsense.quagga.ospf` for `OPNsense\Quagga\OSPF`; PHP class names are case-insensitive), `items` the list path |
| `limits` | what the field's classes check in a non-empty value, where derived ([ADR 0018](adr/0018-field-checks.md)): `minimum`/`maximum` of an integer field; `pattern`, a mask or a class's own pattern in Go (RE2) regexp syntax with its flags inline, passing when its first match is the whole value (OPNsense's Regex validator; an unanchored mask still constrains all of it), matched against the value's bytes when `bytes` (a PHP pattern without the `u` modifier counts bytes) and against each list element when `per_element`; `min_length`/`max_length`, the length the same way counted (a pattern `^X{m,n}$` with a bound over 1000, which Go's regexp does not take, is published as `^X*$` with these); `forbidden`, characters the value may not contain (TextField's `AllowSpaces`, `AllowNewlines`, `AllowSpecial` from 26.1.10); `format`, see [Formats](#formats). The firewall checks the stored value: some classes trim it or change its case first, as the format says. A rule the data cannot express is the gap `validation-unknown`; the firewall's check is then the only one |
| `constraints` | the other model settings that reach the field's class (`Mask`, `MinimumValue`, `AddressFamily`, ...), as strings; `"(structured)"` marks nested settings that are not represented. A setting the class has no setter for has no effect in OPNsense and is left out |
| `gaps` | gap codes: where the derivation is uncertain (concepts, section 8) |

An enum value's availability follows from the variants: an option exists
in the fragments of the variants that list it.

### List fields

A field with `list` holds several elements in one wire string, joined by
`list.separator`. Three kinds of field have it:

| Field | Write | Read (`get`) | `ordered` |
|---|---|---|---|
| `multiple` option or relation field (`base` `BaseListField`) | selected keys, `,` | every option as `{key: {value, selected}}`, in the option order | `false`; `true` only for a relation field with `Sorted` `Y`, which lists the selected keys first in the stored order |
| set field with `AsList` (`base` `BaseSetField`: `CSVListField`, a `NetworkField` or `HostnameField` with `<AsList>Y</AsList>`) | elements joined by `FieldSeparator` (default `,`) | `{element: {value, selected: 1}}`, one per stored element, in the stored order | `true` |
| a type whose own `getNodeData` splits the value the same way (`AliasContentField`, separator newline) | elements joined by the separator | as above; the alias `getItem` also lists other aliases with `selected: 0` | `true` |

A reader takes the keys with `selected` 1 in the order of the answer
(JSON object member order; PHP keeps it). A single select (not
`multiple`, no `list`) that marks the empty key `""` and one other key
holds the other key: OPNsense 25.7 marks the empty choice for every value
PHP's `empty()` calls empty, `"0"` included; 26.1 and later do not. When the keys are `0`, `1`, ...
PHP sends a JSON list instead of an object, and the index is the key. An
empty value reads as one key `""`, which is the empty list. Because the
keys of a JSON object are unique, a read returns each element once even if
it was written twice. An element must not contain the separator.

A set field without `AsList` (a `HostnameField` that takes one name) is a
single value and has no `list`.

### Formats

`limits.format.kind` names the form of value a field class accepts. A
check runs on each element of a `list` field (BaseSetField splits the value
by its separator) unless said otherwise. Each definition follows the PHP
code it is derived from, quirks included, so that a client that implements
it agrees with the firewall; the contract test tries every rule on the
firewall, and the Go client replays those verdicts
(`opnsense/capability/formats.go` is a reference implementation).

| Kind | Classes | The firewall accepts |
|---|---|---|
| `network` | NetworkField | the value trimmed and in lower case: `any` as the whole value when `any`; else each element an IP address (PHP `FILTER_VALIDATE_IP`: no zone index, no leading zeros in IPv4) of the `family` (`ipv4`, `ipv6`; absent: both), optionally `/` and a prefix length of digits only, at most 32 (no `:` in the address) or 128. `netmask`: `forbidden` (no `/`), `allowed`, `required` (an element without `/` is refused). `strict`: no address bits set after the prefix length |
| `hostname` | HostnameField | the value trimmed; per element: with `domain_wildcard` a leading `*.`, with `zone_root` a leading `@.` is dropped first; then accepted are `@` (`zone_root`), `*` (`host_wildcard`), an IP address (`ip`), or a name passing PHP's `FILTER_VALIDATE_DOMAIN` (with `FILTER_FLAG_HOSTNAME` unless `dns_name`: labels of letters, digits and `-`, starting and ending with a letter or digit, at most 63 characters, 253 in all, a final dot ignored) that is not an IP address and has no white space, `/` or `*`. Upstream quirk: once an element passed, a later one is refused only by the last rule (a name with white space, `/` or `*`, or an address where none is allowed) |
| `mac` | MacAddressField | the value trimmed; per element six pairs of hex digits joined by `:` or `-`, or three groups of four joined by `.` (PHP `FILTER_VALIDATE_MAC`; no other lengths) |
| `ip-port` | IPPortField | the value trimmed; per element: for `family` `ipv4` or absent, `address:port` with an IPv4 address, or the address alone with `port_optional`; with `hostname`, `name:port` (or the name alone with `port_optional`) with a host name as in `hostname`; for `ipv6` or absent, `[address]:port`, or the address alone with `port_optional`. A port is a number 1 to 65535 (digits, no leading zero) or a name of the firewall's services database (`/etc/services`), which the data does not list |
| `url` | UrlField | PHP `FILTER_VALIDATE_URL`: only URL characters (letters, digits and ``$-_.+!*'(),{}|\^~[]`<>#%";/?:@&=``), a scheme, a host unless the scheme is `mailto`, `news` or `file`, for `http` and `https` a host name (as `hostname`) or a bracketed IPv6 address, a port up to 65535 |
| `port` | PortField | the value (each comma-separated element when `list`) one of: a number 1 to 65535 as PHP writes it (`0443` is refused); with `any` the word `any`; one of `services`; with `ranges` a range `a-b` of two numbers 1 to 65535 (PHP `FILTER_VALIDATE_INT`: optional `+`, no leading zero) with a < b, with spaces around the numbers only when `range_spaces`; with `aliases` the name of a port alias on the firewall, which the data cannot know. A single value is stored in lower case and trimmed when that makes it `any` or a service name, so `HTTPS` is taken |

A kind a reader does not know must be left to the firewall, not refused.

### Operations and actions

`operations` holds one endpoint per operation of a resource. `wrapper`
is the key around the object in request and response bodies
(`{"network": {...}}`); `outer`, where present, is one more key around it
in the response (three os-squid PAC endpoints today). The key is always
derived: the first argument of a generic helper (`addBase('network', ...)`),
`$internalModelName` for the generic settings `get`/`set`, and for a
settings action the controller writes itself, the key its code uses
(`$result['client'] = ...` in os-freeradius, `getPost('zerotier')` in
os-zerotier). The contract test treats it as data: an answer without it is
a `mismatch`, whatever gaps the operation has. `forced` names
fields the endpoint sets to a fixed value whatever the request says, and
`computed` fields it fills itself; a value sent for them is overwritten.
Examples: the bind endpoints `addPrimaryDomain`/`addForwardDomain` force
`type`; the traffic shaper's `addPipe` forces `origin` and computes
`number`; unbound's `addForward` computes `type`. `transformed` fields
are writable but stored in another form (nginx error pages store the page
content base64-encoded), so they read back differently. An overlay kept in
a variable counts when every use of the variable before the call is an
assignment with a literal key (the interface controllers' `laggif`,
`bridgeif`, `gifif`, `greif`). Where the fields cannot be derived (built by
a helper method, assembled after reading the request, or an expression),
the gap `overlay-unknown` says so: today os-dnsmasq options, virtual IPs
and wireguard's `addServer`. `source` says
how it was derived (concepts, section 6), `gaps` lists what is uncertain,
for example `result-not-returned` (the endpoint returns nothing) or
`post-processed` (the response may differ from the helper's). `apply` is
the endpoint that activates saved changes; writes alone change only the
stored configuration.

`gaps` on an API variant concerns the stored data as a whole: `custom-save`
when the model saves or loads through its own code (`serializeToConfig`:
bind domains, the firewall filter, os-frr's general settings; `init`: the
setup wizard, which reads every value from the rest of the configuration,
so a value written to it does not read back) or the entries are
stored through `toLegacy`/`fromLegacy` (interface assignments, which drop
the DHCPv6 options unless the IPv6 type is DHCPv6), or the controller
overrides `save` (os-stunnel's services, which set `general.enabled` from
the enabled tunnels; the write operations carry the gap as well). A
field's `gaps` has
`custom-write` when its type converts or normalizes values on write. Both
are read from the PHP classes; they say that a value may be stored
differently from the request, not how. A consumer that reads back after
writing should expect differences there and report them as such.

Endpoints that are not resource operations (service actions such as
`reconfigure`, custom actions, a second endpoint of the same kind) are in
`actions`, keyed by path, with variants by fragment. A settings resource
has a `get` with a derived key; where there is none, its endpoints are
actions too: an own settings action whose code does not show one key on
the model (os-relayd's `settings/get` takes a node type, os-zerotier's
`network/get` a UUID, os-squid's `template/get` answers a zip file), and
a generic `set` without a `get` beside it (os-acme-client's `accounts`,
`actions`, `certificates` and `validations` inherit it next to their own
entry `get`; the same data is the `acmeclient/settings` resource).

### Resources that share data

Several controllers can expose the same model: `firewall/filter`,
`firewall/npt` and others share the settings of the core's firewall
model. `shared_with`
lists the other resources of the package whose API names the same model
class and model path, so the same stored data. A consumer should manage
such data through one of them, usually the one that has both `get` and
`set`; two owners of one object overwrite each other. Over 34 releases,
33 resources in 12 models have `shared_with`. Entry lists inside a shared
model are not affected: they have their own model path. The families of
one model path (above) share their data by design and list each other.

## Verification

`verified` in a resource maps a fragment to the contract test that covered
it on a throwaway instance (concepts, section 8). Two real results from
os-frr on 26.7.9:

```json
"quagga/ospfsettings#neighbors.neighbor": {"verified": {"8259be67f862e47b": {
  "release": "26.7.9", "composition": "26.7.9_1~74eecaac799a",
  "generator": "0.7.0", "date": "2026-11-15T12:11:58Z",
  "read": "ok", "write": "ok",
  "steps": ["add", "read-back", "set", "read-back-set", "del"],
  "sample": {"address": "192.0.2.10", "description": "contracttest1", "enabled": "1", "pollinterval": "", "priority": ""}
}}},
"quagga/ospfsettings#networks.network": {"verified": {"8259be67f862e47b": {
  "release": "26.7.9", "composition": "26.7.9_1~74eecaac799a",
  "generator": "0.7.0", "date": "2026-11-15T12:11:58Z",
  "read": "ok", "write": "skipped",
  "runtime_required": ["area", "ipaddr"],
  "findings": ["add rejected; runtime-required: area, ipaddr", "no accepted value for: area (A value is required.)"]
}}}
```

- `read`: the worst result of the read checks of `get` and `search`.
- `write`: for entry lists the write cycle: `ok` with `set` in `steps`
  means add, update and delete were each read back; without `set`, the
  update was not tested. `list-order` in `steps` means a list field
  (`list.ordered`) was written with two elements out of sorted order and
  read back in that order. Write-only fields the cycle sent are checked
  to come back empty from `get`. For settings one field is set to a value
  different from its current one, read back, set back to the original
  value and read back again (`steps`: set, read-back, restore,
  read-back-restore), so a `set` that answers "saved" but stores nothing
  is a `mismatch`. `ok` covers the fields in `sample` (entries) or the one
  changed field (settings), not every field. `skipped` means not written,
  and is not a pass; the finding says why: for settings "model not valid in its
  default state" (a plugin needs required settings first), "set does not
  save configuration" (a job or action under the name `set`, such as
  `core/firmware`), "not a configuration resource", "no plain field" or
  "no other value accepted".
- `probe`: the field probes ([ADR 0017](adr/0017-field-probes.md)):
  every field of the object (a settings model, or the entry the write
  cycle created) is set, alone, to the values the data predicts accepted
  or rejected: every option, a key that is not one, two options together,
  the empty value, `0`/`1`/`2` for booleans, the integer limits and one
  beyond each, a value matching the pattern and one not, the forbidden
  characters, and for each rule of a [format](#formats) a value it
  decides. When a rule over several fields (`model-validation`) refuses
  every value of a field, the test looks for values of other fields (one
  single select at a time, each of its options) with which the field's own
  values decide, and probes it in that context (a port of a firewall rule
  with protocol TCP). `ok`: the firewall judged every value as predicted
  and every accepted value read back as sent. `known-gap`: the differences
  are on fields with `model-validation` for which no such context was
  found, or under `custom-save`. `skipped`: not probed (the object's
  current values are refused, or no entry could be created); not a pass.
- `probes`: per field, the values the firewall `accepted` and `rejected`,
  each about the field's own value: a rejection whose messages named only
  other fields, and a verdict a rule over several fields made without a
  context, are left out. A consumer can run them all through its own value
  check and must reach the same verdict (the Go client does so in its
  tests).
- `returned_by_search`: `write_only` fields whose stored value the
  `search` answer contained in this run (entry lists; see `write_only`).
- `runtime_required`: fields the instance required although the model does
  not mark them as required; `findings` say why a test did not pass.
- `sample`: an entry the instance accepted. Option values in it may only
  exist on the test instance.
- `same_definition_as`: this fragment was not tested itself. The resource
  has the same definition here (every `api` and `fields` variant that
  applies) as in the named fragment, which was tested in a release of the
  same series; `release`, `composition` and the results are that test's.
  Without the key, the result is from this fragment itself.
- No entry for a fragment: not tested, and no tested fragment of the
  series defines the resource the same way. Only the newest release per
  series is tested on each run; older fragments keep their last result, or
  none.
- Entry lists with several families (bind domains) get one write cycle
  per family.

Consumers should show this to users ("written and read back on 26.7.9",
"same definition as tested on 26.7.9", "not tested") rather than treat
everything as equally reliable.

## What a consumer still decides

The data describes what the firewall does. Some things a client or
provider needs cannot be derived from it, and the format says nothing
rather than guess:

- **Secrets.** OPNsense models do not mark a field as secret. Only
  `write_only` fields are recognisable, and even those are returned by
  `search` on newer releases. Fields such as a `password` or
  `secret` of type `TextField` (more than 200 over all packages) look like
  any other text. A consumer has to decide which fields to treat as
  sensitive and should err towards hiding. The Go client
  [opnsense-api-go](https://github.com/pipiobjo/opnsense-api-go) has one
  tested rule for this (`FieldInfo.Secret`: every `write_only` field plus
  text fields whose name looks like a secret, checked against a reviewed
  list over the published data); the planned provider (P1) and MCP server
  (M7) use it, and other consumers may copy it.
- **Value types.** Every value is a string on the wire. `base` and
  `multiple` decide how to present it: `BooleanField` is `"0"`/`"1"`,
  `IntegerField` and `NumericField` are numbers as strings, fields with
  `list` are lists joined by `list.separator`, `server_set` fields are set by the
  firewall. `base` is the nearest class of the core, not
  the root of the hierarchy (`StrictTextField` and `TextField` are both
  bases), so a consumer keeps a small table from base to its own type. A
  change of `base` between variants is often a refactoring in OPNsense
  with no change on the wire.
- **Unknown versions.** Business editions and versions newer than the
  data have no entry; see the order above.

## Compatibility rules

- `format` changes only for changes that break readers: renamed or removed
  keys, changed meaning. Adding keys does not change it; readers must
  ignore unknown keys. Added so far: `version-order.json` (2026-11-18);
  `list`, `empty_ignored`, `same_definition_as`, `returned_by_search` and
  the steps `restore`, `read-back-restore`, `list-order` (generator
  0.10.0); `probe`, `probes`, `limits` (with `format`, `bytes`, `min_length`,
  `max_length`, `per_element`, `forbidden`) and the gaps `model-validation`
  and `validation-unknown` (generator 0.12.0); from 0.12.0 on,
  `constraints` lists only settings that reach the field's class. With 0.10.0 settings `write` changed from writing back the
  current value to writing a different one; older results of settings
  have `steps` [set, read-back] only.
- Fragment hashes are content addresses and do not change. A new generator
  version can produce new fragments for the same package version; the
  older ones stay in the data as long as a catalog state refers to them.
- Versions (`26.7.9_1`, `2.3_10`) are ordered by their dot-separated
  parts and then the revision after `_`, each compared as a number and then
  by any letter suffix (`2.3_10` is newer than `2.3_9`, `1.2a` newer than
  `1.2`). Any reader that picks "the newest" or "the nearest older" version
  must use this order; plain string order is wrong. The generator
  (`pkgrepo.CompareVersions`) and the Go client
  (`capability.CompareVersions`) implement it and both test against
  `version-order.json`, which is published with the data.
- Resource identifiers and field paths are OPNsense's own names. When
  OPNsense renames something, the old name stays with its fragments and the
  new one appears with the new fragments.

## Not in format 1 yet

- `moved_from`: which field a field replaced (a rename or a move within
  the model). Not generated: OPNsense states moves only in migration code,
  and matching removed and added fields by name is wrong in most measured
  cases. Consumers map moves themselves; the provider does it in its name
  table ([ADR 0014](adr/0014-attribute-identity-and-moves.md)). If it is
  ever added, it is an optional key and format 1 stays.
- Per-field verification (today: per resource).
