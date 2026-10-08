# Canonical v1 design-evaluation prerequisites

This directory contains a separate, frozen ten-task corpus for deterministic qualification. It contains **zero agent trials** and makes no agent-performance, optimality, speed or query-execution claim. The preserved nine-run legal-v1 discovery pilot in `../dashboard-evaluation/` is unchanged.

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

## Work still required before the original comparison

The original A/B/C candidates are current canonical syntax with improved support, a coordinated structural simplification, and composition/layout improvements. The old explicit/omitted/fragments discovery forms are not implementations of those three candidates.

No executable B or C prototype is supplied here. Their precise schemas, deterministic lowering to the canonical document, common capability coverage, examples and equivalent authoring references must be designed and qualified against these oracles before any scored run. A new comparison manifest must also define expected trial IDs, source constraints, exact model/effort/tools/budgets, randomized dispatch, first-submission capture and repair rules. New trials need fresh isolated seeds and a new preregistration; these oracle/negative files are grader-only inputs and must not be exposed to trial agents.

The proposed ten tasks × three repetitions × three candidates = 90 runs remain unexecuted. The evolution exercises—adding a presentation capability, adding a query kind and changing a default—also remain unimplemented. Governed synthetic-data execution, retained-revision/export compatibility and responsive/browser evidence remain necessary for their respective claims. This prerequisite does not select a replacement grammar or complete those experiments.
