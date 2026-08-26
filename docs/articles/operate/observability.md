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

## Structured logs

Collect structured application logs from the service output. Preserve timestamp, severity, operation, route, status, duration, principal where safe, project, environment, request/correlation ID, deployment ID, revision digest, and refresh generation when available.

Secrets, bearer tokens, passwords, raw OAuth payloads, and sensitive query data must not appear in logs. Restrict log access according to the most sensitive metadata retained.

Carry a request identifier through the trusted reverse proxy and application. Proxy access logs and application logs should agree on public request time, status, and correlation identity.

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

The JSON response reports due, overdue, running, failed, and evidence
publication counts; recovered leases; last-success age; recovery-point age;
and the latest restore/readiness durations for the fixed operation set. The
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

See [Operational troubleshooting](/docs/guides/operate/troubleshooting), [Audit events](/docs/security/audit), and the [environment reference](/docs/configuration).
