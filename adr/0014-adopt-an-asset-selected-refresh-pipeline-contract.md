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

The original Pipeline document was a handwritten Go and CUE shape containing
one `semanticModel` reference and an optional list of cron schedules. The first
implementation of this ADR moved the shape to TypeSpec but exposed tagged-union
machinery in authored YAML. Neither shape provides the intended ergonomic
contract. The original shape also did not define stable schedule identity,
occurrence identity, concurrency behavior, late-start behavior, plan identity,
or the relationship between a selected semantic asset and the physical work
that will execute.

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
- Use established concurrency and late-start concepts rather than inventing
  Pipeline-specific policy vocabulary.
- Inherit one established specification for each generic concern and make any
  intentional restriction or incompatibility explicit.
- Define a version-pinned cron and timezone compatibility profile, including
  daylight-saving gaps and folds.
- Keep retries, timeouts, backfills, event triggers, execution infrastructure,
  and asset selection as separate extension axes.
- Reuse workload admission, authorization, delivery fencing, immutable
  evidence, and retention boundaries already adopted by prior ADRs.
- Support OpenLineage-compatible observability without treating OpenLineage as
  an authoring, authorization, or execution-interoperability contract.
- Avoid embedding GitHub Actions, Dagster, Argo, Kubernetes, dbt, SQLMesh, or a
  general workflow DSL into the public resource model while still inheriting
  focused semantics from an identified external specification.

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
refreshing governed analytical assets. LeapView adopts the focused Argo
scheduling semantics below without adopting its resource or runtime; Dagster
remains comparison material.

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

Pipeline remains a small project asset expressing selection, schedules, and
concurrency policy. The compiler resolves exact work from the project graph and
produces an immutable generation-bound plan. Each invocation produces an
auditable run and executes the plan through canonical delivery. External
systems may invoke and observe that lifecycle without owning its semantics.

## Decision outcome

LeapView will adopt a native asset-selected refresh Pipeline contract. Pipeline
is author intent, PipelinePlan is the compiler's immutable interpretation of
that intent against one serving generation, and PipelineRun is evidence of one
invocation of one plan.

The identity chain is:

`pipelineId -> generationId -> planDigest -> runId`

A scheduled occurrence is logically identified by:

`projectId + environment + pipelineId + nominalTime`

Pipeline IDs are stable within a project, not globally across every project and
environment. `projectId` and `environment` are therefore required namespace
components of scheduled-occurrence identity; the shorter
`pipelineId + nominalTime` tuple is not a canonical identity. Schedule IDs
identify authored expressions and appear in evidence, but do not split one
Argo-equivalent scheduled execution into multiple LeapView occurrences when
expressions overlap at the same nominal instant.

For each generic concern, LeapView names one normative external specification
and inherits its semantics. LeapView-specific extensions may add identity,
evidence, analytical compilation, admission, or publication invariants, but
must not alter inherited behavior unless this ADR explicitly declares an
incompatibility. Comparison systems are non-normative unless the reference
table says otherwise.

Pipeline structure is authored in TypeSpec and projected through the same
generation boundary used by the other public assets. The current handwritten
`semanticModel` plus `on.schedule` draft shape is replaced directly before the
first public release; no compatibility reader, alias, or migration surface is
retained. The subsequently implemented tagged `selection`, tagged `triggers`,
and `runPolicy.overlap` shape is also replaced before that release because it
exposes code-generation structure rather than author intent. Repository
examples that preserve existing operational intent must be rewritten once to
state `concurrencyPolicy: Replace` explicitly when schedules exist. This source
rewrite must not introduce parser or runtime normalization for an omitted
scheduled-policy value.

### Authored Pipeline contract

The public Pipeline document contains three independent concerns:

- `selection`: the governed asset the author asks LeapView to refresh;
- `schedules`: optional named recurring invocations; and
- Pipeline-wide `timezone`, `startingDeadlineSeconds`, and
  `concurrencyPolicy` fields that govern scheduled invocations.

