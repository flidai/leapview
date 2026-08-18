# Connect a data source

Connections are project-level access definitions. Sources give individual files, objects, or tables stable logical names in the project graph. This guide follows one managed-file workflow so that the procedure stays reproducible; use [Connection configuration](/docs/config/connection) and [Source configuration](/docs/config/source) for the complete set of external-system providers, credentials, formats, and fields.

## Before you begin

Choose a development environment, prepare a representative CSV with a stable header, and confirm that the project validates before your change. Do not begin with production credentials or an unbounded shared directory.

The procedure is:

1. Define one connection for the access and operational boundary.
2. Define logical sources for the physical objects it exposes.
3. Discover those resources from the project manifest and grant only the required resource privileges.
4. Plan and stage the managed revision.
5. Validate the graph and verify the source contract before modeling.

## Design the source boundary

### Choose a stable boundary

Before writing YAML, decide:

- whether LeapView will own uploaded file revisions or read an external system;
- which credentials and defaults belong to the connection;
- which physical objects deserve stable source identities;
- which project resources may consume each source;
- how source field types and missing values will be interpreted.

Use one connection for inputs that share access method, credentials, and operational lifecycle. Do not create a new connection merely to give every file a name; that is the source's responsibility.

### Define the connection

Create `dashboards/connections/commerce.yaml`:

```yaml
apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:commerce
  name: commerce
  displayName: Commerce managed files
  owner: data-platform
spec:
  type: managed
  defaults:
    csv:
      header: true
```

`metadata.name` is the stable identifier used by sources and managed-data commands. The `managed` kind means local files are planned, staged, and activated as immutable revisions. Other connection kinds may require a host, database, root, path, options, or an environment-backed credential secret.

Never put a password, API key, or cloud secret value in this file. Use the runtime credential provider supported by the connection contract.

### Define a source

Create `dashboards/sources/commerce.orders.yaml`:

```yaml
apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:commerce.orders
  name: commerce.orders
  displayName: Orders
  owner: data-platform
spec:
  connection: commerce
  location:
    type: path
    path: orders.csv
    format: csv
  schema:
    mode: compatible
    fields:
      order_id: {datatype: String, description: Stable order identifier.}
      customer_id: {datatype: String, description: Customer identifier.}
      purchased_at: {datatype: String, description: Source purchase timestamp.}
      amount: {datatype: Decimal, description: Source order amount.}
```

The source name is logical identity; `path` is a physical detail that can evolve. Declare the source fields expected by downstream transformations. Model-table SQL should still cast defensively when physical CSV values can be malformed.

## Govern discovery and access

### Discover the resources

Confirm the project manifest includes both directories:

```yaml
spec:
  connections:
    include: [connections/*.yaml]
  sources:
    include: [sources/*.yaml]
```

Include patterns are relative to the project manifest. A duplicate match or undiscovered resource should be corrected in the manifest rather than worked around with an absolute path.

### Reference the source in the project graph

Model resources declare governed SQL references through the source namespace:

```yaml
spec:
  definition:
    type: sql
    sql: SELECT * FROM source."commerce.orders"
```

The compiler derives lineage and keeps SQL reads within the governed project graph. It does not stage managed data or grant every user access to the resulting model.

## Validate ingestion

### Validate and stage managed files

Validate the resource graph:

```sh
leapview validate --project dashboards/leapview.yaml
```

For a managed connection, inspect the local revision before uploading it:

```sh
leapview data plan \
  --project dashboards/leapview.yaml \
  --connection commerce \
  --from ./data/commerce
```

Then stage it to a target with `leapview data sync`. Staging returns an immutable revision digest; deployment activates that reviewed digest separately.

## Verify the source boundary

Check that filenames match source paths exactly, source fields reflect the actual header, credentials resolve in the target instance, and compiler-derived governed lineage covers every source each model SQL expression reads. Continue with [Define model tables](/docs/guides/build/model-tables).

For managed data, retain the revision digest returned by staging and confirm that the target can resolve it before deployment. Re-run the plan against the same input directory; an unchanged directory should produce the same reviewed revision.

## Troubleshooting

If validation cannot discover the connection or source, resolve include patterns relative to `dashboards/leapview.yaml` and check for duplicate matches. If staging reports missing files, compare the source `location.path` with the case-sensitive filename beneath `--from`. If a model later reports a missing source dependency, correct its governed SQL reference or source resource rather than bypassing project validation.

## Next steps

Continue with [Define model tables](/docs/guides/build/model-tables). See [Connection configuration](/docs/config/connection), [Source configuration](/docs/config/source), and [Managed data ingestion](/docs/data-ingestion) for exact contracts and revision behavior.
