# CodeQL reliability and alert remediation plan

Reviewed: 4 October 2026. Status: implementation in progress.
Tracking: [Linear project P-FAI-65](https://linear.app/flid/project/leapview-codeql-reliability-and-security-alert-remediation-c31a24fb8dc0), issues FAI-1077–FAI-1086. Lead: Ganesh Kambli. Target: Sunday, 11 October 2026.
Repository: `flidai/leapview`. Baseline: `ad2c9bfb6b148834268e422ff2e2bbc1670acc81`.
Scope: Go and JavaScript/TypeScript scanning, the seven open `main` alerts, and regression prevention.
The previous, unrelated root plan is preserved unchanged in `plan.kamal-rollout.md`.

## Findings that change this plan

The screenshot reveals two separate workstreams: incomplete analysis and open code findings. Fixing the analysis warning will not automatically resolve the seven findings. A successful Actions job currently proves neither complete extraction nor absence of vulnerabilities.

1. **P1 — Incomplete Go analysis is confirmed.** The SAST runner does not generate required source or use the shared toolchain. CodeQL reports 44 missing local packages while its job and `Security gate` pass. Generation in the dependency job runs on another machine and cannot prepare the SAST workspace.
2. **P2 — Page-stream cookie policy is inconsistent (#91).** Authentication uses the configured secure-cookie policy, but the page-stream cookie independently inspects TLS and an unconditionally trusted forwarded header. This is a real configuration gap; exploitation in the deployed proxy topology has not been demonstrated.
3. **P2 — The desktop verification script needs message validation (#1).** The flagged callback comes from a private `Map`, which prevents arbitrary property dispatch. A separate, adjacent defect is reproducible: a WebSocket payload of JSON `null` throws before validation. This affects the local packaging/accessibility checker; it is not evidence of remote product code execution.
4. **P3 — Integer and allocation alerts already have bounds (#52, #49).** Simplify the integer conversion and prove the allocation bound through both HTTP callers. Do not describe them as demonstrated overflow or memory-exhaustion vulnerabilities.
5. **Triage — Both password-hashing alerts are false positives on the inspected paths (#47, #48).** Their SARIF flows originate from the boolean `HasLocalPassword`, not a password. The sinks create ETags and pagination keys. Actual stored password verifiers use Argon2id.
6. **Triage — The local browser cookie is an intentional loopback exception (#87).** This flow explicitly accepts only `http://127.0.0.1:<port>`, binds its handoff listener to loopback, and disables redirects in its authentication client. Setting `Secure=true` indiscriminately would break the intended flow.
7. **Policy limitation — Required checks do not enforce alert severity.** The active `main` ruleset requires `CI gate` and `Security gate`, but has no `code_scanning` rule. The workflow's aggregate gate checks job outcomes only. Keep this distinct from the proposed analysis-health check.

Priorities above are remediation priorities, not replacements for GitHub's displayed severity ratings. No alerts have been dismissed and no production code or repository settings have been changed during this review.

## Evidence and baseline

- [Inspected security run](https://github.com/flidai/leapview/actions/runs/37200497398), [Go job](https://github.com/flidai/leapview/actions/runs/37200497398/job/111431019646), and [JavaScript job](https://github.com/flidai/leapview/actions/runs/37200497398/job/111431019681) all concern the baseline commit.
- Go analysis ID `1888592831`; JavaScript analysis ID `1888591182`. Authenticated API retrieval confirmed seven open alerts, each with its most recent instance on the baseline commit. The private security page does not load through unauthenticated web browsing.
- The Go log reports 44 unresolved generated packages, including `internal/access/api/gen`, `internal/platform/http/api/gen`, `internal/access/postgres/internal/db`, and `internal/access/ui/signals`. Import/type errors accompany the warning.
- The reported 1,716/3,387 Go file count is not proof that half of production code is omitted: the denominator includes tests and other files that the extractor may legitimately exclude. Acceptance must examine missing dependencies and expected maintained packages, not impose a misleading percentage.
- `.github/workflows/security.yml:86` currently uses Go `autobuild` and JavaScript/TypeScript `none`, with checkout, initialization, and analysis only. The CodeQL action is pinned to `2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2` (v4.38.2); the inspected run used CLI 2.27.1.
- The autobuilder attempts `go mod tidy -e`. A plan that combines autobuild with an immutable dependency graph is inconsistent. Use a manual traced build with read-only module resolution.
- Raw analyzer SARIF contains extraction diagnostics. SARIF downloaded from GitHub's analysis API is processed and omitted invocation diagnostics in this investigation. It is useful for alert traces, not as a fixture for the proposed health checker.
- Active ruleset ID `19956950` was read without modifying it. It enables a merge queue and requires the two aggregate status checks, but contains no code-scanning severity rule.

## All seven alerts: assessment and disposition

### #91 — Cookie Secure attribute: page-stream identity

[Alert #91](https://github.com/flidai/leapview/security/code-scanning/91), GitHub severity **Medium**, rule `go/cookie-secure-not-set`.

Evidence: `internal/platform/web/transport/client_id.go:18` returns an existing valid cookie immediately; line 32 sets `Secure: requestUsesHTTPS(r)`. Lines 37–45 trust `X-Forwarded-Proto` without consulting deployment policy. In contrast, `internal/app/config/config.go:197` computes `CookieSecure()` and `internal/app/postgres_build.go:134` passes that policy to authentication.

A proxied HTTPS deployment with secure cookies configured can still issue a non-secure page-stream cookie if the forwarded header is missing or differs from the helper's expectation. A previously issued cookie is not reissued when the policy changes. The client ID is a routing/session correlation value, not the authentication credential: dashboard session keys also bind the principal (`internal/dashboard/http/handlers.go:349`). Do not infer an authentication bypass from this alert.

Planned change:

- Make page-stream cookie issuance consume an explicit application-owned policy derived at composition from the existing secure-cookie configuration and canonical serving origin. Production HTTPS and direct HTTPS must remain secure independently of client-supplied headers; supported local HTTP must remain functional.
- Thread this policy through the product transport and its callers, including the `Patch*` helpers, dashboard/builder issuance, and admin issuance. Use one policy; remove the existing header-based decision. Update all callers rather than keep an old signature or implicit fallback.
- When ensuring an existing valid client ID, reissue it with the current policy before response headers or SSE bytes are sent. Preserve the ID and other attributes. Request cookies do not reveal their original Secure flag, so the server cannot condition this upgrade on inspecting that flag.
- Keep `HttpOnly`, `SameSite=Lax`, path `/`, host-only scope, and entropy-failure handling.

Regression proof: secure-policy requests over the proxy's internal HTTP connection must emit Secure with absent, misleading, and comma-separated forwarded headers; an existing valid ID must receive the correct attributes; direct HTTPS must stay secure; deliberate local HTTP must work. Verify cookie issuance precedes SSE streaming and principal isolation is unchanged. Confirm a deployed HTTPS response through the actual proxy before closing the finding.

### #1 — Callback dispatch in the accessibility checker

[Alert #1](https://github.com/flidai/leapview/security/code-scanning/1), GitHub severity **High**, rule `js/unvalidated-dynamic-method-call`.

Evidence: `desktop/scripts/accessibility-contract.mjs:155` accepts only loopback WebSocket URLs. Lines 171–193 use a private `Map` of locally registered callbacks and safe-integer IDs. There is no attacker-selected object property or arbitrary function installation. `desktop/scripts/verify-package.mjs:360` invokes this during packaged-app verification.

However, after `JSON.parse`, the code reads `message.id` without first checking the payload's shape. A fake loopback WebSocket delivering `null` reproduced `TypeError: Cannot read properties of null (reading 'id')`. The six existing tests exercise accessibility-tree validation, not this transport boundary.

Planned change:

- Validate that decoded messages are non-null, non-array objects before accessing properties.
- Accept responses only for valid outstanding positive safe-integer IDs. Ignore legitimate CDP notifications and unknown/duplicate response IDs without invoking anything.
- Read the callback into a local variable, check that it is callable, delete its pending entry, then invoke it. Preserve `Map`; do not replace it with a plain object.
- Route malformed frames and socket close/error into controlled rejection, clear outstanding timers, and settle pending operations. Preserve the existing message-size and timeout bounds.

Regression proof: valid response and protocol error; JSON `null`, arrays, primitives, malformed/oversized frames; string/prototype-like and unsafe IDs; unknown/duplicate IDs; close/error while awaiting a response. Tests must verify controlled promise rejection without uncaught event-handler exceptions or leaked timers. Rerun the packaging accessibility smoke and CodeQL. If the original dispatch warning persists, triage that exact warning separately from the now-fixed malformed-frame defect.

### #52 — Platform-sized integer conversion

[Alert #52](https://github.com/flidai/leapview/security/code-scanning/52), GitHub severity **High**, rule `go/incorrect-integer-conversion`.

Evidence: `pkg/duckdbsql/decode_helpers.go:155` parses an int64, checks architecture-specific upper and lower limits, then converts at line 166. The 32-bit branch already rejects out-of-range input; the 64-bit comparison is redundant after int64 parsing. This is a scanner-recognition/clarity issue on the reviewed code, not a reproduced overflow.

Planned change: retain JSON-number decoding, but parse `intValue` directly to the platform `int` range using `strconv.Atoi` on the decoded number. Keep the separate `int64Value` contract for its callers. Remove the manual bit-twiddling bounds. Preserve valid JSON/error behavior and avoid parsing through floating point.

Regression proof: native minimum and maximum int; one below/above them; 32-bit boundary values on both architectures; int64 overflow; malformed/fractional inputs; existing decode error classification. Run the focused pure-Go decoder tests on amd64 and 386 where executable. A compile-only cross-build is not proof that boundary tests ran. Rerun CodeQL before deciding whether any residual finding needs triage.

### #49 — Response slice allocation

[Alert #49](https://github.com/flidai/leapview/security/code-scanning/49), GitHub severity **High**, rule `go/uncontrolled-allocation-size`.

Evidence: `internal/dashboard/semanticapi/semantic_query_support.go:130` allocates capacity `min(len(rows), limit)`. Both production callers, `semantic_queries.go:53,77` and `semantic_datasets.go:203,227`, obtain their limit through `semanticLimitAndOffset` at lines 60–72. It normalizes non-positive values and caps large values at `maxQueryLimit = 1000` (`semantic_http_support.go:141`). The query requests one extra row for pagination, while the response remains capped at the normalized page size. SARIF traces pass through this existing clamp.

Planned disposition: first rescan with complete extraction and add behavior-level boundary tests for both callers. If the warning remains, record a narrow false-positive rationale with this call-chain evidence. Do not merely remove preallocation: appending without a capacity can still allocate unbounded memory in a truly unbounded path. Do not silently change the API from clamping to rejecting oversized requests.

Regression proof: omitted/zero/negative limit, 1, 1000, 1001, maximum representable int; malformed numeric input; returned row counts and one-row pagination probe; cursor behavior for empty, exact-limit, and extra-row results. This conclusion concerns the flagged row-capacity allocation, not all memory use in query execution or column payloads.

### #47 and #48 — Non-secret metadata mistaken for password material

[Alert #47](https://github.com/flidai/leapview/security/code-scanning/47) and [alert #48](https://github.com/flidai/leapview/security/code-scanning/48), GitHub severity **High**, rule `go/weak-sensitive-data-hashing`.

Evidence: the type at `internal/access/access.go:373` declares `HasLocalPassword bool`.

- #47 starts at `internal/access/http/handler.go:230`, the `canChangePassword` capability, and reaches `resourceETag` at lines 597–600 through principal responses.
- #48 starts at line 264, the `canResetPassword` capability, and reaches `apiItemPageKey` at lines 638–649 through principal-list pagination. Principal DTOs have an ID and normally take the earlier ID-based key branch.
- These responses contain capability flags and profile metadata, not plaintext passwords or stored password verifiers. The real verifier functions at `internal/access/postgres/access_core.go:104` use Argon2id; password creation/change call that path.

Planned disposition: preserve SHA-256 for deterministic metadata hashing. Save the source-to-sink evidence for each alert and, after a complete scan, use alert-specific false-positive triage if the warnings remain. Do not swap ETags or cursor keys to salted password hashes, remove capability flags, rename public fields to evade detection, or disable the rule globally.

Regression proof: reuse/extend current-principal and principal-administration response tests to assert that credential material is absent; verify stable ETags for unchanged representations, ETag changes when relevant representation fields change, and pagination continuity. Retain existing password-verifier tests. Document these as false positives, not fixed password-storage vulnerabilities.

### #87 — Local browser session handoff

[Alert #87](https://github.com/flidai/leapview/security/code-scanning/87), GitHub severity **Medium**, rule `go/cookie-secure-not-set`.

Evidence: `internal/app/cli/local_browser_session.go:340` sends the handoff cookie; `browserSessionCookie` explicitly sets `Secure=false` at line 303. `localSessionOrigin` at line 113 restricts origin to explicit HTTP IPv4 loopback plus a port, rejecting userinfo and nonempty paths/queries/fragments after trailing-slash normalization. `openLocalBrowserSession` at line 311 validates that origin again and binds `127.0.0.1:0` at line 322; its random handoff URL has a two-minute lifetime. Authentication redirects are disabled at line 131. The cookie remains HttpOnly, host-only, SameSite Lax.

Planned disposition: retain the deliberately local HTTP behavior and document the exception beside the code. Add missing boundary tests before recommending alert-specific dismissal as intentional local-test/development behavior. Do not globally exclude CLI files or the cookie rule.

Regression proof: reject remote hosts, hostname lookalikes, userinfo, alternate schemes, paths/queries/fragments, and missing ports before network or browser activity. Verify a failed/random path emits no session cookie, the handoff listener is loopback-only, authentication does not follow redirects, and browser handoff retains its cookie attributes and bounded lifetime.

The local trust assumption must be explicit: cookies are not port-scoped, and hostile processes on the same host are not isolated by this design. `Secure` is not a solution to that local-process boundary. Do not claim this cookie is safe for remote HTTP deployment or dismiss it as unused code.

## Implementation sequence

### 1. Restore complete analysis on both matrix runners

Files: `.github/workflows/security.yml`, `Taskfile.yml`, focused tooling under `internal/app/tools/securitysast`, workflow-contract tests, and security operating documentation.

1. Keep existing triggers, check names, least-privilege permissions, commit-pinned actions, matrix independence, and `/language:${{ matrix.language }}` upload categories. Keep the current 45-minute budget initially; measure cold and warm runs before changing it.
2. Run `./.github/actions/setup-ci` with the validation profile and `browser: "false"` before CodeQL initialization. This supplies the repository's locked Go/Node/Bun/sqlc/native toolchain. Do not replace it with an unrelated setup-go version or wrap the traced build in an environment that drops CodeQL instrumentation.
3. Capture the checkout's dependency-manifest baseline before any installer, generator, or helper-build command can change it. Build the small SAST helper before initialization, outside the source tree, using read-only module resolution; include this build in integrity verification. The helper must depend only on policy/standard tooling, not generated application packages.
4. Add a sequential `security:sast:prepare` task that invokes the existing generators. Start with `go:deps`, `db:generate`, `config:generate`, `api:generate`, `ui-signals:generate`, `agent-contracts:generate`, `data-resource-contracts:generate`, `pipeline-contracts:generate`, `desktop-discovery:generate`, `layout-contract:generate`, `map-style:generate`, and `lucide-icons:generate`. Reuse their declared dependency graph: API generation supplies the emitter, permission/dashboard/visualization contracts, and root Node installation. Explicit API generation before UI-signal generation avoids the clean-checkout bootstrap hole. Validate this exact set on a clean checkout rather than assuming it is complete.
5. Use one top-level Task invocation and sequential `cmds`, so dependency installation has a single writer. Use the existing frozen Bun locks and npm `ci`; do not run independent generators concurrently against the same dependency tree. `NPM_CONFIG_AUDIT=false` prevents install-time advisory noise; the separate dependency-security lane stays enabled.
6. Prepare generated source in each matrix job's own workspace. Do not rely on another job's files, commit ignored build inputs, copy stale generated output from caches, or run the entire docs/site-producing `task generate` just to satisfy extraction.
7. Keep JavaScript/TypeScript build mode `none`. Before initialization, validate app/contracts/test TypeScript configs (including site source), playground where relevant, the APIGen emitter's production and test configs, and desktop production/test configs after `desktop:deps`. Invoke installed compilers with no emit; do not package Electron or bundle/minify assets for SAST. These checks establish resolvable source graphs, not full test or CodeQL coverage.
8. Set explicit job environment: `GOFLAGS=-tags=duckdb_arrow`, `CODEQL_OVERLAY_DATABASE_MODE=none`, `CODEQL_ACTION_DIFF_INFORMED_QUERIES=false`, and `CODEQL_ACTION_EXPORT_DIAGNOSTICS=true`. The latter three are supported by the pinned action and need contract coverage on upgrades. Full extraction avoids depending on overlay indexing of ignored generated source; full query evaluation retains diagnostic coverage.
9. For Go, change `build-mode` to `manual`. After CodeQL initialization and before analysis, invoke the prebuilt helper to enumerate every `go-module` in `.security/coverage.yaml`, currently root, `deploy/kamal-trial`, `pkg/apigen`, and `pkg/apigen/example`. Reuse the exported coverage types; reject empty/duplicate/escaping module paths and require the existing inventory policy to validate completeness. Do not add a second hard-coded module inventory.
10. In each module, sequentially run `go list -mod=readonly -deps -tags=duckdb_arrow ./...` and `go build -a -p=2 -mod=readonly -tags=duckdb_arrow ./...`, propagating failures. The forced rebuild ensures restored build caches cannot eliminate the work CodeQL must observe. Preserve the initialized tracer environment and the shared CGO/native toolchain. No `go mod tidy`, `-e`, or continue-on-error in this path.
11. Check dependency integrity after preparation and again after analysis, including on failures. Compare paths, existence, and content of maintained Go manifests/sums and JavaScript manifests/locks with the checkout baseline. Detect newly created files as well as modifications/deletions, including a `go.sum` originally absent. Ignore dependency/cache subtrees, not newly created first-party dependency files. Also reject unexpected tracked source drift from preparation.

### 2. Fail the required job when analysis is incomplete

Extend the focused SAST helper with raw-SARIF validation, with meaningful fixture tests.

- Direct analyzer output to `${{ runner.temp }}/codeql-results`. The pinned analyzer writes `go.sarif` and `javascript.sarif`; the matrix label `javascript-typescript` is not the filename.
- Validate expected SARIF 2.1.0 CodeQL output, expected language/category, at least one matching run, and nonempty invocation records with successful execution. Check every relevant run/invocation; do not inspect only the first. Normalize the category/run-ID representation according to actual output from the pinned analyzer.
- Inspect both `toolExecutionNotifications` and `toolConfigurationNotifications`. Treat warning/error diagnostics as a failed health check. SARIF's omitted notification level defaults to warning. Note/none are allowed; absent notification arrays are legal when there are no diagnostics. Resolve descriptor IDs/indexes and report actionable diagnostic IDs, messages, and locations.
- Reject missing, empty, malformed, unexpected-tool/category, unsuccessful, or unverifiable output. Do not confuse an API-processed report without diagnostics with raw analyzer output. Seed fixtures from actual runner output, plus focused synthetic error cases.
- This is an **analysis-health** check. SARIF `results` are vulnerability findings, not extraction notifications; their handling is tracked separately below. Zero findings must not be mistaken for healthy extraction.
- Run validation after analysis whenever initialization succeeded and the job was not cancelled, including when analysis failed. Missing output must fail rather than skip. Keep CodeQL's normal upload so developers can inspect findings; preserve the original analyze failure too.
- Upload available raw SARIF on success or failure using the repository's pinned upload-artifact action, 14-day retention, and matrix/job/run/attempt-specific names. An artifact uploader may ignore a missing file, but the health check may not.
- Keep the aggregate `Security gate` contract requiring every lane to succeed. A health failure in either matrix entry must propagate through the existing required gate.

Tests must cover multiple runs/invocations, missing and failed invocations, both notification arrays, omitted/default and invalid levels, descriptor references, malformed/truncated JSON, category mismatches, missing output, vulnerability results without extraction warnings, and artifact/condition ordering. Parse workflow YAML for sequencing and conditions rather than rely only on matching text fragments. Update the existing `build-mode: autobuild` expectation. Add the helper tests to `task security:policy`.

### 3. Establish a fresh alert baseline

Run complete Go and JavaScript analysis on the implementation candidate before final triage. Retrieve the refreshed alerts and compare their source-to-sink paths with all seven entries above. Fixing missing packages can expose additional findings or change paths; newly surfaced findings need individual review.

Do not delete historical analysis configurations merely to remove the warning banner. A manual-build configuration may have a different configuration identity; verify the current required analysis and current main status. Preserve categories and evidence so that alert history remains understandable.

### 4. Apply focused code changes and triage

Use separate reviewable commits for the scanner repair, page-stream cookie policy, desktop message validation, and integer parsing cleanup. Add the response-bound and local-loopback regression tests with their relevant rationale. Use red-green-refactor for confirmed defects; characterization tests for already-correct bounds may pass before any change.

Retain the alert-specific evidence in a short repository review record. After fresh analysis, fixes should close through normal scanning; residual false positives or intentional local behavior should receive a narrow, reasoned disposition. Do not bulk-dismiss alerts, mark them fixed before a verifying scan, add blanket path/query exclusions, or place CodeQL dismissals into an unrelated dependency-advisory exception mechanism.

### 5. Record the separate merge-policy decision

The scanner repair makes incomplete scans fail. It does not make open High/Medium alerts fail the job. Document this explicitly in security operating guidance and the implementation PR.

Recommended follow-up: propose a concrete addition to the existing `main` ruleset requiring CodeQL with `security_alerts_threshold: high_or_higher` and `alerts_threshold: none`, retaining its current required checks and queue configuration. This is a repository-setting proposal, not a change made by this plan. Re-read the current rule before producing its final diff, preserve other rules, and validate behavior with a disposable candidate.

GitHub documents two material limits: this native protection does not apply to merge-queue groups, and qualifying alert locations must be in the PR diff. It will not retrospectively gate all seven existing findings. Do not advertise it as exact-candidate severity enforcement. If the project adopts that stronger policy, design and review a separate SARIF-result gate with explicit thresholds and tightly scoped triage handling; do not silently turn the diagnostic checker into an alert suppressor or pretend API dismissals remove findings from fresh raw SARIF.

## Verification and acceptance

### Checks completed during this investigation

- Authenticated GitHub alert inventory, both analysis reports, Go job logs, and active main ruleset inspected.
- Existing tests passed: `go test ./internal/platform/web/transport ./pkg/duckdbsql ./internal/app/tools/securitycontracts ./internal/app/tools/securityresults`.
- Existing desktop accessibility tests passed: `node --test desktop/scripts/accessibility-contract.test.mjs` (6 tests).
- A local fake-WebSocket probe against the unchanged module reproduced the JSON-null TypeError, then shut down its pending call. No real remote service was contacted by that probe.
- Earlier read-only `go list -mod=readonly -deps -tags=duckdb_arrow ./...` failed on missing generated packages, consistent with the extraction warning.

These checks validate the investigation only. Generated-source preparation, the proposed manual traced build, broad application tests, and a new hosted CodeQL scan have not been executed. No claims of fixed alerts or healthy replacement analysis are made.

### Required during implementation

1. Focused tests for `securitysast`, workflow contracts, security policy/results, transport cookies, decoder boundaries, semantic query handlers, principal DTO/ETag/pagination behavior, local CLI handoff, and desktop transport handling. Prepare required generated source before application tests.
2. `task security:policy`, workflow/actionlint validation, and `task ci` before handing off substantial implementation. Run the desktop test/build and packaged accessibility verification appropriate to its change; run the focused decoder tests on both integer widths. Do not substitute a typecheck for those behavior tests.
3. Hosted cold-cache and warm-cache scans of the same candidate. Prove all four Go modules build under tracing, generated import paths resolve, and both language reports contain complete raw diagnostics. Record toolchain, candidate SHA, durations, and extraction evidence. Warm-cache success must still include forced compilation.
4. On a disposable test candidate/fixture, remove a required generated package and inject a diagnostic warning separately. Each must fail the SAST job and required Security gate. Missing SARIF must also fail. Ensure diagnostics are retained even on failure.
5. Change a TypeSpec input in a disposable validation case and confirm generation is refreshed before extraction. Verify no dependency manifest or lockfile changes, including newly created files, in both cold and warm cases.
6. Verify actual proxy HTTPS cookies and local HTTP handoff. Recheck all seven GitHub alerts against the fixing commit and record fixed, false-positive, intentional-exception, or still-open status individually. Review any new findings rather than require the old alert count to remain seven.
7. Verify the normal PR, merge-group, and main workflows on their actual candidate commits. Completion requires healthy current Go and JavaScript scans without the missing-package warning, required checks enforcing analysis health, and a reviewed disposition for every listed alert. The separate alert-severity enforcement limitation must remain explicit until that policy is implemented.

## Research sources

The repository and authenticated SARIF traces provide the case-specific evidence above. These primary sources informed the design:

- [GitHub: compiled-language CodeQL build options](https://docs.github.com/en/code-security/reference/code-scanning/codeql/codeql-build-options-and-steps-for-compiled-languages) and [no source seen during build](https://docs.github.com/en/code-security/reference/code-scanning/troubleshoot-analysis-errors/no-source-code-seen-during-build): manual build sequencing and cache concerns.
- [CodeQL Go autobuilder](https://github.com/github/codeql/blob/main/go/extractor/cli/go-autobuilder/go-autobuilder.go): module-update behavior, corroborated by the inspected job log.
- Pinned CodeQL action [configuration logic](https://github.com/github/codeql-action/blob/2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2/src/config-utils.ts), [feature flags](https://github.com/github/codeql-action/blob/2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2/src/feature-flags.ts), [overlay indexing](https://github.com/github/codeql-action/blob/2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2/src/overlay/index.ts), and [analyze inputs](https://github.com/github/codeql-action/blob/2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2/analyze/action.yml): full extraction, diagnostics export, and output contract.
- [SARIF 2.1.0 specification](https://docs.oasis-open.org/sarif/sarif/v2.1.0/os/sarif-v2.1.0-os.html): invocation and notification semantics.
- CodeQL query guidance for [integer conversion](https://codeql.github.com/codeql-query-help/go/go-incorrect-integer-conversion/), [allocation size](https://codeql.github.com/codeql-query-help/go/go-uncontrolled-allocation-size/), [sensitive-data hashing](https://codeql.github.com/codeql-query-help/go/go-weak-sensitive-data-hashing/), [cookies](https://codeql.github.com/codeql-query-help/go/go-cookie-secure-not-set/), and [dynamic method calls](https://codeql.github.com/codeql-query-help/javascript/js-unvalidated-dynamic-method-call/). The last recommends `Map`, which this repository already uses.
- [Go strconv](https://pkg.go.dev/strconv): platform-sized parsing behavior.
- [GitHub: resolving alerts](https://docs.github.com/en/code-security/how-tos/manage-security-alerts/manage-code-scanning-alerts/resolve-alerts), [merge-protection limitations](https://docs.github.com/en/code-security/concepts/code-scanning/merge-protection), and [setting merge protection](https://docs.github.com/en/code-security/how-tos/find-and-fix-code-vulnerabilities/manage-your-configuration/set-merge-protection): narrow triage and separation of status checks from vulnerability policy.

Implementation finding: clean-workspace TypeScript test checking also requires `docs/visuals/examples.gen.json`. Add `visual-docs:generate` to sequential SAST preparation, using its existing offline extension preparation dependencies. This was exposed by the new type-check gate.
