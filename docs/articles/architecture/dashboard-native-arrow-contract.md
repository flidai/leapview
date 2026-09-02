# Dashboard native Arrow response contract

This document defines the `native-v1` correctness oracle for a future native
Arrow response from dashboard table data queries. It does not activate that
response, change the current dashboard endpoint, or authorize a production
migration. FAI-543 and later experiments must satisfy this contract before
their performance results can be considered.

The existing unversioned dashboard Arrow response is a compatibility boundary:
it projects every cell to UTF-8, represents a source null as an empty string,
and returns its next cursor as an initial response header. A `native-v1`
response instead preserves governed Arrow values and uses a completion trailer.
Those behaviors are not wire-compatible. The current stream must not silently
change; a native response requires explicit client opt-in and the response must
identify `native-v1` in both its header and schema metadata.

## Scope

The first eligible implementation is an ordinary detail/table query. It must
use the same authorization, governance, admission, audit, result-budget,
sorting, and pagination inputs as the current direct dashboard query. Matrix,
pivot, calculations, multi-block shaping, Datastar SSE, retained-result cache,
and warm-cache delivery are outside this contract's first implementation.

## Schema contract

The IPC stream schema is the post-governance projected schema, not the source
table schema. In particular:

- Fields appear in governed projection order. Field names are the requested
  aliases where aliases exist; physical source names must not replace aliases.
- Arrow physical types are preserved. Boolean and signed/unsigned integer
  widths, floating-point widths, dates, timestamps, UTF-8 strings, binary
  values, decimals, and dictionary encodings must not be projected to strings.
- The field's nullable declaration is the final governed effective nullability
  described below. Each array's physical validity bitmap is preserved. A null
  must remain null; it must not become zero, `false`, an empty string, or an
  empty byte slice.
- A decimal preserves its precision, scale, sign, and exact unscaled value.
- A timestamp preserves its Arrow unit and timezone. A timestamp with timezone
  `UTC` represents the same UTC instant; a timezone-neutral timestamp remains
  timezone-neutral rather than acquiring a server-local zone.
- Binary values preserve their exact bytes, including zero bytes and empty
  non-null values.
- A dictionary field preserves its index type, value type, ordering flag,
  dictionary values, indices, and validity bitmap. Expanding a dictionary to
  strings is a contract change.
- Only response-safe governed metadata from the closed allowlists below is
  emitted. It must not expose generated SQL, physical connection or source
  identifiers, policy expressions, credentials, principals, or other internal
  state.

### Nullability authority

`native-v1` uses **governed effective nullability with conservative fallback**.
The server-owned output schema descriptor for the final post-governance
projection is authoritative; neither a source-column declaration nor the
DuckDB Arrow field flag is independently authoritative.

An emitted Arrow field has `Nullable=false` only when the final projected
expression is proven incapable of producing SQL `NULL` after every governed
transformation. A nullable, unknown, incomplete, or conflicting derivation is
emitted as `Nullable=true`. In particular:

- a source `NOT NULL` declaration does not by itself prove that the final
  projection is non-null;
- the nullable side of a left relationship traversal is nullable;
- a column mask that can produce `NULL` makes the output nullable;
- a calculation or derived expression is nullable unless its complete
  operator and input semantics prove otherwise;
- metric nullability includes its governed empty-set policy; and
- filters or the values observed in one page, including a page with zero
  nulls, never narrow the declared nullability.

Unknown always maps to nullable. The rule is stable for empty results: an empty
stream uses the same derived output schema descriptor as a non-empty execution
of the same governed query. It must not infer nullability from the absence of
record batches.

Runtime validity bitmaps remain the authoritative physical statement about
which values are null. The stream forwards them without null-to-value
conversion. If a record contains a null for a field declared non-null, the
execution has violated the governed output contract; Arrow encoder acceptance
does not make that stream valid.

### Implementation boundary

The schema foundation represents each projected field with an internal
`query.OutputFieldDescriptor` containing:

- the final alias;
- the public governed logical type;
- effective nullability represented internally as proven non-null, nullable,
  or unknown; and
- internal derivation provenance sufficient to explain the decision without
  exposing source, policy, SQL, or connection details on the wire.

