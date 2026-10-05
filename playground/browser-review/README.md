# Optional playground browser review

This isolated Playwright configuration reviews the standalone production-component
playground. It does not change product QA, root configuration, package scripts,
dependencies, or CI. It uses the installed `@playwright/test` and
`@axe-core/playwright` packages.

The configuration starts `server.ts` with Bun, which calls the existing
`startPlayground` with `watch: false`. The server builds once, binds to
`127.0.0.1:4410`, and closes with the runner. Set `PLAYGROUND_REVIEW_PORT` for a
different port. An occupied port fails instead of silently using another server.
The existing playground dependency/contract-generation prerequisites still apply;
see [the playground README](../README.md).

## Run and inspect

From the repository root:

```sh
bun run browser:ensure
bun x --no-install playwright test --config playground/browser-review/playwright.config.ts
```

Select either review family or one theme:

```sh
bun x --no-install playwright test --config playground/browser-review/playwright.config.ts --project=visual-light --project=visual-dark
bun x --no-install playwright test --config playground/browser-review/playwright.config.ts --project=accessibility-light --project=accessibility-dark
```

The run uses one worker, Chromium, a 1440 × 1000 viewport, CSS-scale screenshots,
reduced motion, `en-US`, and UTC. Each project has a fresh browser context. A helper
reloads each example with an explicit theme; native component state and edited
fixtures are not shared between examples. No credentials or product services are
needed.

Reports, traces, actual/diff screenshots and accessibility JSON attachments belong
under `.tmp/playground-browser-review`, which can be removed between reviews.
Open the report after a run:

```sh
bun x --no-install playwright show-report .tmp/playground-browser-review/html-report
```

## Screenshot baselines

The representative visual set covers the default `charts/bar`,
`tables/windowed`, and `graphs/asset-lineage` previews at desktop width, plus
`recipes/linked-visuals` in a 390 × 844 browser with the 360 px preview width.
Cases use `openExample` with the project's theme and `preview: true`. Desktop
captures use the playground `.viewport`. Mobile captures use the visible browser
screen, including Exit preview, to avoid clipped offscreen rows inside the scroll
container. They set the browser viewport as well as the preview width.

Keep baseline names descriptive and independent of generated IDs. Linux Chromium
baselines live in `baselines/linux-chromium/visual-light/` and
`baselines/linux-chromium/visual-dark/`. The platform segment isolates other OS
rendering; accepted shared baselines should be produced and reviewed on Linux
with the repository's pinned browser and fonts.

An initial run fails for missing baselines. To intentionally create or update them:

```sh
bun x --no-install playwright test --config playground/browser-review/playwright.config.ts --project=visual-light --project=visual-dark --update-snapshots
```

Inspect every changed PNG and the HTML report before accepting a baseline update.
Then rerun without `--update-snapshots`. Do not accept a baseline merely to make a
failure pass. Comparison allows Playwright's 0.2 per-pixel color threshold but zero
pixels above that threshold. Failures retain actual, expected and diff images.
The renderer mount hook, loaded fonts, reduced motion and Playwright's consecutive
stable screenshot comparison reduce timing noise without masking content.

## Accessibility sweep

The catalog sweep takes several minutes per theme.
See the latest [review results and coverage limits](ACCESSIBILITY.md).
The command fails when findings remain; no rules are disabled.

The sweep uses `discoverExamples` to read the actual **Examples** navigation links,
including collapsed categories. Every route opens with default fixtures in each
theme, then `scanAccessibility` applies WCAG 2 A/AA, 2.1 A/AA and 2.2 AA rules. The scan includes
the shell and the rendered component, traversing open shadow roots.

The sweep collects findings before failing, attaches per-route JSON reports with
violations and incomplete results, and identifies routes/themes in failure
messages. Each theme has a ten-minute limit. Each route gets a fresh page and a
30-second render/scan deadline; errors and timeouts are recorded, the page closes,
and the sweep continues. Reports include browser errors and unexpected
backend/external requests. The sweep fails on violations, render/scan failures,
browser errors, or unexpected requests. It does not exclude components, disable
rules, or treat an axe exception as a clean scan. Incomplete results remain in the
reports for manual review.

An axe pass is limited to the rendered default DOM. Hidden popovers, dialogs,
alternate states, keyboard navigation, focus restoration, canvas chart meaning,
screen-reader usability and WebGL output still require manual review. Changes to
the catalog automatically expand the sweep; pixel baselines remain a small,
deliberately reviewed set.

This follows Playwright's official [visual comparison](https://playwright.dev/docs/test-snapshots)
and [accessibility testing](https://playwright.dev/docs/accessibility-testing)
guides, using the versions already installed by the repository lockfile.
