# FAI-594 product histories and canonical asynchronous events

Status: accepted mutable companion specification

Date: 2026-08-31

Governing decision: [ADR-0016](../0016-adopt-a-postgresql-centered-target-data-architecture.md)

Related: [FAI-592 canonical Watermill envelope](watermill-canonical-envelope.md),
[FAI-593 Watermill Router/subscriber runtime](watermill-router-runtime.md), and
FAI-596 (the narrow dashboard publication relay removal)

## Purpose and authority

This specification records which rows are product history, which rows are
canonical asynchronous-event or delivery state, and which rows are merely
mutable operational state. It is a companion to ADR-0016, not a replacement
for capability schemas or a new retention policy. The owning capability remains
the authority for writes, reads, authorization, identity, ordering, cursors,
holds, and retention of its product history.

`event.event_log` is the one durable integration authority for canonical
asynchronous events. A source mutation and its canonical event are committed
in the same caller-owned PostgreSQL transaction. `event.event_delivery`,
consumer enrollment, attempts, leases, fences, terminal status, replay roots,
and retention floors are durable delivery state for that integration authority;
they are not a product history and never become one by being projected into
Watermill. The canonical event is projected deterministically into a Watermill
message only after its delivery is claimable. Watermill is router/handler
orchestration, not an event log, checkpoint authority, or product-history
store. See [FAI-592](watermill-canonical-envelope.md) and
[FAI-593](watermill-router-runtime.md).

No capability may dual-write the same history into `event.event_log`, a
framework-owned SQL table, and a product table and then choose whichever copy
is easiest to read. A capability may retain a domain projection linked to a
canonical event ID, but that projection remains authoritative for its own API
and business semantics.

## Authority, identity, ACL, order, and cursor rules

- A history owner controls its schema and mutation boundary. The owner applies
  the resource/project/principal ACL on reads; an event consumer, Watermill
  topic, broker subscription, or notification never grants access.
- Domain IDs are stable identifiers (for example a job ID, run ID, revision,
  publication ID, operation ID, UUIDv7 canonical event ID, or cache
  invalidation ID). A canonical event may carry the domain aggregate identity
  and a `domain_event_id` link, but the product ID is not replaced by a
  transport ID.
- Ordering is explicit and local: aggregate version, resource event sequence,
  revision, message sequence, occurrence time plus a domain tie-breaker, or a
  capability-defined keyset. PostgreSQL commit order and a global Watermill
  offset do not imply business order across aggregates.
- API and public SSE cursors are built only from domain keys and timestamps
  (and may be signed or encoded). They must never contain or expose a
  Watermill topic, attempt number, broker offset, delivery-row ID, claim
  generation, lease owner, or other transport identity. The same rule applies
  to public IDs. A canonical UUIDv7 `event_id` is valid because it is the
  domain event identity, not because Watermill has a message UUID.
- A product history row may be produced in the same transaction as its
  canonical event, or may be an idempotent consumer projection. In either
  case, `event.event_delivery` completion/acknowledgement proves only
  integration progress; it does not replace the history owner’s commit or
  ACL.

## Retained product-history inventory

The following inventory is intentionally about authority and relationships,
not an invented set of day counts. Existing owner-specific maintenance
settings remain the only active retention values. Where no bounded policy or
prune function exists, the fail-safe policy is indefinite retention and no
deletion until a follow-up admits a floor, legal-hold behavior, bounded
maintenance, and recovery evidence.

