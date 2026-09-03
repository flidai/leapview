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

## Audit-outbox metrics collection failure alert fires

`LeapViewAuditOutboxMetricsCollectionFailure` means `leapview_audit_outbox_scrape_error` has reported failure, or that metric has been absent from an otherwise scrapeable LeapView target, for more than five minutes. Confirm the affected target still has `up{job="leapview"} == 1`, inspect the protected metrics response without exposing its bearer token, and compare readiness with the bounded `leapview admin audit-outbox` inspection from the approved maintenance environment. If the entire target is unavailable, follow the target-unavailable procedure instead.

Preserve the alert timing, `job` and `instance`, image and deployment digests, readiness result, metric-family presence, collection timeout or aggregate-store error, storage health, and credential-scrubbed application logs. Do not copy audit payloads, metadata, actor, request, project, resource, or event identifiers into alert annotations or unrestricted incident records. Likely boundaries are the local audit store, its aggregate query, storage availability, query timeout, metrics registration, or a deployment mismatch that removed the collector.

Restore access to the existing audit store or deploy the validated collector through the normal service lifecycle. Do not disable the collector, synthesize a zero metric, or treat absent telemetry as healthy. Recovery requires `leapview_audit_outbox_scrape_error` to be present and equal to zero on consecutive successful scrapes, readiness to be assessed independently, aggregate outbox state to be inspectable, and the alert to resolve after its hold and evaluation interval.

## Audit-outbox terminal intents alert fires

`LeapViewAuditOutboxTerminalIntents` means at least one `poison` or `quarantined` audit intent has remained unresolved for more than five minutes. Confirm the aggregate `leapview_audit_outbox_intents{state=~"poison|quarantined"}` count, then use `leapview admin audit-outbox` from the controlled offline maintenance environment to inspect its bounded terminal index. Preserve each printed event ID, terminal state, attempt count, bounded failure code, immutable payload digest, creation time, alert timing, and a consistent backup checksum in restricted incident evidence.

Poison usually marks exhausted delivery attempts; quarantine marks an integrity or payload conflict. Investigate the audit sink, local storage, dispatcher lifecycle, aggregate ordering, and source operation before recovery. Never delete an intent, edit its payload or metadata, bypass its digest/state guards, increase attempts by hand, or requeue an identity that was not obtained from the bounded inspection.

