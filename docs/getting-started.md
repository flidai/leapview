# Get started with LeapView

Use this learning path to go from a new LeapView installation to a validated dashboard change. It keeps setup, the guided lesson, and the underlying project model separate so that each page can serve one purpose well.

## Choose your starting point

- Start with [Installation](/docs/installation) if LeapView is not running yet. It covers both the public image and a source checkout.
- Continue with [Build your first dashboard](/docs/first-dashboard) for the guided learning experience. You will run the sample Sales project, trace its resources, change a semantic metric and dashboard note, validate the project, deploy it locally, and verify the result.
- Read [Project structure](/docs/project-structure) when you are ready to understand how project-wide resources fit together.

The first-dashboard tutorial uses a source checkout because the lesson includes editing the repository project. If you only want to evaluate the running product, use the public-image path in the installation guide. Its explicitly disposable synthetic project is deployed by evaluation mode; the source-authoring showcase is not pre-deployed in the normal server image.

## What you will learn

By the end of the tutorial, you will have practiced the complete local authoring loop:

1. Establish a known-good sample dashboard.
2. Trace presentation fields back to their semantic definitions.
3. Make a small change without breaking stable resource identities.
4. Validate and review the project plan.
5. Deploy to the development target and verify interactive behavior.

The tutorial optimizes for a successful first experience. After completing it, use the task-oriented [Build dashboards](/docs/guides/build) guides for real project work and [Reference](/docs/reference) for exact resource fields, CLI flags, API operations, and visual contracts.

## Explore by goal

- To connect your own data, follow [Connect a data source](/docs/guides/build/connect-data).
- To understand ownership and deployment scope, read [Projects and environments](/docs/concepts/projects-environments).
- To evaluate available charts and tables, browse [Visual types](/docs/visuals/overview).
- To automate delivery, start with [Develop, review, and publish](/docs/cli/validate-deploy).

The project source and issue tracker are available on [GitHub](https://github.com/flidai/leapview).
