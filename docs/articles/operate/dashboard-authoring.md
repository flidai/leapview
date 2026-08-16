# Dashboard authoring and promotion

Use this workflow when a dashboard change must move from a safe project draft to a production deployment. It defines the ownership, environment, source-control, and lifecycle boundaries for file, UI, and agent authors. The browser builder, headless authoring API, and dashboard-authoring agent tools all use the same draft, revision, authorization, and promotion rules.

## Before you begin

Choose a permanent development boundary and a production boundary. Use separate LeapView instances for development and production; each instance is permanently bound to one environment and one active project generation. This workflow does not create temporary environments.

| Boundary choice | Isolation | Cost and operations | Decision |
| --- | --- | --- | --- |
| Separate development and production instances | Strongest: distinct instance identities, targets, credentials, storage, and failure domains | Higher infrastructure and operator cost; promotion crosses an explicit target boundary | **Preferred** for production isolation |

Create separate credentials for development and production. A recommended permission model is:

| Principal | Development | Production |
| --- | --- | --- |
| Author | Edit private drafts, preview, and publish on the development instance | No edit, publish, or deploy grant on the production instance |
| QA/verifier | Read and test the development publication with representative grants | Read-only verification when assigned |
| Production deploy service account | No access unless a separate, explicitly scoped account is needed | Deploy the approved full project; no interactive authoring |
| Viewer | Read only | Read only |
| Operator | Administer the instance and recover it | Administer the instance and recover it; use a separately audited identity |

Use distinct development and production service principals. Target identity and credentials provide the first boundary; project-resource grants remain the second.

## Understand ownership and status

Dashboards are project assets whether their visibility is private, restricted, or organization-visible. The interactive builder must select a governed semantic model from that project graph. Semantic-model usage, visibility counts, and publication state remain observable and governable in catalog, audit, and access surfaces.

File-authored, UI-authored, and agent-authored are origins. Origin is provenance, not ownership: it must not decide who can edit, publish, or deploy. Access and RBAC grant those actions to principals over a project and its assets. Anonymous publication is a separate, explicit publication capability; it does not transfer dashboard ownership or authoring rights.

Treat common UI badges as projections of these separate facts:

| Badge or concept | Meaning | Does not mean |
| --- | --- | --- |
| **Private** / **Restricted** / **Organization-visible** | The dashboard's project visibility | A different owner or a different semantic model |
| **Draft** | A mutable project draft that can be edited by an authorized principal | A production deployment or an immutable version |
| **Published** | A selected immutable dashboard revision is available through the project publication path | That the project is deployed to production |
| **Archived** | The dashboard lifecycle no longer accepts normal publication | That its historical revision or deployment evidence was deleted |
| **File** / **UI** / **Agent** | How the current authored content entered LeapView | Who owns the asset or who may deploy it |

The lifecycle layers are intentionally different:

1. A **draft** is the mutable project-owned editing record. Saving or applying edits may point it at a new revision.
2. A **dashboard revision** is an immutable, complete copy of one dashboard document. Publishing selects a revision; it does not mutate one that has already been published.
3. **Compiled serving-state generations** are immutable full-project deployments for an environment. Each includes the compiled project graph, semantic, dashboard, access, policy, connection, and managed-data decisions needed to serve requests.
4. A DuckLake **data snapshot** is an analytical consistency and retention boundary used by runtime queries. It is not a dashboard revision, a draft fork, or a customer-facing dashboard version.

Do not use a data snapshot, virtual schema, or automatic fork of an underlying model as a dashboard authoring mechanism.

## Follow the normal change flow

1. **Fork or copy the published dashboard into a private draft.** Keep the published dashboard stable while the change is being developed.
2. **Iterate in the draft.** UI, file, and agent edits are equivalent origins. Select and reuse the existing governed semantic model for dashboard-only work; do not fork the model automatically.
3. **Validate and preview.** Validate the project and inspect the draft with the author's effective grants. Check query results, filters, empty and failure states, layout, and accessibility. Use the existing [dashboard creation guide](/docs/guides/build/dashboard) for resource details and [develop, review, and publish](/docs/cli/validate-deploy) for CLI gates.
4. **Publish to development.** Publish the draft on the development instance. Have QA exercise the published result with representative viewer and row-policy identities.
5. **Export canonical YAML.** The export is the reviewable project representation. If Git is enabled, commit it to the user's repository and review the change through the user's Git provider. Merge only after the project and dashboard checks pass.
6. **Deploy the full project to production.** A dedicated production, project-scoped deploy service account or CLI deploys the approved merged project. The production deployment is atomic across the resources in that project.

