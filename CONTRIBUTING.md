# Contributing

Thank you for looking. This project is maintained on a best-effort basis;
issues and pull requests are welcome, but there is no promise of a
response time.

Before you start, read [docs/concepts.md](docs/concepts.md) (how the
project works and why) and skim the [ADRs](docs/adr/). Most design
questions are answered there.

## Running the checks

```sh
mise install          # Go toolchain from mise.toml
gofmt -l .            # must print nothing
go vet ./...
go test ./...
```

The golden tests need the extracted sources of three final releases; they
are skipped locally without them and required in CI:

```sh
tools/ci/golden-fetch.sh
GOLDEN_REQUIRED=1 go test ./internal/assemble/
```

Building releases and running contract tests (needs QEMU, ideally KVM):

```sh
go run ./cmd/opnsense-api-spec build -release 26.7.9 -plugins
tools/testvm.sh verify 26.7 --release 26.7.9 --plugins
```

## Rules for changes

- **No guessing.** If the generator cannot derive something, it marks it
  as a gap with a stable code (`internal/gap`) instead of filling it in.
- **Tests with real inputs.** A generator fix comes with a test that fails
  without it; use a fixture from a real package where possible.
- **Golden hashes.** If a change alters the derived API of a final
  release, the golden test fails on purpose. Update the hashes only when
  the change is intended, and say why in the commit message. Changes to
  the output also need a new generator version (`internal/ir/ir.go`).
- **Accepted findings** (`verify/accepted.json`) declare an upstream
  behaviour as expected. Each entry needs a reason and evidence from more
  than one run, and the narrowest release glob that covers it.
- **Contract tests only against the throwaway VM** on `127.0.0.1`. Never
  point tests at a real firewall.
- **Licence.** Contributions are under the BSD 2-Clause License
  ([LICENSE](LICENSE)); new files carry the SPDX header used in the
  existing files.

## Pull requests and CI

Pull requests go to the branch `dev` and run the code checks (`ci.yml`).
`main` moves only through `promote.yml`, which runs the code checks and
two dry runs of the daily pipeline on a commit of `dev` before it
fast-forwards `main`. The daily pipeline (`pipeline.yml`) never runs for
pull requests, because it publishes.
Workflows from first-time contributors need a maintainer's approval
before they run.
