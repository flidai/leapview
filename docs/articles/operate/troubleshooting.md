# Operational troubleshooting

Start at the first boundary that reports failure and preserve the last active state. Capture timestamps, request or job IDs, application image digest, environment, deployment ID, revision digest, and relevant logs before restarting or retrying.

## Process does not start

Run production validation in the same environment as the service:

```sh
leapview config validate --production
```

Confirm `LEAPVIEW_HOME`, DuckLake catalog, analytical data, and managed-data directories exist and are writable by the service identity. Check remote catalog and object-store connectivity, free space, file descriptor limits, and port binding.

Verify required secrets are present by name without printing their values. An incomplete OIDC, Azure, S3, or credential pair is intentionally rejected.

## Liveness or readiness fails

If the process is absent or `/healthz` fails, inspect process exit and startup logs. If liveness passes but `/readyz` fails, investigate required state attachment and runtime readiness rather than restarting repeatedly.

Compare direct local checks with reverse-proxy checks. A healthy local service with a failing public endpoint points to TLS, routing, host allowlist, firewall, or proxy configuration.

## Authentication fails

For browser auth, confirm exact public issuer and callback URLs, secure cookies, allowed hosts, clock synchronization, provider client credentials, and proxy scheme/host preservation. Test with a fresh private browser session to separate provider failure from a stale cookie.

For tokens, confirm the token is not expired or revoked, the principal remains active, and project/privilege restrictions include the operation. Do not replace a scoped token with a broad owner token merely to make a test pass.

Keep a tested local break-glass path only when policy permits it. Audit every use and rotate temporary credentials afterward.

## Health passes but the project catalog is empty

Check that the intended project deployment is active in the instance environment and that the principal has project-resource access. A successful application upgrade does not publish a project.

List project resources through the CLI/API with the same principal. Inspect deployment activation, the instance environment returned by `GET /api/v1/instance`, and project role bindings.

## Dashboards load but queries fail

Reproduce below the browser with semantic-model or dashboard CLI commands. Inspect active managed revisions, latest refresh state, query queue saturation, timeout, and source/analytical storage availability.

If every query fails, suspect runtime/storage or active deployment. If one semantic field or visual fails, suspect its model, relationship, filter, or query contract. Preserve the exact error and request identity.

## Project deployment fails

- Run local validation and fix every diagnostic.
- Generate a target-aware plan and review active differences.
- Confirm the service principal can deploy to the environment.
- Verify every managed connection has a ready revision for the target to pin.
- Verify each selected revision is staged for the same project and connection.
- Check candidate project and access-resource references.

A failed candidate should leave active projects and revisions unchanged. Confirm that invariant before retrying.

## Refreshes queue or fail

Inspect the latest generation, executor read/write limits, queue lengths, timeouts, source reachability, temporary capacity, and first failing model table. Older runs may be intentionally superseded.

Do not increase concurrency until CPU, memory, disk, and catalog write capacity show headroom. More simultaneous work can make a saturated single node less available.

## Disk usage grows

Identify whether growth is backups, managed upload staging, managed objects, DuckLake catalog, analytical Parquet, logs, or runtime cache. Run storage cleanup without `--apply` first:

```sh
leapview admin storage cleanup
```

Review protected serving states and query leases, then use `--apply` only under the approved maintenance procedure. Do not delete catalog rows or Parquet objects manually.

## Dashboard refresh fast-burn alert fires

Confirm that `leapview:dashboard_refresh_reliability:burn_rate_5m` and `leapview:dashboard_refresh_reliability:burn_rate_1h` are both at least `14.4` for the affected `job` and `instance`, and that `leapview:dashboard_refresh_reliability:eligible_events_1h` is at least `10`. The critical alert has no additional hold duration: its five-minute and one-hour agreement is the persistence check. Missing, canceled-only, or lower-volume traffic cannot fire it.

Inspect the bounded dashboard refresh outcome metric and application logs to determine whether `partial`, `error`, or `other` outcomes dominate. Correlate the incident with the latest deployment, active serving state, managed-data revisions, DuckDB health, executor saturation, queue depth, and dependency errors. Keep command, outcome, request, trace, principal, project, and resource dimensions in restricted diagnostic evidence; they are intentionally absent from alert identity.

Mitigate the underlying refresh failure through the normal deployment, data, or runtime recovery path. Do not change the 99% objective, reclassify outcomes, or exclude eligible traffic merely to silence the alert. Confirm recovery when the five-minute burn rate falls below `14.4` and the alert resolves on the next rule evaluation. Continue to inspect the rolling 30-day error-budget recordings because alert recovery does not restore budget already consumed.

## Dashboard refresh slow-burn alert fires

Confirm that `leapview:dashboard_refresh_reliability:burn_rate_30m` and `leapview:dashboard_refresh_reliability:burn_rate_6h` are both at least `6` for the affected `job` and `instance`, and that `leapview:dashboard_refresh_reliability:eligible_events_6h` is at least `60`. The warning alert has no additional hold duration: its 30-minute and 6-hour agreement is the persistence check. Missing, canceled-only, or lower-volume traffic cannot fire it.

Inspect the bounded refresh outcome metric and application logs for sustained `partial`, `error`, or `other` outcomes. Correlate the degradation with deployments, serving-state changes, managed-data revisions, DuckDB health, executor saturation, queue depth, and dependency errors. If the critical fast-burn alert is also active, follow the fast-burn response first; Alertmanager warning inhibition remains operator-owned. Keep command, outcome, request, trace, principal, project, and resource dimensions in restricted diagnostic evidence rather than alert identity.

Mitigate the underlying refresh failure through the normal deployment, data, or runtime recovery path. Do not change the 99% objective, reclassify outcomes, or exclude eligible traffic merely to silence the warning. Confirm recovery when the 30-minute burn rate falls below `6` and the alert resolves after the next rule evaluation. Continue to inspect the rolling 30-day error-budget recordings because recovery stops new excessive consumption but does not restore budget already spent.

## Recovery qualification freshness alerts fire

Inspect the bounded ledger projection with the service stopped or from the same controlled maintenance environment used for other offline Admin commands:

```sh
leapview admin recovery status
```

For a ledger scrape failure, check the application logs for ledger read errors or the two-second collection timeout and confirm the protected metrics endpoint still scrapes successfully. For an overdue qualification, distinguish a schedule occurrence that was not materialized from pending work or an expired execution lease; the configured staleness policy has already elapsed. For failed evidence publication, check the private evidence destination, service-account access, capacity, and retry worker without printing credentials, signed URLs, evidence content, or failure payloads.

Correct the underlying scheduler, worker, ledger, or publication dependency and let normal reconciliation or persisted publication backoff recover the current state. Do not delete ledger rows or rerun a destructive recovery scenario merely to clear an alert. Preserve occurrence and evidence identities in the restricted incident record, then confirm `leapview_recovery_qualification_scrape_error`, `leapview_recovery_qualification_overdue`, or `leapview_recovery_qualification_evidence{state="failed"}` returns to zero and the alert resolves on the next rule evaluation.

## Gather a useful incident record

Record impact, start time, last known good deployment/image/revision, failing identities, relevant metrics and logs, attempted actions, and whether active state changed. Redact secrets but keep stable digests and IDs.

Correct configuration, data, or project candidates through normal workflows. Repeated restarts, manual pointer edits, and deleting active state destroy evidence and can turn a contained failure into data loss.

Use the generated [environment variable reference](/docs/configuration), [`config` CLI reference](/docs/cli/config), [`admin` CLI reference](/docs/cli/admin), and [API reference](/docs/api) when confirming exact names, flags, and operations during diagnosis.
