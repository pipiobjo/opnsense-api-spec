# 0011: The test VM as a composite action in this repository

Status: accepted 2026-11-15, implemented

## Context

The throwaway VM of ADR 0004 (`tools/testvm.sh` and the package proxy) is
needed by three repositories: this one for contract tests, a Go client for
integration tests, a provider for acceptance tests. Copying it would mean
three implementations of the isolation rules (127.0.0.1 only, no route out,
verified images, snapshot-pinned packages) that drift apart.

Options: a composite action in this repository; a separate repository for
the harness; a container image with QEMU inside.

## Decision

A composite action, `.github/actions/testvm`, in this repository. Other
repositories use it pinned by commit hash. It calls `tools/testvm.sh`,
which gained `start` (boot, key, update, plugins; the VM keeps running and
`.cache/vm/state.env` holds the outputs) and `contract` (contract tests
against the running VM). The daily contract job uses the action itself, so
the code path consumers rely on is exercised every day with all plugins.
A second workflow (`testvm-action.yml`) uses it the way another repository
does: no build data, one plugin, checks through the API with the returned
key, and that `stop` leaves nothing behind.

For `plugins: all`, the list of plugins comes from the release's published
IR, so consumers need nothing from this repository's build.

## Consequences

- One implementation of the isolation rules; a fix reaches all users when
  they move their pin.
- The action's inputs and outputs are an interface
  ([testvm-action.md](../testvm-action.md)); changes must stay compatible
  or be announced.
- A separate repository is not needed now: the action lives next to the
  proxy it depends on, and both change together.
- A container image is not used: QEMU with KVM needs the host's device
  anyway, and the runner's packages are enough.
- The base image cache key changed (`testvm-base-<series>-<image>`); the
  first run after the change downloads each image once.

## Evidence

- Local, KVM, 2026-11-15: `start 26.7 --release latest --plugins "os-frr
  os-tayga"` 1:46 min to a ready 26.7.9; `contract` against it 0
  mismatches; `stop` removes disk, key and state; `contract` without a VM
  is refused.
- The consumer checks of `testvm-action.yml`, run locally against 26.1.11
  with os-frr: pass; each fails when an output is wrong (version, plugin
  list, a non-loopback URL). They also caught a mistake of the first draft
  (26.1 reports the version under `product.product_version`).
