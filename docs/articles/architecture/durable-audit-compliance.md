# Durable audit and compliance controls

LeapView has two related evidence families: administrative audit events record
security-sensitive mutations and access decisions, while query events record
governed analytical activity. Both carry an actor, project/resource identity,
request or correlation identity, outcome, and safe metadata. Query events are
not a replacement for administrative audit events, and application logs are not
the evidence authority.

## Guarantee classes

The guarantee belongs to the producer, not to the transport that happens to
invoke it:

| Guarantee | Meaning when the operation succeeds | Failure behavior |
| --- | --- | --- |
| `transactional` | The state transition and audit row commit in one durable transaction. | The transition fails if the row cannot be committed. |
| `durable-before-stream` | Access identity is persisted before a governed stream releases bytes. | The stream is rejected before release when the identity cannot be persisted. |
| `best-effort` | The operation may complete before its audit append. | The append is bounded/retried, failure is structured and observable, and the completed operation is preserved. |
| `mixed` | A subsystem owns operation families with different explicit guarantees. | Each operation’s generated contract decides; `mixed` never hides an unclassified operation. |

The current producer-by-producer contract is maintained in the machine-readable
`adr/specifications/durable-audit-inventory.json` and governed by ADR-0015 in
the repository decision log. The inventory is an implementation map, not a
promise that every existing best-effort producer has already been upgraded.

## Durable mutation handoff

Producers that own a SQLite mutation can record a canonical audit intent in the
same transaction. The Access-owned outbox assigns a stable event identity and
payload digest, enforces per-aggregate ordering, and rejects an idempotency-key
reuse with different content. A successful commit therefore leaves either both
the domain transition and its audit intent, or neither.

The source write also fails closed when the instance reaches the bounded
undelivered-intent capacity. Delivered handoff rows do not consume that bound
and are removed by the audit retention window; lowering backlog pressure never
permits pending or terminal evidence to be discarded.

The Access dispatcher leases pending intents with a generation fence and
materializes the final `audit_events` row in the same transaction that marks the
intent delivered. Delivery is physically at-least-once but logically exactly
once by event ID. An expired lease is reclaimable after a crash; a stale worker
cannot complete, retry, or terminate it. Transient failures use bounded
exponential backoff. Exhausted attempts become `poison`, while an integrity or
payload conflict becomes `quarantined`; neither state is silently skipped.

System actions may have no principal or capability. If a named principal is
deleted before delivery, the final event follows the audit table's nullable
foreign-key policy while the retained outbox row preserves the original actor
identity until its delivered-row retention window expires.

## Evidence boundaries

Administrative mutations, identity/session changes, grants, policies, and
credential lifecycle records should use the access audit store. A mutation that
needs accountability uses the transactional access repository; an append error
must not be silently converted into success.

Governed data access has a stricter streaming boundary. Publication, candidate,
and view-as streams persist a started access identity before their Arrow sink can
write a schema or rows. A completion event enriches the timeline after
execution, but cannot make an already-delivered stream transactional. Ordinary
query events remain a separate, redacted activity stream and are currently
best-effort.

Generated API command contracts declare the audit action and guarantee in
TypeSpec. Their adapters must preserve request and correlation IDs, use stable
resource identities, and emit a structured failure when a best-effort callback
cannot append. A new callback is incomplete until it has an inventory row and a
focused failure test.

## Compliance controls

- **Integrity:** events are append-only and retain stable IDs, actor/resource
  identity, timestamps, outcome, and correlation data. Exporters preserve IDs
  and provide reconciliation evidence rather than treating a file copy as
  proof of completeness.
- **Confidentiality:** audit metadata is canonical and redacted. Never record
  passwords, bearer tokens, raw OIDC tokens, provider credential values, or
  unrestricted query text. Query diagnostics are bounded to the safe shape
  defined by the query-audit contract.
- **Retention:** audit, query, authentication-state, and conversation history
  have separate policy windows. A dry-run must show the intended deletion and
  a zero value disables pruning for that category; retention policy alone does
  not satisfy an external archive requirement.
- **Access:** audit readers receive their own least-privilege capability and
  project scope. Investigators correlate audit and query events with identity
  provider, proxy, deployment, and secret-manager records without broadening
  ordinary data access.
- **Recovery:** an external archive or SIEM is reconciled from durable local
  event IDs and digests. Delivery failure does not weaken the local mutation
  guarantee, and replay does not invent a second business operation.
- **Operations:** readiness fails on poison/quarantined intents or a sustained
  excessive backlog. Prometheus exports state counts, oldest-undelivered age,
  and scrape failure without event labels. Exact terminal events can be
  requeued through the lock-protected offline command, which records its own
  recovery audit event.

## Review checklist

When adding a security-relevant producer, reviewers should be able to answer:

1. Which exact operation and resource identity does it record?
2. Is the event transactional, required before a stream, best-effort, or a
   mixed family with operation-level declarations?
3. What happens when the audit store is unavailable after the business action?
4. Does the payload pass canonical JSON and secret-redaction checks?
5. Which retention, export, access, and recovery controls cover the event?
6. Is the producer present in the inventory and covered by the no-unclassified
   best-effort test?

For existing event details and incident use, see [Audit events](/docs/security/audit).
