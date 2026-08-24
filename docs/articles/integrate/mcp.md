# Connect an MCP host

LeapView exposes the same governed BI tool catalog used by its built-in agent through a tools-only MCP endpoint. The endpoint belongs to each LeapView deployment:

```text
${LEAPVIEW_PUBLIC_URL}/mcp
```

For example, a deployment whose public URL is `https://bi.example.com` exposes MCP at `https://bi.example.com/mcp`. `https://leapview.dev` hosts the LeapView website and documentation; it is not a shared MCP gateway for independently deployed instances.

LeapView implements stateless Streamable HTTP 2025-11-25. It exposes tools, not MCP resources, prompts, nested conversations, or stdio transport. The built-in agent and MCP use the same catalog, input and output schemas, handlers, authorization, projections, audit path, and execution errors.

## Before you connect

Configure the deployment's exact externally reachable HTTPS origin:

```sh
LEAPVIEW_PUBLIC_URL=https://bi.example.com
LEAPVIEW_ALLOWED_HOSTS=bi.example.com
```

The origin must have no path, query, fragment, or credentials. Reverse proxies must preserve the public scheme and host. The resulting OAuth resource and audience are exactly `https://bi.example.com/mcp`; changing the public URL creates a different resource identity.

An interactive user needs browser authentication and the OAuth `mcp:use` scope. Grant access to the project resources its tools may use with the canonical `RESOURCE_*` capabilities. Each tool call checks the referenced project resource and the tool's capabilities and data policies. A project is not encoded in the MCP connection URL: query tools accept exact catalog refs, while catalog tools return project identity in `{kind,id}` refs.

The MCP endpoint is independent of `LEAPVIEW_AGENT_MODEL` and `LEAPVIEW_AGENT_API_KEY`. External MCP hosts can use LeapView when the built-in model is disabled.

## Connect Claude

Claude connects to LeapView as a remote custom connector:

1. In Claude, open **Customize**, then **Connectors**.
2. Choose **Add custom connector**.
3. Name the connector, for example `LeapView`.
4. Enter your deployment URL followed by `/mcp`, for example `https://bi.example.com/mcp`.
5. Leave advanced OAuth client ID and client secret fields empty. LeapView supports automatic client registration and Client ID Metadata Documents.
6. Connect the new connector. Claude opens the LeapView sign-in and consent flow in the browser.
7. Review and approve the `mcp:use` request, then enable the connector in the conversation where it should be available.

Organization owners may need to add the custom connector before members can connect it. Claude's UI and plan availability can change; use Anthropic's current [remote MCP custom connector guide](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp) when its labels differ from the steps above.

Other remote MCP clients use the same URL and OAuth discovery flow. The client must support Streamable HTTP and OAuth for protected MCP resources.

## Confirm the tool catalog

After connecting, the host's `tools/list` response contains exactly twenty tools:

```text
add_dashboard_page
add_dashboard_visual
assign_dashboard_field
catalog_search
catalog_list
catalog_get
create_dashboard_draft
query_semantic_model
query_dashboard_visual
query_visual
docs_search
docs_read
execute_dashboard_command
export_dashboard_yaml
fork_dashboard
get_dashboard
get_dashboard_draft
list_dashboards
preview_dashboard_draft
set_dashboard_visibility
```

The same names, input schemas, output schemas, safety annotations, and handlers are used by built-in chat. Dashboard authoring tools are governed writes for private drafts and exact revisions; catalog, query, documentation, preview, and export tools remain read-only. There are no model-visible compatibility aliases for the superseded tool catalog. Rediscover tools after upgrading LeapView.

Use `catalog_search` when the project or parent is unknown, `catalog_list` to browse a known parent, and `catalog_get` before querying when an exact definition is needed. Follow [Use the agent tool catalog](/docs/guides/integrate/agent-tools) for refs, hierarchy, locations, pagination, errors, and query-tool selection. The generated [Agent tool reference](/docs/agent-tools) publishes the same schemas and annotations as `tools/list`.

## Understand the sign-in flow

An unauthenticated MCP request receives an OAuth challenge pointing to protected-resource metadata. For the example deployment, clients discover:

