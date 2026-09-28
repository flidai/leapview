# Managed deployment scaffold

First implementation of the [target deployment architecture (ADR-0025, PR #744)](https://github.com/flidai/leapview/pull/744).
This is an **operator scaffold, not a qualified production installation**. It
creates no live resources through CI. Keep using the existing deployment path
until the lifecycle integration and recovery exercises below are complete.

## Layout and ownership

| Directory | Owner and purpose | Implemented here |
|---|---|---|
| `hetzner/` | OpenTofu: per-customer provider resources | App/database VPSs, private network, restricted public firewalls, deletion protection, mocked plan tests |
| `nixos/` | NixOS: host services and configuration | Locked inputs, separate host roles, installation disk layout, deploy-rs profiles, configuration checks |
| `kamal/` | Kamal: application and proxy releases | Pinned Ruby dependencies, native configuration tests, runtime-only secret references and SSE/readiness baseline |
| Existing [`../compose/`](../compose/README.md) | Public self-hosting | Unchanged; this scaffold does not require self-hosters to adopt NixOS |

OpenTofu provisions two dedicated customer VPSs. NixOS runs Docker on the app host
and PostgreSQL 18/pgBackRest on the database host. Kamal owns application and proxy
containers. Application state is on PostgreSQL; analytical files stay on the app
host's SSD. Restic is opt-in for inventoried irreplaceable files, not required for
reconstructible analytics. Tailscale is installed on both hosts; enrollment and
customer-scoped grants are separate operator steps.

Scope is x86_64 Hetzner Cloud in Europe. The disk/interface names in the sample
must be checked against the actual machine. Sizes are examples, not capacity or
availability promises. Both tiers have one host and no automatic failover.

## Offline validation

Use OpenTofu 1.12.6, Nix with flakes enabled and Ruby 3.4. The Nix flake also exposes
an operator development shell. Lockfiles belong in source control. After installing
the locked Kamal bundle, `task managed:check` runs the same checks below.

```sh
tofu -chdir=deploy/managed/hetzner init -backend=false -lockfile=readonly
tofu -chdir=deploy/managed/hetzner fmt -check -recursive
tofu -chdir=deploy/managed/hetzner validate
tofu -chdir=deploy/managed/hetzner test

nix flake check path:./deploy/managed/nixos --no-build
nix build path:./deploy/managed/nixos#checks.x86_64-linux.host-contracts --no-link

cd deploy/managed/kamal
bundle install
bundle exec ruby config_test.rb
```

The provider tests use mocks: they do not require a Hetzner token or create servers.
Nix evaluation checks complete host configurations and deploy-rs definitions. The
small host-contract build checks role separation and service settings. A full
`nix flake check` also builds deploy-rs activation closures; neither evaluation nor
build proves that the target machine boots or restores successfully.

A subsequent [remote component rehearsal](rehearsal-2026-09-28.md) built both
complete host configurations and exercised database/file recovery and proxy
behavior. The [application and guest-boot follow-up](rehearsal-application-2026-09-28.md)
records the subsequent real Kamal and emulated NixOS tests.

An optional, slower integration test (`task managed:hosts:boot-test`) boots
isolated app/database/outsider guests:

```sh
nix build path:./deploy/managed/nixos#boot-test --no-link -L
```

It tests TLS, private-interface filtering and service persistence across guest
reboots. KVM is optional; software emulation is much slower. The emulated fixture
starts containerd separately to avoid dockerd's short internal startup deadline.
It does not exercise Disko, firmware boot or Hetzner networking.

## Infrastructure and private inventory

Copy the `hetzner/` root into a private operations checkout or consume it as a
version-pinned module. Supply `HCLOUD_TOKEN` through the protected operator/CI
environment. Put real customer tfvars, reviewed plans and state outside public Git.
Before any real apply, configure a protected remote backend with locking and tested
recovery. No backend/account is silently selected by this scaffold. Local state
is suitable only for disposable validation, not managed operations.

`terraform.tfvars.example` contains documentation-only addresses. Supply existing
operator SSH key IDs and restricted egress CIDRs. The temporary Ubuntu image is
only an SSH entrypoint for nixos-anywhere. OpenTofu deliberately installs no
application, writes no secrets into cloud-init and does not reconcile the OS after
installation. It ignores later SSH-key/image changes because NixOS owns them.

The `hosts` output gives public and private addresses. Carry those into private
NixOS inventory: the private DHCP address assigned by Hetzner must equal
`leapview.privateAddress`; the database's `appAddress` must equal the app's private
address. No database port is exposed publicly. Hetzner's cloud firewall does not
replace filtering on the private interface; NixOS restricts PostgreSQL to the app
address and PostgreSQL requires TLS plus SCRAM authentication.

`prevent_destroy`, deletion protection and rebuild protection intentionally block
routine destructive changes. Host replacement must preserve the original recovery
point and fence the old writer; do not disable these protections casually.

## NixOS installation and updates

The checked-in `inventory.example.nix` is evaluation-only: its SSH key, bucket and
`.invalid` deployment names must be replaced. Keep real inventory in a private
flake consuming the exported `nixosModules.app` and `nixosModules.database` from a
pinned revision. Import disko and `modules/disk.nix` for installation, or provide an
appropriate customer-owned hardware configuration. Keep a separate stateVersion
for each installed host; updating nixpkgs is not a stateVersion bump.

For a disposable first-host rehearsal:

1. Review the provisioned host identity, SSH fingerprint, disk and interface names.
2. Build/evaluate the private flake before accessing the host.
3. Stage TLS/backup credentials via protected runtime files (see below), using
   nixos-anywhere's `--extra-files` support when needed for first activation.
4. Install using the **locked** nixos-anywhere tool and the private host flake.
   Disko formats the selected disk; use this only on an empty replacement host.
5. Verify boot, private DHCP addresses, SSH/Tailscale enrollment, mounts, TLS,
   PostgreSQL and actual externally reachable ports.
6. Use the **locked** deploy-rs tool for subsequent reviewed configuration updates.
   Do not rerun installation/disk formatting as an update operation.

NixOS provisions Docker before Kamal is used. Do not run a second host bootstrap
or declare Kamal-managed containers as NixOS OCI units. Docker and host firewall
rules need an external exposure test, not just configuration inspection.

Host auto-upgrades, Docker auto-pruning and Nix garbage collection are disabled in
this scaffold. Establish reviewed update schedules, overdue-security alerts and
bounded retention before production. deploy-rs confirms activation/connectivity;
post-update service and reboot checks remain required. OS rollback does not restore
PostgreSQL, files or application migrations.

## Credentials, database bootstrap and backups

Keep values out of tfvars/state, Nix expressions, derivations, logs and command-line
arguments. Stage secret files outside the flake source directory: a `path:` flake
copies its directory into the Nix store, including Git-ignored files. A `.gitignore`
is not a secret boundary for Nix. GitHub environment secrets or a protected operator environment deliver
credentials; deployed services do not fetch GitHub secrets at runtime.

| File | Owner/mode | Use |
|---|---|---|
| `/var/lib/leapview-postgres-tls/server.crt` | `postgres:postgres`, `0444` | Certificate with the database DNS/IP identity used by the client |
| `/var/lib/leapview-postgres-tls/server.key` | `postgres:postgres`, `0400` | Database TLS private key |
| `/var/lib/leapview-backup-secrets/pgbackrest.conf` | `root:pgbackrest`, `0440` | S3 credentials and independent backup encryption passphrase; see example |
| `/var/lib/leapview-trust/postgres-ca.crt` on app | `root:root`, `0444` | Public CA mounted into the container; client URLs use `sslmode=verify-full&sslrootcert=/etc/leapview/trust/postgres-ca.crt` |
| `/var/lib/leapview-secrets/restic-password` | `root:root`, `0400` | Optional Restic repository encryption password |
| `/var/lib/leapview-secrets/restic.env` | `root:root`, `0400` | Optional repository access credentials |

Missing TLS files stop PostgreSQL startup; missing backup credentials cause archive
and backup failures. Deliver them before activation and alert on failures/WAL disk
growth. pgBackRest configuration references the external secret file through an
`/etc` symlink; its contents are never evaluated into the Nix store. Backup commands
use upstream NixOS services/timers; there is no LeapView backup daemon.

This first scaffold does **not** provision application roles/passwords, migrate
schemas or admit a physical pool. Integrate the canonical production bootstrap
and capability separation as the next slice. Do not reuse `deploy/postgres/init.sh`:
it is a development/test initializer with default passwords. Do not grant runtime
credentials schema ownership to bypass bootstrap.

pgBackRest schedules weekly full and daily differential backups with continuous
WAL archiving. The example retains four full backups; choose the actual policy
with the customer. Create/protect the bucket, scope credentials and exercise
stanza creation, archiving, restore and expiration before launch. Object Lock
requires a deliberate bucket-creation decision and compatibility testing.

For irreplaceable files, explicitly set `leapview.fileBackup.enable`, `paths` and
`repository`. Initialize the Restic repository separately; automatic pruning is
not enabled. Define a recovery-consistent file set and the upload durability window.
Neither Restic nor PostgreSQL PITR alone synchronizes a DuckLake catalog with files.

## Kamal integration boundary

Copy `deploy.yml.example`, `ssh_config.example` and `secrets.example` into a private
operator directory as `deploy.yml`, `ssh_config` and `.kamal/secrets`. Copy
`probe_host.rb` beside `deploy.yml`, along with the pinned Gemfile/lockfile. Provide the
required non-secret inventory variables and scoped bootstrap secrets. Pin host
fingerprints through a trusted channel. Run native config tests with fixtures;
never print rendered production configuration/secrets into public CI logs.

The template describes a prebootstrapped application. It supplies PostgreSQL
runtime URLs plus distinct, bounded control/DuckLake maintenance URLs for retention.
Migrator, upgrade-coordinator, schema-owner, cloud and backup credentials belong
only in their separate operations. Preserve the agent credential encryption key
independently of database backups; losing it makes stored integration credentials
unreadable. Root/Docker deployment access remains highly privileged. The app
volume uses the current release image's UID/GID 999.

Kamal checks the image's `service=leapview` label before running it; the release
Dockerfile now provides that metadata. `probe_host.rb` supplies the public hostname
to kamal-proxy's native `--health-check-host` option, which Kamal 2.12 does not expose
in its configuration schema. Load this adapter for every controller invocation:

```sh
bundle exec ruby -r ./probe_host.rb -S kamal <command> -c deploy.yml
```

Without that header, LeapView correctly rejects the internal container hostname.
Keep allowed-host checks enabled. Remove the adapter when an upgraded Kamal exposes
an equivalent supported setting, after updating the native command-construction test.

Production `/readyz` requires an active analytical deployment. First onboarding
therefore needs a restricted bootstrap phase, followed by canonical project/data
publication and a switch to `/readyz`. A private rehearsal can temporarily use
`/healthz`; do not treat that liveness response as serving readiness.

The current shared application home permits one process owner. Ordinary Kamal
replacement starts another container before stopping the old one; it is not a
safe zero-downtime strategy for this layout. A lifecycle adapter must drain and
stop the old application before starting its replacement, and retain a restart
rollback path. Plan for downtime. An old image is usable only while database and
application-state compatibility still permits it; container retention is not a
database downgrade mechanism.

**Do not use this as a production `kamal deploy` runbook yet.** Kamal's ordinary image
reference is `repository:version`. The adapter must verify the approved digest,
reuse LeapView's compatibility/migration/target-binding checks, admit the configured
physical pool and coordinate mutating workers before enabling releases. The current
`leapviewctl` path is Compose-specific; invoking it over Kamal would introduce two
container lifecycle owners. This scaffold intentionally adds no deploy/apply workflow
or custom hooks that bypass those checks.

The direct kamal-proxy TLS path has explicit readiness, buffering and timeout
settings. Cloudflare, certificate renewal, long-lived `/updates`, reconnects,
uploads and rollback after writes still need end-to-end qualification. The scaffold pins kamal-proxy v0.9.2 alongside Kamal 2.12.0;
qualify and security-review this pair before enabling releases.

## Next delivery slices

- Resolve the first-reviewer credential issuance blocker recorded in the
  [application rehearsal](rehearsal-application-2026-09-28.md), then qualify a
  protected first publication and `/readyz`-gated releases.
- Canonical PostgreSQL role/bootstrap and physical-pool admission adapter, with
  operation-only credentials and no default passwords.
- Kamal lifecycle integration and immutable artifact verification; migration,
  draining, worker/publication fencing and restart-based rollback evidence.
- Protected GitHub environments, state backend, Tailscale grants/enrollment and
  independently recoverable keys/artifacts.
- Better Stack instrumentation/collection, backup-age/WAL/disk/pipeline alerts,
  security update process and optional Cloudflare/Postmark configuration.
- Full builds and boot/restore tests: provision, deploy, upgrade, rollback, reboot,
  lose each host, restore PostgreSQL/retained files and rebuild analytics. Record
  interruption, acknowledged-write loss, source freshness and residual manual work.

Keep these adapters and reusable configuration public. Customer inventory, grants,
plans/state and incident records stay private. This scaffold does not claim an SLA,
provider-loss recovery, or completed production qualification.