| Product history | Domain authority and evidence | Identity, order, cursor, and ACL | Retention position |
| --- | --- | --- | --- |
| **Jobs and workflow progress** | `jobs.job`, `jobs.attempt`, `jobs.event_sequence`, and append-only `jobs.event` under the Jobs capability ([schema](../../internal/platform/jobs/postgres/schema.sql)). | Job/resource IDs and per-resource `event_id` are the product identity; event sequence is per resource. The capability route authorizes the resource before history reads. | `jobs.event` is retained indefinitely and has no authorized prune until follow-up work defines its floor, holds, and maintenance evidence. Job-claim lease expiry or River/worker behavior is not a deletion rule. |
| **Agent conversations and runs** | `agent.conversations`, `agent.runs`, `agent.messages`, append-only `agent.events`, and retention floors ([schema](../../internal/agent/postgres/schema.sql)). | Conversation/run/message IDs and run aggregate/stream sequence are domain order. The conversation principal and applicable service/admin grants own ACL evaluation; API cursors use conversation/run sequence or domain event ID. | Existing agent retention controls remain capability-owned. They do not authorize deleting an active run or treating a lease as history. |
| **Deployment and delivery evidence** | Delivery/deployment plans, candidates, build attempts, seals, generations, publications, approvals, leases, GC evidence, and immutable event evidence in `delivery.*` and the deployment repositories ([schema](../../internal/deployment/postgres/schema.sql), [delivery conformance](project-delivery-conformance.md)). | Operation/plan/candidate/attempt/generation/publication IDs and target revisions are domain identity; order is scoped to a target/aggregate. Project/target/environment ACLs and approval policy apply. | Evidence is retained indefinitely and has no authorized metadata prune until a reviewed floor/hold policy is admitted. Artifact or lease TTL is not a deletion rule. |
| **Release history** | `release.release_record`, immutable candidate provenance, deployment linkage, and release connections ([schema](../../internal/release/postgres/schema.sql)). | Release/candidate/deployment IDs and project/environment identity are stable; status transitions and provenance are release order. Release/project ACLs apply. | Release evidence is retained indefinitely and has no authorized prune until the release capability admits an immutable-evidence policy. |
| **Refresh history** | Refresh schedule revisions, operations, runs, attempts, occurrences, publication links, data versions, and recovery state in `refresh.*` ([schema](../../internal/refresh/postgres/schema.sql)). | Operation/run/attempt/occurrence IDs and schedule/nominal-time keys are domain identity; ordering is per operation/pipeline/occurrence, not a broker offset. Project/environment/principal ACLs apply. | History is retained indefinitely and has no authorized prune until policy is established for operations, runs, attempts, occurrences, and recovery evidence separately from mutable schedules and leases. |
| **Managed-data history** | Collections, immutable revisions/files, upload and multipart evidence, environment pointers, binding sets/bindings, leases, retention roots, and reconciliation evidence in `managed_data.*` ([schema](../../internal/manageddata/postgres/schema.sql)). | Collection/revision/upload/session/binding IDs and generation/revision keys are domain identity. Project/environment and data-policy ACLs apply; cursors use revision or domain IDs. | Immutable revisions, bindings, reconciliation evidence, and roots are retained indefinitely and have no authorized prune until a floor is admitted. Terminal upload protocol rows retain their existing bounded cleanup policy. |
| **Dashboard authoring and publication history** | Authoring dashboards/revisions/drafts/compiled revisions/published rows/commands and revalidation evidence, plus `dashboard.publications` and append-only `dashboard.publication_events` ([authoring schema](../../internal/dashboard/authoring/postgres/schema.sql), [publication schema](../../internal/dashboard/publication/postgres/schema.sql)). | Dashboard/revision/draft/command/publication/event IDs and aggregate revisions are domain identity/order. Project/dashboard privileges and the anonymous publication’s configured access policy apply. | Publication and authoring history are retained indefinitely and have no authorized history prune. `dashboard.publication_streams` is mutable registration/CAS state, not history; its relay-table removal is limited to FAI-596 below. |
| **Dashboard appearance, usage, and sessions** | `dashboard.appearance_override` is the current revisioned override, `dashboard.view_day` is a daily aggregate, and `dashboard.view_session` is mutable session state ([appearance](../../internal/dashboard/appearance/postgres/schema.sql), [usage](../../internal/dashboard/usage/postgres/schema.sql), [session](../../internal/dashboard/session/postgres/schema.sql)). | Project/dashboard/principal/day or session ID is the domain key. Dashboard/project authorization applies; none of these keys is a transport cursor. | Appearance keeps current state rather than immutable history. Usage has the capability’s 90-day bounded window. Sessions expire by their TTL. Any future immutable appearance audit belongs to the authoring/audit mutation contract, not Watermill. |
| **Access audit history** | Immutable `audit.audit_event` written by Access and source capability transactions; audit retention floors/prune functions remain audit-owned ([schema](../../internal/access/postgres/schema.sql), [inventory](durable-audit-inventory.json)). | Audit ID, linked canonical domain event ID, aggregate key/sequence, and database occurrence time are evidence identity/order. Reads are restricted to audit/compliance roles and the owning scope. | Existing audit maintenance policy is authoritative. The mutable audit outbox is a handoff/operational queue, not a second audit history; no new period is introduced here. |
| **Query audit history** | Append-only `audit.query_event` and its retention floor/prune function ([schema](../../internal/analytics/queryaudit/postgres/schema.sql)). | Query event ID and retry identity are domain evidence; keyset order is created time plus event ID. Project, principal, and audit ACLs apply; SQL/plan payloads remain subject to redaction. | Existing query-audit maintenance policy remains owner-controlled. Queue leases and query-runtime caches are not query history. |
| **Cache invalidation history** | Append-only `cache.cache_invalidation` with namespace/dependency evidence; notifications are only wake hints ([schema](../../internal/analytics/cache/postgres/schema.sql)). | Invalidation UUID and durable event ID are identity; namespace/dependency epoch and event ID provide order/cursor. Cache namespace/project security domains apply. | Invalidation pruning and cache-object retirement remain cache-capability decisions. `cache_namespace_epoch`, dependency revisions, fill leases, and manifests are current/operational state or object-retention evidence, not a canonical event transport. |
| **Lineage history** | Immutable compiler-owned graphs, revisions, nodes, edges, and serving bindings in `lineage.*` ([schema](../../internal/lineage/postgres/schema.sql)). The compiler graph remains semantic authority; PostgreSQL is its admitted queryable projection. | Graph digest, project/scope, revision, and resource IDs are identity/order; project and serving-state ACLs apply. Traversal cursors use graph/resource keys, never transport offsets. | Graphs are retained indefinitely and have no authorized prune until a floor/hold policy is admitted. No graph may be pruned merely because no Watermill consumer uses it. |
| **Serving-state history** | Immutable serving bundles/assets/edges and delivery generation/seal/publication evidence in `serving_state.*` and `delivery.*` ([schema](../../internal/servingstate/postgres/schema.sql)). | Generation, artifact, seal, snapshot, and target IDs are domain identity; target revision/generation order governs activation. Target/project ACLs apply. | Metadata is retained indefinitely; retention roots, rollback windows, and reader leases govern physical reachability. A reader lease or active pointer is mutable operational state and cannot rewrite immutable generation history. |
| **Project source history** | Project identity, source blobs/snapshots/entries, attestations, and source-sync evidence in `project.*` ([schema](../../internal/project/postgres/schema.sql)). The authored/compiler project graph remains canonical. | Project/source snapshot/blob/attestation IDs and source digest/revision are identity/order; project and source-owner ACLs apply. | Snapshots and attestations are retained indefinitely and have no authorized prune until deletion, legal-hold, and export semantics are admitted. |
| **Operation and idempotency history** | `platform.operation` and capability operation records (including `refresh.operation`) retain the idempotency key, request digest, operation ID, attempt identity, terminal outcome, and reconciliation evidence ([schema](../../internal/platform/operation/postgres/schema.sql)). | Scope plus idempotency key and operation ID are stable domain identity; attempt identity is evidence, not public transport identity. Scope/principal/operation ACLs apply; retries address the same operation. | Pending/indeterminate evidence is retained until safely reconciled. Terminal expiry is owner-controlled and bounded by the operation’s declared retention interval; lease owner, fencing generation, and `expires_at` are mutable/operational fields, not history replacement. |

