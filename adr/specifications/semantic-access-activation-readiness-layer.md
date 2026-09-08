# FAI-649 deadline activation-readiness layer

Status: **IMPLEMENTED / PARTIAL scope; production semantic activation deferred**.

This layer is stacked on `ganesh/fai-648-acceptance-boundary` at
`3897fd1442cbe40c9c124a880c50e825bc1815aa`. The deadline scope explicitly replaces
a complete DataPolicy migration with activation safety, approval integrity,
restricted creation and honest evidence. The [bounded profile](semantic-access-acceptance-boundary.md)
is not expanded. No provider, principal or neutral verification context is
fabricated, and no new classifier, approval model, digest authority or migration
is introduced.

## Implemented versus deferred

| Boundary | Readiness-layer guarantee | Remaining implementation |
| --- | --- | --- |
| Existing deployment approval | Validate retained plan identity, canonical evidence, scope and freshness before trusting approval exemptions; use the existing durable approval service for required approvals. | FAI-622 contract publication/version and policy evidence are not yet retained as exact semantic activation inputs in the delivery plan. Existing authorization fingerprints are not substitutes. This layer does not claim that missing connection is implemented. |
| Protected candidate admission | Reject protected canonical candidate planning while exact semantic publication/approval evidence and neutral structural verification are unavailable. Ordinary approval configuration cannot enable that path. | Retain exact immutable contract bindings, current registry/control and lifecycle evidence, prove admitted consumer/cache compatibility, and implement compiler/planner-owned non-consumer verification before enabling production. |
| DataPolicy transition | Standalone authoring is deprecated. New creation must not be represented as supported by the public API; compatibility readers and existing artifacts remain. | Full authoring/compiler/runtime/API/UI removal and explicit migration of existing source remain deferred. No automatic conversion of arbitrary expressions or masks to semantic grants/filters. |
| Rollback | Existing lifecycle/cache fences and immutable historical evidence remain unchanged. Protected planning remains unavailable, including attempts to use an older or approval-exempt plan as a substitute for readiness. | A deployed protected-generation rollback/restart proof remains absent. Historical decode/replay is not old-binary policy enforcement compatibility. |

The exact semantic flow `classification → publication evidence → approval →
activation` is **not yet end-to-end qualified**. In particular, publication
currently requires an ACTIVE identity and an exact lifecycle sequence; a new
protected identity needs an explicitly designed ordering between publication,
approval and identity activation. Do not make an identity live merely to
manufacture pre-approval evidence. The deadline layer retains rejection instead
of introducing that protocol opportunistically.

## Evidence ownership

- FAI-622/662 remain the classifier and immutable publication authorities.
  `ContractPolicyApprovalInput` explains a validated decision; it is not an
  approval and cannot be supplied as a substitute for one.
- Deployment retains the existing approval service, retained plan digests,
  credentials, expiry and publication scope checks.
- FAI-637 owns registry and durable assignment resolution. Only that bounded
  source profile is a future admission input; claim-source envelope recognition
  does not admit an external provider.
- FAI-641 owns structural verification design and planner barriers. Existing
  neutral protected-plan rejection tests remain valid.
- FAI-642/645 own consumer and cache/lifecycle enforcement. Production principal
  authority and guarded cache configuration are not enabled by this layer.

## DataPolicy migration and rollback guidance

Prefer typed `SemanticModel.accessGrants`, `requiredAccessGrants` and
`accessFilters` for the future contract. That authoring direction does not mean
the protected contract is currently deployable. Existing DataPolicy-bearing
artifacts retain their legacy enforcement; do not remove their policies before
an equivalent, qualified replacement is available. Compatibility source loading
is not a supported way to introduce new policy features.

Inventory retained policies and classify representability explicitly. Policies
that rely on arbitrary expressions, masking or other semantics outside the
bounded contract require manual redesign and review. Do not silently drop,
translate, weaken or reclassify them. No historical artifact, publication row,
checksum, digest or migration is rewritten here.

Follow the [policy-evidence reader-before-writer rules](semantic-access-policy-evidence.md).
An old binary that decodes a newer record but ignores its policy evidence is
not a compatible rollback target. Existing historical replay remains evidence,
not permission to activate or reuse stale cache state. Production rollback
qualification is deferred along with production semantic activation.

## ADR review result

The normative matrix remains **55 PASS / 45 PARTIAL / 2 FAIL**. STR-08 and
ENF-06 still fail because standalone DataPolicy and its authored language remain.
Deprecation or a creation restriction is not complete removal. FAI-649 remains
partial; FAI-648 and FAI-632 are not completed by this safety layer.

The ADRs can be reviewed with this explicit evidence boundary, but they cannot
truthfully claim complete production semantic activation or the final ADR-0016
conformance closure. Hosted validation must cite this layer's exact commit; the
green parent run is not substituted for current-layer qualification.

## Verification

Implementation and regression evidence are deliberately narrower than production
activation acceptance:

| Guarantee | Implementation | Executable evidence |
| --- | --- | --- |
| Retained plan integrity before approval exemptions | `internal/app/sealed_approval.go`, production callback in `internal/app/composition.go` | `TestValidateSealedPublicationPlanBinding`: canonical plan, tampering, scope/candidate/generation substitution, base revision and expiry boundary. Existing `TestDurableApprovalVerifier*` and `TestApproval*` retain required-approval, revocation and mismatch coverage. |
| Protected candidates cannot substitute registry presence or an approval flag for readiness | `internal/app/runtimefactory/semantic_activation_readiness.go` and canonical candidate-plan constructor | `TestCandidatePlanRejectsProtectedSemanticActivationWithoutReadiness`, `TestCandidatePlanReadinessDoesNotHonorApprovalOrRegistryOverrides`; unprotected planning has a positive regression test. These are rejection tests, not proof of current-authority admission. |
| Public standalone creation rejected without opening storage | Access HTTP handler, APIGen adapter and module dispatcher | `TestCreateDataPolicyRejectsNewCreationWithoutStorageMutation`, `TestDispatchAPIGenCreateDataPolicyRejectsNewCreation`. Source compatibility loading remains and is not represented as globally blocked. |
| Existing authority ownership and ordering retained | Production composition and candidate boundary | `TestSemanticActivationReadinessPreservesAuthorities`, full architecture suite. |

Local validation on 2026-09-08 passed focused semantic access, cache,
approval/publication, candidate planning and DataPolicy tests; the complete
architecture suite; `task generated:check docs:check`; and `git diff --check`.
Local Docker access is unavailable, so local tests are not cited as live
PostgreSQL evidence. Canonical hosted CI must execute the required PostgreSQL
gate before this layer is considered CI-qualified. Hosted run/commit evidence
is recorded on FAI-649; none of these checks upgrades the normative matrix or
qualifies production semantic activation.
