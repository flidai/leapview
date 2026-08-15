# Integrate with LeapView

LeapView exposes CLI, HTTP API, built-in agent, and remote MCP surfaces backed by the same active projects, authorization, data policies, and semantic contracts. Choose the highest-level stable operation that fits the integration.

## Choose an integration surface

- Use the [CLI](/docs/cli) for human-operated delivery, CI pipelines, managed-data synchronization, maintenance, and quick headless queries.
- Use the [HTTP API](/docs/guides/integrate/api-quickstart) for application integrations that need typed request, response, status, pagination, and compatibility contracts.
- Use [Headless BI queries](/docs/guides/integrate/headless-bi) for governed dashboard or semantic-model results without the LeapView browser UI.
- Use [Dashboard authoring and promotion](/docs/guides/operate/dashboard-authoring) for the browser builder, headless authoring API, agent authoring tools, and development-to-production promotion flow.
- Use [Public and embedded dashboards](/docs/guides/integrate/public-dashboards) for anonymous, governed dashboard presentations on approved origins.
- Use [Agent integrations](/docs/guides/integrate/agent) for iterative natural-language exploration through the governed tool catalog.
- Use the [agent tool catalog reference](/docs/guides/integrate/agent-tools) when implementing direct MCP calls or evaluating agent tool traces.
- Use [MCP](/docs/guides/integrate/mcp) to expose the same governed tools to Claude or another remote MCP host.

## Design a reliable boundary

Read [API conventions](/docs/guides/integrate/api-conventions) for authentication, resource identity, pagination, errors, retries, timeouts, cancellation, and compatibility. Use [Automation and CI](/docs/cli/automation) when the integration delivers projects unattended.

Every integration should use an attributable principal with the narrowest useful privileges. Read [Service principals and API tokens](/docs/security/tokens) for workload identity and credential handling, and [Roles, grants, and policies](/docs/security/authorization) for effective access.

## Look up exact contracts

Use the generated [CLI command reference](/docs/cli/reference) and [API reference](/docs/api) for accepted syntax and schemas. Prefer those generated contracts over copying flags, paths, or payload shapes from an explanatory page.
