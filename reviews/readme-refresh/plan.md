# LeapView README refresh — corrected implementation plan

Updated: 4 October 2026.

Status: implemented; verification completed with recorded limitations. See
[verification and limitations](verification.md) for executed checks, the
reports-shard watchdog timeout, and the packaged credential-store failure. This plan incorporates the repository audit
and Astra-assisted review. It supersedes the earlier README plans in the chat.
The repository-root `plan.md` remains the separate website rollout record.

## Objective and scope

Rewrite the README as a clear, compelling introduction for analytics authors
and teams evaluating LeapView. Target **600–800 words**, excluding code, with
one product showcase and one reproducible starter example.

Include the README, its screenshot and interaction-recording assets, focused
corrections to directly linked onboarding documentation, and the three
Executive Sales KPI display titles needed for authentic product captures
(including the website's exact copy of that example). No runtime changes, compatibility
interfaces, release-manifest redesign, website deployment, or release
publication.

The research snapshot is `main` at
`81b99e5fb480e1d7bf5aff1971bd222c823c6615`. The current published server alpha
at that snapshot is
[`v0.3.0-alpha.1`](https://github.com/flidai/leapview/releases/tag/v0.3.0-alpha.1).
Recheck these facts before implementation; source-branch functionality and
published-package functionality are separate evidence boundaries.

## 1. Verify the onboarding path before writing it

- Recheck current `main`, published releases, downloadable assets, and relevant
  PR merge states. Use the explicit current alpha release; do not use GitHub's
  `/releases/latest`, which selected the older non-prerelease during research.
- Download the exact published authoring CLI archive for the available
  supported Linux architecture. Verify its external checksum, internal file
  checksums, executable identity, and packaged runtime identity.
- Keep the executable and its runtime package together. Follow the
  [installation instructions](../../deploy/local/INSTALL.md) without using the
  repository-built executable.
- In a fresh temporary project, verify:

  ```sh
  leapview init my-analytics
  cd my-analytics
  leapview dev
  ```

- Confirm **Sales overview** opens, its charts render, and its Region filter
  affects the results. Change the revenue KPI's presentation note and verify
  it appears without restarting. Then verify stop/start persistence.
- Record the tested OS, architecture, Docker provider, CLI version, and result.
  Describe other downloadable platforms as package availability; do not imply
  their complete lifecycle was tested. Do not add an unnecessary manual login
  step: the local runtime has an automatic session-establishment path.
- If this workflow fails, omit the inline CLI quickstart from the README,
  retain a clearly labeled alpha authoring-guide link, and report the failure
  separately. Do not expand this task into runtime repairs.

## 2. Rewrite the README in this order

### Identity and positioning

- Pair the LeapView heading with the existing theme-aware brand mark.
- Use **“Define metrics in code. Explore them everywhere.”**
- Describe open-source BI with shared semantic definitions across dashboards,
  APIs, and optional AI integrations.
- Add compact links to the in-page onboarding chooser, Documentation, the
  explicit alpha release, and Contributing.
- Use three badges: Nightly CI on `main`, Alpha, and Apache-2.0. Keep the alpha
  release link explicit rather than relying on a latest-release badge.

### Real product showcase

- Show Executive Sales from the current source build in light and dark themes.
  Use readable KPI titles in the actual dashboard configuration. Keep a focused
  hero, an expandable full-dashboard capture, and an optional real recording
  of a category filter changing the metrics and chart.
- Caption it as a repository showcase from the development build, separate
  from the generated starter. Do not imply it is preloaded by `leapview init`
  or that its current interface is the published alpha's interface.
- Store images under `.github/assets/` and render them with a theme-aware
  `<picture>` element and descriptive alternative text.

### Verified benefits

- Analytics definitions reviewed in Git.
- Reusable semantic metrics across dashboards and integrations.
- Interactive dashboard building, filtering, charts, and analytical tables.
- Governed access and identity integrations.
- Self-hosted execution using Go, DuckDB, DuckLake, and PostgreSQL.
- Explain that browser authoring creates governed drafts and supports YAML
  export; it does not automatically commit to Git.
- Explain that built-in AI requires provider configuration; external MCP uses
  the deployment's protected endpoint.

### Get started

- Separate analytics authoring, self-hosted operation, and LeapView
  contribution, with an explicit expected result for each.
- For the verified CLI path, link installation prerequisites immediately
  before the three-command quickstart. State the local Docker Engine and
  Compose requirements from the package guide.
- Identify the expected result as **Sales overview**, using deterministic
  synthetic data.
- For operators, state the external PostgreSQL control/DuckLake and storage
  prerequisites and link the Compose guide.
- For contributors, link the complete toolchain/bootstrap instructions before
  mentioning `task dev`, `task playground`, and `task ci`. The authenticated
  `task dev` workflow prepares and stages the sample on a fresh database, as
  verified during implementation.

### A matching code example

- Use the starter's `revenue` metric and revenue KPI—never Executive Sales'
  `aov` metric for the starter walkthrough.
- Show short excerpts from the
  [starter semantic model](../../internal/app/cli/projectinit/template/dashboards/semantic-models/sales.yaml)
  and
  [starter dashboard](../../internal/app/cli/projectinit/template/dashboards/dashboards/sales-overview.yaml),
  clearly identifying their containing fields and linking to complete files.
- Retain current named-list syntax, the `sum` over `amount`, and the typed
  aggregate/KPI definitions.
- Frame the example as one revenue definition feeding several views: the
  total KPI, monthly trend, and category chart.
- Include a compact resource-flow diagram: Connection → Source → Model →
  SemanticModel → Dashboard, with Pipeline shown as refresh orchestration.

### Documentation, contribution, and status

- Group documentation links by building, integrations, operating/security,
  and architecture.
- Link connector and visual catalogs instead of duplicating exhaustive
  inventories.
- Link Issues for ordinary reports and the security policy for private
  vulnerability reporting.
- State that the published alpha is for controlled evaluation and may have
  changing interfaces or incomplete workflows.
- Do not advertise withdrawn desktop downloads, an unauthenticated hosted
  demo, or capabilities that exist only in open PRs.
- End with Apache-2.0. Remove dated website deployment evidence and detailed
  worktree-session mechanics.

Use this precise governance distinction throughout: **semantic access rules
can live in versioned SemanticModel definitions; principals, attribute
assignments, role/resource-grant assignments, Project identity, and publication
state are managed by the target instance.** The
[authored contract](../../api/data-resources/main.tsp) and
[compiler policy boundary](../../internal/project/compiler/data_resources.go)
define portable semantic restrictions. Do not claim either that all access
rules are in Git or that all access policy is target-owned.

## 3. Correct only the onboarding dependencies

- Correct stale claims that authoring CLI archives are unavailable in the
  analytics-development guide and directly linked local-runtime guidance.
  Preserve the distinction between published artifacts and demonstrated
  lifecycle qualification. The existing Compose-focused release manifest does
  not need a schema change for this documentation task.
- Align Getting Started and Installation around the three actual workflows.
  Remove references to the unsupported standalone evaluation command.
- Keep the existing Executive Sales tutorial explicitly identified as a
  source-checkout tutorial. Update its obsolete YAML and use its managed
  development publication workflow consistently: validate the source, then
  use `task dev:publish`. Link the general delivery guide for other targets
  rather than duplicating an unbound plan/build/publish sequence.
- Remove the obsolete `access/` directory from the portable source-root
  example. Preserve the explanation of semantic access rules within
  SemanticModel resources; standalone access resources are not a seventh
  portable resource kind.
- Align contributor setup instructions with the current development launcher.
- Use repository-relative links from the README to corrected onboarding
  documents. Within documents, use canonical GitHub file links where the site
  renderer does not rewrite repository-relative paths, and preserve native
  `/docs/...` navigation required by landing-page validation. Verify both
  GitHub and rendered-site navigation.
- Keep hosted links for broader documentation; do not assume a documentation
  commit updates the manually deployed website.

## 4. Capture and validate safely

- Capture screenshots using a dedicated instance with isolated process state,
  ports, and storage. Leave existing development servers and website images
  untouched. Capture fully loaded charts and tables with no diagnostic overlay
  or credential material visible.
- Do not run the current site capture helper unchanged: it deletes shared
  server state and overwrites website assets. Use a separate capture lifecycle
  and write only the README-specific images.
- Validate complete starter resources using the published CLI. Validate
  current-source showcase resources using the checkout build.
- Check README excerpts against those complete resources. Existing
  documentation checks exclude the root README and do not schema-validate
  partial YAML examples.
- Regenerate affected documentation through existing tasks; run
  `task docs:check`, `task ci`, and `git diff --check`. Retain only intentional
  tracked generated changes.
- Check relative links, release assets, hosted destinations, image rendering,
  both themes, and narrow-screen readability. The local GitHub-style preview
  must highlight code and render the Mermaid diagram.
- Report only checks actually performed. Do not add tests that merely assert
  marketing wording. Record unrelated check failures without representing
  them as successful validation or expanding this task into unrelated fixes.

## Acceptance criteria

- A new reader can identify what LeapView does and choose the correct starting
  path without confusing product installation with contributor development.
- Any runnable quickstart and the code example use the same starter project.
  Omit the inline quickstart if the packaged lifecycle fails, as specified above.
- Governance wording distinguishes portable semantic restrictions from
  instance-managed identity and authorization assignments.
- Feature claims, screenshots, and installation instructions identify the
  appropriate source-build or published-release boundary.
- Corrected onboarding links are usable when the repository change is
  published, without depending on an unperformed website deployment.
- Complete examples and the advertised packaged workflow have the validation
  evidence described above; failed or unperformed checks remain explicit.
- The final diff contains only the planned documentation and presentation
  changes. No runtime, deployment, release, or compatibility changes are made.