`query.Planner.DescribeOutputSchema` derives the descriptor before a response
schema could be emitted from validated final PlanIR, including relationship
traversal, column masks, metric empty-set policy, calculations, and derived
expressions. Unknown derivations remain nullable. The descriptor is matched to
the governed alias and projection order; it is not reconstructed from observed
batches or client-authored metadata.

`arrowquery.OutputSchemaSink` is the un-routed enforcement boundary. It
reconciles only the Arrow field nullability declaration required by the
descriptor, preserves physical Arrow types and validity buffers, respects
borrowed-batch callback lifetimes, and validates every emitted record batch.
Its internal provenance is opaque and is not serialized as response metadata.
No production route currently constructs this sink; this foundation does not
authorize a handler change or native streaming migration.

An empty result is a valid Arrow IPC stream containing the full governed schema
and zero record batches. It is not a schema-less stream and it is not a JSON
empty array.

### Reserved LeapView metadata

The `leapview.*` namespace is server-owned. Upstream/source metadata cannot set
or override it. The allowlists are closed: unknown keys are rejected whether
they use the `leapview.*` namespace, another namespace, or no namespace. Adding
a response metadata key requires a contract revision and corresponding oracle
coverage.

| Location | Key | Authority and required value |
| --- | --- | --- |
| Schema | `leapview.arrow_contract` | `native-v1` |
| Schema | `leapview.query_id` | Same value as `X-Query-ID` |
| Schema | `leapview.serving_snapshot` | Same value as `X-Serving-Snapshot` |
| Schema | `leapview.visualization_schema_version` | Server-controlled visualization schema version |
| Schema | `leapview.visualization_spec_revision` | Server-controlled visualization specification revision |
| Schema | `leapview.visualization_data_revision` | Server-controlled visualization data revision |
| Field | `leapview.logical_type` | Public governed logical type, when available |
| Field | `display.label` | Approved producer metadata for the governed display label |

All schema keys and `leapview.logical_type` are authoritative server values;
producer values cannot override them. `display.label` is the only producer
metadata currently approved for forwarding. SQL, engine, connection, physical
source, and other producer metadata are rejected. In particular, a cursor is
never placed in schema metadata: it is disclosed only through the response
trailer after successful completion.

## Response protocol

A successful native response is an Arrow IPC stream and has these required
headers before the body is committed:

| Header | Value |
| --- | --- |
| `Content-Type` | `application/vnd.apache.arrow.stream` |
| `Cache-Control` | `no-store` |
| `X-Query-ID` | Non-empty request/query correlation ID |
| `X-Serving-Snapshot` | Non-empty serving snapshot bound to the query |
| `X-LeapView-Arrow-Contract` | `native-v1` |
| `Trailer` | Declares `X-Next-Cursor` |

### Version negotiation

The legacy and native contracts share the dashboard visual query route but are
separate, explicitly negotiated representations. A native request supplies
both:

```http
Accept: application/vnd.apache.arrow.stream
X-LeapView-Arrow-Contract: native-v1
```

Legacy behavior remains unchanged: an unversioned Arrow request receives the
all-string schema, the existing initial `X-Next-Cursor` header semantics, and
the existing exact-or-capped `AvailableRows` behavior.

A successful native response echoes
`X-LeapView-Arrow-Contract: native-v1`. An Arrow request without the contract
marker remains on the legacy all-string representation. A marker without the
Arrow media type, or any unknown contract version, fails closed with `406 Not
Acceptable`; the server must never silently downgrade it. This foundation does
not activate the native representation in the production dashboard handler.
Activation remains a separate adoption change.

### Native-v1 page model

The default page limit is 100 rows. The accepted range is 1 through 1,000 rows,
inclusive. Native interactive pagination emits at most 10,000 cumulative rows
for one cursor chain. The server executes a `limit + 1` probe under the same
governed query while there is capacity below that cumulative cap, emits at most
`limit` rows, and uses only the probe row to decide whether continuation exists.
It does not calculate or promise an exact total and does not expose
`AvailableRows`. Clients that require exact or capped totals remain on the
legacy contract.

The first request has offset zero. A middle page resumes from the offset in a
validated native cursor. If the probe row exists and the IPC writer closes
successfully, the server writes an opaque `X-Next-Cursor` trailer. The final
page, an empty result, and a page that reaches the 10,000-row cap have no trailer
value. Exactly `limit` physical rows is a final page; `limit + 1` physical rows
is the first condition that permits a cursor. At offsets where a full requested
page would cross the cumulative cap, the emitted/query limit is reduced to the
remaining allowance and no continuation probe is executed beyond the cap.

