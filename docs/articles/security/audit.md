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
- maintenance and other supported operational actions;
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

The removed offline audit-outbox operation is not available. Use the protected
metrics endpoint and application logs to monitor delivery handoff health,
including pending/retrying work, terminal failures, oldest undelivered age, and
capacity. Keep event IDs, bounded failure codes, and payload digests in
restricted incident evidence only; payload and actor metadata must not be
exported into metrics.

Treat terminal failures, capacity exhaustion, or an unavailable sink as an
outage boundary. Stop writers, preserve readiness, metrics, logs, and the
PostgreSQL-native recovery evidence, then correct the source or sink through its
owner workflow. Do not edit audit rows, increase retries by hand, or replay a
delivery without a reviewed, durable procedure. A PostgreSQL backup/PITR and
DuckLake/object-store recovery runbook remains a separate native operator
responsibility.

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
