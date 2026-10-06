# CLI overview

The `leapview` CLI runs local services, validates and deploys projects, synchronizes managed data, performs administration, and exposes generated API operations. Choose a task-oriented guide for workflow and the generated command pages for exact syntax.

Run `leapview` to see workflow groups and examples. Use `leapview <command> --help` for a command's arguments and flags. Help, version information, and shell completion work before Docker, a server, or credentials are configured.

`leapview init my-analytics` creates a project. Change into that directory, then run `leapview dev`.

`leapview dev` manages the analytics author's local preview. Contributors changing LeapView itself use `task dev`. To run an already-configured server, explicitly use `leapview serve`; bare `leapview` displays help.

Generate shell completion with `leapview completion bash`, `zsh`, `fish`, or `powershell`, then install the script using your shell's completion instructions. Completing saved target names and API operation names requires no target connection.

Run `leapview doctor` to inspect local authoring prerequisites, or add an explicit `--target` to inspect a remote instance. Use `leapview --llms` for offline agent guidance covering commands, effects, output, and recovery.

## Set up access

- [Install and authenticate the CLI](/docs/cli/authentication).
- [Choose targets and environments](/docs/cli/targets).

## Deliver and automate

- [Run the analytics development workflow](/docs/cli/analytics-development) with an exact authoring archive when available or the current source checkout.
- [Develop, review, and publish](/docs/cli/validate-deploy) an exact target candidate.
- [Plan, stage, and activate managed data](/docs/guides/data/revisions).
- [Run automation and CI](/docs/cli/automation) with bounded credentials and preserved evidence.

## Diagnose and look up commands

- Use [CLI troubleshooting](/docs/cli/troubleshooting) for local validation, readiness, authentication, planning, and deployment failures.
- Use the generated [CLI command reference](/docs/cli/reference) for every command, positional argument, flag, default, and inherited option.
- Use [API conventions](/docs/guides/integrate/api-conventions) when a long-lived integration should call the versioned HTTP contract instead of composing CLI output.
