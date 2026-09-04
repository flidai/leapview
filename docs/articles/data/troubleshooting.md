# Data troubleshooting

Diagnose managed-data and refresh failures from the earliest boundary that can reject the operation. Preserve the previous active state while investigating; do not edit serving files or pointers manually to make a failed candidate appear active.

## Establish the failing phase

First identify whether the failure occurs during:

1. local project validation;
2. local managed-data planning;
3. object upload and revision staging;
4. project deployment and revision activation;
5. Model materialization;
6. semantic or dashboard query serving;
7. storage cleanup.

Capture the exact command, project, connection, environment, target, run or deployment ID, revision digest, and relevant server logs. A generic “refresh failed” report without these identities is difficult to correlate safely.

## Planning fails

If `leapview data plan` fails:

- Validate the project and confirm the connection exists and is `managed`.
- Use the connection's resource name, not its title.
- Confirm `--from` points at the intended root and contains every referenced source path.
- Check permissions, symlinks, case sensitivity, and filenames.
- Keep files stable while they are discovered and hashed.
- Confirm the input does not exceed local disk or process limits.

Run the same command again only after the directory is stable. If comparing revisions, ensure `--previous-manifest` is the manifest for the same project connection.

## Sync fails

For `leapview data sync` failures:

- Verify target URL, token, and project/connection authorization.
- Confirm sufficient local, temporary, and backend object-storage capacity.
- Check reverse-proxy request and timeout limits for local resumable uploads.
- Check bucket permissions and multipart lifecycle for direct object-storage uploads.
- Retry resumable transfer without changing the source directory.

The client verifies that a file does not change during transfer. If it changed, create a new plan and revision. Do not suppress the mismatch or relabel an old digest.

## Deployment rejects a revision

The target resolves one immutable revision pin for every managed connection while preparing the deployment candidate. The `deploy` command does not accept client-supplied revision pins.

Confirm that:

- the intended revision completed `data sync` and is ready;
- the revision belongs to the same project and connection;
- the local project has not added or removed a managed connection since staging;
- the token can deploy and any explicit environment assertion matches the target instance;
- the target can prepare the project candidate with its selected pins and validated connection bindings.

Use `leapview data revisions list` to confirm the revision is staged and `current` to see which digest remains active.

## Refresh fails before SQL

If a refresh run cannot resolve inputs:

- confirm the compiler-derived source/model dependencies resolve from each model `spec.definition`;
- confirm the active deployment pins required managed connections;
- verify external connection credentials and network reachability;
- check that model files and source IDs match the active artifact, not only the working tree;
- inspect whether the run was superseded or cancelled by a newer generation.

Troubleshoot the active deployed configuration. A locally fixed file has no effect until it is validated and deployed.

## Model SQL fails

Inspect the first failing Model materialization and its compiler-derived dependency graph. Common causes include:

- renamed or missing source fields;
- invalid casts after source values changed;
- SQL dialect assumptions unsupported by DuckDB;
- unquoted logical source names containing dots;
- a join or expression referencing a source outside the governed dependency graph;
- out-of-disk or temporary-storage exhaustion.

Reproduce with the same source revision in development. Add defensive casts only when null-on-failure is acceptable; otherwise correct the source contract or reject malformed input.

## Refresh succeeds but results are wrong

Treat this as a data correctness incident, not a rendering issue. Compare row count, primary-key uniqueness, null rates, field types, and trusted aggregate totals. Check joins for dataset-row multiplication and verify relationship endpoint keys.

Use semantic preview/query and explain commands to isolate the model from the dashboard. If direct semantic results are correct, then inspect dashboard filters, selections, aliases, sorting, and limits.

## Cleanup or storage fails

Do not delete Parquet files or DuckLake catalog rows by hand. Check active serving references, in-process query leases, backend permissions, disk capacity, and maintenance dry-run output. A snapshot protected by an active pointer or lease is not eligible for cleanup.

Escalate with the failing run identity, storage backend, dry-run output, and relevant logs. See [`leapview data`](/docs/cli/data), [Refresh Runs API](/docs/api/refresh-runs), and [Storage and recovery](/docs/guides/data/storage-recovery).
