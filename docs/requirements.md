# Requirements

What the project must achieve: this repository (the API description and
capability data), the Go client
[opnsense-api-go](https://github.com/pipiobjo/opnsense-api-go), and an
OpenTofu/Terraform provider and an MCP server, both planned. Each repository's
`docs/testing.md` traces its tests back to these IDs. How the parts work:
[concepts.md](concepts.md).

## Goal

An OpenTofu provider for OPNsense, and an MCP server that gives AI agents
safe access to it. On the way, two things that did not exist: an API
description generated from OPNsense itself, and a Go client that adapts
to the firewall it talks to.

## Functional requirements

| ID | Requirement |
|---|---|
| F1 | The provider works with the OPNsense core and with plugins. |
| F2 | It works with different OPNsense versions and different plugin versions, including plugin versions that differ on the same OPNsense version and change their API. |
| F3 | Spec, client and provider are separate and reusable on their own (a client in another language, an MCP server, a fork). |
| F4 | The spec is upgrade safe: a new OPNsense or plugin release produces and publishes a new spec automatically, with the next daily run after the mirror serves its packages. When that is not possible (a new series the derivation does not handle, packages not served), a person is told within 7 days (watchdog conditions `stale` and `missing`). |
| F5 | The client is released on its own. |
| F6 | Nothing is sent that the firewall's version does not have; a write that did not take effect is an error, never silent (OPNsense ignores unknown keys and answers "saved"). Limits, each documented where it applies: `write_only` fields (passwords, keys) cannot be read back, so their effect is not confirmed; `transformed` fields are compared after OPNsense's normalisation; the raw `Call` method bypasses these checks. |
| F7 | The client has a read-only mode: it reads everything but sends nothing that changes the firewall, for analysis and for assistants that help configure (2026-11-17). OPNsense privileges cannot express this, because each grants reading and writing together. |

## Quality requirements

| ID | Requirement |
|---|---|
| Q1 | Everything is tested automatically in CI. Tests must be able to fail: a check that cannot go red does not count. |
| Q2 | As secure as possible: small attack surface, no secrets where avoidable, verified inputs, TLS verified by default (an explicit switch to disable it exists). |
| Q3 | Documentation lets other people follow the approach, use the parts, and adapt them in a fork, without knowing OPNsense internals or the project's history. |
| Q4 | No routine maintenance: after the initial work it runs by itself. A person is told only when action is needed, with what happened and what to do. |
| Q5 | A stable implementation with a clear architecture before anything is released. |
| Q6 | Design consistency: the same problem is solved the same way; code is reused, not copied. |
| Q7 | Quality evidence is visible: coverage reports with a floor, software bills of materials, and a document per repository that says what is tested, how, and what is not. |

## OpenTofu/Terraform provider (planned)

| ID | Requirement |
|---|---|
| P1 | Secrets never reach the state in clear text where the protocol allows it: `write_only` fields are write-only attributes (not stored in state or plan); fields that look like secrets by name (password, secret, key, psk, token) are at least `Sensitive`. Diagnostics never contain field values (the client's errors name fields only). |
| P2 | Drift: every readable field is compared on refresh. `write_only` fields cannot drift-detect; this is stated in the resource documentation. |
| P3 | Data: the provider embeds one immutable capability data state (`catalog-v1-<data_id>`), verified at build time (checksums and attestation), and reports its `data_id`. Optionally it loads a newer attested state at run time (the user's choice), so a new OPNsense release does not need a provider release. |
| P4 | Verification is visible: a resource or field not verified on the firewall's version gives a warning in plan, not silence. |
| P5 | A firewall on a series newer than the data can be read (refresh, import, data sources) with the newest data, marked unverified; changes are refused with a message that names the data to update. |
| P6 | Apply semantics survive the process: a change saved but not applied (failure, lost answer) is applied on the next run; plan shows it. |
| P7 | Tested like the client (Q1, Q7): acceptance tests on the throwaway VM for every supported series, coverage with a floor, a `docs/testing.md`. |

## MCP server (planned)

| ID | Requirement |
|---|---|
| M1 | An MCP server gives AI agents access to OPNsense. It lives in its own repository, is released on its own, and is built on opnsense-api-go: no HTTP code and no API knowledge of its own. |
| M2 | A small, fixed set of generic tools, not one tool per resource and operation (about 390 resources): `list_resources`, `describe_resource` (fields, types, options and flags from the capability data), `get`, `list`, `add`, `set`, `delete`, `apply`, each taking the resource ID as a parameter. The tool set is the same for every OPNsense version; what exists comes from the connected firewall's data at run time. The provider's generic engine follows the same pattern. |
| M3 | The client's guarantees reach the agent (F2, F6): nothing is sent that the firewall's version does not have, every write is read back, and errors come back as tool errors whose message says what failed and what to do. |
| M4 | Read-only by default, through the client's read-only mode (F7). Writing tools exist only when the operator enables them in the configuration; `delete` and `apply` can be enabled separately; an optional allowlist limits the resources or packages a session may touch. |
| M5 | Credentials come only from the operator's environment or a file, never from tool input, and never appear in tool output or logs. TLS is verified as in the client. No generic raw-call tool: it would bypass every check. Local transport (stdio) first; a network transport only with authentication. |
| M6 | Values read from the firewall (descriptions, names, log lines) are returned as data; the tool descriptions say they are untrusted, because they can contain text that looks like instructions. |
| M7 | Secrets stay hidden: `write_only` fields are never returned, and fields that look like secrets (password, secret, key, by name) are masked in tool output unless the operator allows them. |
| M8 | Every mutation is logged for the operator: tool, resource, ID and the names of the changed fields, without secret values. |
| M9 | Tested like the client (Q1, Q7): protocol tests against a fake firewall, integration tests on the throwaway VM for every supported series, coverage with a floor, a `docs/testing.md`. |

## Constraints

| ID | Constraint |
|---|---|
| C1 | Tests never run against a real firewall; only against throwaway VMs on `127.0.0.1` with keys created inside them. Code refuses other targets. |
| C2 | CI needs no secrets beyond the workflow token; credentials of real firewalls never enter a repository, a test or a log. |
| C3 | Examples and fixtures use documentation addresses only (`192.0.2.0/24`, `example.com`). |

## Decisions that refine the requirements

| Date | Decision |
|---|---|
| 2026-11-15 | Unattended operation: a stopped pipeline is stale, never wrong; one self-closing issue after 7 days without a successful run, nothing for a single failure ([ADR 0012](adr/0012-quiet-operation.md)). |
| 2026-11-15 | The client carries no data and has no data release stream of its own; the provider embeds the data. |
| 2026-11-15 | The capability format is published once the provider has used it. |
| 2026-11-16 | The client is re-tested when this repository publishes new data: daily before the first stable release, weekly or monthly after it. |
| 2026-11-17 | A firewall on a series newer than the data is refused with a typed error; a package older than all data is read-only. |
| 2026-11-18 | After the architecture review: the capability data is published before the provider uses it, as GitHub pre-releases without a stability promise: one immutable release per data state (`catalog-v1-<data_id>`) and a rolling `catalog` for the newest (capabilities.md, "Identity and releases"). Replaces the 2026-11-15 decision to wait for the provider. |
| 2026-11-18 | The provider embeds a fixed data state and can load a newer attested one at run time (P3). |
| 2026-11-18 | A firewall on a newer series can be read with the newest data, marked unverified; writes stay refused (P5; refines 2026-11-17). |
| 2026-11-17 | The provider and the MCP server build on the Go client. Code generators for other languages are not run as long as every consumer is Go ([ADR 0007](adr/0007-openapi-3-1.md)). |
