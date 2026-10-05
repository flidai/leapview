# LeapView generic VPS host

This package is the provider-neutral bridge between a fresh VPS and the
canonical LeapView Compose lifecycle. It deliberately supports a small tested
guest-platform matrix—Ubuntu 24.04 LTS or Debian 13 with systemd—on any
provider that can deliver the cloud-init document or run the bootstrap as root.

The cloud-init bootstrap has two explicit commands. `prepare-host` installs
Docker Compose and host prerequisites, then exits. After an operator privately
delivers the external PostgreSQL credentials and reviewed physical-pool
artifacts, `install` pulls the immutable LeapView image, extracts that image's
deployment payload, and invokes `leapviewctl host install`. The Go installer
validates both inputs before staging `/opt/leapview/releases/<digest>`, then
dry-runs pool admission, initializes the control database, applies pool
admission, and starts the instance.

Provider adapters supply a private JSON document with this schema:

```json
{
  "schemaVersion": 1,
  "domain": "dash.example.com",
  "adminEmail": "admin@example.com",
  "environment": "prod",
  "image": "ghcr.io/flidai/leapview@sha256:<digest>",
  "targetId": "<deployment-target-id>",
  "https": true
}
```

The adapter also supplies the same immutable image reference as a private
single-line file because the bootstrap must pull the image before the Go
installer is available. `cloud-init.yaml.tftpl` writes these inputs and the
shared `bootstrap-linux.sh`, which runs only `prepare-host`; it contains no
application lifecycle or database credential logic.

First install requires a separate root-private file at
`/run/leapview/operator-bootstrap.json`, mode `0600`. It is never part of
Terraform variables, state, cloud-init, or provider user data. Deliver it over
the operator's private secret channel after the host is prepared. Its schema
uses six provider-created PostgreSQL URLs and the canonical physical-pool
identity and evidence artifact types:

```jsonc
{
  "schemaVersion": 1,
  "postgres": {
    "controlUrl": "postgres://leapview_control_runtime:<secret>@<provider-host>/leapview_control?sslmode=verify-full",
    "controlMigratorUrl": "postgres://leapview_control_migrator:<secret>@<provider-host>/leapview_control?sslmode=verify-full",
    "controlMaintenanceUrl": "postgres://leapview_control_maintenance:<secret>@<provider-host>/leapview_control?sslmode=verify-full",
    "duckLakeUrl": "postgres://leapview_ducklake_runtime:<secret>@<provider-host>/leapview_ducklake?sslmode=verify-full",
    "duckLakeMaintenanceUrl": "postgres://leapview_ducklake_maintenance:<secret>@<provider-host>/leapview_ducklake?sslmode=verify-full",
    "duckLakeMigratorUrl": "postgres://leapview_ducklake_migrator:<secret>@<provider-host>/leapview_ducklake?sslmode=verify-full"
  },
  "physicalPool": {
    "pool": { /* full canonical physicalpool.PoolIdentity object */ },
    "evidence": { /* full canonical physicalpool.EvidenceArtifact object */ }
  }
}
```

This illustrates the top-level shape; replace both comments with complete
canonical JSON objects before installing. Control runtime, maintenance, and migrator URLs must map to the same
logical control database; DuckLake runtime, maintenance, and migrator URLs
must map to the same logical DuckLake database. Provider pooler and direct
endpoints may differ when they reach that same database. Each URL must use its
fixed role and database, have unique credentials, and set `sslmode=verify-full`.
Use a trusted system CA or `sslrootcert`; LeapView does not require a
deployment-specific certificate path. Pool evidence must be reviewed conformance
evidence matching the pool's compatibility tuple, with no credentials or raw
observations in the artifact.

On Ubuntu or Debian, after private delivery, run:

```sh
sudo /usr/local/sbin/leapview-bootstrap install
```

The installer reads but does not remove the operator file so an interrupted
bootstrap can be retried. After successful installation, remove that file
explicitly through the operator's secret-handling procedure.

For the exact admitted FAI-518 revision-019 predecessor image, the current
bootstrap translates this document to the six fields accepted by that image's
older installer. It rejects extra fields and persists the provisioner-supplied
`targetId` in a private binding file before invoking the older installer. After
installation it verifies that the predecessor-owned marker matches the image
and translated configuration. The current upgrade command accepts that binding
only for this exact predecessor and only while the marker still matches it.
Other images receive the original configuration and keep the normal marker
contract. This compatibility path does not change the predecessor artifact or
qualify the real-host transition. The revision-019 predecessor remains usable
for an already-installed host, but a fresh host requires an image with the
operator-bootstrap lifecycle.

