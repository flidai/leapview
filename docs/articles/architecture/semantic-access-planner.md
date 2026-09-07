# Semantic access planner boundary

FAI-641 consumes the FAI-639 compiler/evaluator handoff. FAI-642 continues to
own discovery, principal-facing catalog filtering, and consumer adoption.
The planner boundary is independent of consumer composition.

## Enforcement design

Policy-bearing semantic models require trusted authorization input when a
plan is built. The planner must validate the compiled policy against its
semantic model, evaluate current registry/control and effective-value evidence
through FAI-639, and reject missing, invalid, stale, or denied input. The
planner does not infer a principal or ingest raw provider claims.

Every protected dataset occurrence must be restricted before joins and
aggregation. A distinct PlanIR `SecurityBarrier` carries the typed predicate
and policy/decision identity over its governed scan. Relationship targets
currently enter SQL implicitly through `TraverseRelationship`; protected
targets therefore need explicit scan/barrier inputs too. Self-joins and
role-playing paths must retain separate occurrence identities.

The renderer must lower a barrier to a restricted relation, not accumulate its
predicate into the final request `WHERE`. This preserves outer-join null
extension and prevents unauthorized rows from contributing to aggregate
populations. Request and named metric filters remain separate operations.

Graph validation must reject missing, duplicate, altered, or relocated
barriers. Rewrite validation must retain their scan/relationship and
authorization identity. Bundle coalescing must not discard protection while
sharing scans. Unsupported substitutions fail closed; this slice does not
implement rollup adoption or cache lifecycle.

## Admission and plan identity

`WithSemanticAccess` accepts an opaque compiled policy and a trusted authority
provider. Final plan construction recompiles against the immutable serving
model, compares policy digests, and evaluates current evidence through FAI-639.
It admits requested members before inserting barriers. A grant-only decision
still receives a barrier carrying its authorization identity.

Insertion follows bundle scan coalescing and precedes rendering and dependency
identity construction. Bundle branches reuse the same admission decision;
they do not independently sample authority. Canonical graph serialization
includes policy and decision digests. Private seals retain scan bindings,
predicates, and occurrence placement so subsequent mutation cannot silently
remove protection. Unsupported rewrites are rejected, not approximated.

The current relationship renderer uses left joins. A protected target is an
explicit scan/barrier input rendered as a derived relation on the joined side.
This implementation does not introduce new relationship join kinds or claim
provider-specific optimizer security guarantees.

## Consumer composition and deferred lifecycle

FAI-642 provides the [shared consumer boundary](/docs/architecture/semantic-access-consumers)
for authoritative instance/generation, principal/group, registry/control,
opaque assignment evidence, and current observations. Direct SQL and
authoring-preview paths remain separately classified. A planner barrier alone
is not consumer authority, and raw claims are not trusted evidence.
VAL-11 remains Partial.

Authorization is sampled at plan admission. These seals do not constitute a
runtime revocation watcher or permission to reuse an old plan after authority
changes. FAI-642 revalidates consumer execution and Arrow delivery;
policy-aware lifecycle/cache reuse remains FAI-645 work.

One concrete deferred path is `query.PrepareRepresentativePlans`, including
its explicit-relationship verification helper. It constructs new planners
without semantic authority; `materialize.Runtime` uses this verification path.
Policy-bearing models therefore fail closed there until the verification and
consumer composition work supplies an appropriate trusted context. Even a
configured planner's explicit-relationship verification currently constructs
a separate candidate planner. This is a neutral activation-context dependency,
not permission for FAI-642 to fabricate a subject. FAI-648/FAI-649 own the
remaining qualification/cutover work; FAI-641 does not bypass admission for
verification or claim protected-model deployment qualification.
