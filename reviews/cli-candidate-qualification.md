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

## Scoped-login candidate follow-up

The completed [candidate build 37585063372](https://github.com/flidai/leapview/actions/runs/37585063372)
contains the local login permission-ceiling fix and was reused for a second exact
installed-archive qualification. All four native archive jobs completed, but
only the Linux AMD64 archive was run through this harness on this VPS.

| Field | Observed value |
| --- | --- |
| Archive | `leapview-cli-candidate-37585063372-1-linux-amd64.tar.gz` |
| Artifact ID | `11467207364` |
| Archive SHA-256 | `f368a4a6e1c71e281dfc6817afef320d46f3e0032e70dfab63ba10d16c6b7cc2` |
| Revision | `dca163dd9cd7321e8ed7162c8c26ad3d6c9ff258` |
| Embedded version | `0.3.0-alpha.1` (candidate, not public release) |
| Build time | `2026-10-07T06:47:20Z` |
| Development / dirty | `false` / `false` |
| Runtime image | `ghcr.io/flidai/leapview@sha256:d449452ac6e97059cce88a3a49ff5ef88fd424412ea7d27e84ec3ff596f27244` |
| Actual host | Same NixOS Linux AMD64 VPS; not Ubuntu acceptance |
| Docker server / Compose | `29.8.0` / `5.4.0` |
| Credential store | GNOME Keyring 50.0 in isolated D-Bus session |

All 12 installed static probes passed, with the required overall `partial`
result. Native authentication and runtime readiness passed, but initial project
policy creation failed with `durable grant was not found`; its verification read
reported `authorization policy was not found`. Sample staging, synchronization,
and retained-data restart were not reached. The exact-confirmation reset exited
0, and the checkout-owned containers, volumes, and network were removed.

The server's canonical binding authorizer accepted REST credentials only, even
though its outer middleware accepted the scoped native authoring session. The
source fix repeats the typed authoring permission check after the existing claim
owner and canonical binding checks, while retaining the REST fallback. A real
PostgreSQL/generated HTTP regression passes for all three bindings, idempotent
retries, policy/grant reads and subsequent grant creation. It also rejects
read-only, foreign-target, foreign-project, other-owner, noncanonical, and
other-recipient requests. Those tests are source evidence; this candidate still
contains the observed failure and must not be recorded as passing after the fix.

Reports remain under
`.tmp/qualification/candidate-37585063372-1/{static,lifecycle}/`. The historical
failed lifecycle report also exposed inconsistent endpoint evidence:
`endpointPinned: false` with `pinState: verified-pre-post`. It does not satisfy
the evidence schema. The harness now promotes both fields together only after
successful development and restart identity checks; the original report is
preserved rather than rewritten. A newly built matching archive/runtime is
required for the next lifecycle attempt.

### Existing candidate browser evidence

The same run retains artifact
`prepublication-candidate-37585063372-1-amd64`. Its report records `success`,
successful enterprise authoring, and `browserJourney: true` against the same
`d449452ac6e97059cce88a3a49ff5ef88fd424412ea7d27e84ec3ff596f27244`
image digest. The installed-candidate journey requires authenticated private
candidate preview of the evaluation dashboard with 24 governed order rows
before publishing. Reuse that observed Compose/runtime preview evidence.
It does not establish local `init`'s Sales fixture rendering, live-edit behavior,
cold/warm/edit-to-visible measurements, macOS provider lifecycle, or public
released-archive qualification. The product already implements candidate preview
and live updates. A temporary local observation driver is prepared but has not
yet reached these scenarios.

## Native static matrix and profile-scope follow-up

[Candidate build 37603015624](https://github.com/flidai/leapview/actions/runs/37603015624)
ran the installed static qualification harness successfully in all four native
archive jobs. Both Linux doctors returned 0; both macOS doctors reported missing
Docker prerequisites, an allowed static result that does not prove lifecycle
acceptance. All four reports correctly remain `partial`.

| Field | Linux AMD64 lifecycle artifact |
| --- | --- |
| Archive | `leapview-cli-candidate-37603015624-1-linux-amd64.tar.gz` |
| Archive SHA-256 | `776d8c91af45a1d0cbefc2f0f8dd09ee777825f040164dad31d377fe336d4fb0` |
| Revision | `475c75abf44373f482bf9491a88a289240e56219` |
| Embedded version / build time | `0.3.0-alpha.1` / `2026-10-07T09:46:51Z` |
| Development / dirty | `false` / `false` |
| Runtime image | `ghcr.io/flidai/leapview@sha256:4c3edcaa11bf245941faee7e6b39188694349817e6d0316836bb75cbb968fcc3` |
| Host / Docker / Compose | NixOS Linux AMD64 / `29.8.0` / `5.5.1` |

The isolated-keyring lifecycle now passes native login and initial policy
bootstrap, then stages the declared sample successfully. Reading the retained
development-profile application returns HTTP 403 before synchronization. Local
login also needs `project.settings.read` and `project.settings.update`; the
server already admits these operations through its scoped bootstrap path.
The remaining delivery, upload and session operations were audited against
the existing default authoring ceiling.

The exact reset exited 0 and removed the checkout-owned containers, volumes and
network. The failed report now consistently records `endpointPinned: false`
and `pinState: not-proven`. Reports remain under
`.tmp/qualification/candidate-37603015624-1/{native-static,lifecycle}/`.
Restart, local browser observations and timing were not reached. This candidate
is preserved as failed; a newly matched archive/runtime must prove the added
profile permissions before lifecycle acceptance can advance.

The same candidate's `prepublication-candidate-37603015624-1-amd64` report
records success against the exact `4c3edcaa` image digest above, including
`browserJourney`, `governedQuery` and `restartPersistence`. This refreshes the
existing Compose evaluation-dashboard evidence; it does not substitute for
local Sales preview, live-edit scenarios or timing observations.

## First synchronized preview and retained-data findings

[Candidate build 37607465258](https://github.com/flidai/leapview/actions/runs/37607465258)
contains the profile-scope correction. Its Linux AMD64 archive is
`leapview-cli-candidate-37607465258-1-linux-amd64.tar.gz`, SHA-256
`372c33dcb19c636b59667d38729e5edcaea2da57c9825cd61ad9e8edf1298e21`,
revision `f04c615a06b72511ccf4c7bd7937cb69d52650ca`, built at
`2026-10-07T10:26:07Z`. Its pinned runtime is
`ghcr.io/flidai/leapview@sha256:f6410f568b95a203693ae2da4c44a95f04f6aa1a5baa49061a8fd8bd7046b7d0`.
The embedded version remains `0.3.0-alpha.1`, with development and dirty false.
All four native static reports were retained with `partial` results and no
failures; this does not establish the missing platform lifecycle observations.

The first installed local `dev` now stages the fixture, applies the profile,
synchronizes and activates the candidate, and emits working preview URLs.
Retained-data restart fails while re-uploading the sample: the token permits
`connection.upload`, but the captured project policy does not. Initial staging
uses `connection.create` before the connection exists; the active connection
correctly requires an explicit upload grant. The local role presets deliberately
exclude upload authority. Exact reset succeeded and no checkout-owned Docker
resources remained. Endpoint verification correctly stays unproven on failure.

A separate browser run against the same archive/image observed these scenarios:

| Scenario | Observed result |
| --- | --- |
| Authenticated local Sales preview | HTTP 200; four settled visuals; governed and accessible KPI values 12 sales and 13,650 revenue |
| Presentation note edit | New candidate rendered with unchanged governed values |
| Invalid semantic reference | Visible stale-preview diagnostic; last valid candidate and governed values retained |
| Repair to a distinct valid source | New candidate rendered and diagnostic cleared |
| Semantic `sum` to `avg` | New candidate rendered 1,137.5 revenue and 12 sales |
| Restore previous semantic source | Failed: old plan/publication replayed after a newer candidate was active |
| Model edit and final restoration observation | Not reached; temporary fixture restored during cleanup |

The restoration diagnostic was `local active generation differs from the exact
published candidate`. A source-only local plan idempotency key reused a prior
plan without accounting for the current target revision. The observer timed out
waiting for a successfully activated candidate, and exact runtime reset passed.
Reports are under `.tmp/qualification/candidate-37607465258-1/`, including
`lifecycle/qualification-report.json` and `local-preview/preview-observation.json`.
These are partial functional observations, not a successful full lifecycle or
latency distribution. Both findings remain in the consolidated #899 work.
