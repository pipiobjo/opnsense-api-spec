# Test VM action

`.github/actions/testvm` gives a GitHub Actions job a throwaway OPNsense
instance to test against: an official image (checksum from two sources,
signature against the announced release key), updated to an exact release,
with the plugins you ask for, and a fresh API key. It is the same VM the
daily contract tests use (ADR 0004, ADR 0011), so a Go client or an
OpenTofu/Terraform provider can run its integration or acceptance tests
without a real firewall.

The instance is reachable only on `https://127.0.0.1:<port>` of the runner,
with a self-signed certificate. It has no route to a LAN or the internet;
packages come from a local proxy that serves only the signed snapshot of
the chosen release. The key is valid only inside this VM, which is deleted
at `stop`.

## Use

Pin the action to a full commit hash of this repository:

```yaml
jobs:
  integration:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@<hash>
      - id: vm
        uses: pipiobjo/opnsense-api-spec/.github/actions/testvm@<commit hash>
        with:
          series: "26.7"
          release: latest      # or 26.7.9, or image
          plugins: os-frr os-haproxy
      - run: go test ./test/integration/...
        env:
          OPNSENSE_URL: ${{ steps.vm.outputs.url }}
          OPNSENSE_KEYFILE: ${{ steps.vm.outputs.keyfile }}
      - if: always()
        uses: pipiobjo/opnsense-api-spec/.github/actions/testvm@<commit hash>
        with:
          command: stop
```

One VM per job. For several releases, use a matrix: one job per release.

## Inputs

| Input | Default | Meaning |
|---|---|---|
| `command` | `start` | `start` boots and prepares the VM; `stop` shuts it down and deletes its disk and key |
| `series` | | release series, for example `26.7` (required for `start`) |
| `release` | `latest` | exact release (`26.7.9`), `latest` (newest release of the series on the official mirror), or `image` (the image as published, no update) |
| `plugins` | empty | space-separated package names, or `all`: every plugin with an API in this release, from the release's published IR (`opnsense-<release>.ir.json`) |
| `require-plugins` | `true` | fail when a plugin could not be installed; with `false` the job continues and `failed` lists them |
| `free-disk` | `true` | on GitHub-hosted runners, delete large preinstalled toolchains (Android, .NET, GHC, CodeQL); the VM disk needs room |

`latest` follows the official mirror, which can be ahead of the published
data of this repository by a day. To test against the data you use, pass
the release from it.

## Outputs

| Output | Content |
|---|---|
| `url` | `https://127.0.0.1:18443` |
| `keyfile` | path of a JSON file with `key` and `secret` (mode 0600); the secret is masked in the log |
| `version` | product version the instance reports, for example `26.7.9_1` |
| `release` | release it was updated to |
| `plugins` | comma-separated installed plugins |
| `failed` | comma-separated plugins that could not be installed |
| `acceleration` | `kvm`, or `tcg` (software emulation) |
| `slow` | the factor the harness stretched its own waits by: `1` with KVM, `6` with `tcg`, or the value of `TESTVM_SLOW` when the job set it. Scale your own test timeouts with it rather than copying the number |
| `dir` | directory with `console.log` and `proxy.log`, for an upload when something fails |

## Time and runner

With KVM (GitHub-hosted Linux runners provide it in most cases): about two
minutes to boot, update and install one plugin; all plugins of a release
take 10 to 20 minutes. Without KVM the action falls back to software
emulation and says so in a warning; that is several times slower (one
26.7 run with all plugins took 69 minutes). The base image is cached per
series and image version (`actions/cache`), the first run of a series
downloads about 500 MB.

The action installs QEMU and socat if missing, and Go only if the job has
none (an existing Go fetches the version this repository needs by itself).
It needs `sudo` for packages and the KVM device, so it runs on
GitHub-hosted Ubuntu runners or similar.

## Outside GitHub Actions

The action is a thin wrapper around `tools/testvm.sh`:

```sh
tools/testvm.sh prepare 26.7
tools/testvm.sh start 26.7 --release latest --plugins "os-frr"
cat .cache/vm/state.env      # url, keyfile, version, release, accel, slow, plugins, failed
tools/testvm.sh down
```

## Stability

The inputs and outputs above are the interface; new inputs and outputs may
be added. Pin a commit hash and update it deliberately, as with any action.
