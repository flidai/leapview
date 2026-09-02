# Semantic attribute registry

The PostgreSQL semantic attribute registry is the control-plane authority for
typed semantic-access attribute definitions. It gives each definition a stable
identity, a closed logical type and shape, versioned ownership and documentation
metadata, and an explicit active or disabled lifecycle. It does not store
principal assignments or trusted claim mappings.

## Capability ownership

The access capability owns the revision-2 migration, sqlc queries, domain
contract, repository, validation, and transactional audit records. The generic
PostgreSQL migration runner owns only advisory locking, ordered revision
application, checksum evidence, and replay verification. Revision 1 remains
byte-for-byte immutable; the registry is an access-owned forward migration.

The registry uses the reconciled PostgreSQL authority roles. The runtime role
can read and mutate definitions through the access repository, readonly and
backup roles can project non-secret registry metadata, and deletes are denied.
Each repository mutation uses a caller-owned transaction and appends immutable
audit evidence before that transaction can commit.

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

The UUID, name, type, shape, profile, and creation timestamp cannot be rewritten.
Metadata and lifecycle changes advance the definition version exactly once.
Definitions are disabled and re-enabled rather than deleted.

`access.semantic_attribute_registry` is a singleton compatibility identity.
Every effective definition change advances its revision and replaces its
SHA-256 digest in the same caller-owned transaction. Reads recompute the digest
from the ordered definition projection and fail closed when stored and computed
identities differ. Idempotent replays do not advance either revision.

## Repository lifecycle

The access repository supports registration, lookup by name or stable UUID,
ordered listing/search, metadata changes, and lifecycle transitions. Registering
an existing name with the same type and shape is a replay; registering the name
with a different type or shape returns a compatibility conflict. A logical type
change therefore requires a new attribute identity instead of an in-place
rewrite.

Before a consumer accepts an attribute value, the repository loads the active
definition and delegates canonicalization to `internal/semanticvalue`. Scalars
produce one canonical value identity. Lists use the v1 bounded homogeneous-set
contract, including canonical sorting and deduplication. Disabled definitions
fail closed. Values themselves are not persisted by this registry.

## Semantic access boundary

This registry qualifies the definition-storage part of ADR-0017 and provides a
stable identity for later assignment and consumer integrations. It deliberately
does not implement principal or group assignments, claim ingestion, semantic
filter evaluation, policy digest propagation, cache identity, or audit value
projection. Those paths remain separately qualified work, so VAL-11 remains
partial.

Migration tests cover ordered revision application and replay identity.
Repository tests cover stable registration, lookup/search, canonical value
validation, metadata versioning, disablement, immutable type enforcement,
registry digests, and transactional audit evidence against the PostgreSQL 18
harness.
