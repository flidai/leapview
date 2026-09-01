# Hetzner single-node deployment

This Terraform deployment runs LeapView and Caddy on one Hetzner Cloud server.
It provides automatic HTTPS, generated production secrets, restricted SSH,
provider-native backup integration.
It is the supported small-instance topology, not a high-availability
deployment. Production authority is PostgreSQL and PostgreSQL-backed DuckLake;
managed objects remain in their configured object stores.

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

Set `admin_email`, `leapview_image`, and `ssh_allowed_cidrs` in
`terraform.tfvars`. Use your public address with a `/32` suffix for SSH. The
module deliberately rejects world-open SSH and mutable image tags.

Provisioning renders the provider-neutral Ubuntu host bootstrap with the
domain, administrator email, environment, and immutable image digest. The
bootstrap pulls that image, extracts its matching deployment payload, and
delegates installation to the Go `leapviewctl host install` command. The
Hetzner module contains no separate Compose, initialization, backup-retention,
upgrade, or rollback implementation.

When `domain` is empty, the deployment uses an HTTPS `sslip.io` hostname. That
is useful for evaluation. Set a domain you control for a durable installation.

## Hosted qualification

The manually dispatched `Ephemeral Hetzner deployment` workflow exercises this
topology from an immutable application image. It creates an isolated server,
qualifies public health, consumes the one-time first-login credentials, verifies
recovery-boundary readiness, and destroys the server even when an earlier step
fails. PostgreSQL/PITR and DuckLake/object-store recovery are provider-native
and require the separate recovery runbook.

The job is protected by the `leapview-ephemeral-qualification` GitHub
environment and authenticates to Infisical through GitHub OIDC. The dedicated
project identity can read `prod:/hetzner-qualification/infrastructure`, which
contains only `HCLOUD_TOKEN`. No long-lived Hetzner or Infisical credential is
stored in GitHub.

## First Login

Provisioning creates a local platform administrator, a forced-change temporary
password, and a privilege-restricted publisher token that expires after 24
hours. Retrieve them once:

```sh
terraform output -raw initial_local_user_command | sh
```

The command removes the root-only credential file after printing it. Sign in at
`terraform output -raw url`, change the temporary password, and store the
publisher token with the CLI before it expires:

```sh
leapview login "$(terraform output -raw url)" \
  --project ../../dashboards/leapview.yaml
```

Initialization is offline; no unrestricted bootstrap token is created or sent
over HTTP.

## Develop and Publish the Project

```sh
leapview data sync \
  --project ../../dashboards/leapview.yaml \
  --connection olist \
  --from /srv/olist \
  --target "$(terraform output -raw url)"

leapview dev --once --no-browser \
  --project ../../dashboards/leapview.yaml \
  --target "$(terraform output -raw url)"

leapview publish \
  --project ../../dashboards/leapview.yaml \
  --target "$(terraform output -raw url)"
```

For project-global file ingestion, follow the [managed data ingestion
guide](../../docs/data-ingestion.md). `data sync` stages a revision; the
private candidate binds that exact revision and target-owned connection
evidence. Review the candidate returned by `dev`; `publish` promotes those
immutable bytes and pins without rebuilding them.

## Operations

Terraform exposes an SSH prefix for the server-side lifecycle command:

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
points before reopening traffic; a complete procedure requires the separate
native runbook and ADR.

For independent encrypted provider backups, configure Restic or the relevant
native service according to the recovery runbook. Keep repository credentials
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
