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

Exports default to at most 10,000 rows and 32 MiB, with the same independent row and byte bounds applied during query retention and encoding. The canonical authored query still caps `limit` at 1,000; the export ceiling never raises that query limit. CSV preserves typed scalar text and prefixes spreadsheet-formula values (including values beginning with tab, carriage return, or newline). Files are sent only after a complete, successful, bounded result is encoded.

## Dashboard handoff

**Add to dashboard** copies the current exploration into an authorized editable
dashboard draft using the same semantic model. Choose the dashboard, page, and
half- or full-width placement. The server selects a non-overlapping position and
checks the current draft revision; it does not silently overwrite a concurrent
edit. Refresh the target after a revision conflict before trying again.

The new tile is an independent native dashboard visual, not a live link to the
saved exploration definition. Editing the exploration later does not change the
tile. Its queries still run against live data under each dashboard viewer's
permissions. Review and publish the draft through the normal dashboard workflow;
adding a tile does not automatically publish, commit to Git, or merge a branch.

Project/YAML dashboards first require an explicit editable copy. The copy link
opens the existing fork workflow in another tab so the current unsaved
exploration remains available. After creating a compatible copy, use **Refresh
targets** and select it. The original project dashboard is unchanged.

**Explore from here** reconstructs a published dashboard component's governed
query, display settings, and applied controls. Unsaved control edits are not
included. Fixed predicates remain fixed; only the matching editable control's
default is replaced. Unsupported mappings fail with an incompatibility message
instead of producing a different query. Model, saved-content, and authorized
chat-context entry links also open canonical live explorations; they never
derive queries from transcript text or stored result rows.

Compatible chat visual artifacts carry their original canonical query alongside
the existing visualization envelope. **Explore visual** reruns that query under
the current viewer's access, with a return link to its conversation. Older or
unsupported artifacts still render but do not offer this action; their query is
never guessed from chart values. Conversation return links also use the normal
authenticated chat route. Explorer chat context follows the latest accepted
query state, including when a saved exploration is reopened.

## Persistence and upgrades

Saved exploration identities, immutable revisions, and retry records live in
the PostgreSQL control plane. A save and its canonical Access audit event share
one transaction: an audit failure rolls back the save. Stored definitions do
not contain cached result rows, and reopening or exporting still applies the
current viewer's authorization.

The saved-exploration schema is installed by the forward control-plane
migration, not by HTTP requests or serving startup. Apply the release's
documented PostgreSQL migration procedure before starting the upgraded server;
do not edit an already-applied migration or grant schema-owner privileges to
the runtime role. See [PostgreSQL operations](/docs/guides/operate/postgresql-operations)
and [Backup and restore](/docs/guides/operate/backup-restore).

This schema upgrade does not import an older SQLite saved-exploration catalog.
If such a catalog contains data you need to retain, preserve its backup and
plan a separately validated data transfer before replacing that installation.

## Troubleshooting rejected exports

- A stale `If-Match` revision returns a precondition failure. Reload the saved exploration and use the returned complete `ETag` value as `If-Match`; never substitute a revision number alone.
- A permission or private-resource failure is returned without source metadata. Ask an administrator for the appropriate viewer/model-use grant rather than forwarding the URL as authorization.
- Oversized, partial, canceled, invalid-format, or unavailable exports return an error and no partial file. Narrow the query, choose a supported format, retry after cancellation, or resolve the runtime/audit availability issue.

Executed exports produce a governed query event plus an export-preparation outcome (format, status, bounded row/byte counts, and stable error class). Preparation success does not prove the client received the file. Terminal failures are audited when the recorder is available; parse/authentication failures before execution and an unavailable audit recorder cannot produce an event. The metadata does not record SQL, filters, plans, or result values.

Parquet preserves declared signed/unsigned integer widths, floats, booleans, strings, dates, timestamps, and fixed-scale decimal128 values up to 38 digits. Arrow-backed results retain that declaration even when a column is all-null or empty. Legacy producers without type metadata use conservative nullable UTF-8 for those columns; mixed, malformed, or unsupported typed values are rejected rather than coerced lossily.