For a dashboard-only change, reuse the already approved semantic model and deploy the dashboard change with that model reference. If model, semantic-model, access-policy, or dashboard changes must move together, validate and QA the complete project in development, then deploy that same full project to production. Do not suggest snapshots, virtual schemas, or automatic model forks to avoid coordinating those changes.

## Use the browser dashboard builder

Open the authenticated edit route:

```text
GET /dashboards/{dashboard}/edit
```

The route requires dashboard edit access. It resolves the repository's current draft pointer and exact retained revision on the server, then streams a typed builder projection. The page shows the governed semantic-model fields, pages, visuals, source origin, visibility, lifecycle, revision number, save state, diagnostics, and source evidence. It never accepts a client-supplied document as authority.

The builder currently exposes four bounded intents:

| Intent | Browser action | Required governed identity |
| --- | --- | --- |
| **Set visibility** | Choose a visibility state | `private`, `restricted`, or `organization-visible` |
| **Add page** | Select **Add page** (including the empty-page state) | The new page ID and optional title; the server allocates missing IDs |
| **Add visual** | Choose a type and select **Add visual** | Target page, visual type, and optional visual/component IDs and title |
| **Assign governed field** | Click **Add** beside a field or drag it onto a visual slot | Target page and visual, semantic field ID, and `measure`, `dimension`, or `detail` role |

Field assignment is validated against the active governed semantic model before the edit is appended. Formatting, filters, interactions, arbitrary YAML patches, and model edits are outside this bounded builder surface; use the project authoring guides for those changes.

Every builder mutation carries the dashboard ID, draft ID, and a complete expected revision token (`revisionId`, `number`, and `contentHash`). The browser supplies a request ID (`X-Request-ID`, with `Idempotency-Key` as a fallback); the authenticated principal is the actor. A successful intent appends one immutable revision, increments the revision number, updates the draft pointer, and leaves an already published revision unchanged. Replaying the same command identity and fingerprint returns the recorded result without appending a second revision. A token that is no longer current returns a stale-revision conflict; reload the current draft and submit the intent again.

### Preview, publish, and export exactly what was reviewed

The **Preview** link identifies one exact draft revision. It includes the draft ID, page ID, revision ID, revision number, and content hash; there is no implicit “latest” revision. The browser route is:

```text
GET /dashboards/{dashboard}/preview?draft={draft}&page={page}&revisionId={id}&revisionNumber={number}&revisionContentHash={hash}
```

Preview compiles that retained document against the active governed runtime and returns the definition, page patch, semantic-model/runtime identity, serving-state ID, and DuckLake snapshot evidence. Preview does not change the draft, publish the dashboard, deploy a serving generation, or mutate data.

**Publish** is a typed command against the same complete expected revision. LeapView compiles the draft with its governed semantic model, stores the compiled revision and publication evidence, and moves the dashboard lifecycle to `published`. Publishing does not deploy a full project to production; use the development publication and full-project promotion gates described above.

**Export YAML** downloads the canonical authored source without changing lifecycle state:

```text
GET /dashboards/{dashboard}/export.yaml
```

The export is reviewable project YAML. A retained project source resolves the current lifecycle draft by its dashboard ID; an active project source is the serving artifact with serving-state/path evidence. Neither export turns a compiled artifact into a fabricated authoring revision.

## Use the headless dashboard-authoring API

The generated API is rooted at `/api/v1/projects/{project}/authoring`. Authenticate with a scoped principal and use IDs returned by the catalog; do not infer identity from titles. The generated OpenAPI contract is authoritative for field types and status codes.

