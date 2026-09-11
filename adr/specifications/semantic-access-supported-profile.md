# ADR-0017 supported qualification profile

Status: **active for the qualified supported profile**.

This document records the combinations exercised by FAI-648. It does not add
an authorization mode, change runtime behavior, or make an unsupported
combination available. A protected request outside this profile must continue
to fail closed at the existing compiler, consumer, plan, cache, or audit
boundary. FAI-649 binds this exact profile into immutable plan and final
pre-commit activation evidence.

## Qualified boundary

| Area | Included | Excluded or rejected |
| --- | --- | --- |
| Attribute authority | Access-owned typed registry, direct assignments, and active-group-derived assignments | Request-supplied attributes, unsigned claims, mutable authored copies, and external trusted-claim provider adapters. The verified-envelope and mapping primitives have component evidence, but no external provider adapter is qualified. |
| Publication | Canonical FAI-620 projection identity and immutable FAI-622 compatibility, security-impact, validation, widening-approval, and replay evidence | Indeterminate evidence, caller-asserted publication identity, and production cutover |
| Consumers | Request-bound dashboard query authorization, Explore protected catalog projection, Semantic API protected model listing and metadata, agent/MCP semantic resource reads, protected materialize execution, independently admitted totals, and governed Arrow release covered by the matrix's named tests | Suggestions/raw-value and other FLT-09 surfaces without named qualification, scheduled/export/embed paths, new consumers, provider-specific adapters, unrestricted physical preview, and any path without a request-bound semantic consumer |
| Plans | Governed scans, the tested left-outer relationship join and self-join barrier occurrences, aggregate/count/rows plans, independently admitted totals, tested derived-metric execution, named/intermediate filters, and guarded result-cache reuse | Other outer-join and many-to-many combinations, unqualified PLN-09 shapes, unsupported rollup/substitution, and multi-query/opaque result reuse; these are rejected or bypass the protected cache rather than being treated as qualified |
| Cache | FAI-645 identity-partitioned result reuse, lifecycle/revision revalidation, waiter revalidation, and protected suggestion-cache bypass | Reuse without exact principal, actor, publication, policy, registry, control, and lifecycle identity |
| Audit | Durable redacted decision-event persistence, principal/actor/policy/decision binding exercised by the named adapter tests, and audit-failure denial | A claim that every generation/member/attribute-version/denial field is qualified by one executable scenario, raw semantic attribute values, SQL/predicate text, best-effort audit, and alternate audit stores |

## Cross-layer evidence

The supported components use the following intended authority flow without
adding another evaluator:

```text
SemanticModel canonical projection and publication evidence
    -> typed registry and coherent control snapshot
    -> deterministic policy compilation and evaluation
    -> SecurityBarrier placement at every protected scan
    -> request-bound consumer authorization
    -> identity-partitioned cache validation
    -> durable redacted audit before disclosure
```

Complete cross-layer qualification remains **PARTIAL**. Current-main evidence
qualifies the linked component and composition boundaries separately, but no
single executable qualification test traverses this entire chain. The matrix
therefore must not be read as end-to-end activation evidence.

The per-requirement links and statuses are in the
[qualification matrix](semantic-access-qualification.md). PostgreSQL evidence
uses the repository's required PostgreSQL 18 conformance lane for the proven
definition, assignment, disable, tombstone/reincarnation, replay, concurrent
update, scoped identity, logical cache-invalidation, and audit-persistence
slices. Provider-backed claims, complete rollback/restart composition, and the
full cross-layer chain remain outside the qualified boundary.

## Fail-closed exclusions

- Unknown, missing, stale, disabled, tombstoned, foreign-instance, or
  digest-inconsistent authority cannot be reclassified as public.
- A protected plan without validated compiled policy and exactly placed
  barriers is rejected before execution.
- A protected consumer without request-bound principal/actor and authority
  evidence is rejected before result disclosure.
- A cache entry or coalesced result with mismatched lifecycle or policy identity
  is rejected; cache absence does not create authorization.
- Audit persistence failure prevents protected admission or release.
- Standalone DataPolicy is absent from public authoring and rejected on new
  activation. Historical rollback retains its original restrictions read-only;
  see the [activation and cutover contract](semantic-access-activation-cutover.md).

## Interpretation

`PASS` in the matrix means the requirement has a current production owner and
named executable evidence for the supported profile. `PARTIAL` is not an
allowlist entry: it records a narrower proven slice or an unsupported path that
still lacks full qualification. The two former cutover-owned failures are
qualified by FAI-649. VAL-11 remains Partial and is not promoted by activation.
