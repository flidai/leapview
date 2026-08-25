# ADR-0015: Adopt durable audit and compliance controls

Status: accepted

Decision date: 2026-08-23

Implementation: durable foundation and prioritized producer adoption

Deciders: LeapView platform and security maintainers

Supersedes: none

Related: [durable audit producer inventory](specifications/durable-audit-inventory.json), [audit events](../docs/articles/security/audit.md)

## Context and problem statement

LeapView currently has several security-relevant audit producers. Access
mutations can append an event in the same SQLite transaction as the mutation,
while generated API commands, query activity, credential rotation, and some
cross-store lifecycle events intentionally use a best-effort callback. The
existing distinction is useful, but it is distributed across TypeSpec contracts,
module adapters, and comments. A reviewer cannot reliably answer which events
are durable, which failure can reject a request, or which evidence is safe to
export without first tracing implementation details.

Compliance and incident response require an explicit evidence contract. In
particular, a successful security-sensitive mutation must not silently become
unaccountable, a stream must not expose governed data before its access identity
is durable, and event retention or export must not turn secrets into evidence.
The contract must also make a newly introduced best-effort producer visible in
review instead of allowing an unclassified callback to become an accidental
guarantee.

## Decision drivers

- Make durability and failure behavior reviewable at the producer boundary.
- Preserve fail-closed authorization and pre-execution evidence for governed
  data streams.
- Keep audit payloads append-only, redacted, access-controlled, and exportable
  without leaking credentials or query secrets.
- Permit non-critical observability to remain best-effort while making that
  trade-off explicit and test-enforced.
- Avoid coupling the control-plane transaction to an external archive or
  notification system.

## Considered options

- **Leave guarantees implicit in each module.** This has low short-term cost,
  but keeps coverage incomplete and makes a new callback easy to miss.
- **Make every audit write transactional with the business mutation.** This
  improves local accountability, but cannot span DuckDB/streaming and external
  sinks without introducing a larger distributed transaction and new failure
  modes.
- **Adopt a classified producer contract with durable local evidence and a
  separately managed export path.** This makes the boundary explicit while
  allowing bounded best-effort telemetry where rejecting a completed operation
  would be incorrect.

## Decision outcome

Adopt the classified producer contract. Every security-relevant producer must
declare one of the guarantees in
[`durable-audit-inventory.json`](specifications/durable-audit-inventory.json):

- **`transactional`** — the event and the state transition commit in one
  durable transaction; a write failure aborts the transition.
- **`durable-before-stream`** — the access identity is durably recorded before
  bytes can be released to a governed stream. A completion record may be
  best-effort, but its failure is observable and cannot rewrite already-sent
  bytes.
- **`best-effort`** — the producer attempts an append after the successful
  operation, retries only within its bounded local policy, logs a structured
  failure, and does not roll back a completed operation.
- **`mixed`** — a producer owns more than one operation family and lists the
  exact transactional and best-effort members in its inventory. `mixed` is not
  permission to omit an operation-level classification.

The local audit store remains the evidence authority for the running instance.
Events are append-only, carry a stable actor/resource/request identity, and
contain canonical redacted metadata. Query activity is a separate event family
from administrative changes, but uses the same actor, project, correlation, and
retention controls. External SIEM/archive delivery is an export or outbox
concern; it is not allowed to weaken the local mutation guarantee.

Retention, export, and access controls are policy inputs rather than producer
shortcuts. Retention jobs must report what was removed, exports must preserve
event IDs and digests needed for reconciliation, and operators must be able to
prove the active policy and the time range used for an investigation. No
producer may place passwords, bearer tokens, raw OIDC tokens, provider values,
or unredacted query text in an audit payload.

Adding a best-effort callback requires, in the same change:

1. a row in the machine-readable inventory;
2. an operation-level TypeSpec guarantee (when the surface is generated);
3. a structured failure log and an explicit statement that the completed
   operation is preserved; and
4. focused failure and redaction evidence.

## Consequences

The inventory and static test add a small maintenance obligation, but they make
coverage gaps fail during review rather than during an incident. Producers are
upgraded only when their source transaction records the durable intent and its
focused rollback/idempotency evidence passes; remaining external-boundary or
observational producers stay explicitly best-effort.

The contract deliberately distinguishes durable pre-stream evidence from a
post-query completion event. It therefore preserves the correct behavior for
streaming transports without pretending that an after-the-fact completion write
can be atomic with bytes already delivered. An external archive still needs
reconciliation, integrity protection, and an independently tested recovery
path.

## Confirmation

- [`durable-audit-inventory.json`](specifications/durable-audit-inventory.json)
  classifies current producers, source contracts, failure behavior, and focused
  evidence.
- `internal/access/auditcontract/audit_inventory_test.go` rejects duplicate or
  incomplete rows and scans production Go callbacks plus TypeSpec
  `best-effort` declarations for an inventory owner.
- `task docs:check` validates the published architecture article and generated
  navigation/LLM index.
- The Access outbox/dispatcher conformance suite covers transaction rollback,
  restart, leasing/fencing, retry, logical exactly-once materialization,
  terminal visibility, capacity, and redaction invariants against real SQLite.
- A producer changes its inventory guarantee only with source-transaction
  adoption plus focused rollback, idempotency, and metadata tests.
