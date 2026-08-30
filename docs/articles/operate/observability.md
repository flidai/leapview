# Health and observability

Observe LeapView at the process, dependency, delivery, data, and query layers. A single “up” signal cannot distinguish a healthy process with a broken active project from a slow query caused by an invalid model grain.

## Liveness and readiness

`/healthz` is the lightweight liveness endpoint. `/readyz` represents readiness for serving. The generated command checks readiness:

```sh
leapview healthcheck \
  --url https://leapview.example.com/readyz \
  --timeout 5s
```

Use liveness for process restart decisions and readiness for traffic admission. Keep both inexpensive; they should not execute a full dashboard query. Use separate synthetic probes for end-to-end analytical behavior.

The unauthenticated readiness response contains only stable check names and fixed states. Reviewed delivery-startup diagnostics may expose documented non-secret codes; platform, analytics, runtime, lease, and arbitrary custom-check errors collapse to `failed`, and active project identifiers are not returned. Use restricted application logs, metrics, and offline operator commands for internal error details rather than widening the readiness payload.

## Metrics

LeapView exposes Prometheus metrics behind `LEAPVIEW_METRICS_BEARER_TOKEN`. Production validation requires a strong token. Restrict the endpoint by network policy as well, inject the token into the scraper securely, and avoid logging it.

Monitor at least process resource use, request rate and latency, error status, read/write executor saturation, queue depth and timeouts, refresh activity, storage capacity, and managed upload failures. Alert on sustained conditions and user-visible symptoms rather than every transient supersession.

### Baseline health alerts

The repository-owned rule file at `deploy/observability/prometheus/leapview-alerts.yaml` provides portable alerts for an unavailable scrape target, sustained application 5xx responses, fatal process-owned DuckDB health, and recovery qualification freshness. The rules assume that Prometheus assigns LeapView targets the stable scrape label `job="leapview"`; `instance` must identify the bounded scrape target. Alert identity is limited to those scrape labels plus the static `service` and `severity` labels. Operation, publication state, occurrence, schedule, artifact, target, request or trace identities, principals, projects, and resource identifiers are aggregated away and never become alert labels.

Validate the syntax, PromQL behavior, firing delay, labels, and annotations with the pinned Prometheus toolchain:

```sh
task observability:alerts:check
```

The task downloads the official Prometheus archive for the supported Linux or macOS architecture, verifies its pinned SHA-256 digest, caches `promtool` under `.tmp/tools`, then runs `promtool check rules` and the healthy and firing rule fixtures.

Copy or mount the rule files into the Prometheus deployment and reference them from the Prometheus configuration:

```yaml
rule_files:
  - /etc/prometheus/rules/leapview-alerts.yaml
  - /etc/prometheus/rules/leapview-recording-rules.yaml
```

Reload Prometheus only after validation succeeds. The target-unavailable rule relies on Prometheus's standard `up` metric and therefore requires the target to remain present in Prometheus service discovery; a target omitted from the scrape configuration cannot alert. The 5xx rule uses `leapview_http_requests_total`, aggregates method, route, and status dimensions down to `job` and `instance`, and requires a positive five-minute error rate continuously for ten minutes. `leapview_duckdb_fatal_health` is present only when LeapView owns a process-local DuckDB environment.

Recovery alerts use current-state ledger projections. `leapview_recovery_qualification_scrape_error` clears after a successful ledger collection. `leapview_recovery_qualification_overdue` is recalculated from each active schedule's staleness policy and current occurrences, so ordinary missing or due work does not alert before its policy deadline. `leapview_recovery_qualification_evidence{state="failed"}` represents unresolved publication failures, remains retryable with persisted backoff, and clears after publication succeeds. The retained terminal-history gauge `leapview_recovery_qualification_failed` is deliberately not used for alerting because a later successful qualification does not remove old failures. Hold windows of five, ten, and fifteen minutes respectively absorb transient collection, reconciliation, and publication retry conditions.

