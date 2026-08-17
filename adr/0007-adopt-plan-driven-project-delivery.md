# ADR-0007: Adopt plan-driven project delivery

Status: proposed

Decision date: 2026-08-17

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Related: [ADR-0004](0004-defer-incremental-project-reconciliation.md),
[ADR-0005](0005-use-project-wide-resource-graph.md), pending
[ADR-0006: Adopt an OSSIE-aligned typed semantic contract](https://github.com/flidai/leapview/pull/306),
[ADR-0008](0008-isolate-ducklake-candidate-physical-state.md),
[ADR-0009](0009-separate-control-and-physical-transactions.md), the
[project-delivery conformance specification](specifications/project-delivery-conformance.md),
the [SQLMesh plan model](https://sqlmesh.readthedocs.io/en/stable/concepts/plans/),
the [Terraform saved-plan model](https://developer.hashicorp.com/terraform/cli/commands/plan),
the [SLSA provenance model](https://slsa.dev/spec/v1.2/provenance),
the [OpenLineage specification](https://openlineage.io/docs/spec/), and the
[dbt build model](https://docs.getdbt.com/reference/commands/build)

## Context and problem statement

ADR-0005 made one complete project graph the atomic authored, validated,
deployed, and activated unit. ADR-0006 fixes the typed semantic contract used
to validate that graph. Neither decision defines the author-facing development
and promotion lifecycle.

The current lifecycle is described as `login -> dev -> publish`:

- `validate` performs target-independent local project checks;
- `plan` lists local resources without comparing them with a target;
- `dev` sends a source snapshot to a target, where LeapView computes an
  implicit deployment difference and builds a private candidate; and
- `publish` activates that retained candidate.

These mechanics preserve important safety properties, but the target-specific
plan is hidden inside candidate preparation. Authors cannot review complete
impact before expensive work begins, local inventory can be mistaken for a
deployment diff, and the separate `deploy` shortcut fragments the mental model.

LeapView needs one lifecycle for human, automation, local, self-hosted, and
hosted delivery. It must expose impact before activation, build privately,
publish exactly what was reviewed, preserve target-owned credentials, and
retain enough immutable evidence for approval, retry, rollback, and audit.

SQLMesh is the closest precedent for dependency-aware target planning and
isolated evaluation. Terraform saved plans establish the useful rule that a
reviewed plan becomes stale when its base state changes. dbt provides familiar
project, build, and dependency concepts. LeapView adopts those lifecycle
invariants without copying another product's command surface or environment
topology.

The question is: what delivery lifecycle must every LeapView project path
implement?

## Decision drivers

- Authors must see direct and downstream impact before activation and before
  avoidable expensive data work.
- Development evaluation must be private and must not alter active serving
  state.
- Publication must activate the exact candidate and evidence that were
  reviewed; it must never rebuild from a moving worktree or source ref.
- Plans, candidates, approvals, and publications need stable identities and
  idempotent retry behavior.
- Publication must use optimistic concurrency against one simple authoritative
  target revision.
- Computational equivalence must distinguish execution inputs from provenance
  and governance metadata.
- Live data inputs need explicit pinned, bounded, or observed semantics rather
  than an assumption that every source can be frozen at planning time.
- A concurrent target change must always prevent stale publication, while a
  target may still allow private qualification against a retained base.
- Development and production must use the same state machine even when their
  policies automate different interactions.
- Cross-target promotion must preserve portable source identity while
  requalifying target-owned state.
- Data restatement must be explicit and separate from a project-code change.
- Rollback must say precisely which serving and physical effects can be
  restored and which external effects cannot be reversed.
- The public workflow should expose only concepts on which an author or
  operator makes a meaningful decision.

## Considered options

### Keep planning implicit inside `dev`

This preserves the smallest command surface and the current exact-candidate
publication guarantee. It was rejected because authors and approval systems
cannot inspect or persist target-specific impact before candidate work begins.

### Reproduce the dbt development command surface

This would expose separate parse, compile, run, test, and build operations. It
was rejected as the governing workflow because those commands do not define
LeapView's target-owned bindings, managed data, semantic behavior, dashboard
preview, approval, and atomic serving-state activation.

### Adopt SQLMesh environments and commands wholesale

SQLMesh provides the closest lifecycle reference, but its complete environment
topology does not match LeapView. A LeapView instance already owns a permanently
bound environment, compute, storage, secrets, policy, and failure boundary.
Candidates provide private evaluation without becoming authored namespaces.

### Generate the deployment plan entirely on the client

This would permit offline planning, but the client cannot authoritatively know
the target's active generation, connection bindings, managed-data pins, policy,
runtime capabilities, or retained bases. Local validation remains useful, but
a deployment plan must be target-owned evidence.

### Adopt a target-owned plan-driven lifecycle

This option makes an immutable target-aware plan the bridge between a portable
source snapshot and a private immutable candidate, then publishes exactly that
candidate. It is selected because it preserves LeapView's security and runtime
boundaries while making impact, computation, qualification, and promotion
explicit.

## Decision outcome

An immutable source snapshot is the portable input to three durable lifecycle
objects on which users make decisions:

```text
Source snapshot
      |
      v
    Plan -> Candidate -> Generation
     |          |             |
what changes   build and    published
               verify       atomically
```

The canonical human workflow is `plan -> build -> publish`. `validate` remains
local feedback, not durable deployment state. Preview, tests, audits, data
diffs, and lineage are evidence attached to a candidate rather than additional
lifecycle stages.

`build` is chosen instead of `apply` because the operation creates private
candidate state rather than changing the active target. Browser, CLI, API,
agent, CI, local evaluation, and hosted workflows use the same identities and
transitions.

### Capture and local validation

LeapView captures a coherent content-addressed snapshot of every project file
reachable from the project entrypoint. The snapshot receives a portable source
digest and may carry source-control and build provenance. File paths, branch
names, and working directories aid discovery; they are not deployment identity.

Local validation performs deterministic checks that do not require
authoritative target state, including schema, complete-graph references,
contracts, and target-independent unit tests. It cannot authorize candidate
preparation or publication.

### Target-aware immutable plans

A target creates a plan from one exact source snapshot, active base generation,
and target revision. Every target/project/environment scope owns a monotonic
`target_revision`. The same SQLite transaction that changes active generation
or another plan-invalidating target fact increments that revision. Query
leases, sessions, audit appends, and other non-invalidating operational changes
do not increment it.

The plan records `base_generation` and `base_target_revision`. Publication uses
those values as its concurrency fence. Detailed target component digests remain
audit and explanation evidence, not a second correctness primitive.

Every plan declares an operation kind. Initial kinds are `code_change`,
`restatement`, `binding_change`, and `policy_change`. The plan record separates
three categories:

- **Execution inputs** determine computation and physical reuse. They include
  portable source and project artifact digests, compiler and executable
  contract versions, dependency locks, result-affecting non-secret
  configuration, semantic connection-binding identity, runtime compatibility,
  managed-data revisions, and declared data-input constraints.
- **Provenance** explains where the portable input came from. It includes
  repository, source revision, builder, build definition, and signed
  attestations. Provenance can be required by publication policy without
  changing computational equivalence when the portable bytes are identical.
- **Governance** controls whether and how work may proceed. It includes expiry,
  authorization scope, approval policy, qualification requirements, and target
  policy. Governance can require a new plan and candidate qualification or
  block publication without falsely making unchanged physical work
  non-reusable.

The target maintains a non-secret semantic identity for each connection
binding. Rotating a credential secret while preserving endpoint, database,
catalog, schema, role, privileges, and other declared execution semantics does
not change execution identity. A change to those semantics does. Resolved
credential values never enter source, plan, manifest, checkpoint, or audit
evidence.

Repository URLs and revision strings are provenance claims rather than proof.
When policy requires trusted provenance, the target verifies an attestation
binding source, build definition, builder, and resulting artifact. Users do not
manually pass internal digests through routine CLI commands.

### Data-input modes

Each data input in a plan declares one mode:

- `pinned` binds an immutable dataset snapshot or managed-data revision. Build
  must read exactly that version.
- `bounded` binds an interval, as-of point, or upper watermark. Build must read
  exactly the declared bound; data arriving outside it does not stale the plan.
- `observed` cannot be fixed authoritatively at planning time. Build resolves
  and records the exact observed value, labels reproducibility as weaker, and
  proceeds only when target policy permits it.

Planning reports the mode and limitation. It must not present an estimate or
latest-at-plan observation as a pinned version. At build time, pinned and
bounded inputs must satisfy their declared constraints; observed inputs become
part of the candidate's resolved execution evidence. Target policy may forbid
observed inputs for protected publication.

### Plan contents and staleness

A plan reports, when applicable:

- added, removed, directly modified, and indirectly affected resources;
- contract-breaking, semantic, authorization, and policy-relevant changes;
- required materialization, refresh, backfill, or restatement scope;
- work that can be safely reused;
- tests, contracts, and data audits required for qualification;
- target binding or managed-data changes; and
- bounded work or cost estimates when evidence supports them.

Ambiguous destructive, compatibility-breaking, or reprocessing behavior must
be resolved explicitly by policy or an authorized decision. It is never
silently categorized as safe.

Plans are immutable, inspectable, expiring records. A plan is stale for
publication whenever active generation or `target_revision` differs from its
base. Cross-target, cross-project, source-mismatched, and expired plans fail
closed. Replanning creates new evidence and never carries approval forward.

### Immutable candidate build and verification

Building a plan executes one owner-private build attempt. A successful attempt
seals at most one immutable candidate binding the exact plan, source,
execution inputs, resolved observed inputs, immutable physical catalog artifact,
tests, audits, data diffs, lineage, and runtime evidence. Retrying a canonical
build after seal returns the same candidate; disposable pre-seal work may be
recomputed without creating a candidate. Changed inputs or deliberate
recomputation require a new plan and candidate identity.

Publication must reject a stale plan or candidate. Build behavior is target
policy:

- a target may reject a stale plan before expensive work; or
- when the exact base and required inputs remain retained, a target may finish
  private qualification against that base for preview and CI evidence.

The latter candidate is marked stale for publication. Staleness is an immutable
base-revision mismatch and a derived publication-eligibility condition, not
candidate content, another lifecycle object, or a mutable candidate revision.
Because target revision is monotonic, a stale candidate cannot regain
eligibility under the same plan. If the target changes while a build is running,
the same rule applies when the candidate seals.

A blocking validation or audit failure creates no ready candidate and leaves
active serving state untouched. Non-blocking checks are declared and remain
visible. Data-diff evidence labels whether it is complete, bounded, or sampled.
Preview uses the reviewer's live authorization and never grants direct access
to target-controlled physical storage.

Candidate physical isolation, immutable catalog sealing, and safe reuse are governed by
[ADR-0008](0008-isolate-ducklake-candidate-physical-state.md). Cross-store
seal recovery and global retention are governed by
[ADR-0009](0009-separate-control-and-physical-transactions.md).

### Exact publication with optimistic concurrency

Publication activates one ready, publication-eligible immutable candidate and
its exact plan. Approval binds both digests and cannot approve a later candidate
from the same source revision, plan request, or development session.

Immediately before cutover, the target rechecks live authorization, approval,
plan expiry, candidate qualification and retention, required input
availability, active base generation, and `target_revision`. It then atomically
compare-and-swaps the active generation and increments the revision. A mismatch
rejects publication and never overwrites, rebases, or reverts concurrent work.

Publication does not reread local files, resolve a moving source ref, recompile,
rerun materialization, silently replan, mutate candidate contents, change
target, or rerun failed qualification. A conclusively failed transition leaves
the last valid generation active.

A timeout, lost response, or crash after activation begins is indeterminate,
not failed. Retry reconciles the durable publication identity and returns the
committed result or proves that cutover did not occur before attempting the
same transition again. The recovery protocol is defined by ADR-0009.

### Rollback and retention semantics

Rollback selects a retained qualified generation through a governed target
operation; it is not a source checkout, reverse plan, or rebuild. It records a
new immutable audit event.

Every generation declares one rollback class:

- `rollback_safe`: all required serving and physical references are immutable,
  retained, and exactly restorable;
- `serving_safe`: LeapView serving references are restorable, but declared
  external effects are not reversed; or
- `non_reversible`: pointer rollback cannot restore a declared physical or
  external effect.

The target reports rollback class, expiry, unavailable dependencies, and
expected recovery scope. Weaker classes require explicit consequence evidence
and an operator recovery path. Physical reference and retention mechanics are
defined by ADR-0009.

### Development ergonomics and automation

`leapview dev` watches reachable project files, validates locally, requests a
fresh target plan, builds a private candidate, runs required checks, and moves
the development session's latest-candidate pointer. Invalid edits preserve the
last valid candidate. File watching never activates shared serving state.

The explicit commands are:

```sh
leapview plan --target <profile>
leapview build <plan-id>
leapview publish <candidate-id>
```

Plan binds the target and candidate inherits it. Later commands do not select a
different destination; an optional target argument can only assert the bound
target. Preview is a candidate URL or view, not a state-changing command.

Headless workflows use the same plan and candidate APIs. A machine-oriented
`release` convenience may compose plan, build, and publication request while
emitting durable identities. `deploy` is removed from the normal human model.
No convenience bypasses qualification, policy, optimistic concurrency, or
immutable evidence.

### Cross-target promotion

Plans and candidates are target-specific. Promotion carries the exact portable
source bytes, artifact identity, source revision, and verified provenance to
the destination. The destination creates a new plan and candidate because its
base, target revision, bindings, policies, data pins, capabilities, and runtime
may differ.

Development qualification remains provenance, not proof that production
qualification can be skipped. Physical work may be reused at the destination
only when independently proven safe under its execution identities.

### Explicit data restatement

Refreshing or restating data without changing project code is a separate plan
operation. It records requested and effective intervals, upstream input modes
and versions, downstream scope, overwrite or merge strategy, idempotency
identity, qualification, and defensible estimates. Any widening from requested
to effective interval is explained before work begins.

### Audit and lineage evidence

Every lifecycle transition appends an immutable event binding actor or workload
identity, operation kind, target, source, plan, candidate or generation,
canonical evidence digests, data versions, timestamps, and result.

Runtime lineage and data-quality assertions should be exportable through
OpenLineage-compatible events where that model is lossless. LeapView evidence
remains authoritative for authorization, approval, publication, and rollback.

### Prohibited shortcuts

The implementation must not:

- present target-independent local inventory as a deployment diff;
- build or publish without an immutable target plan;
- mutate a candidate or carry its approval to another candidate;
- publish by rereading a worktree or mutable source ref;
- publish when active generation or `target_revision` differs from the plan;
- activate shared serving state from a file watcher;
- reuse a plan or candidate across targets;
- treat provenance metadata as computational identity;
- treat an observed data input as pinned;
- hide blocking qualification failure behind a ready candidate;
- expose candidate physical data outside target authorization;
- conflate code change with data restatement; or
- expose partially prepared resources as active serving state.

## Consequences

Authors and reviewers gain one durable explanation of what will change, what
will be computed, what was tested, and what exact object can be published.
Preview, approval, retry, publication, rollback, and audit become one evidence
chain.

The public model remains small: authors normally reason about plans and
candidates; operators occasionally select generations. Internal digests,
target revisions, attempts, and request identities remain inspection and audit
details.

A monotonic target revision makes optimistic concurrency simple, but every
plan-invalidating target mutation must increment it atomically. Over-broad
increments cause needless replanning; missed increments violate correctness.
Detailed evidence digests remain necessary to explain revision changes and
detect implementation drift.

Separating execution, provenance, and governance avoids needless physical
recomputation after credential rotation or policy-only changes. It also
requires precise classification: a policy or binding change that affects rows,
materialization, or executable semantics belongs in execution identity even if
it originated as an operational change.

Pinned, bounded, and observed inputs make live-source behavior honest. Observed
inputs trade reproducibility for practicality and may be unavailable on strict
targets. Bounded inputs require connectors capable of enforcing the declared
bound rather than merely reporting it.

Allowing policy-controlled stale builds preserves useful CI evidence on busy
targets but consumes retained-base storage and compute for a candidate that
cannot publish. Quotas and early rejection remain target policy.

This decision does not authorize incremental compilation or partial serving
publication. ADR-0004's measurement gate remains active. Implementations may
optimize affected work while still validating and publishing one complete
generation.

Existing CLI guides, contributor tasks, CI examples, local `plan` semantics,
and `deploy` naming must be reconciled around `plan -> build -> publish`.

## Confirmation

Conformance requires evidence that:

- all surfaces expose the same `Source snapshot -> Plan -> Candidate ->
  Generation` lifecycle and exact `plan -> build -> publish` semantics;
- target revision changes atomically with every plan-invalidating target
  mutation and publication CAS rejects a stale base;
- execution, provenance, and governance identities remain distinct and secrets
  never enter durable non-secret evidence;
- pinned, bounded, and observed inputs enforce their declared semantics;
- candidates are immutable, exact, privately qualified objects and stale-build
  policy can never authorize stale publication;
- publication activates the exact reviewed candidate without rebuilding;
- development automation cannot publish, and destination targets replan the
  same portable source bytes; and
- rollback, restatement, audit, and failure outcomes preserve the semantics in
  this decision.

The complete architecture, contract, concurrency, crash-recovery, and
end-to-end matrix lives in the linked
[project-delivery conformance specification](specifications/project-delivery-conformance.md).
