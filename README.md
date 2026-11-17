# opnsense-api-spec

Versioned, language-independent API definitions (OpenAPI 3.1) for OPNsense,
generated from the official release packages.

Status: pre-1.0 (see [Stability](#stability)). Core and all plugins, every
release from 25.7 on, rebuilt daily. The newest release of each series is
contract-tested in a throwaway VM with all API plugins installed before it is
published. Not an official OPNsense project.

Background, versioning model, verification and limitations:
[docs/concepts.md](docs/concepts.md). Decisions: [docs/adr/](docs/adr/).
What is missing for full automation: [docs/roadmap.md](docs/roadmap.md).
Changing the generator or running your own copy:
[docs/development.md](docs/development.md).

## What it does

```
pkg.opnsense.org                      extract                    render
MINT/<release>/packagesite  ──►  core (+ os-*) packages  ──►  IR (JSON)  ──►  OpenAPI 3.1
  signed catalog                    model XML, controllers                    + coverage report
```

1. Finds the release snapshot (`<ABI>/<series>/MINT/<release>/latest`).
2. Verifies the catalog like FreeBSD `pkg` does: the signing key must be a
   known OPNsense key (`internal/pkgrepo/verify.go`, list taken from core's
   `fingerprints/OPNsense/{trusted,revoked}`; OPNsense rotates the key per
   series, older series are signed with their rotated key and marked so),
   the signature is RSA PKCS#1 v1.5 over the hex SHA-256 of `packagesite.yaml`.
3. Downloads the packages (zstd or xz) and checks each against its catalog
   checksum (BLAKE2b-512 in pkg base32, or SHA-256 in older catalogs).
4. Reads the shipped MVC sources: model XML (fields, types, defaults,
   options) and API controllers (actions, parameters, generic helper calls).
5. Writes per release (`-core` suffix when built without `-plugins`):
   - `opnsense-<release>.ir.json`: intermediate representation
   - `opnsense-<release>.openapi.json`: OpenAPI 3.1
   - `opnsense-<release>.coverage.md`: what is typed and what is not,
     per derivation source, module and package

## Getting the specs

Every OPNsense release is a GitHub release of this repository, tag
`opnsense-<release>` (for example `opnsense-26.7.9`), with these assets:

| Asset | Content |
|---|---|
| `opnsense-<release>.openapi.json` | OpenAPI 3.1, core and all plugins |
| `opnsense-<release>.ir.json` | intermediate representation (language-neutral) |
| `opnsense-<release>.coverage.md` | what is typed and what is not |
| `opnsense-<release>.verified.json` | contract test results, if this composition was tested |
| `composition.json` | packages and fragments of this catalog state |
| `<previous>.<release>.{json,md}`, `.quality.json` | changes and regressions against the previous release |

A full release means: this exact catalog state (including hotfix revisions)
was contract-tested with the current generator. A pre-release means it was
not; the release notes say why. "Latest" is the newest verified release.

```sh
gh release download opnsense-26.7.9 -R pipiobjo/opnsense-api-spec -p '*.openapi.json'
```

For clients that talk to many firewalls there is also the **capability
data**: every resource, field and action over all versions, with the
package versions they exist in and the contract test results, as plain
JSON ([docs/capabilities.md](docs/capabilities.md)). It is built and
checked on every pipeline run (`go run ./cmd/opnsense-api-spec
capabilities`) and kept as a workflow artifact. Publication as the
release `catalog` is switched off (repository variable
`CATALOG_PUBLISH`) until the planned OpenTofu provider has used the
format; until then consumers build it from the published pipeline state
(see opnsense-api-go's `tools/ci/catalog.sh`).

## Usage

```sh
mise install                       # Go toolchain from mise.toml
go run ./cmd/opnsense-api-spec releases
go run ./cmd/opnsense-api-spec build -release 26.7.5            # core only
go run ./cmd/opnsense-api-spec build -release 26.7.5 -plugins   # core + all os-* plugins
go run ./cmd/opnsense-api-spec matrix -series 25.7,26.1,26.7    # all releases + diffs + dist/index.json
go run ./cmd/opnsense-api-spec diff -from A.ir.json -to B.ir.json
go test ./...
```

## Pipeline and releases

`.github/workflows/pipeline.yml` runs daily ([ADR 0008](docs/adr/0008-pipeline-and-publishing.md)):
new releases and hotfix revisions are built, the newest unverified release
of each series is contract-tested in a throwaway VM with all API plugins,
and only if every gate passed, each changed release is published as GitHub
release `opnsense-<release>` (OpenAPI, IR, coverage, contract test results,
composition, diff). Pre-release means: not contract-tested for this catalog
state. Every release has `SHA256SUMS` with a build provenance
attestation. On failure nothing is published and the next day's run
retries; one self-closing issue labelled `needs-you` appears only when
publishing has been stale for 7 days ([docs/operations.md](docs/operations.md),
[ADR 0012](docs/adr/0012-quiet-operation.md)). Changes reach `main` only
after two dry runs of the pipeline (`promote.yml`).

## Contract tests (throwaway VM only)

```sh
tools/testvm.sh prepare 26.1     # series -> image version (26.1.6), checksum (2 sources) + signature
tools/testvm.sh verify 26.7      # fresh VM, API key, tests, teardown (~1 min with KVM)
tools/testvm.sh verify 26.1 --plugins   # + all plugins with an API (~9 to 15 min)
tools/testvm.sh verify 26.7 --release 26.7.5 --plugins
                                 # update the series image to exactly 26.7.5 first
tools/testvm.sh start 26.7 --release latest --plugins "os-frr"
                                 # leave a VM running for your own tests (.cache/vm/state.env)
```

Other repositories get the same VM in GitHub Actions through the action
`.github/actions/testvm` ([docs/testvm-action.md](docs/testvm-action.md)).

Plugins come from `opnsense-api-spec proxy`, which serves only the signed
package snapshot of the VM's release (`MINT/<release>`) and is reachable from
the VM only as `http://10.99.0.200` (QEMU `guestfwd`, needs `socat`). The
firmware mirror is switched to it through the API.

The VM runs under QEMU/KVM with a fresh overlay per run, four NICs and
isolated user-mode networks (`restrict=on`); the only way in is
`127.0.0.1:18443`. `verify` refuses any other target. It checks every GET
against the model (root key, fields, read shapes) and runs
add -> read back -> update -> read back -> delete per entry list. Results go to
`opnsense-<release>.verified.json` and into the spec:

- `x-opnsense-verified` per operation (instance, date, checks, findings)
- `x-opnsense-runtime-required` on entry Write schemas: fields the instance
  rejected as missing, including PHP-enforced ones the model does not mark
  `Required`
- a request example on each add operation: the entry the instance accepted
  (see [ADR 0009](docs/adr/0009-generated-write-values.md))

Builds with `-plugins` (and `matrix`) also write per-package fragments
(content-addressed), the composition of each catalog state
(`26.7.8_2~e21973a477a4`, status `provisional`/`final`) and `dist/index.json`;
layout and identity rules: [docs/concepts.md, section 5](docs/concepts.md).

Downloads are cached in `.cache/pkg`, unpacked sources in `.cache/src`,
images and the VM in `.cache/images` and `.cache/vm` (about 6.5 GB in total
for the 25.7–26.7 matrix plus one image; safe to delete).

## OPNsense conventions made explicit

| Convention | In the spec |
|---|---|
| Changes take effect only after an apply call | `x-opnsense-apply` on every mutating operation |
| Validation errors come with HTTP 200 | `MutationResult` (`result`, `validations`) |
| Read and write shapes differ (list fields) | `<Model>.Read` / `<Model>.Write`, `SelectMap` |
| Booleans are `"0"` / `"1"` | `enum: ["0", "1"]` |
| Request/response root key | `$internalModelName` for get/set, helper key for entries (`{"blocklist": ...}`) |
| Entry lists are maps keyed by UUID | `additionalProperties` + item schemas `<Model>.<path>.Read/Write/Row` |
| Display values next to raw values (`%field`) | `patternProperties: {"^%": string}` on read schemas |
| Which package provides an operation | `x-opnsense-package` (name, version, `alternatives` for variants such as `os-zabbix6/7/74-agent`) |
| Field names that differ only in case or `_` (`agentPort`, `agent_port`) | `x-opnsense-name-collision`, plus `x-go-name` suffix for Go generators |

## Transparency of limitations

Every operation carries `x-opnsense-derivation`:

- `source: base`: generic base-class action (`getAction`, `setAction`,
  service actions) applied to the bound model. Typed.
- `source: base-call`: thin wrapper around `searchBase`/`getBase`/`addBase`/
  `setBase`/`delBase`/`toggleBase` with literal arguments. Typed.
- `source: signature-only`: only path and parameters are known. Untyped.
- `gaps`: everything the generator noticed but could not resolve
  (overridden actions, custom logic around a helper, HTTP method not
  derivable, no apply endpoint, unresolved field types).

The document also contains `x-opnsense-coverage` and the release gets a
`coverage.md`. Known limitations of this draft:

- PHP is read with patterns, not a full parser.
- Options of dynamic list fields (interfaces, certificates, aliases, model
  relations) depend on the running system and are not enumerated.
- Plugins: `-devel` variants are skipped. Packages shipping identical MVC
  files are treated as alternatives; files with different content from
  several packages are listed under "file conflicts" (first package wins).
  Plugin operations exist only when the plugin is installed.
- Contract tests: the daily pipeline tests the newest release of each
  series with all API plugins; older releases are tested on request. Read
  paths are checked for every GET. Every entry list is added, read back,
  updated, read back and deleted with generated values (26.7.9: 162 of 188
  pass; the others are listed with the reason, mostly relations the test
  cannot satisfy). The accepted entry is the request example of each add
  operation.
- The test client uses HTTP/1.1 on purpose (clients usually use HTTP/2),
  so transfer problems of that protocol show up; large responses were once
  cut over HTTP/1.1 and no longer are on the tested releases.

## Using the spec

A Go client generated with oapi-codegen v2.8.2 compiles for every newest
release of a series (about 5,000 to 5,200 client methods; checked in every
pipeline run, `tools/client-smoke.sh`). oapi-codegen needs
`compatibility.always-prefix-enum-values: true` because some enum values
collide with generated type names. openapi-generator v7.27.0 generates
Python that compiles; its TypeScript output currently fails on enum values
such as `~*` (see [ADR 0007](docs/adr/0007-openapi-3-1.md)).

## Layout

```
cmd/opnsense-api-spec/   CLI (releases, fetch, build, matrix, diff, verify, failures, proxy, index, plan, capabilities)
internal/capability/     capability data per package, catalog, JSON Schema
internal/pkgrepo/        mirror, catalog, signature, key chain, checksums, archives
internal/php/            the one PHP scanner: comments, brackets, namespaces, classes
internal/model/          model XML and field type resolution
internal/controller/     PHP controller patterns, inheritance
internal/ir/             endpoint classification, apply endpoints
internal/gap/            stable gap codes
internal/openapi/        OpenAPI 3.1 rendering, coverage
internal/fragment/       content-addressed fragments per package
internal/assemble/       release views, golden and determinism tests
internal/pipeline/       matrix, compositions, status, index, verification plan
internal/diff/           release-to-release changes (models, fields, endpoints, packages)
internal/quality/        invariants, regression report, framework fingerprint
internal/schemacheck/    OpenAPI 3.1 / JSON Schema 2020-12 validation
internal/verify/         contract tests against a running test instance
internal/jsonfile/       reading and writing the JSON files in one format
verify/accepted.json     reviewed upstream findings that do not count as failures
tools/testvm.sh          throwaway OPNsense VM (QEMU/KVM, software emulation fallback)
.github/actions/testvm/  the VM as a GitHub Action for other repositories
tools/ci/                pipeline helpers (gates, state, publish, round trip, published-file check, failure report, needs-you check)
test/consumer-go/        consumer scenario for the generated Go client
```

Dependencies: `github.com/klauspost/compress` (zstd),
`github.com/ulikunitz/xz` (older catalogs), `golang.org/x/crypto`
(BLAKE2b), `github.com/santhosh-tekuri/jsonschema/v6` (OpenAPI and
capability schema validation) and `golang.org/x/text` (its messages).
Everything else is the Go standard library.

## Stability

Pre-1.0. What consumers can rely on:

- Tag names `opnsense-<release>` and composition ids
  (`26.7.9_1~74eecaac799a`) keep their meaning.
- The meaning of the `x-opnsense-*` extensions and of the gap codes
  (`x-opnsense-derivation.gaps[].code`) does not change silently; changes
  are recorded in the ADRs and the generator version.
- Every output names the generator version that produced it; the matching
  git tag is `v<version>`.

What may still change before 1.0: asset names and layout, the IR format,
how fragments and the index are published, and component names in the
OpenAPI documents. Pin by tag and composition id, and check the generator
version when you update.

The assets of a release are replaced when its catalog state changes
(hotfix revision, plugin update) or the generator changes; the release
notes name the composition and the generator commit.

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md) and
[docs/development.md](docs/development.md). Report security problems privately
as described in [SECURITY.md](SECURITY.md), not in a public issue.

This project is maintained on a best-effort basis; there is no support
promise.

## Licence

BSD 2-Clause License (see [LICENSE](LICENSE)), Copyright (c) 2026,
opnsense-api-spec contributors. This also applies to the generated API
descriptions.

The descriptions are derived from OPNsense (core and plugins, all
BSD-2-Clause, Copyright Deciso B.V. and the respective authors). Anyone
redistributing them must keep the notices and the BSD 2-Clause conditions
and disclaimer; every generated document carries them in `info.license`
and `x-opnsense-notice`, details in [NOTICE](NOTICE). The vendored OpenAPI
3.1 JSON Schema is Apache-2.0. Licence information follows the
[REUSE](https://reuse.software) specification (`reuse lint`).

This project is not affiliated with, endorsed by or sponsored by Deciso B.V.
or the OPNsense project.
