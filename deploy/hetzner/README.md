# Hetzner single-node deployment

This Terraform configuration prepares one Hetzner server for a single-node
LeapView and Caddy deployment. It configures the firewall, restricted SSH, and
cloud-init host prerequisites; an operator-delivered first-install step
installs the application afterward. It is the small-instance topology, not a
high-availability deployment. Production authority is PostgreSQL and
PostgreSQL-backed DuckLake; managed objects remain in their configured object
stores.

## Deploy

Prerequisites:

- Terraform 1.7 or newer
- A Hetzner Cloud API token
- An SSH public key
- The immutable image reference from a LeapView release's
  `image-reference.txt` asset

```sh
cd deploy/hetzner
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars
export HCLOUD_TOKEN=...
terraform init
terraform apply
```

Set `admin_email`, `target_id`, `leapview_image`, and `ssh_allowed_cidrs` in
`terraform.tfvars`. Use your public address with a `/32` suffix for SSH. The
module deliberately rejects world-open SSH and mutable image tags. Set
`target_id` to the same authoritative deployment target ID used by release
preflight; it is persisted in the installed host marker.

Provisioning renders the provider-neutral Ubuntu host bootstrap with the
domain, administrator email, target identity, environment, and immutable image
digest. Cloud-init runs only `leapview-bootstrap prepare-host`, which installs
Docker Compose and host prerequisites. After the host is ready, privately
deliver `/run/leapview/operator-bootstrap.json` with provider-created external
PostgreSQL URLs and reviewed physical-pool identity/evidence, then run
`sudo /usr/local/sbin/leapview-bootstrap install`. Never put database
credentials in Terraform variables, user data, or state. The operator input
remains available for retry and should be removed explicitly after successful
installation. The Hetzner module contains no separate Compose, initialization,
backup-retention, upgrade, or rollback implementation.

When `domain` is empty, the deployment uses an HTTPS `sslip.io` hostname. That
is useful for evaluation. Set a domain you control for a durable installation.

## Hosted qualification

The manually dispatched `Deploy / Ephemeral Hetzner` workflow currently creates
an isolated server, verifies cloud-init host preparation, reports that private
bootstrap and first-install acceptance are pending, and destroys the server.
This repository has no automated private delivery channel for the required
operator input, so the workflow does not install LeapView or claim application
health, first-login, recovery, or host qualification. Complete that separate
operator-delivered first install before using a host as an application
acceptance result. PostgreSQL/PITR and DuckLake/object-store recovery are
provider-native; follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore).

The job is protected by the `leapview-ephemeral-qualification` GitHub
environment and authenticates to Infisical through GitHub OIDC. The dedicated
project identity can read `prod:/hetzner-qualification/infrastructure`, which
contains only `HCLOUD_TOKEN`. No long-lived Hetzner or Infisical credential is
stored in GitHub.

## First Login

After the operator-delivered first install completes, LeapView has created a
local platform administrator, a forced-change temporary password, and a
privilege-restricted publisher token that expires after 24 hours. Retrieve
them once:

```sh
terraform output -raw initial_local_user_command | sh
```

The command removes the root-only credential file after printing it. Sign in at
`terraform output -raw url`, change the temporary password, and store the
publisher token with the CLI before it expires:

```sh
leapview login "$(terraform output -raw url)" \
  --project-id <canonical-project-id>
```

Initialization is offline; no unrestricted bootstrap token is created or sent
over HTTP.

## Develop and Publish the Project

```sh
leapview data sync \
  --source-root ../../dashboards \
  --connection olist \
  --from /srv/olist \
  --target "$(terraform output -raw url)"

leapview dev --once --no-browser \
  --source-root ../../dashboards \
  --target "$(terraform output -raw url)"

leapview publish <candidate-id>
```

For project-global file ingestion, follow the [managed data ingestion
guide](../../docs/data-ingestion.md). `data sync` stages a revision; the
private candidate binds that exact revision and target-owned connection
evidence. Review the candidate returned by `dev`; `publish` promotes those
immutable bytes and pins without rebuilding them.

## Operations

After application installation, Terraform exposes an SSH prefix for the
server-side lifecycle command:

```sh
$(terraform output -raw operations_command) status
$(terraform output -raw operations_command) logs
```

Important paths:

- Docker volume `leapview_leapview-state`: application state, analytical data, and local managed data
- `/opt/leapview/leapview.env`: generated application configuration
- `/opt/leapview/deployment.env`: pinned images and deployment metadata

Use Hetzner's provider backup features for the host where appropriate, but do
not treat a local volume archive as a PostgreSQL target recovery point. Use
PostgreSQL-native backup/PITR and the DuckLake/object-store provider's native
snapshot, versioning, replication, or backup mechanism. Coordinate recovery
points before reopening traffic; follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore) for the complete procedure.

For independent encrypted provider backups, configure Restic or the relevant
native service according to the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore). Keep repository credentials
root-only and retain enough history for the declared RPO/RTO.

## Destroy

Confirm that provider-native PostgreSQL/DuckLake recovery points and required
keys are retained before destroying the server. Hetzner server backups are
deleted with the server.

```sh
terraform destroy
```

The deployment stores no application secrets in Terraform state or outputs.
See the generated [configuration reference](../../docs/configuration.md) for the
complete process-global LeapView environment contract.
