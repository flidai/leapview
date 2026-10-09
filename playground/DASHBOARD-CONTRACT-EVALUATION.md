# Exploratory dashboard evaluation

Prepared 2026-10-08. This file records the preregistered local legal-v1 discovery pilot and an evolution impact analysis. All nine preregistered first attempts were submitted and scored against the real compiler and frozen intent oracles; no agent-performance winner is claimed.

Separate follow-up completed 2026-10-09: see the [90-attempt design comparison](dashboard-design-evaluation/comparison/README.md) and [executed evolution probes](dashboard-design-evaluation/evolution/README.md). This historical nine-attempt pilot and its frozen controls/results remain unchanged; its counts must not be combined with the later experiment.

## Scope and controls

The reviewed plan proposes 90 runs across three broader design candidates. This initial 9-run pilot instead compares three existing legal representations before new contract/layout prototypes are qualified: explicit layout defaults, omitted equivalent defaults, and confined visuals/pages fragments. Three tasks have one independent fresh-context submission in each representation. This is a discovery screen, not a statistically reliable comparison or completion of the 90-run evaluation.

The exact fixed reference, prompts, seeds and grader-only expected documents are under `dashboard-evaluation/`. `PREREGISTRATION.md` fixes controls and scoring before dispatch. Agents read only their prompt, common reference and designated seed files. They receive no compiler feedback and make one authored attempt, with no repairs. All trials use inherited model configuration and the same shell access. Catalog, task meaning and authoring reference are identical; the representation constraint is the treatment.

Tasks are create a monthly trend plus KPI, change grain/alias/sort while preserving neighbors, and edit one reused visual after definitions are reordered and titles duplicated. Expected semantic selections and page placements are frozen in grader-only oracle documents. The reference is purposefully small and fixed: this screens editing mechanics with supplied context, not discovery from the full schema or general dashboard competence.

Root records dispatch order, elapsed time when available, source hashes and changed files before evaluation. Grading expands fragments through the real Go document loader, strictly compiles against copied bundled semantic inputs, and compares intended expanded canonical content to the task oracle. Normalize definition order by visual ID and omitted layout to 12/48/16/16; retain ordered query selections, pages and components. Check assigned source representation separately. Compilation and exact document-intent checks establish the requested authored intent, not rendered appearance or executed data results.

Report parse/structural acceptance, compiler acceptance, exact intended content, preservation, submitted byte counts and changes separately. Failures remain in the denominator. One run per task/candidate cannot establish reliable success rates; no percentage improvement claim is warranted. If all submissions pass, the finding is that these supplied-context tasks do not discriminate the forms, not proof of equivalent general performance.

## Evolution impact matrix

These are verified implementation seams, not implemented future changes. Existing file paths were checked in this worktree.

| Change exercise | Authoritative declaration and generated surfaces | Behavioral and editing seams to assess | Compatibility question and acceptance evidence |
| --- | --- | --- | --- |
| Add an optional renderer-neutral presentation capability | `api/dashboard/main.tsp` presentation families; `api/apigen.yaml`; generated `internal/dashboard/document/models.gen.go`, `schemas/json/dashboard-document.schema.json`, OpenAPI and feature projections. Add visualization IR declaration in `api/visualization/main.tsp` only if compiled render meaning needs it. | `internal/dashboard/document/presentation_applicability.go`; `internal/dashboard/compiler/dashboard_presentation_lowering.go`, `dashboard_presentation_cartesian.go`; `internal/dashboard/authoring/reducer_visual_presentation.go`; catalog/builder behavior and render adapter tests. | Closed old schemas reject unknown authored fields. An optional field is not automatically forward-compatible. Specify omission behavior, supported marks, old-reader rejection, retained-revision replay and exported meaning. Test omitted, explicit, incompatible, null and boundary values through generation, compilation and authoring/export. |
| Add a governed query kind | `api/dashboard/main.tsp` DashboardQuery union and subtype; generated DTO/schema/API projections; distinct result-shape or IR additions only when needed. | `internal/dashboard/compiler/dashboard_query_authoring.go`, `dashboard_query_lowering.go`, `dashboard_query_helpers.go`; `dashboard_layout_canonical.go` visual/query compatibility; `internal/dashboard/authoring/reducer.go` defaults; catalog/builder, semantic query planner and governed execution qualification. | Unknown tags must reject, never infer execution from a visual mark or silently fall back. Define result aliases, legal visual bindings, budgets, filter application, export and revision behavior. Test exact query semantics and authority boundaries, not schema acceptance alone. |
| Change an omitted layout default | Existing constants at `internal/dashboard/compiler/dashboard_layout_canonical.go:18` and resolution at line36; explicit layouts remain authored data. Generated schema does not encode these runtime defaults today. | `CompileDashboardLayout` and page overrides; `internal/dashboard/ui/page.go`, `internal/dashboard/report/report.go` have corresponding initial grid values; authoring creation/layout reducer and browser builder defaults must be inventoried, not mechanically changed. | Recompiling identical omitted source could change served placement even without source changes. Decide version boundary, canonical materialization of defaults, retained compiled artifacts and revision/export meaning. Compare omitted/explicit documents before and after; existing meaning must be preserved or the breaking transition explicit. |

