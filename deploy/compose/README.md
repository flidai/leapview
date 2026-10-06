# LeapView Docker Compose

This is the production operations package for the public LeapView image. It
runs exactly one application process with one named state volume and one
configured environment, and adds hardened defaults and HTTPS. The included
`leapviewctl` is a standalone Go
operations binary for the archive's operating system and architecture.

```sh
cp deployment.env.example deployment.env
cp leapview.env.example leapview.env
# Configure external PostgreSQL URLs and roles in leapview.env for direct
# Compose initialization. Host first-install can instead select the bundled
# PostgreSQL adapter through operator-bootstrap.json; see below.
# Initialize credential encryption, then configure the chatbot in Agent Settings.
# Keep all deployment secrets out of source control.
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
direct Compose initialization uses provider-owned PostgreSQL control and DuckLake URLs, distinct
migrator/runtime/maintenance roles, and the exact target delivery pool ID and
compatibility digest. Edit those values in `leapview.env`; initialization
preserves them and fails with the missing variable name when they are absent.
Each PostgreSQL URL must use `sslmode=verify-full` with a trusted provider CA
(through `sslrootcert` or the image's system trust store); `require` and
`verify-ca` are intentionally rejected because they do not authenticate both
the server certificate and hostname.

LeapView platform admins configure the chatbot in Agent Settings. Compose
initialization generates `LEAPVIEW_AGENT_CREDENTIAL_KEY` in private `leapview.env`
when absent; preserve and back up this encryption key separately from PostgreSQL.
Existing installations must provision this key and run `./leapviewctl start` once
before admin-managed settings become available. Subsequent model changes require
no restart. Do not replace the encryption key after credentials have been saved.

For legacy installations, `LEAPVIEW_AGENT_API_KEY`, `LEAPVIEW_AGENT_BASE_URL`,
`LEAPVIEW_AGENT_MODEL`, and optional `LEAPVIEW_AGENT_REASONING_EFFORT` still supply
startup configuration until the first successful admin save. Shared templates
leave model and reasoning unset. After admin takeover, redeployment cannot
overwrite the selected configuration.

The pre-initialization pool command must be a dry run. Apply the same reviewed
pool/evidence pair only after `init`, because durable admission verifies the
control baseline created during initialization. Inject the DuckLake migrator
URL only into that apply command through the target secret manager; ordinary
serving must not receive it.
Direct `leapviewctl init` remains the external-provider path. The host
installer can explicitly select the bundled profile in
`/run/leapview/operator-bootstrap.json`:

```json
{
  "schemaVersion": 1,
  "postgresProfile": "bundled",
  "postgres": {},
  "physicalPool": {
    "pool": { /* full canonical physicalpool.PoolIdentity object */ },
    "evidence": { /* full canonical physicalpool.EvidenceArtifact object */ }
  }
}
```

The physical-pool identity and evidence remain operator-supplied and reviewed.
During host install, LeapView starts the PostgreSQL 18 Compose service before
the existing pool dry-run, then runs the same initialization and apply steps
as the external profile. Passwords and server TLS keys are persisted in
root-private files under `/opt/leapview/.postgres-secrets`; the Compose service
receives individual password and server TLS files, while the application gets
only the read-only CA certificate. Connections use the Compose service hostname
`postgres`, a certificate for that hostname, and `sslmode=verify-full`. The
database uses an internal Compose network with no published port and a
persistent named volume.

The profile is fixed by a private install marker. Host install retries reuse
the same secrets and rerun idempotent role/database/schema reconciliation, so
an interruption after `initdb` does not strand an initialized but unprovisioned
volume. Retry with the same operator JSON and keep the PostgreSQL volume and
`.postgres-secrets` together. If either is missing or inconsistent, the
adapter fails closed; it never silently replaces passwords or keys. Restore a
matching volume and secret set from the same recovery point. This optional
single-node service does not add automated backup/PITR, HA, database upgrades,
or certificate rotation. Those remain operator recovery and maintenance work.
Run the isolated Docker regression locally with
`LEAPVIEW_TEST_BUNDLED_POSTGRES_DOCKER=1 go test ./internal/app/cli/composectl -run '^TestBundledPostgresDockerResumesProvisioningAndPreservesVolume$' -count=1 -v`;
the hosted `task test:qualification:native-postgres` lane runs the same check.

HTTPS is enabled by default through the Caddy overlay. Initialization derives
`LEAPVIEW_PUBLIC_URL=https://<domain>`, the allowed host, and the Caddy domain
from the validated `--domain` hostname. Use `--no-https` only when a trusted
external HTTPS proxy fronts the localhost-bound application port; it disables
the Caddy overlay but preserves the HTTPS public URL and secure cookies.

## Private first publication on an installed host

`leapviewctl host install` records `private-bootstrap` before it starts the
application. With managed HTTPS, the app and Caddy ports bind only to host
loopback, and the bootstrap Caddyfile uses Caddy's internal CA for the canonical
domain. The host keeps the public URL and secure-cookie origin unchanged. For a
private browser session, resolve the configured domain to `127.0.0.1` on the
operator machine, trust the Caddy root CA obtained from
`/data/caddy/pki/authorities/local/root.crt` in the Caddy container, and open an
SSH tunnel to the host's loopback HTTPS port:

```sh
ssh -N -L 127.0.0.1:443:127.0.0.1:443 root@HOST
```

This binds local port 443, which may require elevated permission and must be
unused. Copy the private bootstrap root certificate from the Caddy container
through the protected SSH connection, then add it to the operator's browser
trust store:

```sh
ssh root@HOST 'docker exec "$(docker ps --filter label=com.docker.compose.project=leapview --filter label=com.docker.compose.service=caddy -q | head -n 1)" cat /data/caddy/pki/authorities/local/root.crt' > ./leapview-bootstrap-ca.crt
```

Retrieve the one-time administrator credentials on the host with
`ssh root@HOST 'leapviewctl first-login'` and deliver them through the protected
operator channel. Complete project setup and the first publication through
LeapView's normal authoring flow, with a distinct reviewer nominated before
planning and approving the candidate. `/readyz` remains 503 until the first
publication is active. Once it returns 200 on the host's loopback listener, run:

```sh
ssh root@HOST 'curl --fail --silent --show-error http://127.0.0.1:8080/readyz'
ssh root@HOST 'leapviewctl activate-first-install'
```

The command verifies readiness itself, applies the normal public Caddy
configuration, waits for the application's Docker health check and `/readyz`,
then records the public phase. A command failure attempts to restore the private
proxy configuration and reports a restore error if that fails. The Compose and
marker updates are sequential; an abrupt stop
after the public Caddy switch but before the marker becomes durable can leave
Docker restarting the public configuration until a later `leapviewctl start`
reapplies the pending private phase. This interruption can happen only after
`/readyz` has returned 200. Remove the temporary domain override and internal CA
trust after public TLS is verified.

With `--no-https`, the application stays loopback-bound and Caddy is not
managed by LeapView. The external proxy operator must keep its public route
disabled until publication is ready and `activate-first-install` succeeds.

Pulling and running the public image does not require this package or the
controller; see the installation guide for the localhost evaluation path. For
production, `leapviewctl` provides the supported initialization and health
lifecycle. Production backup/PITR and DuckLake/object-store recovery use
provider-native tooling; follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore). Run `./leapviewctl help` for the
current lifecycle commands.

The same archive also carries the provider-neutral Linux bootstrap and host
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
