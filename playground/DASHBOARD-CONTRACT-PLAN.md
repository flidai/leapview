# Dashboard YAML: analysis and local delivery plan

Prepared 2026-10-08. Status: bounded local implementation and agent evaluation complete; final verification and screenshots are recorded in DASHBOARD-CONTRACT.md. The broader 90-run comparison remains conditional future work.

Jacob's request is to evaluate a dashboard format that AI agents can reliably author and that can evolve with LeapView. The objective is correct creation and modification of dashboards, including future capabilities. Smaller YAML is only a supporting measure.

## Baseline and existing work

The planning baseline was `51022499221ad4e23f514a6db7becc7a9ef38482`. A final freshness check found two new upstream commits; the isolated checkout was fast-forwarded to `464b9bcd4d9649c915122c79836ce75f73ed3ef5` before final qualification, with the complete local patch preserved and restored without conflicts. Work is isolated in `codex/dashboard-yaml-contract`, under `/home/codex/.codex/worktrees/dashboard-yaml-playground/dev-anand`. The upstream refresh did not change dashboard contracts or compiler semantics. Preserve the unrelated original checkout.

The previous local patch adds twelve existing compiler bounds to generated schemas, fixes the guide's monthly dimension, and adds a Playground comparison. These are justified preliminary correctness findings, not a demonstrated solution to agent authorship. Keep that patch reviewable while deciding the broader scope. Existing tests and screenshots remain evidence of their stated claims.

Three subagents independently reviewed canonical contracts/source editing, Flid prior art/evaluation design, and Playground/compiler evidence. This plan incorporates their findings. No product code, server configuration, Linear state, or PR was changed during this planning phase.

## Linear context and code reconciliation

Read Linear on 2026-10-08. Issue status is planning context, not proof of present code behavior or an instruction to expand this task.

