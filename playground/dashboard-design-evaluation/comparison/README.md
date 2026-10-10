# Dashboard source design comparison — executed evidence

Retain the canonical v1 authoring contract for this change. Jacob's aim is a contract that agents can author correctly and that LeapView can evolve safely. In this bounded comparison, **A, B and C each passed all 30 first attempts**: all 90 submissions met the assigned prototype boundary, real compiler, exact intended document, preservation, process and integrity gates. The grader reports no global issues. Equal observed success leaves no correctness advantage to justify a coordinated product grammar change. It does not prove that canonical v1 is optimal.

B offers a plausible direction for a later measured simplification: explicit category/series roles and visual-specific fields remove a redundant presentation discriminator. Its resource totals were somewhat lower in this sample, while source size and edit spans grew. C expresses row composition directly but adds row identities, gaps, spans and within-row offsets. Both are limited prototypes over five marks and aggregate/records use cases; B is not qualified for the full canonical visual/query union, and C rejects complex repeated-column staggered blocks. Adopting either would require coordinated declarations, generated consumers, builder/agent APIs, editing/export, revision compatibility and capability coverage. The present evidence does not justify that rollout cost.

## What was compared

The ten task archetypes below each received three fresh-context repetitions in each candidate: 10 × 3 × 3 = 90. A uses canonical v1. B seals area/bar/combo/kpi/table branches, uses explicit aggregate category/series roles, retains explicit query kinds and derives presentation family from the mark. C retains canonical visual/query semantics, replaces component placements with named row blocks and lowers them back to exact canonical placements without reordering components. Source filenames, confined includes, shared visual references and existing stable identities remain meaningful.

| Task archetype | A | B | C |
| --- | --- | --- | --- |
| Create monthly trend and KPI | 3/3 | 3/3 | 3/3 |
| Create bounded ranking | 3/3 | 3/3 | 3/3 |
| Change grain, alias and sort | 3/3 | 3/3 | 3/3 |
| Dependent filter and target scope | 3/3 | 3/3 | 3/3 |
| Reuse a visual through page fragments | 3/3 | 3/3 | 3/3 |
| Move/resize, retain reading order | 3/3 | 3/3 | 3/3 |
| Edit stable ID after definition reorder | 3/3 | 3/3 | 3/3 |
| Combo mark/axis bindings | 3/3 | 3/3 | 3/3 |
| Record fields, sort and limit | 3/3 | 3/3 | 3/3 |
| Repair a query, preserve neighbors | 3/3 | 3/3 | 3/3 |

The [untouched final grader report](graded-results-final.json) contains the complete 30 task/replicate contrasts and all 90 records. The task matrix is entirely tied at success: each task has 3/3 in every arm, so there is no observed within-task success variance. [Resource statistics](resource-summary.json) include per-arm sample spread and per-task values; three repetitions over these chosen tasks do not establish performance on a broader authoring population.

Before authoring, 31 fixed cases × three candidates = **93 deterministic qualification cases** passed: 10 seeds, 10 oracles and 11 valid-but-wrong negatives per arm. These are not 93 additional agent trials. All shape boundaries accepted their intentionally representable fixtures; nine seeds compiled, while the repair seed retained its intentional unresolved member. Oracles compiled and matched intent; every negative compiled but failed exact intent. B/C round-trips preserved all authored source values before these real Go checks. Compiler acceptance alone therefore could not award success.

## Controls and provenance

The raw [replacement preregistration](preregistration.json), [qualification report](prototype-qualification.json) and [execution identity](execution-identity.json) are preserved byte-for-byte. Each candidate received the same two semantic examples, mechanically encoded into its syntax, identical common rules and task wording, frozen sales resources, and candidate root/fragment schemas. The treatment includes both syntax and its authoring/reference bundle. The bundles were A 471,463 bytes, B 600,973 bytes and C 475,446 bytes; a pure syntax-only causal effect was not isolated.

