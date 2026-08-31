# Operational troubleshooting

Start at the first boundary that reports failure and preserve the last active state. Capture timestamps, request or job IDs, application image digest, environment, deployment ID, revision digest, and relevant logs before restarting or retrying. For repository-owned Prometheus alerts, use the alert-specific procedure below together with the [correlated incident investigation workflow](#correlated-incident-investigation-workflow).

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

## Target unavailable alert fires

`LeapViewTargetUnavailable` means Prometheus has reported `up{job="leapview"} == 0` for the affected `instance` for more than two minutes. Confirm the target is still present in service discovery, inspect Prometheus's current scrape error and last successful scrape, and compare the protected metrics endpoint with direct `/healthz` and `/readyz` checks from an approved network path. Do not print or copy the metrics bearer token into incident notes or commands.

Preserve the alert start time, target labels, image digest, recent process exit or startup evidence, health responses, and bounded scraper, TLS, proxy, DNS, and network diagnostics. A failed scrape can originate at the process, listener, authentication, certificate, name-resolution, network-policy, proxy, or scraper-configuration boundary; a healthy local process with a failed remote scrape narrows the failure away from application liveness.

Restore the intended discovered target, credentials, certificate chain, or network path through the normal deployment and monitoring workflow. If the process is absent or irrecoverably unhealthy, preserve its evidence before replacing it through the approved service lifecycle; do not remove the target from discovery or disable the alert to create a healthy signal. Recovery requires `up` to return `1` for consecutive scrapes, the alert to resolve on the next rule evaluation, and liveness and readiness to be assessed independently before returning traffic.

## Sustained HTTP 5xx alert fires

`LeapViewSustainedHTTP5xx` means LeapView has observed a positive five-minute rate of application-generated HTTP 5xx responses continuously for more than ten minutes on the affected `instance`. Confirm the aggregate alert expression, then use the bounded source metric to identify method, route, and status patterns. Compare an affected request with `/healthz` and `/readyz`; a ready process can still have a failing route or dependency, while a proxy-generated response that never reaches LeapView will not appear in this application metric.

Collect the alert start time, affected route and status classes, request and correlation IDs, validated upstream trace IDs when present, image and deployment digests, active serving-state identity, dependency health, executor saturation, queue depth, and credential-scrubbed application errors. Distinguish route-specific validation or dependency failures from process-wide runtime, storage, network, or active-deployment failures. Keep principals, projects, resources, query values, authorization, cookies, and bodies out of alert annotations and unrestricted incident records.

Correct the failing configuration, dependency, capacity condition, data state, or deployment through its normal owner workflow. A reviewed rollback or instance replacement is appropriate only when the incident is correlated with that change and active-state evidence has been preserved; do not reclassify 5xx responses, change the threshold, or suppress the alert as mitigation. Recovery requires the five-minute 5xx rate to return to zero, representative affected requests to succeed, and the alert to resolve after the next evaluation without immediate recurrence.

## DuckDB fatal health alert fires

`LeapViewDuckDBFatalHealth` means a process-owned DuckDB environment has reported a fatal analytical cleanup-safety failure for more than one minute. Confirm `leapview_duckdb_fatal_health{job="leapview"} > 0` for the affected `instance` and inspect readiness and the first credential-scrubbed fatal error. This signal is absent when LeapView does not own a process-local DuckDB environment and is distinct from an ordinary failed query or transient cleanup retry.

Preserve the alert onset, image and deployment digests, active serving state and managed revision identities, fatal and cleanup outcomes, connection and lease state, disk and memory pressure, recent refresh activity, and the first relevant runtime error. Likely boundaries include process-owned DuckDB connection lifecycle, secret-scope cleanup, interrupted refresh work, analytical storage, and host resource exhaustion. Do not capture raw queries, credentials, signed URLs, catalog contents, or sensitive result data in unrestricted evidence.

Stop admitting affected analytical work through the normal traffic-management path and follow the approved service or release recovery procedure. Fatal health is sticky for the affected DuckDB environment, so do not attempt to clear it by editing catalogs, deleting analytical files, forcing lease cleanup, or repeatedly restarting without diagnosis. Recovery requires replacement with a validated environment, `leapview_duckdb_fatal_health` to report zero, readiness to pass, one bounded representative analytical request to succeed, and the alert to resolve without another fatal transition.

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

## Correlated incident investigation workflow

Use this workflow with the specific response for [target unavailability](#target-unavailable-alert-fires), [sustained HTTP 5xx responses](#sustained-http-5xx-alert-fires), [DuckDB fatal health](#duckdb-fatal-health-alert-fires), [dashboard refresh fast burn](#dashboard-refresh-fast-burn-alert-fires), [dashboard refresh slow burn](#dashboard-refresh-slow-burn-alert-fires), or [recovery qualification freshness](#recovery-qualification-freshness-alerts-fire). It defines evidence pivots and recovery records; it does not authorize remediation.

### 1. Record alert intake

Preserve the alert name, first observed firing time, current evaluation time, `job`, `instance`, severity, summary, and affected component before changing the service or monitoring configuration. The alert firing time follows any configured hold duration and Prometheus evaluation interval, so record the earliest independently observed symptom separately rather than treating the firing time as the incident start. Keep the original labels and annotations with the incident record and confirm which deployed rule revision produced them.

Use the alert-specific procedure to identify the first reported boundary: scrape path for target unavailability, application HTTP handling for sustained 5xx responses, the process-owned analytical runtime for fatal DuckDB health, dashboard refresh reliability for burn alerts, or recovery scheduling, execution, ledger collection, and evidence publication for recovery alerts. `job` and `instance` identify the bounded alert target; they do not identify a user, project, request, or root cause.

### 2. Inspect metrics and reliability context

Read the active alert expression and compare it with the repository-owned rule. Evaluate its source metrics for the same `job`, `instance`, and a time range that begins before the first symptom. Inspect raw inputs before derived recordings so missing source series, scrape gaps, counter resets, and aggregation are not mistaken for recovery or healthy traffic. Preserve the expression, evaluation range, observed values, and relevant scrape health in the incident record.

For dashboard refresh burn alerts, inspect the matching short- and long-window burn rates, eligible-event floor, eligible and bad event volumes, five-minute and 30-day reliability ratios, and rolling error-budget state. Use the completed-refresh latency ratios only as separate evidence of successful-but-slow work; they do not redefine outcome reliability. Missing ratios or budget recordings mean the population was absent or unevaluable, not that reliability was perfect.

### 3. Correlate HTTP requests

For an application HTTP symptom, first narrow the bounded HTTP metric by method, route pattern, and status, then find structured `http request` records on the affected instance and time range. The request log contains the concrete path rather than the bounded route label, so keep paths with project or resource identities in restricted evidence. Preserve `request_id` as the identity of one LeapView request and use `correlation_id` to group records only when the caller supplied a meaningful grouping; when no correlation ID was supplied it equals the request ID. The same canonical values are returned as `X-Request-ID` and `X-Correlation-ID`, including early middleware failures, so an observed response can be matched to the application request record.

Client-provided request and correlation IDs are untrusted evidence metadata. Treat them as opaque exact-match values, quote or escape them in operator tooling, and never use them as proof of a principal, authorization, tenancy, project access, or causality. Do not copy authorization, cookies, query values, request bodies, bearer tokens, or other secrets into searches or unrestricted incident records. If a request panicked, correlate the `http handler panic` record with the resulting request record by the same instance, time, method, and path; do not assume the panic record itself contains request or correlation IDs.

### 4. Use inbound trace identity when present

A valid remote W3C context adds `trace_id` and `upstream_span_id` to request and panic logs. Use those parsed values as an optional pivot into evidence retained by the upstream system, and record which system supplied that evidence. If the fields are absent, do not generate or infer them; malformed or missing inbound context is intentionally ignored.

LeapView currently creates no local spans, performs no sampling, and exports no trace telemetry. The upstream IDs prove only that valid remote context reached the HTTP boundary: they do not provide an end-to-end trace chain through LeapView or its background work. Never preserve raw `traceparent`, `tracestate`, or baggage headers in the incident record.

### 5. Correlate dashboard and domain evidence

For a dashboard incident, inspect structured `dashboard refresh` records in the affected instance and time range. Pivot within the refresh lifecycle using `refreshId`, `generation`, and `servingStateId`, then compare the outcome and target evidence with the active deployment, image and revision digests, managed-data revisions, query or audit events, DuckDB health, and workload saturation. Keep dashboard, page, project, principal, and resource identifiers only in the restricted incident record when they are necessary to explain impact.

Request and correlation IDs are not currently propagated through every refresh or background-work boundary. Do not claim a causal HTTP-to-refresh join unless another retained domain record establishes it. Otherwise describe the relationship as an inference based on the bounded instance and time range plus refresh, generation, or serving-state evidence, and record competing explanations.

### 6. Preserve the incident and recovery record

Record impact, UTC timestamps and time-zone source, alert details, last known good deployment, image and revision, Prometheus expressions and values, observed symptoms, relevant request, correlation, upstream trace, refresh, generation, and serving-state identifiers, evidence locations, actions taken, and whether active state changed. Separate observed facts from inferences. Redact secrets and sensitive payloads, retain only the minimum principal, project, or resource metadata required, and store restricted evidence according to its sensitivity and retention policy.

Use the objective recovery criteria in the alert-specific procedure. Record the time source metrics returned to their expected state, the alert resolved, and representative liveness, readiness, request, analytical, or qualification checks succeeded as applicable. Alert resolution alone does not prove user-visible recovery, and repeated restarts, manual pointer edits, deleted active state, relaxed thresholds, or reclassified outcomes are not recovery evidence.

Use the generated [environment variable reference](/docs/configuration), [`config` CLI reference](/docs/cli/config), [`admin` CLI reference](/docs/cli/admin), and [API reference](/docs/api) when confirming exact names, flags, and operations during diagnosis.
