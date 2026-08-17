# Add a configuration resource

Configuration changes must update parsing, validation, discovery, compilation, schemas, examples, generated reference, deployment behavior, and compatibility together. A resource is not complete when only one Go struct can decode it.

## Decide ownership and identity

Before implementation, define how the project-wide resource is discovered, its stable `kind` and `metadata.id`/`metadata.name`, which graph resources it may reference, and whether it becomes a securable object or contributes to deployment/serving state.

Prefer extending an existing resource when the new fields share ownership and lifecycle. Create a new kind when it has independent identity, permissions, discovery, or deployment behavior.

## Define the contract

Add the closed resource shape to `internal/project/schema/contracts/contracts.cue` using the standard `apiVersion`, `kind`, `metadata`, and `spec` envelope. Define enums, required fields, nested closed objects, and reference types explicitly.

Field descriptions should explain semantics and failure boundaries, not repeat the field name. Apply defaults in one authoritative layer and reflect them in generated outputs where supported. Do not accept arbitrary maps unless the extension point is intentionally open, such as connector-specific options.

## Implement typed loading and validation

Add typed resource representation in the owning project package. Register include discovery and decoding. Validate:

- duplicate IDs and conflicting discovery;
- project graph ownership;
- references to known resources;
- permitted graph dependencies;
- identifier and enum rules;
- semantic constraints that schema shape alone cannot express;
- deterministic diagnostics with source location/context.

Create valid, minimal, representative, and invalid fixtures. Test unknown fields because closed resources should reject misspellings rather than silently ignore them.

## Compile and activate

Extend project compilation so the resource becomes the intended artifact metadata, access snapshot, runtime definition, or operational state. Test candidate validation and atomic activation. A failed new resource must not partially update active project serving state.

If it creates a securable object, register its parent hierarchy and test authorization does not synthesize missing objects during reads. If it affects data, include it in digest/refresh/lineage behavior as appropriate.

## Generate schema and reference

Add a representative example to the schema generator inputs and register the exported schema/catalog entry. Run:

```sh
task schema:generate
go test ./internal/app/tools/schemadocgen
task docs:generate
task docs:check
```

Confirm the generated page contains the correct title, JSON Schema link, example, fields, nested definitions, and catalog route. Do not edit the generated Markdown to fix a generator omission.

## Update users of the contract

If the resource is exposed through API, CLI, project UI, search, lineage, agent tools, or audit, update the owning source contracts and generated surfaces. Add a complete example under `dashboards/` so CI continually validates the feature in a real project graph.

Write a task-oriented guide when users need design or operations advice beyond the field reference.

## Compatibility

Adding an optional field is usually easier to evolve than changing meaning or identity. For a breaking change, define migration behavior, validation error, release note, and the last version that accepts the old form.

Avoid indefinite aliases. If an alias is necessary, normalize it in one layer, document precedence, warn or report deprecation, test both forms during the migration window, and set a removal plan.

## Final checklist

- Contract source and typed representation agree.
- Invalid/unknown fields fail clearly.
- Discovery and references are deterministic.
- Compilation/activation remains atomic.
- Authorization and audit integration are correct.
- JSON Schema, example, catalog, and docs are generated.
- Existing projects have a migration or remain valid.
- Focused tests and `task ci` pass.
