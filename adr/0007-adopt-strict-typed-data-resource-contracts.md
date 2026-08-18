# ADR-0007: Adopt strict typed data-resource contracts

Status: accepted

Decision date: 2026-08-18

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Related: [ADR-0005](0005-use-project-wide-resource-graph.md);
[ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md);
[ADR-0008](0008-adopt-a-canonical-dashboard-document.md)

## Context and problem statement

ADR-0005 established Connection, Source, Model, SemanticModel, Pipeline, and
Dashboard as project-wide resources with stable identity. ADR-0006 subsequently
made Model identity, fields, entities, and grain part of a strict semantic
contract. The resource graph and common envelope are sound, but the authored
Connection, Source, and transformation portions of Model do not yet meet the
same standard.

The public Connection schema is one closed object containing every connector's
optional fields. It exposes endpoint, database, username, SSL, and credential
fields even though project compilation correctly rejects those fields as
target-owned. Connector-specific configuration and source-reader defaults are
represented by untyped option maps, so generated schemas cannot explain which
fields a connector accepts or validate option values before compilation.

DuckDB is already LeapView's data-access and analytical execution substrate.
Its readers and approved extensions provide file, object-store, database, and
table-format connectivity. That runtime capability is not itself a stable
public connector contract: exposing DuckDB extension names, support tiers, or
option maps directly would couple `leapview.dev/v1` to an implementation API.
Conversely, adding ADBC between LeapView and DuckDB sources would introduce a
second connector abstraction in the wrong direction; DuckDB's ADBC driver is a
client interface for applications communicating with DuckDB, not how DuckDB
reaches external sources.

Source location is an implicit union: the compiler infers a path source or
database object source from which optional field is populated. Path format may
be inferred from a filename, reader options are untyped, and declared source
field types use an unconstrained vocabulary distinct from the portable Model
datatype vocabulary. The contract also does not express the freshness
expectations assigned to Source by ADR-0005.

Model identity and grain are strong, but Model input and transformation are
another implicit union. A passthrough Model uses singular `source`; a SQL Model
uses plural `sources` plus `transform.sql`; and the compiler parses the SQL and
then requires its inferred source references to duplicate the authored list.
The contract has no closed vocabulary for executable data-quality assertions.
Calling authored SQL read-only merely because its first statement begins with
`SELECT` or `WITH` is also insufficient: DuckDB table and scalar functions can
open files, reach networks, invoke connectors, or execute SQL against attached
systems from within a syntactically read-only query.

LeapView is not publicly released. These shapes can be replaced directly. The
question is which portable, strict authoring boundary should describe data
access, raw inputs, and model transformations without leaking target secrets or
retaining multiple equivalent representations.

## Decision drivers

- Make generated schemas describe exactly what project compilation accepts.
- Generate public structural shapes, language DTOs, and schemas from one
  TypeSpec declaration rather than maintaining parallel Go, TypeScript, CUE,
  JSON Schema, and documentation definitions.
- Keep endpoints, credentials, secret providers, and environment bindings owned
  by the target instance.
- Represent connector, source-location, and transformation variants as closed
  tagged unions rather than combinations of optional fields.
- Validate connector and reader options structurally before preparing runtime
  SQL or opening an external connection.
- Keep one LeapView connector registry as a stable, reviewed compatibility
  profile over approved DuckDB readers and extensions.
- Preserve stable project resource identity and the Model entities, grain, and
  logical datatypes adopted by ADR-0006.
- Keep source and model lineage exact without asking authors to repeat facts the
  compiler can derive deterministically.
- Add portable freshness and data-quality assertions without turning Models
  into a general orchestration or adapter-specific configuration language.
- Make external source setup trusted and bounded while ensuring authored Model
  SQL can obtain data only through compiler-provided governed relations.
- Use the pre-release window to remove old forms instead of accumulating
  compatibility readers, aliases, or migrations.

## Considered options

- Keep the existing optional-field structures and rely on Go validation for
  connector-specific invariants.
- Generate documentation from the connector registry while retaining untyped
  authoring maps and implicit variants.
- Continue hand-maintaining equivalent authored-resource structures in Go,
  TypeScript, and CUE.
- Make TypeSpec the structural contract, generate language and JSON Schema
  projections through APIGen, and use CUE only for contextual project rules.
- Introduce ADBC as a generic source-connectivity layer between LeapView and
  DuckDB.
- Adopt dbt profiles, sources, models, and tests as LeapView's native authored
  contract.
