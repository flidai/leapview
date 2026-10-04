# README refresh verification

Date: 4 October 2026.
Source revision: `81b99e5fb480e1d7bf5aff1971bd222c823c6615`.

## Changes and review

- Rewrote the README around product benefits, three onboarding audiences,
  accurate starter excerpts, governance, and the published alpha boundary.
  It contains 750 words excluding fenced code.
- Corrected directly linked installation, contributor, tutorial, project
  structure, and authoring-package documentation. Regenerated `docs/llms.txt`
  from the corrected navigation summaries.
- Astra reviewed the diff twice. Broken site-relative links, obsolete plan
  review wording, misleading credential paths, and the broken bypass-first
  contributor recipe were corrected.
- Rechecked GitHub during implementation: `main` still matched the source
  revision. Open PRs were #834, #833, #832, #802, #744, #742, and #659. No open
  PR's proposed functionality is represented as a released feature.

## Published authoring package

Tested the Linux AMD64 archive from `v0.3.0-alpha.1`, using its packaged binary
and sibling runtime. External and internal SHA-256 checks passed. Binary,
package, release identity, and runtime image agreed on:

- Version: `0.3.0-alpha.1`.
- Revision: `bf792c45bd1bf346ab30d33c467ad43f802359b4`.
- Image: `ghcr.io/flidai/leapview@sha256:f435b922968975c295ae738e622805c4c738b9cdf6b7624a4cd5cd7af0557a86`.

The available host was Ubuntu 26.04 AMD64, Docker Engine 29.1.3, Compose
2.40.3. This is not the package's declared Ubuntu 24.04 qualification target.
No other host or provider matrix was tested.

`leapview init` and complete starter validation passed. In an independent
temporary project, `leapview dev --no-browser` reached automatic session setup
but failed because D-Bus had no `org.freedesktop.secrets` service. The README
therefore omits an inline runnable quickstart, as the approved plan requires
when the packaged lifecycle cannot be demonstrated. Installation now states
the native credential-store prerequisite. Packaged browser rendering, filter
interaction, edit-to-visible behavior, and restart persistence remain
unverified. No runtime workaround or fix was introduced.

## Source showcase and screenshots

The fresh `task dev:bypass` attempt failed because bypass skips credential
creation while publication reads `credentials.json`. The normal authenticated
`scripts/dev-server.sh start`, after the Task preparation steps, successfully
created credentials, staged Olist, compiled a candidate, and published it.
Onboarding now prescribes `task dev`, whose preparation ends in that same
authenticated helper. Subsequent explicit publication uses `task dev:publish`.

The checkout binary passed `validate --source-root dashboards`. Both README
YAML excerpts were checked against the full initializer templates; the tutorial
examples match the checked-in Olist semantic model and dashboard.

Playwright captured the real source-build Executive Sales overview at
1280 × 1200 in light and dark themes. All six visualization envelopes were
ready or partial, and server telemetry recorded six successful targets with
zero errors. The capture used the dashboard's Fit page control and hid only
the contributor Datastar inspector. Both images were visually inspected; no
credentials or diagnostic overlay appear. Existing website assets and other
worktrees' servers were untouched.

## Documentation and rendering checks

- `task docs:check`: passed after generation, including rendered docs links.
- Complete source showcase validation: passed.
- Complete released starter validation: passed.
- README Mermaid parser and accessibility metadata: passed.
- README local links and both image paths: passed.
- All 13 hosted documentation destinations in the README: HTTP 200.
- GitHub Markdown API rendering: succeeded. A local preview with basic
  responsive styles loaded the correct theme image at widths 390 and 1000,
  with document width equal to viewport width. Mobile onboarding and both
  captures were visually inspected. This is a local preview, not a deployed
  GitHub page or a website deployment.
- `git diff --check`: passed.
- Package qualification tests: passed after correcting the runtime README
  installation link to an absolute URL usable inside an extracted archive.
  The initial CI run detected this regression; `go test ./deploy/local` passed
  after the correction.
- The corrected `task ci` run passed generation, Go packages, external
  integration, PostgreSQL conformance, quality/coverage, and the core frontend
  shard. Its reports shard hit a five-second visibility timeout in the unchanged
  standalone playground gauge test (59 other playground tests passed).
  The isolated gauge retry passed in 0.99 seconds; the full playground suite
  subsequently passed all 60 tests. The reports shard exceeded its unchanged
  300-second aggregate watchdog on both attempts. The second attempt completed
  every group through dashboard-builder without an assertion failure, then
  timed out during windowed-table. Separate windowed-table and filter-menu
  runs passed, followed by the bounded chat, data, and site shards and
  `task generated:check` (combined continuation exited 0). The aggregate
  watchdog gate did not pass; all individual test groups passed across these
  runs. A clean, single-invocation `task ci` result is not claimed.

The plain Nix launcher initially failed because this host did not enable
`nix-command`. A non-mutating invocation with `nix-command flakes` enabled
successfully opened the locked environment. The full CI invocation uses the
available conventional toolchain; no machine-wide Nix settings were changed.

