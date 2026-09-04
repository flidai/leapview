# LeapView Docker Compose

This is the production operations package for the public LeapView image. It
runs exactly one application process with one named state volume and one
configured environment, and adds hardened defaults and HTTPS. The included
`leapviewctl` is a standalone Go
operations binary for the archive's operating system and architecture.

```sh
cp deployment.env.example deployment.env
cp leapview.env.example leapview.env
# Configure the external PostgreSQL URLs and roles in leapview.env.
# Run pool bootstrap without --apply; the database-free result contains the
# deterministic pool_id and compatibility_digest. Copy them into leapview.env.
./leapviewctl init --admin-email admin@example.com --domain dash.example.com
# Initialization has now applied the control baseline. Inject the operation-only
# DuckLake migrator credential and repeat the exact bootstrap with --apply.
# Do not store that owner-capable credential in leapview.env.
./leapviewctl start
./leapviewctl first-login
```

Set the released `LEAPVIEW_IMAGE` digest before initialization. Production
requires provider-owned PostgreSQL control and DuckLake URLs, distinct
migrator/runtime/maintenance roles, and the exact target delivery pool ID and
compatibility digest. Edit those values in `leapview.env`; initialization
preserves them and fails with the missing variable name when they are absent.
The pre-initialization pool command must be a dry run. Apply the same reviewed
pool/evidence pair only after `init`, because durable admission verifies the
control baseline created during initialization. Inject the DuckLake migrator
URL only into that apply command through the target secret manager; ordinary
serving must not receive it.
LeapView does not provision a PostgreSQL container in this bundle. HTTPS is
enabled by default through the Caddy overlay. Initialization derives
`LEAPVIEW_PUBLIC_URL=https://<domain>`, the allowed host, and the Caddy domain
from the validated `--domain` hostname. Use `--no-https` only when a trusted
external HTTPS proxy fronts the localhost-bound application port; it disables
the Caddy overlay but preserves the HTTPS public URL and secure cookies.

Pulling and running the public image does not require this package or the
controller; see the installation guide for the localhost evaluation path. For
production, `leapviewctl` provides the supported initialization and health
lifecycle. Production backup/PITR and DuckLake/object-store recovery use
provider-native tooling; follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore). Run `./leapviewctl help` for the
current lifecycle commands.

The same archive also carries the provider-neutral Ubuntu bootstrap and host
operations assets. VPS adapters use the matching payload embedded in the
immutable application image and delegate installation to `leapviewctl host
install`; they do not maintain a provider-specific Compose lifecycle.

## Qualify the exact installed candidate

Before publishing or adopting a release, follow the bundled
[installed-candidate qualification plan](QUALIFICATION.md). Its executable
journey validates the archive checksums, anonymous immutable image pull,
initialization, browser-approved enterprise authoring and protected publish,
five-minute sample, governed access and denial auditing, restart persistence,
and recovery-readiness checks:

```sh
./leapviewctl qualify installed-candidate
```

The release archive carries the canonical PostgreSQL role/bootstrap script at
`qualification/postgres-init.sh`; release packaging verifies it byte-for-byte
against `deploy/postgres/init.sh`. Qualification starts an isolated PostgreSQL
18 sidecar on the Compose-owned network, generates short-lived TLS files in a
private temporary directory, and requires
`LEAPVIEW_POSTGRES_REQUIRE_TLS=true`. The sidecar is removed before the
application network and volumes are torn down. No SQLite or file-backed
control-plane fallback is used by this journey.

The controller writes only bounded redacted evidence and removes its isolated
containers, volumes, temporary credentials, and restored instance when it
finishes.

## Verify the release identity

The archive, controller, container labels, running server, and release page
must describe the same build. Before initialization, verify the archive
checksum and compare the packaged identity with the controller:

```sh
sha256sum --check ../leapview-compose-*.tar.gz.sha256
cat release-identity.json
./leapviewctl version --json
```

After pulling the immutable image reference in `image-reference.txt`, inspect
its OCI labels and execute the server's version command:

```sh
LEAPVIEW_IMAGE="$(cat image-reference.txt)"
docker pull "$LEAPVIEW_IMAGE"
docker image inspect "$LEAPVIEW_IMAGE" \
  --format '{{index .Config.Labels "org.opencontainers.image.version"}} {{index .Config.Labels "org.opencontainers.image.revision"}}'
docker run --rm "$LEAPVIEW_IMAGE" version --json
```

The `version` and `revision` values must agree with
`release-identity.json`; the release must also report `"dirty": false` and
`"development": false`. Once the server is running, an API token authorized
to use the evaluation project can verify the authenticated runtime endpoint:

```sh
curl --fail --silent --show-error \
  --header "Authorization: Bearer $LEAPVIEW_API_TOKEN" \
  "$LEAPVIEW_PUBLIC_URL/api/v1/capabilities"
```

Its `buildVersion`, `buildRevision`, `buildTime`, `buildDirty`, and
`buildDevelopment` fields must match the packaged identity. `BuildTime` is the
release commit timestamp, rather than wall-clock packaging time, so rebuilding
the same revision remains reproducible.
