# Permanent leapview.dev site origin

This Terraform root manages the permanent, independently deployable
`leapview-site` origin. It deliberately does not reuse the stateful LeapView
product deployment in `deploy/hetzner`.

The topology is one reserved IPv4, one small Hetzner server, and one firewall.
Docker Compose runs exactly Caddy and the public-site binary. The site has no
application-state volume, DuckDB runtime, administrator bootstrap, or product
backup job. Only Caddy's certificate and renewal data persists on the host.

## Lifecycle ownership

Terraform owns the server, reserved address, firewall, operator key, and
creation-time bootstrap. The `bootstrap_site_image` value is only the image
used to create a replacement server. Terraform ignores later cloud-init
changes, so updating that value cannot replace the server or reserved IP.

The existing, pre-migration host uses pull-based deployment. The replacement
workflow and guarded migration are documented in [the Kamal integration](../kamal-site/README.md).
Do not enable the replacement before its qualification and handover gates pass.

Legacy deployment was pull-based. The protected GitHub Actions workflow
builds and verifies the site image, then promotes that immutable manifest to the
`ghcr.io/flidai/leapview-site:production` desired-state tag. A root-owned systemd
timer on the origin resolves the tag to its immutable digest and invokes the
bounded deployment command locally. No hosted runner receives production SSH or
infrastructure credentials, and SSH remains restricted to reviewed operator
CIDRs for bootstrap and break-glass operations.

## Remote state

Production state uses the HCP Terraform workspace
`Flid/leapview-site-production`. The workspace is configured for local
execution: the protected GitHub workflow still creates and applies the reviewed
plan, while HCP Terraform provides encrypted remote state, locking, and state
history without a dedicated Object Storage service.

The protected `leapview-site-production` GitHub environment must provide:

| Kind | Name | Purpose |
| --- | --- | --- |
| variable | `SITE_SSH_ALLOWED_CIDRS` | JSON list of restricted operator CIDRs |

Production credentials live in the Infisical `leapview` project:

| Environment/path | Secret | Purpose |
| --- | --- | --- |
| `prod:/hetzner-site/infrastructure` | `HCP_API_TOKEN` | HCP Terraform workspace state access |
| `prod:/hetzner-site/infrastructure` | `HCLOUD_TOKEN` | Hetzner Cloud resource management |
| `prod:/hetzner-site/operator` | `SITE_SSH_PRIVATE_KEY` | Bootstrap and break-glass operator identity |

GitHub authenticates to Infisical with OIDC; there is no long-lived Infisical
credential in GitHub. The machine identity is organization-level `no-access`,
project-level `viewer`, and bound to the `flidai/leapview` repository plus the
`leapview-site-production` GitHub environment. The current Infisical plan does
not support a custom folder-scoped role, so the workflow additionally fetches
only `/hetzner-site/infrastructure`. The non-secret operator public key is
versioned in `operator-ssh-key.pub`.

Configure required reviewers and prevent administrators from bypassing the
environment gate. The workflow first creates and retains a readable and binary
plan for 90 days. Selecting `apply` runs a second environment-gated job that
downloads and verifies that exact plan before applying it. A post-apply plan
must be empty.

For local read-only validation without production state:

```sh
terraform init -backend=false
terraform fmt -check -recursive
terraform validate
terraform test
```

## DNS and operations

After apply, use `terraform output -json dns_records` as the reviewed DNS input.
The `reserved_ipv4`, `canonical_hostname`, and `deployment_target` outputs are
stable and contain no credentials. IPv6 is intentionally disabled until its
complete DNS and qualification path is managed.

On the server:

```sh
cd /opt/leapview-site
docker compose --env-file deployment.env ps
docker compose --env-file deployment.env logs --tail=200
```

## Routine site deployment

Merges to protected `main` invoke `.github/workflows/site-deploy.yml`. The
workflow calls the canonical multi-platform image publisher, enters the
`leapview-site-production` GitHub environment, rejects a superseded source
revision, and moves only the verified digest to the `production` desired-state
tag. The host polls once per minute, activates the resolved digest, and reports
the serving source revision and image at `/build.json`. The workflow succeeds
only after that identity and the public health endpoints match.

Configure required reviewers and prevent administrator bypass for the
production environment. Protect `main` with the CI and merge-queue checks before
enabling automatic promotion.

### Deployment controller migration

The old `scripts/deploy_site.sh` entrypoint is retired. Routine operator requests
must use `task site:deploy`, which dispatches the same gated, serialized GitHub
workflow as automatic main deployments. `task site:access-check` requests a
read-only network/SSH inventory. See [the Kamal integration runbook](../kamal-site/README.md)
for prerequisites, exact draft status and migration/rollback gates.

The installed legacy controller continues operating until the explicit handover.
Its history and rollback environment snapshots must be retained during migration.
The reviewed non-secret SSH host-key fingerprint remains in
`ssh-host-key.sha256`; verify any intentional replacement against the provider
control plane before changing it.

## Break-glass destruction

Normal plans cannot destroy the server, firewall, or reserved address:
Terraform `prevent_destroy` and Hetzner API deletion protection are both
enabled. There is intentionally no hosted destroy workflow.

Destruction requires a reviewed source change that:

1. records the current state, outputs, DNS records, and recovery decision;
2. removes `prevent_destroy` from the exact resources being retired;
3. changes the server and primary-IP `delete_protection` attributes to `false`
   and applies that change;
4. obtains a second review before issuing a targeted destroy;
5. removes DNS only after the retirement is verified.

Never delete the HCP Terraform workspace or its state history as part of an
origin retirement.