| Method and path | Purpose and access |
| --- | --- |
| `GET /api/v1/projects/{project}/authoring/catalog` | List governed dashboard identities (`RESOURCE_READ`). |
| `GET /api/v1/projects/{project}/authoring/dashboards/{dashboard}` | Read one dashboard summary (`RESOURCE_READ`). |
| `GET /api/v1/projects/{project}/authoring/dashboards/{dashboard}/draft` | Read the current private draft, lifecycle pointer, document, and exact revision (`RESOURCE_EDIT`). |
| `GET /api/v1/projects/{project}/authoring/dashboards/{dashboard}/drafts/{draft}/revisions/{revision}` | Read that exact current draft revision (`RESOURCE_EDIT`); the draft and revision path values must match. |
| `GET /api/v1/projects/{project}/authoring/dashboards/{dashboard}/revisions/{revision}` | Read that exact published revision (`RESOURCE_READ`); this path never means “latest draft.” |
| `POST /api/v1/projects/{project}/authoring/drafts` | Create one named private draft. |
| `POST /api/v1/projects/{project}/authoring/commands` | Apply one closed command: one builder intent, `publish`, or `archive`. |
| `POST /api/v1/projects/{project}/authoring/forks` | Fork a retained project source into a new private draft. |
| `POST /api/v1/projects/{project}/authoring/dashboards/{dashboard}/drafts/{draft}/preview` | Preview one exact revision and page. |
| `GET /api/v1/projects/{project}/authoring/sources/{kind}/{dashboard}/export` | Export canonical YAML for the `project` source kind. |

`POST /drafts`, `POST /commands`, and `POST /forks` require an `Idempotency-Key` header (1–200 characters). The key is an operation-idempotency identity, separate from actor and tool-call provenance, and is audited with the authenticated actor; it is not a repository or Git authority. For create and fork, the same key with the same normalized payload durably replays the original draft/result; the same key with a changed payload is a conflict; a different key creates a new draft. Command retries likewise replay only when the same key is reused with the same request fingerprint. Preview, reads, and export do not require an idempotency key.

Command requests must name the exact `dashboardId`, `draftId`, and `expectedRevision` token:

```json
{
  "dashboardId": "revenue",
  "draftId": "draft-7",
  "expectedRevision": {
    "revisionId": "revision-12",
    "number": 12,
    "contentHash": "sha256:..."
  },
  "addVisual": {
    "pageId": "overview",
    "type": "bar",
    "title": "Revenue by month"
  }
}
```

The union accepts exactly one of `setVisibility`, `addPage`, `addVisual`, `assignField`, `publish`, or `archive`. Every successful edit returns the new immutable revision token and the repository-authoritative lifecycle pointer. A stale token is a `409` conflict; read the current draft and reconcile rather than dropping the token or defaulting to a newer revision. Preview requests repeat the complete token in their body, and responses include the same revision plus runtime and snapshot evidence.

## Use dashboard-authoring agent tools

The dashboard-authoring subset of built-in chat and MCP contains these twelve tools:

```text
list_dashboards
get_dashboard
get_dashboard_draft
create_dashboard_draft
execute_dashboard_command
fork_dashboard
preview_dashboard_draft
export_dashboard_yaml
set_dashboard_visibility
add_dashboard_page
add_dashboard_visual
assign_dashboard_field
```

The first three read the governed catalog or exact private draft. `create_dashboard_draft` and `fork_dashboard` create private drafts. The four intent tools apply the same visibility, add-page, add-visual, and assign-field union used by the browser. `execute_dashboard_command` is reserved for exact-revision `publish` and `archive`; `preview_dashboard_draft` requires an exact revision and page; and `export_dashboard_yaml` returns canonical YAML. Catalog, query, and documentation tools remain separate integration surfaces.