- Introduce native closed tagged unions for Connection, Source location, Model
  definition, source freshness, and model checks while retaining the existing
  project graph and compiler ownership.

## Decision outcome

LeapView will retain its project-wide resource envelope and replace the
authored Connection, Source, and Model-definition contracts with native closed
tagged unions. Project authoring and target binding will have distinct types and
schemas. One definition will be authoritative at each boundary; runtime types
may contain resolved fields that are impossible to author.

This decision and ADR-0008 finalize `leapview.dev/v1` before its first public
release. Repository examples, generated schemas, APIs, CLI export, builder
output, and documentation move directly to the final shape. Removed draft forms
cease to exist: the compiler will not retain readers, translators, aliases,
migrations, or deprecation warnings for them. ADR-0006's semantic behavior
carries forward unchanged; this decision finalizes the adjacent data-resource
contracts without reopening semantic execution choices.

### Public resource structure is generated from TypeSpec

TypeSpec is the authoritative structural declaration for public Connection,
Source, and Model documents. It owns public names, required and optional fields,
closed tagged unions, enum vocabularies, scalar formats, and descriptions. The
existing APIGen contract IR is extended to generate the corresponding Go
authoring DTOs, TypeScript DTOs where needed, JSON Schema 2020-12 artifacts, and
reference documentation. Generated structs use the same camelCase JSON and YAML
names.

The project CUE validation layer consumes the generated structural JSON Schema
and adds only contextual constraints such as cross-resource identity,
connector/location compatibility, and target-independent project invariants.
It must not restate the structural fields as a separately maintained public
contract. Runtime and compiler types may add resolved fields that cannot be
authored, but conversion from generated authoring DTOs is explicit and tested.

Connector variants carry LeapView-only profile metadata in the TypeSpec
contract for activation mode, location capabilities, approved extensions,
secret type, support status, and adapter key. A custom APIGen projection emits
the Go connector registry and capability documentation. Bounded adapter
implementations remain handwritten and register by generated key; generation
fails unless every declared adapter key is implemented and every implementation
has one declaration. No experimental CUE-to-Go generator is part of the public
contract pipeline.

### Connections are portable logical declarations

A project Connection selects one connector through `spec.type`. Each connector
is a separate closed variant containing only portable logical configuration.
Connector variants must not share one optional-field bag.

An authored Connection may declare connector selection, portable access policy,
and typed source defaults. It may not declare a physical production endpoint,
host, port, database credential, username, password, token, environment-secret
name, Infisical reference, or resolved authentication material. Public access,
when a connector supports it, is an explicit access policy rather than a
`credentials.provider: none` credential object. All non-public resolution is
owned by the target Connection binding.

Portable means portable between LeapView targets that provide a compatible
connector capability. It does not promise execution on a different analytical
engine. The project carries logical connector intent; each target supplies its
endpoint, database or object scope, network configuration, transport policy,
authentication mode, and credentials. Moving the same project from developer
to staging or production changes the target binding, not the Connection
resource.

For example, a managed-file declaration remains concise while its defaults are
typed by reader format:

```yaml
apiVersion: leapview.dev/v1
kind: Connection
metadata:
  id: connection:olist
  name: olist
spec:
  type: managed
  defaults:
    csv:
      header: true
```

The generated connector registry is the runtime source of truth for supported
variants, target-binding requirements, approved runtime readers and extensions,
location capabilities, option schemas, and LeapView support status. It is a
stable LeapView compatibility profile over DuckDB capabilities, not a mirror of
DuckDB's extension registry. DuckDB extension support tiers and newly added
extension options do not automatically become LeapView support or public YAML.
Generated CUE, JSON Schema, compiler validation, deployment validation, runtime
preparation, and documentation derive from the TypeSpec profile declarations or
are checked against their generated registry. An arbitrary `options` escape
hatch is not part of the canonical contract.

ADBC is not the source-connectivity abstraction. LeapView may use Arrow or an
ADBC client at another boundary where an application communicates with DuckDB,
but source activation uses the approved DuckDB reader or extension selected by
the connector registry.

### Target bindings lower to a restricted DuckDB runtime

All external data access is implemented through a pinned, integrity-verified,
LeapView-approved DuckDB reader or extension. Supporting a connector requires
one typed authored profile, one typed target-binding profile, a bounded runtime
adapter, and an explicit LeapView support status. Raw DuckDB reader arguments,
extension settings, and connection strings are runtime implementation details
and cannot cross into project resources.