Stop the running instance so the offline lock can be acquired, then follow the exact guarded requeue procedure in [Audit events](/docs/security/audit#monitor-durable-delivery) for one reviewed terminal intent. A stale or conflicting result is a stop condition requiring another inspection. Recovery requires poison and quarantined aggregate counts to return to zero, the reviewed event to reach `delivered`, readiness to pass, exactly one `audit.outbox.requeued` recovery event to exist, one representative audited mutation to materialize, and the alert to resolve.

## Audit-outbox excessive backlog alert fires

`LeapViewAuditOutboxExcessiveBacklog` means the current undelivered audit population has remained above 10,000 intents or its oldest intent has remained older than one hour for more than ten minutes. The expression sums only current `pending`, `retry`, `leased`, `poison`, and `quarantined` states and aggregates `state` away. Exactly 10,000 intents and exactly one hour do not cross the code-owned readiness boundary. Confirm both the aggregate state sum and `leapview_audit_outbox_oldest_undelivered_age_seconds`; historical attempt and materialization totals are not health signals.

Preserve alert and readiness timing, all aggregate state counts, oldest age, capacity and remaining capacity, dispatcher and lease status, storage health, sink availability, recent deployment changes, and credential-scrubbed retry errors. Growing pending or retry state points toward dispatcher or sink throughput; persistent leased work points toward worker or lease lifecycle; terminal state requires the terminal-intent procedure. Do not export payloads or unrestricted identity metadata while investigating aggregate pressure.

Restore the dispatcher, audit sink, storage, or capacity dependency through its normal owner workflow and allow the durable retry and ordering policy to drain the queue. Do not discard evidence, edit timestamps, bypass aggregate ordering, or raise the threshold to clear readiness. Recovery requires the undelivered count to be at most 10,000, oldest age to be at most one hour, counts to keep declining under representative audited traffic, readiness to pass, and the alert to resolve without immediate recurrence.

## Audit-outbox capacity exhausted alert fires

`LeapViewAuditOutboxCapacityExhausted` means a positive configured `leapview_audit_outbox_capacity` has had `leapview_audit_outbox_capacity_remaining <= 0` for more than five minutes. New audited mutations fail closed at this boundary, so confirm both capacity gauges, readiness, aggregate state counts, oldest age, and whether representative security-sensitive commands are being rejected. A missing capacity series is telemetry uncertainty, not proof of available capacity.

Preserve the alert onset, capacity and remaining capacity, aggregate states, oldest age, terminal index when applicable, image and configuration revision without secrets, storage and sink evidence, and the first bounded failure code. Likely boundaries include a stopped or failing dispatcher, unavailable audit sink, storage failure, aggregate-order blockage, or unresolved terminal evidence. Capacity is a local evidence-safety limit; external archive availability does not prove the local handoff is healthy.

Stop admitting nonessential audited mutations through normal traffic controls while preserving recovery access. Repair the underlying delivery boundary and use only the reviewed guarded terminal recovery procedure where applicable; do not increase capacity as an incident shortcut, delete rows, or edit payloads. Recovery requires positive remaining capacity, a draining undelivered population, terminal counts of zero or separately controlled recovery, readiness to pass, a representative audited mutation to materialize, and the alert to resolve.

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

Inspect the latest generation, executor read/write limits, queue lengths, timeouts, source reachability, temporary capacity, and first failing Model materialization. Older runs may be intentionally superseded.

Do not increase concurrency until CPU, memory, disk, and catalog write capacity show headroom. More simultaneous work can make a saturated single node less available.

## Disk usage grows

Identify whether growth is managed-upload staging, managed objects, DuckLake
catalog data, analytical Parquet, logs, or runtime cache. The removed offline
storage-cleanup command is not available; do not delete catalog rows or
Parquet/object-store data manually. Use provider-native retention and garbage
collection procedures only after confirming active serving snapshots and query
leases, and record the procedure in the operations runbook.

## Dashboard refresh fast-burn alert fires

Confirm that `leapview:dashboard_refresh_reliability:burn_rate_5m` and `leapview:dashboard_refresh_reliability:burn_rate_1h` are both at least `14.4` for the affected `job` and `instance`, and that `leapview:dashboard_refresh_reliability:eligible_events_1h` is at least `10`. The critical alert has no additional hold duration: its five-minute and one-hour agreement is the persistence check. Missing, canceled-only, or lower-volume traffic cannot fire it.

Inspect the bounded dashboard refresh outcome metric and application logs to determine whether `partial`, `error`, or `other` outcomes dominate. Correlate the incident with the latest deployment, active serving state, managed-data revisions, DuckDB health, executor saturation, queue depth, and dependency errors. Keep command, outcome, request, trace, principal, project, and resource dimensions in restricted diagnostic evidence; they are intentionally absent from alert identity.

Mitigate the underlying refresh failure through the normal deployment, data, or runtime recovery path. Do not change the 99% objective, reclassify outcomes, or exclude eligible traffic merely to silence the alert. Confirm recovery when the five-minute burn rate falls below `14.4` and the alert resolves on the next rule evaluation. Continue to inspect the rolling 30-day error-budget recordings because alert recovery does not restore budget already consumed.

## Dashboard refresh latency fast-burn alert fires

`LeapViewDashboardRefreshLatencyFastBurn` means slow completed dashboard refreshes are consuming the 99% latency SLO budget at least `14.4` times faster than its sustainable rate over both five minutes and one hour. Confirm that `leapview:dashboard_refresh_latency:burn_rate_5m` and `leapview:dashboard_refresh_latency:burn_rate_1h` are both at least `14.4` for the affected `job` and `instance`, and that `leapview:dashboard_refresh_latency:completed_events_1h` is at least `10`. The alert has no additional hold duration; dual-window agreement is its persistence check, and rule evaluation can lag the source condition by one interval.

Inspect `leapview:dashboard_refresh_latency:completed_events_5m`, `leapview:dashboard_refresh_latency:slow_completed_events_5m`, and their one-hour equivalents to distinguish broad degradation from sparse samples. Compare those recordings with `leapview:dashboard_refresh_latency:timely_completed_ratio_5m`, the rolling 30-day ratio, and latency error-budget state. At the source, compare the completed `leapview_dashboard_refresh_duration_seconds_count` series with the cumulative completed `le="5"` bucket; the subtraction represents completed work above five seconds. Missing, canceled-only, or fewer than 10 completed one-hour events cannot fire the alert. A single slow refresh below that floor is diagnostic evidence, not a page.

Preserve the alert evaluation time, bounded `job` and `instance`, both burn rates, completed and slow-completed volumes, the raw histogram changes, active deployment and serving-state identities, refresh and generation identifiers, executor or queue saturation, DuckDB health, managed-data revision state, and relevant restricted logs. Do not place request, trace, user, project, resource, command, or outcome identifiers into alert labels or unrestricted incident records. Because the latency population includes completed refreshes only, also check the outcome-reliability alerts and recordings: simultaneous latency and outcome burn indicates both slow successful work and terminal failures rather than duplicate alerts.

Mitigate the identified workload, deployment, data, dependency, or capacity boundary through its normal controlled recovery procedure. Do not raise the five-second boundary, alter the 99% objective, discard completed events, or restart destructively merely to clear the alert. Recovery is objective when the five-minute latency burn rate falls below `14.4`, the alert resolves after the next evaluation, completed refresh volume remains evaluable, and new completed work no longer accumulates an excessive slow fraction. Continue to record the rolling 30-day latency budget because alert resolution does not restore budget already consumed.

## Dashboard refresh latency slow-burn alert fires

`LeapViewDashboardRefreshLatencySlowBurn` means slow completed dashboard refreshes are consuming the 99% latency SLO budget at least `6` times faster than its sustainable rate over both 30 minutes and 6 hours. Confirm that `leapview:dashboard_refresh_latency:burn_rate_30m` and `leapview:dashboard_refresh_latency:burn_rate_6h` are both at least `6` for the affected `job` and `instance`, and that `leapview:dashboard_refresh_latency:completed_events_6h` is at least `60`. The warning has no additional hold duration; dual-window agreement is its persistence check, and rule evaluation can lag the source condition by one interval.

Inspect the 30-minute and 6-hour completed and slow-completed event recordings to distinguish sustained degradation from sparse traffic. Compare them with the five-minute and rolling 30-day timely-completed ratios, latency error-budget state, and raw completed histogram count and cumulative `le="5"` bucket. Missing, canceled-only, or fewer than 60 completed six-hour events cannot fire the alert. Check the critical latency fast-burn alert first when both are active; warning inhibition remains operator-owned.

Preserve the alert evaluation time, bounded `job` and `instance`, both burn rates, completed and slow-completed volumes, raw histogram changes, deployment and serving-state identities, refresh and generation identifiers, saturation evidence, DuckDB health, managed-data revision state, and relevant restricted logs. Keep request, trace, user, project, resource, command, and outcome identifiers out of alert labels and unrestricted incident records. Also inspect outcome-reliability alerts: simultaneous latency and outcome burn represents slow successful work alongside terminal failures, not duplicate evidence.

Mitigate the identified workload, deployment, data, dependency, or capacity boundary through its normal controlled recovery procedure. Do not change the five-second boundary or 99% objective, discard completed events, or restart destructively to silence the warning. Recovery is objective when the 30-minute latency burn rate falls below `6`, the alert resolves after the next evaluation, completed volume remains evaluable, and new completed work no longer accumulates an excessive slow fraction. Continue tracking the rolling 30-day latency budget because resolution does not restore budget already consumed.

## Dashboard refresh slow-burn alert fires

Confirm that `leapview:dashboard_refresh_reliability:burn_rate_30m` and `leapview:dashboard_refresh_reliability:burn_rate_6h` are both at least `6` for the affected `job` and `instance`, and that `leapview:dashboard_refresh_reliability:eligible_events_6h` is at least `60`. The warning alert has no additional hold duration: its 30-minute and 6-hour agreement is the persistence check. Missing, canceled-only, or lower-volume traffic cannot fire it.

Inspect the bounded refresh outcome metric and application logs for sustained `partial`, `error`, or `other` outcomes. Correlate the degradation with deployments, serving-state changes, managed-data revisions, DuckDB health, executor saturation, queue depth, and dependency errors. If the critical fast-burn alert is also active, follow the fast-burn response first; Alertmanager warning inhibition remains operator-owned. Keep command, outcome, request, trace, principal, project, and resource dimensions in restricted diagnostic evidence rather than alert identity.

Mitigate the underlying refresh failure through the normal deployment, data, or runtime recovery path. Do not change the 99% objective, reclassify outcomes, or exclude eligible traffic merely to silence the warning. Confirm recovery when the 30-minute burn rate falls below `6` and the alert resolves after the next rule evaluation. Continue to inspect the rolling 30-day error-budget recordings because recovery stops new excessive consumption but does not restore budget already spent.

## Provider recovery or catalog drift

LeapView does not schedule or execute production backup, restore, image
upgrade, host rollback, or recovery-qualification drills. If PostgreSQL,
DuckLake, or object-store state is unavailable or inconsistent, stop writes and
preserve readiness, metrics, deployment identifiers, and credential-scrubbed
logs. Use the provider's native backup/PITR, catalog snapshot, versioning,
replication, or restore procedure; follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore) for the complete workflow. Do not edit control-plane rows, catalog metadata, or
Parquet/object-store objects by hand.

