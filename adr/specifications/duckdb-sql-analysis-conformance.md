# DuckDB SQL analysis conformance specification

Status: accepted
Last updated: 2026-08-19
Owners: LeapView maintainers
Governing decisions: ADR-0010 and ADR-0012

## Purpose

This specification defines the evidence required to implement and maintain the
DuckDB SQL analysis boundary selected by ADR-0012. It separates generic analysis
of DuckDB query syntax from LeapView-specific admission, security, dependency,
and binding policy.

The requirements are intentionally testable. They are suitable as acceptance
criteria for the implementation project and as an upgrade gate whenever the
pinned DuckDB version changes.

## Boundary requirements

- **DBS-01:** `pkg/duckdbsql` must not import any package below `internal/` or any
  LeapView application package.
- **DBS-02:** Public generic types must not mention LeapView sources, models,
  projects, connectors, dependency evidence, or admission policy.
- **DBS-03:** `internal/analytics/modelsql` must own LeapView SQL admission and the
  conversion of accepted relations into normalized application dependencies.
- **DBS-04:** Production authorization decisions must not depend on LeapSQL,
  SQLGlot, a documentation-derived grammar, or any other secondary parser.
- **DBS-05:** DuckDB's raw serialized AST and plan JSON must not be exposed as a
  public package contract or persisted as a durable application artifact.
- **DBS-06:** The initial public parsing contract is DuckDB query/`SELECT` SQL.
  Unsupported statement classes must return a typed diagnostic rather than be
  accepted by an incomplete decoder.
- **DBS-07:** A future extraction to a separate Go module must move the parser,
  generated model, tests, and DuckDB pin atomically. LeapView must never operate
  with two authoritative DuckDB SQL analyzers.

## Parser and diagnostic requirements

- **PAR-01:** Parsing must use the exact DuckDB engine version pinned by the
  owning Go module.
- **PAR-02:** Parser connections must begin with external access, autoloading,
  autoinstallation, and other mutable extension behavior disabled. The bundled
  JSON extension may be loaded only through an explicit initialization step,
  after which restrictive settings must be reasserted and locked.
- **PAR-03:** Parser connections must have no application data bindings,
  credentials, attached databases, source registrations, or user-defined macros.
- **PAR-04:** Syntax failures must preserve DuckDB's error type, message, subtype,
  and byte position when supplied. Diagnostic fields must be decoded separately
  from successful-AST shape validation so new diagnostic metadata cannot be
  mistaken for AST drift.
- **PAR-05:** Successful decoding must preserve statement order, identifier
  spelling, aliases, and byte locations needed for precise diagnostics and safe
  source rewriting.
- **PAR-06:** An unknown node or value in an otherwise successful serialized AST
  must fail closed with a typed compatibility error.
- **PAR-07:** Malformed, truncated, excessively deep, or excessively large JSON
  must return bounded errors without panics or partial dependency evidence.
- **PAR-08:** Connection pooling is permitted only when every connection satisfies
  the same isolation contract and tests prove that parser state cannot leak
  between requests.

## Typed AST and traversal requirements

- **AST-01:** The generated and handwritten model together must cover every query
  node, table reference, expression, result modifier, and supporting value emitted
  by the pinned DuckDB serialization format for the supported statement class.
- **AST-02:** Node and enum families must use closed sum types appropriate to the
  pinned DuckDB version. Unknown variants must not silently decode into generic
  maps.
- **AST-03:** The canonical walker must visit every child-bearing field. Adding a
  new child-bearing node or field must fail compilation, generation checks, or a
  conformance test until traversal is updated.
- **AST-04:** Relation extraction must distinguish catalogs, schemas, relation
  names, aliases, CTE references, subqueries, table functions, and source spans.
- **AST-05:** Function extraction must retain catalog, schema, name, arguments,
  named arguments, filters, order modifiers, and window clauses where present. It
  must not assign LeapView safety classifications.
- **AST-06:** CTE analysis must model declaration order, nesting, shadowing,
  recursion, and illegal forward references without confusing CTE references with
  physical relations.
- **AST-07:** Source rewrites must reject missing, duplicated, overlapping,
  out-of-range, or non-boundary-aligned spans. Accepted rewrites must be
  deterministic and preserve all bytes outside the requested spans.
- **AST-08:** Explain-plan decoding may expose stable typed fields required by
  callers, but it must not substitute plan inspection for syntax-tree analysis.

## Generation requirements

- **GEN-01:** AST decoder generation must derive its node and enum inventory from
  the serialization descriptions shipped with the pinned DuckDB source.
- **GEN-02:** Function, keyword, and type inventories must be generated from the
  pinned runtime through `duckdb_functions()`, `duckdb_keywords()`, and
  `duckdb_types()` respectively.
