# Projects and environments

A LeapView Project is a durable deployment namespace, not an authored resource. An instance is permanently bound to one Project UID and environment, such as `dev`, `staging`, or `prod`; activation selects the validated resource generation and managed-data revisions that serve that instance. The compiler produces an unbound bundle from the authored source root without Project or environment identity.

## Project graph

The source root discovers the authored analytical resources from conventional directories:

```text
dashboards/
  connections/*.yaml
  sources/*.yaml
  models/*.yaml
  semantic-models/*.yaml
  pipelines/*.yaml
  dashboards/*.yaml
```

The graph has exactly six authored kinds: `connection`, `source`, `model`, `semantic_model`, `pipeline`, and `dashboard`. Access declarations and publication state are target policy and publication concerns, not additional source-root catalog kinds. Stable IDs make dependencies explicit, so a semantic model can reuse a shared Model or dimension without copying files into another container.

## Environment

An environment is the immutable serving identity of an instance. It is not another resource directory or a request-time project selector. Keep environment-specific secrets, service URLs, storage locations, and active state in the instance configuration; keep business definitions in the shared project tree.

## Bootstrap the target claim

Before staging data or planning a deployment, bootstrap the target using an
instance-administrator credential:

```sh
leapview bootstrap-project https://leapview.example.com --token "$INSTANCE_ADMIN_TOKEN"
```

The CLI persists one opaque Project UID and issuer identity in its existing
local profile document before contacting the target. Repeated bootstrap and
target-profile deletion/recreation reuse that identity. Preserve this local
deployment-authority state when recreating infrastructure; deleting the authority
state itself is not a target reset.

If a deployment authority has already issued a UID, supply it with
`--project-uid` on first use. A different UID cannot replace existing local
authority state. Separate environment targets receive the same UID, but each
keeps its own claim and deployment state.

The existing `login --project-id` flag accepts the same externally issued UID;
it does not select an authored Project or mint identity on the target.

Bootstrap uses the existing singleton claim transaction and records durable
issuer, principal, target, Project, environment, time, and outcome evidence.
Exact retries are idempotent; a different Project or environment cannot retarget
an instance. Project-scoped grants alone do not authorize bootstrap. Ordinary
source planning and data staging require the claim to exist already.

This operation does not create authored Project YAML, a Project registry, or
Project selection/rename APIs. Delivery-plan binding remains a separate boundary.

## Deployment progression

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

See [Project structure](/docs/project-structure) and [Targets and environments](/docs/cli/targets) for the exact source-root workflow and durable target identity.