Every alert links to [Operational troubleshooting](/docs/guides/operate/troubleshooting); recovery alerts link directly to the [recovery qualification response](/docs/guides/operate/troubleshooting#recovery-qualification-freshness-alerts-fire). Configure routing and receivers in the operator-owned Alertmanager deployment.

### Dashboard refresh reliability SLI

The repository-owned recording rules at `deploy/observability/prometheus/leapview-recording-rules.yaml` derive dashboard refresh reliability only from `leapview_dashboard_refresh_duration_seconds_count`. A finished refresh is eligible unless its outcome is `canceled`. `complete` is good; `partial`, `error`, and the bounded fallback `other` are bad. Cancellations are excluded because refresh supersession and stream closure are expected lifecycle behavior rather than evidence of failed dashboard work.

`leapview:dashboard_refresh_reliability:ratio_5m` records the five-minute good-to-eligible ratio. `leapview:dashboard_refresh_reliability:ratio_30d` records the rolling 30-day ratio. Both aggregate command and outcome dimensions to the stable `job` and `instance` scrape labels. Validate the rules, counter-reset behavior, empty-traffic handling, aggregation, and label contract with the existing pinned toolchain:

```sh
task observability:sli:check
```

Prometheus must retain at least 30 days of source samples for a complete rolling window. Until that history has accumulated after initial deployment or retention loss, the 30-day rule reflects only the available portion of the window. Counter resets are handled by `rate`. When an instance has no eligible refresh traffic, including when it has only canceled refreshes, the recording rules emit no ratio; missing or idle traffic is never reported as perfect reliability. Sparse traffic can make the five-minute ratio volatile and the 30-day ratio statistically weak, so operators should inspect eligible event volume before interpreting either series.

This SLI measures only completed dashboard refresh work. It does not measure refreshes still in flight, HTTP or SSE transport continuity, authentication, static assets, direct API queries, or an end-to-end synthetic journey. It defines no SLO target, error budget, burn rate, or alert threshold.

## Structured logs

Collect structured application logs from the service output. Preserve timestamp, severity, operation, route, status, duration, principal where safe, project, environment, request/correlation ID, deployment ID, revision digest, and refresh generation when available.

Secrets, bearer tokens, passwords, raw OAuth payloads, and sensitive query data must not appear in logs. Restrict log access according to the most sensitive metadata retained.

LeapView establishes `X-Request-ID` and `X-Correlation-ID` before process-wide middleware and route handling. A non-empty client request ID is preserved for idempotency compatibility; otherwise LeapView generates one. A missing correlation ID defaults to the request ID. Both canonical values are returned in response headers, including responses rejected by early security middleware, so proxy and application logs can agree on public request time, status, and correlation identity.

When an upstream service supplies valid W3C `traceparent` and `tracestate` headers, LeapView validates and carries that remote trace context through the request. Request and panic logs add the parsed `trace_id` and `upstream_span_id`; they never log the raw trace headers, baggage, authorization, cookies, query values, or request bodies. Missing or malformed trace context is ignored without rejecting the request or generating a trace ID, and the request ID remains LeapView's local correlation identity. This contract does not create spans, sample traces, or export telemetry.

## Delivery signals

Track project deployment IDs, environment, acting principal, candidate validation results, managed revision pins, activation outcome, and active deployment. Uploading an artifact or staging a data revision is not the same as successful activation.

Alert when production has no active deployment, a rollout repeatedly fails, or the active deployment differs from the intended promotion record.

## Data and refresh signals

Track refresh generation, target project asset, queued/running duration, terminal status, cancellation or supersession, and active serving state. Monitor source and output row counts, unexpected schema changes, data-file growth, and available disk space.

An expected superseded refresh is not necessarily an incident. Repeated failures of the latest generation, growing queue delay, or inability to activate a valid candidate are actionable.

## Query events and audit

Query events help identify slow or failing workloads by operation, project, duration, and diagnostic metadata. Audit events answer who changed security or administrative state. They serve different purposes and have separate retention controls.

Use `leapview admin maintenance` in dry-run mode to review bounded retention before applying deletion. Preserve relevant events externally when organizational policy requires longer history.

## Synthetic verification

After deployment or upgrade, run a small authenticated sequence:

1. Check readiness.
2. Request the current principal.
3. List an expected project resource.
4. Describe a known semantic model.
5. Execute one bounded semantic or dashboard query.
6. Confirm the active managed revision for production.

Keep the synthetic principal read-only and scoped to the test project. This verifies routing, auth, active project state, and analytical execution without granting deployment privilege.

## Recovery qualification ledger

Scheduled backup, restore, upgrade, and rollback qualifications use one durable
occurrence identity derived from the schedule, planned UTC time, scenario,
policy version, and target scope. Scheduler retries therefore attach to the
same occurrence instead of creating a second drill. Each attempt is protected
by a renewable generation fence; an expired worker cannot heartbeat, complete,
or publish evidence after another worker reclaims the occurrence.

Inspect the bounded operator projection with the service stopped or from the
same controlled maintenance environment used for other offline Admin commands:

```sh
leapview admin recovery status
```

The JSON response distinguishes an unconfigured system from configured
schedules, scheduled runs that were never materialized, overdue work, expired
execution or publication leases, running and failed work, and evidence
publication state. This projection is passive: scheduler and worker failure is
visible without requiring another worker to mutate the ledger. It also reports
recovered leases, last-success age, recovery-point age, and the latest restore,
readiness, and end-to-end qualification durations for the fixed operation set. The
same aggregate values are exported on the protected Prometheus endpoint under
`leapview_recovery_qualification_*`. Labels are limited to operation and
publication state—occurrence, schedule, artifact, scenario, and target
identifiers are never metric labels.

Terminal records retain the exact immutable artifact identity, measured
timestamps and durations, result, bounded content-digested evidence references,
and a redacted failure reason. Evidence upload has its own retryable lease, so
an upload outage does not rerun a destructive recovery drill. Never store raw
logs, archives, credentials, signed URLs, or secret-bearing query strings in a
ledger evidence reference.
Failed, canceled, or expired work that produced no owner evidence is terminal
with `evidenceStatus: "none"`; only a failed publication of real evidence is
retried, using persisted backoff.

Schedule definitions are immutable revisions. Changing artifact identity,
policy, target, scenario, cadence, or staleness closes the prior revision only
after its due occurrences are materialized, then creates a new revision
boundary. A catch-up run therefore cannot be relabeled as qualification of a
newer artifact.

The application recovery lifecycle reconciles definitions, enqueues due work,
claims one fenced logical occurrence, calls the operation owner's adapter, and
publishes the exact existing transition qualification, backup-manifest-v2, or
restore-preflight bytes after retaining and verifying them in the private
content-addressed evidence store. Ledger references use bounded
`artifact://qualification/...` identities rather than host filesystem paths, so
node topology is not copied into durable records. Evidence
publication and retention remain separate from scenario execution. If no
reviewed definitions and adapters are configured, status reports
`unconfigured: true`; an empty ledger must not be interpreted as successful
qualification.

Production composition registers the four owner adapters when
`LEAPVIEW_RECOVERY_QUALIFICATION_ENABLED=true`. Supply the exact released
`LEAPVIEW_IMAGE`, the admitted candidate bundle with its candidate-bound policy
through `LEAPVIEW_RECOVERY_QUALIFICATION_BUNDLE`, and its `leapviewctl` through
`LEAPVIEW_RECOVERY_QUALIFICATION_CONTROLLER`. The work directory must be an
absolute private path outside `LEAPVIEW_HOME`; evidence is retained beneath the
instance artifact directory and excluded from subsequent qualification
backups. `LEAPVIEW_RECOVERY_QUALIFICATION_CRON` controls the reviewed schedule.
Startup fails closed if the managed policy is not bound to the running image or
does not contain the exact predecessor identity. Every schedule revision and
occurrence stores the SHA-256 of that exact policy; a different policy with the
same policy version is rejected. The controller must execute the isolated
release qualification environment available to the service account; the
ledger never substitutes synthetic reports when it is unavailable.

For `LEAPVIEW_MANAGED_DATA_BACKEND=s3`, configure both canonical FAI-515 input
files. `LEAPVIEW_RECOVERY_QUALIFICATION_EXTERNAL_RECOVERY_POINTS` is an absolute
path to a JSON array such as:

```json
[{"role":"managed-data","recoveryPoint":"version-42","evidenceKey":"managed-data-version"}]
```

`LEAPVIEW_RECOVERY_QUALIFICATION_EXTERNAL_EVIDENCE` is an absolute path to the
operator evidence map used by both restore preflight and restore:

```json
{"managed-data-version":"version-42"}
```

The resulting owner manifest retains the canonical provider, endpoint, region,
bucket, prefix, recovery point, and evidence key. Both files must be regular,
non-symlink JSON files; startup and each scheduled run fail closed if they are
missing or invalid.

Backup and restore adapters call the platform backup, preflight, and restore
owners against an isolated restore target. Upgrade and rollback adapters call
the installed-candidate transition owner with the immutable predecessor and
candidate bundle. Recovery points are read from those validated owner reports;
restore and readiness durations are derived only from ledger-owned persisted
start and completion phases. Time spent before or after those phases affects
only the separately reported end-to-end qualification duration. A reclaimed
occurrence uses a new fenced run-directory generation and removes only its own
superseded, no-longer-leased generations.

Persisted failures use bounded machine codes and credential-scrubbed summaries.
Full owner errors belong only in restricted transient logs. URL credentials,
DSNs, JSON secrets, signed URL parameters, provider credentials, and multiline
error bodies must never be copied into the ledger.

See [Operational troubleshooting](/docs/guides/operate/troubleshooting), [Audit events](/docs/security/audit), and the [environment reference](/docs/configuration).