## Mutable and TTL operational state

Operational state may change or expire without deleting a product history:

- canonical delivery claims, attempts, leases, fences, consumer lifecycle,
  backfill frontiers, and retention roots;
- job current status and claim lease, refresh schedules/recovery leases,
  managed-data upload/multipart sessions and current environment pointers;
- dashboard publication stream registrations, command filters/generation CAS,
  heartbeat/expiry, local Pagestream subscriptions, and broker relay buffers;
- Access sessions, credentials/tokens, auth/device state, and audit/query
  outbox handoff rows;
- serving active pointers, reader/query leases, cache namespace epochs/fill
  leases, cache manifests while admitted/retiring, and operation owner/fence
  leases.

These rows still have an owning capability, ACL, and state-machine
preconditions. A TTL is not permission to remove immutable evidence, and a
notification or in-memory cache is not a recovery checkpoint. Where an
operational row contains terminal evidence, its owner decides whether that
evidence is itself retained as history.

## Transport versus history

The boundaries below are mandatory:

1. A canonical producer appends one finalized row to `event.event_log` (and
   enrolled delivery rows) in the source transaction. The Watermill adapter
   reconstructs a byte-identical message from that row. Watermill retries,
   `Ack`/`Nack`, topics, offsets, and middleware metrics are transport
   mechanics only.
2. A Watermill handler may make an idempotent domain mutation or append a
   product projection, then complete the durable delivery before `Ack`. It
   must not use a topic, attempt, offset, or delivery identity as the
   projection’s public ID or cursor.
