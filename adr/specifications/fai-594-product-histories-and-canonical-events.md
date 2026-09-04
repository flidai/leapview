# FAI-594 product histories and canonical events

Status: accepted mutable companion specification

Date: 2026-08-31

Governing decision: [ADR-0020](../0020-adopt-a-postgresql-centered-target-data-architecture.md)

## Purpose and authority

This specification records which rows are product history and which are
mutable operational state. Capability schemas remain authoritative for writes,
authorization, identity, ordering, cursors, holds, and retention.

`event.event_log` is immutable mutation evidence. A source mutation and its
event commit in the same caller-owned PostgreSQL transaction. No asynchronous
consumer is currently admitted, so consumer registration, delivery claims,
retry/dead-letter/replay state, broker envelopes, and transport offsets are not
part of the target.

A capability may retain a domain projection linked to a canonical event ID,
but that projection remains authoritative for its own API and business
semantics. No capability may dual-write equivalent history and later choose
whichever copy is convenient.

## Authority, identity, ACL, order, and cursor rules

- History owners control their schema and ACLs; transport state never grants
  access.
- Product IDs and UUIDv7 event IDs are stable domain identities.
- Ordering is explicit and local to an aggregate, revision, resource sequence,
  or documented keyset. Database commit order does not create global business
  order.
- Public API and SSE cursors contain only domain keys and timestamps.
- Owner projections remain synchronous with their mutation unless a future
  named, bounded, idempotent asynchronous effect is separately admitted.

## Retained product-history inventory

Where no bounded policy exists, history is retained indefinitely until the
owner admits a floor, legal-hold behavior, bounded maintenance, and recovery
evidence.

