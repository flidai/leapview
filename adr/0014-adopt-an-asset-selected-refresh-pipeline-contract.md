# ADR-0014: Adopt an asset-selected refresh pipeline contract

Status: accepted

Decision date: 2026-08-20

Implementation: complete

Deciders: LeapView maintainers

Supersedes: none

Related: [ADR-0005](0005-use-project-wide-resource-graph.md);
[ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md);
[ADR-0007](0007-adopt-plan-driven-project-delivery.md);
[ADR-0008](0008-isolate-ducklake-candidate-physical-state.md);
[ADR-0009](0009-separate-control-and-physical-transactions.md);
[ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md);
[ADR-0013](0013-separate-workload-admission-from-application-policy.md)

## Context and problem statement

ADR-0005 established Pipeline as a project-wide resource. ADR-0006 made
SemanticModel a governed contract over project Models, and ADR-0007 through
ADR-0009 established immutable plans, private candidate construction, exact
publication, and data restatement. Pipeline authoring has not yet been brought
to the same contract standard as Connection, Source, Model, SemanticModel, and
Dashboard.

The current Pipeline document is a handwritten Go and CUE shape containing one
`semanticModel` reference and an optional list of cron schedules. Its compiled
runtime form contains the same reference and schedule data. It does not define
stable trigger identity, occurrence identity, overlap behavior, catch-up
behavior, plan identity, or the relationship between a selected semantic asset
and the physical work that will execute.

The apparent execution scope is also misleading. The refresh planner resolves
the Models required by the selected SemanticModel and creates child run records
for that ordered set. Canonical refresh execution then creates a delivery
restatement whose current implementation requires candidate-wide full
materialization. The authored selector, compiled plan, operational run tree,
and delivery operation therefore describe different granularities:

`authored SemanticModel selection -> required Model work -> candidate-wide restatement`

A complete immutable candidate is the correct publication unit, but it does
not follow that every relation must be recomputed. ADR-0007 already separates
execution identity from provenance and governance, while ADR-0008 permits a
new candidate catalog to reuse proven physical relations. Pipeline planning
must carry its selected materialization scope into that lifecycle rather than
discarding it at the canonical execution boundary.

GitHub Actions is the repository's CI execution service and can invoke
LeapView commands. Its workflow syntax is not an appropriate public Pipeline
contract: it is repository- and runner-oriented, exposes general-purpose jobs
and credentials, and scheduled workflows are coupled to the default branch
rather than an immutable active LeapView serving generation. Adopting it would
make deployment infrastructure part of the portable analytical contract.

The question is how LeapView should define portable refresh intent, compile it
against the resource graph, schedule and deduplicate invocations, execute it
through the plan-driven delivery lifecycle, and export interoperable
observability without becoming a general workflow engine.

## Decision drivers

- Give Pipeline the same TypeSpec-owned, generated public contract discipline
  as the other `leapview.dev/v1` assets.
- Let authors select governed assets while keeping dependency discovery,
  ordering, and physical scope compiler-owned.
- Make the selected scope mean the same thing in the authored document,
  immutable plan, run tree, physical build, qualification, and audit evidence.
- Preserve complete immutable candidate and generation publication while
  avoiding recomputation of unrelated relations when reuse is proven safe.
- Bind every plan and run to one immutable serving generation and prevent a
  stale generation from publishing.
- Give scheduled occurrences stable identity and deterministic recovery across
  process restarts, scheduler outages, concurrent dispatchers, and deployment
  changes.
- Make overlap and missed-occurrence behavior explicit rather than hidden in
  queue and scheduler implementation.
- Define a portable cron and timezone contract, including daylight-saving
  gaps and folds.
- Keep retries, timeouts, backfills, event triggers, execution infrastructure,
  and asset selection as separate extension axes.
- Reuse workload admission, authorization, delivery fencing, immutable
  evidence, and retention boundaries already adopted by prior ADRs.
- Support OpenLineage-compatible observability without treating OpenLineage as
  an authoring, authorization, or execution-interoperability contract.
- Avoid embedding GitHub Actions, Dagster, Argo, Kubernetes, dbt, SQLMesh, or a
  general workflow DSL into the public resource model.

## Considered options

### Use GitHub Actions workflows as the Pipeline specification

