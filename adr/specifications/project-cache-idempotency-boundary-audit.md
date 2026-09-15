# ADR-0018 cache and idempotency boundary audit

This inventory is the maintained API-06 evidence for runtime result caches and
durable command replay. It records the complete authoritative identity used at
each boundary; a request digest is a separate conflict check and is never a
substitute for scope.

## Cache consumers

| Consumer | Authoritative scope | Result identity | Validation |
| --- | --- | --- | --- |
| Project query-result scope | Target, Project, environment, production/candidate kind, and candidate identity | Canonical dependency digest, effective policy fingerprint, and canonical query digest | `TestProjectBoundaryCacheKeysDoNotCollide`, `TestProjectBoundaryCacheRejectsMissingScope`, `TestQueryResultCacheKeyUsesStableCompositeIdentity` |
| Generation byte scope | Project result partition plus serving-state generation; candidate scopes also include candidate identity | Immutable artifact, semantic, source-data, relation, and snapshot evidence supplied to the runtime | `TestProjectCacheIdentitiesSeparateStableResultsFromGenerationBytes`, `TestProjectRuntimeCacheIdentitySeparatesCandidateFromActiveState` |
| Protected semantic result | Target/instance, Project, environment, generation, model, principal, optional actor, registry/control revisions and digests, effective attributes, policy, decision, direct/trusted evidence, and publication policy | The detached semantic-access identity is part of the dependency digest and is revalidated before every cache boundary | `TestSemanticAccessIdentityFieldPartitioning`, `TestQueryResultCacheKeyIncludesAuthorizationProjection`, semantic cache lifecycle tests |
| Process-local materializer cache | A unique, non-exported pool identity; it cannot be reopened by another runtime | The same canonical query/dependency/policy key contract | Query-cache key and runtime ownership tests |

No cache implementation changes are required by this completion: the existing
typed partitions already reject missing Project, environment, target, or
candidate scope, generation-specific byte caches do not alias across serving
states, and protected semantic dependencies rotate with authorization state.

## Durable idempotency consumers

| Consumer | Authoritative scope | Replay behavior | Status |
| --- | --- | --- | --- |
| Shared API and browser protocol | Authenticated principal, credential identity, operation, method/path, server target, environment, server Project, active generation, generated command resource locator, and caller key | Exact-scope, exact-request replay only; current authorization is checked again. Resolver failures fail before a durable claim. | Completed by the versioned `flid.http.idempotency-scope.v2` scope |
| Native refresh admission/cancellation | Project, environment, serving generation, operation type, actor, and key | Exact request digest and terminal run evidence are required; this bypass owns the transaction and does not rely on the shared HTTP record. | Generation added to the bounded native operation scope |
| Candidate-source planning bypass | Project, retained-source owner, artifact digest, candidate key, and caller key | Exact request digest and immutable plan evidence are required. Environment and serving generation are not inputs because capture occurs before delivery binding. | Existing complete scope |
| Release commands | Server Project plus the shared protocol scope; release persistence verifies the exact Project/environment/generation and artifact request digest | Same release is returned only for the exact persisted identity. | Existing inner guard plus completed outer scope |
| Managed-data upload and multipart commands | Server Project, connection, upload/multipart resource, and caller key plus the shared protocol scope | Deterministic resource IDs and exact manifest/part comparisons reject drift. Serving generation is not an authoring input. | Existing inner guard plus completed outer scope |
| Delivery plan/build/publication/rollback commands | Singleton target claim and explicit Project/environment/plan/candidate/generation request evidence plus the shared protocol's active generation and concrete resource locator | Transactional native operations return only exact terminal evidence; changed evidence fails closed. | Existing inner guard plus completed outer scope |
| Access, agent, connection-binding, dashboard-authoring, and publication commands | Concrete aggregate/resource identity plus the shared server Project/environment/generation scope where Project-owned | Transactional aggregate guards reject changed requests; the shared layer prevents cross-scope replay and reauthorizes every replay. | Existing inner guards plus completed outer scope |

The platform Project-claim bootstrap is the sole pre-Project exception. Its
scope contains the immutable instance target and configured environment; the
issuer-owned Project UID remains in the exact request digest and is accepted
only by the authorized, unclaimed bootstrap transaction.

## API-06 guarantees

- Reusing one principal/key pair in another Project, environment, active
  generation, candidate/generation locator, or instance target addresses a
  different durable record.
- An identical request in an identical scope replays the original result only
  after current authorization succeeds.
- A missing or non-canonical authoritative scope fails closed before domain
  work and before any idempotency record is claimed.
- Request-digest comparison remains defense in depth for changed inputs within
  one authoritative scope.
