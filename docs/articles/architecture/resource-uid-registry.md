# Instance-local resource identity

FAI-670 adds registry identity beneath the existing issuer-owned Project claim.
An authored `metadata.id` is portable. A ResourceUID is an opaque, random UUID
allocated by one instance for that authored ID within its claimed Project.
Names, paths, environments, dbt identifiers, and artifact or serving hashes are
not registry keys. The authored kind is immutable after allocation.

## Admission and activation

The verified source bundle supplies a complete resource inventory, including
the empty inventory. Admission retains this inventory with its exact bundle,
graph, Project, target, environment, and generation evidence. It allocates no
ResourceUIDs and does not modify portable artifacts.

The existing protected delivery activation transaction consumes the retained
inventory. Registry allocation, reuse, removal, and generation bindings must
commit atomically with activation. A failed activation must leave no allocated
identities. Runtime callers cannot directly write registry rows or invoke a
standalone allocator. Scoped reads require both instance and Project identity;
possession of a ResourceUID is not authorization.

The application compiler/admission capability remains trusted to produce
canonical contract bytes. Its Go API accepts only a sealed inventory and uses
FAI-620's full decoders and projectors; PostgreSQL independently checks scope,
graph completeness, tuple identity, and byte digests. The database does not
reimplement the compiler or SQL-AST validator. Runtime database credentials
are not an untrusted client API and must never be exposed to callers.

## Contract evidence is not publication authority

The registry distinguishes three evidence states:

- `canonical`: an authored versioned contract is projected through FAI-620;
  exact `leapview.contract/v1` bytes and their digest are retained.
- `unversioned`: a contract-bearing resource has no authoritative version;
  the registry does not invent a version, compatibility promise, or digest.
- `not_contract_bearing`: Connection, Pipeline, and Dashboard have no
  `leapview.contract/v1` projection.

Current Source and Model authoring can supply contract metadata. The current
SemanticModel authoring schema has no contract-version field. ResourceUID
allocation does not add one or manufacture a constructor fallback. Missing or
invalid authored evidence for contract-bearing resources fails inventory
construction rather than being silently treated as unversioned.

FAI-622 remains responsible for version/publication authority, compatibility
classification, and publication approval. Registry evidence does not authorize
publication or semantic access, and a serving asset hash is never a contract
digest.

## Lifecycle boundary

Removal retains a tombstone; normal activation cannot silently reuse that
authored identity. Explicit, privileged restore evidence binds one resource to
one exact generation and records its actor and request identity. A previously
bound generation can reuse its historical ResourceUIDs during rollback, within
the same instance and Project. Registry restore authority is not authority to
reauthorize control-plane grants or publications; their consumer integration
must be qualified separately before claiming complete RID-06 conformance.

The additive migration does not fabricate registry evidence for historical
generations. A generation without retained inventory is not eligible for a
registry-backed activation merely because legacy serving assets exist.
Governed consumer integration and semantic resolver closure remain separately
owned by FAI-671 and FAI-675; this foundation does not claim their completion.