GitHub Actions already supplies cron triggers, concurrency controls, retries,
logs, and hosted execution. LeapView could generate workflows or ask authors to
commit them directly.

This would bind a portable project asset to one source host, branch model,
runner environment, secret system, and imperative command language. GitHub
Actions can remain an automation client, but it cannot be the authority for
asset selection, serving-generation identity, delivery scope, or publication.

### Adopt Dagster or Argo Workflows as the runtime and public contract

Dagster's asset-selection model is a close conceptual match. Argo CronWorkflow
has mature timezone, concurrency, suspension, and missed-run controls. Either
could execute capable workflows.

Adopting either contract wholesale would duplicate LeapView's compiler,
resource graph, authorization, workload admission, immutable candidate, and
publication lifecycles. It would also expose infrastructure concepts far beyond
refreshing governed analytical assets. Their semantics are references, not new
runtime dependencies.

### Expose only project-wide refresh

LeapView could make the current candidate-wide behavior honest by defining
Pipeline as a whole-project restatement. This is simple, but it discards the
meaningful selection and dependency information already available to the
compiler and forces unrelated physical work as projects grow.

### Adopt a general vendor-neutral workflow DSL

The Open Workflow Specification is a useful vendor-neutral specification to
track. It covers tasks, schedules, events, retries, timeouts, data flow, and
service invocation.

LeapView v1 does not need arbitrary tasks, service calls, control flow,
scripting, containers, or a second expression language. A general workflow DSL
would weaken the governed asset boundary and substantially enlarge the security
and runtime surface.

### Adopt a native asset-selected refresh contract

Pipeline remains a small project asset expressing selection, triggers, and run
policy. The compiler resolves exact work from the project graph and produces an
immutable generation-bound plan. Each invocation produces an auditable run and
executes the plan through canonical delivery. External systems may invoke and
observe that lifecycle without owning its semantics.

## Decision outcome

LeapView will adopt a native asset-selected refresh Pipeline contract. Pipeline
is author intent, PipelinePlan is the compiler's immutable interpretation of
that intent against one serving generation, and PipelineRun is evidence of one
invocation of one plan.

The identity chain is:

`pipelineId -> generationId -> planDigest -> runId`

A scheduled occurrence is logically identified by:

`projectId + environment + pipelineId + triggerId + nominalTime`

Pipeline IDs are stable within a project, not globally across every project and
environment. `projectId` and `environment` are therefore required namespace
components of scheduled-occurrence identity; the shorter
`pipelineId + triggerId + nominalTime` tuple is not a canonical identity.

Pipeline structure is authored in TypeSpec and projected through the same
generation boundary used by the other public assets. The current handwritten
`semanticModel` plus `on.schedule` draft shape is replaced directly before the
first public release; no compatibility reader, alias, or migration surface is
retained. Repository examples that preserve existing operational intent must
be rewritten once to state `overlap: replace` explicitly. This source rewrite
must not introduce parser or runtime normalization for an omitted value.

### Authored Pipeline contract

The public Pipeline document contains three independent concerns:

- `selection`: the governed asset the author asks LeapView to refresh;
- `triggers`: the ways invocations may be created; and
- `runPolicy`: policy applied when an invocation is admitted.

An illustrative v1 document is:

```yaml
apiVersion: leapview.dev/v1
kind: Pipeline
metadata:
  id: pipeline:sales-refresh
  name: sales_refresh
spec:
  selection:
    type: semanticModel
    semanticModel: semantic-model:sales
  triggers:
    - id: manual
      type: manual
    - id: weekdays-0600
      type: schedule
      cron: "0 6 * * 1-5"
      timezone: Europe/Copenhagen
      missedOccurrences: latest
  runPolicy:
    overlap: replace
```

`selection` is a closed tagged union. V1 contains only the `semanticModel`
variant. Variant-specific reference fields are retained so generated APIs make
the selected resource kind explicit. Future selection variants require their
own compilation and authorization semantics; they are not open strings.

`triggers` is a closed tagged union. V1 contains `manual` and `schedule`.
Every trigger has a non-empty ID unique within the Pipeline. A manual trigger
permits authorized on-demand invocation; it does not grant authorization by
itself. A schedule trigger creates occurrences under a target-owned scheduler
workload identity. `managedDataPublished` may be added later as a trigger
variant without changing selection or run policy.

