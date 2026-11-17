# 0012: Quiet operation: stale, never broken

Status: accepted 2026-11-15, implemented

## Context

The pipeline is meant to run without a person. In its first week it did
not feel that way:

- A failed run opened an issue and sent an e-mail at once, and the issue
  stayed open until someone closed it by hand, even after the next run
  passed.
- Four kinds of signal (`pipeline-failure`, `watchdog`, `attention`,
  `digest`) plus notification tests: more to read, nothing to decide.
- Changes were pushed straight to `main`, which the nightly run uses. Two
  of the first three scheduled runs failed, both because of such a change:
  the path of a day without contract tests had not been run before.
- Dependency updates had no automation; 16 security alerts were closed by
  hand.

What makes a quieter design safe: nothing downstream breaks when the
pipeline stops. Published releases stay valid, and consumers handle
OPNsense versions newer than their data (nearest known data, read-back of
every write). A stopped pipeline makes data older, not wrong.

## Decision

1. **One signal.** At most one open issue labelled `needs-you`, written by
   `watchdog.yml` (daily), for three conditions: no successful scheduled
   run for 7 days; `pipeline.yml` disabled (other than for inactivity,
   which is re-enabled silently); a release whose catalog state the
   pipeline first built in the last 14 days (`first_seen`) lost typing
   against the release before it. The issue says what is
   stale, why (the newest failed run, its failed jobs and steps), what
   happens if nobody acts, and how to start the fix. It is edited while
   the condition lasts and **closed by the watchdog when it is over**.
2. **A failed run notifies nobody.** Its findings go to the run's summary
   page, which the issue links. The next day's run is the retry.
3. **`main` moves only through `promote.yml`.** Work lands on `dev`.
   `promote.yml` runs the code gates and the whole pipeline as a dry run
   twice on that exact commit (as planned, and with no contract tests, the
   no-change path), then fast-forwards `main`. A live run of
   `pipeline.yml` first checks that its commit passed `promote.yml` and
   publishes nothing otherwise. (A ruleset cannot do this for a personal
   repository: GitHub Actions cannot be exempted from an update rule, and
   a required-checks rule rejected the promotion's own push although all
   three checks were green, 2026-11-15.) `pipeline.yml` is live only on
   its schedule or a manual run with "publish" on; a call from
   `promote.yml` can never publish.
4. **Dependencies update themselves.** Dependabot opens one grouped pull
   request per ecosystem per month against `dev`, for versions at least 7
   days old. `automerge.yml` merges it when `ci.yml` passed and starts
   `promote.yml`. The Go toolchain follows the newest patch release of its
   minor version (`mise.toml`), so Go security fixes need no commit.
5. **Published files are checked as a consumer gets them.** Every release
   gets `SHA256SUMS` with a build provenance attestation; the last step of
   a live run downloads everything it published without credentials and
   checks both.

Removed: the `pipeline-failure`, `watchdog`, `attention` and `digest`
issues, and the notification test workflow.

## Consequences

- A normal month produces no message. A real problem reaches a person
  after a week, at the earliest; nothing breaks in that week.
- A failure of `promote.yml` concerns only whoever pushed to `dev`; it
  notifies nobody and leaves `main` where it was.
- A typing loss is reported only for new releases, and only for 14 days.
  Older losses stay recorded in each release's `*.quality.json`.
- A commit pushed to `main` by hand is not published until it passed
  `promote.yml`.
- Before `main` moves, `promote.yml` uploads the newest release to a draft
  release with the publishing code, checks names and checksums as GitHub
  stored them, and deletes the draft. A fake of GitHub could not have
  caught the renaming below; the real round trip does (reintroducing the
  bug makes it fail with the six renamed files, 2026-11-16).
- Releases whose published `SHA256SUMS` differ from the files are
  republished by the next run. The first live run (2026-11-16) found that
  GitHub stores `26.7.8..26.7.9.json` as `26.7.8.26.7.9.json`; assets are
  now staged under GitHub's name before checksumming.

## Evidence

- `tools/ci/alerts_test.sh`: 26 checks, including "one failed day sends
  nothing", "just under 7 days sends nothing", "recovery closes the
  issue"; three deliberate breaks of the script were caught.
- A dry run of `alerts.sh check` against this repository on 2026-11-15
  found typing losses in six historic releases; this led to the 14-day
  window. The release creation date proved useless as "new" (34 releases
  were created at once), so each catalog state records when the pipeline
  first built it (`first_seen`), and states from before have none.
- `tools/ci/check-published.sh` against a local server: passes on intact
  files, fails on a changed one.
