# Security

## Reporting a problem

Please report security problems privately through GitHub: **Security ->
Report a vulnerability** in this repository. Do not open a public issue.

You can expect an acknowledgement within a week. This is a volunteer
project without a support contract; fixes are made on a best-effort basis.

## What is in scope

- The generator and the published documents: anything that could make a
  spec describe something other than what the signed OPNsense packages
  contain, for example a way around the catalog signature, the key chain
  ([ADR 0005](docs/adr/0005-signing-key-chain.md)) or the package
  checksums.
- The pipeline: a way for untrusted input (a pull request, a package, a
  mirror response) to run code with the workflow's write permissions or to
  change published releases.
- The test VM harness: a way for the test instance to reach anything other
  than the package proxy, or for tests to target anything other than
  `127.0.0.1`.

Out of scope: vulnerabilities in OPNsense itself (report them to the
OPNsense project), and the behaviour of third-party code generators.

## How the project protects itself

- No secrets: the pipeline uses only the workflow's own short-lived token,
  with permissions set per job and given only to the steps that call
  `gh`. The generator and its dependencies run in jobs whose token can
  only read; the job that writes releases and requests attestations runs
  no build code and starts only in live runs
  ([ADR 0015](docs/adr/0015-publish-without-build-code.md)).
- Inputs are verified the way FreeBSD `pkg` does it: signed catalog,
  signing keys accepted only through OPNsense's signed key chain, every
  package checked against its catalog checksum. Test VM images are checked
  against two independent checksum sources and a signature.
- Contract tests run only in a throwaway VM without a route to any network
  except a proxy that serves the signed package snapshot of the tested
  release ([ADR 0004](docs/adr/0004-contract-tests-throwaway-vm.md)).
- All third-party actions are pinned by commit hash; publishing jobs never
  run for pull requests; nothing is published when a gate fails.

## Dependencies

| What | Used for | Licence |
|---|---|---|
| `github.com/klauspost/compress` | zstd package archives | BSD-3-Clause |
| `github.com/ulikunitz/xz` | xz package archives | BSD-3-Clause |
| `github.com/santhosh-tekuri/jsonschema/v6` | schema checks of the generated documents | Apache-2.0 |
| `golang.org/x/crypto` (`blake2b` only) | package checksums | BSD-3-Clause |
| `golang.org/x/text` | text normalisation | BSD-3-Clause |
| Tools in CI, isolated in rootless Podman: `oapi-codegen` (consumer gate), `cyclonedx-gomod` (SBOM), `govulncheck`, the official `golang` image pinned by digest; all pinned in `tools/pins.env` | | |
| GitHub Actions: GitHub's own and `jdx/mise-action`, pinned by commit hash | | |

How they are watched: `tools/ci/vulncheck.sh` (govulncheck, in CI and every promotion) fails on a vulnerability that the generator's code reaches and that has a fix, and warns about one without a fix yet; Dependabot alerts are on; Dependabot proposes
updates monthly (grouped, versions at least 7 days old) against `dev`,
and they reach `main` only through promotion. The Go toolchain is the
newest patch release of the minor version in `mise.toml`. Pins that
Dependabot does not update (the `golang` image by digest, the CI tools)
are in `tools/pins.env`; the watchdog checks them daily
(`tools/ci/pins.sh`) and opens the needs-you issue when the image's Go
minor differs from `mise.toml` or is no longer supported, a newer Go patch
release is 7 days old, or a newer tool release 30 days old. The update
itself is still a commit to `dev`.

Build tools: `jdx/mise-action` installs mise only from releases at least
7 days old and verifies the binary against mise's signed checksums; mise
verifies the Go download against go.dev's checksums. Every action is
pinned to the commit of its release tag (checked 2026-11-17: all match,
all at least 6 weeks old except `jdx/mise-action` v5.2.8, released
2026-11-12; later versions come through Dependabot, which waits 7 days).

Review of 2026-11-17 against the OSV database (which includes the Go
vulnerability database): no known vulnerability in code the generator
compiles. The findings for its module graph concern packages it does not
import (`x/crypto/openpgp`, `x/net/html`); the one for Go 1.27.3 (HTTP
server) is fixed in 1.27.4, which CI uses. `test/consumer-go` has old
modules in its graph through `oapi-codegen/runtime`'s optional
middleware; only the runtime, `google/uuid` and `go-jsonmerge` are
compiled, without known vulnerabilities, and it runs only against the
test VM.
