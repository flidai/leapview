# ADR-0017 semantic-access activation and cutover

Status: **active for the qualified supported profile**.

FAI-649 closes the deployment boundary for ADR-0017 without adding another
authorization language. The immutable delivery plan selects the exact
SemanticModel publication, compatibility/security classification, widening
approval (when required), compiled-policy digest, and registry/control
revision and digest. The plan also names the qualified SecurityBarrier,
consumer, cache, and audit profiles. Delivery approval therefore covers the
same evidence that the final activation fence re-derives.

## Activation flow

1. Candidate planning inspects the retained compiled source bundle. Public
   projects carry no semantic-activation evidence. Every protected
   SemanticModel must match exactly one immutable FAI-622 publication.
2. Planning validates current publication policy and approval evidence,
   resolves a coherent FAI-637 registry/control snapshot, compiles the FAI-639
   policy, and seals the resulting evidence into the delivery-plan digest.
3. Immediately before the activation CAS, the final fence reloads the exact
   generation, plan, serving artifact, publication history, and registry and
   control authorities. Any identity, revision, digest, lifecycle, approval,
   compiler, or enforcement-profile mismatch rejects activation.
4. The fence compiles the generation-bound policy and durably appends a
   redacted audit event for each protected model. Audit failure rejects the
   cutover before the active pointer changes.
5. Existing FAI-641 barriers, FAI-642 consumer admission, and FAI-645
   request-time identity revalidation remain the runtime enforcement path.

Activation is replay-safe: the same immutable job and plan re-run the same
fence. The PostgreSQL target CAS serializes concurrent attempts, while the
fence never replaces or weakens that authority.

## DataPolicy migration

Standalone `DataPolicy` is no longer a public TypeSpec/API authoring surface,
and project source discovery rejects it. New candidate activation additionally
rejects any retained serving artifact that contains standalone policies.

- A representable row restriction moves to a SemanticModel `accessFilter`
  whose `userAttribute` is defined and assigned by the access control plane.
- A reusable allow condition moves to `accessGrants`, referenced through
  `requiredAccessGrants` on the governed dataset or member.
- Column masks, arbitrary SQL or JSON expressions, templates, and other
  general policy expressions are not part of the v1 semantic-access profile.
  They require manual redesign; activation does not silently discard them.

The legacy manifest/runtime decoder remains read-only for historical serving
evidence. An explicit rollback may reactivate a retained historical generation
with its original DataPolicy restrictions intact. Ordinary publication cannot
use this exception, malformed historical evidence still fails closed, and the
rollback flag is bound into approval workflow evidence so job replay cannot
change its meaning.

## Rollback and recovery

Rollback selects a retained generation and its immutable delivery plan. The
activation fence matches the candidate source against publication history
rather than a mutable latest pointer, revalidates current registry/control and
approval evidence, recompiles the policy, and records a new activation audit
before the CAS. Stale cache entries remain unusable because FAI-645 keys and
revalidates publication, policy, registry, control, and lifecycle identities.

If the process stops before the CAS, no active pointer changed. If it stops
after commit but before runtime reconciliation, the durable activation job is
retried and the native coordinator replays its committed outcome before
reconciliation. Historical audit and publication records are immutable.

## Boundary

This cutover covers only the providers, consumers, plan shapes, cache paths,
and audit path named by the [supported profile](semantic-access-supported-profile.md).
Unsupported paths remain fail closed. VAL-11 remains **PARTIAL**; FAI-649 does
not broaden canonicalizer qualification or add providers or consumers.
