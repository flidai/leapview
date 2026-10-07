# Production runner access prerequisite

Read-only verification on 2026-09-26 found the production host online at
`100.73.220.23` on Tailscale. Operator SSH works using the Infisical operator key
and repository host-key fingerprint. Public SSH is restricted to an operator
CIDR; GitHub-hosted runners have no verified route. No Tailscale CI credential
name was found in LeapView's production secrets. The production GitHub environment
currently has branch restrictions, with no reviewer rule returned by its API.

If there is no existing suitable identity, a tailnet administrator should create
an OIDC federated identity for site deployment:

1. Create `tag:leapview-site-ci`, restricted to CI ownership.
2. Allow that tag to reach only `100.73.220.23:22`; review effective existing grants
   as well, since adding a narrow rule does not override broader allow rules.
3. Give the identity the `auth_keys` scope and that tag. Trust issuer
   `https://token.actions.githubusercontent.com`, subject
   `repo:flidai/leapview:environment:leapview-site-production`, and custom claims
   `repository=flidai/leapview`, `ref=refs/heads/main`,
   `workflow_ref=flidai/leapview/.github/workflows/site-deploy.yml@refs/heads/main`.
4. Record its client ID and generated audience in the production environment as
   `SITE_TS_CLIENT_ID` and `SITE_TS_AUDIENCE`. They are identifiers, not a reusable
   OAuth client secret. Keep SSH private keys in Infisical.

The pinned Tailscale GitHub action can exchange GitHub's OIDC token for an
ephemeral CI node using `oauth-client-id`, `audience` and the selected tag.
The job needs `id-token: write`; node cleanup occurs when the action finishes.
See [Tailscale's GitHub Action guide](https://tailscale.com/docs/integrations/github/github-action).

The identity's claim binding must match the final reviewed workflow. Main-only
production access must not be granted to the experimental branch. Validate network
access and the SSH host key in a read-only job after the default-off integration
is merged. Ordinary OpenSSH over Tailscale still uses the existing SSH key; this
does not require enabling Tailscale SSH. A successful connection must not enable
activation: host ownership/readiness and deployment mode remain separate gates.
See [workload identity federation](https://tailscale.com/docs/features/workload-identity-federation).

This is a proposed setup, not an applied tailnet policy or a verified CI route.
The workspace's logged-in Tailscale node is not a credential that can be reused
by hosted GitHub runners.
