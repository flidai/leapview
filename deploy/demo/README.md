# Hosted product demo

`https://demo.leapview.dev` is the continuously deployed, client-neutral
LeapView demonstration environment. It runs the same canonical Olist project
as `task dev`:

- Executive Sales
- Fulfillment Operations
- Visual Showcase

The project source remains in `dashboards/`. This directory contains only the
deployment contract and the reviewed SSH host identity; it must never contain
client configuration or secret values.

## Delivery

After `Main artifacts` builds and qualifies an immutable image from `main`,
`.github/workflows/demo-deploy.yml`:

1. resolves the exact `ghcr.io/flidai/leapview@sha256:...` image;
2. temporarily admits the GitHub-hosted runner to the demo server firewall;
3. upgrades the server through `leapviewctl`, including its healthchecked
   rollback path;
4. downloads the digest-pinned public Olist dataset and synchronizes it as
   managed data;
5. publishes `dashboards/leapview.yaml` through the normal candidate,
   approval, and activation APIs; and
6. verifies the Visual Showcase and public readiness.

The `leapview-demo` GitHub environment authenticates to Infisical through
GitHub OIDC. The Infisical `prod:/demo/deployment` path supplies:

- `DEMO_FIREWALL_ID`
- `DEMO_HOST`
- `DEMO_PUBLISHER_CLIENT_ID`
- `DEMO_PUBLISHER_CLIENT_SECRET`
- `DEMO_RELEASE_CLIENT_ID`
- `DEMO_RELEASE_CLIENT_SECRET`
- `DEMO_SSH_PRIVATE_KEY`

The existing `prod:/hetzner-qualification/infrastructure` path supplies
`HCLOUD_TOKEN`. The publisher and release identities are separate service
principals. Their credentials are exchanged for one-hour, project-scoped OAuth
workload tokens on every deployment. The publisher is restricted to
managed-data ingestion, project authoring, and release publication. The
release principal is restricted to viewing, approving, and activating the demo
project environment, plus managing the public dashboard publications declared
by the canonical showcase.

Target capability discovery requires an authenticated credential but no
pre-existing project grant. This is essential on the first deployment,
because the project graph is not active until its exact candidate is
activated. The subsequent authoring, ingestion, publication, approval, and
activation calls remain protected by the canonical project grants.

## Human access

Human credentials are isolated from the deployment identity in the Infisical
`prod:/demo/access` path. GitHub Actions must never import this path. It contains
the private administrator recovery login and the deliberately shared product
demo login:

- `DEMO_ADMIN_EMAIL`
- `DEMO_ADMIN_PASSWORD`
- `DEMO_VIEWER_EMAIL`
- `DEMO_VIEWER_PASSWORD`

The shared principal is `demo@leapview.dev`. The project grants it only
`RESOURCE_USE` and `RESOURCE_READ` on the canonical project and dashboard
resource IDs. Do not bind it to the built-in `viewer` role: that role
also enables the agent and shared conversation history. The shared login must
never receive administration, authoring, preview, refresh, deployment, token,
or connection privileges.

Treat the shared credential as public. To rotate it, reset the local password,
revoke every existing session for the principal, complete the forced password
change, and update `DEMO_VIEWER_PASSWORD` in Infisical. A password reset alone
does not revoke an already-issued browser session. On a replacement instance,
create the local shared principal before the first project deployment; the
project deployment then reconciles its least-privilege grants.

The checked-in `ssh-host-key.sha256` pins the server identity. The workflow
adds only its current runner `/32` to SSH, then restores the complete prior
firewall rule set on exit.

Manual recovery is available from the workflow dispatch control. It deploys
the selected `main` revision through the identical digest-pinned path.
