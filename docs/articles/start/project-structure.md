# Project structure

A LeapView analytics source root compiles into one atomic resource graph. Connections and sources provide governed inputs; models, semantic models, pipelines, and dashboards are discovered from fixed directories and can reference one another by stable IDs.

```text
dashboards/
  connections/
    warehouse.yaml
  sources/
    warehouse.orders.yaml
  models/
    orders.yaml
  semantic-models/
    sales.yaml
  pipelines/
    sales-refresh.yaml
  dashboards/
    executive-sales.yaml
```

These six directory names are the authoring contract. LeapView discovers top-level `.yaml` and `.yml` resources in deterministic kind-and-path order. A resource ID may appear only once across the entire source root.

## Source-root entry point

Pass the directory itself to authoring commands:

```sh
leapview validate --project dashboards
leapview plan dashboards
```

There is no authored `kind: Project` manifest or include registry. Groups, role bindings, grants, and dashboard publications are instance control-plane state managed through authenticated UI and API surfaces. `DataPolicy` remains a transitional compatibility input under `access/` until the semantic access contract completes qualification.

## Resource layers

- **Connections** define how LeapView reaches physical data.
- **Sources** use a connection and provide stable logical names, paths, and field definitions.
- **Model tables** transform permitted sources into reusable analytical tables.
- **Semantic models** define dimensions, metrics, and relationships across model tables. Shared dimensions can serve multiple semantic consumers in the same graph.
- **Pipelines** select a semantic model and optionally define named schedules with explicit timezone, late-start, and concurrency policy.
- **Dashboards** compose semantic queries into filters, visuals, tables, pages, and layout.
Managed-data planning and revision activation operate at deployment scope. A deployment can therefore pin a consistent set of shared input revisions while changing several dependent resources atomically.

## Resource identity and metadata

Every resource uses the same envelope: `apiVersion`, `kind`, `metadata`, and `spec`. `metadata.id` is the explicit immutable resource identity within an instance; `metadata.name` is its stable symbolic name. `displayName`, `description`, `owner`, and `tags` communicate intent without changing identity. A Project or workspace container and `metadata.workspace` are not accepted authoring contracts.

Use stable names and IDs, and avoid encoding environment names in them. Deploy the same source root to separate dev, staging, and production instances instead of creating parallel resource trees.

## Validate discovery

Validate the source root after moving or adding files:

```sh
go run ./cmd/leapview validate --project dashboards
```

Validation catches duplicate IDs, misplaced kinds, invalid references, unsupported fields, and other contract failures before deployment. The generated [configuration reference](/docs/config/connection) links the exact schema for each accepted kind.
