# Use the agent tool catalog

LeapView exposes one governed tool catalog through built-in chat and the deployment's MCP endpoint. Catalog, query, and documentation tools are read-only; dashboard authoring adds a bounded twelve-tool surface for private drafts and exact-revision commands. Use catalog tools to identify governed resources before calling a query tool, and use documentation tools when the question is about LeapView itself.

## Choose a tool

| Need | Tool |
| --- | --- |
| Find a resource without knowing its workspace or parent | `catalog_search` |
| Browse the known catalog hierarchy one level at a time | `catalog_list` |
| Inspect one exact resource definition | `catalog_get` |
| Query a semantic model | `query_semantic_model` |
| Query an existing dashboard visual | `query_dashboard_visual` |
| Create a temporary visualization from semantic fields | `query_visual` |
| Find version-matched LeapView documentation | `docs_search` |
| Read a document returned by documentation search | `docs_read` |

The discovery/query/documentation subset does not expose connections, raw sources, model tables, refresh runs, lineage, raw SQL, previews, explanations, filter-value enumeration, page-wide queries, or mutation operations. Dashboard authoring is a separate governed product surface; use the authoring tools or generated headless authoring API when creating a private draft, applying one of its four intents, previewing an exact revision, publishing, forking, or exporting YAML.

## Dashboard authoring tools

The dashboard-authoring subset contains exactly these twelve names:

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

`list_dashboards`, `get_dashboard`, and `get_dashboard_draft` read governed dashboard state. `create_dashboard_draft` creates a new private draft. `set_dashboard_visibility`, `add_dashboard_page`, `add_dashboard_visual`, and `assign_dashboard_field` are the four bounded builder intents; each requires an exact draft revision token. `execute_dashboard_command` accepts exact-revision `publish` and `archive`, `fork_dashboard` copies a retained workspace or project source into a new private draft, `preview_dashboard_draft` previews one exact page and revision, and `export_dashboard_yaml` returns canonical authored YAML.

The model cannot supply identity or provenance fields. The server binds `origin: agent`, the authenticated principal as `actorId`, the active conversation as `conversationId`, and the tool invocation as `toolCallId`. Workspace scope, object privileges, semantic-model governance, and optimistic revision checks run before every mutation. Agent calls set the operation idempotency key and actual `toolCallId` from the invocation ID. Create and fork calls with the same invocation ID and normalized payload replay their durable draft/result; a changed payload conflicts, while a different invocation ID creates a new draft. Intent and lifecycle-command retries use the same command identity and request fingerprint replay rules. MCP writes are deliberately advertised as non-idempotent because the server assigns a fresh tool-call ID to each request; durable replay applies only when an agent caller reuses the original invocation identity. See [Dashboard authoring and promotion](/docs/guides/operate/dashboard-authoring) for browser, API, and promotion details.

## Identify resources with refs

Every catalog resource is identified by a closed `CatalogRef`:

```json
{
  "workspaceId": "sales",
  "type": "dashboard",
  "id": "executive-sales"
}
```

Supported `type` values are `workspace`, `dashboard`, `page`, `visual`, `filter`, `semantic_model`, `semantic_table`, `field`, and `measure`.

Treat the complete ref as the resource identity. IDs are meaningful only with their workspace and type, and some returned IDs encode their parent identity. Pass returned refs unchanged instead of constructing IDs from names.

Search and list results use the same compact item envelope. It contains the ref, display name, optional description, workspace, ancestors, known dashboard/page locations, browser URL, and the next tools that can act on the item.

## Find an unknown resource

`catalog_search` searches all workspaces the principal may access. It does not require a preceding workspace-list call.

```json
{
  "query": "monthly revenue",
  "types": ["dashboard", "visual", "measure"],
  "workspaceIds": ["sales"],
  "context": {
    "dashboardId": "executive-sales",
    "pageId": "overview"
  },
  "limit": 10
}
```

