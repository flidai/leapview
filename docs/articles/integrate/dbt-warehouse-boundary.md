# Use dbt at the warehouse boundary

LeapView integrates with dbt by consuming the physical data product that dbt
publishes. dbt remains the transformation runtime; LeapView remains the BI
serving runtime.

```text
raw inputs -> dbt staging -> dbt marts -> warehouse relation or Parquet
                                                |
LeapView Connection -> Source -> thin Model -> SemanticModel -> Dashboard
```

The boundary is the physical relation, including its locator, schema, grain,
freshness, and publication state. `manifest.json` and `run_results.json` are not
serving inputs and are not accepted as evidence that physical publication
completed.

## Run the local showcase

From the repository root, run:

```sh
task dbt:warehouse
```

The task prepares the versions pinned in
`examples/dbt-warehouse-boundary/dbt/requirements.txt`, runs `dbt build`, and
verifies that the exact two expected non-empty Parquet files exist. It then
delegates to the repository's managed LeapView development flow, synchronizes
those files through the ordinary managed Connection, builds and qualifies a
candidate, publishes it, and starts the dashboard server. No paths need manual
editing.

The non-interactive CI equivalent is:

```sh
task dbt:warehouse:qualify
```

dbt staging and mart SQL owns normalization, joins, and aggregation. The
LeapView Models only select fields, assign the stable `snake_case` BI field
IDs, perform safe casts, and enforce the consumer-side contract. The
SemanticModel adds labels such as “Customer region” and formats revenue and
average order value as currency without another cosmetic physical
transformation.

The Source schema is strict about physical field names and types. Its fields
remain nullable because portable Parquet discovery does not carry dbt's
row-level non-null proof; error-severity Model checks re-establish that proof
against the candidate's actual rows before activation.

Removing dbt's `target/`, manifest, or run-results files after publication does
not affect LeapView. Serving also requires no Python installation, dbt binary,
dbt profile, package credentials, or transformation credentials.

## Use the production Azure reference

`.github/workflows/dbt-warehouse-boundary-reference.yml` is a deliberately
manual, copyable reference. Configure its `dbt-warehouse-boundary-production`
GitHub environment with:

- `DBT_PRODUCER_CLIENT_ID`, `AZURE_TENANT_ID`, and
  `AZURE_SUBSCRIPTION_ID` variables for the producer's Azure OIDC login;
- `DBT_PRODUCER_STORAGE_ACCOUNT`, `DBT_PRODUCER_SOURCE_CONTAINER`, and
  `DBT_PRODUCER_PUBLICATION_CONTAINER` variables;
- `LEAPVIEW_TARGET` and `LEAPVIEW_PROJECT_ID` variables; and
- `LEAPVIEW_WORKLOAD_CLIENT_ID` and `LEAPVIEW_WORKLOAD_CLIENT_SECRET` secrets.

The producer identity may read only the two bounded reference inputs and may
create objects in the publication container. It does not receive LeapView
serving-state access. The workflow runs dbt and its tests, verifies the local
physical files, creates a unique prefix from the workflow run, attempt, and Git
revision, uploads exactly the selected marts without overwrite, and lists the
prefix to prove that the complete expected set exists. Only then does it render
an ordinary Azure-backed Connection/Source bundle and invoke `leapview dev
--once --no-browser` followed by `leapview publish`.

The LeapView target owns two credentials that are not present in the producer
workflow:

- the `azure_blob` Source binding has read-only access to the publication
  container (and should be scoped to its prefix where the target supports it);
- the DuckLake/serving binding has write access only to LeapView-owned physical
  storage.

These are three distinct authorities: producer write, Source read, and
DuckLake write. Never copy the producer's Azure profile or dbt credentials into
LeapView. GitHub masks configured secrets, and the reference commands suppress
Azure response bodies and dbt row output. Do not add raw-data previews or
credential-bearing artifacts to the workflow.

## Choose the consistency contract

An ordinary mutable relation or several independently observed Sources provide
the consistency promised by their connector and producer. LeapView does not
claim that independent upstream reads occurred at one point in time. Candidate
activation is atomic only after the selected inputs have been read and the
LeapView Models have been materialized and qualified.

For coordinated marts, publish the complete set under one new immutable
versioned prefix (or use a warehouse transaction/snapshot with equivalent
semantics). Start LeapView only after exact file-set verification succeeds.
The reference workflow never selects a partially uploaded prefix and never
rewrites an admitted prefix. No publication marker, dbt-specific release type,
or custom release envelope is involved.

## Verify failure and recovery behavior

| Failure | Result |
| --- | --- |
| dbt compilation or test failure | Publication and LeapView steps do not run. |
| upload failure or partial file set | Verification fails and LeapView does not run. |
| stale Source observation | ADR-0010 freshness qualification blocks activation. |
| malformed Parquet or incompatible schema | Source discovery/materialization blocks the candidate. |
| invalid grain or failed Model check | ADR-0010 qualification blocks activation. |
| LeapView activation failure | Existing activation/reconciliation leaves the prior generation selected. |

Producer objects are not deleted or mutated to recover LeapView. Existing
Project/environment-scoped candidate cleanup, retained generations, rollback,
leases, and recovery remain authoritative.

Ingestion minimization and semantic policy solve different problems. The
producer should omit unnecessary columns and rows before publication. LeapView
masking, row-level access, and semantic permissions govern downstream serving;
they do not retroactively minimize what LeapView ingested.

## Deferred features

The v1 integration intentionally does not include dbt artifact import,
MetricFlow, dbt Semantic Layer import, immutable data promotion across
environments, or cross-Project imports. A future metadata importer may assist
authoring, but it must remain optional and reconcile against physical data
rather than becoming runtime authority.