After provider recovery, verify instance identity, active deployment pointers,
authorization, managed-data revisions, and representative governed queries
before reopening traffic. Keep the failed state and provider evidence until
the incident is closed.

## Correlated incident investigation workflow

Use this workflow with the specific response for [target unavailability](#target-unavailable-alert-fires), [sustained HTTP 5xx responses](#sustained-http-5xx-alert-fires), [DuckDB fatal health](#duckdb-fatal-health-alert-fires), [audit-outbox metric collection](#audit-outbox-metrics-collection-failure-alert-fires), [terminal audit intents](#audit-outbox-terminal-intents-alert-fires), [audit backlog](#audit-outbox-excessive-backlog-alert-fires), [audit capacity exhaustion](#audit-outbox-capacity-exhausted-alert-fires), [dashboard refresh outcome fast burn](#dashboard-refresh-fast-burn-alert-fires), [dashboard refresh latency fast burn](#dashboard-refresh-latency-fast-burn-alert-fires), [dashboard refresh latency slow burn](#dashboard-refresh-latency-slow-burn-alert-fires), [dashboard refresh slow burn](#dashboard-refresh-slow-burn-alert-fires), or [provider recovery or catalog drift](#provider-recovery-or-catalog-drift). It defines evidence pivots and recovery records; it does not authorize remediation.

### 1. Record alert intake

Preserve the alert name, first observed firing time, current evaluation time, `job`, `instance`, severity, summary, and affected component before changing the service or monitoring configuration. The alert firing time follows any configured hold duration and Prometheus evaluation interval, so record the earliest independently observed symptom separately rather than treating the firing time as the incident start. Keep the original labels and annotations with the incident record and confirm which deployed rule revision produced them.

Use the alert-specific procedure to identify the first reported boundary: scrape path for target unavailability, application HTTP handling for sustained 5xx responses, the process-owned analytical runtime for fatal DuckDB health, audit-outbox collection, delivery, backlog, or capacity, dashboard refresh reliability for burn alerts, or the provider's recovery boundary for catalog or object-store drift. `job` and `instance` identify the bounded alert target; they do not identify a user, project, request, or root cause.

### 2. Inspect metrics and reliability context

Read the active alert expression and compare it with the repository-owned rule. Evaluate its source metrics for the same `job`, `instance`, and a time range that begins before the first symptom. Inspect raw inputs before derived recordings so missing source series, scrape gaps, counter resets, and aggregation are not mistaken for recovery or healthy traffic. Preserve the expression, evaluation range, observed values, and relevant scrape health in the incident record.

For dashboard refresh outcome burn alerts, inspect the matching short- and long-window burn rates, eligible-event floor, eligible and bad event volumes, five-minute and 30-day reliability ratios, and rolling outcome error-budget state. For the latency fast-burn alert, inspect its five-minute and one-hour burn rates, completed-event floor, completed and slow-completed volumes, timely-completed ratios, and rolling latency budget. The completed-only latency population does not redefine outcome reliability, and the outcome population does not classify latency. Missing ratios or budget recordings mean the applicable population was absent or unevaluable, not that reliability or timeliness was perfect.

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

Use the objective recovery criteria in the alert-specific procedure. Record the time source metrics returned to their expected state, the alert resolved, and representative liveness, readiness, request, or analytical checks succeeded as applicable. Alert resolution alone does not prove user-visible recovery, and repeated restarts, manual pointer edits, deleted active state, relaxed thresholds, or reclassified outcomes are not recovery evidence.

Use the generated [environment variable reference](/docs/configuration), [`config` CLI reference](/docs/cli/config), [`admin` CLI reference](/docs/cli/admin), and [API reference](/docs/api) when confirming exact names, flags, and operations during diagnosis.