3. Pagestream is the browser SSE transport. Public stream/session IDs are
   product-owned domain IDs, and the stream registry/CAS is the durable
   authority for registration and command state. SSE delivery is not a
   replayable product history. PostgreSQL notifications and cache invalidation
   notifications are wake-up hints that require reconciliation after loss.
4. Capability histories remain readable and authorized when no async consumer
   is running. Conversely, a successful transport acknowledgement does not
   make a missing product-history commit appear.

### Activation and lost acknowledgement

Activation, publication, and deployment commands must be safe when the
response or acknowledgement is lost after the durable commit. A retry or a
successor node must reconcile the same operation/idempotency identity against
the authoritative target revision, active pointer/generation, publication
projection, and canonical event/evidence rows. It returns the already
committed domain result without a second CAS advance, second activation, or
second canonical event. If the durable commit did not occur, the same
operation may be retried under its original identity. No SSE notification,
Watermill acknowledgement, in-memory cursor, or broker offset can decide this
case. This is the required activation lost-ack boundary demonstrated by
[`TestPublishReconcilesCommittedLostResponseWithoutSecondActivation`](../../internal/deployment/sealedcontrol/coordinator_test.go)
and the native dashboard projection reconciler
([activation.go](../../internal/app/dashboardpublication/activation.go)).

## FAI-596 exact deletion boundary

FAI-596 is intentionally narrow. It removes only the legacy SQLite short-lived
relay backed by
`dashboard_publication_stream_events`:

- remove the SQLite relay implementation in
  `internal/dashboard/publication/sqlite/broker.go`,
  including its polling relay and event encoding;
- remove exactly these four generated SQL leaves from
  [`internal/dashboard/publication/sqlite/queries/publication.sql`](../../internal/dashboard/publication/sqlite/queries/publication.sql):
  `InsertDashboardPublicationStreamEvent`,
  `GetLatestDashboardPublicationStreamEventID`,
  `ListDashboardPublicationStreamEventsAfter`, and
  `DeleteExpiredDashboardPublicationStreamEvents`;
- remove the `Reconcile` call that prunes those relay rows in
  [`internal/dashboard/publication/sqlite/streams.go`](../../internal/dashboard/publication/sqlite/streams.go);
- remove the SQLite broker factory assignment
  `newSQLitePublicationBroker` in
  [`internal/dashboard/module/module.go`](../../internal/dashboard/module/module.go)
  and any now-unreachable SQLite relay wiring;
- remove or rewrite only the cross-replica relay test
  [`TestPublicationBrokerRelaysEventsAcrossReplicas`](../../internal/app/dashboard_publications_test.go);
- apply forward Goose
  [migration 095](../../internal/platform/migrations/095_dashboard_publication_stream_relay_removal.sql),
  which drops only `dashboard_publication_stream_events` and its
  `dashboard_publication_stream_events_stream_idx` index without rewriting
  migration 040. Fresh-install, reopen, predecessor-upgrade, and
  migration-chain assertions prove the table/index are gone while retained
  publication history and stream-registration rows survive.

The FAI-596 implementation preserves `dashboard_publication_streams`, its
registration/heartbeat/expiry lifecycle, command-state compare-and-swap,
`StreamRegistry`, and all PostgreSQL publication stream functions and tests.
It must also preserve `dashboard_publication_events`, dashboard authoring and
publication history, the local Pagestream broker, canonical
`event.event_log`/`event_delivery`, and every other product history or
operational state listed above. No other SQLite table, SQL method, migration,
factory, or test is in scope.

`watermill-sql/v4` remains qualification-only. The stock SQL publisher and
subscriber tables/offsets are not production authority. Retiring the optional
qualification dependency may be proposed separately after FAI-591 proof and
FAI-592/593 adapter work; FAI-594 and FAI-596 must not make that retirement a
condition of the narrow relay deletion.

## Confirmation evidence

Conformance is demonstrated by repository inspection and focused tests:

- canonical event identity, append/fan-out, delivery fencing, lost-ack
  recovery, and absence of Watermill SQL authority in
  [`internal/platform/events`](../../internal/platform/events) and the
  [FAI-592/593 specifications](watermill-canonical-envelope.md);
- each owner’s schema/ACL/immutability/retention tests for the inventory above,
  with unresolved retention rows tracked as follow-up work rather than silent
  defaults;
- API/SSE contract tests that expose only domain IDs and domain keyset cursors;
- activation replay tests proving a committed lost response reconciles without
  a second activation;
- FAI-596 focused SQLite tests and migration assertions proving only the relay
  table/index, four query leaves, prune call, factory assignment, and relay
  test were removed while stream registry/CAS and all other authorities remain.
