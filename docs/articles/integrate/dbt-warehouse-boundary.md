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

## Evidence status

The integration has three deliberately separate evidence classes:

- **Proven local** — `task dbt:warehouse` builds the pinned dbt project,
  verifies the exact two physical Parquet marts, and runs them through the
  ordinary local LeapView candidate lifecycle.
- **Structurally validated, not live Azure** — the CI contract and the manual
  Azure reference are YAML-validated for ordering, trusted-ref gating,
  immutable publication, credential scope, and CI-gate wiring. Repository CI
  does not exercise live Azure OIDC, storage retention, or Azure RBAC. The
  separate manual Azure qualification workflow is maintained and fail-closed,
  but a successful protected-main run is required before those boundaries are
  classified as proven live.
- **Project-free source root** — the profile is discovered from conventional
  resource directories. The issuer bootstraps the durable ProjectUID before
  target binding; `LEAPVIEW_WORKLOAD_PROJECT` requests the exact bound scope
  for the CI workload identity, but does not issue Project identity or place it
  in the portable source tree.

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

For the separate two-producer Project-closure proof, run
`task dbt:warehouse:proof`. It reuses the example's thin Models and SemanticModel
with two target-bound Connections, a consumer-owned mart built from a local dbt
package, and an independent CRM publication. The
[proof fixture](https://github.com/flidai/leapview/tree/main/examples/dbt-warehouse-boundary/multi-source)
documents the assertions and distinguishes local physical qualification from
PostgreSQL registry and full headless lifecycle validation. The proof also
binds the compiled cross-source mapping to the existing request-scoped semantic
access consumer: execution without that capability fails, both dataset scans
carry authorization barriers, and denied target-owned attributes fail closed.

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
manual, copyable reference. Its production job runs only when manually
dispatched from the repository's protected default branch, using GitHub's
`github.ref`, `github.event.repository.default_branch`, and
`github.ref_protected` context. Configure its
`dbt-warehouse-boundary-production` GitHub environment with:

- `DBT_PRODUCER_CLIENT_ID`, `AZURE_TENANT_ID`, and
  `AZURE_SUBSCRIPTION_ID` variables for the producer's Azure OIDC login;
- `DBT_PRODUCER_STORAGE_ACCOUNT`, `DBT_PRODUCER_SOURCE_CONTAINER`, and
  `DBT_PRODUCER_PUBLICATION_CONTAINER` variables;
- the LeapView target URL and workload Project ID variables; and
- `LEAPVIEW_WORKLOAD_CLIENT_ID` and `LEAPVIEW_WORKLOAD_CLIENT_SECRET` secrets.

The following IAM scopes are operator requirements, not live authorization
evidence supplied by this repository: the producer identity must read only the
two bounded reference inputs and create objects only in the publication
container; the LeapView `azure_blob` Source binding must read only that
publication container (and, where supported, its selected prefix); and the
DuckLake/serving binding must write only LeapView-owned physical storage. The
reference workflow does not claim live Azure RBAC or checksum evidence.

The workflow runs dbt and its tests, verifies the local physical files, creates
a globally namespaced prefix from the repository, workflow run, attempt, and
Git revision, uploads exactly the selected marts without overwrite, and lists
the prefix to prove that the complete expected set exists. The producer job
then ends its Azure session and exposes only the non-secret prefix to a separate
activation job. That job has no Azure OIDC permission; it renders an ordinary
Azure-backed Connection/Source source root and invokes `leapview dev --once
--no-browser` followed by `leapview publish`. The target binds the durable
Project identity already bootstrapped by the issuer. The
`LEAPVIEW_WORKLOAD_PROJECT` value requests that exact bound Project scope for
the activation workload; it does not mint a ProjectUID.

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

### Run the live Azure boundary qualification

`.github/workflows/dbt-warehouse-boundary-azure-qualification.yml` is the
manual FAI-688 qualification layer. It is deliberately separate from the
production reference workflow: it verifies Azure data-plane permissions and
publication integrity without becoming a serving path or changing the
portable source bundle. It runs only from the protected default branch through
the `dbt-warehouse-boundary-azure-qualification` GitHub environment.

Configure that environment with the tenant, subscription, storage account,
and container variables used by the production reference, plus:

- `DBT_QUALIFICATION_SOURCE_CLIENT_ID`, the OIDC application whose deployed equivalent
  is bound to the `azure_blob` Source;
- `DBT_QUALIFICATION_DUCKLAKE_CLIENT_ID`, the OIDC application whose deployed
  equivalent owns LeapView physical-state writes;
- `DBT_QUALIFICATION_DUCKLAKE_STORAGE_ACCOUNT` and
  `DBT_QUALIFICATION_DUCKLAKE_CONTAINER`, a
  qualification scope separate from producer input and publication storage;
- `DBT_QUALIFICATION_PROJECT_UID` and `DBT_QUALIFICATION_ENVIRONMENT`, the durable scope used to
  namespace temporary DuckLake qualification writes.

The three client IDs must be distinct. Use container-scoped Azure assignments
at minimum: producer input read plus publication write for the producer,
publication read for the Source identity, and DuckLake-container write for the
DuckLake identity. Narrow the Source assignment with Azure attribute-based
conditions when prefix enforcement is supported. Do not grant account-key
access or management-plane key-listing permission to any qualification job.

The producer publishes one intentionally incomplete prefix and one new
run/attempt/Git-SHA-qualified complete prefix. Only the complete prefix is
exported to the read qualification. Uploads forbid overwrite, the exact file
set is listed, and both downloaded Parquet files must match producer SHA-256
evidence. The digest values are qualification evidence only; LeapView does not
consume a marker, manifest, or release envelope. The producer also requires
denied DuckLake-scope reads and conditional writes.

The Source identity must read and checksum the complete prefix. Safe
conditional Azure REST probes then require HTTP 403 with
`AuthorizationPermissionMismatch` for create and overwrite, and a
nonexistent-object probe requires the same denial for delete. The conditional
write probes use an impossible ETag, so an unexpectedly privileged identity
still cannot mutate admitted data. Cross-container probes also reject producer
input and DuckLake read/write access. The DuckLake identity performs a bounded
round trip inside its own qualification prefix, removes only that probe, and
must receive HTTP 403 from the producer publication scope.

The incomplete producer prefix is never exported to LeapView and is not
deleted as recovery. Its eventual removal belongs to the Azure storage
lifecycle configured for the qualification container. The same run executes
the existing consumer failure matrix, which proves stale observations,
incompatible schemas, failed Model checks, partial input, and activation
failure retain the prior serving generation through existing LeapView
lifecycle operations.

A workflow definition or architecture-test pass is not live Azure evidence.
Record the successful protected-main workflow run URL, immutable prefix,
non-secret checksum summary, and Azure role-assignment review in FAI-688 before
marking its live-cloud acceptance criteria complete.

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
or custom release envelope is involved. If an upload fails, an orphan partial
prefix remains unselected; storage lifecycle retention is the cleanup and
retention mechanism. Recovery must not mutate or reinterpret that prefix.

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
leases, and recovery remain authoritative. Orphan partial publication prefixes
remain unselected and require storage lifecycle retention, not recovery
mutation.

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
