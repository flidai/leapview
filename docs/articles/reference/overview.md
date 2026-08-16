# Reference

Reference pages describe exact accepted syntax and supported machine contracts. They are generated from application source definitions wherever possible. Choose the contract you are writing or inspecting.

## Configuration

- Use [Configuration reference](/docs/config) for generated project, connection, source, model, semantic-model, pipeline, dashboard, access, and publication fields.
- Use [Environment variable reference](/docs/configuration) for process settings, defaults, lifecycle, secret classification, and production relationships.
- Use [Supported capabilities](/docs/reference/capabilities) for the current boundary of resource, data, visual, security, operational, and integration surfaces.

## Commands and APIs

- Use [Agent tool reference](/docs/agent-tools) for generated names, privileges, defaults, MCP annotations, and input and output schemas.
- Use [CLI command reference](/docs/cli/reference) for generated usage, arguments, flags, defaults, and inherited options.
- Use [API reference](/docs/api) for generated operation groups and the downloadable OpenAPI contract.
- Use [API conventions](/docs/guides/integrate/api-conventions) for shared authentication, pagination, error, retry, timeout, and compatibility rules.

## Visuals

Use [Visual types](/docs/visuals/overview) for every supported renderer-neutral visual shape, its dashboard configuration, expected query shape, and live example.

## Keep generated reference authoritative

When a guide and generated reference disagree on exact syntax, follow the reference for the deployed version and report the guide drift. Agent tool contracts are generated from the same provider composition used by built-in chat and MCP. Do not hand-edit generated pages. Contributors should use [Write documentation](/docs/contributing/documentation) and the [Repository and development workflow](/docs/contributing/repository) to regenerate and verify reference artifacts.
