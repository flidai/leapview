# Semantic attribute registry

FAI-636 adds the PostgreSQL control-plane authority for typed semantic-access
attribute definitions. Each definition has a stable identity, a closed
logical type and shape, versioned ownership and documentation metadata, and an
active or disabled lifecycle. The registry does not store principal
assignments, trusted claim mappings, or semantic consumer policy resolution.

## Capability ownership

The access capability owns the revision-11 migration, domain contract, raw
PostgreSQL repository, validation, and transactional audit records. The global
PostgreSQL migration runner owns ordered revision application, checksum
evidence, and replay verification. Revisions 1–10 remain immutable; this
registry is a forward-only global migration.

The singleton is scoped by the existing one-control-database-per-instance
authority and its immutable `platform.instance_identity`; it is not process
global state. Shared-database multi-instance lookup is not an advertised
configuration, and this slice does not add a global mutable fallback.

The registry uses the reconciled PostgreSQL authority roles. The runtime role
can read and mutate definitions through the access repository, while readonly
and backup roles can read non-secret registry metadata. Deletes are denied.
Repository mutations run in one transaction with their durable audit event.
Direct runtime-role DML is not a supported application boundary; registry
reads recompute the digest and fail closed if out-of-band writes desynchronize
the definition projection from its recorded identity.

## Definition and registry identity

Each `access.semantic_attribute_definition` row contains:

- an immutable UUID and case-sensitive semantic attribute name;
- one logical type from `String`, `Boolean`, `Integer`, `Decimal`, `Date`, or
  `Timestamp`;
- a `scalar` or homogeneous `list` shape;
- the `leapview.semantic-access/v1` canonicalization profile;
- instance, principal, or group ownership metadata;
- display name, description, and optional credential-free HTTPS documentation
  URL;
- a monotonically increasing definition version; and
- database-owned creation, update, and disable timestamps.

The UUID, name, type, shape, profile, and creation timestamp cannot be
rewritten. Metadata and lifecycle changes advance the definition version
exactly once. Definitions are disabled and re-enabled rather than deleted.

`access.semantic_attribute_registry` is a singleton compatibility identity.
Every effective definition change advances its revision and replaces its
SHA-256 digest in the same transaction. Reads recompute the digest from the
ordered definition projection and fail closed when stored and computed
identities differ. Idempotent registration, metadata, and lifecycle replays do
not advance either revision.

## Repository lifecycle

The access repository supports registration, lookup by name or stable UUID,
bounded ordered search, metadata changes, and lifecycle transitions.
Registering an existing name with the same type and shape is a replay;
registering the name with a different type or shape returns a compatibility
conflict. A logical type change therefore requires a new attribute identity
instead of an in-place rewrite.

Before accepting an attribute value, the repository loads the active
definition and delegates canonicalization exactly to `internal/semanticvalue`.
Scalars produce one canonical value identity. Lists use the v1 bounded
homogeneous-set contract, including canonical sorting and deduplication.
Disabled definitions fail closed. Values themselves are not persisted by this
registry.

## Explicit boundary

This slice qualifies definition storage, lifecycle, canonical value validation,
and stable registry identity. It does not implement principal or group
assignments, claim ingestion, compiler/contextual reference resolution,
semantic filter evaluation, policy digest propagation, cache identity, or audit
value projection. Reference/affected-object indexing and dependency health also
remain deferred with those contextual layers. ATT-01 and VAL-11 therefore
remain partial in the semantic-access evidence ledger.