The recorded normal CLI configuration was `gpt-6.1-sol`, reasoning effort `high`, service tier `default`, using Codex CLI 0.160.1. The runner did not override the model per arm; JSONL records usage and distinct thread identity, rather than independently emitting the resolved model. Dispatch order was shuffled with seed 93820261009 and concurrency three. Every cell used one ephemeral CLI authoring turn, a 240-second limit, at most 12 observed tool calls and **zero feedback/repair rounds**. The deliberate repair task tests a first-attempt repair instruction; the experiment does not measure iterative repair quality.

The [initial score](graded-results.json) and its matching `dispatch/grade-trials.ts.txt` are retained unchanged. The final regrade uses the strengthened criterion that complete preexisting C row objects—not just their IDs—remain unchanged outside the move/resize task. This makes the common instruction to preserve unmentioned fields explicit, including unused authored row height. The focused grader suite passes 14 cases with 58 assertions. An independent Astra audit also found all 60 preexisting row objects in the 27 non-move C attempts deeply identical to their seeds. The final score still records A/B/C 30/30 with no global issues; no authored source, trial input or attempt was changed or rerun. Its exact grader is archived as `dispatch/grade-trials-final.ts.txt`.

The first completed author turn was frozen before any grading. Transcripts and source inventories were audited for contamination, unapproved source changes, tool budgets and provenance; all 90 final gates passed. Filesystem reads were bounded by instructions and audit, not physically blinded from host sibling files. Authors did not receive the oracle or compiler feedback. An offline CLI authoring evaluation is not a live product LLM integration test.

An earlier cohort was stopped after 11 attempts when the runner mistook normal post-turn CLI teardown for an incomplete authoring result. The **entire cohort was excluded before replacement dispatch**, not selected by source quality; all 11 outcome records remain in [aborted-cohort.json](aborted-cohort.json) and the full artifact root. No earlier source or passing outcome was reused. A separate real filesystem probe qualified the corrected terminal-freeze behavior, then all 90 replacement cells received fresh attempts. The old planned cohort is reported separately rather than blended into the replacement denominator.

The grader also discloses a preparation-controller hash change between qualification and dispatch. Exact archived controller sources show both stages; the change affected frozen corpus/prompt preparation and policy, while the qualified B/C lowerer remained unchanged. The raw replacement manifest and frozen assigned inputs govern the scored cohort. This disclosure is preserved in `preparationProvenance` in the untouched report.

## Descriptive resource observations

Values come from all 30 recorded attempts per arm, not only a selected subset. Elapsed sums/means describe author-turn durations and **are not the whole experiment's wall-clock time**. Input usage includes cached input. Parallel scheduling, service variation and cache behavior were not standardized enough to infer a general speed or cost winner.

| Arm | Elapsed seconds, mean / median | Tool calls, total / median | Input tokens, total | Output tokens, total / median | Final source bytes, total | Edited byte span, total |
| --- | --- | --- | --- | --- | --- | --- |
| A | 24.76 / 20.81 | 134 / 4 | 3,783,606 (3,175,808 cached) | 15,646 / 417.5 | 45,954 | 20,139 |
| B | 23.06 / 21.31 | 120 / 4 | 3,439,688 (2,843,520 cached) | 14,567 / 407.0 | 50,448 | 22,320 |
| C | 23.98 / 21.03 | 121 / 4 | 3,593,979 (2,980,864 cached) | 15,070 / 374.5 | 55,291 | 24,819 |

B's total author-turn duration was 691.84 seconds versus A's 742.73 seconds, and its output usage was 14,567 versus 15,646 tokens. Yet B's median duration was 21.31 seconds versus A's 20.81 seconds. Within the 30 same-task/replicate contrasts, B had lower duration in 18 and higher in 12 (mean difference −1.70 seconds); lower output usage in 21 and higher in nine (mean difference −35.97 tokens). C had lower duration in 15 and higher in 15 (mean difference −0.78 seconds). Pair labels align task and repetition; they do not replay the same stochastic trajectory.