Target bindings remain authoritative for endpoints and credentials. During
trusted runtime preparation, LeapView resolves a versioned credential snapshot
from the target-owned provider and lowers it to the minimum DuckDB configuration
needed by the connector. Where DuckDB Secrets Manager is supported, credentials
become compiler-named, scoped, temporary in-memory secrets. Persistent DuckDB
secrets and DuckDB-managed credential persistence are prohibited. Secret values
must not enter authored resources, compiled plans, fingerprints, logs,
diagnostics, lineage, or audit payloads.

External database attachments and equivalent connector sessions are read-only
whenever the underlying connector can enforce that mode. Source activation
does not grant Model transformations permission to modify an upstream system.
A future write-capable external resource requires a distinct typed contract and
architecture decision.

Runtime preparation loads only the exact approved extension artifacts required
by the compiled project. Automatic extension installation and loading are
disabled, unsigned or otherwise unapproved extension artifacts are prohibited,
and configuration is locked before authored work executes. A reviewed non-core
extension may be distributed only through LeapView's pinned and
integrity-verified supply path; DuckDB's broad community-extension setting is
not a substitute for a LeapView allowlist.

Trusted setup may create temporary secrets, read-only attachments, and governed
source relations because some valid Sources require filesystem or network
access. A blanket `enable_external_access = false` setting therefore cannot be
the primary sandbox on the same session. The security boundary is the compiled
relation namespace plus complete SQL validation: setup code owns external
capabilities, while authored Model SQL never invokes them directly.

### Sources use an explicit location union

Source retains a stable project identity and one Connection reference. Its
`location.type` selects a closed connector-supported location variant. Initial
portable variants distinguish path-backed data from database relations; a
connector may add a reviewed typed variant when neither is sufficient.

```yaml
apiVersion: leapview.dev/v1
kind: Source
metadata:
  id: source:olist.orders
  name: olist.orders
spec:
  connection: olist
  location:
    type: path
    path: olist_orders_dataset.csv
    format: csv
    options:
      header: true
```

Path-backed sources require an explicit format. Format inference may exist in
an interactive creation workflow, but the exported and committed document is
explicit. Format options are a closed type owned by LeapView, not a direct
unversioned pass-through of DuckDB reader arguments.

Option precedence is deterministic: an explicit Source location option wins
over the corresponding authored Connection default, which wins over the
versioned LeapView default. Target bindings may supply target-owned operational
configuration, but cannot change authored data interpretation. The compiler
records the effective non-secret options so discovery, deployment validation,
and runtime preparation cannot resolve the same Source differently.

Database relations use structured identity rather than an overloaded object
string where the connector can distinguish catalog, schema, and relation:

```yaml
location:
  type: relation
  catalog: analytics
  schema: commerce
  name: orders
```

Source schema declarations use the shared portable logical datatype vocabulary
and may declare nullability. Connector-native physical types remain discovery
evidence and compiled metadata; they are not a second authoring vocabulary.
Schema enforcement has an explicit mode. In inferred mode, the authored Source
does not claim a field contract and discovery records the observed schema. In
compatible mode, every declared field must exist with a compatible logical type
and nullability while additional physical fields may exist. In strict mode, the
declared and observed field sets must match and every type and nullability claim
must be compatible; field order is not semantic. The resolved schema and the
evidence used to establish it are retained separately.

Source may also declare typed freshness expectations, including the field or
revision timestamp that establishes freshness and structured warning or error
durations. Freshness evaluation produces deployment and refresh evidence; it
does not silently change query results.

### Models use one explicit definition variant

Model keeps the fields, entities, and exact grain adopted by ADR-0006. Its data
production mechanism moves under one required `definition` tagged union. The
initial variants are a direct source projection and a SQL transformation:

```yaml
spec:
  definition:
    type: sql
    sql: |
      SELECT order_id, customer_id
      FROM source."olist.orders"
  entities:
    order: {type: primary, fields: [order_id]}
  grain: {entity: order}
  fields:
    order_id: {datatype: String}
    customer_id: {datatype: String}
```

The singular `source`, plural `sources`, and optional `transform.sql` forms are
removed. SQL may obtain data only from compiler-provided relations in the
governed `source` and `model` namespaces. The compiler derives exact Source and
Model dependencies from one completely parsed and validated SQL representation
and stores that lineage in the compiled graph; authors do not repeat a
dependency list that can disagree with the query.

