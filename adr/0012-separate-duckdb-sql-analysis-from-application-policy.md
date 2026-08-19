# ADR-0012: Separate DuckDB SQL analysis from application policy

Status: accepted

Decision date: 2026-08-19

Implementation: complete

Deciders: LeapView maintainers

Supersedes: none

Related: [ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md);
[DuckDB SQL analysis conformance](specifications/duckdb-sql-analysis-conformance.md)

## Context and problem statement

ADR-0010 made the pinned DuckDB parser the canonical boundary for authored
Model SQL. LeapView serializes DuckDB's parsed query representation through
`json_serialize_sql`, visits a closed set of nodes, derives Source and Model
lineage, and later binds the query against compiler-created governed relations.
That decision correctly avoids a parser whose interpretation could diverge
from the engine that executes the query.

The first implementation locates all of this behavior under
`internal/analytics/duckdb/queryjson`. It combines several concerns:

- opening and restricting an isolated DuckDB parser connection;
- decoding DuckDB's version-specific serialized query representation;
- validating generic AST shape, enum values, and source locations;
- traversing relations, expressions, functions, columns, and CTE scopes;
- decoding `EXPLAIN (FORMAT json)` output and rewriting relation references;
- deciding which schemas, functions, and query features LeapView permits in a
  governed Model.

Only the last concern is specific to LeapView's application policy. The others
are reusable DuckDB query-analysis capabilities. Keeping them interleaved makes
the version-coupled protocol harder to generate and test exhaustively, forces
generic parser changes through application packages, and obscures which code
establishes structural correctness versus product authorization.

LeapSQL and SQLGlot demonstrate useful parser, resolver, formatter, and DuckDB
test corpora. Neither is the engine that will execute LeapView Model SQL. Using
either as the compile-time authority would create a second interpretation of
DuckDB syntax at a security boundary. Conversely, generating an independent
grammar from documentation examples would reproduce only documented positive
examples, not DuckDB's complete lexical, precedence, transformation, and
binding behavior.

The question is where generic DuckDB query parsing and analysis should live,
which implementation is authoritative, and where LeapView-specific admission
rules should be enforced.

## Decision drivers

- Keep one authoritative interpretation of DuckDB SQL from parse through
  execution.
- Make structural parsing and application authorization distinct, testable
  boundaries.
- Reuse generic DuckDB query analysis outside Model compilation without
  importing application internals.
- Preserve useful parser diagnostics and exact source locations.
- Generate version-coupled structures and metadata wherever DuckDB provides a
  machine-readable source.
- Make DuckDB upgrades produce reviewable AST and metadata drift rather than
  silently changing accepted Model SQL.
- Keep raw DuckDB AST and plan formats out of persisted LeapView contracts.
- Avoid introducing a Python runtime, parser sidecar, or second binding and
  type system.
- Allow future editor tooling to use an error-recovering parser without making
  that parser an authorization authority.

## Considered options

- Retain the combined parser, visitor, policy, rewrite, and plan behavior under
  `internal/analytics/duckdb/queryjson`.
- Build and maintain an independent DuckDB grammar, parser, resolver, and type
  system in Go.
- Adopt SQLGlot as the canonical parser through a Python library or sidecar.
- Reuse LeapSQL's Go parser as the compile-time authority.
- Extract a new standalone repository and Go module before its API and
  conformance contract have stabilized inside LeapView.
- Create a reusable `pkg/duckdbsql` package backed by the pinned DuckDB parser,
  and keep LeapView admission policy in an internal application package.

## Decision outcome

LeapView will create `pkg/duckdbsql` as the reusable Go boundary for generic
DuckDB query parsing and analysis. The pinned DuckDB engine remains the sole
authority for the syntax and meaning of the parsed query. The package is not an
independent SQL parser and does not claim that documentation examples define a
complete grammar.

The initial public package contract covers DuckDB query/`SELECT` SQL because
DuckDB's `json_serialize_sql` interface exposes serialized `SELECT` statements.
The package must not imply authoritative support for DDL, DML, administration,
or extension-management statements until the pinned engine exposes an
equivalent parser representation that the package can consume. A non-query
statement produces a typed parser or unsupported-statement diagnostic rather
than being approximated by another parser.

### Generic package boundary

`pkg/duckdbsql` owns generic behavior that is meaningful without a LeapView
project:

- creation and lifecycle of a restricted, isolated DuckDB parser connection;
- query parsing through the pinned engine and preservation of its structured
  diagnostics;
- strict typed decoding of the serialized DuckDB query AST;
- exhaustive traversal of query nodes, table references, expressions, result
  modifiers, functions, columns, CTEs, and source spans;
- generic extraction of referenced relations, functions, and columns;
- location-aware, overlap-checked SQL reference rewriting;
- typed decoding of the relevant DuckDB JSON plan representation;
- generated inventories of DuckDB functions, keywords, and logical types; and
- conformance fixtures, differential tests, mutation tests, and fuzz targets
  for the generic boundary.

The package does not import `internal/` packages and does not contain LeapView
resource types, governed namespace names, Model lineage evidence, connector
policy, extension admission, or application-specific function allowlists. It
may expose stable LeapView-owned query, reference, span, diagnostic, and visitor
types. It must not expose raw serialized DuckDB JSON as its public API.

DuckDB's serialized AST remains an ephemeral input protocol. Typed decoding
structures may be version-coupled and generated, but callers consume stable
package abstractions. Parsed AST JSON is not stored in project resources,
deployment artifacts, events, lineage, or APIs.

### Generated DuckDB integration