Immutable revision/repository surfaces include `internal/dashboard/authoring/repository.go`, `internal/dashboard/authoring/postgres/repository.go`, `internal/dashboard/document/doc.go` and source-edit application seams. Their presence does not prove a future schema reader can replay old revisions: that needs concrete version and retained-artifact tests for any selected change.

Changing a default is qualitatively different from adding an optional field. Compare resolved semantics and artifacts, not only whether both sources validate. The matrix intentionally separates generated structural authority from behavioral/compiler/editor work.

## Evidence and interpretation

Local legal-form/default evidence: `api/dashboard/main.tsp` DashboardIncludes and page/visual declarations; `internal/project/compiler/dashboard_document_test.go` real include expansion fixtures; `internal/dashboard/compiler/dashboard_layout_canonical.go` omitted defaults.

Prior art remains scoped: Perses authoring helpers combine panel/layout authoring before emitting the canonical separate collections; this supports ergonomic authoring while preserving one persisted contract. [Primary helper](https://github.com/perses/perses/blob/main/cue/dac-utils/dashboard/dashboard.cue). Rill demonstrates rows/items/defaults, not proven superior AI outcomes. [Primary Canvas YAML](https://docs.rilldata.com/reference/project-files/canvas-dashboards). Power BI demonstrates per-object files/public schemas. [Primary report format](https://learn.microsoft.com/en-us/power-bi/developer/projects/projects-report).

## Results

The nine first attempts were authored on `51022499221ad4e23f514a6db7becc7a9ef38482`. Initial qualification regraded the same frozen submissions on main `6443c25befced1da531e4076c20b5a060d359a84`. The review refresh regrades them again after merging main `f072723d3389cfaae27cb6621eb624ebd4858ca2`; neither regrading is an additional agent trial. Upstream changes do not change the canonical dashboard query/layout declarations.

All nine first attempts passed generated-schema decoding, real project compilation, exact normalized intent comparison and their assigned representation check. No repairs or compiler feedback were provided to the trial agents. Frozen preregistration/reference/oracle hashes were verified unchanged before scoring. Final source hashes are in `dashboard-evaluation/submissions.json`; machine results are in `dashboard-evaluation/results.json`.

| Representation | Create trend + KPI | Change grain/alias/sort | Edit reused visual after reorder |
| --- | --- | --- | --- |
| Explicit default layout | Pass | Pass | Pass |
| Omitted default layout | Pass | Pass | Pass |
| Confined fragments | Pass | Pass | Pass |

The expected-content comparison checks metadata, queries, titles, filters, page order, component order, references and layout. It normalizes only identity-independent visual definition ordering, source-only includes after expansion and the documented omitted layout defaults. The scorer regression tests reject wrong-target visual changes, page reordering and changed explicit defaults. Compilation by itself is not awarded a pass.

Source byte counts are raw submitted YAML, including fragment files. They are descriptive; agents can choose different whitespace, so these are not a controlled token-efficiency score:

| Trial | YAML bytes | Changed files |
| --- | ---: | --- |
| explicit-create | 1321 | evaluation.yaml |
| explicit-grain | 1338 | evaluation.yaml |
| explicit-reuse | 2004 | evaluation.yaml |
| fragments-create | 1345 | pages.yaml, visuals.yaml |
| fragments-grain | 1342 | visuals.yaml |
| fragments-reuse | 1952 | visuals.yaml |
| omitted-create | 1249 | evaluation.yaml |
| omitted-grain | 1266 | evaluation.yaml |
| omitted-reuse | 1932 | evaluation.yaml |

Per-trial elapsed time and tokens were not available, and no speed or cost improvement is claimed. There is one observation per task/form, a small supplied catalog/reference and no execution of dashboard queries. These tasks did not discriminate the existing forms; this is not proof of general equivalence or superiority. At the pilot date the broader 90-run A/B/C comparison was unexecuted; the separate completed follow-up is linked above. No new syntax prototype is being shipped.

## Local delivery decision

Retain the canonical v1 grammar for this bounded change. Deliver the demonstrated schema/example fixes, agent-authoring guidance, independent Playground comparisons and exact-input recorded compiler evidence. Existing defaults and fragments already cover the tested alternatives. A breaking simplification or new layout language needs a qualified prototype and stronger comparative evidence before a coordinated canonical decision.

The original guide is a useful failure case: its YAML shape is accepted but its unknown dimension fails the real compiler. Corrected monthly selection compiles with verified grain/alias/sort/limit. Zero spans fail earlier; positive out-of-grid placements remain compiler failures. These distinctions address actual authoring mistakes without pretending that every schema-valid document is correct.

## Reproduction

From the isolated worktree, use the documented `duckdb_arrow` tag and local build-stamping workaround where required:

```sh
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go run ./internal/app/tools/dashboardcontracttrials -qualify
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go run ./internal/app/tools/dashboardcontracttrials -output playground/dashboard-evaluation/results.json
bun run check:dashboard-contract-evidence
```

The trial submissions are preserved first attempts. Regrading them is deterministic validation, not nine new agent attempts. New trials must start from fresh recorded seeds and a new preregistration; the frozen input hashes alone are not source backups.
