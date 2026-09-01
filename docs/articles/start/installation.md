# Installation

LeapView ships as a public multi-architecture container image. Pulling that image is the primary onboarding path; no source checkout, registry login, or installer is required. One running container with one persistent state volume is one LeapView instance.

## Current controlled-testing release

The supported candidate is
[`v0.2.0-rc.1`](https://github.com/flidai/leapview/releases/tag/v0.2.0-rc.1),
built from revision
[`dfb3086d59284c6597180e99a7d07f41e36a7f7e`](https://github.com/flidai/leapview/commit/dfb3086d59284c6597180e99a7d07f41e36a7f7e).
It is a release candidate for controlled testing, not GA. Its immutable image
is:

```text
ghcr.io/yacobolo/leapview@sha256:8b32fc291c86005c69c2ca1fa673dcaa4cb84d39cfc951e065a2775b122f81d9
```

Download the version-matched operations bundle and checksum for the machine
that will run `leapviewctl`:

| Operating system | Architecture | Archive | Checksum |
| --- | --- | --- | --- |
| Linux | amd64 | [leapview-compose-v0.2.0-rc.1-linux-amd64.tar.gz](https://github.com/flidai/leapview/releases/download/v0.2.0-rc.1/leapview-compose-v0.2.0-rc.1-linux-amd64.tar.gz) | [SHA-256](https://github.com/flidai/leapview/releases/download/v0.2.0-rc.1/leapview-compose-v0.2.0-rc.1-linux-amd64.tar.gz.sha256) |
| Linux | arm64 | [leapview-compose-v0.2.0-rc.1-linux-arm64.tar.gz](https://github.com/flidai/leapview/releases/download/v0.2.0-rc.1/leapview-compose-v0.2.0-rc.1-linux-arm64.tar.gz) | [SHA-256](https://github.com/flidai/leapview/releases/download/v0.2.0-rc.1/leapview-compose-v0.2.0-rc.1-linux-arm64.tar.gz.sha256) |
| macOS | amd64 | [leapview-compose-v0.2.0-rc.1-darwin-amd64.tar.gz](https://github.com/flidai/leapview/releases/download/v0.2.0-rc.1/leapview-compose-v0.2.0-rc.1-darwin-amd64.tar.gz) | [SHA-256](https://github.com/flidai/leapview/releases/download/v0.2.0-rc.1/leapview-compose-v0.2.0-rc.1-darwin-amd64.tar.gz.sha256) |
| macOS | arm64 | [leapview-compose-v0.2.0-rc.1-darwin-arm64.tar.gz](https://github.com/flidai/leapview/releases/download/v0.2.0-rc.1/leapview-compose-v0.2.0-rc.1-darwin-arm64.tar.gz) | [SHA-256](https://github.com/flidai/leapview/releases/download/v0.2.0-rc.1/leapview-compose-v0.2.0-rc.1-darwin-arm64.tar.gz.sha256) |

## Before you begin

Install Docker Engine. The five-minute path below needs no source checkout,
registry login, Go, Bun, Task, manual YAML, or external dataset. A public
production instance additionally needs Docker Compose, a DNS name, HTTPS,
durable secret storage, and provider-native recovery storage.

## Run the five-minute evaluation

Pull the public image and start its self-contained evaluator:

```sh
IMAGE='ghcr.io/yacobolo/leapview@sha256:8b32fc291c86005c69c2ca1fa673dcaa4cb84d39cfc951e065a2775b122f81d9'
docker pull "$IMAGE"
docker run --detach --name leapview-evaluate --init \
  --publish 127.0.0.1:8080:8080 \
  --volume leapview-evaluate:/var/lib/leapview \
  "$IMAGE" evaluate
```

The evaluator generates private runtime secrets, creates a forced-change local
administrator, stages the small synthetic dataset shipped in the image,
creates a private candidate, and publishes those exact bytes into one
disposable project. It prints no secret to the container log. Wait for the
health check, then consume the credentials once:

```sh
docker exec leapview-evaluate leapview healthcheck
docker exec leapview-evaluate leapview evaluate first-login
```

Open <http://localhost:8080>, sign in, and change the temporary password. Choose
**Five-minute Sales Evaluation**, select a State, and confirm that the Orders
KPI, revenue chart, and governed order table update together. This exercises
authentication, immutable serving-state deployment, managed data, semantic
query planning, DuckDB execution, filters, and table rendering.

The `127.0.0.1` publish address is part of the security boundary. Evaluation
mode intentionally uses local HTTP and generated local secrets; do not expose
it on a LAN or the internet and do not treat the synthetic project as
production seeding.

### Persistence, diagnostics, and cleanup

The named volume preserves the initialized instance, changed password, managed
data, and active deployment:

```sh
docker restart leapview-evaluate
docker exec leapview-evaluate leapview healthcheck
```

If initialization does not become healthy, inspect the deterministic bootstrap
log and container state:

```sh
docker logs leapview-evaluate
docker inspect --format '{{.State.Health.Status}}' leapview-evaluate
```

`first-login` is deliberately one-time. If its output was lost, reset the
disposable evaluator. Removing the container does not remove its volume:

```sh
docker rm --force leapview-evaluate
```

Delete the persisted evaluation instance only when a full reset is intended:

```sh
docker volume rm leapview-evaluate
```

The evaluator is pinned to the supported candidate digest so that the
instructions, runtime, and retained evidence cannot drift independently.

To run another evaluator concurrently, give it a separate volume, container
name, host port, and target port:

```sh
docker run --detach --name leapview-evaluate-2 --init \
  --publish 127.0.0.1:8081:8081 \
  --volume leapview-evaluate-2:/var/lib/leapview \
  "$IMAGE" evaluate --port 8081
```

Each evaluator then has its own instance identity and ordinary target origin
(`http://localhost:8080` or `http://localhost:8081`). Reusing either the state
volume or listen port is rejected instead of merging instances.

### Move beyond the sample

The bundled synthetic project exists only to qualify the product journey.
From a source checkout, the evaluator is also an ordinary authoring target:

```sh
leapview login http://localhost:8080 \
  --project evaluation/project/leapview.yaml
PLAN_JSON=$(leapview plan evaluation/project/leapview.yaml \
  --target http://localhost:8080 --format json)
PLAN_ID=$(printf '%s' "$PLAN_JSON" | jq -r .planId)
BUILD_JSON=$(leapview build "$PLAN_ID" --format json)
CANDIDATE_ID=$(printf '%s' "$BUILD_JSON" | jq -r .candidateId)
leapview publish "$CANDIDATE_ID"
```

Complete the browser sign-in prompted by `login`. Local evaluation policy
activates the exact candidate without a separate enterprise approver; durable
production targets retain protected approval and activation. To connect real
data, start with [Connect a data source](/docs/guides/build/connect-data). For
a durable or externally reachable instance, use the versioned Compose release
below; it adds immutable image pinning and HTTPS. Set up PostgreSQL/PITR and
DuckLake/object-store protection separately with the native provider runbook;
LeapView does not provide a Compose backup or upgrade controller.

For an isolated one-shot private preview of the evaluator project, you may use
the optional `dev` loop against the same loopback target:

```sh
leapview dev --once \
  --project evaluation/project/leapview.yaml \
  --target http://localhost:8080 --no-browser
```

This preview is a convenience for checking an authoring change; reviewed
publication still follows the `plan`, `build`, and `publish` commands above.

## Run a durable production instance

The released Compose package is the recommended operations layer around the
same public image. It is not a separate LeapView distribution. It supplies
hardened container settings, generated production secrets, and optional Caddy
HTTPS. Production PostgreSQL/DuckLake backup and restore remain provider-native
operations that require a separate runbook and ADR; Compose does not provide
image-and-state upgrade or rollback.

1. Select, download, verify, and extract the current platform archive:

```sh
VERSION='v0.2.0-rc.1'
case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac
ARCHIVE="leapview-compose-${VERSION}-${OS}-${ARCH}.tar.gz"
BASE="https://github.com/flidai/leapview/releases/download/${VERSION}"
curl --fail --location --remote-name "$BASE/$ARCHIVE"
curl --fail --location --remote-name "$BASE/$ARCHIVE.sha256"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum --check "$ARCHIVE.sha256"
else
  shasum -a 256 --check "$ARCHIVE.sha256"
fi
tar -xzf "$ARCHIVE"
cd "${ARCHIVE%.tar.gz}"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum --check SHA256SUMS
else
  shasum -a 256 --check SHA256SUMS
fi
```

2. Confirm every checksum reports `OK`. The archive contains an immutable application
image reference, the base Compose stack, an optional Caddy HTTPS overlay, and
the native Go `leapviewctl` operations binary.

3. Copy the deployment template and initialize the instance:

```sh
cp deployment.env.example deployment.env
./leapviewctl init \
  --admin-email admin@example.com \
  --domain dash.example.com \
  --environment prod
```

4. Start the instance and consume the one-time credentials:

```sh
./leapviewctl start
./leapviewctl first-login
```

Before adoption, run `./leapviewctl qualify installed-candidate` from the
extracted archive.
`QUALIFICATION.md` maps every automated assertion to the corresponding human
check, including anonymous distribution, the five-minute sample, audited
authorization denial, restart persistence, and recovery-readiness checks using
the separately managed secret configuration.

Initialization treats `--domain` as the canonical public hostname and derives `LEAPVIEW_PUBLIC_URL=https://<domain>`, the allowed host, and the Caddy domain from it. It also generates production secrets, creates the persistent volume, validates the resulting production configuration, and atomically creates a forced-change local administrator plus a restricted publisher token. `first-login` prints and deletes that one-time credential file.

`leapviewctl` is an optional production operations controller, not a prerequisite for pulling or running LeapView. It invokes the installed Docker Compose CLI and does not require Bash or direct access to the Docker socket API. You may manage the image with your existing container platform if it preserves the same single-process, persistent-home, initialization, provider-native recovery, and environment contracts.

Operators integrating the image directly must set the documented production
environment first. The initialization command authenticates the dedicated
control migrator, applies or verifies the exact control baseline, closes that
owner-capable pool, and creates the first administrator through the ordinary
control runtime role:

```sh
leapview admin initialize --format json > initial-credentials.json
# Store the mode-0600 file in the target secret manager before acknowledging it.
leapview admin initialize --acknowledge-credentials

leapview admin delivery pool bootstrap \
  --pool pool-identity.json \
  --evidence shared-pool-evidence.json
leapview admin delivery pool bootstrap \
  --pool pool-identity.json \
  --evidence shared-pool-evidence.json \
  --apply
```

Set `LEAPVIEW_DELIVERY_PHYSICAL_POOL_ID` and
`LEAPVIEW_DELIVERY_PHYSICAL_POOL_COMPATIBILITY_DIGEST` to the exact values
printed by the bootstrap command, then start `leapview serve --production`.
Both initialization and pool bootstrap are exact-replay operations. After
credential acknowledgement, initialization reports that the instance already
exists and never returns the credential material again. The Compose controller
performs the initialization and one-time credential handoff atomically; target
provisioning must still supply the reviewed pool identity and evidence.

The Caddy overlay is enabled by default. Pass `--no-https` only when an existing trusted HTTPS proxy fronts the localhost-bound application port. This changes where TLS terminates, not the external scheme: the generated public URL remains HTTPS, secure cookies remain enabled, and forwarded host and scheme headers must come only from that trusted proxy.

## Understand the instance boundary

Production authority is the PostgreSQL control plane and PostgreSQL-backed
DuckLake catalog, with Parquet and managed objects protected in their configured
object stores. The local volume contains runtime state and caches; it is not a
PostgreSQL recovery artifact. Use provider-native PostgreSQL backup/PITR and
DuckLake/object-store versioning or replication, coordinated to one recovery
point.

Use separate Compose project directories and names for development, staging, and production. Never scale one project to multiple application containers or point two processes at the same volume.

The controller exposes only basic lifecycle operations:

```sh
./leapviewctl status
./leapviewctl logs
./leapviewctl start
```

Use the container platform's immutable image rollout and change-management
workflow for upgrades or host rollback. The target-level `leapview rollback`
command remains available for a retained serving generation, but it does not
restore PostgreSQL, DuckLake, object-store, or Compose state. Those recovery
operations are external and require the separate native runbook and ADR.

## Contributor installation

Source checkout is the contributor workflow, not the production packaging path. Install the Go version from `go.mod`, Bun, and Task, then run:

```sh
task node:deps
task generate
task dev
```

`task dev` starts one worktree-local target and opens the private authoring
watcher. For a durable rollout, use the canonical `plan`, `build`, and
`publish CANDIDATE_ID` commands shown above. Use `task dev:status`,
`task dev:logs`, and `task dev:stop` for lifecycle operations. Run `task ci`
before handing off substantial changes.

## Validate

For the local image path, run `docker inspect --format '{{.State.Health.Status}}' leapview` and expect `healthy`. For Compose, run `docker compose config --quiet` and `./leapviewctl status`. A production application container must report healthy, and its resolved image must include a `sha256` digest.

For a released Compose archive, verify that every shipped surface has the same
identity before trusting the deployment:

```sh
sha256sum --check leapview-compose-*.tar.gz.sha256
cat release-identity.json
./leapviewctl version --json
LEAPVIEW_IMAGE="$(cat image-reference.txt)"
docker image inspect "$LEAPVIEW_IMAGE" \
  --format '{{index .Config.Labels "org.opencontainers.image.version"}} {{index .Config.Labels "org.opencontainers.image.revision"}}'
docker run --rm "$LEAPVIEW_IMAGE" version --json
```

The semantic version and full Git revision must agree across
`release-identity.json`, `leapviewctl`, the server binary, and the OCI labels.
A release reports `"dirty": false` and `"development": false`; an ordinary
local or candidate build reports version `development` and can never claim the
release version. LeapView uses the release commit timestamp as `buildTime` so
the identity remains reproducible.

After startup, compare the same identity through the authenticated
capabilities endpoint using a token authorized to use the project:

```sh
curl --fail --silent --show-error \
  --header "Authorization: Bearer $LEAPVIEW_API_TOKEN" \
  "$LEAPVIEW_PUBLIC_URL/api/v1/capabilities"
```

The `/api/v1/capabilities` response fields `buildVersion`, `buildRevision`,
`buildTime`, `buildDirty`, and `buildDevelopment` must match the packaged
identity.

## Verify

Open the configured HTTPS URL, sign in with the temporary administrator credentials, and change the password when prompted. Verify the instance identity and readiness through the authenticated capabilities endpoint; follow the provider-native recovery runbook separately for PostgreSQL/DuckLake protection.

## Troubleshooting

Use `./leapviewctl logs` when startup or health checks fail. A second process cannot open the same state volume, and an instance initialized for one environment cannot be started as another; use a separate Compose project and volume instead of changing `LEAPVIEW_ENVIRONMENT`.

## Next steps

Continue with [Self-hosting](/docs/guides/operate/self-hosting), [Connect a data source](/docs/guides/build/connect-data), and [Build your first dashboard](/docs/first-dashboard).

The commands above illustrate the installation workflow. Use the generated [`admin` CLI reference](/docs/cli/admin), [`serve` CLI reference](/docs/cli/serve), and [environment variable reference](/docs/configuration) for the exact current command and runtime contracts.
