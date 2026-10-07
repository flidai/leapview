# Project structure

A LeapView source root is a directory of project resources. Connections and
sources provide governed inputs; Models, semantic models, pipelines, and
dashboards reference one another by stable IDs. The source root is portable
authoring input: the durable Project identity is bound to the target instance,
not stored in an authored `leapview.yaml` file.

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

## Source-root discovery

Pass the directory containing these conventional resource directories to the
CLI. LeapView discovers supported YAML resources from the source root; there is
no project manifest or include list to maintain. Keep files in the directory
for their kind, use one stable ID per resource, and avoid overlapping copies.

The six documented authoring resource kinds are Connection, Source, Model,
SemanticModel, Pipeline, and Dashboard. SemanticModel definitions can contain
portable access grants and filters. Principals, attribute assignments, role
bindings, resource-grant assignments, and public dashboard publication are
target-owned; standalone access resources are not source-root catalog kinds.

## Resource layers

- **Connections** define how LeapView reaches physical data.
- **Sources** use a connection and provide stable logical names, paths, and field definitions.
- **Models** transform permitted sources into reusable analytical outputs.
- **Semantic models** define datasets, dimensions, metrics, and relationships across semantic datasets. Shared dimensions can serve multiple semantic consumers in the same graph.
- **Pipelines** select a semantic model and optionally define named schedules with explicit timezone, late-start, and concurrency policy.
- **Dashboards** compose semantic queries into filters, visuals, tables, pages, and layout.

Managed-data planning and revision activation operate at project scope. A
deployment can therefore pin a consistent set of shared input revisions while
changing several dependent resources atomically.

## Resource identity and metadata

Every authored analytical resource uses the versioned `apiVersion`, `kind`,
`metadata`, and `spec` envelope. `metadata.id` is the explicit immutable graph
identity; `metadata.name` is the stable project-local name. `displayName`,
`description`, `owner`, and `tags` communicate intent without changing
identity.

The target supplies the durable Project identity and environment when a source
root is planned or published. Deploy the same source root to separate dev,
staging, and production targets instead of adding environment or workspace
containers to YAML.

## Validate discovery

Validate from the repository directory that contains the source root:

```sh
go run ./cmd/leapview validate --source-root dashboards
```

Validation catches duplicate resources, invalid references, unsupported fields,
and other contract failures before deployment. Use the generated pages for
[Connection](https://leapview.dev/docs/config/connection), [Source](https://leapview.dev/docs/config/source),
[Model](https://leapview.dev/docs/config/model), [SemanticModel](https://leapview.dev/docs/config/semantic-model),
[Pipeline](https://leapview.dev/docs/config/pipeline), and [Dashboard](https://leapview.dev/docs/config/dashboard-document)
field details.
