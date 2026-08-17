# Project structure

A LeapView project is one project-wide resource graph. Connections and sources provide governed inputs; model tables, semantic models, pipelines, dashboards, and access resources are discovered from the same project manifest and can reference one another by stable IDs.

```text
dashboards/
  leapview.yaml
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
  access/
    sales-analysts.yaml
```

Directory names are conventions. The include lists in `leapview.yaml` define which files belong to a project deployment, so a resource is discovered exactly once by its project.

## Project entry point

The project manifest is the root of configuration discovery:

```yaml
apiVersion: leapview.dev/v1
kind: Project
metadata:
  id: project:commerce
  name: commerce
spec:
  connections:
    include: [connections/*.yaml]
  sources:
    include: [sources/*.yaml]
  models:
    include: [models/*.yaml]
  semanticModels:
    include: [semantic-models/*.yaml]
  pipelines:
    include: [pipelines/*.yaml]
  dashboards:
    include: [dashboards/*.yaml]
  access:
    include: [access/*.yaml]
  publications:
    include: [publications/*.yaml]
```

Paths are resolved relative to the project manifest. Keep include patterns narrow enough that ownership remains obvious; a resource should not be discovered twice.

## Resource layers

- **Connections** define how LeapView reaches physical data.
- **Sources** use a connection and provide stable logical names, paths, and field definitions.
- **Model tables** transform permitted sources into reusable analytical tables.
- **Semantic models** define dimensions, metrics, and relationships across model tables. Shared dimensions can serve multiple semantic consumers in the same graph.
- **Pipelines** describe refresh triggers for semantic models.
- **Dashboards** compose semantic queries into filters, visuals, tables, pages, and layout.
- **Access and publication resources** govern project resources and public delivery without creating another resource container.

Managed-data planning and revision activation also operate at project scope. A deployment can therefore pin a consistent set of shared input revisions while changing several dependent resources atomically.

## Resource identity and metadata

Every resource uses the same envelope: `apiVersion`, `kind`, `metadata`, and `spec`. `metadata.id` is the explicit immutable graph identity; `metadata.name` is the stable project-local name. `displayName`, `description`, `owner`, and `tags` communicate intent without changing identity. A workspace container or `metadata.workspace` field is not part of the accepted project contract.

Use stable names and IDs, and avoid encoding environment names in them. The removed `workspace`/`workspaces` containers and `metadata.workspace` field are rejected by project validation; deploy the same project source to separate dev, staging, and production targets instead of creating parallel resource trees.

## Validate discovery

Validate from the project root after moving files or changing include patterns:

```sh
go run ./cmd/leapview validate --project dashboards/leapview.yaml
```

Validation catches duplicate resources, missing includes, invalid references, unsupported fields, and other contract failures before deployment. The generated [Project configuration](/docs/config/project) page is the source of truth for exact fields.
