# Two producers, one LeapView Project

This FAI-678 proof extends the existing showcase without changing its primary
two-mart example. Run from the repository root:

```sh
task dbt:warehouse:proof
```

The task uses the same pinned dbt requirements as `task dbt:warehouse:build`.
Set `DBT_BIN` to an absolute executable path to use a separately installed
toolchain. The test runs producers in disposable directories; it does not write
dbt outputs, packages, credentials, or target bindings into portable source.

## Physical handoffs

```text
upstream_orders (local dbt package)
    → proof_consumer dbt build → commerce/fct_orders.parquet
                                      ↓ warehouse Connection / orders Source
independent CRM SQL snapshot → directory/dim_customers.parquet
                                      ↓ directory Connection / customers Source
                one Project-free LeapView source bundle
                    → thin Models → sales SemanticModel
                    → request-bound semantic access → existing dashboard
```

The CRM producer does not read the commerce publication. `consumer-overlay/`
adds a second ordinary managed Connection and routes the existing customers
Source to it. The test overlays these two files on `../leapview/`; it reuses
the existing Models, semantics, and dashboard. It does not collapse the two
physical roots or import dbt graph edges into LeapView.

dbt resolves the local package and builds/tests a **consumer-owned** order mart
before the handoff. This exercises the package case, not a live dbt Mesh
service. Live references to package/Project-qualified Models are rejected.
Project, repository, commit, target, manifest location, and invocation ID (from
dbt's CLI log) are logged as producer-only test evidence. The harness does not
read/import dbt manifests or run results. The consumer receives Parquet alone
and retains its authored IDs.

## Evidence and limits

`internal/app/dbt_multi_source_proof_test.go` uses the real CLI ProfileStore
issuer, rootless compiler, destination plan builder, DuckLake materializer,
Source/Model qualification gates, runtime-host activation and leased semantic
query. The same portable artifact binds to separate dev/prod target contexts
under one issuer-supplied ProjectUID. The directory target binding changes
`north` to `west` in production; query assertions check both regional revenue
results and distinct generation/plan identities. A missing second publication
blocks a replacement candidate while the retained generation still queries.
The runtime composition assertion applies the already-qualified portable
semantic-access representation to the compiled dbt/CRM mapping, proves that an
unbound execution is rejected, verifies authorization barriers for both
source-backed datasets, and rejects a principal whose target-owned attribute
does not satisfy the policy. Semantic-access policy lowering, durable registry
resolution, activation, cache, and audit remain owned by the ADR-0017 suites;
this proof does not reimplement them.

The local physical proof uses the existing test repository and activation
callback; it is **not** a substitute for PostgreSQL admission, bootstrap audit,
registry durability, or the full headless deployment path. Run those separately:

```sh
task dbt:warehouse:qualify
LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=true go test -tags duckdb_arrow ./internal/deployment/postgres -run '^TestPostgresResourceUIDMultiSourceProjectClosure$' -count=1 -v
```

The PostgreSQL fixture compiles this same consumer graph, seals its complete
ResourceUID inventory, and commits the inventory through FAI-670's actual
activation authority. It checks compatible-generation UID stability plus
foreign-Project and foreign-instance lookup rejection. The surrounding
ResourceUID qualification suite owns rollback, kind, tombstone, restore, and
concurrency behavior; this focused proof does not duplicate those tests. Its
minimal admitted-generation scaffold does not replace full delivery CI.

ResourceUIDs are stable **within an instance**. Environment is not part of their
identity key; promotion to a different environment uses a different instance
and therefore does not copy that instance's ResourceUIDs. The ProjectUID and
authored resource IDs preserve portable meaning. Neither UID comes from dbt
names, file paths, repository coordinates, or producer invocation metadata.

These are ordinary independently observed Sources: their captured LeapView
candidate activates together, but this proof makes no atomic upstream snapshot,
cross-environment byte-identical data, cloud IAM, or live Mesh claim. Missing
Docker blocks the PostgreSQL and headless commands and must be reported as an
environmental limitation rather than treated as a pass.
