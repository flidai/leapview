# ADR-0024: Use named lists for authored definitions

Status: accepted

Decision date: 2026-09-14

Implementation: complete

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0022](0022-adopt-dataset-local-semantic-authoring.md), named
collection syntax and member-name defaults only;
[ADR-0023](0023-unify-source-and-model-fields-and-checks.md), Source and Model
field and entity collection syntax only;
[ADR-0017](0017-adopt-a-looker-aligned-semantic-access-contract.md),
SemanticModel access-grant definition syntax only;
[ADR-0011](0011-adopt-a-canonical-dashboard-document.md), authored definition
collection syntax and fragment composition by identity only

Related: [ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md);
[ADR-0016](0016-adopt-standards-aligned-data-contracts-and-interchange.md)

## Context and problem statement

LeapView represents semantic datasets, metrics, dimensions, relationships,
physical fields, and dashboard visuals as keyed maps, while dashboard filters
and pages use lists with explicit identities. Authors must learn two ways of
declaring identifiable objects. A definition copied from a map also needs its
parent key to retain its identity.

Named lists are familiar in other semantic modeling tools, but their syntax
does not supply map uniqueness or identity-aware merging. Adopting them must
preserve namespace rules, deterministic compilation, fragment diagnostics,
contract history, and query behavior.

## Decision drivers

- Consistent, self-contained declarations for authors, editors, and generators.
- Explicit identity independent of declaration position.
- Strict validation and deterministic semantic compilation.
- Preservation of existing reference, access, and contract boundaries.
- One canonical authoring format without permanent legacy aliases.

## Considered options

- Retain the mixed format: compact maps offer direct lookup and convenient
  object patching, but declaration conventions remain inconsistent.
- Use lists for every collection: visually uniform, but burdens genuine
  dictionaries with unnecessary entry objects and identity fields.
- Use named lists for definitions, maps for dictionaries, and objects for
  configuration records.
- Accept both maps and lists permanently: eases migration but duplicates the
  public contract and complicates tooling, validation, and canonical export.

## Decision outcome

Use named lists for authored definitions, retaining dictionaries and ordinary
configuration objects. This is an authoring contract decision; runtime lookup
structures remain indexed maps where appropriate.

### Collection shapes and identity

| Collection | Canonical authored shape |
|---|---|
| Source and Model field declarations; Model entities | List with required `name` |
| Semantic datasets, local and shared dimensions and metrics, relationships, filters, and access-grant definitions | List with required `name` |
| Shared dimension bindings | List with required `dataset` |
| Dashboard visual definitions | List with required `id`, consistent with existing dashboard object identities |
| Existing identified lists such as filters, pages, and checks | Retain their existing identity vocabulary |
| Genuine dictionaries such as category-value-to-style assignments | Retain maps |
| Single records such as metadata, definition, time, and relationship endpoints | Retain objects |

Apply this distinction to other authored collections by their meaning; do not
mechanically replace every `Record` or introduce names for anonymous values.
Reference lists remain lists of references and acquire no artificial identity.
Resource envelopes and their existing metadata identity remain unchanged.

For example, the semantic authoring shape becomes:

```yaml
spec:
  datasets:
    - name: sales_orders
      model: sales_orders
      metrics:
        - name: revenue
          type: simple
          agg: sum
  dimensions:
    - name: state
      datatype: String
      bindings:
        - dataset: sales_orders
          field: sales_customers.state
          path: [orders_customers]
```

Existing identifier syntax, case behavior, namespace scopes, and reference
resolution remain authoritative. In particular, dataset nesting does not
introduce separate metric or dimension namespaces. Defaults formerly derived
from a map key derive from the explicit member `name`, with the same meaning.
Names remain technical identifiers; labels and display names remain separate.

Validate required identities and duplicates before lowering lists into maps.
Reject duplicate identities within a document and across composed fragments;
never silently overwrite or choose the first entry. Diagnostics must identify
the offending member and source location, and identify the conflicting origin
when available. JSON Schema `uniqueItems` compares complete values and is not
sufficient to enforce uniqueness of an identity property.

### Order, composition, and editing

