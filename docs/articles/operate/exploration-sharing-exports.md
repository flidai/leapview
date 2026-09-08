# Exploration links and exports

Data Explorer can share the current exploration state and download a governed result. These are viewer operations; they do not publish a dashboard or create a copy of someone else's unpublished work.

## Choose the link intentionally

- **Current query link** is a live, canonical URL for the working query. A recipient must authenticate and have `RESOURCE_USE` access to the referenced semantic model. The query runs again against the active serving generation and current data policies, so results may change.
- **Saved exploration (latest saved version)** contains only the saved-exploration ID. It resolves the latest saved version through the normal viewer authorization path; possessing the URL does not grant access. Unsaved edits remain local until you explicitly save them.

The browser labels both links and provides a visible link fallback beside its Copy link control. Personal saved links remain personal. Choose **Organization** when saving only if the intended audience has the corresponding viewer/data access; visibility does not bypass model permissions, row policies, or masking.

**Save as current query** creates a new personal exploration from your current working state without changing the original saved item. **Duplicate saved version** copies the persisted revision instead; it does not include unpublished edits. Use **Save** after reopening when you intend to update the original item.

## Download CSV or Parquet

Download the current query from the explorer, or use the authenticated API for an exact saved revision:

```text
POST /api/v1/projects/{project}/saved-explorations/{exploration}/export
If-Match: "<complete revision token>"
{"format":"csv"}
```

The URL-backed API accepts the canonical `ExplorationSpec` in `POST /api/v1/projects/{project}/saved-explorations/url-export`. Both paths execute through the current viewer's policy and serving lease. They do not accept a saved ID as a shortcut to another user's working copy.

Exports default to at most 10,000 rows and 32 MiB, with the same independent row and byte bounds applied during query retention and encoding. CSV preserves typed scalar text and prefixes spreadsheet-formula values (including values beginning with tab, carriage return, or newline). Files are sent only after a complete, successful, bounded result is encoded.

## Troubleshooting rejected exports

- A stale `If-Match` revision returns a precondition failure. Reload the saved exploration and use the returned complete `ETag` value as `If-Match`; never substitute a revision number alone.
- A permission or private-resource failure is returned without source metadata. Ask an administrator for the appropriate viewer/model-use grant rather than forwarding the URL as authorization.
- Oversized, partial, canceled, invalid-format, or unavailable exports return an error and no partial file. Narrow the query, choose a supported format, retry after cancellation, or resolve the runtime/audit availability issue.

Executed exports produce a governed query event plus an export-preparation outcome (format, status, bounded row/byte counts, and stable error class). Preparation success does not prove the client received the file. Terminal failures are audited when the recorder is available; parse/authentication failures before execution and an unavailable audit recorder cannot produce an event. The metadata does not record SQL, filters, plans, or result values.

Parquet preserves signed/unsigned integers, fixed-scale decimal128 values up to 38 digits, booleans, nulls, and strings. Because the result contract carries names but no declared column types, all-null or empty columns conservatively use nullable UTF-8; mixed or unsupported values are rejected rather than coerced lossily.
