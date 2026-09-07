# Managed data and revisions

Managed data gives file-backed analytical inputs an immutable identity that can move through the same reviewed delivery boundary as project configuration. A managed connection and its source definitions belong to the project graph.

## Content-addressed revisions

A revision identifies a canonical manifest of files for one managed connection. Its digest changes when the content or canonical manifest metadata changes; a folder name, upload time, or environment does not define revision identity.

This makes planning and transfer repeatable. Content already present in the backing store can be reused, while changed input produces a new candidate instead of mutating a serving revision in place.

## Staging and activation

Staging makes a revision available to LeapView but does not change what dashboards query. Activation happens through project deployment, which pins one revision for every managed connection alongside the compiled project artifact.

The distinction prevents partially updated serving state. A model change and the data revision it expects become active together, or the prior project and revision pointers remain active together when validation fails.

## Product and transport boundaries

LeapView owns upload sessions, revision identity, authorization, expiry, finalization, and activation. A negotiated transport such as resumable local upload or direct multipart object-store upload owns byte transfer details.

Transport completion is therefore not activation. The product must finalize the upload session into an immutable revision, and a later reviewed deployment must select that revision. Storage credentials remain behind the server or short-lived signed transport requests rather than becoming part of project configuration.

## Runtime consumption

DuckDB reads the active revision through native file scanners and runtime views. Ingestion does not insert each source row into an application-owned transactional table. Model refreshes transform the active inputs into governed analytical state with their own snapshot lifecycle.

Managed source revisions and analytical serving snapshots solve different problems and can have different retention windows. A retained source revision is not automatically a complete customer-facing rollback point unless compatible project artifacts and analytical state are also available.

## Storage responsibility

Production recovery uses PostgreSQL-native backup/PITR for control-plane
metadata and the configured object store's native versioning, replication, or
backup mechanism for managed objects. A local SQLite/file archive is not a
PostgreSQL target recovery point. A recoverable deployment needs both sides of
the ownership boundary: metadata without referenced objects is incomplete, and
objects without corresponding revision and deployment records are not a
serving-state restore. Follow the [PostgreSQL operations
guide](/docs/guides/operate/postgresql-operations) and [Backup and restore
guide](/docs/guides/operate/backup-restore) for the complete procedure.

Follow [Data revisions and activation](/docs/guides/data/revisions) to stage and deploy a revision, [Materialization and refresh](/docs/guides/data/refresh) to rebuild analytical tables, or [Storage and recovery](/docs/guides/data/storage-recovery) to design the backup boundary.
