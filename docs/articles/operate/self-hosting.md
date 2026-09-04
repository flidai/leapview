# Self-hosting

LeapView v1 supports a single-instance topology. The public container image is the application distribution. The generic Docker Compose package is the recommended production operations layer, while the Hetzner Terraform module adds cloud provisioning and firewalling to the same application and initialization contracts. Provider-native backup and recovery remain external operations; use the [PostgreSQL operations guide](/docs/guides/operate/postgresql-operations) and [Backup and restore guide](/docs/guides/operate/backup-restore).

## Before you begin

Choose one environment, DNS name, Compose project name, and persistent-volume boundary for the instance. Install Docker Engine with Compose, configure DNS before requesting public HTTPS, and arrange encrypted provider-native recovery storage before serving production data.

## Topology

One instance contains exactly one LeapView process and environment. Production authority lives in the PostgreSQL control plane and a PostgreSQL-backed DuckLake catalog; DuckDB executes bounded serving reads and refresh transactions over DuckLake-managed analytical data. The Compose stack binds the application port to localhost and mounts only the local state required by the configured storage topology. The optional Caddy overlay publishes ports 80 and 443 with automatic HTTPS.

Horizontal application replicas and an independently writable DuckLake catalog
are not supported in the self-hosted v1 package. Multi-node lease/takeover and
HA/PITR support remain separately qualified target requirements. SQLite is
limited to isolated tests, evaluation fixtures, and offline tooling; it is not
a serving or production control-plane fallback. Deploy another independent
instance when you need another environment or capacity boundary.

## Deploy Compose

For a localhost evaluation, follow the pull-and-run flow in [Installation](/docs/installation). For production, use the platform-specific versioned Compose archive attached to the application release. It consumes the same public image, embeds its immutable digest, and includes a native Go `leapviewctl` binary that invokes Docker Compose.

1. Copy the deployment and application templates. Review the image digest,
   localhost bind, domain, and memory limit, then fill in the external
   PostgreSQL URLs/roles and target delivery-pool identities in `leapview.env`.
2. Initialize the persistent volume and offline administrator:

```sh
cp deployment.env.example deployment.env
cp leapview.env.example leapview.env
./leapviewctl init --admin-email admin@example.com --domain dash.example.com
```

3. Start the service and consume the one-time credentials:

```sh
./leapviewctl start
./leapviewctl first-login
```

Initialization derives `LEAPVIEW_PUBLIC_URL=https://<domain>`, the allowed host, and the Caddy domain from the validated `--domain` hostname. It preserves the explicitly supplied PostgreSQL and delivery settings and reports actionable missing-variable errors; it never provisions a bundled PostgreSQL service. For an existing reverse proxy, pass `--no-https`, keep the application bound to localhost, and forward the original HTTPS scheme and host from that trusted proxy. `--no-https` disables only the bundled Caddy overlay; it does not make the public origin HTTP. Do not expose the unencrypted application port publicly.

For a fresh target, run pool bootstrap without `--apply` to derive its stable
ID and compatibility digest, place those values in `leapview.env`, run
`leapviewctl init`, and only then repeat bootstrap with `--apply`. Inject the
DuckLake migrator credential into that one operation; do not retain it in the
serving environment.

The controller is optional if an existing container platform already provides equivalent secret management, health checks, and graceful shutdown. Those contracts remain required even when Compose is not used.

## Generic VPS host contract

LeapView's provider adapters share one Ubuntu 24.04 LTS host bootstrap. It installs Docker Compose and the host prerequisites, pulls the immutable application image, extracts the image's matching deployment payload, and delegates all installation behavior to `leapviewctl host install`. The typed Go installer validates configuration before mutation, stages immutable digest-named host generations, activates one generation atomically, initializes the instance once, and starts it.

