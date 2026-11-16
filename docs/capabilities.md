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

Size over 34 releases, core and 75 packages: 3.8 MB as JSON, 0.13 MB
compressed with zstd. The core document is the largest (1.8 MB).

## Identifiers

- **Fragment**: 16 hex digits, the hash of a package's derived API
  (concepts, section 5.2). Equal API, equal hash, regardless of release.
- **Framework**: 16 hex digits, the hash of the core's MVC base classes.
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
the consumer's policy; the recommended order is: the newest fragment of
the same package version on any framework of the same series, then the
newest fragment of the package in that series, with a warning that the
combination is unverified. Never fall back silently.

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
| `read_shape` | `string`, or `select` (a read returns `{key: {value, selected}}`, a write takes the keys comma-separated); `unknown` when the class could not be resolved |
| `multiple`, `required`, `volatile` | as in the model; `volatile` fields are not stored |
| `default` | the model default; absent means empty |
| `options` | static options from the model; dynamic options (interfaces, certificates) only exist on the instance |
| `relations` | entry lists a relation field points to: `source` is the model class as written in the model, with dots and in any case (`OPNsense.quagga.ospf` for `OPNsense\Quagga\OSPF`; PHP class names are case-insensitive), `items` the list path |
| `constraints` | other model settings (`Mask`, `MinimumValue`, `AddressFamily`, ...), as strings; `"(structured)"` marks nested settings that are not represented |
| `gaps` | gap codes: where the derivation is uncertain (concepts, section 8) |

An enum value's availability follows from the variants: an option exists
in the fragments of the variants that list it.

### Operations and actions

`operations` holds one endpoint per operation of a resource. `wrapper`
is the key around the object in request and response bodies
(`{"network": {...}}`); `outer`, where present, is one more key around it
in the response (three os-squid PAC endpoints today). `forced` names
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
when the model saves through its own code (`serializeToConfig`: bind
domains, the firewall filter, os-frr's general settings) or the entries are
stored through `toLegacy`/`fromLegacy` (interface assignments, which drop
the DHCPv6 options unless the IPv6 type is DHCPv6). A field's `gaps` has
`custom-write` when its type converts or normalizes values on write. Both
are read from the PHP classes; they say that a value may be stored
differently from the request, not how. A consumer that reads back after
writing should expect differences there and report them as such.

Endpoints that are not resource operations (service actions such as
`reconfigure`, custom actions, a second endpoint of the same kind) are in
`actions`, keyed by path, with variants by fragment.

### Resources that share data

Several controllers can expose the same model. `acmeclient/accounts`,
`acmeclient/actions` and `acmeclient/settings` all have a settings `set`
on the whole os-acme-client model; `firewall/filter`, `firewall/npt` and
others share the settings of the core's firewall model. `shared_with`
lists the other resources of the package whose API names the same model
class and model path, so the same stored data. A consumer should manage
such data through one of them, usually the one that has both `get` and
`set`; two owners of one object overwrite each other. Over 34 releases,
36 resources in 12 models have `shared_with`. Entry lists inside a shared
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
- `write`: the write cycle (entry lists only). `ok` with `set` in `steps`
  means add, update and delete were each read back; without `set`, the
  update was not tested. `skipped` means not written, and is not a pass.
- `runtime_required`: fields the instance required although the model does
  not mark them as required; `findings` say why a test did not pass.
- `sample`: an entry the instance accepted. Option values in it may only
  exist on the test instance.
- No entry for a fragment: not tested. Only the newest release per series
  is tested on each run; older fragments keep their last result, or none.
- Settings resources are read-tested only; writing settings is not tested
  yet.

Consumers should show this to users ("written and read back on 26.7.9",
"not tested") rather than treat everything as equally reliable.

## What a consumer still decides

The data describes what the firewall does. Some things a client or
provider needs cannot be derived from it, and the format says nothing
rather than guess:

- **Secrets.** OPNsense models do not mark a field as secret. Only
  `UpdateOnlyTextField` is recognisable (written, never returned on read,
  so a reader always sees it empty). Fields such as a `password` or
  `secret` of type `TextField` (more than 200 over all packages) look like
  any other text. A provider has to decide which fields to treat as
  sensitive, for example by name, and should err towards hiding.
- **Value types.** Every value is a string on the wire. `base` and
  `multiple` decide how to present it: `BooleanField` is `"0"`/`"1"`,
  `IntegerField` and `NumericField` are numbers as strings, `multiple`
  fields are comma-separated lists, `UniqueIdField` and `AutoNumberField`
  are set by the firewall. `base` is the nearest class of the core, not
  the root of the hierarchy (`StrictTextField` and `TextField` are both
  bases), so a consumer keeps a small table from base to its own type. A
  change of `base` between variants is often a refactoring in OPNsense
  with no change on the wire.
- **Unknown versions.** Business editions and versions newer than the
  data have no entry; see the fallback order above.

## Compatibility rules

- `format` changes only for changes that break readers: renamed or removed
  keys, changed meaning. Adding keys does not change it; readers must
  ignore unknown keys.
- Fragment hashes are content addresses and do not change. A new generator
  version can produce new fragments for the same package version; the
  older ones stay in the data as long as a catalog state refers to them.
- Resource identifiers and field paths are OPNsense's own names. When
  OPNsense renames something, the old name stays with its fragments and the
  new one appears with the new fragments.

## Not in format 1 yet

- `moved_from`: which field a field replaced (a rename or a move within
  the model). Planned as an additional key, generated from the diffs.
- Write tests of settings resources.
- Per-field verification (today: per resource).
