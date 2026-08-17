# CLI troubleshooting

Troubleshoot from the narrowest local check outward. Preserve the exact command, exit status, and sanitized error output before changing configuration; the first failure is usually more useful than errors produced after several speculative changes.

## Validate the local environment

Check process-wide production requirements, then compile the project without contacting a target:

```sh
leapview config validate --production
leapview validate --project dashboards/leapview.yaml
```

Validation diagnostics identify the source file and invalid field or reference. Fix the earliest root diagnostic first; later missing-resource messages may be consequences of it. If behavior differs in CI, compare the project revision, working directory, generated files, environment variables, and CLI version.

## Check server readiness

If local validation succeeds but a remote command cannot connect, check the instance readiness endpoint directly:

```sh
leapview healthcheck \
  --url https://dash.example.com/readyz \
  --timeout 10s
```

A connection or TLS error points to DNS, certificates, proxies, or network policy. A non-success readiness response means the instance is reachable but not ready; inspect server logs before retrying a deployment.

## Check target identity and authentication

Confirm the scheme, hostname, expected environment, and project. Saved credentials are keyed by the exact target URL. Run a read-only plan with explicit boundaries:

```sh
leapview plan \
  --project dashboards/leapview.yaml \
  --target https://dash.example.com \
  --environment production \
  --project project:retail
```

For `401` responses, verify that a token was supplied for that target and is still valid. For `403`, inspect the authenticated identity's effective grants for the failed operation. Do not broaden the credential until the missing privilege is understood.

## Interpret planning and deployment failures

If a plan shows unexpected removals, stop and inspect project discovery patterns and stable resource IDs. If a managed revision is rejected, confirm that its immutable digest was staged on the same target and connection named by the project. If an environment assertion fails, correct the target rather than changing the assertion to match an unintended instance.

Deployment failures should leave the last valid serving state active. Verify that state, preserve the rejected candidate and server diagnostics, then correct and re-run validation and planning. Do not repeatedly submit a changing candidate while diagnosing one failure.

## Find command-specific help

Use `leapview <command> --help` for syntax at the terminal and the [generated CLI reference](/docs/cli/reference) for complete flags and subcommands. Continue with [Authentication](/docs/cli/authentication), [Targets and environments](/docs/cli/targets), or [Develop, review, and publish](/docs/cli/validate-deploy) according to the failing stage.
