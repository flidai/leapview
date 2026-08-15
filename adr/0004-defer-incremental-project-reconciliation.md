# ADR-0004: Defer incremental project reconciliation

Status: accepted

Decision date: 2026-08-05

Implementation: deferred pending corrected measurement

Deciders: LeapView maintainers

Amended by: [ADR-0005](0005-use-project-wide-resource-graph.md) supersedes
workspace-scoped identity and fixture assumptions; the measurement gate and
atomic publication decision remain active

## Decision

LeapView continues to capture, compile, validate, and publish one complete
immutable project generation. We will not put incremental reconciliation on
the production path until the corrected benchmark protocol has produced a
controlled reference run and a prototype demonstrates the required end-to-end
gain.

This is not a finding that whole-project compilation is fast enough. The first
measurement used comment-only edits, reported benchmark means as though they
were p95 samples, and represented too few resource kinds. Those results cannot
support either implementing or rejecting an incremental compiler. Keeping the
existing path while the gate is re-measured preserves the critical invariant:
an invalid edit leaves the last valid candidate active, and a valid edit
becomes visible only after every resource and cross-resource dependency has
validated.

## Measurement gate

An affected-resource prototype is justified only when both conditions hold on
an idle reference developer machine or a dedicated CI worker:

1. Whole-project coherent build latency exceeds a 2 second p50 or 3 second p95
   for a supported project (currently up to roughly 250 authored resources),
   or exceeds 25% of the end-to-end edit-to-preview budget.
2. The common semantic single-resource edit has an estimated affected
   dependency closure no larger than 25% of compiled assets, making at least a
   2x end-to-end improvement plausible after mandatory global validation and
   immutable generation assembly.

The first condition prevents a dependency engine from being introduced to
optimize work already below the interaction budget. The second prevents
“incremental” bookkeeping from retaining most of the full compile cost.

## Corrected benchmark protocol

The committed compiler benchmark builds valid small, medium, and large
projects. The large fixture contains 244 authored resources and covers global
connections and sources plus workspace models, semantic models, dashboards,
access groups and bindings, refresh pipelines, and dashboard publications.
The dev-loop benchmark uses the current showcase project.

Each edit scenario alternates complete valid YAML documents rather than
appending comments. The benchmark fails if a semantic edit does not change the
compiled project digest. Mutation remains outside the timed region; coherent
capture, parsing, compilation, cross-resource validation, lineage extraction,
and immutable project assembly remain inside it.

Each benchmark reports:

- `p50-ms` and `p95-ms` from individual operation durations;
- `affected-assets` and `affected-pct`, the observable compiled content-hash
  delta;
- `closure-assets` and `closure-pct`, that delta expanded through reverse
  lineage dependencies; and
- allocations and authored-resource count.

The closure is an evidence-based estimate from the compiler's emitted lineage,
not proof of achievable reuse. A prototype still has to trace mandatory global
validation and assembly work.

Run each large scenario in a fresh process so retained Go heaps and GC work do
not contaminate later scenarios. Use at least ten operations and five process
runs for a decision:

```sh
go test ./internal/project/compiler -run '^$' \
  -bench '^BenchmarkWholeProjectCompilation/large/no_edit$' \
  -benchtime=10x -count=5 -benchmem

go test ./internal/project/compiler -run '^$' \
  -bench '^BenchmarkWholeProjectCompilation/large/(leaf_dashboard_edit|workspace_access_edit|shared_source_edit|multi_dashboard_edit)$' \
  -benchtime=10x -count=5 -benchmem

go test ./internal/project/devloop -run '^$' \
  -bench '^BenchmarkFilesystemBuilderCoherentSnapshot/(no_edit|single_dashboard_edit|multi_dashboard_edit)$' \
  -benchtime=10x -count=5 -benchmem
```

Record CPU model, available disk space, other CPU-intensive processes, Go
version, and commit. Reject a run when the worker is under sustained unrelated
CPU, memory, or storage pressure. The protocol-validation run on 2026-08-05 was
discarded for the decision because unrelated processes and a 97%-full data
volume produced order-of-magnitude latency drift between scenarios.

## Outcome matrix

| Latency gate | Closure gate | Action |
|---|---|---|
| Not crossed | Either | Keep whole-project compilation; profile allocation churn first. |
| Crossed | Over 25% | Keep whole-project compilation; reduce global work and improve graph precision. |
| Crossed | At or below 25% | Build a Project-owned incremental prototype and compare end-to-end latency. |

No production implementation is authorized by benchmark results alone. The
prototype must beat the whole-project path by at least 2x at p50 without
regressing p95, diagnostics, cancellation, or atomic publication.

## Required prototype design

Any prototype remains behind a Project-owned internal boundary and must satisfy
all of these constraints:

- **Identity:** resource identity is API version, kind, workspace scope, and
  canonical metadata name—not filename. Source paths remain change evidence.
- **Invalidation:** a content or identity change invalidates the resource and
  all reverse-dependency descendants. Compiler-version and relevant global
  option changes participate in cache keys and invalidation.
- **Errors and status:** pending, compiling, valid, and invalid status remains
  build-local. No partial state becomes serving state; the last valid candidate
  stays active.
- **Rename and delete:** reconcile deletion of the old identity before creation
  of the new identity, invalidate both closures, and reject duplicate identities.
- **Cancellation:** a newer coherent capture cancels obsolete work. Canceled
  work cannot populate reusable caches or replace a newer result.
- **Atomic publication:** independently compiled resources are reassembled,
  globally validated, assigned one digest, prepared, verified, and activated as
  exactly one immutable serving-state generation.

Avoid a global reconciler registry and never expose a mutable partially
reconciled catalog to requests.

## Revisit signals

Run the corrected matrix on a controlled worker, when the supported project
envelope changes, and after material compiler optimizations. If the gate is
crossed, open the prototype as a separate implementation issue with the raw
benchmark output attached.

## Confirmation

The production compiler must continue to publish one complete immutable project
generation. Architecture tests must reject partial serving-state publication.
Introducing incremental compilation on the production path requires the
measurement and prototype evidence defined here and a superseding ADR.
