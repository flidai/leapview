# Executed dashboard evolution experiments

These are evaluation-only reader and lowering experiments. `leapview.experiment/v2` is not a product API version, and no production declaration, compiler behavior, or layout default changes here. The “current reader” is the generated reader in this checkout, not a claimed older released binary. The retained v1 fixture is a qualification capture, not a production database export.

The executable tests are `internal/dashboard/authoring/compileradapter/evolution_experiments_test.go`. They derive a complete, closed future JSON Schema from the current generated Dashboard schema and the additions in `reader-contract.json`. The future reader validates the entire source before lowering it into canonical v1, then runs the generated Go decoder and real compiler adapter. Semantic models and runtime leases use the existing compiler test fixture; no query data is executed and no future syntax is accepted by production.

## Experiments and observed results

| Exercise | Isolated authored behavior | Actual qualification |
| --- | --- | --- |
| Optional presentation capability | A line/area presentation may contain `benchmark: {value: 0, label: Target}`. Omission adds no marker. The reader lowers it to one neutral `primary_y` numeric reference line with stable ID `evolution-benchmark`. | The actual visualization IR contains that zero-valued marker. Query bindings, neighboring KPI and page placements remain identical. Absent versus empty labels remain distinct; null/empty objects, extra keys, incompatible marks and simultaneous `referenceLines` (including `[]`) reject. |
| New governed query tag | `type: topCategories` takes one semantic `dimension`, one `metric`, and required integer `limit` in 1–1000, on bar/column only. Lowering produces aggregate selection with metric descending, dimension ascending as a deterministic tie-break, and the exact limit. | Actual compiled bindings retain the selected dimension/metric, sort sequence and limit. Boundaries 1 and 1000 compile; zero, negative, excessive, missing, null and string limits reject. Empty/null references, SQL extensions, unknown tags and incompatible marks reject. An unknown semantic dimension passes shape validation but fails the real compiler. |
| Changed omitted default | Under the experimental version only, omitted dashboard layout resolves to columns 12, row height 48, gap 8 and padding 16. V1 still uses its existing gap 16. | The retained overview’s compiled height changes from 272 to 248 pixels on naive version opt-in. Its page with explicit zero gap/padding remains unchanged. Explicit root zero gap yields 224 pixels. Empty page overrides inherit; null and incomplete root layouts reject. |

The first two additions reuse capabilities already expressible in the existing visualization/query IR. They test adding a new authored capability and governed query discriminator with explicit lowering; they do not implement a new rendering primitive, aggregation algorithm or query engine. That choice makes the compatibility claims executable against the actual compiler rather than asserting hypothetical engine behavior.

Every unlowered future document is outside the current contract. Tests also place the new property/tag under the existing v1 version: both the current and future readers reject this attempt to smuggle future syntax into v1. Unknown future versions reject; readers never infer a query kind from the visual mark.

## Preservation and migration

The future reader routes v1 documents through the unchanged generated current decoder. The retained authored document, omitted layout and literal `retainedV1ContentHash` remain unchanged. Optional-capability omission is compared with explicit old defaults to isolate that exercise from the separate new-default experiment.

The explicit migration first decodes a valid v1 document. If dashboard layout is omitted, it materializes the old 12/48/16/16 values before opting into the experimental version. Authored explicit values, including zero, remain intact. A malformed source or already-future document cannot be passed through this v1 migration.

Lowered compatibility exports are real canonical v1 YAML: the benchmark becomes a reference-line declaration, ranking becomes an aggregate query, and future omitted defaults become explicit values. The generated reader accepts these exports and the real compiler reproduces the same serving meaning. This is a semantic compatibility export; it intentionally does not retain the original future shorthand.

For the default migration, the tests construct, serialize, deserialize and validate a new immutable canonical revision. Its authored hash differs because omission became explicit, while its compiled meaning equals the original. The retained old revision is reloaded with its original hash and original omission, and recompiles with its original geometry. Existing `TestRetainedV1RevisionExportEditAndRestorePreserveCompiledMeaning` separately exercises the actual revision-restore reducer. No future revision storage format is invented or claimed to be production-ready.

## Production changes required before adoption

| Selected change | Concrete production work still required |
| --- | --- |
| Optional benchmark capability | Add the declaration/version policy in `api/dashboard/main.tsp`; regenerate DTOs, schema, OpenAPI and authoring projections through `api/apigen.yaml`. Add applicability to `internal/dashboard/document/presentation_applicability.go` and lowering at the existing dashboard presentation/context seams. Specify conflict behavior and stable marker identity; add builder/agent capability guidance and editor/export tests. The chosen lowering needs no new visualization IR field. |
| Governed ranking tag | Add the tagged query declaration to `api/dashboard/main.tsp`; regenerate the query union, DTO/schema/API consumers. Add supported visual/query combinations and deterministic lowering in the dashboard query compiler. Update authoring reducer defaults, builder/catalog discovery and agent guidance. The chosen ranking semantics need no new execution engine, but real governed execution and result tests are still needed before shipping. |
| Changed omitted default | Choose a real supported resource-version boundary and reader dispatch. Preserve v1 default resolution in `internal/dashboard/compiler/dashboard_layout_canonical.go`; materialize old defaults on explicit migration, rather than changing the global constant for every retained document. Wire version-aware canonical export/revision import, and reconcile builder/report/UI creation defaults. Verify database-backed revision replay, actual responsive rendering and deployment behavior for the selected migration. |

A production version change also needs coordinated revision validation, source editing, API/agent contracts and generated consumers. This test-only schema extension does not demonstrate that those generated outputs already support the hypothetical version. Closed schemas establish rejection, not automatic forward compatibility.

## Reproduce and interpret

From the repository root:

```sh
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test -count=1 \
  ./internal/dashboard/authoring/compileradapter -run '^TestEvolution' -v
GOFLAGS='-tags=duckdb_arrow -buildvcs=false' go test -count=1 \
  ./internal/dashboard/authoring/compileradapter
```

`results.json` records the local qualification and input hashes. The tests first failed with a future reader that delegated unchanged to the production reader, then passed after implementing the explicit versioned reader/lowering/migration. The final run includes the existing compileradapter/retained-v1 regressions.

These results establish concrete declaration, compatibility-export and default-migration behavior for the three stated experiments. They add no agent trials and make no claim about a deployed future feature, executed query values, historical released-reader compatibility, database persistence of future syntax, or completion of the broader A/B/C authoring comparison.