Manual invocation is an operation available to authorized callers for every
Pipeline. It is not an authored trigger and does not require a placeholder in
the document. A Pipeline without `schedules` is therefore a valid manual-only
Pipeline.

An illustrative v1 document is:

```yaml
apiVersion: leapview.dev/v1
kind: Pipeline
metadata:
  id: pipeline:sales-refresh
  name: sales_refresh
spec:
  selection:
    semanticModel: sales
  schedules:
    weekdays-0600: "0 6 * * 1-5"
  timezone: Europe/Copenhagen
  startingDeadlineSeconds: 3600
  concurrencyPolicy: Replace
```

The resource envelope intentionally retains the `metadata.id` and
`metadata.name` distinction established by ADR-0005 and ADR-0006. Aligning all
authored assets with the Backstage/Kubernetes `name` plus optional `title`
convention is a cross-asset identity decision; this Pipeline ADR must not create
a one-off metadata convention or silently supersede those earlier decisions.

`selection` is a closed object. V1 contains only the `semanticModel` field,
whose value is the project-unique authored name of a SemanticModel. The field
name already identifies the selected resource kind, so a second
`type: semanticModel` discriminator and a kind-prefixed reference add no
information. Future selection kinds require distinct fields with their own
compilation and authorization semantics; LeapView must not introduce a generic
selector language until the use case needs the graph, set, or tag operations
provided by mature selector contracts such as dbt.

`schedules` is an optional map. Each key is a non-empty schedule ID unique
within the Pipeline and supplies the durable identity that would otherwise
require a repeated `id` field. Each value is an Argo-compatible cron expression.
The keys add authoring, change-review, and execution-evidence identity only.
Removing the keys produces the corresponding Argo `schedules` list; key names
must not change occurrence cardinality, missed-run recovery, concurrency, or
daylight-saving behavior. The compiled projection orders expressions by their
canonical cron text and retains duplicate expressions so map iteration and key
renames cannot alter scheduler behavior. A schedule creates occurrences under
a target-owned scheduler workload identity. Future event sources may be added
as separate named collections; they must not force every schedule through a
tagged-union list.

When `schedules` is non-empty, `timezone`, `startingDeadlineSeconds`, and
`concurrencyPolicy` are required Pipeline-wide fields. They have the same scope
as the corresponding Argo CronWorkflow fields. They are absent from a
manual-only Pipeline because Argo scheduling semantics do not govern externally
initiated invocations.

`concurrencyPolicy` uses the established Kubernetes CronJob and Argo
CronWorkflow vocabulary for scheduled-versus-scheduled overlap. V1 supports:

- `Forbid`: while an earlier scheduled run for the same Pipeline and
  environment is nonterminal, record the new scheduled occurrence as skipped
  and do not begin its physical work; and
- `Replace`: supersede earlier scheduled runs before admitting the new
  scheduled execution. Cancellation of already executing physical work is best
  effort, but lease revocation and publication fencing guarantee that a
  superseded scheduled run can never publish.

V1 does not expose `Allow`. Concurrent restatements from the same active base
cannot both satisfy exact publication compare-and-swap semantics, so permitting
both to build would advertise concurrency that normally degenerates into stale
work. A future ADR may add `Allow` only with defined merge, partition, or
independent-scope publication semantics.

The contract does not reserve a one-field `runPolicy` wrapper for hypothetical
future features. Retry and timeout policy may be added when their semantics are
defined. Bounded restatement ranges and backfills are invocation inputs, not
schedule or selection variants. Manual and backfill conflicts are governed by
Pipeline invocation admission below, not by `concurrencyPolicy`.

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

The normative generic graph operation is dbt's ancestor expansion: include the
selected node's upstream parents transitively. LeapView does not import dbt
selector syntax; it applies that graph direction to the typed SemanticModel
selection and then projects only materializable Models into execution work.
Dagster remains useful design precedent for asset-oriented orchestration, but
does not define Pipeline selection semantics.

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
- effective invocation source and scheduling policy when applicable;
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

