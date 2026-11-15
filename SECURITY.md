# Security

## Reporting a problem

Please report security problems privately through GitHub: **Security ->
Report a vulnerability** in this repository. Do not open a public issue.

You can expect an acknowledgement within a week. This is a volunteer
project without a support contract; fixes are made on a best-effort basis.

## What is in scope

- The generator and the published documents: anything that could make a
  spec describe something other than what the signed OPNsense packages
  contain, for example a way around the catalog signature, the key chain
  ([ADR 0005](docs/adr/0005-signing-key-chain.md)) or the package
  checksums.
- The pipeline: a way for untrusted input (a pull request, a package, a
  mirror response) to run code with the workflow's write permissions or to
  change published releases.
- The test VM harness: a way for the test instance to reach anything other
  than the package proxy, or for tests to target anything other than
  `127.0.0.1`.

Out of scope: vulnerabilities in OPNsense itself (report them to the
OPNsense project), and the behaviour of third-party code generators.

## How the project protects itself

- No secrets: the pipeline uses only the workflow's own short-lived token,
  with permissions set per job.
- Inputs are verified the way FreeBSD `pkg` does it: signed catalog,
  signing keys accepted only through OPNsense's signed key chain, every
  package checked against its catalog checksum. Test VM images are checked
  against two independent checksum sources and a signature.
- Contract tests run only in a throwaway VM without a route to any network
  except a proxy that serves the signed package snapshot of the tested
  release ([ADR 0004](docs/adr/0004-contract-tests-throwaway-vm.md)).
- All third-party actions are pinned by commit hash; publishing jobs never
  run for pull requests; nothing is published when a gate fails.
