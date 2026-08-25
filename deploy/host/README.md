# LeapView generic VPS host

This package is the provider-neutral bridge between a fresh VPS and the
canonical LeapView Compose lifecycle. It deliberately supports one guest
platform—Ubuntu 24.04 LTS with systemd—on any provider that can deliver the
cloud-init document or run the bootstrap as root.

The bootstrap has one bounded responsibility: install Docker Compose and the
host prerequisites, pull an immutable LeapView image, extract that image's
deployment payload, and invoke `leapviewctl host install`. The Go installer
validates the typed configuration and payload, stages it under
`/opt/leapview/releases/<digest>`, atomically activates the `current` generation,
initializes the instance once, starts it, and enables the common backup timers.

Provider adapters supply a private JSON document with this schema:

```json
{
  "schemaVersion": 1,
  "domain": "dash.example.com",
  "adminEmail": "admin@example.com",
  "environment": "prod",
  "image": "ghcr.io/flidai/leapview@sha256:<digest>",
  "https": true
}
```

The adapter also supplies the same immutable image reference as a private
single-line file because the bootstrap must pull the image before the Go
installer is available. `cloud-init.yaml.tftpl` writes these inputs and the
shared `bootstrap-ubuntu.sh`; it contains no application lifecycle logic.

The production image carries the matching payload under
`/usr/local/share/leapview/deployment`. A digest therefore selects the server,
controller, Compose files, proxy defaults, and host operations assets together.
Host upgrades stage that payload before downtime, switch it with the application
image, and restore the previous payload if health checks fail or the operator
requests rollback.

Mutable instance configuration, backup archives, and rollback markers remain
under `/opt/leapview`; operational files are stable symbolic links through
`/opt/leapview/current`. A crash while staging a release therefore cannot expose
a mixture of payload versions.

After installation, every provider exposes the same operations interface:

```sh
leapviewctl status
leapviewctl logs
leapviewctl backup
leapviewctl restore <archive>
leapviewctl upgrade --transition-policy release-transition-policy.json ghcr.io/flidai/leapview@sha256:<digest>
leapviewctl rollback --transition-policy release-transition-policy.json --confirm
```

The maintained deployment workflow verifies the GitHub artifact attestation for
the selected digest before creating infrastructure. Operators performing a
manual upgrade should apply the same gate before connecting to the host:

```sh
gh attestation verify \
  oci://ghcr.io/flidai/leapview@sha256:<digest> \
  --repo flidai/leapview
```

Every operator-created archive has a required SHA-256 sidecar that is verified
before a restore stops the running service. To configure restic, create the
root-only `/etc/leapview/restic.env` with `RESTIC_REPOSITORY`, `RESTIC_PASSWORD`,
and any backend credentials, then initialize the repository explicitly:

```sh
chmod 0600 /etc/leapview/restic.env
/usr/local/sbin/leapview-backup-hook --init
```

The daily timer creates and uploads backups. A separate weekly timer applies
retention, prunes unused data, and runs `restic check`; repository access errors
never trigger implicit initialization.

DNS, provider firewalls, server creation, provider snapshots, and destruction
remain provider responsibilities. Provider adapters must not implement Docker
Compose, initialization, backup retention, upgrade, or rollback behavior.
