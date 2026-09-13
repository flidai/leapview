# ADR-0018 final conformance evidence reconciliation

Status: reconciled against merged behavior; final closure pending

Evidence snapshot: 2026-09-13

Repository baseline: `ef87272c377c630279d2e38ea14e1d0f8b323d80`

Governing decision:
[ADR-0018](../0018-retain-project-as-the-durable-deployment-namespace.md)

Normative requirements:
[Project namespace conformance](project-namespace-conformance.md)

Original disposition map:
[ADR-0018 implementation delta](project-namespace-implementation-delta.md)

## Purpose and status rules

This inventory prepares FAI-679's final evidence review without claiming that
ADR-0018 is implemented. It records exactly one accountable implementation
owner for each of the 54 normative requirements, merged change and validation
evidence, and the genuine evidence or documentation gaps that still prevent
closure.

`Baseline` means the requirement was already conforming when FAI-665 recorded
the delta and remains owned by the cited subsystem; it is an explicit owner,
not an unowned gap. A supporting PR in an evidence cell does not create a
second implementation owner.

The evidence states are:

- **Main**: the cited implementation is merged on the repository baseline.
- **Partial**: merged implementation covers a defined slice, but the full
  requirement still lacks maintained evidence.
- **Gap**: required documentation alignment or maintained evidence is absent.

## Dependency snapshot

