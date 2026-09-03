# Backup and restore

LeapView production recovery is a coordinated, provider-native operation. The
control plane is PostgreSQL and analytical state is a PostgreSQL-backed DuckLake
catalog plus its Parquet/object-store data. The removed offline administrative
backup and restore commands are not a supported production recovery path.

## Recovery objectives

Record an RPO and RTO for application metadata, active project state,
analytical snapshots, managed-data revisions, identity/access state, and every
external store. Keep project source in Git, but do not treat Git as a backup of
database state or analytical data.

## Native protection boundary

Use the PostgreSQL operator's supported backup and point-in-time recovery (PITR)
mechanism for the control plane. Protect the DuckLake catalog and Parquet or
object-store data with their provider-native snapshot, versioning, replication,
or backup mechanism. Select recovery points that are mutually consistent and
retain the encryption keys and secret-manager procedures needed to restore
them.

LeapView does not provide a product-owned local SQLite/file archive that can be
used as a PostgreSQL target backup. Follow [PostgreSQL operations and high
availability](/docs/guides/operate/postgresql-operations) for the provider
ownership boundary, alert conditions, maintenance fencing, credential
rotation, and failover checks. Do not claim that a local archive or copied
Parquet files are a supported restore artifact.

## Restore and verify

Stop writes, restore PostgreSQL and the matching DuckLake/object-store recovery
points with the native provider tools, then start LeapView with the matching
configuration and image. Set `LEAPVIEW_RECOVERY_SET_ID` to the exact published
frontier whose immutable passed validation evidence describes the restored
points; LeapView never selects the latest set. Before admitting traffic, verify
the instance identity, active project/deployment pointers, authorization state,
managed-data revisions, representative semantic queries, and dashboards.
Preserve the failed state and recovery evidence until the incident is closed.

For an empty or development SQLite fixture, use the fixture's own test harness;
that path is not production recovery. Coordinate any future operator workflow
through the [PostgreSQL operations guide](/docs/guides/operate/postgresql-operations)
and this guide rather than inventing CLI flags.