- **GEN-03:** Every generated artifact must record the DuckDB version and an
  immutable source identity. Generation must be deterministic for the same pin.
- **GEN-04:** The normal generated-artifact check must fail when regeneration
  changes tracked files. Generated files must not be manually edited.
- **GEN-05:** Function metadata must preserve enough information to distinguish
  function kind, schema, parameters, return shape, side-effect declaration, and
  stability when DuckDB exposes those fields.
- **GEN-06:** Generated metadata must not automatically add a function or feature
  to LeapView's admission profile.
- **GEN-07:** Documentation scraping may supply examples and descriptions when
  useful, with source and license attribution, but must not define the grammar or
  security policy.

## Application policy requirements

- **APP-01:** Model SQL must contain exactly one supported query statement after
  parsing.
- **APP-02:** Physical relations must resolve only through explicit LeapView
  source or model namespaces. Catalog and schema handling must be intentional and
  covered by policy tests.
- **APP-03:** Table functions, file and network readers, external SQL bridges,
  secret access, attachments, and extension-management paths must be rejected
  unless a separately reviewed application capability explicitly admits them.
- **APP-04:** Functions must be admitted through a reviewed LeapView profile.
  DuckDB metadata such as `side_effects = false` is advisory evidence, not a
  sufficient authorization rule.
- **APP-05:** Query features such as recursion, sampling, pivoting, windows,
  ordering, limits, and subqueries must have explicit admitted or rejected states.
- **APP-06:** Policy evaluation must be exhaustive over the generic typed AST and
  fail closed when the pinned analyzer exposes a new construct.
- **APP-07:** Downstream compiler and runtime code must consume only normalized
  dependency and policy evidence, never raw serialized AST JSON.
- **APP-08:** Runtime execution must reparse the stored SQL with the same analyzer
  contract and compare normalized evidence before executing rewritten SQL.
- **APP-09:** DuckDB binding against a synthetic schema that contains only
  authorized relations remains authoritative for name resolution, function
  resolution, typing, and output description.

## Conformance corpora

The test suite must combine independent sources rather than treating any one
corpus as complete:

- **COR-01:** Queries and edge cases from DuckDB's engine and serialization tests.
- **COR-02:** Runnable query examples from the DuckDB documentation, pinned to a
  recorded documentation revision.
- **COR-03:** SQLGlot DuckDB parser and normalization fixtures relevant to syntax
  LeapView supports. Transpilation-only expectations are excluded unless the
  generic package intentionally implements that behavior.
- **COR-04:** LeapSQL DuckDB fixtures that exercise useful syntax, traversal, and
  diagnostics.
- **COR-05:** LeapView security fixtures covering bypass attempts, relation
  shadowing, table functions, readers, external access, comments, quoting, and
  multi-statement input.
- **COR-06:** Generated mutation cases for every node, enum, optional child,
  collection, and diagnostic payload shape in the pinned serialization model.
- **COR-07:** Fuzz and metamorphic cases proving no panics, deterministic decoding,
  traversal completeness, source-span safety, and stable normalization.

Every fixture must label separate expectations for generic parsing and LeapView
admission. A syntactically valid DuckDB query may be accepted by `pkg/duckdbsql`
while being deliberately rejected by `internal/analytics/modelsql`.

## DuckDB upgrade gate

A DuckDB pin change is not complete until all of the following occur:

1. Regenerate the serialized-AST model and runtime metadata inventories.
2. Review every added, removed, or shape-changed node, enum, function, keyword,
   type, and diagnostic field.
3. Update the canonical walker and prove exhaustive child traversal.
4. Make an explicit application-policy decision for each newly representable
   construct and relevant built-in capability.
5. Run the full conformance, mutation, fuzz, rewrite, binder, and security suites.
6. Record the reviewed DuckDB version and source identity in generated artifacts
   and release evidence.
7. Block the upgrade if any compatibility difference remains unclassified.

## Initial evidence map

| Concern | Required evidence |
|---|---|
| Package boundary | Dependency test proving `pkg/duckdbsql` has no application or `internal` imports |
| Parser isolation | Connection-setting and cross-request state-leak tests |
| Diagnostics | Golden syntax-error payloads including subtype and byte position |
| Generated AST | Clean regeneration plus complete node and enum inventory |
| Traversal | Mutation coverage for every child-bearing field |
| Metadata | Clean runtime inventory regeneration for functions, keywords, and types |
| Rewriting | Property tests for span validation and byte preservation |
| Corpora | Pinned provenance manifest and per-fixture expectation labels |
| Application policy | Allow, deny, and bypass suites for every supported construct |
| Binding authority | Synthetic-schema bind and describe tests using the pinned DuckDB engine |
| Upgrade gate | Reviewed generated diff and passing compatibility suite |
