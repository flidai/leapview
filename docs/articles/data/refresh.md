# Materialization and refresh

A refresh rebuilds project analytical tables from the sources and revisions selected by the active deployment. It creates replacement analytical state and activates it only after the complete candidate succeeds.

## What a refresh uses

At refresh start, LeapView resolves:

- the target project, instance environment, and active deployment;
- project-resource source permissions;
- active managed-data revision pointers and external connection settings;
- model-table dependency order and transformation SQL;
- the current DuckLake catalog and serving-state boundary.

That resolved input should remain consistent for the run. A later project deployment or refresh generation must not silently rewrite the meaning of already running work.

## Lifecycle

The expected lifecycle is:

1. Create a refresh run and record its target generation.
2. Resolve and validate source bindings.
3. Execute model-table transformations into isolated replacement state.
4. Validate schemas and required analytical metadata.
5. Commit the candidate as a DuckLake snapshot.
6. Atomically move the project serving pointer for the instance environment to the new state.
7. Mark the old state as draining and reconcile it later when no query lease protects it.

Queries that began against the previous active snapshot continue using that snapshot for their request. New queries resolve the new pointer after activation. Users never intentionally see a half-refreshed combination of model tables.

## Start and observe work

Use the project asset refresh surface or the generated [Refresh Runs API](/docs/api/refresh-runs) according to the caller. Track the returned run identity and generation rather than assuming every transient loading state will be observed.

Refresh state should distinguish queued, running, succeeded, failed, and cancelled or superseded outcomes. Inspect the latest relevant run when a user starts several refreshes quickly; an older run may be cancelled so it cannot overwrite newer state.

## Write deterministic transformations

A reproducible refresh depends on more than immutable managed files. Model SQL should also be deterministic:

- provide explicit tie-breaking when deduplicating;
- avoid wall-clock-dependent values unless intentionally part of the model;
- cast source values to stable types;
- declare every source dependency;
- keep keys and grain invariant across runs;
- use bounded external-source reads and stable snapshots where the connector supports them.

An external database can change during a refresh unless its connector and transaction boundary provide a consistent snapshot. Document that operational expectation for each non-managed connection.

## Validate the candidate

Configuration validation cannot prove data correctness. Before promoting a changed transformation, compare:

- row counts and key uniqueness;
- schema and nullability;
- failed cast and unexpected-null rates;
- representative semantic totals;
- relationship cardinality assumptions;
- refresh duration and data-file growth.

A successful SQL execution that produces zero rows or duplicates a dataset grain is still an invalid business result.

## Failure and cleanup

A failed or cancelled run must not alter the active pointer. Partial tables, uncommitted files, and job metadata are cleaned according to the storage lifecycle. A committed snapshot that never becomes active is also a cleanup candidate once no serving reference or in-process query lease protects it.

Do not manually delete analytical files to recover space. DuckLake owns file manifests and snapshot metadata; use the supported maintenance and cleanup workflow so metadata and physical files remain consistent.

Read [Storage and recovery](/docs/guides/data/storage-recovery) and [Storage architecture](/docs/storage-architecture) for ownership and retention details.