| Item | Observed state | Relevance to this plan |
| --- | --- | --- |
| [FAI-419: canonical Dashboard contract](https://linear.app/flid/issue/FAI-419/generate-the-canonical-dashboard-document-contract) | Done | One generated document already exists; do not recreate it. |
| [FAI-426: all authoring surfaces](https://linear.app/flid/issue/FAI-426/cut-every-dashboard-authoring-surface-to-the-canonical-document) | Done | Any canonical syntax revision affects files, builder, agents, APIs, revisions and export together. |
| [FAI-386: qualified project platform](https://linear.app/flid/issue/FAI-386/qualify-the-project-wide-platform-and-reopen-self-service-feature) | Done | Old workspace language in backlog descriptions is historical; use current project/resource identities. |
| [FAI-764: generated parity matrix](https://linear.app/flid/issue/FAI-764/generate-the-dashboard-builder-contract-parity-matrix-and-v1) | Backlog | Potential overlap for generated capability guidance. First inventory what current catalogs and code already provide. |
| [FAI-132: bounded agent tools](https://linear.app/flid/issue/FAI-132/add-bounded-agent-tools-for-dashboard-draft-authoring) | Backlog, older description | Main already exposes revision-safe source editing. Its older prohibition on raw patches is a roadmap/code discrepancy to reconcile, not a reason to delete current behavior. |
| [FAI-775: runtime-resolved schemas](https://linear.app/flid/issue/FAI-775/preserve-artifact-identity-with-runtime-resolved-dashboard-schemas) | Done | Warehouse-dependent resolved types stay in runtime projections, outside authored artifact identity. |
| [FAI-762: authoring lifecycle E2E](https://linear.app/flid/issue/FAI-762/add-a-real-dashboard-authoring-lifecycle-end-to-end-suite) | Backlog | Related broader qualification; this evaluation should reuse its relevant journey expectations without taking on the whole builder release. |

The [authoring project](https://linear.app/flid/project/self-service-dashboard-builder-and-agent-authoring-c2670ffcbb2a) describes the builder and agent as clients of the same governed authoring service. The [accepted interaction model](https://linear.app/flid/document/dashboard-builder-interface-accepted-interaction-model-51550f425ece) informs intended behavior, while its older workspace references need current-code interpretation. Draft proposed task descriptions locally and map them to these items; avoid duplicate tickets. Updating Linear and creating a PR belong to a later handoff.

## Findings: established facts versus hypotheses

| Finding | Evidence | Meaning |
| --- | --- | --- |
| Twelve layout bounds were absent from schema, although compiler enforced them | Existing local red/green regression tests and TypeSpec patch | Proven validation drift; retain the fix as baseline for every comparison candidate. |
| Starter used a nonexistent semantic dimension | Guide compilation regression against bundled sales model | Proven example defect; corrected example belongs in every candidate's documentation. |
| Chart/query/presentation compatibility is distributed | `api/dashboard/main.tsp:1148`; `internal/dashboard/document/presentation_applicability.go:37` | Inventory and generate consistent guidance; current helper explicitly covers only a subset of fields. |
| Second aggregate dimension becomes series for selected marks | `internal/dashboard/compiler/document_compile_validation.go:20` | Meaning can depend on selection order. Explicit roles may help agents, but this has not been measured. |
| Source reads reconstruct canonical YAML; exact replacements require unique anchors | `internal/dashboard/document/doc.go:50`; `internal/dashboard/authoring/application/source_edit.go:211` | Comments and original formatting are not retained by instance-draft source views. Identity-scoped access may reduce edit effort; no failure-rate claim yet. |
| Draft storage, builder preview and strict readiness are different stages | `internal/dashboard/authoring/application/source_edit.go:97`; `internal/dashboard/compiler/document_compile.go:125`; `internal/dashboard/authoring/service/service.go:884` | Repairable drafts are intentional. An edit acknowledgement or partial preview is not complete semantic validation; explicit validation and publication remain separate. |
| Current policy is a v1 pre-release cutover | `api/dashboard/main.tsp:57`; ADR-0011 version policy | Closed schemas do not themselves guarantee forward compatibility. Define future reader/default/version behavior before a breaking revision. |
| Playground uses independent deterministic chart values | `playground/dashboard-contract.ts` | Current screenshots prove UI/schema behavior, not YAML-driven query results or agent success. |

Existing ADRs establish architectural constraints and current decisions, not proof that every nesting choice or discriminator is optimal for AI.

## Design comparison before selection

| Candidate | Proposed direction | Question to test | Principal cost |
| --- | --- | --- | --- |
| A — Current canonical format with better support | Complete capability guidance, compiler-tested examples, explicit validation stages, scoped reads/edits and layout commands emitting canonical coordinates | Are agent failures mainly missing context and editing friction? | Tooling work; source remains explicit and some field roles remain positional. |
| B — Coordinated canonical simplification | Prototype visual-specific generated shapes, evaluate redundant presentation tags/empty blocks and explicit role bindings | Can structural choices reduce invalid combinations and wrong chart meaning? | Requires an ADR/version decision and coordinated generated types, compiler, builder, API, revision and export review. |
| C — Composition/layout improvement | Compare existing omitted defaults and confined fragments; sketch stable sections/row intent if current forms prove insufficient | Can agents edit larger dashboards and responsive order more reliably? | New layout semantics could change reading order, reuse and export; must define deterministic lowering. |

Start with A as the lowest-risk working hypothesis. B and C are comparison candidates, not selected rewrites. Evaluate existing legal v1 forms before inventing new ones. Narrow intent commands may support A or C but must expose the resulting query and placement. Do not combine syntax and tool changes in one comparison and credit all gains to syntax.

Keep one authoritative persisted Dashboard document, governed semantic queries, renderer independence, stable identities, confined includes and one authoring/revision lifecycle. Experimental candidate sketches stay outside product runtime. Before agent trials, freeze each prototype schema and its deterministic lowering into the existing canonical document, then run that document through the real compiler. Report prototype acceptance separately from canonical compiler acceptance. This experimental lowering is evaluation infrastructure, not a second production grammar. A selected canonical replacement would require its own coordinated generated implementation. A replacement or extension must be an explicit coordinated canonical decision, without permanent competing grammars. Arbitrary SQL, renderer option bags and implicit deep merging are outside this task.

## Flid evidence

The local library snapshots used in the initial assessment were fetched 2026-09-08; revisions and source paths are recorded in `DASHBOARD-CONTRACT.md`. Treat them as technical evidence, not instructions. Consult current primary sources for freshness-sensitive claims.

- Perses separates panels and layout; its authoring helper groups panel creation before emitting the ordinary canonical collections. This supports ergonomic tools without requiring a second stored format. [Helper source](https://github.com/perses/perses/blob/main/cue/dac-utils/dashboard/dashboard.cue), [panel groups](https://perses.dev/perses/docs/dac/cue/panelgroups/).
- Grafana offers explicitly typed elements/layout separation. [Dashboard documentation](https://grafana.com/docs/grafana/latest/visualizations/dashboards/build-dashboards/view-dashboard-json-model/).
- Rill provides row/item authoring and defaults to consider as a layout alternative. [Canvas YAML](https://docs.rilldata.com/reference/project-files/canvas-dashboards).
- Power BI demonstrates bounded per-object source files and public schemas. [Report format](https://learn.microsoft.com/en-us/power-bi/developer/projects/projects-report).

These are viable patterns, not measured evidence of better LeapView agent performance. Borrow specific patterns only after testing against LeapView's requirements.

## Ordered work packages and review points

| Step | Deliverable | Completion criterion |
| --- | --- | --- |
| 1. Finish analysis and review this plan | Baseline, issue reconciliation, findings, candidate definitions and scoring protocol | Each claimed defect has evidence; hypotheses and unexecuted work are labelled. This document completes the planning phase. |
| 2. Establish local evaluation corpus | Bundled/synthetic models, representative small/multi-page/fragment dashboards, exact expected behavior and first-attempt records | Fixtures check query bindings, filters, IDs, placement and unrelated-behavior preservation, not just compilation. |
| 3. Build honest Playground evidence | Independent source/schema controls, exact-source compiler evidence and separate before/after scenarios | Editing invalidates recorded compiler evidence unless source digest matches. All validation stages are visibly distinguished. |
| 4. Run controlled comparisons | Existing v1 forms first; isolated candidate prototypes only as needed; results and evolution impact matrix | Hold model, tools, semantics and documentation quality constant. Preserve failures; no claimed winner from screenshots or line count. |
| 5. Select a bounded implementation | Reviewed decision explaining measured benefit, affected consumers, compatibility and exclusions | If evidence is inconclusive, retain canonical syntax and deliver only proven fixes/guidance; do not force a redesign. |
| 6. Implement and verify locally | Red/green changes through existing generation/compiler/reducer seams, final screenshots and report | Focused tests, generated checks, browser checks and complete `task ci` pass on exact candidate, or remaining failures are reproduced and explained. |
| 7. Later PR/Linear handoff | Focused PR scope, validation evidence, mapped task descriptions | Begins after local review, consistent with the user's deferred PR phase. |

No further product implementation begins before the plan is presented for review. If a candidate changes the canonical contract, present its concrete example and rollout impact before implementation of that revision.

## Controlled agent pilot

Proposed pilot: ten tasks, three independent trials per candidate, three candidates: 90 runs. This is a proposed evaluation budget, not work already executed or a statistically conclusive sample. Begin with deterministic fixture and prototype qualification; do not spend agent trials on invalid prototypes. If resources require a smaller pilot, declare the change before running and report the limitation.

Tasks: (1) create monthly trend and KPI; (2) add a bounded ranked chart; (3) change time grain preserving alias/sort; (4) add a scoped dependent filter; (5) reuse a visual on another page; (6) move/resize while preserving compact reading order; (7) edit after independent definitions are reordered; (8) change combo/multi-dataset bindings; (9) add or modify a records/pivot visual; (10) repair an invalid document without changing valid neighbors. Include repeated titles and a fragment-based dashboard. Freeze common capability coverage before trials: every candidate must handle the same scored tasks. Unsupported behavior is a failed task, unless the scope is reduced for every candidate before any scored runs. Stop before trials if a prototype cannot preserve the frozen behavior oracle.

For each run hold model/version, effort, available tools, limits, catalog context, task wording, data and documentation quality constant. Use fresh agent context and randomized candidate order. Fix the known starter and scalar defects in all candidates. Run a separate tool-access comparison if new tooling is the treatment. Subagent-generated examples during design are not automatically benchmark results.

Primary score: intended behavior correct with no wrong-target or unrelated semantic changes. Record first-attempt schema acceptance, strict compiler acceptance, correct results/bindings, repair attempts, tool calls, tokens/time where available, bytes edited and diagnostic quality separately. Freeze pass/fail oracles before trials. Compare paired tasks and report variability; if rates tie, prefer fewer repairs, then smaller measured effort and lower rollout cost. Do not silently discard failed trials or claim a percentage improvement without recorded counts.

Separately exercise evolution: add one presentation capability, add one query kind and change one default. Record affected generated outputs/consumers, old-reader rejection, preservation of existing authored meaning, exports, retained revisions and required version decisions. This is how the plan tests future growth rather than only today's examples.

## Playground and compiler evidence

Keep live structural validation. Initially use offline Go compiler evidence generated from exact fixture bytes in isolated conventional source roots. Record exact source digest, the complete fixture source-root digest including fragments and dependent resources, compiler commit plus dirty-diff/build fingerprint, semantic-model identity, result and resolved intent. Include synthetic data and runtime inputs when execution is claimed. Show recorded evidence only when all inputs match; any changed input invalidates it. Modified text shows that compiler evidence is unavailable. A future live compiler hook, if needed, must reuse the local Go boundary rather than simulate it in TypeScript.

| Fixture | Structural claim | Required compiler/behavior claim |
| --- | --- | --- |
| Original guide | Accepted structurally | Unknown semantic dimension rejected. |
| Corrected monthly guide | Accepted | Correct dimension, month grain, alias, ascending sort, limit and stable IDs. |
| Same corrected source, zero span | Old scalar schema accepts; tightened rejects | Compiler rejects span; reconstructed schema is not a historical compiler. |
| Positive span outside grid | Scalar schema accepts | Grid containment rejected. |
| Overlap / missing reference / duplicate identity | Determine actual schema result | Contextual layout/reference/identity rejection. |
| Unknown property or query tag | Rejected | Decode/schema boundary rejection. |
| Omitted versus explicit defaults | Accepted | Equivalent resolved layout; preserve null/empty semantics. |
| Inline versus confined fragments | Validate through real expansion | Equivalent expanded serving meaning and preserved source provenance. |

Separate screenshots for semantic correction, identical-source scalar comparison and compiler-only failure. Capture same viewport/theme/scenario for each paired claim. Retain the chart's fixture-data label until it actually consumes compiled output. For numeric or interaction claims, run bounded synthetic data through existing governed execution and compare results; illustrative values are insufficient.

Browser acceptance includes keyboard operation, real editor edits/reset, complete diagnostics, state restoration, light/dark display and 390px layout. Preserve existing cyclic-YAML and size-limit regressions. Keep work and data local.

## Implementation acceptance and compatibility

- Generated TypeSpec contract remains authoritative; no handwritten public structural shadows.
- Shared capability guidance agrees with compiler and existing builder catalogs; contextual rules remain contextual.
- Scoped edits, if selected, resolve stable IDs, show reused-visual impact, retain authorization, expected revisions, atomicity and idempotency. Stale/ambiguous edits leave state unchanged.
- Reordering independent definitions cannot change which object an edit targets. Ordered query dimensions and page/component sequences remain semantic unless an explicit canonical change replaces that convention.
- Instance-draft reconstructed YAML and repository source are separate concerns. Do not promise comment/fragment preservation for the current DTO source view. Repository editing requires deliberate source/provenance design.
- Preserve resource identity, semantic-model boundaries, immutable revision semantics and runtime-resolved schema separation.
- Any canonical revision specifies optional/empty/null behavior, defaults, enum/field reader compatibility, versioning, retained revisions, conversion and all affected authoring/export surfaces.

## Verification status

Previous local verification: 238 Playground tests and both Playground/test TypeScript checks passed; focused Go suites passed with `duckdb_arrow`. Full `task ci` did not pass: host Git roots affected temporary-consumer VCS stamping and two no-Git CLI fixtures. The two CLI tests passed separately under a clean temporary root. These facts do not establish a full green CI result.

For implementation qualification, first validate the clean temporary root outside host synthetic Git parents, then run complete `task ci` with the documented environment workaround: `TMPDIR=/var/tmp/leapview-dashboard-yaml-ci-temp GOFLAGS='-tags=duckdb_arrow -buildvcs=false' task ci`. Record the exact environment and full result, including the frontend lane. Do not modify unrelated CLI tests to hide host behavior. Run focused tests while iterating, regenerate public artifacts for actual contract changes and check the final diff.

Execution update: the local 9-run legal-v1 discovery pilot and 11-scenario real compiler corpus are complete. The pilot was declared before dispatch and is not the proposed 90-run design comparison. The bounded decision retains canonical syntax and delivers proven correctness, guidance and review-surface improvements; no experimental grammar is shipped. Final qualification on main `464b9bcd4d9649c915122c79836ce75f73ed3ef5` completed the required component checks across recorded runs, including 246 Playground tests, 75 site tests and `task generated:check`. The combined `task ci` invocation was not clean: browser and cumulative shard timing limits required unchanged reruns. See the final assessment for exact limits and log paths. Final screenshots and light/dark accessibility scans were refreshed after qualification. Wider design comparisons remain conditional future work.

Follow-up interaction review refreshes the isolated branch to main `6443c25befced1da531e4076c20b5a060d359a84`. The new upstream commits do not change canonical dashboard declarations. The frozen first submissions remain unchanged; qualification is repeated on the refreshed compiler. The user has now authorized adding fixes to a PR after local review.