The trailer name is declared before commitment on every successful native
response so clients can consume final and continuing pages uniformly. Initial
headers and Arrow schema metadata never contain the native cursor. Probe rows
count toward physical execution and result budgets even though they are not
emitted. The empty response remains a complete native schema with zero rows and
no cursor.

### Dashboard-native cursor domain

Native-v1 uses the signed `d3` cursor domain. Legacy dashboard `d1` cursors and
semantic-query `q1` cursors are not interchangeable with `d3`; every decoder
rejects cursors from the other domains. A native cursor expires after 15 minutes
and binds:

- the `native-v1` contract version;
- dashboard, page, and visual identity;
- server-computed canonical digests of normalized filters, selections, and
  effective sorting;
- the requested page limit;
- the final governed effective-policy identity;
- the serving snapshot;
- the next offset, cumulative rows consumed, and 10,000-row cap; and
- its expiry.

The signed payload contains a digest of the request/governance scope rather
than raw filters, selections, sort definitions, policies, principals, SQL,
source identifiers, or physical connection details. Governance and
authorization are recomputed for each page before the cursor binding is
accepted. A changed contract, identity, normalized request state, limit, or
policy identity is an invalid cursor (`400`). A different serving snapshot is a
cursor conflict (`409`). Clients treat the entire cursor as opaque.

## Error and commit boundary

Before the Arrow response is committed, failures use the API's JSON problem
shape with `Content-Type: application/problem+json` and no Arrow contract
headers. Existing resource-concealment rules remain authoritative.

| Failure | Required result before commit |
| --- | --- |
| Authentication failure | `401` problem response |
| Authorization failure | `403`, or `404` where the route conceals inaccessible resources |
| Malformed or wrong-scope cursor | `400` problem response |
| Expired, wrong-contract, changed-filter, changed-selection, changed-sort, changed-limit, or changed-policy cursor | `400` problem response |
| Cursor serving-snapshot mismatch | `409` problem response |
| Row or byte budget failure | `422` problem response |
| Admission rejection or resource exhaustion | `503` problem response, including `Retry-After` where applicable |
| Admission queue timeout or execution timeout | `504` problem response |
| Internal failure | `500` problem response |

Request cancellation stops query work and releases borrowed batches and other
leases. If the connection remains writable and no bytes have been committed,
the failure follows the normal problem mapping; a disconnected client may
observe only connection termination.

A runtime null in a field declared non-null is a native contract failure. If
detected before response commitment, the server returns the normal structured
JSON problem response and does not start an Arrow stream. If detected after
commitment, the server terminates the Arrow stream, does not append a JSON
fallback, and does not publish a successful `X-Next-Cursor` trailer value.

After the Arrow response is committed, the server cannot switch formats. A
query, IPC, cancellation, or transport failure terminates the stream. There is
no JSON suffix or fallback, no successful completion signal, and no
`X-Next-Cursor` trailer value. Consumers must treat an unreadable or incomplete
IPC stream as failed even if the HTTP status was already `200`.

Cursor publication is ordered after successful IPC close. Cancellation,
timeout, row/byte budget failure, admission failure, partial write, IPC write
failure, or IPC close failure therefore cannot publish a successful cursor.

## Direct-stream resource policy

The unrouted FAI-602 lifecycle foundation defines bounded synchronous delivery
for a future native-v1 route. It does not activate native dashboard serving.

- Native streaming is disabled unless the analytical pool has at least two
  connections. The initial limit is one stream per instance, one per principal,
  and one per project. Future limits remain no greater than `pool size - 1` and
  approximately 25 percent of the analytical pool, preserving at least one
  ordinary-query connection.
- Stream-capacity acquisition is non-queuing and occurs before workload
  admission, serving-generation leasing, or database acquisition. A request
  already holding another workload permit is rejected rather than nested or
  queued.
- The workload admission occupancy starts at its grant and spans planning,
  execution, synchronous IPC delivery, IPC close, cursor publication decision,
  and terminal audit recording.
- The absolute lifetime is 30 seconds from admission grant. An earlier request,
  workload, shutdown, or serving deadline wins. Every socket write has a
  five-second no-progress deadline; forward progress refreshes only the idle
  deadline and never extends the absolute lifetime.
