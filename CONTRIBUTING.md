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
tools/ci/gates.sh     # exactly what CI runs: gofmt, vet, all tests incl. golden releases
```

[docs/development.md](docs/development.md) describes the full local loop
(all releases, contract tests on the throwaway VM), how to add a
derivation rule, a gap code or a check, and how to run your own copy.

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
  than one run, the narrowest release glob that covers it, and a `match`
  text that only the reviewed failure contains.
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