## Scope

No application code, release manifest, deployment contract, compatibility
interface, or website publication changed. Temporary packaged services, the source capture server, its PostgreSQL
service, and the local README preview were stopped. Private local state was
retained; other worktrees were left untouched.

## PR review follow-up

- Incorporated `main` through `b2f47afe6` (the public-site/theme update), preserving
  the merge already made on the remote PR branch.
- Recaptured the authenticated source dashboard using Fit width. The hero is
  now a 1296 × 704 landscape view of the filters, KPI cards, and monthly trend,
  with the page sidebar and lower charts outside the capture framing. Product
  data, labels, and chart rendering were not edited. All six envelopes loaded
  before capture; only the contributor inspector was hidden.
- Retained the original full-dashboard captures in a theme-aware expandable
  section, so the categories and orders table remain available.
- Kept navigation and badges compact and updated the hero alternative text.
- Corrected the saved plan's stale statement about `task dev` data staging.
- GitHub Markdown API rendering and a responsive local browser preview passed
  at widths 390 and 1000, light and dark, with the detail section both closed
  and open: all four image paths resolved to the expected theme, with no
  document-level horizontal overflow. Both hero captures and the final desktop
  preview were visually inspected.
- `go run ./internal/app/tools/docsitegen --check` and `git diff --check` passed
  after the merge. This presentation-only follow-up did not repeat the full
  application test suite. The previously recorded watchdog and native
  credential-store limitations remain explicit; hosted CI runs on the updated
  PR head.


## Product-story polish

- Paired the existing theme-aware brand mark with “Define metrics in code.
  Explore them everywhere.” Reworked the benefits around reader outcomes while
  retaining the portable-policy, instance-identity, optional-AI, and alpha
  qualifications.
- Pointed the top Get started link to the three-path chooser. Each path states
  its expected result. A mobile inspection caught a horizontally scrolling
  three-column draft; the final paths use short list items.
- Explained how the starter's total KPI, monthly trend, and category chart all
  query the same `revenue` definition. Checked these references against the
  complete initializer resources.
- Added readable titles to the three Executive Sales KPIs and to the website's
  exact copy of that example. Full source validation and `go test ./site`
  passed. The initial local CI run caught the missing website copy; it was
  synchronized before the corrected run.
- Published the changed showcase to this worktree's isolated authenticated
  development instance. Recaptured both hero and full-dashboard images in
  both themes, with only the contributor inspector hidden.
- Recorded the actual Category input and its change event, selecting
  `health_beauty` and then clearing it. The displayed count/revenue changed
  from 99.4K/16M to 8.81K/1.44M and back. The monthly chart changed with it;
  server telemetry recorded six successful targets and zero target errors for
  each filter refresh. The optional GIF was cropped and encoded from that
  browser recording, without rewriting product text, values, or rendering.
- Rebuilt the local preview from GitHub Markdown API output with GitHub-style
  Markdown CSS, code highlighting, working local anchors, and locally rendered
  Mermaid. The preview remains a worktree artifact, separate from the README.
- Browser checks passed at 390 and 1000 pixels in light and dark themes, with
  both expandable sections closed and open. Every image loaded, the correct
  hero theme was selected, the diagram rendered, the Get started anchor
  navigated correctly, and there was no document-level horizontal overflow.
  Desktop, mobile onboarding, and recording contact sheets were inspected.
- README relative links/assets, the Mermaid parser/accessibility metadata,
  documentation generation verification, and `git diff --check` passed.
- A corrected full `task ci` run was started after synchronizing the website
  example. Its final result and the hosted PR/merge-queue results are reported
  on PR #836; they are not assumed from the focused checks above.

## Merge-queue visual baseline correction

- The first merge candidate's site job passed its frontend tests, generated
  checks, and all 13 route checks, then failed the two compact Executive Sales
  screenshot comparisons. The reviewed failure artifacts showed that the
  baselines still expected the old KPI IDs after the intentional title change.
  The other ten visual comparisons passed.
- Incorporated `main` through `3c8bb50f2` before refreshing the baselines. Used
  `task qa:ui-framework:visual:update` to regenerate all four Executive Sales
  snapshots, including desktop cases whose label differences were within the
  existing tolerance. Reviewed every changed light/dark, desktop/compact PNG.
- The official task passed all 24 QA configuration tests, all 12 snapshot
  update cases, and all 12 cases in its separate fresh comparison run. It then
  stopped its isolated development server and removed its QA PostgreSQL
  services successfully. No comparison thresholds, masks, or test logic changed.
- The corrected full local `task ci` run described above passed generation,
  Go/application/external tests, PostgreSQL conformance, coverage, and the core
  frontend shard, then hit the reports shard's unchanged 300-second watchdog
  on both attempts. Later frontend shards were not reached in that invocation.
  The hosted PR CI and Security gate passed on `8b8f2d06c`; validation of the
  baseline correction and its new merge candidate is tracked on PR #836.
