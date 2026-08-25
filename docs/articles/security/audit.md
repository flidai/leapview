# Audit events

Audit events record security-sensitive and administrative activity with acting principal and project-resource context. Query events separately record governed data operations and their execution context. Use both when an incident crosses authorization and analytical activity.

## What to investigate

Security and administrative audit history is useful for:

- principal, service-principal, group, and membership changes;
- role bindings, grants, ownership, and data-policy changes;
- local-user creation and password reset;
- token or service-principal secret issuance and revocation;
- project deployment and activation;
- target credential refresh, version adoption, degradation, and recovery;
- refresh and managed-data operations;
- backup, restore, maintenance, and storage cleanup;
- agent and API actions that emit audited operations.

Not every request is an administrative audit event. Query events provide filters for principal, surface, operation, kind, semantic model, target, status, text search, and time range.

## Query the API

The generated [Audit API](/docs/api/audit) exposes project-scoped endpoints:

```text
GET /api/v1/projects/{project}/audit-events
GET /api/v1/projects/{project}/query-events
```

Use bounded time ranges and pagination. Filter by actor/action/target for administrative changes or principal/surface/operation/status for queries. Record the request time and page tokens when exporting an investigation set so the collection process can be repeated.

Audit access requires its own privilege. Restrict it to security and operational roles that need the relevant project visibility. Query text and target metadata can reveal sensitive business context even when row data is absent.

## Monitor durable delivery

Inspect the local handoff without exposing event payloads:

```sh
leapview admin audit-outbox
```

The command reports counts for pending, retrying, leased, delivered, poison,
and quarantined intents plus the oldest undelivered age. The metrics endpoint
exports the same aggregate state as `leapview_audit_outbox_intents`,
`leapview_audit_outbox_oldest_undelivered_age_seconds`, and
`leapview_audit_outbox_scrape_error`. Readiness becomes unhealthy when terminal
intents exist or a sustained backlog exceeds the built-in safety bounds.
New audited mutations fail closed before the undelivered handoff reaches its
hard capacity, so treat a growing backlog as an availability incident rather
than waiting for capacity rejection.

The inspection also prints at most 100 terminal rows. Each row contains only
the exact event ID, terminal state, attempt count, bounded failure code,
immutable payload digest, and creation time; metadata and payload fields are
never exported. Record those values before taking recovery action. The
`attempts`, `leases`, `materialized`, `capacity`, and `capacity_remaining`
metrics are aggregate gauges with no event, actor, resource, or request labels.

Investigate the source operation and storage health before recovery. To retry
one reviewed poison or quarantined event, stop the running instance so the
offline lock can be acquired, then use its exact non-secret event identity:

```sh
leapview admin audit-outbox --requeue-event EVENT_ID --apply
```

For a stronger stale-operator guard, copy the terminal facts printed by the
inspection into the recovery command:

```sh
leapview admin audit-outbox \
  --requeue-event EVENT_ID \
  --expected-state poison \
  --attempt-count ATTEMPTS \
  --failure-code AUDIT_SINK_UNAVAILABLE \
  --payload-digest 'sha256:...' \
  --apply
```

The exact recovery uses one SQLite compare-and-swap: the event must still be
poison or quarantined and every supplied state/attempt/code/digest guard must
match. A changed or already recovered row is reported as stale/conflicting;
rerun inspection instead of guessing. Repeating a successful command cannot
create a second recovery event. The recovery resets that intent to retry and
atomically records one direct `audit.outbox.requeued` event (without enqueueing
another outbox intent). It cannot alter event payloads or requeue an active,
pending, retrying, leased, or already delivered intent.

## Outage and recovery verification checklist

Treat any readiness failure mentioning terminal intents, capacity exhaustion, or
an unavailable outbox store as an outage boundary:

1. Capture readiness, image/configuration revision (without secrets), metrics,
   and logs; stop writers before applying recovery.
2. Run `leapview admin audit-outbox` and preserve the bounded terminal rows,
   especially event ID, state, attempt count, failure code, and digest.
3. Confirm the source/database sink issue is understood and take a consistent
   backup. Do not edit `audit_outbox` payload or metadata columns.
4. Apply one exact guarded recovery only for the reviewed terminal event. A
   stale/conflicting result is a stop condition; inspect again.
5. Restart the dispatcher and verify the event reaches `delivered`, terminal
   counts are zero, capacity is not exhausted, and readiness is healthy.
6. Confirm exactly one `audit.outbox.requeued` audit event for the recovery,
   then exercise one representative audited mutation and verify its final
   event. Preserve command output, metrics, backup checksum, and timestamps.

If the sink remains unavailable, leave readiness failed and preserve the
terminal rows. Do not increase retries by hand, delete terminal evidence, or
requeue an event whose identity/digest was not captured from inspection.

## Correlate sources

For authentication incidents, correlate LeapView audit/application logs with identity-provider sign-in events, SCIM provider logs, reverse-proxy request IDs, and secret-manager access history.

For data or deployment incidents, correlate project commit, deployment ID, environment, managed revision digest, refresh generation, active serving state, and query request identity. Preserve timestamps in a consistent timezone.

Credential-rotation records contain binding and target identity, provider version, actor, operation, timestamp, outcome, and a bounded reason code. They do not contain provider values, bearer tokens, passwords, or raw OIDC tokens. If an external log captured a secret, treat the logging system as part of the exposure.

## Retention

Use policy-driven bounded retention. The maintenance command defaults to separate windows for audit, query, auth-state, and archived agent-conversation history and runs as a dry-run unless `--apply` is supplied:

```sh
leapview admin maintenance \
  --audit-days 365 \
  --query-days 90
```

Review the dry-run output, preservation requirements, and external archive before applying deletion. A value of zero disables pruning for that category; it does not automatically satisfy storage or compliance needs.

The audit window also prunes delivered outbox handoff rows after their final
audit events age out. Non-delivered and terminal rows are never removed by
routine retention.

Export or forward events to an approved security system when organizational retention exceeds the operational database window. Protect integrity and access to the export.

## Incident workflow

1. Define actor, resource, action, and time window.
2. Preserve relevant audit, query, application, proxy, and provider records.
3. Build a chronological timeline using stable IDs.
4. Determine effective privileges at the time where possible.
5. Contain credentials or access without deleting evidence.
6. Correct grants, policy, project, or runtime state through normal workflows.
7. Record findings and validate that new detection covers recurrence.

Audit history supports accountability but does not replace least privilege, secure credential handling, or external monitoring.

## Verify audit coverage

Exercise a representative administrative change and a governed query in a non-production project target. Confirm that the audit and query APIs record the expected principal, action, resource, status, and correlation identifiers, and verify that secret values are absent from every emitted record.