One leading `SELECT` or `WITH` token is not proof of safety. Validation rejects
multiple statements and every construct outside the closed read-only query
profile, including direct connector, filesystem, object-store, network, secret,
attachment, extension, and arbitrary table-function access. `ATTACH`, `DETACH`,
`COPY`, `INSTALL`, `LOAD`, `CREATE SECRET`, DDL, DML, `PRAGMA`, `CALL`, and
functions that execute SQL against an external system are prohibited even when
nested inside a query. Relation and function validation operates on the parsed
representation rather than token scanning or string rewriting.

LeapView promotes its existing DuckDB SQL-to-JSON analysis path to the one
canonical Model SQL parser. Every SQL definition is passed to the pinned
DuckDB engine's `json_serialize_sql` function, including definitions for which
an earlier scan would infer no dependencies. The result must contain exactly
one serialized `SELECT` statement. A fail-closed visitor accepts only the
versioned AST node, relation, and function vocabulary required by the Model
query profile; an unknown node or field that affects execution is an error.
The same visit derives Source and Model dependencies and rejects direct readers,
table functions, external catalogs, and non-governed relations.

SQL analysis runs in an isolated in-memory DuckDB connection containing no
target bindings, credentials, external attachments, or Source data. LeapView
explicitly loads only its pinned JSON extension, disables automatic extension
installation and loading and external access, and locks configuration before
parsing authored SQL. Only after the AST passes the closed validation profile
may LeapView bind or obtain `EXPLAIN (FORMAT json)` evidence against compiler-
created stub relations in the governed `source` and `model` schemas.

DuckDB's serialized AST is ephemeral version-coupled parser input. It is not
stored in project resources, compiled artifacts, lineage events, or APIs.
LeapView normalizes accepted nodes into its own analysis result and plan.
DuckDB upgrades require AST snapshot and adversarial conformance tests before
the pinned version changes.

The compiler lowers validated governed relation references to runtime relations
only after authorization and dependency resolution. Authored SQL cannot name an
attached catalog, invoke the underlying reader used to construct a Source, or
smuggle a path, URI, connection string, or secret reference through a function
argument. Any SQL feature not explicitly in the closed Model-query profile is
rejected before candidate preparation.

A Model's declared `fields` are its exact output contract. Direct and SQL
definitions must produce every declared field exactly once, must not expose
undeclared fields, and must produce compatible logical types. Column order is
not semantic. This validation occurs before a candidate can be activated, so
downstream semantic models never observe a best-effort shape.

Model may declare checks from a closed initial vocabulary such as non-null,
unique entity, accepted values, relationship integrity, and bounded row count.
Entity and grain declarations imply the corresponding required identity checks
during deployment. Check results are evidence and activation gates according to
their declared severity. Arbitrary SQL tests are excluded from the initial
contract.

Physical materialization remains compiler and runtime policy. It is not an
authored adapter-specific knob. A future need for selectable materialization
strategies requires its own typed contract and decision.

### Descriptive metadata has one owner

Resource-level display name, description, owner, domain, tags, documentation,
and provenance remain under common `metadata`. Connection, Source, and Model
specifications do not repeat a resource-level description. Descriptions of
fields, entities, checks, and other spec members remain beside those members.

## Consequences

Generated schemas become useful authoring tools because they expose only valid
connector and location fields. Target secrets and endpoints cannot leak through
the portable project schema. Source behavior becomes reviewable without knowing
which optional fields happen to be mutually exclusive, and Model lineage can no
longer disagree with a manually duplicated source list.

The connector registry, schema generator, compiler, target-binding system,
discovery, deployment validation, CLI export, examples, and documentation must
move together. Supporting a new connector or source format requires defining
its typed authored and target-bound contracts rather than adding arbitrary map
keys. This costs more initially but makes compatibility and security review
explicit.

DuckDB is the selected `leapview.dev/v1` execution substrate, but it remains
behind LeapView's typed project and target-binding contracts. LeapView's
connector registry, rather than DuckDB's extension registry or ADBC, defines
which connectors and capabilities the product supports. Temporary DuckDB
secrets, read-only attachments, and reader functions are implementation reuse;
they do not become public resource concepts and can be replaced without
changing authored projects.

Complete Model SQL validation becomes a security boundary, not merely a lineage
feature. The compiler must use one parser-backed representation for statement
shape, governed relation and function allowlists, dependency derivation, and
runtime lowering. A first-token scanner, keyword blocklist, or textual rewrite
cannot satisfy this contract.

