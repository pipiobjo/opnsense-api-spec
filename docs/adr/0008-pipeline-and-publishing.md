# 0008: Daily pipeline on hosted runners, GitHub releases as publication and state

Status: accepted (implemented; KVM availability on hosted runners still to be measured)

## Context

A new OPNsense release (patch, hotfix revision under the same number, new
series) should yield a verified, published API description without anyone
doing anything (roadmap goal). Everything up to the contract test was
scripted but nothing ran on its own, and nothing was published.

Constraints:

- No own infrastructure and no secrets. The only token is the workflow's
  `GITHUB_TOKEN`; the test VM never sees anything else (ADR 0004).
- Hosted Linux runners: about 14 GB free disk (more after deleting
  preinstalled toolchains), 6 h per job. `/dev/kvm` is present on hosted
  runners but not guaranteed; the upstream provider runs OPNsense under
  QEMU without KVM.
- `dist/` is 539 MB for 34 releases, 9.7 MB as `tar.zst` (JSON compresses
  well). The package cache is about 0.5 GB for all releases; VM images and
  the proxy cache are the large part (images about 2.2 GB per series).
- A daily run usually finds nothing new; it must be cheap then.

## Decision

**Workflow** (`.github/workflows/pipeline.yml`, daily and on dispatch):
`build` (restore state, gates, `matrix -series all`, client smoke test of
the newest release per series, `plan`) -> `contract` (one job per planned
release, at most 3 in parallel) -> `publish` (only if every job passed) ->
`report` (on any failure). `ci.yml` runs the code gates on every push and
pull request.

**State is the last publication.** The complete `dist/` of the last
successful run is the asset `dist.tar.zst` of the rolling release `state`.
A run restores it and then behaves like a local run: unchanged releases are
skipped (catalog hash and generator version, ADR 0003), the history of
hotfix compositions, fragments and contract test results is kept. Without a
published state the run starts from scratch and says so; nothing counts as
verified then. Packages are cached in the Actions cache only to save
downloads; losing that cache costs time, not correctness.

**What gets contract-tested** (`opnsense-api-spec plan`, `VerifyPlan`): the
newest release of each series whose current composition is not verified.
Verified means: last run on exactly this composition, with this generator,
every package with endpoints tested, no unexplained failure. A failed or
partial run (plugin not installable, core-only run) is not a verification.
Older releases keep `needs_verification` visible in the index and their
release notes; re-testing them after a generator change is a manual
dispatch (`verify: 26.7.5,...`), not a daily cost.

**Acceleration**: KVM if `/dev/kvm` can be opened (udev rule on the
runner), otherwise software emulation (`TESTVM_ACCEL=tcg`, all waits
stretched by `TESTVM_SLOW`). Which one was used is in the job summary and a
warning annotation; it is never silent.

**Publication**: one GitHub release per OPNsense release, tag
`opnsense-<release>`, assets = the release view of `dist/<release>/`
(OpenAPI, IR, coverage, contract test results, composition, diff and quality
report against the previous release). A release is re-published when its
index entry changed (new composition, new generator, new test result, status
change); assets are replaced in place. Pre-release while the current
composition is not verified; "latest" is the newest release when verified.
OCI artifacts (ORAS) come later, from the same files.

**Failure**: nothing is published (`publish` needs all jobs; a planned
release that is still unverified after the contract jobs fails the publish
gate). `report` opens one issue labelled `pipeline-failure` with the job
results and the failing checks per release, or comments on the open one.

**Loud by construction**: `verify` exits non-zero on unexplained findings;
the workflow shell is `bash -eo pipefail` (the implicit default has no
`pipefail`, so `verify | tee` would have passed); golden tests fail instead
of skipping when their inputs are missing (`GOLDEN_REQUIRED=1`, inputs
fetched first).

## Consequences

- The published releases are the product and the input of the next run; a
  broken state asset is repaired by deleting it (full rebuild) or uploading
  a corrected one.
- Tags keep pointing at the commit that first published a release; the
  release notes name the generator commit of the current assets.
- Private repository: hosted runner minutes count against the account's
  quota. A no-op run (state restore, gates, matrix without builds, client
  smoke test) is the daily cost; contract jobs only run for new compositions.
- Accepting an upstream issue stays a reviewed change in
  `verify/accepted.json`; the pipeline never accepts anything on its own.

## Evidence

- Full local check before the pipeline (2026-11-12): 34 releases, tests,
  client smoke test (5094 methods), contract runs with all plugins on
  25.7.11, 26.1.11, 26.7.9. It found two silent errors: `verify` exited 0
  with a failure, and the index counted a failed run as verified.
- While building the pipeline: a core-only run counted as verification of a
  release with 70 plugin packages; now recorded as `untested_packages`.