The logical scheduled-occurrence key excludes generation and schedule ID so
activating a new generation, renaming a schedule, or matching the same nominal
instant through overlapping expressions cannot execute the same
Argo-equivalent occurrence twice. The captured generation and every matching
schedule ID remain part of occurrence evidence. Durable uniqueness and atomic
run attachment make concurrent dispatchers and crash recovery idempotent.

`startingDeadlineSeconds` follows Argo CronWorkflow's Pipeline-wide late-start
model:

- `0`: advance past a missed nominal time without creating a recovery
  execution; or
- a positive integer: permit one recovery execution when the Argo compatibility
  baseline considers a missed scheduled time to be within the deadline.

The complete schedule set is evaluated as one CronWorkflow. Recovery creates
at most one execution for the Pipeline, not one execution per schedule ID.
Schedule IDs add evidence about the expressions matching the chosen nominal
time; they do not change the recovery decision. Replaying multiple historical
intervals requires explicit bounded backfill invocation inputs.

Every occurrence records its outcome, including admitted, skipped because of
concurrency policy, superseded, or attached run. Scheduler restart, claim
expiry, and lost acknowledgement must converge on that record rather than
create another run.

### Pipeline invocation admission

Argo `concurrencyPolicy` governs only collisions between scheduled executions
of the same Pipeline and environment. LeapView Pipeline invocation admission
governs collisions involving externally initiated manual and backfill
operations. It is distinct from the capacity-oriented workload admission in
ADR-0013.

The collision outcomes are:

| Active invocation | Incoming invocation | Required outcome |
|---|---|---|
| Scheduled | Scheduled | Apply the Pipeline's Argo `Forbid` or `Replace` policy. |
| Manual or backfill | Scheduled | Durably record the occurrence and terminal `admission_denied_external_active` outcome; perform no work and do not queue it. |
| Any nonterminal invocation | Manual or backfill | Reject the request as a conflict by default; do not reinterpret `concurrencyPolicy`. |

`Replace` never authorizes a scheduled occurrence to terminate a manual or
backfill run. Replacing any externally initiated or scheduled run outside the
scheduled-versus-scheduled rule is a separate, explicit, authorized operation.
That operation creates supersession evidence, revokes the earlier run's leases
and publication authority atomically, and admits a new invocation only after
the fencing transition succeeds.

### Cron and daylight-saving semantics

Scheduling uses a restricted, version-pinned Argo-compatible profile. The
normative baseline is the Argo Workflows v4.0.8 CronWorkflow contract, including
its documented Kubernetes CronJob-compatible `robfig/cron` profile and
behavior. LeapView accepts that documented cron grammar, including:

- five-field minute, hour, day-of-month, month, and day-of-week expressions;
- lists, ranges, steps, and named months and weekdays;
- `?` where the baseline treats it as equivalent to `*`; and
- the documented `@yearly`, `@annually`, `@monthly`, `@weekly`, `@daily`,
  `@midnight`, and `@hourly` macros.

There is no GitHub Actions-derived five-minute minimum. Every-minute schedules
are valid. Seconds fields, the undocumented `@every` descriptor, and
schedule-embedded `TZ` or `CRON_TZ` declarations are rejected as explicit
profile restrictions.

LeapView deliberately narrows, but does not reinterpret, the Argo contract:
`timezone` must be an explicit IANA timezone rather than inheriting the
scheduler machine timezone; `startingDeadlineSeconds` and `concurrencyPolicy`
must be explicit when schedules exist; and V1 rejects Argo's `Allow` policy.
For every accepted document, cron evaluation, missed-run recovery,
scheduled-versus-scheduled concurrency, and daylight-saving behavior are the
baseline Argo behavior. In particular, nonexistent local times are skipped on
a spring-forward transition and matching local times may run twice during a
fall-back fold. Nominal time is stored as an absolute UTC instant together with
the configured timezone, matching schedule IDs, and wall-clock evidence.

The implementation must pin the parser dependency and maintain an Argo v4.0.8
conformance corpus. A future dependency or compatibility-baseline upgrade is a
contract change when it changes accepted syntax or observable scheduling
behavior. Scheduler host timezone must never affect evaluation.

