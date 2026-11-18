# Testing

What has to be true for the published descriptions to be right, which
test shows each part, how we know those tests can fail, and what is not
tested. Requirements (IDs F, Q, C): [requirements.md](requirements.md).
How to run everything locally: [development.md](development.md).

## What has to be true

| # | Claim | Requirement |
|---|---|---|
| S1 | Every input is authentic: package catalogs are signed by a key that chains back to the pinned OPNsense key, packages match the checksums in the signed catalog, images match two independent checksum sources. | Q2 |
| S2 | The derivation is correct and deterministic: the same inputs give byte-identical output, every rule matches what the PHP source does, and what cannot be derived is marked as a gap, never guessed. | F1, F2 |
| S3 | The published documents are valid and usable: the OpenAPI documents against the official OpenAPI 3.1 schema, the capability data against its JSON Schema, and a third-party code generator produces a client that compiles. | F3 |
| S4 | The description matches a real firewall: every endpoint of every installed plugin is read, every entry list with a derivable value is written, read back and deleted, on the newest release of each series. | F1, F2, F6 |
| S5 | Versions are told apart correctly: version order (including hotfix revisions), fragments per package version and framework, selection for a given firewall. | F2 |
| S6 | Publishing is correct: what is published is what was built and checked, with checksums, provenance and a software bill of materials, and only from a commit that passed promotion. | F4, Q2, Q7 |
| S7 | The pipeline runs unattended and tells a person only when needed. | Q4 |
| S8 | The contract test itself is trustworthy: it fails on a mismatch, a skipped check never counts as a pass, it never runs against anything but a loopback VM. | Q1, C1 |

## Test layers

| Layer | Where | Runs | Shows |
|---|---|---|---|
| Unit tests with fixtures | `internal/*/*_test.go`, fixtures in `testdata/` copied from real packages | `tools/ci/gates.sh`: every push, every promotion, every pipeline run | each derivation rule, check and helper |
| Golden releases | `internal/assemble/golden_test.go` on the real sources of 25.7.10, 26.1.10, 26.7.7 | same; `GOLDEN_REQUIRED=1` makes missing inputs fail | determinism and the hash of the derived API; a change to the output must be intended |
| Script tests | `tools/ci/alerts_test.sh` | same | the issue logic of unattended operation |
| Vulnerability check | `tools/ci/vulncheck.sh` (govulncheck) | every push and promotion, not the daily pipeline | no known vulnerability with a fix in code the generator calls | |
| Consumer gate | `tools/client-smoke.sh`: oapi-codegen generates a Go client from the newest release of each series, `go vet` compiles it with a provider-style scenario | every pipeline run, blocking | the documents work for a third-party generator |
| Contract test | `opnsense-api-spec verify` on a throwaway VM with every plugin (`tools/testvm.sh`) | every pipeline run for each release whose current state is not yet verified | the description against a real firewall |
| Publishing round trip | `tools/ci/roundtrip.sh` on a draft release, `tools/ci/check-published.sh` after a live run | every promotion; every live run | names, checksums and provenance as GitHub stores and serves them |

## Claims and their tests

