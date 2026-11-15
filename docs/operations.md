# Operations: how problems are reported

The pipeline runs unattended. Nobody is expected to watch it; every
situation that needs a person arrives as a GitHub issue with a label, the
cause and what to do. Watching the repository's issues (Watch -> Custom ->
Issues) is enough to be told.

| Label | Opened by | Means | Closes |
|---|---|---|---|
| `pipeline-failure` | `pipeline.yml`, job `report` | a gate or contract test failed; nothing was published | by hand, after the fix |
| `watchdog` | `watchdog.yml`, daily | `pipeline.yml` is disabled, or its last successful scheduled run is older than 3 days | by itself, when the pipeline succeeds again |
| `attention` | `pipeline.yml`, step "Coverage alarm" | the derivation lost typing in a release that was just built (an operation became untyped, or a field's read shape became unknown); not blocking | by hand |
| `digest` | `watchdog.yml`, Mondays | one issue, rewritten in place: runs, publications, verification per series, warnings and open issues of the last 7 days | stays open |

Rules that keep the noise down:

- One open `pipeline-failure` issue at a time; later failures add a
  comment.
- One open `watchdog` issue at a time; it is closed with a comment once a
  scheduled run succeeds again.
- A coverage alarm is raised once per release and set of regressions, and
  only for releases whose catalog state changed in that run; older
  regressions stay in the per-release `*.quality.json` files.
- The digest is edited, not re-posted, so it sends no notification. It is
  for a glance, not for action.

## What to do

| Issue | Action |
|---|---|
| `pipeline-failure` | Read the findings in the issue. A generator bug: fix it. An upstream behaviour that is correct: add it to `verify/accepted.json` with reason and evidence from two runs. Close the issue when the next run passes. |
| `watchdog` | If a `pipeline-failure` issue is open, that is the cause. Otherwise check the Actions tab: a disabled workflow (a schedule disabled by GitHub for inactivity is re-enabled automatically; one disabled by hand is not), an account or billing problem, or a GitHub outage. |
| `attention` (coverage) | Read the diff of the release named in the issue for the listed controllers or fields. Extend the derivation patterns, or confirm the upstream code became custom. |
| `digest` | Nothing, unless something in it looks wrong. |

## Keeping the schedules alive

GitHub disables scheduled workflows of public repositories after 60 days
without repository activity. The pipeline re-enables itself and the
watchdog on every scheduled run, and the watchdog re-enables the pipeline
when GitHub disabled it. If both stop at once, only an outside check
notices, for example monitoring that the release `catalog` or `state` is
younger than a few days.

## Running the checks by hand

```sh
GITHUB_REPOSITORY=pipiobjo/opnsense-api-spec ALERTS_DRY_RUN=1 tools/ci/alerts.sh watchdog
GITHUB_REPOSITORY=pipiobjo/opnsense-api-spec ALERTS_DRY_RUN=1 tools/ci/alerts.sh digest
tools/ci/alerts_test.sh
```

`ALERTS_DRY_RUN=1` prints the issue it would open or edit instead of doing
it. The digest also runs on request: Actions -> watchdog -> Run workflow ->
"Also rewrite the weekly digest now".
