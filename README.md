# opnsense-api-spec

Versioned, language-independent API definitions (OpenAPI 3.1) for OPNsense,
generated from the official release packages.

Status: draft. Core and plugins, all releases from 25.7 on, contract-tested
against throwaway 26.7 and 26.1.6 instances with all 69 API plugins installed.
Not an official OPNsense project.

Background, versioning model, verification and limitations:
[docs/concepts.md](docs/concepts.md). Decisions: [docs/adr/](docs/adr/).
What is missing for full automation: [docs/roadmap.md](docs/roadmap.md).

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

## Contract tests (throwaway VM only)

```sh
tools/testvm.sh prepare 26.1     # series -> image version (26.1.6), checksum (2 sources) + signature
tools/testvm.sh verify 26.7      # fresh VM, API key, tests, teardown (~45 s)
tools/testvm.sh verify 26.1 --plugins   # + all plugins with an API (~9 min)
```

Plugins come from `opnsense-api-spec proxy`, which serves only the signed
package snapshot of the VM's release (`MINT/<release>`) and is reachable from
the VM only as `http://10.99.0.200` (QEMU `guestfwd`, needs `socat`). The
firmware mirror is switched to it through the API.

The VM runs under QEMU/KVM with a fresh overlay per run, four NICs and
isolated user-mode networks (`restrict=on`); the only way in is
`127.0.0.1:18443`. `verify` refuses any other target. It checks every GET
against the model (root key, fields, read shapes) and runs
add -> get -> delete per entry list. Results go to
`opnsense-<release>.verified.json` and into the spec:

- `x-opnsense-verified` per operation (instance, date, checks, findings)
- `x-opnsense-runtime-required` on entry Write schemas: fields the instance
  rejected as missing, including PHP-enforced ones the model does not mark
  `Required`

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
- Contract tests: 26.7 and 26.1.6 with all plugins. Write cycles only use
  server defaults; entry types that need real values are reported as
  skipped (with the runtime-required fields).
- Large responses over HTTP/1.1 can arrive with broken chunked encoding
  (OPNsense web server); the test client uses HTTP/1.1 on purpose so this
  stays visible.

## Using the spec

Generated Go client (oapi-codegen v2.4.1) for 26.7.5 core + plugins
compiles (oapi-codegen v2.8.2, 5094 client methods). oapi-codegen needs
`compatibility.always-prefix-enum-values: true` because some enum values
collide with generated type names. openapi-generator v7.27.0 generates
Python that compiles; its TypeScript output currently fails on enum values
such as `~*` (see [ADR 0007](docs/adr/0007-openapi-3-1.md)).

## Layout

```
cmd/opnsense-api-spec/   CLI (releases, fetch, build)
internal/pkgrepo/        mirror, catalog, signature, checksums, archives
internal/model/          model XML and field type resolution
internal/controller/     PHP controller patterns, inheritance
internal/ir/             endpoint classification, apply endpoints, gaps
internal/openapi/        OpenAPI 3.1 rendering, coverage
internal/diff/           release-to-release changes (models, fields, endpoints, packages)
internal/verify/         contract tests against a running test instance
tools/testvm.sh          throwaway OPNsense VM (QEMU/KVM)
```

Dependencies: `github.com/klauspost/compress` (zstd),
`github.com/ulikunitz/xz` (older catalogs), `golang.org/x/crypto` (BLAKE2b). Everything else is the Go standard library.
