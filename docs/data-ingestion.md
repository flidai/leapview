# Managed data ingestion

LeapView managed data turns local files into immutable, project-global revisions that can be reviewed and activated with the project. Use this page to choose the concept, procedure, or exact contract that matches your current need.

## Understand the lifecycle

Read [Managed data and revisions](/docs/concepts/managed-data) to understand content-addressed identity, staging versus activation, upload transport boundaries, atomic project delivery, and storage ownership.

If you are designing physical inputs first, read [Connections and sources](/docs/concepts/connections-sources) for the boundary between a connection, a reusable source, and project-resource authorization.

## Complete a data task

- [Plan, stage, and activate a data revision](/docs/guides/data/revisions).
- [Refresh model tables](/docs/guides/data/refresh) without disrupting active readers.
- [Back up or recover analytical storage](/docs/guides/data/storage-recovery).
- [Diagnose ingestion and refresh failures](/docs/guides/data/troubleshooting).

These guides contain goal-oriented procedures and observable verification steps. They link back to concepts instead of embedding lifecycle explanations into every command sequence.

## Look up exact contracts

Use the generated [CLI data command reference](/docs/cli/data) and [data revisions command reference](/docs/cli/data-revisions) for current flags and accepted arguments. Use the [Managed Data API reference](/docs/api/managed-data) for upload-session and revision operations, and the [Configuration reference](/docs/config) for connection, source, model-table, and refresh-pipeline fields.
