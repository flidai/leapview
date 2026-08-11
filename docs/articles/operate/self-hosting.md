# Self-hosting

LeapView v1 supports a single-instance topology. The public container image is the application distribution. The generic Docker Compose package is the recommended production operations layer, while the Hetzner Terraform module adds cloud provisioning, firewalling, scheduled backups, and Restic to the same application and initialization contracts.

## Before you begin

Choose one environment, DNS name, Compose project name, and persistent-volume boundary for the instance. Install Docker Engine with Compose, configure DNS before requesting public HTTPS, and arrange an encrypted off-host destination for backups.

## Topology

One instance contains exactly one LeapView process, environment, control-plane SQLite database, global DuckLake catalog, and analytical data store. The Compose stack mounts all local authoritative state beneath `/var/lib/leapview` and binds the application port to localhost. The optional Caddy overlay publishes ports 80 and 443 with automatic HTTPS.

Horizontal replicas, a shared SQLite home, and a remote multi-writer DuckLake catalog are not supported in v1. Deploy another independent instance when you need another environment or capacity boundary.

## Deploy Compose

For a localhost evaluation, follow the pull-and-run flow in [Installation](/docs/installation). For production, use the platform-specific versioned Compose archive attached to the application release. It consumes the same public image, embeds its immutable digest, and includes a native Go `leapviewctl` binary that invokes Docker Compose.

1. Copy the deployment template and review its image digest, localhost bind, domain, and memory limit.
2. Initialize the persistent volume and offline administrator:

```sh
cp deployment.env.example deployment.env
./leapviewctl init --admin-email admin@example.com --domain dash.example.com
```

3. Start the service and consume the one-time credentials:

```sh
./leapviewctl start
./leapviewctl first-login
```

Initialization derives `LEAPVIEW_PUBLIC_URL=https://<domain>`, the allowed host, and the Caddy domain from the validated `--domain` hostname. For an existing reverse proxy, pass `--no-https`, keep the application bound to localhost, and forward the original HTTPS scheme and host from that trusted proxy. `--no-https` disables only the bundled Caddy overlay; it does not make the public origin HTTP. Do not expose the unencrypted application port publicly.

The controller is optional if an existing container platform already provides equivalent secret management, health checks, graceful shutdown, backup validation, and image-and-state rollback. Those contracts remain required even when Compose is not used.

## Generic VPS host contract

LeapView's provider adapters share one Ubuntu 24.04 LTS host bootstrap. It installs Docker Compose and the host prerequisites, pulls the immutable application image, extracts the image's matching deployment payload, and delegates all installation behavior to `leapviewctl host install`. The typed Go installer validates configuration before mutation, installs the operational files atomically, initializes the instance once, starts it, and enables the common backup timer.

This boundary keeps server creation, IPs, firewalls, DNS, and optional provider snapshots in thin provider adapters. Compose configuration, proxy defaults, initialization, backup retention, upgrades, and rollback remain provider-neutral. After bootstrap, operators use the same `leapviewctl` commands on every supported VPS provider.

Provider independence does not expand the guest operating-system matrix: the automated host contract supports Ubuntu 24.04 LTS with systemd on `linux/amd64` and `linux/arm64`. Other Docker hosts can continue to use the generic Compose package directly.

## Persistent and external storage

The named state volume contains the control database, DuckLake catalog and Parquet data, deployed artifacts, runtime state, and local managed objects. Backups stop the application briefly and archive that complete boundary.

Customer source data is configured per connection. Object storage is the recommended production source, but an instance may connect to many object stores, databases, and HTTP sources. External sources are direct reads and are not copied into instance backups. Use immutable object keys or versioned prefixes; use managed data when LeapView must own a pinned revision.

When managed data uses S3, instance backups contain its metadata and cache rather than authoritative objects. Enable bucket versioning and independent backup or replication.

## Operations

```sh
./leapviewctl status
./leapviewctl logs
archive="$(./leapviewctl backup)"
./leapviewctl restore "$archive"
./leapviewctl upgrade ghcr.io/flidai/leapview@sha256:<digest>
```

Restore validates the archive and preserves the current state before replacement. Upgrade records the prior image and a pre-upgrade state checkpoint. Failed health checks reinstate both automatically. `rollback --confirm` restores the checkpoint after an otherwise successful upgrade and therefore discards later state.

Keep at least one encrypted off-host backup and rehearse restore into an isolated instance with the same environment identity.

## Hetzner provider recipe

The Terraform module under `deploy/hetzner` provisions the same single-instance application contract with Caddy, restricted SSH, provider backups, a systemd backup timer, and optional Restic replication. It consumes the canonical offline initializer, so no unrestricted bootstrap token crosses the HTTP boundary.

Set `admin_email`, an immutable `leapview_image`, and restricted `ssh_allowed_cidrs`, then apply the module. Use its generated operations command for status, logs, backup, restore, upgrade, and rollback.

## Validate

Before exposing an instance:

1. Verify HTTPS, allowed-host enforcement, `/healthz`, and `/readyz`.
2. Consume the one-time credentials and change the temporary password.
3. Deploy a representative project and verify a semantic query and dashboard filter.
4. Refresh an external object source and confirm the previous snapshot survives a failed refresh.
5. Create a backup and restore it into an isolated instance.
6. Exercise failed-upgrade rollback before relying on it during an incident.

## Verify

After validation, sign in through the public URL, deploy a representative project, refresh one external source, and confirm the active dashboard continues to serve if a later refresh fails. Record the instance environment returned by `GET /api/v1/instance` in the deployment inventory.

## Troubleshooting

Use `./leapviewctl status` and `./leapviewctl logs` for health failures. Environment mismatch errors mean the state volume belongs to another target instance; do not rewrite its identity. Restore or upgrade failures preserve a recovery archive in `backups/`; inspect that archive before attempting another state-changing operation.

## Next steps

Continue with [Production configuration](/docs/guides/operate/production-configuration), [Backup and restore](/docs/guides/operate/backup-restore), and [Health and observability](/docs/guides/operate/observability).

Use the generated [environment variable reference](/docs/configuration) and [`serve` CLI reference](/docs/cli/serve) as the source of truth for accepted runtime settings and flags.