- Cleanup is synchronous and ordered: close the Arrow reader, release the
  database connection, release workload admission, release native-stream
  capacity, and finally release the serving-generation lease. The hard cleanup
  bound is two seconds, with a one-second p95 target.
- One result budget is established before lifecycle acquisition and remains
  shared through governed execution and transport delivery. The governed Arrow
  producer charges the physical schema, record bytes, emitted rows, and the
  `limit + 1` probe exactly once. The streaming boundary charges only
  server-added contract metadata and actual IPC bytes accepted by the
  transport. A failure after commitment aborts the stream and suppresses the
  successful cursor trailer.
- Terminal observations record emitted and probe rows, IPC bytes, connection
  hold time, admission occupancy, timeout reason, cancellation cleanup latency,
  cleanup-bound violations, and post-commit aborts. Success is recorded only
  after clean IPC close and a successful cursor publication decision, while all
  lifecycle resources remain owned.

The operation executes with the analytical lease context so nested governed
execution reuses the pinned connection instead of acquiring a second one. The
foundation requires the lease and Arrow reader to release synchronously. Its
cleanup interval begins when cancellation, timeout, or operation failure starts
and includes operation unwind and ordered resource release. It measures
cleanup-bound violations but does not add a second database pool, asynchronous
buffering, or a production routing path.

## Security and governance invariants

The native transport changes representation only. Before any schema or batch
is written, the request must pass the same boundaries as the current governed
direct query:

1. Resolve the authenticated principal and active serving snapshot.
2. Authorize the dashboard/model dependencies and preserve any resource
   concealment behavior.
3. Apply row policies and column masks and compute the effective policy
   fingerprint. The emitted schema is the masked projection; it cannot reveal
   denied source columns or pre-mask physical types/metadata.
4. Acquire workload admission using the same class, identity, operation, and
   memory estimate as the control query.
5. Apply row and byte budgets to the schema, emitted rows, and pagination
   probe. A transport encoder cannot bypass budget accounting.
6. Record the same audit actor, credential/effective subject, operation,
   resource target, start, success, and failure outcomes as the control query.

FAI-543's borrowed DuckDB batches may be observed only synchronously inside
the sink callback. The sink must finish reading or encoding a batch before the
callback returns, must not retain borrowed buffers, and must not populate or
read the retained dashboard result cache.

## Correctness qualification

A candidate is rejected before performance comparison if it differs from the
control in column order, aliases, physical types, values, null positions,
metadata, sorting, offset/limit behavior, cursor behavior, cancellation,
partial-write handling, authorization, row policies, masks, admission,
budgets, or audit identity. The executable fixtures in
`internal/dashboard/http/arrow_contract_test.go` lock the native value and wire
semantics. Existing authorization, admission, budget, and audit tests lock the
shared governed execution boundaries.

Only after those gates pass may an experiment compare the candidate with the
current `api_direct` dashboard path using the same query and physical query
behavior. Warm-cache measurements are a guardrail, not a comparable lane.

Nullability qualification must cover, at minimum:

- base `NOT NULL` and nullable fields;
- aliases and projection ordering;
- empty results;
- fields on both sides of a left relationship traversal;
- masks that preserve, replace, or introduce nulls;
- metrics with null and zero empty-set policies;
- calculations and derived expressions with proven and unknown semantics;
- multiple record batches; and
- non-null declaration mismatches detected both before and after response
  commitment.

## Client compatibility and activation

No first-party browser component currently decodes this API Arrow stream, but
API and generated-client consumers may rely on the current string/null
projection. Therefore:

- native delivery requires the two-header `native-v1` opt-in above; merely
  sending the Arrow media type continues to select the existing legacy stream;
- the response must return `X-LeapView-Arrow-Contract: native-v1`, and clients
  must reject unknown contract versions;
- clients must support native Arrow types, validity bitmaps, dictionaries, and
  HTTP trailers before opting in;
- generated clients continue to prefer JSON unless native Arrow support is
  explicitly selected.

The request marker must be added to TypeSpec/OpenAPI when production activation
is proposed. This contract foundation intentionally remains unrouted so the
generated and live endpoint cannot claim support before native serving,
resource policy, and client qualification are complete.
