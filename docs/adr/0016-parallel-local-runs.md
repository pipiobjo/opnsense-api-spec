# 0016: Parallel local test runs with plugin groups; CI keeps one VM

Status: accepted 2026-11-26, implemented

## Context

A full local run (contract test, or the Go client's integration test, on
the newest release of the three supported series) took about 29 minutes,
one VM after the other. Measured on one VM (2 vCPU, 2 GB, KVM; 26.7 image
updated to 26.7.10, all 70 plugins): boot and API key 36 s, update and
reboot 69 s, plugin installation 294 s, the test itself about 3 minutes.
The plugin installation is one firmware job per plugin, and the firmware
runs one job at a time, so it cannot get faster inside one VM. The VM is
wait-bound: it used about 0.6 of a core; the workstation (24 cores) was
mostly idle.

Two ways out were considered: keep an image with all plugins installed
and boot from it (setup in about 40 s), or run several fresh VMs at once.
A kept image would no longer test that every plugin installs on the
release; that installation is part of what a run proves (a plugin that
does not install leaves its endpoints untested, ADR 0008).

## Decision

1. **Every run installs every plugin fresh.** No image with plugins is
   kept.
2. **Slots.** `tools/testvm.sh` runs several VMs from one checkout:
   `TESTVM_SLOT=N` gives a VM its own state directory, ports, QEMU name,
   helper binary and lock file. The read-only base images stay shared;
   building one holds a lock and replaces the file in one step. The
   package caches are written through a temporary file per writer and a
   rename (`pkgrepo.WriteAtomic`), because the package proxies of
   parallel VMs share them. Without `TESTVM_SLOT` everything stays as it
   was (paths, ports), so CI and the test VM action are unchanged.
3. **Plugin groups.** `tools/parallel.sh` splits each release's plugins
   into groups (`opnsense-api-spec groups`, `internal/groups`): named
   scenario groups from a groups file (`verify/groups.json` here,
   `test/integration/groups.json` in the client), the rest over balanced
   groups (3 by default). Plugins that need each other share a group:
   package dependencies from the signed catalog, and from the IR model
   relations and API actions inherited from another package's controller;
   what cannot be derived is printed as a note. A need that neither
   source shows, found by comparing a parallel run with one VM, goes into
   the file's `together` list with its evidence (the only one so far:
   os-ndp-proxy-go needs an external alias, which on a fresh firewall only
   os-crowdsec's install code creates).
   The core is tested on exactly one VM. Balancing uses the measured
   times of the last run (installation per plugin, the group's test time
   shared by endpoints), and the number of packages before the first run.
4. **Conflict check.** One more VM per release installs every plugin
   together and runs no test. A plugin that does not install there, or
   in its group, fails the run. Together with step 1 this keeps what the
   single VM proved: each plugin installs on the release, alone with its
   group and next to all others.
5. **Merged results.** The reports of a release's groups are merged into
   the report one run writes (`merge-verified`, `groups.MergeReports`); a
   package in two groups, or groups that saw different instance versions,
   are an error. Coverage floors apply to the merged profile of a series,
   not to one VM. The client merges its per-VM summaries the same way
   (`TestMergedGroups`) and checks the per-series floor of tested
   resources there.
6. **CI and the scheduled pipeline do not change.** They keep one VM per
   release with every plugin (`--plugins all`). Hosted runners have two
   cores; the parallel runner is for workstations.

## Consequences

- A local suite runs all VMs at once: 12 VMs for three releases with
  three groups each, about 2 GB of memory per VM. The wall time is set by
  the conflict-check VM (all plugins, one firmware job each); see
  Evidence.
- Results must be the same in substance as one VM with every plugin.
  Where a test's outcome depends on another plugin being installed, the
  dependency must be derivable (package or model relation); an
  underivable one would show as a difference between the parallel and
  the CI run, which tests on one VM.
- A forgotten dependency between plugins shows up as a write cycle that
  cannot create its prerequisite entry (skipped, not passed): visible in
  the counts, never hidden.

## Evidence

Workstation with 24 cores and 93 GB, KVM, 2026-11-26; newest releases
25.7.11, 26.1.11, 26.7.10, every plugin with an API (68, 69, 70).

| Contract test, all three releases | Wall time | Peak host CPU | Peak memory used |
|---|---:|---:|---:|
| one VM per release, one after the other | 1818 s | 61 % | 39 GB (33 GB before) |
| `tools/parallel.sh`, 12 VMs | 495 s to 509 s | 97 % | 60 GB |

Per VM in the parallel run: boot and key about 40 s, update and reboot
95 s to 135 s (69 s on its own: the twelve VMs unpack the update at
the same time), installing a group of 17 to 27 plugins 75 s to 180 s,
test 10 s to 190 s. The conflict-check VM installs all plugins in 310 s
to 395 s and sets the wall time. The merged results of the last run
equal those of the serial run on every release (same packages; every
path, method, check and status, with multiplicity). The first runs found
three differences, each from a need between packages the split did not
know: os-web-proxy-sso's controller extends os-squid's (now derived from
the IR), os-ndp-proxy-go's write cycle needed a core list for its
prerequisite while the core was tested elsewhere (the contract test now
takes prerequisites from the core in any case), and the external alias
os-crowdsec creates (the `together` entry).
