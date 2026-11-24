# 0015: Publishing rights only in a job that runs no build code

Status: accepted, implemented

## Context

Until 2026-11-18 one job (`publish`) of `pipeline.yml` built the index,
the SBOMs and the capability data with `go run`, and in the same job
uploaded releases and requested provenance attestations. It held
`contents: write`, `id-token: write` and `attestations: write`, and the
workflow token was in the environment of every step. Dry runs from
`promote.yml` received the same rights; only `if:` conditions kept them
from publishing.

`go run` compiles and runs every Go dependency of the generator.
Dependency updates are merged without a person (`automerge.yml`, ADR
0012) after a 7-day cooldown. A compromised dependency that stays
unnoticed for those 7 days would therefore run with a token that can
replace assets of any release, the state release included, and with the
right to request a Sigstore attestation for whatever it uploads; the
attestation would then vouch for manipulated data. `SECURITY.md` names
exactly this class (untrusted input running with the workflow's write
permissions) as in scope.

## Decision

1. **Split by rights.** `prepare` runs everything that executes the
   generator or its dependencies, with `contents: read` and no
   `id-token`. Its result (the publishing plan from `publish.sh plan`,
   staged assets and release notes, checksums, the packed state, the
   capability data) goes to the next job as one archive. `upload` holds
   `contents: write`, `id-token: write` and `attestations: write` and runs
   `gh`, `sha256sum`, `tar`, `jq`, the attestation action and shell
   scripts of the repository; no Go, no mise, nothing of the build.
2. **Upload treats the plan as data.** `publish.sh upload` accepts
   release tags, generator tags, asset names and the commit only in fixed
   shapes, refuses links and a plan made for another commit, and executes
   nothing from the plan.
3. **Dry runs never start a job with `id-token`.** `upload` has a
   job-level condition that is true only for the schedule and a manual run
   with "publish" on. The publishing round trip of `promote.yml` runs in
   its own job (`roundtrip`) with `contents: write` (a draft release) and
   no `id-token`. `promote.yml` still grants the rights to the called
   workflow, because GitHub checks the grant before any job starts.
4. **The token only where `gh` runs.** No workflow-level `GH_TOKEN`; each
   step that calls `gh` gets it. Checkouts keep no credentials
   (`persist-credentials: false`). In `prepare` the token can only read.

## Consequences

- A compromised dependency can still make wrong data (it builds the
  data); it can no longer write anything itself or obtain an attestation,
  and its output is published only by the next live run, after the
  checks of `prepare` and the contract tests. Attestations name what the
  pipeline on `main` produced, as before; they do not prove the data is
  right.
- Amended 2026-11-24 (review finding SEC-1 of 2026-11-24): until then
  `upload` and `roundtrip` unpacked `publish-input.tar` straight into the
  checkout, so a member `tools/ci/publish.sh` replaced the script the job
  ran next with its write token and `id-token`; the sentence above was
  not true. `tools/ci/unpack-input.sh` now accepts only plain files and
  directories below the hand-over paths in `.cache/ci` and refuses the
  whole archive otherwise; the state's `dist.tar.zst` in `roundtrip` must
  not contain links. What stays: a compromised dependency decides the
  content of what `upload` publishes and attests (see the first point).
- `publish.sh upload --dry-run` runs in every dry run against the real
  plan, so the upload's reading of the plan is tested before `main`
  moves, although the upload job itself runs only live.
- Three more jobs and one artifact per run (`publish-input`, kept 7
  days). A job-level condition also decides whether a job runs at all,
  and a skipped job looks like a pass: the first promotion of this split
  (run 44264067994) skipped the round trip because a skipped contract job
  made the default success() false, and still moved `main`. The `upload`
  and `roundtrip` conditions now start with `!cancelled()`, and the job
  `complete` fails a run in which a live upload or a requested round trip
  did not succeed.
- The check of what was published now runs before the state is saved: a
  failure leaves the previous state, so the next run publishes the same
  releases again. `publish.sh` compares the asset list as well as
  `SHA256SUMS` and deletes assets a release no longer lists.

## Evidence

- Review finding SEC-1 and OPS-1 of the architecture review of
  2026-11-17.
- `tools/ci/publish_test.sh` (fake `gh`): a left-over asset with
  matching `SHA256SUMS` is republished and deleted; a bad release name,
  asset name, link or commit in the plan is refused; `unpack-input.sh`
  refuses a script, a link, `..` and another `.cache/ci` path in the
  hand-over archive and writes nothing then. Each was seen failing with
  its rule removed.
- Dry run of the split pipeline on `dev` (workflow_dispatch, publish off):
  `prepare` ran with a read-only token, `upload` did not start.