The existing DuckDB JSON-AST and JSON-plan infrastructure removes the need for
a second SQL parser or a language-runtime sidecar. LeapView nevertheless owns a
small closed visitor and normalized analysis result. That visitor is coupled to
the pinned DuckDB version intentionally, so an engine upgrade includes contract
snapshots and security-corpus review rather than silently accepting new syntax.

TypeSpec and APIGen become build dependencies for authored-resource DTOs in
addition to APIs, signals, and Visualization IR. This removes structural drift
but makes generator correctness part of the resource contract. Generated files
remain reviewable snapshots, and contextual CUE and compiler checks remain
handwritten where they express behavior rather than shape.

Compiled dependency and validation evidence remains LeapView-owned. It may be
emitted using the OpenLineage interoperability format established by ADR-0005,
but OpenLineage is not an authored dependency list or an execution contract.

Freshness and model checks add execution and evidence costs. Implementations
must bound their work, distinguish warnings from activation failures, and avoid
running duplicate checks when entity or schema validation already proves the
same fact.

The refactor replaces the current draft forms directly. Existing Connection,
Source, and Model fixtures are rewritten in the same change; no compatibility
aliases, dual writes, implicit converters, migrations, or deprecated fields
will remain in production.

## Confirmation

- Generated CUE and JSON Schemas expose a closed variant for every registered
  authored connector and reject all target-owned endpoint and credential fields.
- Generation tests prove one TypeSpec declaration deterministically produces
  matching Go and TypeScript DTOs, sealed JSON Schema, imported CUE structure,
  connector registry metadata, and reference documentation. Repository checks
  reject independently maintained structural shadow types.
- Registry consistency tests prove that schema, compiler, deployment, runtime
  preparation, and documentation recognize the same connector, location,
  format, option set, approved extension, and LeapView support status. DuckDB
  extension tiers and newly available options do not change that status.
- Runtime security tests prove that only pinned, integrity-verified approved
  extensions can load, automatic installation and loading remain disabled, and
  configuration is locked before authored SQL executes.
- Target-binding tests prove credentials lower only to scoped temporary secrets,
  persistent DuckDB secret storage remains disabled, external attachments are
  read-only where supported, and credential values never enter plans,
  fingerprints, diagnostics, lineage, logs, or audit payloads. Source activation
  exposes no external write capability.
- Source tests reject zero or multiple location variants, missing path formats,
  options for the wrong format, paths outside target scope, and relation fields
  unsupported by the selected connector. Precedence tests prove explicit Source
  options override authored Connection defaults and versioned LeapView defaults
  identically in discovery, deployment validation, and runtime preparation.
- Schema discovery tests prove logical datatype and nullability compatibility
  while retaining connector-native physical types only as evidence. They prove
  inferred, compatible, and strict modes differ exactly in whether a declaration
  is required and whether additional observed fields are accepted.
- Model tests prove that direct and SQL definitions are exclusive, SQL lineage
  is complete and deterministic, the resulting columns exactly match declared
  fields and compatible logical types, undeclared namespaces are rejected, and
  no authored dependency list exists to diverge from compiled lineage.
  Adversarial DuckDB JSON-AST tests cover every allowed node family and reject
  multiple statements, unknown semantic nodes, direct readers and connectors,
  paths and URIs, external SQL execution, attachments, secrets, extensions,
  DDL, DML, `COPY`, `PRAGMA`, and `CALL`, including when concealed in nested
  expressions or CTEs. Definitions with no governed dependencies still pass
  through the same parser and validator.
- SQL-analysis isolation tests prove the analysis connection has no target
  bindings or Source data, loads only the pinned JSON extension, disables
  automatic extension installation and loading and external access, and locks
  configuration before parsing. DuckDB upgrade tests snapshot the accepted AST
  corpus and fail on unreviewed semantic changes.
- Freshness and check tests cover warning, blocking, unavailable, empty, and
  bounded-execution outcomes and preserve the active serving generation after a
  failed candidate.
- Architecture tests prove that target endpoint, DuckDB implementation, and
  credential types cannot be imported by project schema or authored-resource
  packages, and that source activation does not depend on ADBC as a connector
  abstraction.
- Repository fixtures, generated contracts, APIs, CLI output, and documentation
  contain no removed Connection, Source, or Model-definition forms before
  implementation is marked complete.
