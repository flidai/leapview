# Agent integrations

LeapView conversations are global and owned by the authenticated principal. Project resources are governed catalog assets: a resource-aware tool uses an exact `{kind,id}` reference, then enforces the principal's privileges, any REST credential restrictions, data policies, and the governed query layer.

## Curated tool catalog

Built-in chat, MCP discovery, and `leapview agent tools` expose one governed catalog. Its discovery, query, and documentation subset remains read-only; the dashboard-authoring subset adds the twelve bounded authoring tools documented in [Dashboard authoring and promotion](/docs/guides/operate/dashboard-authoring).

- `catalog_search` searches every authorized project resource when a resource's location is unknown.
- `catalog_list` browses one deterministic hierarchy level. Omit `parent` to list authorized projects, then pass a returned `{kind,id}` ref to continue.
- `catalog_get` returns the compact definition for one exact ref. Shared visuals and filters may require one of the returned dashboard/page locations.
- `query_semantic_model` executes governed semantic queries.
- `query_dashboard_visual` queries one existing dashboard visual.
- `query_visual` creates a read-only visualization from governed semantic fields.
- `docs_search` and `docs_read` search and read the version-matched product documentation.

Catalog search and list silently omit inaccessible resources. Exact lookup returns the same not-found result for missing and inaccessible refs. These discovery/query/documentation tools are read-only, idempotent, non-destructive, and closed-world. Dashboard authoring is the explicit exception: its twelve tools can create private drafts and apply the four bounded intents or lifecycle commands, while still enforcing project-resource grants, governed fields, exact revisions, and no access to connections, raw sources, lineage, refresh runs, raw SQL, credentials, semantic-model mutation, or data mutation.

See [Use the agent tool catalog](/docs/guides/integrate/agent-tools) for refs, hierarchy, pagination, shared-resource locations, tool-selection guidance, and stable error behavior. Use the generated [Agent tool reference](/docs/agent-tools) for exact schemas and metadata.

## Product documentation tools

The built-in agent and deployment MCP catalog expose `docs_search` and `docs_read`. Documentation search returns ranked, bounded matches from the immutable documentation index embedded in the running LeapView release. Each page reports `count` and `hasMore`; continue with its opaque, snapshot-bound `nextCursor` when needed. Each match includes a stable `doc:` ID, documentation path, public URL, summary, and focused excerpt.

Pass a returned ID to `docs_read`. Reads are line- and byte-bounded and return `nextOffset` when more content remains. Continue from that offset only when the current window is insufficient. The tools can read authored guides and generated CLI, API, configuration, and visual references, but cannot access arbitrary deployment files or execute documented operations.

## Configure the built-in model provider

The built-in chat surface uses an OpenAI-compatible provider configuration:

```sh
LEAPVIEW_AGENT_BASE_URL=https://api.openai.com/v1
LEAPVIEW_AGENT_MODEL=<model-id>
LEAPVIEW_AGENT_API_KEY=<secret>
```

Store the API key in the deployment secret manager. The global administrator-controlled system prompt is configured in the agent administration page. Provider prompts and responses may contain business context; review the provider's data handling, retention, regional, and contractual requirements before enabling it.

The MCP endpoint does not depend on this provider configuration. External MCP hosts can use LeapView tools when the built-in model is disabled.

## Ask through the CLI

```sh
leapview agent ask \
  --target "$LEAPVIEW_TARGET" \
  --token "$LEAPVIEW_API_TOKEN" \
  "Which categories contributed most to revenue in the sales project?"
```

Use `--conversation <id>` to continue an existing principal-owned conversation and `--json` for machine processing. List conversations with bounded pagination through `leapview agent conversations`. The CLI follows the asynchronous run to a terminal state.

## Integrate through REST

The generated [Agent API](/docs/api/agent) is rooted at `/api/v1/agent` and exposes global conversation creation, update, archive, messages, runs, and run events. The removed `/api/v1/workspaces/{workspace}/agent` routes have no compatibility aliases.

A typical client creates or selects a conversation, starts a run, records its identity, follows the run/event surface to a documented terminal state, renders the assistant message and tool evidence, and archives conversations according to retention policy. List endpoints use opaque pagination tokens.

## Integrate through MCP

Set `LEAPVIEW_PUBLIC_URL` to the deployment's canonical HTTPS origin, then give an MCP host such as Claude the deployment-specific URL `${LEAPVIEW_PUBLIC_URL}/mcp`. The host discovers authorization automatically and opens LeapView's sign-in and consent flow. LeapView implements Streamable HTTP 2025-11-25 with stateless JSON responses and exposes tools only—no resources, prompts, nested conversation tools, or stdio transport.

MCP and built-in chat consume the same catalog, schemas, handlers, authorization, projections, audit path, and execution errors. Successful tool calls return both `structuredContent` and equivalent JSON text. MCP access requires `USE_AGENT`; catalog operations additionally require `VIEW_ITEM`, while data tools require `QUERY_DATA`. Rediscover tools after a LeapView upgrade instead of caching schemas across releases.

For dashboard authoring, actor and provenance are server-bound. Tool inputs do not accept an actor, conversation, or tool-call identity: the server records the authenticated principal as `actorId`, the active conversation as `conversationId`, the invocation identity as `toolCallId`, and `origin: agent`. See the exact tool names and create/fork/promotion flow in [Dashboard authoring and promotion](/docs/guides/operate/dashboard-authoring).

By default, LeapView is the MCP authorization server. It supports authorization code with S256 PKCE, refresh-token rotation, OAuth protected-resource and authorization-server discovery, Client ID Metadata Documents, and Dynamic Client Registration. The user approves the coarse `mcp:use` scope; live LeapView RBAC and data policies remain authoritative for every tool call. Access tokens last 15 minutes and refresh tokens last 30 days.

General LeapView API tokens and browser-session cookies are intentionally rejected at `/mcp`. A development bearer token remains available only with the local development bypass. Automated MCP clients use an existing LeapView service-principal ID and secret with the OAuth `client_credentials` grant, the `mcp:use` scope, and an exact `resource` of `${LEAPVIEW_PUBLIC_URL}/mcp`.

To delegate MCP authorization to an organization-wide provider, set `LEAPVIEW_MCP_OAUTH_ISSUER_URL`. The issuer must publish OpenID Connect discovery and sign JWT access tokens whose audience is exactly `${LEAPVIEW_PUBLIC_URL}/mcp`, whose subject identifies the user, and whose scope contains `mcp:use`. LeapView maps the external subject/email to a principal, then applies the same live authorization checks. In this mode clients use the external provider's advertised authorization endpoints; LeapView's embedded authorization endpoints are unavailable.

Cross-origin MCP requests are rejected. OAuth tokens are bearer credentials on the wire, so deploy only behind HTTPS and never place them in URLs or logs. Follow [Connect an MCP host](/docs/guides/integrate/mcp) for deployment configuration, Claude setup, OAuth discovery, service-principal automation, and troubleshooting.

## Validate answers and operate safely

Natural-language output is not a replacement for governed results. Present tool evidence, resource identity, filters, and relevant time or deployment context so a user can validate claims. Use deterministic semantic or dashboard queries for automated decisions that cannot tolerate interpretive variation.

Test empty results, authorization failures, project-scoped credentials, ambiguous questions, provider timeouts, cancelled runs, and active deployment changes. Audit conversation and tool activity, apply bounded retention with `leapview admin maintenance`, and never log provider API keys or raw sensitive prompts into general diagnostics.

See [Service principals and API tokens](/docs/security/tokens) and the generated [`agent` CLI reference](/docs/cli/agent).
