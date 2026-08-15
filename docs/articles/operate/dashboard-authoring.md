# Dashboard authoring and promotion

Use this workflow when a dashboard change must move from a safe workspace draft to a production deployment. It defines the ownership, environment, source-control, and lifecycle boundaries for file, UI, and agent authors.

## Before you begin

Choose a permanent development boundary and a production boundary. The preferred topology is one LeapView instance for development and another for production. A lower-cost topology keeps both in one instance, but uses permanent `<workspace>-dev` workspaces alongside production workspaces. In that topology, workspace grants and service-principal scopes must prevent ordinary users and service accounts from crossing the boundary. This workflow does not create temporary environments.

| Boundary choice | Isolation | Cost and operations | Decision |
| --- | --- | --- | --- |
| Separate development and production instances | Strongest: distinct instance identities, targets, credentials, storage, and failure domains | Higher infrastructure and operator cost; promotion crosses an explicit target boundary | **Preferred** for production isolation |
| One instance with permanent `<workspace>-dev` and production workspaces | Workspace RBAC and service-principal scopes must enforce the boundary inside shared infrastructure | Lower cost and simpler infrastructure; grants, names, and target checks need tighter review | Supported lower-cost option |

Create separate credentials for development and production. A recommended permission model is:

| Principal | Development | Production |
| --- | --- | --- |
| Author | Edit private drafts, preview, and publish to the development workspace | No edit, publish, or deploy grant |
| QA/verifier | Read and test the development publication with representative grants | Read-only verification when assigned |
| Production deploy service account | No access unless a separate, explicitly scoped account is needed | Deploy the approved full project; no interactive authoring |
| Viewer | Read only | Read only |
| Operator | Administer the instance and recover it | Administer the instance and recover it; use a separately audited identity |

Use distinct development and production service principals even when both boundaries share an instance. In a separate-instance topology, target identity and credentials provide the first boundary; workspace grants remain the second.

## Understand ownership and status

Dashboards are workspace assets whether their visibility is private or shared. The interactive builder must select a governed semantic model from that workspace. Semantic-model usage, private/shared counts, and publication state remain observable and governable in catalog, audit, and access surfaces.

File-authored, UI-authored, and agent-authored are origins. Origin is provenance, not ownership: it must not decide who can edit, publish, or deploy. Access and RBAC grant those actions to principals over a workspace and its assets. Anonymous publication is a separate, explicit publication capability; it does not transfer dashboard ownership or authoring rights.

Treat common UI badges as projections of these separate facts:

| Badge or concept | Meaning | Does not mean |
| --- | --- | --- |
| **Private** / **Shared** | The dashboard's workspace visibility | A different owner or a different semantic model |
| **Draft** | A mutable workspace draft that can be edited by an authorized principal | A production deployment or an immutable version |
| **Published** | A selected immutable dashboard revision is available through the workspace publication path | That the project is deployed to production |
| **Archived** | The dashboard lifecycle no longer accepts normal publication | That its historical revision or deployment evidence was deleted |
| **File** / **UI** / **Agent** | How the current authored content entered LeapView | Who owns the asset or who may deploy it |

The lifecycle layers are intentionally different:

1. A **draft** is the mutable workspace-owned editing record. Saving or applying edits may point it at a new revision.
2. A **dashboard revision** is an immutable, complete copy of one dashboard document. Publishing selects a revision; it does not mutate one that has already been published.
3. **Compiled serving-state generations** are immutable full-project deployments for an environment. Each includes the compiled workspace, semantic, dashboard, access, policy, connection, and managed-data decisions needed to serve requests.
4. A DuckLake **data snapshot** is an analytical consistency and retention boundary used by runtime queries. It is not a dashboard revision, a draft fork, or a customer-facing dashboard version.

Do not use a data snapshot, virtual schema, or automatic fork of an underlying model as a dashboard authoring mechanism.

## Follow the normal change flow

