# Install and authenticate the CLI

Use a LeapView release for routine operation, or run the CLI from a checked-out repository when developing LeapView itself. Confirm that the command you invoke matches the server contract you intend to operate:

```sh
leapview --help
```

## Authenticate an interactive workstation

Run device login from the project you intend to publish:

```sh
leapview login https://dash.example.com
```

LeapView discovers the target's canonical origin, immutable instance identity, and environment; reads the project identity from `dashboards/leapview.yaml`; opens the target's browser approval screen; and requests only `RESOURCE_USE`, `RESOURCE_READ`, `RESOURCE_EDIT`, and `RESOURCE_PUBLISH` for that project. It does not request connection-secret, approval, or production-activation access. The credential lasts 15 minutes and rotates through a revocable CLI session.

Access and refresh credentials are stored only in the operating-system credential store. The versioned CLI profile contains the canonical origin, instance ID, environment, project ID, and a credential-store account reference. It never contains a token. LeapView CLI and LeapView Desktop use separate credential namespaces and cannot reuse each other's sessions.

Use the same target URL or a stable profile name supplied with `--name`:

```sh
leapview plan \
  --project dashboards/leapview.yaml \
  --target https://dash.example.com
```

Use `leapview login <target> --no-browser` on a headless workstation, then open the displayed verification URL on a browser that can reach the LeapView instance. Use `leapview logout <target>` to revoke the server-side CLI session and remove both the native credential and non-secret profile.

## Authenticate automation

CI exchanges a service-principal secret for a short-lived, non-refreshable workload credential. Inject the service-principal secret from the CI secret manager; do not run human device login or persist a credential directory:

```sh
export LEAPVIEW_TARGET=https://dash.example.com
export LEAPVIEW_WORKLOAD_CLIENT_ID=sp_project_deployer
export LEAPVIEW_WORKLOAD_CLIENT_SECRET='<injected by the CI secret manager>'
export LEAPVIEW_WORKLOAD_PROJECT=analytics
leapview plan --project dashboards/leapview.yaml --json
```

The CLI exchanges those values immediately before the operation for a credential bound to the discovered instance, exact project, author/publish/request actions, and a 15-minute maximum lifetime. It does not persist the service-principal secret or workload access token.

`LEAPVIEW_API_TOKEN` and `--token` remain a compatibility path for small teams and transitional automation. They are never written by `leapview login`; prefer workload identity for production CI.

## Diagnose authentication failures

First confirm the target URL is exact and reachable. A profile is not reused after its immutable instance identity, origin, or project changes. Then check the active CLI session in LeapView and the native credential-store availability. An HTTP `401` usually means the credential expired or was revoked; `403` means the authenticated principal lacks permission or the short-lived credential does not include that project/action.

Do not solve a `403` by immediately granting broad administrative access. Compare the failed operation with the service identity's effective grants and add the narrowest missing privilege.

See [Targets and environments](/docs/cli/targets) for safe target selection, [Automation and CI](/docs/cli/automation) for secret handling in pipelines, and the [CLI troubleshooting guide](/docs/cli/troubleshooting) for a diagnostic sequence.
