# Authoring package qualification (release-gated)

This directory owns the Milestone 5 qualification contract for a future or
exact released `leapview` authoring archive. It is independent of the source
checkout and of the unfinished remote preview/deploy flows. Current public
archives are recorded as Compose/`leapviewctl`-only until FAI-798 ships the
installable authoring CLI; running this harness against repository code does
not create released evidence.

## Qualification evidence boundary

Run the harness with an archive and its adjacent checksum:

```sh
./deploy/local/qualification/qualify.sh \
  --archive leapview-cli-v<version>-<os>-<arch>.tar.gz \
  --checksum leapview-cli-v<version>-<os>-<arch>.tar.gz.sha256 \
  --evidence-dir .tmp/qualification/authoring-package
```

The equivalent repository convenience entrypoint is
`scripts/authoring_package_qualification.sh`.

When an exact public authoring archive is available, the harness verifies the
outer archive checksum, safe archive members, the
inner `SHA256SUMS` manifest, `authoring-package.json`,
`release-identity.json`, `image-reference.txt`, `runtime-package.json`, and
the executable's `leapview version --json` identity. It also checks the
read-only command surfaces (`init`, `dev`, `plan`, `build`, `publish`, and
`deploy`) through their help output. The existing release `authoring-cli` job
remains a separate build/provenance gate and is not replaced by this lane.

## Optional local lifecycle

The lifecycle is deliberately opt-in because local device authentication can
require a person to approve a browser challenge:

```sh
./deploy/local/qualification/qualify.sh \
  --archive leapview-cli-v<version>-linux-amd64.tar.gz \
  --run-lifecycle \
  --manual-prerequisites-confirmed \
  --docker-host unix:///var/run/docker.sock
```

Lifecycle qualification requires a supported Linux or macOS host, an explicit
local Unix Docker socket, Docker Compose 2.17 or newer, and the manual
authentication prerequisite. SSH endpoints, arbitrary TCP endpoints, loopback
tunnels, and unknown socket paths are rejected. A forwarded daemon deliberately
bound onto an otherwise recognized Engine/Desktop socket remains a required
external adversarial platform case; this harness does not infer its absence
from a responsive API. The selected socket is inspected and explicitly supplied
before the harness runs `init` or `dev`; ambient Docker context or host values
are not used as a fallback. The daemon's effective identity is compared before
and after lifecycle operations, and
`endpointPinned: true` is retained only after those checks agree. A changed or
unverifiable identity fails closed. The temporary checkout and its runtime are
reset after the run when a reset plan is available.

The lifecycle creates the generated project, requires `leapview dev` to stage
its declared sample without copied target or Project identifiers, synchronizes
a candidate with a stable session-preview URL, then runs `dev` again to prove
the same retained local data and pinned daemon survive restart.

Use `--required` in CI or another release gate. Without it, the harness may
return `skipped` only for an explicitly unsupported host or an explicitly
missing lifecycle prerequisite. In required mode, those same conditions fail
closed. Archive, manifest, checksum, command, and cleanup failures always
fail.

## Evidence contract

`qualification-report.json` is validated by
[`evidence.schema.json`](evidence.schema.json). It contains actual OS,
toolchain, Docker/Compose, hardware, fixture, network, warmup, and repetition
metadata plus bounded redacted command results. Credential-shaped values,
authorization headers, cookies, and credential-bearing URLs are removed before
retention. `raw-results.json` contains the same bounded command result records
for consumers that need the raw observations.

The report is explicitly scoped as `milestone-5-static` and uses result
`partial` for an archive/static run. It never reports overall `passed` while
the lifecycle, preview scenarios, or measurements remain unexecuted. The
report also distinguishes `failed`, `skipped`, and `not-run`. Numeric
samples are populated only from observed commands or browser measurements;
planned repetitions have `executed: 0`, an empty `samplesMs` array, and a
reason. The semantic, model, dashboard, presentation, and invalid edit
scenarios, including cold uncached/cached, warm restart, and edit-to-visible
measurements, remain `not-run` until the released preview surface provides a
supported observation contract. This lane never fabricates those values.

The machine-readable source is
[`qualification-contract.json`](qualification-contract.json). It records the
boundary between currently released package/lifecycle evidence and the
planned preview and production-deploy qualification work described by
[ADR-0021](../../../adr/0021-adopt-a-local-first-analytics-development-workflow.md)
and the [analytics development CLI contract](../../../adr/specifications/analytics-development-cli-contract.md).
