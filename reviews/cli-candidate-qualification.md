# CLI candidate qualification, 7 October 2026

This is an observed Linux AMD64 candidate run for FAI-1133, not public release
acceptance or a completed platform matrix. The existing
[candidate build](https://github.com/flidai/leapview/actions/runs/37579919515)
contains the merged CLI changes; no duplicate release build was dispatched.

## Exact artifact and environment

| Field | Observed value |
| --- | --- |
| Archive | `leapview-cli-candidate-37579919515-1-linux-amd64.tar.gz` |
| Archive SHA-256 | `445852d1507b66ded6fb83241b70f9b4d552a47cd94d050190cee56a3c7feeb8` |
| Revision | `63c139359e1f9a621dfdb311e9efb453395bcd08` |
| Embedded version | `0.3.0-alpha.1` (candidate identity, not the September public archive) |
| Build time | `2026-10-07T04:18:27Z` |
| Development / dirty | `false` / `false` |
| Runtime image | `ghcr.io/flidai/leapview@sha256:641485cc9aecc7c8040b8b441efa25d347d9b8bf6c6801084b5b9294d2d65155` |
| Actual host | NixOS 26.05, Linux 6.18.54, AMD64, 16 logical CPUs |
| Docker server / Compose used by harness | `29.8.0` / `5.5.1` |
| Explicit endpoint | `unix:///var/run/docker.sock`, resolved to `/run/docker.sock` |
| Native credential store for second attempt | GNOME Keyring 50.0, private D-Bus session and temporary home |

The archive declares the Ubuntu 24.04 Docker Engine support profile. The actual
host above is NixOS; this run does not establish Ubuntu platform acceptance.
The harness ran under the repository's Nix development environment, against
the unmodified installed archive executable and its pinned runtime image.

## Observed results

| Check | Result |
| --- | --- |
| Adjacent archive checksum, safe extraction, inner manifest, CLI/runtime identity | Passed |
| Version, eight command help probes, bare root help, offline `--llms` | Passed |
| Installed `doctor --format json --timeout 10s` and no created CLI state | Passed, doctor status `pass` |
| Static qualification | Exit 0, overall report `partial` as required by the evidence contract |
| First lifecycle attempt, without Secret Service | Failed during native credential storage; confirmed runtime reset succeeded |
| Second lifecycle attempt, isolated unlocked Secret Service | Initialized sample and started runtime; automatic native credential storage succeeded; project policy bootstrap failed with HTTP 403 |
| Cleanup after second attempt | Exact reset confirmation obtained; confirmed reset exited 0 and removed checkout-owned runtime/state |
| Declared sample staging, candidate synchronization, retained-data restart | Not reached |
| Preview edit scenarios and timing repetitions | Not run |

The second attempt's blocking diagnostic was:

```text
bootstrap local Project authorization policy: create initial project policy binding:
POST .../api/v1/projects/<project>/role-bindings: Forbidden
(current policy verification failed: GET .../role-bindings?limit=200: Forbidden)
```

The expected reset-plan command exits 1 while presenting its exact confirmation;
the subsequent confirmed reset exits 0. That is successful cleanup, not a passing
development lifecycle. The failed lifecycle report leaves `endpointPinned` false.

## Reproduction and retained evidence

Download the Linux AMD64 artifact from the candidate run above, preserving its
adjacent `.sha256` file. With Docker Engine, Compose, D-Bus, GNOME Keyring and
Python available, run:

```sh
python3 deploy/local/qualification/with_keyring.py \
  ./deploy/local/qualification/qualify.sh \
  --archive <artifact-directory>/leapview-cli-candidate-37579919515-1-linux-amd64.tar.gz \
  --evidence-dir <evidence-directory> \
  --required --run-lifecycle \
  --docker-host unix:///var/run/docker.sock --timeout-seconds 900
```

Bounded reports and raw command records remain locally under
`.tmp/qualification/candidate-37579919515-1/{static,lifecycle-initial,lifecycle-keyring}/`.
Those paths are execution artifacts, not portable release evidence. This document
records the artifact identity and durable observed outcomes. FAI-1133 remains In
Progress until the authorization failure and remaining exact public archive,
platform, preview and measurement gates have observed results.