Only `query` is required. Type and workspace filters constrain the search; optional dashboard/page context influences ranking without changing authorization. The default limit is 10 and the maximum is 25.

Every search page includes `count` and `hasMore`. Use `nextCursor` unchanged when `hasMore` is true. Cursors are opaque and bound to the search, caller, and catalog snapshot. Restart from the first page if the catalog changed.

## Browse a known hierarchy

Call `catalog_list` without a parent to list accessible workspaces. Pass a returned ref as `parent` to browse exactly one level:

| Parent | Children |
| --- | --- |
| none | workspaces |
| workspace | dashboards and semantic models |
| dashboard | pages |
| page | visuals and filters |
| semantic model | semantic tables, conformed-dimension fields, and measures |
| semantic table | fields |

```json
{
  "parent": {
    "workspaceId": "sales",
    "type": "dashboard",
    "id": "executive-sales"
  },
  "childTypes": ["page"],
  "limit": 25
}
```

`childTypes` is optional, but every requested type must be valid for the parent. Results are deterministically ordered. The default limit is 25 and the maximum is 50. Every page includes `count` and `hasMore`; pass `nextCursor` back as `cursor` when `hasMore` is true. Do not parse or edit the cursor.

Listing an exact parent first resolves and authorizes that parent. Search and list silently omit inaccessible results.

## Inspect an exact definition

Pass a returned ref to `catalog_get`:

```json
{
  "ref": {
    "workspaceId": "sales",
    "type": "semantic_model",
    "id": "commerce"
  }
}
```

The result combines the normalized item envelope with type-specific `details`:

- workspaces include metadata and the active serving identity;
- dashboards include their semantic-model ref and page, visual, and filter counts;
- pages include their components;
- visuals include the compiled definition, query fields, columns, and placement;
- filters include their field, configuration, and placement;
- semantic models separately count tables, physical fields, conformed dimensions, facts, atomic measures, metrics, and relationships; they also include relationship definitions and authorized dashboard usage;
- semantic tables include source, grain, keys, fact/dimension roles, and counts;
- model-level fields include their bindings to physical fields and relationship paths;
- table fields include their expression, source field, key status, grain, and nullability when known;
- atomic measures include their fact ref, aggregation, input, filters, empty-result behavior, and field dependencies;
- derived metrics include their expression and measure dependencies.

The result is a compact domain projection, not the raw deployed asset payload.

A visual or filter can appear on more than one page. If `catalog_get` returns `catalog_location_required`, choose one of the item's returned locations and retry:

```json
{
  "ref": {
    "workspaceId": "sales",
    "type": "visual",
    "id": "executive-sales.revenue-by-month"
  },
  "location": {
    "dashboardId": "executive-sales",
    "pageId": "overview"
  }
}
```

Returned locations include dashboard and page names plus a browser `href`; the retry input intentionally needs only `dashboardId` and `pageId`.

## Query governed data

Use the capabilities returned with a catalog item to choose the next tool.

- Use `query_semantic_model` with a semantic-model ref and the field and measure IDs discovered through catalog browsing. It returns governed row data and supports bounded pagination. Agent calls default to 25 rows and accept at most 50 rows per page even though the corresponding REST operation supports larger application-oriented pages.
- Use `query_dashboard_visual` with the workspace, dashboard, page, and visual location of an existing visual. It preserves the dashboard definition, filters, authorization, and data-policy boundary. The agent receives a compact analytical rowset—not the renderer envelope—with the visual title/type, semantic columns, normalized applied filters, status and diagnostics, cardinality/completeness, query provenance, and freshness. Calls return at most 50 rows per agent page; follow `nextCursor` when `hasMore` is true.
- Use `query_visual` when no saved visual fits. Provide a workspace, semantic model, dataset, visual type, semantic fields, and optional governed semantic filters. Inline data and arbitrary expressions are rejected. Built-in chat and MCP receive the same compact generated result: field and filter refs, units and formats when defined, row completeness, status and diagnostics, query provenance, freshness, and the display signal. LeapView retains the renderer-independent visualization artifact only as display content; the call does not save or mutate the dashboard.