| Claim | Tests |
|---|---|
| S1 | `TestVerifyCatalog`, `TestVerifyCatalogRefusesBadKeyMaterial`, `TestVerifyCatalogRevokedKeyOnlyForOlderSeries`, `TestEmbeddedKeysCoverPublishedSeries`, `TestRevokedKeyRefusedForNewSeries`, `TestVerifyPackage`, `TestSumHash`, `TestEncodePkgBase32`, `TestKeysHaveOneTrusted`, `TestLearnKeysOneWay`, `TestKeyChainAcrossSeries`, `TestUnknownKeyFailsLoudly`, `TestStaleCachedPackageIsRefetched`, `TestProxyRefreshesCatalog`, `TestReadArchiveZstd`, `TestReadArchiveXZ`, `TestParseCatalog`; image checks in `tools/testvm.sh prepare` |
| S2 | `TestGolden`, `TestDeterminism`, `TestWriteSideOnRealSources`, `TestLoadAllFixture`, `TestWriteSideGaps`, `TestServerSetAndWriteOnly`, `TestResolveSettings`, `TestResolveService`, `TestSnake`, `TestOverlay`, `TestClassify`, `TestStripCommentsKeepsStrings`, `TestMatchSkipsStrings`, `TestSplitArgs`, `TestQualify`, `TestClasses`, `TestMayDiffer`, `TestHasAndFirst`, `TestEveryCodeIsDocumented`, `TestFingerprintIgnoresCommentsAndWhitespace`, `TestCompare`, `TestInvariants`, `TestCompareSafesearchMove`, `TestLiveSummary` (needs extracted sources) |
| S3 | `TestValidate`, `TestValidateDocument`, `TestClassification`, `TestRenderWrappersAndSchemas`, `TestRenderFragmentUsesCommon`, `TestRenderVerifiedSample`, `TestSchemaRejects`, `TestWrite`, `TestDataID` (the data state names itself; any changed bit gives another ID), `TestRoundTripAndFormat`; `tools/client-smoke.sh` |
| S4 | the contract test on the VM; its building blocks: `TestCandidates`, `TestCandidatesUseDocumentationAddressesOnly`, `TestPHPRegex`, `TestSameValue`, `TestWriteValue`, `TestCompareNode`, `TestCompareNodeTypeErrors`, `TestCompareNodeEmptyModel`, `TestCheckModelGetShape`, `TestEntryGet`, `TestWriteCycleGeneratesValues`, `TestWriteCycleDetectsIgnoredField`, `TestWriteCycleCreatesPrerequisite`, `TestWriteCycleUnlocksBeforeDelete`, `TestCycleFamilies`, `TestSettingsWriteBack` |
| S5 | `TestCompareVersions` (same table as the Go client), `TestSplitCoversEverythingOnce`, `TestHash`, `TestComposeAndStatus`, `TestBuildVariantsAndSelect`, `TestSelectConflict`, `TestFamilies`, `TestWriteSideGapsInData`, `TestServerSetWriteOnlyInData`, `TestFirstSeen`, `TestFetchAttribution`, `TestUnavailablePackages` |
| S6 | `TestForRelease` (SBOM valid against the official CycloneDX 1.6 schema, real hashes, deterministic), `TestForReleaseRefusesBadInput`, `TestMatrixLifecycle`, `TestDecide`, `TestVerifyPlan`, `TestNeedsVerification`, `TestUntested`, `TestVerification`; `tools/ci/roundtrip.sh`, `tools/ci/check-published.sh`; the "only promoted commits publish" step of `pipeline.yml` |
| S7 | `tools/ci/alerts_test.sh` (41 cases: one failed day sends nothing, 7 days open one issue, a new workflow that has not run yet sends nothing, a release without current data for 7 days is named, recovery closes it, ...) |
| S8 | `TestNewClientRefusesOtherHosts`, `TestPackageListUsesHTTP2ChecksUseHTTP1` (checks over HTTP/1.1 so a cut answer shows; the package list over HTTP/2 so detection does not depend on it), `TestApplyAccepted`, `TestWriteCycleWithoutPrerequisiteIsSkipped`, `TestWriteCycleUpdateRejectedIsNotVerified`; the failure rule `Status.Failed` used by the CLI, the run summary and the report |

`TestEveryTestIsDocumented` fails when a Go test exists that this page
does not name, or the page names one that does not exist.

## How we know a test can fail

Every rule and check is introduced with a test that was seen failing:
the code is changed so the claim becomes false, the test must go red,
then the change is reverted. Recent examples: skipping the catalog
signature check, accepting a key that is not RSA, a gap code without
documentation, a field rule that matched a commented-out function. The
golden hashes fail on any change of the derived API of three real
releases. Commit messages record the checks ("seen failing for ...").

## Coverage

`tools/ci/coverage.sh` reports statements covered per package (CI
summary, HTML artifact) and fails below the floor in
`tools/ci/coverage-floor.txt`. Measured with `-coverpkg=./...`.

| Profile | Covered | Floor | Where |
|---|---:|---:|---|
| unit (all Go tests, golden releases included) | 80.4% | 80% | `gates.sh`, artifact `coverage-unit` |
| contract (the command and `internal/verify` against a VM with every plugin; `internal/verify` alone 84.4% to 84.9%) | 72.4% | 72% | pipeline contract job, artifact `coverage-contract-<release>` |

The contract test's main loop (`internal/verify`, 66% in unit tests)
runs against the VM; together the two profiles reach 87% of the
repository. The command line (`cmd/`, 14% in unit tests, 45% with the VM
run) is glue that the pipeline runs end to end. The pipeline's shell
scripts (publishing, state, assets) have no coverage measurement; they
are checked by effect: two dry runs and a draft-release round trip
before `main` moves, and a check of what was published after each live
run. Coverage says which
code ran, not whether its result was checked; the claims and the
deliberate breaks above are what make the tests count.

## Results of the contract test

The newest result per release is published with each release
(`opnsense-<release>.verified.json`) and summarised in the capability
data. Most recent local run on fresh VMs with every plugin, generator
0.9.3: 0 failures on 25.7.11_9, 26.1.11_10 and 26.7.9_3.

## What is not tested

- Only the newest release of each series is contract-tested on each
  run; older releases keep their last result, or have none.
- Entry lists for which no value can be derived that the firewall
  accepts (they need certificates, hardware or other objects first) are
  read, not written; the published result says `skipped` with the reason.
- Settings are written back with their current value, so a setting that
  is valid only together with others is not changed.
- Responses of custom actions (no model behind them) are checked for
  reachability, not for shape.
- Business editions (no public images).
