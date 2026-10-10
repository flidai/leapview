# Dashboard source design evaluation

The original `manifest.json` defines a separate, frozen ten-task corpus for deterministic qualification. That corpus qualifier reports **zero agent trials**; the new comparison uses a separate experiment manifest and first-attempt records. Neither compiler acceptance nor screenshots establish agent performance or executed query values. The preserved nine-run legal-v1 discovery pilot in `../dashboard-evaluation/` is unchanged.

Each task has a precise prompt, recoverable seed source files, a canonical-v1 oracle source root, coverage declarations and at least one compiler-valid wrong-intent document. `manifest.json` records SHA-256 hashes of every prompt and source file, including a bounded copied bundled sales context. The supporting root contains one managed connection, six source declarations, two models and the sales semantic model; compilation reads declarations and does not fetch CSV files or execute queries. The snapshots derive from the bundled `dashboards/` resources, so future upstream changes cannot silently change this corpus.

Run from the repository root:

```sh
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test ./internal/app/tools/dashboardcontracttrials
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go run ./internal/app/tools/dashboardcontracttrials \
  -qualify-corpus playground/dashboard-design-evaluation/manifest.json \
  -output /tmp/dashboard-design-qualification.json
```

The qualifier requires all ten manifest tasks and frozen input bytes. Every seed passes the generated document boundary; nine seeds must compile strictly through the real project compiler. `repair-preserving-neighbors` must instead fail with the declared `nonexistent_period` diagnostic. Every final oracle must compile. Every negative must also compile but differ from its oracle, proving that compiler acceptance alone does not satisfy the requested authored intent. Qualification stops on the first failed prerequisite and exits unsuccessfully; partial JSON remains available for diagnosis.

Expanded documents use the existing pilot's canonical normalization: omitted layout resolves to 12/48/16/16, source includes disappear after real expansion, and visual definitions are indexed by stable identity. Ordered query selections, filters, pages and components remain meaningful. No new production grammar or patch engine is introduced. The second-page task preserves confined fragments in both its recoverable seed and oracle. Its normalized document comparison alone does not establish that a future agent edited only the permitted source file; trial scoring must separately enforce that source constraint.

The ten tasks cover monthly trend and KPI creation, bounded category ranking, grain/alias/sort changes, page-scoped dependent filters, second-page visual reuse, movement/resizing with authored component order, stable-ID edits after definition reorder and duplicate titles, combo mark/axis bindings, records field/limit changes, and repair without changing valid neighbors. The move task establishes authored placement and sequence only; responsive rendering needs separate browser checks. This corpus chooses the plan's allowed combo and records alternatives, and does not claim multi-dataset or pivot coverage.

Qualification JSON records manifest, complete frozen-input and supporting-resource digests; Git commit and tracked-diff digest; executing binary digest, Go version and Go build information. The binary fingerprint also identifies builds containing untracked local Go changes. Reports identify the exact qualification build and may differ across machines or dirty worktrees. They are not frozen agent results.

## Concrete comparison and future growth

`prototypes.ts` implements isolated source formats, outside every production import path:

- A keeps canonical v1.
- B uses generated visual-specific shapes, explicit aggregate category/series roles, and derives presentation family from the visual mark.
- C keeps canonical queries/visuals and lowers named rows to explicit placements while preserving the component reading order.

The schemas derive unchanged public fields from the generated canonical contract. These are bounded comparison prototypes, not competing persisted product grammars. B covers the five marks and aggregate/records pairings used by this corpus. C supports deterministic row blocks with explicit gaps, spans and within-row offsets; complex repeated-column staggered blocks reject. Those limitations count against replacing the full canonical format.

`prototypes.test.ts` exercises all 62 B/C seed/oracle/negative round-trips, strict rejection, ordered selections, stable identities, source confinement and null/empty/zero distinctions. `controller.ts` additionally checks 93 A/B/C fixture cases through the real Go project compiler and frozen behavioral oracle before preparing any trial manifest. The compiler-invalid repair seed remains intentionally invalid.

Each candidate gets the same two semantic examples, mechanically encoded into its source form, common guidance, generated root/fragment schemas, sales resources and task wording. Candidate syntax and the size/shape of its reference bundle are the treatment; tools and task semantics are held constant. This is not a tool-access comparison.

The scored protocol is ten tasks × three candidates × three fresh-context repetitions, with shuffled order, normal configured model/effort, 240 seconds and 12 observed tool calls per attempt. First output is frozen before grading. Repair budget is explicitly zero; there is no compiler or oracle feedback during authoring. Process, provenance, source constraints and transcript contamination are independent failure gates. Every planned cell remains in the denominator. Filesystem read boundaries are instructed and audited, not physically enforced.

For C, preexisting page-local row identities must remain on tasks that preserve row structure. The move/resize task explicitly permits regrouping or removing layout rows while retaining visual, component and page identities. Canonical lowering does not by itself prove this source-level constraint.

`evolution/` and the real compiler-adapter tests execute three isolated growth probes: an authored benchmark capability, a governed ranking query tag, and a versioned omitted-gap change with explicit old-default migration. They demonstrate reader rejection, compiled bindings, canonical export and retained-meaning behavior; they do not establish B/C evolution superiority or implement a future production version/query engine.

Reproduce the preparation from the repository root:

```sh
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go build \
  -o /tmp/dashboard-design-grader ./internal/app/tools/dashboardcontracttrials
bun playground/dashboard-design-evaluation/controller.ts prepare \
  /tmp/dashboard-design-grader /tmp/dashboard-design-experiment-new
python3 scripts/dashboard-design-trials.py \
  --manifest /tmp/dashboard-design-experiment-new/experiment.json \
  --work-root /tmp/dashboard-design-authors-new \
  --output-root /tmp/dashboard-design-results-new --concurrency 3
```

The final command is a preflight by default. `--execute` deliberately starts new, potentially costly inference; regrading preserved first attempts does not require new inference. Exclusive reservations prevent reusing or overwriting any attempt. Use new directories for a new experiment. Snapshot grader controls remain outside all author workspaces.

An initial cohort was stopped when the harness mistook normal CLI teardown for an incomplete authoring result. Its eleven attempts and failure records are preserved; none is reused as a passing attempt. The corrected runner freezes source on the first completed turn and records controlled shutdown separately. A distinct real filesystem probe passed before the separately registered replacement comparison started. The [completed comparison](comparison/README.md) reports all 90 fresh attempts, both cohorts and the exact evidence hashes. A, B and C each passed 30/30; the recommendation retains canonical v1. The three evolution probes also passed through the real compiler adapter.
