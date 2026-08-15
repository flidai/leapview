# API conventions

LeapView APIs use explicit workspace/resource paths, bearer authentication, JSON payloads, bounded pagination, and conventional HTTP status. The OpenAPI document is authoritative for each operation's parameters and schemas.

## Base path and content

Headless operations live beneath `/api/v1`. Send `Accept: application/json`. For a JSON request body, send `Content-Type: application/json` and encode exactly the generated schema; unknown or malformed fields should be treated as client bugs.

Do not call internal browser update routes as an integration API. Datastar endpoints are UI transport contracts, while `/api/v1` is the versioned headless surface.

## Authentication

Send API tokens with the bearer scheme:

```text
Authorization: Bearer <token>
```

Use a dedicated service principal and never place the token in a URL. Protect it from logs, traces, error reporting, and request dumps. TLS validation must remain enabled.

Authorization combines the principal's effective privileges with token restrictions and data policies. A valid token can still receive `403` for a workspace or operation outside its scope.

## Resource identity

Workspace, dashboard, semantic-model, dataset, page, visual, table, principal, and run identifiers are explicit path parameters. Use IDs returned by discovery operations, not titles or labels.

Percent-encode path segments through a proper URL builder. Do not concatenate untrusted IDs into raw URLs or assume an ID from one workspace is valid in another.

## Pagination

List operations that expose `limit` and `pageToken` use bounded cursor-style pagination. Choose a limit appropriate to processing and service constraints. Treat the returned token as opaque and stop when no next token is provided.

Do not store page tokens as durable bookmarks or compare them lexically. Active data and access can change between pages, so integrations that require a consistent inventory should define their own reconciliation strategy.

## Errors

Handle status before decoding a success schema:

- `400` — request syntax, parameter, or body contract is invalid.
- `401` — credentials are absent or invalid.
- `403` — authenticated principal lacks effective permission.
- `404` — the scoped resource is unavailable or intentionally not disclosed.
- `409` — request conflicts with current lifecycle state.
- `429` — caller must reduce rate and back off.
- `500` — server could not complete the operation.

Preserve a bounded error body and any correlation identifier for diagnosis. Do not expose raw server errors directly to end users.

## Retries

Retry safe reads after transient network errors, `429`, or eligible server failures using exponential backoff and jitter. Set a maximum attempt count and total deadline.

Do not blindly retry create, deploy, refresh, turn, or administrative operations. First determine whether the server accepted the original request and whether the operation exposes an ID or current-state lookup that makes reconciliation safe.

`409` usually requires reading current lifecycle state rather than waiting and repeating the same payload. `400`, `401`, and `403` require correction, not retry.

Dashboard-authoring writes are explicit: always send `Idempotency-Key` on draft creation, commands, and forks. The key is separate from actor/tool-call provenance. Reusing a key with the same normalized payload or command fingerprint replays the durable result; reusing it with a changed payload conflicts; a different key creates a new draft. Builder commands and previews must carry the complete expected revision token rather than assuming the server will select the latest revision. See [Dashboard authoring and promotion](/docs/guides/operate/dashboard-authoring) for the route table and lifecycle rules.

## Timeouts and cancellation

Set connection, response-header, and overall operation deadlines. BI queries and refresh operations have different expected durations; do not give every request one unlimited timeout. Cancel client requests when their result is no longer needed so server work can be released where supported.

## Compatibility

Regenerate clients when the OpenAPI contract changes. Preserve unknown response fields where the client framework permits additive evolution, but do not send undocumented request fields. Pin integration tests to representative operations and run them during application upgrades.

See [API quickstart](/docs/guides/integrate/api-quickstart) and the generated [API reference](/docs/api).