Agent inputs deliberately omit actor and provenance fields. The server binds `origin: agent`, the authenticated principal as `actorId`, the active conversation as `conversationId`, and the tool invocation ID as `toolCallId`; a model cannot spoof any of them. Agent calls use the invocation ID for both their operation idempotency key and actual tool-call identity. Agent tools enforce the project scope, object privilege, governed semantic-model check, and exact revision token before applying a mutation. Create and fork replay a durable result for the same invocation ID and normalized payload, conflict on a changed payload, and create a new draft for a different invocation ID; intent and lifecycle command retries follow command-ID/fingerprint replay rules. MCP writes are advertised as non-idempotent because MCP assigns a fresh server tool-call ID for each request; durable replay requires an agent caller to reuse the original invocation identity.

## Choose a create, fork, or promotion flow

| Situation | Recommended flow |
| --- | --- |
| New dashboard with an existing governed semantic model | Create a private draft in the active project, use the four bounded intents, preview the exact latest revision, publish on the development instance, then export and promote the full project. |
| Change to an existing published project dashboard | Fork the exact published project revision into a new private draft; keep the source unchanged, iterate, preview, publish in development, and promote the reviewed project. |
| Only an active project artifact is available | Fork the project source into a private draft. Record its serving-state/path evidence, understand that it has no authoring revision token, and continue editing the retained project source when one is available. |
| Dashboard and model, policy, or semantic changes must move together | Validate and QA one complete project on the development instance, export the canonical YAML, and deploy that same full project atomically to the production instance. |

The create/fork operation never deploys, publishes a serving generation, mutates the semantic model, creates a data snapshot, or makes a temporary schema. Git is an optional review and evidence integration after export; LeapView has no native Git integration and no repository authority. The server remains authoritative for lifecycle, authorization, revisions, and deployment targets.

## Use Git when it helps review

Git is highly recommended, but opt-in. LeapView has no native Git integration, pull-request workflow, or merge automation. A mature integration exports canonical YAML, lets the user's Git provider handle branches and review, and invokes LeapView's CLI or API from a dedicated project-scoped deploy service account after merge.

Repository, ref, and commit metadata may be recorded as evidence and provenance. They are not a server-enforced “managed by Git” authority: the server still validates the project, authorization, candidate, and deployment target. Without Git, use the same draft, preview, development publication, and full-project production gates with an explicitly authorized publisher or service account.

## Validate the promotion

Before production deployment, confirm that:

- the draft and published revision belong to the intended project and use the intended governed semantic model;
- the development and production instances are separate, permanent environment boundaries;
- dashboard visibility (`private`, `restricted`, or `organization-visible`), access grants, row and column policies, and anonymous publication settings are intentional;
- QA verified the development publication with representative viewer identities;
- the exported YAML, source revision (when Git is used), plan, and deploy service-account identity are retained as evidence; and
- coordinated model, semantic, policy, and dashboard changes are present in one full-project candidate.

## Verify the result

After production activation, verify the serving-state generation and exercise one representative dashboard page, filter path, and table or query window. Check that the expected dashboard revision is published, visibility counts remain visible to authorized operators, and access policies still hold for viewers. A failed validation or activation leaves the previous valid generation serving; use the governed rollback operation rather than editing active files or deleting data snapshots.

## Troubleshooting

| Symptom | Corrective action |
| --- | --- |
| An author can see or edit production assets | Remove the author's production-instance role or credential; do not rely on naming alone. |
| A draft edit changes the published dashboard | Confirm the author copied the published asset into a private draft and published a new revision only after review. |
| Git metadata is present but the server accepts an unintended source | Review the canonical YAML, candidate digest, target identity, and permissions. Repository/ref metadata is evidence, not authority. |
| A model and dashboard disagree after promotion | Treat them as one coordinated full-project change, revalidate in development, and redeploy the complete candidate. |
| A data snapshot is mistaken for a dashboard version | Inspect the dashboard revision and serving-state generation separately; snapshots only describe analytical runtime consistency. |

## Next steps

Read [Projects and environments](/docs/concepts/projects-environments) for resource boundaries, [API conventions](/docs/guides/integrate/api-conventions) for headless retry and error handling, [Use the agent tool catalog](/docs/guides/integrate/agent-tools) for agent transport details, [Targets and environments](/docs/cli/targets) for target safeguards, and [Dashboard authoring patterns](/docs/guides/build/patterns) for maintainable dashboard structure.
