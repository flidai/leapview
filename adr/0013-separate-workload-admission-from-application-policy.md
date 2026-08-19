# ADR-0013: Separate workload admission from application policy

Status: accepted

Decision date: 2026-08-19

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Related: [ADR-0005](0005-use-project-wide-resource-graph.md),
[workload-admission conformance](specifications/workload-admission-conformance.md),
[LEA-376](https://linear.app/leapstack/issue/LEA-376/replace-workspace-workload-fairness-with-class-principalgroup-and),
and the [ADR-0013 Linear project](https://linear.app/leapstack/project/adr-0013-reusable-workload-admission-b299c3b038db)

## Context and problem statement

LeapView admits interactive queries, background work, refreshes, control-plane
operations, and maintenance through one instance-local workload controller. The
controller already implements valuable application-neutral mechanics:

- bounded instance, class, principal, and group concurrency;
- bounded instance, class, principal, and group memory accounting;
- ordered class scheduling with reserved capacity and borrowing;
- actor-fair queues within each class;
- queue and execution deadlines;
- cancellation, nested admission, shutdown, statistics, and observations; and
- deterministic clock injection for tests.

The implementation nevertheless lives under `internal/workload` and combines
those mechanics with LeapView's class vocabulary, class ordering, default
limits, configuration, metric projections, and lifecycle wiring. Other
applications cannot reuse the controller without copying it, while changes to
LeapView policy can accidentally alter generic scheduling behavior.

ADR-0005 and LEA-376 removed workspace partitioning in favor of class,
principal, group, and instance limits. That behavior has now established a
coherent mechanism which can be qualified independently. Durable jobs and
analytical runtimes also benefit from depending on a small admission contract
rather than a product configuration package.

The question is how LeapView can preserve one production admission path while
making the scheduling mechanism reusable and keeping product classification,
authorization, defaults, and telemetry policy inside the application.

## Decision drivers

- Admission correctness must remain deterministic under concurrency,
  cancellation, timeout, release, and shutdown races.
- Spare capacity must be borrowable; reserved capacity must not remain idle
  merely because its owning class has no eligible work.
- A noisy principal or group must not starve other eligible actors.
- Applications must choose their own workload classes, order, limits, identity
  semantics, observations, and operating defaults.
- The reusable package must not authorize requests, interpret application
  resources, persist jobs, export vendor metrics, or know LeapView concepts.
- One qualified scheduler must remain authoritative after the cutover.
- The package must be usable and testable without importing an application
  package or starting the LeapView process.
- Extraction must not require a new repository, nested module, distributed
  queue, or compatibility framework.

## Considered options

### Keep the controller under `internal/workload`

This preserves the current layout but leaves a mature, capability-neutral
mechanism inseparable from LeapView policy. Other packages can share it only
through an application-owned import, and reuse outside this module requires a
copy. This option was rejected.

### Move `internal/workload` wholesale to `pkg/workload`

This is mechanically simple but would publish LeapView's classes, order,
defaults, metric assumptions, and zero-value fallback as if they were generic
policy. Future applications would either inherit unsuitable behavior or add
conditionals to the package. This option was rejected.

### Publish a standalone module or service immediately

A nested module, separate repository, or admission service would add release,
versioning, networking, and operational boundaries before the in-repository API
has proven stable. LeapView needs an embedded process-local controller, not a
distributed scheduler. This option was rejected for the initial extraction.

### Separate the reusable mechanism from LeapView policy

`pkg/workload` owns the application-neutral scheduler and its precise lifecycle
contract. `internal/workload` owns LeapView classification, defaults,
configuration, telemetry adapters, and process wiring. The package is qualified
before production consumers adopt it, and the old scheduler core is removed in
one final cutover. This option is selected.

## Decision outcome

LeapView will introduce `github.com/flidai/leapview/pkg/workload` as an
in-repository reusable package. It is designed for LeapView first but must be
liftable into another Go application without bringing LeapView dependencies or
policy with it.

### Reusable package responsibilities

`pkg/workload` owns:

- validated, explicitly ordered host-defined class identifiers;
- instance and per-class running, queued, and memory limits;
- per-principal and per-group running, queued, and memory limits;
- class reservations, safe borrowing of unused capacity, and deterministic
  class scheduling;
- fair actor rotation within a class and starvation resistance for eligible
  work;
- canonical copying and comparison of opaque principal and group identifiers;
- request validation for class membership, operation identity, and positive
  memory estimates;
- queue deadlines, execution deadlines, parent cancellation, and typed
  rejection reasons;
- reference-safe admission leases, same-admission nesting, conflicting nested
  admission rejection, and idempotent release;
- deterministic controller shutdown and rejection of new or queued work after
  shutdown;
- immutable statistics snapshots and application-neutral observation events;
  and
- injectable time for deterministic tests.

The package may define interfaces and small adapters needed to express these
mechanics. It contains no built-in worker, job repository, database, HTTP
handler, metrics exporter, configuration loader, or application service.

### Explicit configuration

The host supplies the complete class sequence and limits. Class ordering is a
slice or another explicitly ordered contract; map iteration never determines
scheduling. Empty or duplicate class identifiers and inconsistent limits fail
construction.

The generic package has no LeapView `DefaultConfig` and does not interpret an
all-zero configuration as application defaults. Limit zero semantics are
explicit:

- zero class `MaximumRunning` disables execution for that class;
- zero instance or class `MaximumQueued` disables queuing at that scope;
- zero memory limits and zero per-principal/per-group limits mean no additional
  limit at that scope; and
- zero queue or execution duration disables that deadline.

Reservations may not exceed class maxima, their sum may not exceed instance
capacity, and reservation priority never prevents another class from borrowing
currently unused capacity.

### Application responsibilities

`internal/workload` remains the LeapView policy boundary and owns:

- the `interactive`, `background`, `refresh`, `control`, and `maintenance`
  vocabulary and its scheduling order;
- production default limits and environment/configuration mapping;
- the decision that maps an authorized operation to a class, principal, groups,
  operation label, and memory estimate;
- Prometheus/OpenTelemetry names, stable label vocabularies, aggregation, and
  exporter integration;
- module construction, process lifecycle, and composition adapters; and
- any LeapView-specific error translation or operator presentation.

Authentication and authorization occur before admission. The generic package
treats principal, group, class, and operation strings as opaque bounded
identifiers and never grants application permission.

### Dependency and delivery rules

`pkg/workload` must not import any `internal/` package. Initial package work
targets `main`, remains outside production call paths, and may proceed in
parallel with ADR-0012. It stays in the root Go module until a proven external
distribution need justifies another decision.

LeapView adoption begins only after the generic package independently satisfies
the companion conformance specification. The final production cutover removes
the superseded scheduler implementation and any temporary adapter that
duplicates scheduling behavior. `pkg/jobs` may later consume the stable
admission interface, but durable job behavior is outside this decision.

## Consequences

Positive consequences:

- scheduling and lifecycle correctness can be qualified without application
  startup, persistence, DuckDB, or browser dependencies;
- LeapView policy changes no longer require modifying the scheduler core;
- other Go applications can embed the controller with their own classes and
  observations;
- jobs and analytical runtimes can depend on a narrow stable mechanism; and
- the package boundary makes fairness, resource accounting, and shutdown
  invariants easier to review and fuzz.

Negative consequences and costs:

- class configuration must become explicit, so zero-value convenience is lost;
- the extraction requires a temporary non-production package phase followed by
  an atomic application cutover;
- generic observations cannot assume LeapView metric names, requiring an
  adapter; and
- a public package raises the compatibility cost of future API changes even
  while it remains in the same repository.

The decision does not create distributed fairness. Controllers in different
processes do not coordinate capacity. A future multi-process admission design
would require a separate ADR.

## Confirmation

The [workload-admission conformance specification](specifications/workload-admission-conformance.md)
defines the numbered package, configuration, scheduling, actor, lifecycle,
observation, robustness, and application-integration requirements.

Conformance is demonstrated by:

- an architecture test prohibiting `internal/` imports and LeapView policy in
  `pkg/workload`;
- deterministic tests over every class and actor scheduling branch;
- starvation, reservation borrowing, limit, cancellation, timeout, nesting,
  release, and shutdown property tests;
- race and fuzz suites with bounded execution;
- application policy tests proving LeapView classes and defaults remain
  outside the package;
- one final production call path with no superseded scheduler; and
- focused tests plus `task ci` after the cutover.