`runPolicy.overlap` is required and has no implicit default. V1 supports:

- `forbid`: while an earlier run for the same Pipeline and environment is
  nonterminal, record the new invocation as skipped and do not begin its
  physical work; and
- `replace`: atomically supersede earlier queued or running runs before
  admitting the replacement. Cancellation of already executing physical work
  is best effort, but lease revocation and publication fencing guarantee that a
  superseded run can never publish.

V1 does not expose `allow`. Concurrent restatements from the same active base
cannot both satisfy exact publication compare-and-swap semantics, so permitting
both to build would advertise concurrency that normally degenerates into stale
work. A future ADR may add `allow` only with defined merge, partition, or
independent-scope publication semantics.

Retry and timeout belong to `runPolicy` when introduced. Bounded restatement
ranges and backfills are invocation inputs, not trigger or selection variants.

### Selection and compiled materialization scope

Selecting SemanticModel `M` resolves to the required-input materialization
closure of `M`. For v1, that closure contains:

- every physical Model directly bound by a dataset in `M`;
- every transitive Model dependency required by those Models; and
- each required Source as resolved input and data-version evidence.

The SemanticModel remains the governed selected asset. Models are the ordered
materialization work. Sources are inputs unless their own contract explicitly
requires managed materialization. Dashboards and other consumers of the
SemanticModel are not part of this closure.

The compiler follows consumer-to-dependency graph edges and emits a stable
topological Model order. It must reject missing dependencies, cycles, ambiguous
physical identities, or a selection that resolves outside the caller's
authorized project and environment. Authors do not list dependencies or steps
that the compiler can derive.

The plan calls this projection `materializationScope`; it does not reuse the
delivery evidence name `downstreamScope`, whose graph direction is ambiguous in
this context. It also does not use `target` as the compiled counterpart of
`selection`: target identifies the target-owned deployment and publication
boundary elsewhere in the architecture, while `materializationScope` names the
compiler-resolved physical work.

Canonical execution must build and qualify exactly the compiled
materialization scope. A candidate remains a complete immutable project
generation: unaffected relations are carried forward only when ADR-0007
execution identities prove exact reuse. A Pipeline restatement must not force
candidate-wide recomputation merely because publication is candidate-wide.
Project-wide restatement is legal only when a future explicit project selection
compiles to that scope.

### Immutable PipelinePlan

Admission creates or resolves an immutable PipelinePlan containing at least:

- Pipeline ID and canonical authored-selection digest;
- project, environment, serving generation, and source artifact digest;
- selected resource and resolved `materializationScope`;
- deterministic Model execution order and resolved Source inputs;
- required qualification checks;
- requested and effective restatement interval or watermark, when present;
- effective trigger and run policy;
- execution, provenance, governance, and evidence digests; and
- an overall plan digest.

Execution inputs alone determine `executionDigest`. Work estimates such as
rows, bytes, duration, or cost are advisory evidence. They may be captured as
an immutable observation and participate in `evidenceDigest` and the overall
plan evidence identity, but they must not change `executionDigest` or physical
reuse equivalence.

The same canonical authoring bytes, serving generation, compiler version,
resolved inputs, selection, and policy produce the same execution plan and
digest regardless of map order, dispatcher, process, or invocation time.

### Occurrence and generation semantics

When a scheduled nominal time becomes due, LeapView durably creates the logical
occurrence before dispatch. That transaction captures the then-active serving
generation and artifact digest. A manual invocation captures them when its
authorized request is admitted. The subsequent plan and run never silently
switch generations.

Immutability does not authorize stale publication. If the captured generation
or target revision is no longer active before execution or publication, the run
becomes `superseded`; it cannot publish and cannot reinterpret itself against
the new generation. Recovery may resume the same run only against its captured
plan. Creating a replacement against the new generation is a new invocation
with a new run ID and must follow the configured admission policy.

The logical scheduled-occurrence key excludes generation so activating a new
generation cannot execute the same Pipeline trigger and nominal time twice.
The captured generation remains part of occurrence evidence. Durable uniqueness
and atomic run attachment make concurrent dispatchers and crash recovery
idempotent.

Each schedule trigger declares `missedOccurrences`:

- `skip`: advance past missed nominal times without creating an invocation; or
- `latest`: create at most the latest missed occurrence for that trigger.