The production image carries the matching payload under
`/usr/local/share/leapview/deployment`. A digest therefore selects the server,
controller, Compose files, proxy defaults, and host operations assets together.
Mutable instance configuration remains under `/opt/leapview`; operational files
are stable symbolic links through `/opt/leapview/current`.

After installation, every provider exposes the same operations interface:

```sh
leapviewctl status
leapviewctl logs
leapviewctl start
```

## Existing-host upgrade

`leapviewctl host upgrade` supplies the stage, activate, and restart effects
for an existing-host release transition. It records each successful effect in
the existing durable transition operation before reporting success. Run it from a controller build
that includes it, with
`LEAPVIEWCTL_ROOT=/opt/leapview`. The command is in the OCI host payload built
with CGO and DuckDB Arrow support; the standalone CGO-free controller archive
does not expose this authoritative `--phase` interface. The separate operator
maintenance subcommands below are available in both builds. This interface
requires an existing, live fenced
operation whose authoritative preflight and migrations have already completed.
The installed host marker must contain the provisioned `targetId` matching
the operation, except for the exact revision-019 predecessor installed through
the current provisioner with its matching private target binding. Other legacy
markers without a target ID fail closed until the host is explicitly
reprovisioned; passing `--target-id` alone does not bind an existing
installation.
The trusted migration owner registry contains public keys used by the existing
migration-capability authority; it is not generated by the command.
The control authority URL belongs in a private root-readable file so its
credentials do not appear in a process command line.

```sh
LEAPVIEWCTL_ROOT=/opt/leapview leapviewctl host upgrade \
  --phase candidate-staged \
  --operation-id <transition-operation-id> \
  --candidate-image 'ghcr.io/flidai/leapview@sha256:<digest>' \
  --target-id <deployment-target-id> \
  --control-url-file /etc/leapview/control-authority-url \
  --migration-owner-registry /etc/leapview/migration-owner-registry.json
```

After staging completes and is recorded, invoke the same command with
`--phase candidate-activated` and then `--phase candidate-restarted`, passing
the same exact operation, candidate, and target selectors. Each invocation
returns a JSON effect result only after the durable phase advances.

The command re-resolves authoritative preflight from PostgreSQL and requires
its digest, predecessor and candidate admissions, target, and recovery
frontier to match the durable operation. It requires the installed image and
active generation to match the admitted predecessor, then pulls the exact
digest-pinned candidate, extracts its deployment payload, and stages a new
generation. Only after staging is durably recorded does activation switch
`current` and select the candidate image in Compose. The restart phase starts
the service and verifies the running container was created from that exact
image. The command records each completed host phase in the existing transition
operation. Post-validation and operation completion remain separate runner
responsibilities.

The existing `host install` command still rejects changed image configuration.
If an effect fails after activation, the operation records an indeterminate
outcome and needs operator assessment; this command does not roll back.

### Operator maintenance for a single host

`leapviewctl host upgrade plan|apply|recover|status --request <private-request>`
provides a separate operator-authorized lifecycle for local PostgreSQL 18 and
local application/managed-data volumes. It reuses the host payload staging and
activation functions and the canonical Goose migration provider. It does not
weaken or fabricate the owner-backed operation required by `--phase` above.

The request binds an immutable qualified image, live OCI admission, exact source
compatibility and a versioned installation profile. The profile selects the host,
installation/recovery roots, Compose services/project, PostgreSQL image and
container, network, four state volumes, HTTPS origin and loopback validation
bindings. `controlMigratorUrlFile` optionally references a root-private URL file;
otherwise the adapter reads the existing installed control-migrator environment
binding. Credentials never appear in request evidence or command arguments.
Unknown writable state, external tablespaces/storage, engine changes and remote
Docker endpoints are rejected. Different hostnames, roots, projects and volume
names require configuration, not a different compiled controller.

The operation closes traffic, captures a stopped whole-cluster/files recovery
point, restores an isolated copy, validates the predecessor and rehearses the
candidate upgrade before live migration. The caller must validate the original
HTTPS origin at each operation-bound checkpoint. The demo adapter supplies CFO
checks; another installation supplies its own application checks. EOF, mismatched
acknowledgments and timeouts are failures, never approval.

The journal and `.leapviewctl.lock` reside at the installation root, outside all
restored volumes. Normal lifecycle commands respect pending maintenance after
reboot. During the first upgrade, the supported launcher temporarily selects the
retained candidate controller so an older installed controller cannot ignore the
journal. Completed operations restore the canonical `current/leapviewctl` link;
the previous immutable payload is never rewritten.