Declaration order of independent semantic definitions has no effect on query
meaning, reference resolution, access decisions, or semantic fingerprints.
Normalize these collections deterministically when compiling and projecting
semantic contracts. Preserve order for operations where it is meaningful,
including query sorts, relationship paths, and dashboard page sequences.
Authored order may be preserved for editing without becoming execution order.
Source-byte and bundle digests may change when authored bytes change.

Dashboard fragment composition continues to combine definitions by identity
and reject conflicts. Ordered collections retain their existing concatenation
rules. Local definitions cannot override included definitions, and changing
the syntax does not introduce deep merging or implicit overrides.

Tools that edit definitions must resolve entries by identity against the
current document. Array positions are not durable member references. Generic
JSON Merge Patch replaces whole arrays; named lists do not gain identity-aware
patching automatically. This ADR does not introduce a new patch protocol or
claim improved Git merge behavior.

### Contract and migration boundary

Implement a coordinated pre-release conversion of generated authoring types
and schemas, validation, lowering, native exporters, fragments, fixtures,
examples, and authoring documentation. Use one canonical shape after cutover;
do not retain permanent map/list aliases. Conversion must preserve member
identities and defaults, and preserve comments and fragment boundaries where
possible. Explicitly specify and test omission, empty collections, and null
handling rather than letting Go map-to-slice changes decide these semantics.

Keep generated schemas as the structural authoring authority and perform
identity validation at the compiler boundary. Retain efficient indexed runtime
representations. Published contract projections, wire contracts, external
interchange formats, and historical evidence do not automatically adopt the
new authoring shape. Preserve semantic projections where meaning is unchanged;
any required profile change follows ADR-0016, including historical digest and
replay guarantees. Canonical native export emits the new list format.

## Research basis

The Flid reference library's Cube documentation and MetricFlow fixtures and
current official documentation were reviewed on 2026-09-14.
[Snowflake semantic view YAML](https://docs.snowflake.com/en/user-guide/views-semantic/semantic-view-yaml-spec)
and [Cube YAML](https://docs.cube.dev/docs/data-modeling/concepts/syntax) use
named definition lists. [dbt semantic models](https://docs.getdbt.com/docs/build/semantic-models)
also use named lists; their current model/column nesting differs from older
MetricFlow authoring. These establish precedent, not syntax compatibility.

[YAML](https://yaml.org/spec/1.2.1/) distinguishes ordered sequences from
unordered mappings. [JSON Schema array validation](https://json-schema.org/understanding-json-schema/reference/array)
defines whole-item uniqueness. [JSON Merge Patch](https://www.rfc-editor.org/info/rfc7396/)
replaces arrays rather than merging entries by identity.
[Kubernetes server-side apply](https://kubernetes.io/docs/reference/using-api/server-side-apply/)
provides precedent for explicitly identifying entries in list-shaped maps;
LeapView does not adopt Kubernetes patch machinery through this decision.

## Consequences

Authors learn a consistent declaration convention, and each definition carries
its own identity. The compiler retains efficient lookups and existing semantic
boundaries. Genuine dictionaries remain concise.

Lists add explicit identity fields and require application-level duplicate
validation. Schema-only editors cannot guarantee name uniqueness. Generic
patching becomes less convenient, and textual merge conflicts remain possible.
The conversion spans authoring tools and exporters, not just example YAML.
Preserving source formatting requires source-aware tooling rather than relying
on runtime maps to reconstruct the original document.

## Confirmation

Implementation conforms when:

- Generated schemas enforce required identity fields and existing identifier
  restrictions, accept canonical lists, and reject removed map forms.
- Compiler tests reject duplicate identities within and across scopes governed
  by existing uniqueness rules, including fragments with source diagnostics.
- Reordering independent definitions preserves compiled semantics, access
  behavior, and semantic projections; ordered-operation fixtures retain order.
- Converted examples preserve field defaults, reference resolution, query
  results, and published semantic contracts.
- Fragment tests preserve conflict rejection and ordered concatenation;
  editing tests, where applicable, target identity after entry insertion or
  reordering rather than stale positions.
- Native import/export round trips preserve meaning; migration fixtures cover
  optional, empty, and null collections and fragment boundaries.
- Historical publication verification and replay remain valid under their
  original profiles. Authoring-only changes do not silently rewrite evidence.
- Public documentation describes the new syntax only when implementation ships;
  acceptance of this ADR alone does not establish runtime support.