This boundary keeps server creation, IPs, firewalls, DNS, and optional provider snapshots in thin provider adapters. Compose configuration, proxy defaults, and initialization remain provider-neutral. Provider-native backup, retention, image rollout, and host rollback are outside LeapView; follow the [PostgreSQL operations guide](/docs/guides/operate/postgresql-operations) and [Backup and restore guide](/docs/guides/operate/backup-restore), plus the provider's change-management procedure. After bootstrap, operators use the same `leapviewctl` status, logs, and start commands on every supported VPS provider.

Provider independence does not expand the guest operating-system matrix: the automated host contract supports Ubuntu 24.04 LTS with systemd on `linux/amd64` and `linux/arm64`. Other Docker hosts can continue to use the generic Compose package directly.

## Persistent and external storage

Production authority is the PostgreSQL control plane and PostgreSQL-backed DuckLake catalog, with Parquet and managed objects protected in their configured object stores. The named volume contains runtime state and caches; a local file archive is not a PostgreSQL recovery point.

Customer source data is configured per connection. Object storage is the recommended production source, but an instance may connect to many object stores, databases, and HTTP sources. External sources are direct reads and are not copied into LeapView state. Use immutable object keys or versioned prefixes; use managed data when LeapView must own a pinned revision.

When managed data uses S3, protect authoritative objects with bucket versioning and independent backup or replication. Coordinate the object-store point with PostgreSQL and DuckLake recovery.

## Operations

```sh
./leapviewctl status
./leapviewctl logs
./leapviewctl start
```

The controller does not upgrade or roll back a Compose or host installation.
Use the container platform and provider change-management workflow to roll out
an immutable image, and use the retained-generation `leapview rollback` command
only for an application delivery rollback. PostgreSQL/PITR and
DuckLake/object-store recovery are external provider operations. Keep encrypted
provider-native recovery copies and rehearse the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore) procedures in an isolated instance
with the same environment identity.

## Hetzner provider recipe

The Terraform module under `deploy/hetzner` provisions the same single-instance application contract with Caddy and restricted SSH. Provider snapshots or Restic replication are external infrastructure choices; they do not provide a LeapView restore path. The module consumes the canonical offline initializer, so no unrestricted bootstrap token crosses the HTTP boundary.

Set `admin_email`, an immutable `leapview_image`, and restricted `ssh_allowed_cidrs`, then apply the module. Use its generated operations command for status and logs; use the provider's image rollout and recovery tooling for host changes and PostgreSQL/DuckLake recovery.

## Validate

Before exposing an instance:

1. Verify HTTPS, allowed-host enforcement, `/healthz`, and `/readyz`.
2. Consume the one-time credentials and change the temporary password.
3. Deploy a representative project and verify a semantic query and dashboard filter.
4. Refresh an external object source and confirm the previous snapshot survives a failed refresh.
5. Verify provider-native PostgreSQL/PITR and DuckLake/object-store recovery evidence using the [PostgreSQL operations guide](/docs/guides/operate/postgresql-operations) and [Backup and restore guide](/docs/guides/operate/backup-restore).
6. Exercise a retained-generation `leapview rollback` in a non-production target before relying on it during an incident.

## Verify

After validation, sign in through the public URL, deploy a representative project, refresh one external source, and confirm the active dashboard continues to serve if a later refresh fails. Record the instance environment returned by `GET /api/v1/instance` in the deployment inventory.

## Troubleshooting

Use `./leapviewctl status` and `./leapviewctl logs` for health failures. Environment mismatch errors mean the state volume belongs to another target instance; do not rewrite its identity. For data loss or catalog drift, preserve evidence and follow the [PostgreSQL operations guide](/docs/guides/operate/postgresql-operations) and [Backup and restore guide](/docs/guides/operate/backup-restore) rather than attempting a local file restore.

## Next steps

Continue with [Production configuration](/docs/guides/operate/production-configuration), [Backup and restore](/docs/guides/operate/backup-restore), and [Health and observability](/docs/guides/operate/observability).

Use the generated [environment variable reference](/docs/configuration) and [`serve` CLI reference](/docs/cli/serve) as the source of truth for accepted runtime settings and flags.
