# Roadmap

Goal: a new OPNsense release (patch, hotfix revision or series) yields a
verified, published API description without anyone doing anything. Humans
are only involved when something genuinely new happens, and then through an
automatically created report, not an investigation.

State and reasoning: [concepts.md](concepts.md), decisions:
[adr/](adr/README.md). Last review: 2026-11-10.

## What a new release needs today

| Step | Patch release (e.g. 26.7.9) | New series (27.1) | Today |
|---|---|---|---|
| Notice that something is new | `matrix` finds the new `MINT/` directory | same, if the series is listed in `-series` | manual: nobody runs `matrix` |
| Series list | - | `-series` must be extended by hand | manual |
| Signing key | - | the new series is signed with a key not in the static list; the build fails loudly | manual (ADR 0005) |
| Hotfix under the same release number | catalog hash differs -> rebuild | same | automatic, tested against a fake mirror |
| Previous release becomes `final` | next run | same | automatic |
| Contract tests | the VM tests the image version only (26.7), not 26.7.9 | `prepare 27.1`; image naming and announcement path must match | manual, image versions only |
| Detect silent breakage (framework changes) | regression report, framework fingerprint | same | automatic (reports); gate is the contract test |
| Client smoke test (oapi-codegen) | - | - | manual |
| Publish | - | - | missing |

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
3a. **Accepted findings.** A reviewed list (path, check, reason, upstream
   link) so that known upstream issues do not block publishing forever
   (26.7 today: 6 findings, e.g. chunked encoding, captive portal `api`).
4. **Contract tests per release.** Boot the series image, update it through
   the package proxy to exactly the target release, install plugins, run
   `verify`. Today only image versions (26.7, 26.1.6) are tested.
5. **Key chain and series list.** Follow key rotation through the signed
   chain (ADR 0005); derive the series list from the mirror (all series
   from a fixed floor).
6. **Client scenario test in the repository.** The provider-style scenario
   with the generated Go client lives outside the repository; make it a
   blocking gate (ADR 0007), with OpenAPI 3.1 validation.
7. **Pipeline and publishing.** Scheduled workflow, GitHub release assets
   (later OCI), an issue with diff and findings on failure, nothing
   published on failure.
8. **Retention and size.** `dist/` is ~350 MB for 33 releases, the cache
   ~13 GB; rules for what is kept, and a cache that fits a CI runner.

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
  udev rule). Reportedly reliable for public repositories; to be verified
  for this repository. Fallback: software emulation.
- Constraints: about 14 GB free disk. Matrix and contract tests run as
  separate jobs; the image is converted from `.bz2` to qcow2 without keeping
  the unpacked file; one release snapshot per contract job; Actions cache.
- Isolation stays as in ADR 0004; no secrets are needed.

## Later

- Superset view per package with `x-opnsense-availability` and
  `x-opnsense-moved-from` (concepts 9.1).
- Capability layer for the Go client and the provider (ADR 0006).
- Write paths with generated test values instead of server defaults.
- Second derivation: compare endpoint lists with OPNsense's
  `collect_api_endpoints.py`.
- Licence (open; to be decided before the first publication).
