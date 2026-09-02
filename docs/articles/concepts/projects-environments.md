# Projects and environments

A LeapView analytics source root compiles into an atomic configuration graph. An instance is permanently bound to one environment, such as `dev`, `staging`, or `prod`; activation selects the validated generation and managed-data revisions that serve that instance.

## Project graph

The source root discovers exactly six public authored kinds from fixed directories: connection, source, model, semantic model, pipeline, and dashboard. A synthesized internal root can preserve graph compatibility, but it is not an authored resource or public identity. Groups, grants, role bindings, and publication state belong to authenticated instance control-plane workflows. Stable resource IDs make dependencies explicit, so a semantic model can reuse a shared model table or dimension without copying files into another container.

## Environment

An environment is the immutable serving identity of an instance. It is not another resource directory or a request-time project selector. Keep environment-specific secrets, service URLs, storage locations, and active state in the instance configuration; keep business definitions in the shared project tree.

The standard progression is:

1. Validate the same project source locally.
2. Plan against the target instance's active deployment.
3. Review the graph and data-revision changes.
4. Deploy the candidate to that instance; the CLI asserts its bound environment.
5. Verify the resulting active state before promoting the same revision onward.

## Atomic delivery

LeapView builds and validates the complete project graph before activation. Activation switches serving pointers only after the candidate is acceptable, so a failed candidate does not partially update dependent resources. Managed-data revisions follow the same principle: deployment activates the reviewed combination of immutable revisions and project definitions.

## Choosing boundaries

Ask these questions when organizing a repository:

- Is this input shared and governed? Define it as a project connection or source.
- Do these dashboards share semantic definitions and access rules? Keep them in one project graph and reuse stable model and semantic IDs.
- Does only infrastructure or serving state differ? Use separate environment targets, not copied YAML trees.
- Must several changes become visible together? Deliver them in one project deployment.

See the generated [configuration reference](/docs/config/connection) and [Targets and environments](/docs/cli/targets) for the exact contracts and workflow.
