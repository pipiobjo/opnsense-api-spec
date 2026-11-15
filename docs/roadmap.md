# Roadmap

Goal: a new OPNsense release (patch, hotfix revision or series) yields a
verified, published API description without anyone doing anything. Humans
are only involved when something genuinely new happens, and then through an
automatically created report, not an investigation.

State and reasoning: [concepts.md](concepts.md), decisions:
[adr/](adr/README.md). Last review: 2026-11-15.

## What a new release needs today

| Step | Patch release (e.g. 26.7.9) | New series (27.1) | Today |
|---|---|---|---|
| Notice that something is new | `matrix` finds the new `MINT/` directory | `-series all` | daily workflow |
| Series list | - | `-series all` finds it | automatic |
| Signing key | - | learned from the last patch release of the previous series | automatic (ADR 0005) |
| Hotfix under the same release number | catalog hash differs -> rebuild | same | automatic, tested against a fake mirror |
| Previous release becomes `final` | next run | same | automatic |
| Contract tests | `testvm.sh verify 26.7 --release 26.7.9 --plugins` | `prepare 27.1` (image naming and announcement path must match) | daily workflow, newest unverified release per series |
| Detect silent breakage (framework changes) | regression report, framework fingerprint | same | automatic (reports); gate is the contract test |
| Client smoke test (oapi-codegen) | `tools/client-smoke.sh` | same | daily workflow, newest release per series |
| Publish | GitHub release per release | same | daily workflow, only after all gates |

What stays human by nature (and must be reported automatically): adapting
the generator when OPNsense restructures its framework, an emergency key
rotation outside the signed chain, reviewing golden diffs of our own
generator changes.

## Gaps, ranked by correctness risk

1. ~~**Orchestration without tests.**~~ Done (2026-11-11): `internal/pipeline`
   with tests against a fake mirror (signed catalogs, packages, listings):
   first build, no-op run, hotfix under the same release number, a new
   release making the previous one final, stale cached package, unknown
   key. A mutation check (catalog changes ignored) fails 6 assertions.
2. ~~**Gap codes instead of text.**~~ Done (2026-11-11): `internal/gap` with
   stable codes; behaviour depends only on codes (`gap.MayDiffer`); the
   verifier reports runtime-required fields as a structured field instead
   of text parsed by the renderer.
3. ~~**Silent breakage on new releases.**~~ Done (2026-11-11):
   invariants stop the build; regression report and framework fingerprint
   per release; unknown setting tags reported, typed elements always fields
   (this found one more skipped field, `Type` in `os-caddy`, which had been
   misreported as an upstream issue). Measured: framework files change in
   21 of 32 transitions, so these are reports, not gates; the gate is the
   contract test per new composition (concepts 8.1). The index records
   which composition was verified (`needs_verification`).
3a. ~~**Accepted findings.**~~ Done (2026-11-11): `verify/accepted.json`
   (path, check, release glob, reason, evidence, optional upstream link);
   matching results become `accepted`. With the current generator 26.7 and
   26.1.6 (all 69 API plugins) have 0 failures left: 2 accepted each
   (captive portal client API by design, chunked encoding over HTTP/1.1,
   os-udpbroadcastrelay HTTP 500 on 26.1.6). New entries need review.
4. ~~**Contract tests per release.**~~ Done (2026-11-11):
   `tools/testvm.sh verify <series> --release <target> --plugins` boots the
   series image, updates it through the proxy to exactly the target
   (packages from the target snapshot, FreeBSD base/kernel sets from the
   series), installs all API plugins and runs `verify`. 26.7 -> 26.7.5:
   69/69 plugins, 0 failures, 7 minutes. Plugin success is taken from the
   installed package list, job completion from the firmware lock.
5. ~~**Key chain and series list.**~~ Done (2026-11-11): the signed chain is
   re-derived on every run (ADR 0005); `matrix -series all` takes every
   series from `-series-floor` (25.7) on the mirror.
6. ~~**Client scenario test in the repository.**~~ Done (2026-11-11):
   `test/consumer-go/` (own module, pinned oapi-codegen v2.8.2 and runtime
   v1.8.0) and `tools/client-smoke.sh`; OpenAPI 3.1 validation of every
   release and fragment document stops the build. The gate found a
   fragment without operations rendered with `tags: null`, and fragment
   renderings that were never refreshed after generator changes (now
   rewritten on every build; orphaned fragments are pruned).
7. ~~**Pipeline and publishing.**~~ Implemented (2026-11-12, ADR 0008):
   `pipeline.yml` (daily: state restore, gates, matrix, client smoke test,
   contract tests per unverified newest release, publish, failure issue),
   `ci.yml` (code gates per push). GitHub releases `opnsense-<release>`,
   state as `dist.tar.zst` on the release `state`. Found on the way: a
   failed run and a core-only run counted as verified. Dry run on GitHub
   passed (1 h 38 min from scratch; KVM on 2 of 3 contract jobs, TCG
   fallback 77 min). First unattended publishing run 2026-11-13. The
   first runs without contract jobs (2026-11-14, -15) failed in the publish
   job (artifact layout), fixed. OCI later.
8. ~~**Generated write values.**~~ Done (2026-11-15, ADR 0009): 162 of 188
   entry lists on 26.7.9 pass add -> read back -> update -> read back ->
   delete (was 20); found two published generator errors and a wrong
   endpoint choice in the verifier.
9. **Availability data for consumers.** Implemented (2026-11-15, ADR 0010,
   [capabilities.md](capabilities.md)): one JSON document per package with
   variants by fragment, exact fragment selection, verification results;
   built and validated on every run, release `catalog` with `SHA256SUMS`
   and attestations. Publishing waits for the format review. Open:
   `moved_from`, `SHA256SUMS` and attestations for the per-release assets.
10. **Retention and size.** `dist/` is ~540 MB for 34 releases (10 MB
    compressed); the Actions caches about 2.6 GB. Open: keeping the packages
    of final releases, because the mirror can withdraw them.

## Target pipeline

```
daily:
  discover (all series >= 25.7 from the mirror)
  -> extend the key chain (a new key only through the signed chain)
  -> matrix (catalog hash -> rebuild only what changed; update status)
  -> gates: unit, golden, determinism, invariants, ratchet, fingerprint,
            OpenAPI validation, oapi-codegen + compile + scenario test
  -> per new composition: contract test
       (series image -> update through proxy to target release -> plugins -> verify)
  -> publish  |  on failure: issue with diff and findings, nothing published
```

### Runner

GitHub-hosted Linux runners, no self-hosted runner:

- The upstream provider already runs OPNsense under QEMU on `ubuntu-latest`
  (software emulation, no KVM), so it works without own infrastructure.
- GitHub provides KVM on hosted Linux runners (introduced for Android
  emulation; the runner user must get access to `/dev/kvm`, usually via a
  udev rule). Measured in a private repository: 4 of 6 contract jobs had
  KVM (about 14 min with all plugins), the others ran under software
  emulation (77 to 104 min). Both work; which one was used is reported.
- Constraints: about 14 GB free disk. Matrix and contract tests run as
  separate jobs; the image is converted from `.bz2` to qcow2 without keeping
  the unpacked file; one release snapshot per contract job; Actions cache.
- Isolation stays as in ADR 0004; no secrets are needed.

## Later

- `moved_from` in the capability data (generated from the diffs).
- Capability layer for the Go client and the provider (ADR 0006).
- Second derivation: compare endpoint lists with OPNsense's
  `collect_api_endpoints.py`.
