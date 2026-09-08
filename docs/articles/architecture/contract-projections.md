# Canonical contract projections

The `internal/project/contractprojection` package owns the reserved
`leapview.contract/v1` projection boundary for Source, Model, and SemanticModel.
It is distinct from graph, artifact, release, deployment, and authorization
decision identities. It does not publish contracts or authorize activation.

## Authority boundary

Projection payloads are generated from
`api/data-resources/contract-projections.tsp`. They are explicit allowlists,
not embeddings of authoring or runtime objects. Resource inputs must pass the
existing generated authoring validation before a constructor can seal a
projection. Canonicalization accepts only the sealed projection types.
Exported read views cannot become sealed inputs through JSON decoding.

Contract identity includes the projection profile, resource envelope, stable
authored identity, version/compatibility input, and resource-specific contract
content. It excludes credentials, target configuration, runtime state, source
locations, and reviewed descriptive fields. Projection does not allocate
Project or Resource UIDs.

Model source references, SQL resource references, and SemanticModel dataset
references resolve through a `ReferenceContext` built from the existing validated
project graph. Resolution uses explicit IDs first and project-local names as a
fallback, verifies the expected resource kind, and fails on unavailable authority.
The context does not create a registry or allocate an identity. Relationship
targets retain their member suffix while resolving the resource portion.

Source and Model authoring metadata may carry the version and compatibility used
by the projection. When present, that authored contract metadata is authoritative:
the constructor fallback must match it, and a mismatch is rejected. Resources
without authored contract metadata may still use the explicit constructor
fallback. Field classification, critical-data-element markers, authoritative
definition links, and deprecation guidance are projected; authoritative links are
a sorted, deduplicated set after URL normalization. Tags and prose remain
reviewed descriptive exclusions. FAI-622 must bind these bytes to its
publication authority before downstream deployment/history use.

The reviewed exclusion manifest records why an authored field is omitted and
binds that decision to the generated field shape. Generation rejects
unclassified fields, overlapping exclusions, stale paths, and changed excluded
shapes. New fields require a projection or an explicit reviewed exclusion;
they do not silently disappear from identity.

## Determinism and integrity

The projection boundary normalizes typed semantic values, contractual
defaults, and declared collections before RFC 8785 serialization. Exact
semantic literals use tagged representations; unsupported approximate or
unsafe numeric representations fail instead of rounding into another identity.
General text is normalized to NFC, with invalid text and normalization
collisions rejected. SHA-256 identifies the exact canonical bytes.

Named filter literals are typed using their full physical-field reference in
declared dimension bindings, not the dimension's presentation name. Missing or
conflicting typed bindings fail closed; this package does not infer a physical
schema or duplicate compiler lowering. Unique-check fields are sets, while
entity/relationship field tuples and relationship paths retain their order.
Check severity, aggregate empty-result behavior, and declared time defaults
are materialized consistently with the existing runtime.

Model SQL is represented by an allowlisted projection of the existing parsed
SQL representation, not raw SQL text or DuckDB's raw parser JSON. Unsupported
SQL constructs fail closed. Publication decoding and digesting validate every
nested AST node against the same closed representation and require its exact
deterministic encoding. Implicit relation qualifiers resolve through query
scope to stable resource identities; explicit aliases and CTE names retain
their query-local meaning, and ambiguous qualifiers fail closed. Execution
SQL and existing execution digests are unchanged.

Projection identifiers use the generated contracts' ASCII character rules.
URL normalization preserves distinct IPv6 zone identifiers, including zone
names that begin with `25`; percent decoding is not applied a second time.

Tests cover canonical bytes and digests, input sealing, malformed inputs,
projection coverage, and an independent JavaScript serialization corpus.
The JavaScript implementation is fixture-only; production has one RFC 8785
implementation behind the projection package.

## Downstream ownership

FAI-620 supplies canonical inputs for future FAI-622 compatibility/security
classification and immutable publication evidence. This package does not
implement that classifier, widening approvals, publication storage, replay
transactions, or deployment-history integration.

FAI-662's sealed-input and coverage safeguards belong at this boundary. Its
database publication-integrity requirements remain with publication storage.
FAI-670 retains Project-qualified ResourceUID authority. FAI-645 remains a
downstream consumer; its cache, lifecycle, and audit implementation is not
part of this reconciliation.

This boundary is not a claim that ADR-0016 or ADR-0017 qualification is complete.
