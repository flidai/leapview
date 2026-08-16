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

See [Operational troubleshooting](/docs/guides/operate/troubleshooting), [Audit events](/docs/security/audit), and the [environment reference](/docs/configuration).