### PipelineRun and operational evidence

PipelineRun binds one admitted invocation to one immutable PipelinePlan. It
records run ID, occurrence or request identity, actor or workload identity,
invocation source and matching schedule IDs when applicable, nominal and actual
times, selected resource, materialization scope, serving generation, plan and
execution digests, parent and child execution relationships, admission result,
lease revisions, status, attempts, data versions, qualification, publication,
and terminal reason.

Child Model executions are operational projections of the PipelinePlan, not a
second source of dependency truth. Their order and identity must agree with the
plan. Authorization, workload admission, lease ownership, memory estimates,
and cancellation remain target runtime concerns governed by existing access
and workload boundaries.

Run state is append-only evidence. Replacement, retry, or recovery creates new
attempt or supersession evidence rather than rewriting why an earlier run was
admitted or what plan it referenced.

### Reference specifications and interoperability

LeapView adopts one normative external specification for each generic concern:

| Concern | Reference | LeapView decision |
|---|---|---|
| Graph selection | [dbt graph operators](https://docs.getdbt.com/reference/node-selection/graph-operators) | Inherit dbt's upstream ancestor meaning. LeapView supplies a typed SemanticModel root and compiler-owned materialization projection instead of importing dbt selector syntax. |
| Scheduling | [Argo Workflows v4.0.8 CronWorkflow](https://github.com/argoproj/argo-workflows/blob/v4.0.8/docs/cron-workflows.md) | Inherit cron parsing, timezone/DST behavior, one-execution deadline recovery, and scheduled concurrency. Require explicit timezone and policy, omit `Allow`, and add schedule IDs as evidence-only metadata. |
| Observability | [OpenLineage object model](https://openlineage.io/docs/spec/object-model/) and [facet extensibility](https://openlineage.io/docs/spec/facets/) | Inherit Job, Run, Dataset, standard-facet, and custom-facet semantics. LeapView evidence remains authoritative. |

Dagster asset jobs remain design precedent, not a selection specification.
Temporal schedules and the Open Workflow Specification remain comparison
material, not sources of Pipeline behavior. GitHub Actions may invoke LeapView
APIs or CLI from repository automation, but neither its workflow nor cron
semantics contribute to the Pipeline contract.

For OpenLineage export, Pipeline maps to Job and PipelineRun maps to Run.
Scheduled occurrences use the nominal-time facet. Separately emitted Model
execution runs use the parent facet to refer to the PipelineRun. Sources and
materialized Model outputs map to Datasets; a Model definition is not itself a
Dataset merely because it appears in the project graph.

OpenLineage is an observability interoperability boundary. It does not own
execution, retries, authorization, approval, publication, rollback, or canonical
LeapView identity. Lossless standard fields are used where available. Every
LeapView custom facet key must use the `leapView_` prefix, its facet type must
use the `LeapView` prefix and OpenLineage entity suffix, and it must contain
`_schemaURL`. That URL must be the single canonical URL for the schema version
and must identify an immutable release or content digest, never a mutable
branch. Such facets may carry generation, plan, selection, admission, and
qualification evidence without replacing the internal ledger.

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
- recover missed occurrences independently per schedule ID;
- allow schedule IDs or map iteration order to change Argo scheduling behavior;
- give concurrency or late-start policy a hidden runtime default;
- apply `concurrencyPolicy` to a manual or backfill invocation;
- queue a scheduled occurrence blocked by an external invocation;
- interpret scheduler host timezone as authored schedule timezone;
- change Argo cron, missed-run, or daylight-saving behavior while claiming
  compatibility;
- include nondeterministic estimates in execution identity;
- treat child run records as an independent dependency graph; or
- describe OpenLineage export as runtime or execution interoperability;
- emit an unprefixed LeapView OpenLineage facet or a mutable `_schemaURL`.

## Consequences

Authors gain a small declarative Pipeline resource that says what governed
asset to refresh, when scheduled invocations may occur, and how concurrency is
handled. They do not maintain an imperative DAG that duplicates Model lineage.
Generated schemas and documentation can describe every accepted field and
reject hidden or unsupported behavior before deployment.

Operators gain stable schedule, occurrence, plan, and run identities. Scheduler
outages, concurrent dispatchers, deployment races, replacement, and stale
publication have explicit outcomes. The chain from author intent to compiler
decision to physical execution becomes inspectable and exportable.

Scoped materialization can substantially reduce restatement work while
preserving a complete immutable candidate. It also makes relation-level reuse
and correct scope propagation mandatory. The current canonical restatement
path, scheduler grouping, interim TypeSpec shape, and run evidence do not yet
conform and must change.

Requiring explicit Pipeline-wide `timezone`, `concurrencyPolicy`, and
`startingDeadlineSeconds` when schedules exist makes scheduled documents
slightly more verbose. It avoids host-dependent timezones and behavioral
migrations under hidden defaults. Manual-only Pipelines carry none of those
irrelevant fields. Existing examples must be rewritten to the new
TypeSpec-generated shape.

Stable schedule-map keys become durable API and audit identities. Renaming a
schedule is an evidence change, not a scheduling-policy change. Tooling must
surface the renamed evidence identity, while occurrence uniqueness prevents the
rename from duplicating an execution for the same Pipeline and nominal time.

Argo-compatible DST behavior can skip a nonexistent spring-forward wall time or
produce two executions for a matching fall-back wall time. This is less
analytics-specific than the former LeapView rule, but it is predictable,
portable to an established controller contract, and testable without a bespoke
exception.

V1 intentionally omits arbitrary task DAGs, user code, containers, `Allow`
concurrency, unbounded catch-up, retries, timeout, event triggers, and backfill
grammar. Those omissions keep the trust and execution boundary narrow. They may
be added independently when a demonstrated use case has precise semantics.

GitHub Actions remains useful for repository CI and deployment automation.
Dagster, Temporal, and Open Workflow Specification remain comparison material.
dbt, Argo, and OpenLineage provide the normative generic semantics identified
above without becoming mandatory runtime infrastructure.

## Confirmation

Conformance requires evidence that:

- TypeSpec is the sole structural authority for Pipeline and generates Go,
  JSON Schema, documentation, and other required projections;
- the generated schema accepts only the closed selection fields and named
  string-valued schedule map, requires Pipeline-wide timezone, concurrency, and
  late-start policy exactly when schedules exist, and rejects both the old
  `semanticModel` plus `on.schedule` shape and the interim tagged `selection`
  and `triggers` shape;
- compilation of a SemanticModel selection produces the exact deterministic
  dbt-ancestor-equivalent Model closure, topological order, Source inputs, and
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
- the schedule map projects to Argo-equivalent scheduling independent of key
  names and map iteration, with conformance cases for overlapping expressions,
  identical expressions under different IDs, multiple missed expressions, and
  daylight-saving transitions;
- missed occurrences are evaluated Pipeline-wide and recovery creates at most
  one execution, with table-driven tests covering every supported
  `concurrencyPolicy` and `startingDeadlineSeconds` interaction;
- replacement revokes prior publication authority even when physical
  cancellation is delayed or impossible;
- Argo v4.0.8 conformance tests cover five-field grammar, ranges, steps, named
  values, `?`, macros, every-minute schedules, IANA zones, host-zone
  independence, spring-forward skips, fall-back duplicates, missed-run
  recovery, and deterministic nominal UTC time;
- scheduled-versus-scheduled, scheduled-versus-external, and
  external-versus-active tests enforce the collision matrix without allowing
  `Replace` to terminate a manual or backfill run;
- manual and scheduled invocations enforce the same authorization, workload,
  planning, execution, and publication boundaries after Pipeline invocation
  admission; and
- OpenLineage contract tests map Pipelines, runs, child executions, nominal
  time, Sources, and materialized outputs, require `leapView_` custom-facet keys
  and immutable canonical `_schemaURL` values, and never treat exported events
  as execution authority.
