# LeapView generic VPS host

This package is the provider-neutral bridge between a fresh VPS and the
canonical LeapView Compose lifecycle. It deliberately supports one guest
platform—Ubuntu 24.04 LTS with systemd—on any provider that can deliver the
cloud-init document or run the bootstrap as root.

The bootstrap has one bounded responsibility: install Docker Compose and the
host prerequisites, pull an immutable LeapView image, extract that image's
deployment payload, and invoke `leapviewctl host install`. The Go installer
validates the typed configuration and payload, installs the operational files
atomically, initializes the instance once, starts it, and enables the common
backup timer.

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

After installation, every provider exposes the same operations interface:

```sh
leapviewctl status
leapviewctl logs
leapviewctl backup
leapviewctl restore <archive>
leapviewctl upgrade ghcr.io/flidai/leapview@sha256:<digest>
leapviewctl rollback --confirm
```

DNS, provider firewalls, server creation, provider snapshots, and destruction
remain provider responsibilities. Provider adapters must not implement Docker
Compose, initialization, backup retention, upgrade, or rollback behavior.