Semantic query rows are positional. Read each cell using the column at the same index. Precision-sensitive numbers remain strings, while SQL `NULL` is JSON `null` and is distinct from a genuine empty string. Column descriptors identify the governed field or measure ref, label, semantic kind, data type, nullability, unit, and format when defined; these descriptors come from the semantic model and table schema rather than values sampled from the current page.

Each semantic result includes `queryId`, `servingSnapshot`, and `completeness.returnedRows`/`hasMore`. When a successful data version is recorded, `freshness` also includes `lastSuccessfulRefreshAt`, DuckLake snapshot ID, serving-state ID, publish-or-refresh source, and whether that version matches the queried serving snapshot. Refresh time is deliberately not called “data as of”: it does not prove the upstream event-time watermark.

The exact contracts are versioned with the running LeapView release. Use the generated [Agent tool reference](/docs/agent-tools) for readable input and output schemas, or download the [machine-readable manifest](/docs/agent-tools/manifest.json). You can also inspect the matching local release with:

```sh
leapview agent tools
```

For MCP clients, the same schemas are returned by `tools/list`. Do not cache schemas across a LeapView upgrade without rediscovering the catalog.

## Read product documentation

Use `docs_search` for product behavior, configuration, CLI commands, API operations, and visual support:

```json
{
  "query": "semantic model relationships",
  "path": "concepts",
  "limit": 8
}
```

Search pages include `count` and `hasMore`. When `hasMore` is true, pass the returned opaque `nextCursor` as `cursor` with the same query and path. Documentation cursors are bound to the embedded release snapshot; restart the search if the documentation changed.

Pass a returned `doc:` ID unchanged to `docs_read`:

```json
{
  "id": "doc:concepts/semantic-models",
  "offset": 1,
  "limit": 200
}
```

Reads are line- and byte-bounded. Continue from `nextOffset` only when the current window is insufficient. These tools read the immutable documentation embedded in the running release; they cannot read arbitrary deployment files.

## Handle authorization and errors

Catalog, query, and documentation tools are read-only, idempotent, and non-destructive. Dashboard-authoring read tools are also non-mutating; its create, intent, publish, archive, and fork tools are governed writes. MCP access requires `USE_AGENT`. Catalog operations require `VIEW_ITEM`; data-query tools require `QUERY_DATA`; dashboard authoring checks `EDIT_ITEM` for draft edits and `MANAGE_ITEM` for publish/archive while continuing to enforce workspace grants and data policies.

Catalog lookup deliberately does not reveal inaccessible resources:

| Code | Meaning | Recovery |
| --- | --- | --- |
| `invalid_arguments` | The ref, child relationship, limit, filter, or cursor is invalid. | Correct the request using the discovered schema. |
| `catalog_not_found` | The resource is missing or inaccessible. | Search or list again with the current principal; do not infer which case occurred. |
| `catalog_location_required` | A shared visual or filter needs an exact dashboard/page location. | Retry with one of the returned locations. |
| `catalog_snapshot_changed` | The catalog changed during cursor pagination. | Restart the search or list from its first page. |

Expected tool failures are returned as tool errors. Transport and protocol failures are separate from resource and query errors.

Tool providers calculate every semantic page before it reaches the agent harness. The harness does not shorten arrays, strings, or nested objects after a tool returns because doing so could make a cursor skip unseen rows. If a provider unexpectedly exceeds the absolute model-result ceiling, the call fails transparently with `tool_output_contract_violation`, including the actual and maximum byte counts; no partial result is presented as successful.

## Verify the exposed surface

Run `leapview agent tools` against the same release used by a deployment. The dashboard-authoring subset in built-in chat and deployment MCP `tools/list` must expose exactly:

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

The same names, schemas, and server-bound provenance are used by built-in chat and MCP. Discover the catalog for the other read/query/documentation tools separately; do not infer authoring tool arguments from a model response or cache schemas across a release change.
