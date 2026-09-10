# ADR-0017 supported qualification profile

Status: **qualification boundary only; no production activation**.

This document records the combinations exercised by FAI-648. It does not add
an authorization mode, change runtime behavior, or make an unsupported
combination available. A protected request outside this profile must continue
to fail closed at the existing compiler, consumer, plan, cache, or audit
boundary. FAI-649 owns activation and cutover.

## Qualified boundary

| Area | Included | Excluded or rejected |
| --- | --- | --- |
| Attribute authority | Access-owned typed registry, direct assignments, group-derived assignments, and configured trusted-claim evidence | Request-supplied attributes, unsigned claims, unknown providers, and mutable authored copies |
| Publication | Canonical FAI-620 projection identity and immutable FAI-622 compatibility, security-impact, validation, widening-approval, and replay evidence | Indeterminate evidence, caller-asserted publication identity, and production cutover |
| Consumers | Dashboard query execution, Explore/catalog projection, semantic API metadata/query paths, agent/MCP semantic resource reads, suggestions/raw values, totals, and governed Arrow result delivery | New consumers, provider-specific adapters, unrestricted physical preview, and any path without a request-bound semantic consumer |
| Plans | Governed scans, inner/outer relationship joins exercised by current planners, role-playing/self-join occurrences, aggregate/count/rows/totals, named and intermediate filters, derived metrics, and guarded result-cache reuse | Unsupported rollup/substitution and multi-query/opaque result reuse; these are deterministically rejected or bypass the protected cache rather than being treated as qualified |
| Cache | FAI-645 identity-partitioned result reuse, lifecycle/revision revalidation, waiter revalidation, and protected suggestion-cache bypass | Reuse without exact principal, actor, publication, policy, registry, control, and lifecycle identity |
| Audit | Canonical decision events before protected admission or output, actor/principal and generation/member/policy binding, redacted metadata, retained identity replay, and failure denial | Raw semantic attribute values, SQL/predicate text, best-effort audit, and alternate audit stores |

## Cross-layer evidence

The qualified flow uses existing authorities without adding another evaluator:

```text
SemanticModel canonical projection and publication evidence
    -> typed registry and coherent control snapshot
    -> deterministic policy compilation and evaluation
    -> SecurityBarrier placement at every protected scan
    -> request-bound consumer authorization
    -> identity-partitioned cache validation
    -> durable redacted audit before disclosure
```

The per-requirement links and statuses are in the
[qualification matrix](semantic-access-qualification.md). PostgreSQL evidence
uses the repository's required PostgreSQL 18 conformance lane for definition,
assignment, claim, disable, tombstone/reincarnation, rollback, replay,
concurrent update, cross-instance, cache-denial, and audit-retention behavior.

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
- Standalone DataPolicy removal, production profile activation, and cutover are
  deliberately not qualified here; they remain FAI-649 work.

## Interpretation

`PASS` in the matrix means the requirement has a current production owner and
named executable evidence for the supported profile. `PARTIAL` is not an
allowlist entry: it records a narrower proven slice or an unsupported path that
still lacks full qualification. `FAIL` records the two cutover-owned clauses
that cannot pass while standalone DataPolicy remains available. VAL-11 remains
Partial and is not promoted by FAI-648.
