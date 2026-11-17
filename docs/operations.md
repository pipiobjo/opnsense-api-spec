# Operations

The pipeline runs unattended, and nobody is expected to watch it
([ADR 0012](adr/0012-quiet-operation.md)). Watching the repository's
issues (Watch -> Custom -> Issues) is enough to be told when a person is
needed.

## When a stopped pipeline is urgent

Published releases never become wrong. When the pipeline stops, new
OPNsense releases and plugin updates are simply not described yet. For a
new hotfix or plugin update of a known series, consumers use the nearest
older data and read back every write: not urgent, and a single failed run
sends no message; the next day's run is the retry.

A **new series** (for example 27.1, twice a year) is different: consumers
refuse to write to it until data for it exists (capabilities.md,
"Selecting the fragments"). From its release, the chain must deliver data
within the time set in requirements.md (F4). The watchdog reports a
release the mirror lists but the data does not cover after 7 days (table
below).

## The one issue: `needs-you`

`watchdog.yml` runs daily and keeps at most one open issue labelled
`needs-you`. It opens it when one of these is true:

| Condition | Clears when |
|---|---|
| No successful scheduled run of `pipeline.yml` for 7 days | a scheduled run succeeds |
| `pipeline.yml` is disabled by hand (a workflow GitHub disabled for inactivity is re-enabled without a message) | it is enabled again |
| A release whose catalog state the pipeline first built in the last 14 days lost typing against the release before it (`first_seen` in the index) | 14 days later |
| A release on the mirror has had no current data for 7 days: never built (packages not served; the important case is a new series) or kept on an old build (a rebuild could not get its packages) | data for it is built |

The issue is edited while the condition lasts and closed by the watchdog
when it is over. Nobody closes it by hand. Its text has four parts: what
is stale, why (the newest failed run with its failed jobs and steps), what
happens if nobody acts, and how to start the fix.

Not sent: an issue per failed run, digests, notification tests, dependency
pull requests that pass their checks.

## Fixing the cause

Start a session in this repository with the issue link. Typical causes:

| Cause | Fix |
|---|---|
| Contract test finding | generator bug: fix it; correct upstream behaviour: an entry in `verify/accepted.json` with reason and evidence from two runs |
| New series with a different layout (image name, key, ABI) | adjust `tools/testvm.sh` or the key chain |
| GitHub platform change | update the pinned action or step |
| Typing lost | read the release's diff for the listed controllers; extend the derivation, or confirm that upstream made them custom |

Every fix goes to the branch `dev`. `promote.yml` tests it with the code
gates and two dry runs of the pipeline and then moves `main`; the next
scheduled run closes the issue.

## Changes and dependency updates

- `main` changes only through `promote.yml`; a live run refuses a commit
  that did not pass it. A failed promotion notifies nobody; `main` stays
  where it was.
- Dependabot opens one grouped pull request per month for Go modules and
  for actions, against `dev`, for versions at least 7 days old.
  `automerge.yml` merges it when `ci.yml` passed and starts `promote.yml`.
- The Go toolchain is the newest patch release of the minor version in
  `mise.toml`.

## Keeping the schedules alive

GitHub disables scheduled workflows of public repositories after 60 days
without repository activity. The pipeline re-enables itself and the
watchdog on every scheduled run, and the watchdog re-enables the pipeline.
If both stop at once, only an outside check notices, for example
monitoring that the newest `opnsense-*` release is not much older than the
newest OPNsense release.

## Checking by hand

```sh
GITHUB_REPOSITORY=pipiobjo/opnsense-api-spec ALERTS_DRY_RUN=1 tools/ci/alerts.sh check
tools/ci/alerts_test.sh
```

`ALERTS_DRY_RUN=1` prints the issue it would open, edit or close instead
of doing it.
