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
| F4 | The spec is upgrade safe: a new OPNsense or plugin release produces and publishes a new spec automatically. |
| F5 | The client is released on its own. |
| F6 | Nothing is sent that the firewall's version does not have; a write that did not take effect is an error, never silent (OPNsense ignores unknown keys and answers "saved"). |

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

## MCP server (planned)

| ID | Requirement |
|---|---|
| M1 | An MCP server gives AI agents access to OPNsense. It lives in its own repository, is released on its own, and is built on opnsense-api-go: no HTTP code and no API knowledge of its own. |
| M2 | A small, fixed set of generic tools, not one tool per resource and operation (about 390 resources): `list_resources`, `describe_resource` (fields, types, options and flags from the capability data), `get`, `list`, `add`, `set`, `delete`, `apply`, each taking the resource ID as a parameter. The tool set is the same for every OPNsense version; what exists comes from the connected firewall's data at run time. The provider's generic engine follows the same pattern. |
| M3 | The client's guarantees reach the agent (F2, F6): nothing is sent that the firewall's version does not have, every write is read back, and errors come back as tool errors whose message says what failed and what to do. |
| M4 | Read-only by default. Writing tools exist only when the operator enables them in the configuration; `delete` and `apply` can be enabled separately; an optional allowlist limits the resources or packages a session may touch. |
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
| 2026-11-17 | The provider and the MCP server build on the Go client. Code generators for other languages are not run as long as every consumer is Go ([ADR 0007](adr/0007-openapi-3-1.md)). |
