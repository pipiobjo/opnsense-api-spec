# Development

How to change the generator, test the change, and run your own copy of
the repository. Read [concepts.md](concepts.md) first: it explains what
is derived from where and why. This page explains how.

## Layout

| Path | What it does |
|---|---|
| `cmd/opnsense-api-spec` | the command line (`build`, `matrix`, `verify`, `capabilities`, ...); `-h` lists everything |
| `internal/pkgrepo` | package mirror: catalogs, signatures, key chain (`keys.json`), version order |
| `internal/php` | the one PHP scanner: comments, class bodies, `use` and `namespace` headers |
| `internal/model` | model XML and model classes: fields, types, defaults, model gaps |
| `internal/controller` | API controllers: actions, helper calls, overlays, operation gaps |
| `internal/ir` | the intermediate representation; `GeneratorVersion` |
| `internal/assemble` | sources of one release -> IR; golden tests on three real releases |
| `internal/openapi`, `internal/schemacheck` | IR -> OpenAPI 3.1, checked against the official schema |
| `internal/fragment`, `internal/pipeline` | per-package fragments, compositions, `dist/index.json` |
| `internal/capability` | capability data ([capabilities.md](capabilities.md)) |
| `internal/verify` | contract tests against a throwaway instance; the failure rule (`Status.Failed`) |
| `internal/gap` | gap codes; every code has a row in concepts.md (a test checks) |
| `tools/testvm.sh` | the throwaway OPNsense VM |
| `tools/ci/` | scripts the workflows run; each also runs locally |
| `verify/accepted.json` | upstream behaviour declared as expected, with evidence |

What each test shows: [testing.md](testing.md). A new test is named there
under its claim (`TestEveryTestIsDocumented` checks).

## The local loop

Everything CI does runs on a workstation. Use the pipeline only to move
`main` and to publish.

```sh
mise install
tools/ci/gates.sh                      # gofmt, vet, all tests incl. golden, script tests, coverage
```

`gates.sh` is exactly what `ci.yml` and the pipeline run. It downloads
the sources of the three golden releases once (`tools/ci/golden-fetch.sh`).

A generator change is checked against real releases and a real firewall:

```sh
go run ./cmd/opnsense-api-spec matrix -series all            # every release with plugins, into dist/
tools/testvm.sh verify 26.7 --release latest --plugins       # fresh VM, all plugins, contract test, VM gone
tools/testvm.sh contract-coverage                            # coverage of that contract test, with its floor
go run ./cmd/opnsense-api-spec failures dist/*/opnsense-*.verified.json
go run ./cmd/opnsense-api-spec index
go run ./cmd/opnsense-api-spec capabilities
```

Run `verify` for each supported series (25.7, 26.1, 26.7). It needs QEMU,
ideally with KVM. `failures` prints nothing when nothing failed; that is
the same rule the pipeline uses.

To start from what the pipeline last published instead of building every
release: `tools/ci/state.sh restore` (needs `gh`, read access is enough).

## Changing the derivation

Every rule follows the same steps. The commit a418cb7 is a small complete
example: model classes that override `init` are marked `custom-save`.

1. **Find the cause in the source.** Reproduce the behaviour on the test
   VM with raw API calls and find the PHP code responsible. A behaviour
   you cannot point to in the source is not a rule yet.
2. **Write the rule** in the package that reads that kind of file
   (`internal/model` for model XML and classes, `internal/controller` for
   controllers). Read PHP through `internal/php` (comment-free, per class)
   rather than with a new regular expression on the raw file: comments and
   neighbouring classes otherwise produce false matches.
3. **Add a fixture** under the package's `testdata/`, modelled on the real
   file, and a test that fails without the rule. Break the rule once on
   purpose and watch the test fail (a test that cannot fail proves
   nothing).
4. **Raise `GeneratorVersion`** in `internal/ir/ir.go` when the output
   changes. The golden test then fails; check that the hash changes are the
   intended ones and update them (`internal/assemble/golden_test.go`).
5. **Run all series** on fresh VMs as above. Zero failures, or each
   remaining one explained.
6. **Document** the rule where consumers read it (`capabilities.md` for
   the data, `concepts.md` for the derivation).

### A new gap code

