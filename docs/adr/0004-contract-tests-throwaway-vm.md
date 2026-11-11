# 0004: Contract tests against a throwaway, isolated VM

Status: accepted, implemented (per-release automation planned)

## Context

Static derivation cannot prove correctness. A real instance can, but tests
write data, and nobody's production firewall may ever be a target. OPNsense
runs on FreeBSD, so containers on Linux are not an option.

## Decision

- QEMU/KVM with the official nano image, verified by checksum (mirror and
  release announcement) and signature; fresh disk overlay per run.
- User-mode networks with `restrict=on`: no route to a LAN or the internet.
  In: a port forward on `127.0.0.1` only. Out: a package proxy that serves
  only the signed package snapshot of the VM's release; pinning keeps the
  repository's core version equal to the installed one, which the firmware
  requires before installing plugins.
- Throwaway API key created with the factory login of the fresh image.
- The verifier refuses targets other than `127.0.0.1`/`localhost` and uses
  HTTP/1.1 deliberately, so server-side transfer problems stay visible.

## Consequences

- Needs KVM on the runner (local or self-hosted; availability on hosted CI
  to be checked).
- Any patch release is tested by updating the series image through the
  proxy to exactly that release (implemented 2026-11-11; the proxy also
  serves the signed FreeBSD base/kernel sets of the series).
- Write paths use server defaults; generated test values are planned.

## Evidence

Runs with all 69 plugins with an API: 26.7 541 shapes ok, 23 confirmed
known gaps, 3 mismatches; 26.1.6 534 / 23 / 3. Found real upstream issues
(broken chunked encoding over HTTP/1.1 for large responses, HTTP 500 in
`os-udpbroadcastrelay` on 26.1.6, a `BooleanField` with the invalid default
`N` in `os-nginx`). An "undocumented field `Type`" reported for `os-caddy`
turned out to be our parser bug (a typed field whose name equals a setting
tag was skipped; fixed 2026-11-11).
