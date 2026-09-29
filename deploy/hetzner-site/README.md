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

The pre-migration host serves the original Compose application behind Caddy.
The legacy updater is disabled. Its old mutable-tag polling path is retained only
for protected migration recovery; do not restart it. The build workflow now only
publishes and qualifies immutable production images. Manual Kamal activation and
the persistent Caddy-only topology are documented in
[the operator runbook](../kamal-site/README.md). Qualification, review and controlled
handover must complete before that topology is installed on production.

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

Merges to protected `main` invoke `.github/workflows/site-deploy.yml` to build and
qualify images. Automatic VPS activation is deferred. There is no promotion to
the old `production` desired-state tag and no CI SSH/access job.

Use `task site:deploy -- status` for read-only operator inventory, then the manual
`prepare`, `deploy`, `rollback` and `maintain` commands described in
[the Kamal runbook](../kamal-site/README.md). The old `scripts/deploy_site.sh` is
retired. Preserve the legacy Compose/env/Caddy recovery copies through migration
acceptance. After handover the active Compose definition contains only Caddy and
must never recreate the legacy application.

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