All three arms had a median of four observed tool calls. B and C produced more final source bytes than A in every paired contrast. Their edited byte spans were greater in 24/30 and 27/30 contrasts respectively, with three ties each. These mixed observations support describing B's somewhat lower aggregate resource effort in this sample, not declaring a syntax winner, a significant difference, a dollar cost saving or universal speed improvement.

`editedBytes` uses this exact algorithm per seeded dashboard file: remove the common byte prefix, then the common suffix without crossing the prefix; add the remaining **old plus new** lengths and sum across files. It is an approximate changed byte span, **not minimum edit distance, keystrokes or semantic edit count**. Distant small changes or formatting can produce a large span. Final source bytes likewise reflect the candidate form, not the normalized runtime document.

## Evolution and product scope

The [executed evolution probes](../evolution/README.md) independently exercise adding a benchmark presentation shorthand, a governed ranking query tag, and an experimental version whose omitted gap changes from 16 to eight. Their closed future reader lowers to current canonical v1 and reaches the actual generated decoder/compiler. Tests establish explicit rejection, intended bindings, canonical compatibility export and old-default materialization on migration, while preserving retained v1 meaning and hashes. The capability/query probes reuse existing reference-line/aggregate IR; they introduce no rendering primitive or query execution engine. They are **canonical growth probes, not evidence of B/C evolution superiority** or shipping support for an experimental version.

This PR's production contract work adds twelve explicit minimum constraints for existing layout/default/placement fields and generated documentation, aligning structural checks with existing layout requirements; it does not change the 12/48/16/16 default values or introduce B/C runtime syntax. The dashboard guide's starter now selects the real `purchase_date` member at month grain and uses `purchase_month` as an alias; the guide is compiled against bundled sales definitions. Current-v1 export/edit/revision regression tests add compatibility evidence. All B/C codecs, experiment controls and hypothetical future-reader code remain offline tooling or tests.

The Playground adds a review surface for schema behavior and cached real compiler fixture evidence. That evidence carries source/compiler fingerprints and can be regenerated; editing arbitrary YAML performs structural validation, not a new live Go compilation or governed data query. Browser fixtures and cached compiler records establish their stated UI/compiler behavior, not the numerical truth of a chart. This comparison executes no data queries and makes no claim about live product agent workflows, query result values, complete renderer parity, responsive equivalence of C, historical released-reader compatibility or deployed future revision persistence.

## Portable evidence and remaining artifact scope

`sources/<trial-id>/dashboards/` contains all **108 first-attempt files from the 90 replacement attempts**, copied without rewriting from their terminal-frozen inventories. [inventory.json](inventory.json) records each copied byte count and SHA-256 and verifies it against the original result inventory; it also fingerprints the untouched initial and final scores, controls and archived implementation sources. Files in `dispatch/` end in `.ts.txt` or `.py.txt` so archived snapshots do not enter TypeScript or Python test discovery.

This is a portable review snapshot, not a self-contained rerun package: the untouched preregistration intentionally retains its original absolute artifact paths. Full frozen reference/seed controls, event/stderr transcripts, reservations, probe and excluded-cohort evidence remain in the complete task artifact roots (`/tmp/pr938-design-trials`, `/tmp/pr938-design-trials-v2`, `/tmp/pr938-design-results-v2`, `/tmp/pr938-design-results` and `/tmp/pr938-runner-terminal-probe`). The complete local bundle is `pr938-jacob-design-evaluation.zip` (50,992,874 bytes, SHA-256 `110abee41dc66d33ecf0bef1eea5fb9c542af3ad643b8fecfaaa163a1beab430`), attached in the task response. It contains the exact Linux grader binary and both cohorts, with original `/tmp` root names preserved as archive-relative paths. Final CI is reported separately in the PR; the bundle was captured while CI was running. No hosted evidence URL or final CI success is invented here. The report preserves observed results and explicit limits without dispatching new inference.