Typed decoder and visitor inputs are generated from the serialization
descriptions maintained with the pinned DuckDB source, including query-node,
parsed-expression, table-reference, and result-modifier definitions. Function,
keyword, and type inventories are generated from the exact pinned DuckDB
distribution through `duckdb_functions()`, `duckdb_keywords()`, and
`duckdb_types()`. Closed enum inventories are generated from hash-locked DuckDB
headers. User-facing function documentation is generated separately from the
pinned `extension/core_functions` JSON descriptors that also feed DuckDB's
documentation build. CI verifies every snapshot against the exact source
commit as well as regenerating it hermetically from checked-in inputs.

The generator commands and their pinned source snapshots are private package
implementation details under `pkg/duckdbsql/internal`. Application tooling
invokes those package-owned commands but does not own or duplicate DuckDB SQL
metadata generation.

Generated metadata describes what DuckDB exposes; it does not decide what
LeapView authorizes. In particular, a function reported as scalar, aggregate,
internal, stable, or free of database side effects is not automatically safe,
deterministic, or appropriate for governed Model execution. Newly generated
functions remain unavailable to Model SQL until the application policy admits
them explicitly.

Documentation metadata may supply examples and descriptions when its license
and provenance are retained. It remains separate from the runtime overload
inventory and is not an authoritative grammar or admission source.

### Application policy boundary

LeapView-specific SQL admission moves to `internal/analytics/modelsql`, which
consumes `pkg/duckdbsql` and owns:

- the requirement for exactly one query statement;
- governed `source` and `model` relation namespaces;
- rejection of external catalogs and ungoverned relations;
- table-function, reader, attachment, secret, and external SQL restrictions;
- the reviewed function and query-feature profile for Model execution;
- rules for recursive CTEs, pivots, samples, and other optional query features;
- derivation and validation of Source and Model dependencies;
- normalized compiler evidence and comparison with runtime analysis; and
- integration with stub binding, output-contract validation, physical read
  planning, and governed source lowering.

The application package produces a small normalized analysis result. DuckDB
AST node types are not application domain types, and Model policy must not be
encoded by removing nodes from the generic decoder.

DuckDB continues to bind accepted queries against compiler-created stub
relations and remains responsible for name resolution, overload selection,
implicit casts, logical output types, `DESCRIBE`, and physical planning through
`EXPLAIN`. `pkg/duckdbsql` must not grow a competing binder or type system.

### Secondary parsers and future extraction

An error-recovering parser derived from LeapSQL, SQLGlot, or another project may
support formatting, highlighting, completion, and immediate editor diagnostics.
Its result is advisory. Compile, deployment, and execution paths always pass
the final SQL through the pinned DuckDB-backed package and LeapView policy.

`pkg/duckdbsql` will stabilize inside the LeapView repository first. Moving it
to a separately versioned module may be considered after its API, generator,
upgrade gate, and downstream use have demonstrated an independent lifecycle.
No duplicate in-repository and external implementations may coexist during a
future extraction.

## Consequences

Generic DuckDB analysis becomes independently reusable and testable. The
application policy becomes smaller and its security decisions become easier to
review because structural AST validation is no longer mixed with admission
rules. DuckDB upgrades produce generated diffs that identify new or changed
nodes, fields, enums, functions, keywords, and types.

The public package introduces an API that must be kept coherent. Its stable
types require more deliberate design than an internal helper, and exposing raw
DuckDB structures for convenience would make every engine upgrade an
application-wide migration. Generation tooling and pinned upstream source
inputs become build dependencies.

The generic decoder must represent syntactically valid query features that
LeapView currently rejects. This increases its structural test surface, but
keeps rejection in the correct application layer. The application policy must
remain fail-closed when the generic package reports a feature it does not yet
understand or admit.

Parser startup and JSON transport remain costs. Implementations may reuse a
bounded isolated parser service or connection, but must preserve connection
isolation, disabled external access and automatic extension resolution, pinned
extension supply, and configuration locking. Performance concerns do not
justify a second authoritative parser.

Test cases imported from DuckDB, SQLGlot, LeapSQL, or documentation require
source and license attribution. Their positive examples are supplementary to
differential tests against the pinned DuckDB engine and do not automatically
expand LeapView's accepted Model profile.

## Confirmation

- Architecture tests require `pkg/duckdbsql` to remain independent of every
  `internal/` and application package, and require LeapView SQL policy to live
  outside the generic package.
- Generated-file checks deterministically reproduce typed AST decoding and the
  function, keyword, and type inventories from the exact pinned DuckDB source
  and runtime distribution.
- Generic conformance tests cover every serialized query-node,
  table-reference, expression, and modifier family emitted by the pinned
  engine; unknown structural data fails closed without losing parser error
  diagnostics.
- Differential tests prove accepted generic fixtures are parsed by the pinned
  DuckDB engine, decoded without semantic reinterpretation, and preserve
  relation and source-location evidence. Mutation and fuzz tests prove malformed
  payloads cannot panic or bypass exhaustive traversal.
- Upgrade checks present AST and metadata drift as a reviewable diff and prevent
  new DuckDB features or functions from entering the Model profile implicitly.
- Application tests prove Model policy independently rejects multiple
  statements, ungoverned namespaces, external catalogs, readers, table
  functions, side-effecting or unapproved functions, and unsupported query
  features, including when nested in CTEs or expressions.
- Binding tests prove DuckDB, rather than `pkg/duckdbsql` or the application
  visitor, remains responsible for name resolution, function overloads,
  implicit casts, output types, and physical plan evidence.
- Repository tests prove no production compiler or runtime path imports the
  superseded `internal/analytics/duckdb/queryjson` package after migration.