| Dependency | Repository evidence | Current effect |
| --- | --- | --- |
| FAI-670 | [PR #542](https://github.com/flidai/leapview/pull/542), head `a4f7efa53`, merged as `c9e6482a8`; required CI and Security gates passed | Merged. RID lifecycle evidence is authoritative on main; acceptance evidence and Linear status were reconciled to Done on 2026-09-13. |
| FAI-671 | [PR #546](https://github.com/flidai/leapview/pull/546), head `01fc491eb`, merged as `4435ea6c1`; required CI and Security gates passed | Merged. The maintained runtime-boundary audit deliberately identifies its non-exhaustive BND/API evidence. |
| FAI-675 | [PR #549](https://github.com/flidai/leapview/pull/549), head `fbe7ffb37`, merged as `372039da7`; required CI and Security gates passed | Merged. Closed-graph and unsupported-foreign-reference evidence is available on main. |
| FAI-677 | Local documentation work was recorded in Linear, but no dedicated merged PR or main commit was found | ISO-03 remains open because current maintained documentation still describes an authored Project manifest. |
| FAI-678 | [PR #581](https://github.com/flidai/leapview/pull/581), head `e1c56b830`, merged as `f4ad4a032`; exact merge-queue CI and Security passed | Merged. dbt and independent multi-Source adoption evidence is available on main. |
| FAI-679 | This reconciliation inventory | Final documentation alignment, remaining evidence closure, exact-candidate protected validation, and ADR status update remain open. |

## Requirement evidence map

| Requirement | Sole implementation owner | Change evidence | Validation evidence | Current status / remaining evidence |
| --- | --- | --- | --- | --- |
| PRJ-01 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | PostgreSQL atomic replay/conflict and concurrent same-tuple bootstrap tests | — (Main) |
| PRJ-02 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Issuer mint-once, external UID, and replacement-before-HTTP tests | — (Main) |
| PRJ-03 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Unauthenticated bootstrap rejection and instance-admin bootstrap contract tests | — (Main) |
| PRJ-04 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Atomic claim/audit replay, conflict audit, and audit-failure rollback tests | — (Main) |
| PRJ-05 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Conflicting Project/environment bootstrap preserves immutable claim state | — (Main) |
| PRJ-06 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Authority reuse across targets; independent-environment planning in [#512](https://github.com/flidai/leapview/pull/512) | — (Main) |
| PRJ-07 | Baseline | [#474](https://github.com/flidai/leapview/pull/474) inventory / current serving-state repository | Environment-qualified active-pointer CAS and foreign-Project rejection tests | — (Main) |
| PRJ-08 | FAI-667 | [#499](https://github.com/flidai/leapview/pull/499) / `fdba57d29` | Development publishing bootstraps and reuses issuer identity before Project work | — (Main) |
| PRJ-09 | Baseline | Bound-Project read contract and Project-free browser navigation on main | Generated API inventory and Develop route tests | — (Main) |
| BND-01 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Six-kind schema/discovery and forbidden Project/access/publication authoring tests | — (Main) |
| BND-02 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Recursive deterministic conventional-directory discovery tests | — (Main) |
| BND-03 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Portable deterministic bundle and checkout-independent digest tests | — (Main) |
| BND-04 | FAI-669 | [#512](https://github.com/flidai/leapview/pull/512) / `62d039399` | Exact persisted-plan, stale-target, rehashed-plan, and lock-order admission tests | — (Main) |
| BND-05 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Rootless graph, missing endpoint, global ID uniqueness, cycle, and deterministic-byte tests | — (Main) |
| BND-06 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1` | Locator, claim, cache, and idempotency boundary tests cover only part of the required inventory | Complete the documented storage/retention/cleanup, audit/lineage/error, release, and cache-consumer inventory (Partial) |
| BND-07 | FAI-669 | [#512](https://github.com/flidai/leapview/pull/512) / `62d039399` | Same bundle planned independently for multiple environment targets without copied target state | — (Main) |
| RID-01 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Duplicate authored ID across kinds rejected before graph construction | — (Main) |
| RID-02 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | PostgreSQL first-activation allocation and sealed-inventory admission qualification | — (Main) |
| RID-03 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | Instance/Project isolation and independent random UID allocation qualification | — (Main) |
| RID-04 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) plus canonical evidence from [#534](https://github.com/flidai/leapview/pull/534) | Exact instance, Project, authored identity, kind, contract evidence, and generation binding tests | — (Main) |
| RID-05 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | Stable replay/reuse and kind-conflict rollback qualification | — (Main) |
| RID-06 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | `TestPostgresResourceUIDAdmissionAndActivationQualification` covers immutable tombstone, authorized exact-generation restore, same-scope historical rollback, atomic audit, and rollback safety | — (Main) |
| RID-07 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8` | Registry-key validation excludes paths, names, environments, dbt identifiers, and artifact hashes | — (Main) |
| API-01 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1` | Existing Project-qualified deployment/authorization/audit/lineage evidence plus generated-locator tests | Complete the end-to-end public identity propagation inventory (Partial) |
| API-02 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1` | Shared query selector, browser, agent-context, and generated locator rejection tests | Complete search/release and remaining request-body selector inventory (Partial) |
| API-03 | FAI-669 | [#512](https://github.com/flidai/leapview/pull/512) / `62d039399`; #546 supplies supporting generated-locator coverage | Foreign scope and missing claim rejected before source I/O, repository mutation, or workload admission | — (Main) |
| API-04 | Baseline | Explicit serving identity in authorization snapshots and runtime installation | Capability, malformed identity, and authorization-install fail-closed tests | — (Main) |
| API-05 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1` is supporting but explicitly non-exhaustive | Generated locator tests do not cover the complete list/search/discovery/autocomplete/audit/lineage/error inventory | Complete the systematic filtered-surface inventory (Partial) |
| API-06 | FAI-671 | [#546](https://github.com/flidai/leapview/pull/546) / `4435ea6c1` | Project/target/environment/candidate cache partitions and replay reauthorization tests | Prove authorization and generation identity at every cache consumer (Partial) |
| API-07 | Baseline | Runtime-host and serving-state admission on main; #546 adds durable claim validation | Mixed Project/environment and foreign active-state rejection tests | — (Main) |
| ENV-01 | FAI-669 | [#512](https://github.com/flidai/leapview/pull/512) / `62d039399` | Same Project UID and source bundle planned independently for dev/staging/production targets | — (Main) |
| ENV-02 | Baseline | Immutable serving-generation repository on main | Project/environment binding and immutable idempotent publication tests | — (Main) |
| ENV-03 | Baseline | Target revision/base-generation fences and activation CAS on main | Stale delivery and failed-gate evidence preserve prior active generation | — (Main) |
| ENV-04 | FAI-670 | [#542](https://github.com/flidai/leapview/pull/542) / `c9e6482a8`; delivery binding from [#512](https://github.com/flidai/leapview/pull/512) is supporting evidence | ResourceUID qualification selects exact retained generations, preserves same-scope UID history, and rejects foreign Project/instance restore or lookup | — (Main) |
| ENV-05 | Baseline | Delivery plan/release provenance on main; #512 preserves separation | Source digest/commit remains distinct from generation and execution identity | — (Main) |
| SEM-01 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7` | Missing, foreign-qualified, wrong-kind, and incomplete semantic dependency rejection corpus | — (Main) |
| SEM-02 | Baseline | Immutable graph lease and active serving-state graph reader on main | Exact-generation graph pinning and scope-mismatch tests | — (Main) |
| SEM-03 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7` | Provenance cannot create a semantic resolver alias; ordinary Models retain normal runtime rules | — (Main) |
| SEM-04 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | `TestDBTMultiSourceProjectClosure` resolves the upstream package before Parquet handoff and rejects package-, Project-, and missing live Model references without a runtime dbt resolver | — (Main) |
| SEM-05 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7` | Closed topology and foreign-reference tests are independent of future hosting topology | — (Main) |
| ISO-01 | Baseline | Project-qualified authorization plus instance-bound runtime/claim evidence on main | Authorization snapshot and bound runtime tests | — (Main) |
| ISO-02 | Baseline | Singleton Project/environment runtime topology on main | Second-Project and environment-mismatch admission rejection tests | — (Main) |
| ISO-03 | FAI-677 | No dedicated implementation PR | ADR language is explicit; adjacent public documentation and product claims have not been fully audited | Implement and validate FAI-677 (Gap) |
| XPR-01 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7`; [#581](https://github.com/flidai/leapview/pull/581) supplies upstream-package coverage | Deterministic foreign-qualified reference rejection without foreign catalog disclosure | — (Main) |
| XPR-02 | FAI-675 | [#549](https://github.com/flidai/leapview/pull/549) / `372039da7` | Unsupported `projectOutput` Source variant fails closed without selector disclosure | — (Main) |
| XPR-03 | Baseline | Ordinary Connection-backed Sources and reusable closed source bundles on main | Source-to-Model-to-SemanticModel closed graph compilation tests | — (Main) |
| XPR-04 | Baseline | Exact active serving-state graph lease on main | Runtime generation pinning and Project scope-mismatch tests | — (Main) |
| XPR-05 | Baseline | ADR-0018 cross-Project boundary requires a separate future ADR | Specification review verifies no native import authority in this profile | — (Main) |
| DBT-01 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | `task dbt:warehouse:proof` and `TestDBTMultiSourceProjectClosure` exercise the reference adoption path | — (Main) |
| DBT-02 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | The proof rejects dbt project, repository, invocation, manifest, target, and path values from portable identity while reusing one issuer ProjectUID | — (Main) |
| DBT-03 | FAI-666 | [#495](https://github.com/flidai/leapview/pull/495) / `5d766dc4f` | Conventional-directory dbt/LeapView example contains no Project manifest | — (Main) |
| DBT-04 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | The same portable bundle is proven against distinct dev/prod target Connection and Source bindings without changing Project identity | — (Main) |
| DBT-05 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | Upstream package resolution occurs before the physical warehouse handoff; no dbt Mesh runtime resolver is introduced | — (Main) |
| DBT-06 | FAI-678 | [#581](https://github.com/flidai/leapview/pull/581) / `f4ad4a032` | The proof compiles two Connections and Sources from independent producers into one closed semantic graph; PostgreSQL qualification preserves the complete generation ResourceUID inventory | — (Main) |

## Ownership audit

- Requirements mapped: **54**.
- Unique requirement identifiers: **54**.
- Requirements without an accountable owner: **0**.
- Requirements with more than one accountable owner: **0**.
- Requirements with merged maintained evidence: **48**.
- Requirements with merged partial evidence: **5** (`BND-06`, `API-01`,
  `API-02`, `API-05`, and `API-06`).
- Requirements with a documentation evidence gap: **1** (`ISO-03`).
- Supporting evidence that crosses issue boundaries is labeled as supporting
  rather than being assigned a second owner.

## Final blockers

ADR-0018 must remain `pending` until all of the following are true:

1. FAI-677 lands the ISO-03 public-documentation and product-claim alignment.
   Current `spec.md` still describes an authored Project manifest and therefore
   contradicts the Project-free source model.
2. FAI-679 either maps maintained evidence for `BND-06`, `API-01`, `API-02`,
   `API-05`, and `API-06`, or adds evidence-only coverage without changing
   implementation behavior. The merged FAI-671 audit explicitly says its
   surface and cache-consumer inventories are not exhaustive.
3. The exact documentation/evidence candidate passes generation, documentation,
   conformance, CI, and Security validation before ADR-0018's Implementation
   field changes.

FAI-670, FAI-671, FAI-675, and FAI-678 are no longer implementation merge
blockers. Their merged evidence replaces the stale provisional and gap claims
from the previous snapshot.