1. **Fork or copy the published dashboard into a private draft.** Keep the published dashboard stable while the change is being developed.
2. **Iterate in the draft.** UI, file, and agent edits are equivalent origins. Select and reuse the existing governed semantic model for dashboard-only work; do not fork the model automatically.
3. **Validate and preview.** Validate the project and inspect the draft with the author's effective grants. Check query results, filters, empty and failure states, layout, and accessibility. Use the existing [dashboard creation guide](/docs/guides/build/dashboard) for resource details and [develop, review, and publish](/docs/cli/validate-deploy) for CLI gates.
4. **Publish to development.** Publish the draft to the permanent development instance or `<workspace>-dev` workspace. Have QA exercise the published result with representative viewer and row-policy identities.
5. **Export canonical YAML.** The export is the reviewable project representation. If Git is enabled, commit it to the user's repository and review the change through the user's Git provider. Merge only after the project and dashboard checks pass.
6. **Deploy the full project to production.** A dedicated production, workspace-scoped deploy service account or CLI deploys the approved merged project. The production deployment is atomic across the resources in that project.

For a dashboard-only change, reuse the already approved semantic model and deploy the dashboard change with that model reference. If model, semantic-model, access-policy, or dashboard changes must move together, validate and QA the complete project in development, then deploy that same full project to production. Do not suggest snapshots, virtual schemas, or automatic model forks to avoid coordinating those changes.

## Use Git when it helps review

Git is highly recommended, but opt-in. LeapView has no native Git integration, pull-request workflow, or merge automation. A mature integration exports canonical YAML, lets the user's Git provider handle branches and review, and invokes LeapView's CLI or API from a dedicated workspace-scoped deploy service account after merge.

Repository, ref, and commit metadata may be recorded as evidence and provenance. They are not a server-enforced “managed by Git” authority: the server still validates the project, authorization, candidate, and deployment target. Without Git, use the same draft, preview, development publication, and full-project production gates with an explicitly authorized publisher or service account.

## Validate the promotion

Before production deployment, confirm that:

- the draft and published revision belong to the intended workspace and use the intended governed semantic model;
- the development target or `<workspace>-dev` boundary is permanent and the production boundary is explicit;
- private/shared visibility, access grants, row and column policies, and anonymous publication settings are intentional;
- QA verified the development publication with representative viewer identities;
- the exported YAML, source revision (when Git is used), plan, and deploy service-account identity are retained as evidence; and
- coordinated model, semantic, policy, and dashboard changes are present in one full-project candidate.

## Verify the result

After production activation, verify the serving-state generation and exercise one representative dashboard page, filter path, and table or query window. Check that the expected dashboard revision is published, private/shared counts remain visible to authorized operators, and access policies still hold for viewers. A failed validation or activation leaves the previous valid generation serving; use the governed rollback operation rather than editing active files or deleting data snapshots.

## Troubleshooting

| Symptom | Corrective action |
| --- | --- |
| An author can see or edit production assets | Remove the cross-boundary workspace or environment grant; do not rely on naming alone. |
| A draft edit changes the published dashboard | Confirm the author copied the published asset into a private draft and published a new revision only after review. |
| Git metadata is present but the server accepts an unintended source | Review the canonical YAML, candidate digest, target identity, and permissions. Repository/ref metadata is evidence, not authority. |
| A model and dashboard disagree after promotion | Treat them as one coordinated full-project change, revalidate in development, and redeploy the complete candidate. |
| A data snapshot is mistaken for a dashboard version | Inspect the dashboard revision and serving-state generation separately; snapshots only describe analytical runtime consistency. |

## Next steps

Read [Projects, workspaces, and environments](/docs/concepts/projects-workspaces-environments) for resource boundaries, [Targets and environments](/docs/cli/targets) for target safeguards, and [Dashboard authoring patterns](/docs/guides/build/patterns) for maintainable dashboard structure.