Catch-up is evaluated independently per trigger ID, never globally per
Pipeline. After occurrences are produced, `overlap` deterministically governs
their admission in nominal-time and trigger-ID order. `latest` is bounded
recovery, not a backfill mechanism; replaying multiple historical intervals
requires explicit bounded backfill invocation inputs.

Every occurrence records its outcome, including admitted, skipped because of
overlap, superseded, or attached run. Scheduler restart, claim expiry, and lost
acknowledgement must converge on that record rather than create another run.

### Cron and daylight-saving semantics

Schedule triggers use LeapView's portable cron profile:

- exactly five fields: minute, hour, day of month, month, and day of week;
- no seconds field and no `@daily`-style aliases;
- named months and weekdays are accepted;
- schedules must not resolve more frequently than once every five minutes; and
- `timezone` is required and must be an IANA timezone name.

The cron expression is evaluated as local wall-clock time in the declared
timezone. If a selected wall time does not exist during a daylight-saving gap,
the occurrence advances to the first valid local minute at or after that wall
time on the same local date. If a selected wall time occurs twice during a
fold, LeapView emits exactly one occurrence at the earlier instant. Nominal time
is stored as an absolute UTC instant together with the trigger timezone and
wall-clock schedule evidence.

Changes to this cron grammar, minimum interval, or gap and fold behavior are
contract changes and require conformance-test updates. Scheduler host timezone
must never affect evaluation.

### PipelineRun and operational evidence

PipelineRun binds one admitted invocation to one immutable PipelinePlan. It
records run ID, occurrence or request identity, actor or workload identity,
trigger ID and type, nominal and actual times, selected resource,
materialization scope, serving generation, plan and execution digests, parent
and child execution relationships, admission result, lease revisions, status,
attempts, data versions, qualification, publication, and terminal reason.

Child Model executions are operational projections of the PipelinePlan, not a
second source of dependency truth. Their order and identity must agree with the
plan. Authorization, workload admission, lease ownership, memory estimates,
and cancellation remain target runtime concerns governed by existing access
and workload boundaries.

Run state is append-only evidence. Replacement, retry, or recovery creates new
attempt or supersession evidence rather than rewriting why an earlier run was
admitted or what plan it referenced.

### Reference specifications and interoperability

LeapView borrows focused semantics rather than adopting one external workflow
contract:

| Concern | Reference | LeapView decision |
|---|---|---|
| Asset selection | [Dagster asset jobs](https://docs.dagster.io/guides/build/jobs/asset-jobs) | Authors select governed assets; the compiler derives dependency work. Dagster is not a runtime dependency. |
| Scheduling policy | [Argo CronWorkflow](https://argo-workflows.readthedocs.io/en/latest/cron-workflows/) | Borrow explicit timezone, concurrency, and missed-run concepts, but define LeapView-specific occurrence and DST semantics. |
| Observability | [OpenLineage object model](https://openlineage.io/docs/spec/object-model/) | Export standards-aligned lineage and run observations; LeapView evidence remains authoritative. |
| General workflow portability | [Open Workflow Specification](https://github.com/open-workflow-specification/specification/blob/main/dsl.md) | Track as a useful vendor-neutral specification; do not expose its general task language in v1. |
| Repository automation | [GitHub Actions scheduled workflows](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#schedule) | Permit CI/CD invocation of LeapView APIs and CLI; do not use workflow YAML as the Pipeline contract. |

For OpenLineage export, Pipeline maps to Job and PipelineRun maps to Run.
Scheduled occurrences use the nominal-time facet. Separately emitted Model
execution runs use the parent facet to refer to the PipelineRun. Sources and
materialized Model outputs map to Datasets; a Model definition is not itself a
Dataset merely because it appears in the project graph.

OpenLineage is an observability interoperability boundary. It does not own
execution, retries, authorization, approval, publication, rollback, or canonical
LeapView identity. Lossless standard fields are used where available; scoped
LeapView facets may carry generation, plan, selection, and qualification
evidence without replacing the internal ledger.

### Prohibited shortcuts

The implementation must not:

- use GitHub Actions, an external orchestrator, or runner configuration as the
  canonical Pipeline document;
- ask authors to enumerate dependency steps derived from the project graph;
- call a field `selection` while executing a broader materialization scope than
  the immutable plan records;
- treat complete-candidate publication as requiring candidate-wide physical
  recomputation;
- silently retarget a queued or running invocation to a newer generation;
- allow a superseded run or revoked lease to publish;
- deduplicate missed occurrences globally across independent trigger IDs;
- give overlap or missed-occurrence policy a hidden runtime default;
- interpret scheduler host timezone as authored schedule timezone;
- include nondeterministic estimates in execution identity;
- treat child run records as an independent dependency graph; or
- describe OpenLineage export as runtime or execution interoperability.

## Consequences

Authors gain a small declarative Pipeline resource that says what governed
asset to refresh, when invocations may occur, and how overlap is handled. They
do not maintain an imperative DAG that duplicates Model lineage. Generated
schemas and documentation can describe every accepted field and reject hidden
or unsupported behavior before deployment.

Operators gain stable trigger, occurrence, plan, and run identities. Scheduler
outages, concurrent dispatchers, deployment races, replacement, and stale
publication have explicit outcomes. The chain from author intent to compiler
decision to physical execution becomes inspectable and exportable.

Scoped materialization can substantially reduce restatement work while
preserving a complete immutable candidate. It also makes relation-level reuse
and correct scope propagation mandatory. The current canonical restatement
path, scheduler grouping, authored CUE/Go shape, and run evidence do not yet
conform and must change.

Requiring explicit `overlap` and `missedOccurrences` makes documents slightly
more verbose. It avoids silently preserving current behavior or introducing a
behavioral migration under a default. Existing draft examples must be rewritten
to the new TypeSpec-generated shape.

Stable trigger IDs become durable API and audit identities. Renaming a trigger
is an operational change, not cosmetic metadata. Tooling must surface the
resulting schedule replacement and prevent accidental duplicate occurrence
creation around activation.

The DST gap policy delays a missing occurrence to the first valid minute, while
the fold policy emits only the earlier occurrence. These choices differ from
some external cron systems, so LeapView must document and test them rather than
claim generic cron equivalence.

V1 intentionally omits arbitrary task DAGs, user code, containers, `allow`
overlap, unbounded catch-up, retries, timeout, event triggers, and backfill
grammar. Those omissions keep the trust and execution boundary narrow. They may
be added independently when a demonstrated use case has precise semantics.

GitHub Actions remains useful for repository CI and deployment automation;
Dagster, Argo, Open Workflow Specification, and OpenLineage remain references
or interoperability boundaries rather than mandatory infrastructure.

## Confirmation

Conformance requires evidence that:

- TypeSpec is the sole structural authority for Pipeline and generates Go,
  JSON Schema, documentation, and other required projections;
- the generated schema accepts only the closed selection and trigger variants,
  requires explicit overlap and schedule recovery policy, and rejects the old
  `semanticModel` plus `on.schedule` shape;
- compilation of a SemanticModel selection produces the exact deterministic
  required Model closure, topological order, Source inputs, and
  `materializationScope`, while excluding unrelated Models and dashboards;
- repeated compilation produces identical execution digests independent of
  source map order, process, and dispatcher;
- work estimates can change evidence without changing execution identity or
  proven physical reuse;
- canonical refresh physically rebuilds exactly the plan scope, reuses only
  proven unaffected relations, and publishes one complete immutable candidate;
- an active-generation or target-revision change makes the captured run stale
  and no stale or superseded run can publish;
- occurrence uniqueness and atomic run attachment prevent duplicates across
  concurrent dispatchers, restart, claim expiry, activation, and lost
  acknowledgement;
- missed occurrences are coalesced per trigger, and table-driven tests cover
  every supported `overlap` and `missedOccurrences` interaction with multiple
  triggers and recovery;
- replacement revokes prior publication authority even when physical
  cancellation is delayed or impossible;
- cron conformance tests cover the five-field grammar, named values, minimum
  interval, IANA zones, host-zone independence, daylight-saving gaps and folds,
  and deterministic nominal UTC time;
- manual and scheduled invocations enforce the same authorization, workload,
  planning, execution, and publication boundaries; and
- OpenLineage contract tests map Pipelines, runs, child executions, nominal
  time, Sources, and materialized outputs without treating exported events as
  execution authority.
