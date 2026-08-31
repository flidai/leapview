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

The repository-owned rule file at `deploy/observability/prometheus/leapview-alerts.yaml` provides portable alerts for an unavailable scrape target, sustained application 5xx responses, fatal process-owned DuckDB health, recovery qualification freshness, and dashboard refresh error-budget burn. The rules assume that Prometheus assigns LeapView targets the stable scrape label `job="leapview"`; `instance` must identify the bounded scrape target. Alert identity is limited to those scrape labels plus the static `service` and `severity` labels. Operation, publication state, occurrence, schedule, artifact, target, request or trace identities, principals, projects, and resource identifiers are aggregated away and never become alert labels.

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

Every alert carries a `runbook_url` for its canonical response procedure. The baseline alerts link directly to [target-unavailable response](/docs/guides/operate/troubleshooting#target-unavailable-alert-fires), [sustained HTTP 5xx response](/docs/guides/operate/troubleshooting#sustained-http-5xx-alert-fires), and [DuckDB fatal-health response](/docs/guides/operate/troubleshooting#duckdb-fatal-health-alert-fires). Recovery qualification and dashboard refresh burn alerts likewise deep-link to their alert-specific procedures. These links define evidence collection, safe mitigation, and objective recovery checks; they do not perform remediation. Configure routing and receivers in the operator-owned Alertmanager deployment.

### Dashboard refresh reliability SLI and SLO

The repository-owned recording rules at `deploy/observability/prometheus/leapview-recording-rules.yaml` derive dashboard refresh reliability only from `leapview_dashboard_refresh_duration_seconds_count`. A finished refresh is eligible unless its outcome is `canceled`. `complete` is good; `partial`, `error`, and the bounded fallback `other` are bad. Cancellations are excluded because refresh supersession and stream closure are expected lifecycle behavior rather than evidence of failed dashboard work.

`leapview:dashboard_refresh_reliability:ratio_5m` records the five-minute good-to-eligible ratio. `leapview:dashboard_refresh_reliability:ratio_30d` records the rolling 30-day ratio. Both aggregate command and outcome dimensions to the stable `job` and `instance` scrape labels. Validate the rules, counter-reset behavior, empty-traffic handling, aggregation, and label contract with the existing pinned toolchain:

```sh
task observability:sli:check
```

Ten companion recordings make the ratio's underlying traffic volume explicit. The `eligible_events` recordings cover 5-minute, 30-minute, 1-hour, 6-hour, and 30-day windows and include every outcome except `canceled`; their corresponding `bad_events` recordings include `partial`, `error`, and `other`. They use `increase` over the source histogram counter, aggregate to the same `job` and `instance` labels, and handle counter resets. For a nonzero eligible volume, the reliability ratio is equivalent to one minus bad events divided by eligible events over the same window.

Prometheus must retain at least 30 days of source samples for a complete rolling window. Until that history has accumulated after initial deployment or retention loss, the 30-day ratio and event volumes reflect only the available portion of the window. `increase` may return fractional estimates at range boundaries, so event volumes are operational context rather than accounting totals. Canceled-only traffic records zero eligible and bad events while still emitting no reliability ratio. Completely missing source metrics emit neither volume nor ratio; missing or idle traffic is never reported as perfect reliability. Sparse traffic can make the five-minute ratio volatile and the 30-day ratio statistically weak, so operators should inspect the eligible event recording before interpreting either series.

The repository-owned dashboard refresh reliability objective is 99% over a rolling 30-day window. The LeapView engineering/operations owner owns this initial target. It is a realistic baseline for detecting meaningful degradation while production behavior is still being established, without requiring an immature system to meet a stricter objective before sufficient operational history exists. Revisit the target after production reliability data establishes normal dashboard refresh behavior.

`leapview:dashboard_refresh_reliability:objective_ratio_30d` records the `0.99` objective wherever the 30-day reliability ratio is evaluable. `leapview:dashboard_refresh_reliability:error_budget_consumption_30d` divides the observed bad-event fraction by the approved 1% bad-event allowance. A value below `1` is within budget, `1` is exhausted, and a value above `1` is overdrawn. `leapview:dashboard_refresh_reliability:error_budget_remaining_30d` records one minus consumption, so a negative value remains visible after exhaustion rather than being clamped. All three recordings retain only `job` and `instance`.

The objective and budget recordings are absent when eligible traffic is zero or missing, including canceled-only traffic; absence must not be interpreted as compliance. Low traffic remains mathematically valid but statistically weak, so evaluate budget state together with `eligible_events_30d`. Prometheus cannot distinguish a complete 30-day history from a partial window after deployment or retention loss, and the budget recordings naturally reflect only the samples available. They inherit the SLI's `increase` estimation behavior and are not accounting totals.

The 5-minute, 30-minute, 1-hour, and 6-hour `burn_rate` recordings divide each window's bad-event fraction by the approved 1% error-budget allowance. A value of `1` spends budget at exactly the sustainable rate; `6` and `14.4` spend it six and 14.4 times faster respectively. A burn-rate recording is absent when its eligible volume is zero or missing, including canceled-only traffic.

The critical `LeapViewDashboardRefreshFastBurn` alert fires only when its five-minute and one-hour burn-rate recordings are both at least `14.4` for the same `job` and `instance` and the one-hour window contains at least 10 eligible refreshes. The dual windows reject a short spike that is not also significant over one hour; the volume floor prevents very sparse one-hour traffic from paging. There is intentionally no separate five-minute floor or additional hold duration. A firing alert means the current failure rate is rapidly consuming the existing 99% SLO budget; follow the linked [dashboard refresh response](/docs/guides/operate/troubleshooting#dashboard-refresh-fast-burn-alert-fires). Alertmanager routing remains operator-owned.

The warning `LeapViewDashboardRefreshSlowBurn` alert fires only when the 30-minute and 6-hour burn rates are both at least `6` for the same `job` and `instance` and the six-hour window contains at least 60 eligible refreshes. It detects sustained moderate degradation that can exhaust the 30-day budget without meeting the critical fast-burn threshold. The dual windows provide persistence filtering, so there is no additional hold duration. Missing, canceled-only, or lower-volume traffic cannot fire the alert. Follow the linked [slow-burn response](/docs/guides/operate/troubleshooting#dashboard-refresh-slow-burn-alert-fires).

The warning and critical alerts are independent and can both fire during severe degradation; operator-owned Alertmanager routing may inhibit the warning when the critical alert is active. This SLO measures only completed dashboard refresh work. It does not measure refreshes still in flight, HTTP or SSE transport continuity, authentication, static assets, direct API queries, or an end-to-end synthetic journey. The repository defines no additional burn tier, long-window budget alert, paging receiver, or Alertmanager routing. Services below either alert's volume floor require direct inspection of the 30-day budget recordings. Because Prometheus evaluates the recording and alert groups independently, firing and recovery may trail source conditions by one rule-evaluation interval.

### Dashboard refresh latency SLI

The dashboard refresh latency SLI measures only refreshes whose normalized outcome is `complete`. A completed refresh is timely when its recorded end-to-end duration is less than or equal to five seconds, using the cumulative `le="5"` bucket from `leapview_dashboard_refresh_duration_seconds`. The inclusive five-second boundary matches an existing histogram bucket and the current dashboard qualification expectations. Partial, error, other, canceled, and in-flight refreshes are excluded rather than classified as slow; the outcome reliability SLI above remains the canonical measure for those terminal failures.

`leapview:dashboard_refresh_latency:timely_completed_ratio_5m` records the five-minute timely-to-completed ratio. `leapview:dashboard_refresh_latency:timely_completed_ratio_30d` records the rolling 30-day ratio. Both use Prometheus counter-reset-aware rates and aggregate the histogram's command, outcome, bucket, and any other source dimensions to `job` and `instance`. They emit no series when completed-refresh traffic is zero or missing, so absence must not be interpreted as timely behavior.

This latency SLI does not define a latency SLO, error budget, percentile, or alert. Its five-minute ratio can be volatile at low volume, and its 30-day ratio is statistically weak when few completed refreshes occur. Prometheus must retain 30 days of source histogram samples for a complete rolling window; after deployment or retention loss, the long-window ratio naturally reflects only available history. The metric covers server-side refresh coordination through terminal target completion, not browser rendering or an end-to-end synthetic journey, and bucket classification cannot expose degradations that remain on the same side of the five-second boundary.

### Query-result cache cutover rollout gates

Serving-generation cutover reuses a stable partition result cache while keeping
execution flights generation-owned. Qualify a cache rollout with deterministic
correctness and race gates before interpreting production metrics:

```sh
task test:cache:rollout
```

The command requires 100 consecutive passes of the cutover correctness and
metrics checks and 20 consecutive passes of the focused race checks. The
selected qualification checks have no conditional skip paths. Any failure,
race report, stale-store acceptance, sentinel leak, unexpected physical
execution, or Arrow ownership imbalance fails the rollout. The qualification
remains opt-in and focused; it does not expand the ordinary pull-request test
matrix.

The hard correctness and memory gates are:

| Gate | Required threshold |
| --- | --- |
| Compatible reuse | 100/100 generation A-to-B-to-C qualifications pass; generation B and reactivated generation C perform zero physical queries for the warmed key |
| Identity isolation | 100/100 dependency, policy, governed-query, production/candidate, and candidate-ID mutations miss their incompatible entries |
| Flight ownership | 100/100 same-generation callers coalesce to one owner per key; 100/100 cross-generation cold callers execute one owner per generation and key |
| Invalidation | 100/100 stores carrying a pre-invalidation token are rejected as stale |
| Memory | At every qualification checkpoint, entries and retained bytes are at or below the configured runtime and node limits |
| Arrow ownership | Stable cache-owned Arrow holds equal stable retained entries; consumer leases remain readable after eviction/invalidation and all cache-owned holds release at pool shutdown |
| Race stability | 20/20 focused race runs complete with no race report, deadlock, leaked flight, or lifecycle assertion failure |

Capture latency evidence on the same otherwise-idle host, Go toolchain, CPU
count, and power profile for the merge-base baseline and rollout candidate:

```sh
task bench:cache:cutover:full
```

The full task records ten 500 ms samples at one logical CPU for the public
resultcache lifecycle and governed materialize runtime paths. The quick task is
only a smoke check:

```sh
task bench:cache:cutover:quick
```

The first Phase 2 full run establishes and archives the bootstrap baseline,
because the benchmark harness does not exist on the pre-Phase 2 merge base.
After that baseline is established, every future cache rollout compares its
candidate output with the archived baseline or a fresh full run from its merge
base.

Compare the ordinary Go benchmark output with `benchstat`. A candidate fails
when a statistically significant latency increase exceeds the threshold below.
An increase above the threshold without significance must be rerun on the same
host; it is not treated as a pass until the comparison is conclusive or an
explicit rollout exception records the evidence and owner.

| Benchmark lane | Maximum candidate regression |
| --- | ---: |
| Warm shared-generation hit | 10% |
| Warm cutover-retained hit | 10% |
| Dormant scope open, retained lookup, and close | 15% |
| Overlapping generation open, lookup, and close | 10% |
| Governed cold plan, execute, and store | 15% |
| Consumer Arrow lease acquisition after cache invalidation | 10% |

During a controlled canary cutover, retain at least 30 minutes of samples and
verify all of the following:

- `leapview_query_result_cache_entries` remains equal to
  `leapview_query_result_cache_arrow_holds`.
- `leapview_query_result_cache_bytes` never exceeds the configured node byte
  limit, and retained entries never exceed the configured node entry limit.
- `leapview_query_result_cache_scopes{state="dormant"}` becomes nonzero during
  the zero-reference interval, then
  `leapview_query_result_cache_scope_transitions_total{transition="reactivated"}`
  increases when the compatible generation opens.
- `leapview_dashboard_query_cache_hits_total{source="cutover_retained"}`
  increases after the warmed canary query is served by the reactivated
  generation.
- `leapview_cache_invalidations_total{cache="stable_result"}` does not increase
  during cutover unless the operator intentionally invalidates the partition.
- The absolute 30-minute stable-result eviction delta is zero:
  `sum(increase(leapview_cache_evicted_entries_total{cache="stable_result"}[30m])) == 0`.
  Any positive delta indicates memory pressure or unexpected churn and fails
  the canary; investigate before rollout rather than diluting the signal with
  generation-byte store volume.

These are rollout gates, not new cache semantics or alerting rules. Persistent
production alerts and any threshold adjustment require separately reviewed
operational evidence.

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