Precommit failure restores the matching application, database/files and original
configuration. Once the operation commits public exposure, retries only finalize
the candidate. Recovery failures keep traffic closed. Snapshot cleanup is explicit.
This is coordinated provider-native filesystem recovery, not a logical application
backup API. Managed databases, HA, external object storage and engine upgrades
remain separate provider qualification work. See the [demo adapter runbook](../demo/README.md#database-upgrades-and-interrupted-operation-recovery)
for the workflow wiring and concrete profile example.

DNS, provider firewalls, server creation, provider snapshots, and destruction
remain provider responsibilities. Provider adapters must not implement Docker
Compose, initialization, or application backup/restore behavior.

Migration URLs that name TLS certificate or key files must reference existing
read-only application bind mounts. The one-shot migrator receives only those
individual files, never the application environment or entire secret directory.

## NixOS host prerequisites

For a NixOS host, import [`nixos.nix`](./nixos.nix) from the host's existing
flake configuration. For example, add the module to the existing
`nixosSystem.modules` list:

```nix
modules = [
  (inputs.leapview.outPath + "/deploy/host/nixos.nix")
  ./configuration.nix
];
```

Keep host-specific networking, SSH and Tailscale settings, and private provider
directory configuration in the host configuration. The reusable module enables
Docker and its Compose v2 CLI plugin, installs Python, OpenSSL and filesystem
tools, enables `nix-ld` with the C++ runtime library, opens TCP ports 80 and 443
plus UDP 443, and adds a `leapviewctl` command to the NixOS system package path.
That command exports `LEAPVIEWCTL_ROOT=/opt/leapview` and executes the installed
`/opt/leapview/leapviewctl`, forwarding its arguments and exit status. Mutable
configuration, credentials, and deployment files remain outside the Nix store.

The module only configures host prerequisites. It defines no Compose service,
installer hook, or application lifecycle action, so `nixos-rebuild switch` does
not install or initialize LeapView. To install on NixOS, privately create the
same operator file and a mode-0600 `bootstrap.json`, then pull the immutable
image, extract `/usr/local/share/leapview/deployment` from a temporary
container, and run its `leapviewctl host install` with `--config`,
`--operator-config`, `--payload`, and `--source-image` set to those exact paths
and image. For example, after privately delivering the two input files:

```sh
image='ghcr.io/flidai/leapview@sha256:<digest>'
payload="$(mktemp -d)"
container="$(docker create "$image")"
docker cp "$container:/usr/local/share/leapview/deployment/." "$payload/"
docker rm "$container"
sudo "$payload/leapviewctl" host install \
  --config /run/leapview/bootstrap.json \
  --operator-config /run/leapview/operator-bootstrap.json \
  --payload "$payload" \
  --source-image "$image"
rm -rf "$payload"
```

The Go installer rejects a missing operator file on first install.
The normal Docker restart policies continue to start existing containers when
Docker starts after boot. PostgreSQL is not published through the host
firewall.

The automated package bootstrap supports Ubuntu 24.04 and Debian 13. NixOS
hosts use the module above and the same private first-install inputs; the
provider-neutral apt bootstrap does not run on NixOS.

After activating the host configuration, check the noninteractive command
environment before running the normal controller installation procedure:

```sh
sudo -n leapviewctl version
docker compose version
python3 --version
openssl version
```

## Disposable first-install qualification

The protected `nix-compose-candidate.yml` workflow includes an eight-cell
Ubuntu 24.04 / Debian 13, AMD64 / ARM64, bootstrap / Nix-controller guest
matrix. Each cell uses a checksum-pinned cloud image from
`nix/guest-images.json`, a fresh QEMU guest, the exact controller archive,
and an admitted immutable application image. Software virtualization is
recorded as `tcg`; these runs do not claim hardware-virtualization coverage.

The verifier creates a disposable TLS PostgreSQL fixture, checks the runtime
roles with `sslmode=verify-full`, and invokes the candidate's pool qualification
command before installation. Probe inputs live separately from the fresh
installation target. Only canonical pool artifacts and private operator input
are passed into installation; migration credentials must be absent from the
resulting serving environment. The guest then reboots and proves automatic
startup of the same image and installation generation. Retained evidence must
not contain fixture credentials.

A passing guest receipt covers only its named first-install and reboot checks.
It does not qualify NixOS, upgrades, rollback, recovery, or production
observation. Until a successful exact-image matrix is retained, this path is
implemented but runtime qualification remains pending. The separate Hetzner
preparation workflow is not a substitute for that evidence.
