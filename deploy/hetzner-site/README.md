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

Routine image deployment is pull-based. The protected GitHub Actions workflow
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

### Bootstrap and break glass

The operator command installs or refreshes the root-owned reconciliation units
and can directly activate an image from the canonical public GHCR package. Use
it once to enable pull-based deployment on an origin created before the timer
was added, and thereafter only for break-glass recovery:

```sh
LEAPVIEW_SITE_IMAGE='ghcr.io/flidai/leapview-site@sha256:<digest>' task site:deploy
```

The task uses the authenticated Infisical CLI session to inject
`prod:/hetzner-site/operator/SITE_SSH_PRIVATE_KEY`. The operator writes it to a
mode-0600 temporary file for the duration of the command and removes it on
exit. For break-glass operation, invoke `scripts/deploy_site.sh` directly and
set `LEAPVIEW_SITE_SSH_KEY` to a readable identity file; without either input,
the script falls back to `~/.ssh/leapview-site-production`.

The reviewed, non-secret SSH host-key fingerprint is stored in
`ssh-host-key.sha256`; change it only after verifying a deliberate server
replacement against the Hetzner control plane.

The operator command scans the presented host key into a temporary
`known_hosts` file, requires the exact reviewed fingerprint, installs the
versioned deployment scripts, and invokes the bounded server-side deploy
command. The server serializes deployments, pulls the candidate before changing
the active environment, retains a rollback snapshot, and restores and
re-qualifies the previous image if the candidate fails. Finally, the operator
checks the server's recorded digest, public health and readiness routes, and
the `www` redirect.

The reconciliation service records a confirmed failed application candidate and
does not retry that same candidate every minute. Infrastructure failures remain
retryable. A later promotion clears that suppression.
If hosted activation does not converge, the workflow restores the previous
desired-state digest while the server-side deployment command restores and
re-qualifies the active image.

Successful and failed deployment decisions are appended to root-readable
`/opt/leapview-site/deployment-history.tsv`. Rollback environment snapshots are
retained as `/opt/leapview-site/deployment.env.rollback.*`. Recovery can append a
repeated history entry if an interruption occurred after the original append;
the current/previous image files and transaction record determine recovery state.

## Bounded site-image retention

The site host retains the active site image and one previous successful site
image. Locally cached immutable rollback images can be started without contacting
the registry. A deployment also protects its incoming candidate until its outcome is
known. Caddy and every image referenced by a running or stopped container are
protected independently. References from other repositories are never cleanup
targets. Historical `deployment.env.rollback.*` files are recovery records;
they do not indefinitely pin every historical image. Recovering an older
snapshot can require re-admitting and downloading that exact immutable image.

The Python 3 standard-library helper defaults to a dry-run inventory:

```sh
sudo python3 /opt/leapview-site/site_image_retention.py plan
```

Review its protected images, deletion candidates and filesystem free space.
Use `apply` for targeted local image removal; it does not remove registry
artifacts, volumes or certificate state:

```sh
sudo python3 /opt/leapview-site/site_image_retention.py apply
```

Inspection failures or contradictory/missing active or rollback identities
block deletion. The explicit first-install marker is only for an installation
that has never had a predecessor. Do not create that marker to work around
missing state on an existing host. Docker aliases and container references
must be resolved before deleting an image; `<none>` in the tag column does not
mean an image is disposable.

Cleanup, pulls and deployment use the same host mutation lock. Maintenance
runs before a pull and after successful activation. The default free-space
floor is 5 GiB on containerd's filesystem (or Docker's storage filesystem for
legacy installations). Detection queries Docker's active backend and the
containerd root configuration. Imported or unsupported containerd configuration
requires an explicit `--storage-path` for manual maintenance, or
`LEAPVIEW_SITE_STORAGE_PATH` in the reconciliation service environment. Do not
add updater settings to `deployment.env`, which holds only the two image pins.
This is a conservative initial policy; measure candidate download/extraction
requirements and actual filesystem headroom rather than summing Docker image
sizes. A larger future image may require a higher floor.

Capacity, registry and lock failures remain retryable. An obsolete-image
cleanup failure after healthy activation is reported as a maintenance warning;
it does not roll back the working website. Future pulls remain blocked when
headroom is insufficient. If state is ambiguous after interruption, preserve
extra images and resolve it before applying cleanup. The reconciler first attempts
recovery of a recorded transaction. An operator can run the same recovery without
selecting a new candidate:

```sh
sudo /opt/leapview-site/deploy.sh --recover
```

A malformed or contradictory record requires investigation; do not delete it to
force cleanup. The installer likewise leaves reconciliation stopped if it cannot
fully restore a consistent script set, and reports the retained staging path.

### Recovering a full existing site host

1. Confirm the actual site container, deployed-image, previous-image, Caddy,
   public build identity and health. Record the before inventory and free bytes.
2. Coordinate with any GitHub promotion and pause the reconciliation timer;
   wait for active deployment work under the host locks. Keep serving containers
   running. Do not terminate an in-progress deployment service.
3. Run the reviewed helper in plan mode and inspect the concrete removal set.
   If the full disk prevents staging the helper, use a reviewed in-memory
   operator invocation; do not manually delete files from containerd's store.
4. Apply only the reviewed obsolete site-image removals, preserving all protected
   references. Verify free bytes, active/rollback image availability and public
   health. Do not use broad `docker system prune`, forced image deletion or
   volume pruning.
5. Install the reviewed scripts through the operator path. It checks the staged
   bundle, waits for the locks, and restores the previous script set on an
   ordinary installation failure. Then resume reconciliation and run the normal
   site deployment for the current verified main image.
6. Verify `/build.json` matches the selected image/revision, `/healthz` and
   `/readyz` succeed, and public documentation links work. Record post-deployment
   inventory and free bytes. If cleanup did not resolve activation, diagnose
   that independent failure before retrying.

This retention change preserves the existing Compose replacement and rollback
behavior; it does not promise uninterrupted traffic switching. It does not
modify the stateful product demo hosts.

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