```text
https://bi.example.com/.well-known/oauth-protected-resource/mcp
https://bi.example.com/.well-known/oauth-authorization-server
```

You can verify that discovery is publicly reachable before configuring a client:

```sh
curl -fsS https://bi.example.com/.well-known/oauth-protected-resource/mcp
curl -fsS https://bi.example.com/.well-known/oauth-authorization-server
```

LeapView's embedded authorization server supports authorization code with S256 PKCE, Dynamic Client Registration, Client ID Metadata Documents, refresh-token rotation, and explicit resource indicators. Browser sessions authenticate the consent page but are not accepted as MCP credentials. The issued OAuth access token is a bearer token on the wire, lasts 15 minutes, and has an exact MCP audience. Refresh tokens last 30 days and rotate when used.

General LeapView API tokens are intentionally rejected at `/mcp`. They have different audience and delegation semantics and should continue to be used for the REST API and CLI.

## Connect an automated workload

Use a dedicated LeapView service principal for a non-interactive MCP client. Grant it the `mcp:use` OAuth scope and only the project-resource capabilities its tools require. Exchange the service-principal ID and secret at the deployment's OAuth token endpoint:

```sh
curl -fsS https://bi.example.com/oauth/token \
  -u "$MCP_CLIENT_ID:$MCP_CLIENT_SECRET" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode grant_type=client_credentials \
  --data-urlencode scope=mcp:use \
  --data-urlencode resource=https://bi.example.com/mcp
```

Pass the returned access token to the MCP transport as `Authorization: Bearer <access-token>`. The service-principal secret is presented only to the token endpoint; it is not itself an MCP access token. Rotate and revoke it through the existing service-principal lifecycle.

## Use an external authorization server

Set `LEAPVIEW_MCP_OAUTH_ISSUER_URL` to delegate MCP authorization to an organization-wide issuer. The issuer must be an HTTPS URL without URL credentials, a query string, or a fragment; issuer paths remain supported. It must publish OpenID Connect discovery and sign JWT access tokens with:

- an audience exactly equal to `${LEAPVIEW_PUBLIC_URL}/mcp`;
- a subject that LeapView can map to an existing principal;
- an `mcp:use` scope.

LeapView still performs live RBAC and data-policy checks for every tool call. In external-issuer mode, clients use the external provider's advertised authorization endpoints and LeapView's embedded authorization endpoints are unavailable. Ensure the provider and client together support the interactive remote-MCP flow you intend to use.

## Troubleshoot connections

| Symptom | What to check |
| --- | --- |
| The client cannot discover OAuth | Confirm public DNS and TLS, then fetch both well-known URLs. Verify `LEAPVIEW_PUBLIC_URL` exactly matches the connection origin. |
| Sign-in loops or returns to the wrong host | Check reverse-proxy scheme and host handling, allowed hosts, secure cookies, and registered browser-auth callback URLs. |
| MCP returns `401` | Acquire a fresh OAuth token with `mcp:use` and the exact MCP resource. Do not substitute a LeapView API token. |
| MCP returns `403` before a tool runs | Grant the client the `mcp:use` scope and the required project-resource capability. |
| A tool returns an authorization error | Check the query's catalog ref, the principal's resource privilege, data policy, and any service-principal restrictions. |
| Claude cannot reach an internally healthy deployment | Remote connectors run outside your private network. Expose a trusted HTTPS endpoint or use a client that can reach the deployment. |
| A browser-origin request is rejected | Connect through an MCP host. LeapView rejects cross-origin MCP transport requests deliberately. |

Successful calls return both MCP `structuredContent` and equivalent JSON text. Expected tool failures are returned as tool errors; malformed MCP requests use protocol errors. Request cancellation propagates into query execution, and every call is recorded through the agent-tool audit path.

Review [Agent integrations](/docs/guides/integrate/agent), [Service principals and API tokens](/docs/security/tokens), [Audit events](/docs/security/audit), and [Production configuration](/docs/guides/operate/production-configuration) before enabling production clients. Use the generated [environment variable reference](/docs/configuration) and [Access API reference](/docs/api/access) for exact runtime and principal-management contracts.
