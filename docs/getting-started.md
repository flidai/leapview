# Get started with LeapView

Choose a starting point for authoring analytics, operating a server, or contributing to LeapView. The source-checkout tutorial then walks through a validated dashboard change.

## Choose your starting point

- **Author analytics:** install a published alpha [authoring CLI](https://github.com/flidai/leapview/blob/main/deploy/local/INSTALL.md),
  then follow the [analytics development workflow](/docs/cli/analytics-development).
  `leapview init` creates **Sales overview**, a small synthetic project. The CLI
  manages a checkout-scoped local Docker runtime; no LeapView source checkout
  or contributor toolchain is required.
- **Operate a server:** follow [Installation](/docs/installation) for
  the Compose package and its external PostgreSQL/DuckLake and storage setup.
  A normal server image does not pre-deploy the repository showcase.
- **Change LeapView itself:** follow the [contributor setup](/docs/contributing/repository),
  then [Build your first dashboard](/docs/first-dashboard). That
  tutorial uses the source checkout's Olist-backed **Executive Sales** project,
  which is separate from the CLI starter.

Read [Project structure](/docs/project-structure) to understand the
six portable resource kinds. Published packages are alpha software for
controlled evaluation; package availability does not qualify every host or
workflow. The authoring guide separates current branch behavior from released
package evidence.

## What you will learn

By the end of the tutorial, you will have practiced the complete local authoring loop:

1. Establish a known-good sample dashboard.
2. Trace presentation fields back to their semantic definitions.
3. Make a small change without breaking stable resource identities.
4. Validate the complete resource graph.
5. Deploy to the development target and verify interactive behavior.

The tutorial optimizes for a successful first experience. After completing it, use the task-oriented [Build dashboards](/docs/guides/build) guides for real project work and [Reference](/docs/reference) for exact resource fields, CLI flags, API operations, and visual contracts.

## Explore by goal

- To connect your own data, follow [Connect a data source](/docs/guides/build/connect-data).
- To understand ownership and deployment scope, read [Projects and environments](/docs/concepts/projects-environments).
- To evaluate available charts and tables, browse [Visual types](/docs/visuals/overview).
- To automate delivery, start with [Develop, review, and publish](/docs/cli/validate-deploy).

The project source and issue tracker are available on [GitHub](https://github.com/flidai/leapview).