| Product history | Domain authority and evidence | Identity, order, cursor, and ACL | Retention position |
| --- | --- | --- | --- |
| **Jobs and workflow progress** | `jobs.job`, `jobs.attempt`, `jobs.event_sequence`, and append-only `jobs.event` under the Jobs capability ([schema](../../internal/platform/jobs/postgres/schema.sql)). | Job/resource IDs and per-resource `event_id` are the product identity; event sequence is per resource. The capability route authorizes the resource before history reads. | `jobs.event` is retained indefinitely and has no authorized prune until follow-up work defines its floor, holds, and maintenance evidence. Job-claim lease expiry or River/worker behavior is not a deletion rule. |
| **Agent conversations and runs** | `agent.conversations`, `agent.runs`, `agent.messages`, append-only `agent.events`, and retention floors ([schema](../../internal/agent/postgres/schema.sql)). | Conversation/run/message IDs and run aggregate/stream sequence are domain order. The conversation principal and applicable service/admin grants own ACL evaluation; API cursors use conversation/run sequence or domain event ID. | Existing agent retention controls remain capability-owned. They do not authorize deleting an active run or treating a lease as history. |
| **Deployment and delivery evidence** | Delivery/deployment plans, candidates, build attempts, seals, generations, publications, approvals, leases, GC evidence, and immutable event evidence in `delivery.*` and the deployment repositories ([schema](../../internal/deployment/postgres/schema.sql), [delivery conformance](project-delivery-conformance.md)). | Operation/plan/candidate/attempt/generation/publication IDs and target revisions are domain identity; order is scoped to a target/aggregate. Project/target/environment ACLs and approval policy apply. | Evidence is retained indefinitely and has no authorized metadata prune until a reviewed floor/hold policy is admitted. Artifact or lease TTL is not a deletion rule. |
| **Release history** | `release.release_record`, immutable candidate provenance, deployment linkage, and release connections ([schema](../../internal/release/postgres/schema.sql)). | Release/candidate/deployment IDs and project/environment identity are stable; status transitions and provenance are release order. Release/project ACLs apply. | Release evidence is retained indefinitely and has no authorized prune until the release capability admits an immutable-evidence policy. |
| **Refresh history** | Refresh schedule revisions, operations, runs, attempts, occurrences, publication links, data versions, and recovery state in `refresh.*` ([schema](../../internal/refresh/postgres/schema.sql)). | Operation/run/attempt/occurrence IDs and schedule/nominal-time keys are domain identity; ordering is per operation/pipeline/occurrence, not a broker offset. Project/environment/principal ACLs apply. | History is retained indefinitely and has no authorized prune until policy is established for operations, runs, attempts, occurrences, and recovery evidence separately from mutable schedules and leases. |
| **Managed-data history** | Collections, immutable revisions/files, upload and multipart evidence, environment pointers, binding sets/bindings, leases, retention roots, and reconciliation evidence in `managed_data.*` ([schema](../../internal/manageddata/postgres/schema.sql)). | Collection/revision/upload/session/binding IDs and generation/revision keys are domain identity. Project/environment and data-policy ACLs apply; cursors use revision or domain IDs. | Immutable revisions, bindings, reconciliation evidence, and roots are retained indefinitely and have no authorized prune until a floor is admitted. Terminal upload protocol rows retain their existing bounded cleanup policy. |
| **Dashboard authoring and publication history** | Authoring dashboards/revisions/drafts/compiled revisions/published rows/commands and revalidation evidence, plus `dashboard.publications` and append-only `dashboard.publication_events` ([authoring schema](../../internal/dashboard/authoring/postgres/schema.sql), [publication schema](../../internal/dashboard/publication/postgres/schema.sql)). | Dashboard/revision/draft/command/publication/event IDs and aggregate revisions are domain identity/order. Project/dashboard privileges and the anonymous publication’s configured access policy apply. | Publication and authoring history are retained indefinitely and have no authorized history prune. `dashboard.publication_streams` is mutable registration/CAS state, not history; its relay-table removal is limited to FAI-596 below. |
| **Dashboard appearance, usage, and sessions** | `dashboard.appearance_override` is the current revisioned override, `dashboard.view_day` is a daily aggregate, and `dashboard.view_session` is mutable session state ([appearance](../../internal/dashboard/appearance/postgres/schema.sql), [usage](../../internal/dashboard/usage/postgres/schema.sql), [session](../../internal/dashboard/session/postgres/schema.sql)). | Project/dashboard/principal/day or session ID is the domain key. Dashboard/project authorization applies; none of these keys is a transport cursor. | Appearance keeps current state rather than immutable history. Usage has the capability’s 90-day bounded window. Sessions expire by their TTL. Any future immutable appearance audit belongs to the authoring/audit mutation contract. |
| **Access audit history** | Immutable `audit.audit_event` written by Access and source capability transactions; audit retention floors/prune functions remain audit-owned ([schema](../../internal/access/postgres/schema.sql), [inventory](durable-audit-inventory.json)). | Audit ID, linked canonical domain event ID, aggregate key/sequence, and database occurrence time are evidence identity/order. Reads are restricted to audit/compliance roles and the owning scope. | Existing audit maintenance policy is authoritative; no separate handoff queue or second audit history is introduced here. |
| **Query audit history** | Append-only `audit.query_event` and its retention floor/prune function ([schema](../../internal/analytics/queryaudit/postgres/schema.sql)). | Query event ID and retry identity are domain evidence; keyset order is created time plus event ID. Project, principal, and audit ACLs apply; SQL/plan payloads remain subject to redaction. | Existing query-audit maintenance policy remains owner-controlled. Queue leases and query-runtime caches are not query history. |
| **Lineage history** | Immutable compiler-owned graphs, revisions, nodes, edges, and serving bindings in `lineage.*` ([schema](../../internal/lineage/postgres/schema.sql)). The compiler graph remains semantic authority; PostgreSQL is its admitted queryable projection. | Graph digest, project/scope, revision, and resource IDs are identity/order; project and serving-state ACLs apply. Traversal cursors use graph/resource keys, never transport offsets. | Graphs are retained indefinitely and have no authorized prune until a floor/hold policy is admitted. No graph may be pruned based on transport state. |
| **Serving-state history** | Immutable serving bundles/assets/edges and delivery generation/seal/publication evidence in `serving_state.*` and `delivery.*` ([schema](../../internal/servingstate/postgres/schema.sql)). | Generation, artifact, seal, snapshot, and target IDs are domain identity; target revision/generation order governs activation. Target/project ACLs apply. | Metadata is retained indefinitely; retention roots, rollback windows, and reader leases govern physical reachability. A reader lease or active pointer is mutable operational state and cannot rewrite immutable generation history. |
| **Project source history** | Project identity, source blobs/snapshots/entries, attestations, and source-sync evidence in `project.*` ([schema](../../internal/project/postgres/schema.sql)). The authored/compiler project graph remains canonical. | Project/source snapshot/blob/attestation IDs and source digest/revision are identity/order; project and source-owner ACLs apply. | Snapshots and attestations are retained indefinitely and have no authorized prune until deletion, legal-hold, and export semantics are admitted. |
| **Operation and idempotency history** | `platform.operation` and capability operation records (including `refresh.operation`) retain the idempotency key, request digest, operation ID, attempt identity, terminal outcome, and reconciliation evidence ([schema](../../internal/platform/operation/postgres/schema.sql)). | Scope plus idempotency key and operation ID are stable domain identity; attempt identity is evidence, not public transport identity. Scope/principal/operation ACLs apply; retries address the same operation. | Pending/indeterminate evidence is retained until safely reconciled. Terminal expiry is owner-controlled and bounded by the operation’s declared retention interval; lease owner, fencing generation, and `expires_at` are mutable/operational fields, not history replacement. |

## Mutable and TTL operational state

Operational state may change or expire without deleting product history:
current job claims, refresh schedules and recovery leases, managed-data upload
sessions and environment pointers, dashboard stream registrations, access
sessions and credentials, serving active pointers, reader leases, and
operation ownership fences. A TTL is not permission to delete immutable
evidence, and an in-memory notification is not a recovery checkpoint.

## Event record versus delivery

The canonical event append is part of the owner transaction and is independent
of any consumer. Pagestream is the browser SSE transport; it is not a
replayable product history. A future consumer must define its idempotent effect,
ordering, recovery, retention, and authorization before a delivery mechanism
is selected.

Activation, publication, and deployment commands must reconcile a lost
response against their product operation ID, target revision, active
generation, publication projection, and canonical evidence. No notification
or broker acknowledgement decides whether the commit occurred.

## Confirmation evidence

Conformance requires:

- canonical event UUIDv7, aggregate ordering, caller-owned transaction,
  idempotent replay, bounded JSON payload, immutability, and retention-role
  tests;
- owner schema, ACL, immutability, and retention tests for the inventory above;
- API and SSE contract tests exposing only domain identities and keyset
  cursors; and
- activation replay tests proving a committed lost response does not cause a
  second activation.
