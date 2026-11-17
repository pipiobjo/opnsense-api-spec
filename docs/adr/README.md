# Architecture decision records

| # | Decision | Status |
|---|---|---|
| [0001](0001-packages-as-source-of-truth.md) | Release packages, not git or docs, are the source of truth | accepted, implemented |
| [0002](0002-explicit-derivation-no-guessing.md) | Pattern-based extraction with explicit derivation levels; never guess | accepted, implemented |
| [0003](0003-fragments-by-api-content.md) | Fragments identified by API content instead of one spec per release number | accepted, implemented (superset view planned) |
| [0004](0004-contract-tests-throwaway-vm.md) | Contract tests against a throwaway, isolated VM with a snapshot-pinned package proxy | accepted, implemented |
| [0005](0005-signing-key-chain.md) | Follow signing key rotation only through OPNsense's signed key chain | accepted, implemented |
| [0006](0006-client-capability-layer.md) | Consumers need a capability layer; the server ignores unknown keys | accepted, implemented (capability data, ADR 0010; consumer: opnsense-api-go) |
| [0007](0007-openapi-3-1.md) | OpenAPI 3.1 as the only output format, with compatibility checks per build | accepted, implemented (Go gate blocking; openapi-generator manual) |
| [0008](0008-pipeline-and-publishing.md) | Daily pipeline on hosted runners; GitHub releases as publication and state | accepted, implemented |
| [0009](0009-generated-write-values.md) | Generated write values, read-back and prerequisite entries in the contract test | accepted, implemented |
| [0010](0010-capability-data.md) | Capability data per package (plain JSON with variants by fragment) instead of an OpenAPI superset | accepted |
| [0011](0011-testvm-action.md) | The test VM as a composite action in this repository, used by the contract job and by other repositories | accepted, implemented |
| [0012](0012-quiet-operation.md) | Quiet operation: one self-closing signal after 7 stale days, `main` only through dry runs, dependencies update themselves | accepted, implemented |

Format: context, decision, consequences, evidence. A superseded ADR stays
and links to its successor.