Add the constant to `internal/gap/gap.go` and a row to the gap table in
`concepts.md`; `TestEveryCodeIsDocumented` fails until both exist. Codes
are part of the output format and are never renamed or reused.

### A new field type

Field types are resolved through the class hierarchy of the core and the
plugins (`model.TypeIndex`): a plugin's own field class is followed to the
nearest base class of the core. The read shape is derived from the
`getNodeData`/`getNodeOptions` code of the classes on that path, and
`custom-write` from a `setValue` override, so a new type usually needs no
change. Only the flags `server_set` and `write_only` name base classes
(`Node.ServerSet`, `Node.WriteOnly` in `internal/model/model.go`); a new
core class with that behaviour is added there. If a type cannot be
resolved, the field gets `field-type-unresolved` rather than a guess.

### A new check in the contract test

Checks report a typed `verify.Status`. Whether a result counts as a
failure is decided in one place, `Status.Failed`; the CLI `failures`, the
run summary and the failure report all use it. A new check comes with a
deliberate break that turns it red. A skip names its reason and never
counts as a pass. Tolerances come only from the data (gaps such as
`custom-save`), never from a list of resource names.

### Upstream behaviour that is correct

If OPNsense behaves differently from what the derivation says and the
behaviour is intended upstream, the first choice is a rule that derives
it. Only when that is not possible, an entry in `verify/accepted.json`
with reason, evidence from two runs and the narrowest release glob.

## Version order

Versions are compared by `pkgrepo.CompareVersions`: dot-separated parts,
then the revision after `_`, each numerically and then by letter suffix.
The Go client has the same function with the same test table
(`capability.CompareVersions` in opnsense-api-go). Change both together;
[capabilities.md](capabilities.md#compatibility-rules) makes the order
part of the format.

## Changing the capability format

Format 1 grows only by additive keys. A reader must ignore keys it does
not know, so a new key needs no new format number. A renamed or removed
key or a changed meaning needs `format: 2`, a migration note in
`capabilities.md`, and a period in which both are published. The JSON
Schema (`internal/capability/capability.schema.json`) and its test are
updated with every key.

## Branches and how changes reach `main`

Work goes to the branch `dev`. `promote.yml` runs on every push to `dev`:
the gates, two dry runs of the pipeline on that commit (one with a
publishing round trip on a draft release, one as a day without contract
tests) and then a fast-forward of `main`. A live run of the pipeline
refuses a commit that did not pass `promote.yml`.
[operations.md](operations.md) describes the unattended operation.

## Running your own copy

Nothing in the repository depends on its name or owner when it runs in
GitHub Actions; scripts take the name from `GITHUB_REPOSITORY`. The
default `pipiobjo/opnsense-api-spec` in the scripts is only for local
runs, and `TESTVM_SPEC_REPO` overrides the source of release files in
`tools/testvm.sh`. No secret is needed: everything uses the workflow
token.

1. Fork or copy the repository and create the branch `dev`.
2. Settings -> Actions -> General: workflow permissions read-only;
   allowed actions "selected" with full commit hashes required; approval
   for workflows from fork pull requests. The workflows ask for the write
   permissions they need per job.
3. Rulesets, as in this repository: the default branch without force
   push and deletion; the tags `opnsense-*`, `v*`, `state`, `catalog`
   never updated or deleted.
4. Watch the repository's issues (Watch -> Custom -> Issues); that is how
   the watchdog reaches you.
5. Run `pipeline.yml` by hand with publishing off and read the summary.
   The first live run (scheduled, or by hand with "publish") builds every
   release from scratch, so it takes much longer than a daily run.
6. The capability data is published only when the repository variable
   `CATALOG_PUBLISH` is `true`. Without it the data is built and kept as a
   workflow artifact.
7. Software bills of materials ([ADR 0013](adr/0013-sbom.md)) are
   published only when the repository variable `SBOM_PUBLISH` is `true`;
   dry runs always build and check them. The runner needs Podman (GitHub's
   Ubuntu runners have it).

`internal/pkgrepo/keys.json` holds OPNsense's signing keys; it is the
trust anchor for every downloaded package and is followed through the
signed key chain. A copy keeps it unchanged. The schedules (`pipeline.yml`
at 05:17 UTC, `watchdog.yml` at 17:41 UTC) can be moved freely.
