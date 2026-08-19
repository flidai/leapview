# Workload admission conformance specification

Status: accepted

Last updated: 2026-08-19

Owners: LeapView maintainers

Governing decision: [ADR-0013](../0013-separate-workload-admission-from-application-policy.md)

Delivery authority: [ADR-0013 Linear project](https://linear.app/leapstack/project/adr-0013-reusable-workload-admission-b299c3b038db)

## Purpose

This specification defines the evidence required to extract and maintain the
reusable workload-admission mechanism selected by ADR-0013. It is mutable as
tests and package APIs evolve; the architectural boundary in the ADR remains
the governing decision.

The terms **must**, **must not**, **should**, and **may** are normative. A
requirement is complete only when the Linear delivery issue records a passing
test, architecture check, generated check, or explicit reviewed deferral.

## Package boundary

- **PKG-01:** All generic admission code lives under `pkg/workload`; it imports
  no path beneath this repository's `internal/` tree.
- **PKG-02:** The package contains no LeapView class constants, default limits,
  configuration keys, metric names, routes, resource types, or operation
  allowlists.
- **PKG-03:** The package performs no authentication, authorization,
  persistence, SQL execution, worker dispatch, network access, or ambient
  filesystem access.
- **PKG-04:** The initial package remains in the root Go module and introduces
  neither a nested module nor a separate repository.
- **PKG-05:** Package tests compile and run without importing or constructing a
  LeapView application package.
- **PKG-06:** Public errors and rejection reasons are typed or mechanically
  inspectable; callers do not need to parse error text.
- **PKG-07:** Public values returned by the controller do not expose mutable
  internal maps, queues, waiters, or counters.
- **PKG-08:** One package-level API owns admission semantics; production code
  cannot bypass limits through a second generic controller implementation.

## Configuration and validation

- **CFG-01:** The host supplies a non-empty, deterministic ordered class
  sequence. Empty, duplicate, whitespace-padded, or control-character class
  identifiers are rejected.
- **CFG-02:** Every configured class has exactly one policy, and policies for
  undeclared classes are rejected.
- **CFG-03:** Instance maximum running capacity is positive. Negative limits or
  durations are rejected.
- **CFG-04:** A class reservation never exceeds its maximum; total reservations
  never exceed instance capacity; class running and queue maxima never exceed
  their corresponding instance maxima.
- **CFG-05:** Per-principal and per-group concurrency and queue limits either
  equal zero for no additional limit or do not exceed the corresponding
  instance limit.
- **CFG-06:** A positive memory limit at a narrower scope never exceeds a
  positive parent-scope memory limit.
- **CFG-07:** Zero semantics match ADR-0013 exactly: disabled class execution or
  queuing where stated, unlimited additional memory/actor limits where stated,
  and disabled deadline where stated.
- **CFG-08:** An all-zero or empty configuration is never replaced by hidden
  application defaults.
- **CFG-09:** Construction defensively copies configuration; later caller
  mutation cannot change live scheduling policy.
- **CFG-10:** Invalid configuration fails before any goroutine, timer, queue, or
  observer-visible controller state is created.

## Requests and identity

- **REQ-01:** Admission accepts only a configured class, a canonical non-empty
  principal, a canonical non-empty bounded operation label, and a positive
  estimated memory value.
- **REQ-02:** Group identifiers are validated, deduplicated, sorted, and copied
  before they participate in admission or observations.
- **REQ-03:** Principal, group, class, and operation identifiers are opaque;
  the package does not trim, case-fold, authorize, or derive application
  identity.
- **REQ-04:** Caller mutation of request slices after `Acquire` begins cannot
  change queued, running, observed, or released accounting.
- **REQ-05:** A request that can never fit a configured positive memory bound is
  rejected immediately with the narrowest applicable typed reason and never
  consumes queue capacity.
- **REQ-06:** Invalid requests do not mutate running, queued, actor, group, or
  memory counters.

## Scheduling and resource accounting

- **SCH-01:** Running work never exceeds the instance maximum, class maximum,
  or any applicable principal/group maximum.
- **SCH-02:** Accounted memory never exceeds any applicable positive instance,
  class, principal, or group bound and cannot overflow `int64`.
- **SCH-03:** Queue admission never exceeds instance, class, principal, or group
  queue bounds. The rejected request is not retained after rejection.
- **SCH-04:** Configured class order, not map iteration or goroutine timing,
  determines deterministic class rotation when several eligible classes can
  run.
- **SCH-05:** Reserved capacity is considered before borrowed capacity, while
  unused reservations remain borrowable by other eligible classes.
- **SCH-06:** Borrowed work cannot prevent newly eligible reserved work from
  obtaining capacity after a running lease releases.
- **SCH-07:** Within one class, eligible principals rotate fairly; a continuously
  noisy principal cannot starve another eligible principal.
- **SCH-08:** An ineligible head request caused by actor, group, or memory limits
  does not block another eligible actor in the same class.
- **SCH-09:** Group limits apply to every group on a request; releasing or
  canceling work decrements each group exactly once.
- **SCH-10:** Every grant increments instance, class, principal, group, and
  memory accounting atomically before the lease becomes visible.
- **SCH-11:** Every terminal non-grant path removes queued accounting exactly
  once, including parent cancellation, queue timeout, rejection, and shutdown.
- **SCH-12:** Every lease release decrements running accounting exactly once and
  immediately makes newly eligible work schedulable.

## Deadlines, cancellation, nesting, and shutdown

- **LIF-01:** Parent cancellation before grant removes the waiter and returns
  the parent error without leaking capacity or timers.
- **LIF-02:** A queue deadline rejects only work that has not already won the
  grant race; a simultaneous grant either returns a valid lease or releases it
  exactly once.
- **LIF-03:** A positive execution deadline is reflected in the lease context;
  a zero execution deadline preserves parent cancellation without adding a
  deadline.
- **LIF-04:** Releasing a lease is idempotent, concurrency-safe, cancels its
  execution context, and records one terminal observation.
- **LIF-05:** Nested acquisition on the same controller, class, principal, and
  canonical group set reuses the existing admission without double-accounting.
- **LIF-06:** Nested acquisition with a different controller, class, principal,
  or group set fails with a typed conflict and changes no accounting.
- **LIF-07:** Controller shutdown is idempotent, rejects new work, rejects every
  queued waiter, cancels every active lease context, and leaves no scheduler
  goroutine or timer owned by the controller.
- **LIF-08:** Release, parent cancellation, timeout, observer callbacks, and
  shutdown may race without panic, deadlock, negative counters, double close,
  or leaked work.
- **LIF-09:** A nil or unusable controller fails closed and never grants an
  unaccounted lease.

## Observability

- **OBS-01:** Statistics snapshots include instance, class, principal, group,
  queue, running, memory, reservation, and borrowing information sufficient for
  a host adapter to export policy-relevant telemetry.
- **OBS-02:** Snapshots and events own their slices and maps; observers cannot
  mutate controller state.
- **OBS-03:** Admission observations distinguish admitted, rejected, canceled,
  timed-out, and released outcomes and carry typed rejection reasons where
  applicable.
- **OBS-04:** Queue wait and execution durations use the configured clock where
  deterministic time is required.
- **OBS-05:** Observer callbacks do not run while the scheduler mutex is held
  and may query statistics without deadlock.
- **OBS-06:** A nil observer has no behavioral effect. Observer integration does
  not require a metrics vendor or global registry.
- **OBS-07:** Observer latency or panic policy is explicit and tested; it cannot
  silently corrupt scheduler accounting.

## Correctness and qualification

- **COR-01:** Deterministic tests cover every rejection reason, zero-limit
  meaning, class selection branch, actor rotation branch, nesting branch, and
  terminal lifecycle path.
- **COR-02:** Property or model-based tests generate valid configurations and
  request/release sequences and continuously assert all running, queue, memory,
  and ownership invariants.
- **COR-03:** Starvation tests demonstrate progress for every continuously
  eligible class and actor under sustained competing load.
- **COR-04:** Race tests cover acquire/grant/cancel, grant/timeout,
  release/shutdown, nested release, observer replacement, and statistics reads.
- **COR-05:** Fuzz targets cover configuration validation, identity
  canonicalization, request validation, and bounded scheduler operation without
  panic or unbounded allocation.
- **COR-06:** Benchmarks record bounded time and allocation baselines for empty,
  contended, many-class, many-actor, and many-group workloads.
- **COR-07:** The package passes focused tests and the repository's required
  race/fuzz lanes on every supported operating system.
- **COR-08:** A simulated application with class names and limits unrelated to
  LeapView can construct, exercise, observe, and close the package.

## LeapView integration and cutover

- **APP-01:** `internal/workload` is the sole owner of LeapView class names,
  class order, production defaults, configuration mapping, and metric
  projection.
- **APP-02:** Authorization and operation classification occur before package
  admission; the package cannot turn an unauthenticated or unauthorized request
  into authorized work.
- **APP-03:** Existing interactive, background, refresh, control, and maintenance
  policies are represented explicitly and retain reviewed behavior unless a
  deliberate policy change is recorded separately.
- **APP-04:** Existing consumer contexts, cancellations, memory estimates,
  principal/group identities, and operation labels reach the package without a
  permissive fallback.
- **APP-05:** LeapView telemetry preserves bounded-cardinality labels and does
  not export raw unbounded principal, group, or operation identities unless
  separately reviewed.
- **APP-06:** Application shutdown joins or cancels all admitted work before
  dependent databases, analytical runtimes, and temporary resources close.
- **APP-07:** Production repository search finds no second scheduler, old class
  queue, workspace/domain partition, or compatibility controller after cutover.
- **APP-08:** Durable jobs depend on the stable admission contract through an
  adapter and do not move job persistence or recovery semantics into
  `pkg/workload`.
- **APP-09:** Focused workload, jobs, analytics, refresh, managed-data,
  serving-state, dashboard, composition, and shutdown suites pass after
  adoption.
- **APP-10:** `task ci` passes on the final combined tree before ADR-0013
  implementation metadata changes from pending.

## Evidence ledger

Implementation issues must maintain the evidence ledger below. A requirement
may cite more than one test when its proof crosses package and application
boundaries.

| Requirement range | Evidence | Status |
|---|---|---|
| PKG-01–PKG-08 | LEA-440 and LEA-441: `pkg/workload`, `TestPackageDoesNotImportApplicationPrivateCode`, `TestReusableWorkloadPackageContainsOnlyGenericMechanisms`, and `TestReusableWorkloadPackageHasSingleProductionAdapter` | Complete |
| CFG-01–CFG-10 | LEA-440 and LEA-441: configuration validation and defensive-copy tests in `pkg/workload/workload_test.go`, plus application-owned default mapping in `internal/app/config` | Complete |
| REQ-01–REQ-06 | LEA-440 and LEA-441: request, identity, mutation, memory-eligibility, and typed-error tests in `pkg/workload` | Complete |
| SCH-01–SCH-12 | LEA-441 and LEA-442: deterministic scheduler, bounded-model, actor-rotation, head-skipping, and starvation tests | Complete |
| LIF-01–LIF-09 | LEA-441 and LEA-442: cancellation, timeout, nesting, release, shutdown, nil-timer, and race tests | Complete |
| OBS-01–OBS-07 | LEA-441 and LEA-442: typed defensive observations, reentrant and panic-isolated observers, injected-clock timing, replacement races, and borrowed-capacity statistics | Complete |
| COR-01–COR-08 | LEA-442: `test:workload:qualify` normal/race suites, model test, starvation tests, four fuzz targets, five benchmark baselines, and the external-host test | Complete |
| APP-01–APP-10 | LEA-443–LEA-445: the `internal/workload` adapter and module, application config/telemetry/lifecycle wiring, fail-closed refresh and composition paths, architecture gates, consumer suites, and `task ci` | Complete |

### Maintained verification

The generic scheduler, independent qualification, application cutover, and
repository contract are verified with:

```sh
go test ./pkg/workload -count=1
go vet ./pkg/workload
task test:workload:qualify
go test -race ./internal/workload/... -count=1
go test ./internal/platform/architecture -run '^(TestReusableWorkloadPackageContainsOnlyGenericMechanisms|TestReusableWorkloadPackageHasSingleProductionAdapter|TestOnlyWorkloadAdaptersAndCompositionDependOnWorkload|TestWorkloadImportsNoProductCapabilities|TestEveryProductionPackageHasAnArchitecturalOwner|TestArchitectureDecisionLogIsWellFormed)$' -count=1
go test ./internal/platform/jobs/... ./internal/refresh/... ./internal/manageddata/... ./internal/servingstate/... ./internal/dashboard/... ./internal/app/... -count=1
task ci
```

`task ci:full` includes `test:workload:qualify`, so the maintained full contract
reruns the normal, race, fuzz, and benchmark qualification lanes. Production
code has one scheduler implementation in `pkg/workload` and one policy adapter
in `internal/workload`; application composition rejects a missing admission
dependency instead of constructing a permissive fallback.
