# CLI troubleshooting

Troubleshoot from the narrowest local check outward. Preserve the exact command, exit status, and sanitized error output before changing configuration; the first failure is usually more useful than errors produced after several speculative changes.

## Validate the local environment

Inspect local authoring prerequisites, then compile the project without contacting a target:

```sh
leapview doctor
leapview validate --source-root dashboards
```

Validation diagnostics identify the source file and invalid field or reference. Fix the earliest root diagnostic first; later missing-resource messages may be consequences of it. If behavior differs in CI, compare the project revision, working directory, generated files, environment variables, and CLI version.

Doctor checks the installed runtime package, local Docker endpoint, Compose, project, profile, credential shape, and existing runtime state. It does not start containers, pull images, repair state, create locks, connect to source databases, or create deployment plans. Outside an analytics project, implicit project checks skip. Select a source or profile explicitly to diagnose a missing or invalid path. A skipped check remains unverified.

Use `leapview doctor --format json` for an ordered report. Required failures exit 1; warnings and skipped checks alone exit 0. Each failed check gives a next action. The overall time budget defaults to 30 seconds and can be reduced with `--timeout`; external probes take at most five seconds each.

For production server configuration, use `leapview config validate --production` separately. That command checks server requirements, while doctor checks the authoring workflow.

## Check server readiness

If local validation succeeds but a remote command cannot connect, check the instance readiness endpoint directly:

```sh
leapview healthcheck \
  --url https://dash.example.com/readyz \
  --timeout 10s
```

A connection or TLS error points to DNS, certificates, proxies, or network policy. A non-success readiness response means the instance is reachable but not ready; inspect server logs before retrying a deployment.

## Check target identity and authentication

Confirm the scheme, hostname, expected environment, and project. Inspect readiness and public identity with an explicit target:

```sh
leapview doctor --target https://dash.example.com --format json
```

Doctor ignores ambient targets in local mode. Remote mode accepts a URL or saved target name and rejects local Docker and profile selectors. It compares public instance identity with saved metadata. Authenticated capability and current-principal checks run only with `--token` or `LEAPVIEW_API_TOKEN`; doctor never reads or refreshes native OAuth credentials, exchanges workload credentials, or initiates login.

For `401` responses, verify that a token was supplied for that target and is still valid. For `403`, inspect the authenticated identity's effective grants for the failed operation. Do not broaden the credential until the missing privilege is understood. `leapview plan` creates durable state on the target; use it after diagnosis when you intend to review a delivery operation.

## Interpret planning and deployment failures

If a plan shows unexpected removals, stop and inspect project discovery patterns and stable resource IDs. If a managed revision is rejected, confirm that its immutable digest was staged on the same target and connection named by the project. If an environment assertion fails, correct the target rather than changing the assertion to match an unintended instance.

Deployment failures should leave the last valid serving state active. Verify that state, preserve the rejected candidate and server diagnostics, then correct and re-run validation and planning. Do not repeatedly submit a changing candidate while diagnosing one failure.

For guided `deploy`, retain the reported operation handle. Exit 3 means confirmation or server approval is still needed; exit 4 means the result is indeterminate. Follow the reported next action with `--resume` and the retained handle. Confirm only the exact reviewed plan with `--confirm-plan`. A client timeout does not prove that the server failed, and client confirmation never substitutes for server approval.

## Find command-specific help

Use `leapview <command> --help` for syntax at the terminal and the [generated CLI reference](/docs/cli/reference) for complete flags and subcommands. Continue with [Authentication](/docs/cli/authentication), [Targets and environments](/docs/cli/targets), or [Develop, review, and publish](/docs/cli/validate-deploy) according to the failing stage.
