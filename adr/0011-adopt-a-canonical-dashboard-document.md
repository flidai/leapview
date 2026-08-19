# ADR-0011: Adopt a canonical dashboard document

Status: accepted

Decision date: 2026-08-18

Implementation: complete

Deciders: LeapView maintainers

Supersedes: none

Related: [ADR-0001](0001-semantic-model-first.md);
[ADR-0002](0002-use-maplibre-for-geographic-rendering.md);
[ADR-0005](0005-use-project-wide-resource-graph.md);
[ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md);
[ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md)

## Context and problem statement

ADR-0001 made dashboards governed presentation documents over one semantic
model. ADR-0005 established one canonical dashboard lifecycle shared by
project-authored files, the interactive builder, agents, compiler, and runtime.
Those boundaries remain correct. The authored document, however, has grown
through several generations of chart, table, filter, interaction, layout, and
accessibility work without a corresponding contract consolidation.

The current document mixes camelCase and snake_case, accepts both sequences and
mappings for the same query collections, and retains compatibility shorthands.
Dashboard filter definitions use `predicates` for the expression shapes a user
is allowed to author, even though those values are not executable semantic
predicates. Stateful filter bindings commonly repeat the filter's ID and point
back to that same definition, while page slicers introduce a third name for the
same user-facing control. Aggregate visuals can address physical dataset fields
instead of ADR-0006 semantic dimensions, and materially different query shapes
are inferred from whichever fields happen to be present or from the visual type
that contains them.

Some permissiveness is result-affecting rather than cosmetic. Sort direction is
effectively an open string. `sort.expr` is accepted as authoring input but is
discarded when the query is compiled. Several presentation enums admit any
string after enumerating supported values. This weakens generated schemas and
can make an accepted document behave differently from what it appears to say.

The document also assumes one large file. Named visuals and pages are valuable
local identities, but substantial dashboards become difficult to review when
every query, presentation definition, interaction, page, component, and layout
coordinate must live in one YAML document. The visual showcase is already
several thousand lines.

LeapView is not publicly released and does not need dashboard compatibility
syntax. The question is how to preserve the typed visualization and lifecycle
architecture while making the committed dashboard document strict, readable,
composable, and identical to builder and agent output.

## Decision drivers

- Preserve one canonical Dashboard document for files, builder drafts, agent
  commands, immutable revisions, compiler input, and export.
- Generate the Dashboard's structural contract and language projections from
  one TypeSpec declaration rather than maintaining equivalent Go, TypeScript,
  CUE, JSON Schema, API, and documentation shapes.
- Keep dashboards governed: no SQL, joins, aggregate expressions, credentials,
  grants, or executable AI instructions.
- Make every accepted authoring field observable in compiled behavior or reject
  it.
- Use one naming convention and one canonical collection shape throughout the
  public YAML contract.
- Express each interactive filter once without exposing compiler capability and
  state-binding layers as separate authoring concepts.
- Make aggregate dashboards semantic-member-first and give every materially
  different query grammar an explicit closed type.
- Retain stable local identities for visuals, filters, pages, and page components
  so interactions and revisions remain deterministic.
- Allow large dashboards to be reviewed in bounded files without turning local
  fragments into independently authorized or deployed project resources.
- Remove compatibility shorthands and permissive enum escape hatches before the
  first public release.

## Considered options

- Keep the existing contract and improve documentation and examples only.
- Make builder and agent documents distinct from project-authored YAML and
  translate between them.
- Adopt a third-party dashboard document or renderer-native ECharts options as
  the authored contract.
- Continue hand-maintaining parallel Dashboard structures for project YAML,
  builder and agent DTOs, APIs, browser signals, and schema validation.
- Make TypeSpec the structural Dashboard contract, generate its projections
  through APIGen, and retain CUE and compiler code only for contextual rules and
  behavioral compilation.
- Consolidate LeapView's existing renderer-independent document into one strict,
  composable shape shared by every authoring origin.

## Decision outcome

LeapView will retain a native renderer-independent Dashboard document and
replace its authored shape with one strict canonical contract. Builder, agent,
file loader, export, compiler, and revision storage will use the same semantic
document rather than lossy adapters between public variants.

ADR-0010 and this decision jointly finalize `leapview.dev/v1` before its first
public release. Current draft dashboard forms and compatibility shorthands are
removed outright. There is no old-version reader, translator, alias, migration,
or deprecation period. The semantic query vocabulary and typed execution
behavior adopted by ADR-0006 remain authoritative.

### Dashboard structure is generated from TypeSpec

TypeSpec is the authoritative structural declaration for the canonical
Dashboard document. It owns public camelCase names, required and optional
fields, closed tagged unions, enum vocabularies, scalar formats, collection
shapes, and descriptions. The existing APIGen contract IR generates Go
authoring and API DTOs, TypeScript builder, agent, and browser DTOs, sealed JSON
Schema 2020-12 artifacts, and reference documentation from that declaration.
Generated Go fields use identical camelCase JSON and YAML tags.

The project CUE validation layer consumes the generated structural JSON Schema
and adds only contextual constraints that require project knowledge. The Go
compiler owns behavioral rules such as semantic-member resolution, query and
visual compatibility, result-name uniqueness, filter-target compatibility, and
fragment expansion. Neither layer independently restates public structural
fields. Builder commands, agent tools, stored revisions, API payloads, and
browser signals may project subsets for their operation, but every projection
is generated from or explicitly mapped to the canonical TypeSpec types and is
covered by lossless round-trip tests.

The Dashboard TypeSpec package remains distinct from the existing
Visualization IR TypeSpec package. Dashboard authoring compiles into Visual IR;
it does not embed or inherit renderer-facing fields. The two generated
contracts share scalar and result-field primitives where their meanings are
identical, while preserving the architecture boundary between authored intent
and renderer input. No experimental CUE-to-Go generator is part of the contract
pipeline.

### Public names use camelCase

All public resource YAML uses camelCase below the common envelope, matching
`apiVersion`, `displayName`, `semanticModel`, `defaultTimeDimension`, and the
generated JSON/API vocabulary. Existing names such as `filter_bindings`,
`filter_application`, `max_selected_values`, `reader_editable`, `row_height`,
`col_span`, `display_units`, and `conditional_formatting` move to camelCase.

Compiler and runtime structs may use idiomatic Go names, but generated CUE,
JSON Schema, YAML, JSON, CLI export, documentation, builder commands, and agent
contracts must agree on one public spelling.

### Interactive filters are one ordered semantic declaration

`filters` is one ordered sequence of interactive filter declarations. Each
entry has a stable `id` and references an ADR-0006 semantic dimension by
`dimension`; aggregate-dashboard filters cannot address a physical dataset
field. The entry also owns its label, closed control type, supported operators,
option source, selection cardinality, optional real default, required state,
reader editability, optional target scope, and optional friendly URL parameter
name. Compiler-internal capability, state, and query-application objects may
remain separate, but they are not separate public authoring collections.

The Dashboard public terms `predicates`, `bindings`, and `filterControls` are
removed. A control type and its semantic dimension datatype determine the
allowed typed expression families; the compiler rejects an unsupported
combination. Absence of `default` means unfiltered. A real default remains a
closed typed value, and `required: true` prevents the dashboard from running
without a value but is not an authorization mechanism.

```yaml
filters:
  - id: purchaseDate
    label: Purchase date
    dimension: purchaseDate
    control:
      type: dateRange
    default:
      type: relativePeriod
      period: P30D
    urlParameter: period

  - id: state
    label: State
    dimension: state
    control:
      type: multiSelect
      maxSelectedValues: 50
      options:
        type: distinct
        dataset: orders
        dependsOn: [purchaseDate]
        limit: 50
```

`options.type: distinct` requires one semantic `dataset` used only to enumerate
option values. The filter itself remains bound to its semantic dimension and
applies to visuals independently of this option source. The dimension must
resolve safely from the option dataset. Option queries enforce authorization,
row policies, and masking normally; exclude the filter's own current value;
and apply only the selected values of filters named explicitly by `dependsOn`.
Each dependency must exist, must not be the filter itself, and must resolve
safely from the option dataset. Visual `targets` do not change option-query
meaning.

Distinct options use canonical typed-value ascending order, omit null unless
`includeNull: true`, and apply `limit` only after authorization, dependency
filters, distinctness, and ordering. Equal typed values have one canonical
option identity independent of formatting or renderer locale. A missing
`dependsOn` means no other interactive filter state narrows the option domain.

Sequence order is filter-bar order. On each page, a filter with no corresponding
page component appears in that page's filter bar. Exactly one page component
with `type: filter` may reference the filter ID to relocate that control onto
the page canvas; the component supplies placement only and does not introduce a
binding or a second state identity. Multiple canvas references to one filter on
the same page are invalid. Placements on different pages share the one filter
state. There is no authored `control.location`.

With no `targets`, a filter applies to every query-backed visual and compilation
fails if any such visual is incompatible. An explicit `targets` sequence may
only narrow that scope, and every listed target must be compatible. For an
aggregate query, compatibility requires a valid semantic-dimension binding for
every participating metric root. For a records query, the dimension must
resolve safely from the query's root dataset. Dashboard target declarations
cannot remap a filter to a physical field; a required remapping belongs in the
semantic model's dimension bindings and safe relationship paths.

The compiler and preview surface the resolved target set and explain every
incompatibility. A visible filter must never be accepted while a nominally
targeted visual silently ignores it.

Filter values and expressions remain typed tagged unions. Their tag is `type`,
consistent with metrics, visual definitions, calculations, queries, controls,
and page components. `kind` is reserved for the top-level project resource
envelope.

URL state is a compatibility surface independent of the Dashboard resource
version. Authors may select a friendly `urlParameter`, but never an encoding or
codec version. Shared links carry an independently versioned LeapView state
protocol, and changing that protocol does not require changing `apiVersion`.
Every authored URL parameter name must satisfy the public identifier grammar,
must be unique within the Dashboard, and must not collide with parameters
reserved by the LeapView state protocol.

### Queries are semantic-member-first tagged unions

Every visual query has an explicit `type`; visual presentation never implicitly
selects query execution semantics. The initial public union contains
`aggregate`, `records`, `pivot`, `histogram`, and `distribution`. Spatial tiling
is a runtime strategy for a compatible governed query, not an authored query
kind. A visual type may constrain the query types and result shapes it can
present, but it cannot change the meaning of an otherwise accepted query.

These tags identify governed query semantics rather than renderer chart types.
`pivot` owns semantic row and column axes, grouping, totals, and windowed pivot
behavior. `histogram` owns bin domain, bucket boundaries, null handling, and any
declared approximation. `distribution` owns quantiles, whiskers, outliers, and
statistical completeness. If a future presentation needs none of those distinct
query behaviors, it must use an existing query type rather than add a tag named
after a renderer visual.

Aggregate queries reference ADR-0006 semantic dimensions and metrics. Temporal
grain belongs on the semantic dimension reference, so the planner resolves the
correct metric-origin-relative binding. Dataset-qualified physical fields are
rejected in aggregate dimensions. Records queries name one semantic dataset and
project physical fields safely from that root. Pivot axes use semantic
dimensions and metrics. Histogram and distribution queries use their own closed
statistical operands and parameters rather than visual-type inference.

Dimensions, metrics, record fields, and pivot axes use ordered sequences. An
unaliased metric reference may be a string. A reference that needs grain or a
different output name uses one closed object with `dimension`, `metric`, or
`field` as appropriate and an optional `alias`. Mapping collections and
null-valued metric entries are removed.

```yaml
query:
  type: aggregate
  dimensions:
    - dimension: purchaseDate
      grain: month
      alias: purchaseMonth
  metrics: [revenue]
  sort:
    - field: purchaseMonth
      direction: asc
```

```yaml
query:
  type: records
  dataset: orders
  fields:
    - field: order_id
      alias: orderId
    - field: purchase_date
      alias: purchaseDate
```

Every query compiles to a result frame with unique, stable field names. An
explicit `alias` is the result field name. Without an alias, a semantic
dimension uses its dimension name, a metric uses its metric name, and a record
field uses its canonical field name. Result names must satisfy the public field
identifier grammar. If two selections would derive the same name, the query is
invalid until the author supplies distinct aliases; the compiler and renderer
never invent suffixes or reorder fields to resolve a collision.

Sorts, presentation bindings, calculations, interaction mappings, Visual IR
encodings, accessibility summaries, and exports address these result names. A
reference to a source semantic member or physical field is not accepted where a
compiled result name is required.

Sort accepts exactly a compiled result field and `asc` or `desc`. `sort.expr`
is removed. Dashboards cannot introduce inline metrics, arbitrary expressions,
SQL snippets, renderer expressions, or untyped sort behavior. Visual
calculations remain the existing closed template vocabulary and address only
compiled result-frame fields.

Every enum is closed. Constructs such as `"asc" | "desc" | string` and
format enums with an unrestricted string branch are prohibited. Extensibility
requires a versioned extension point or a reviewed new enum member, not silent
acceptance followed by renderer-dependent behavior.

### Presentation uses one authoritative representation

Each presentation concern has one representation. Compatibility fields such as
`show_labels` are removed; the typed label policy is authoritative. Visual type
selects a closed presentation contract and constrains compatible typed query
result shapes. Fields that do not apply to that visual or query type fail
validation instead of being ignored.

Named visuals remain separate from page placement. This preserves stable query
identity, interaction targets, reuse across pages, and layout-only edits that do
not rewrite a visual definition. Page components retain stable IDs and explicit
placement.

Dashboard-level layout defaults provide grid columns, row height, gaps, and
padding inherited by pages. Authored canvas width and height are removed: width
is responsive and height derives from the final occupied row. A page authors
only documented overrides. `column`, `row`, `columnSpan`, and `rowSpan` are the
one canonical placement representation. Responsive behavior below the minimum
useful width is deterministic renderer behavior; v1 does not expose authored
breakpoints. Automatic layout may be offered as an authoring tool that exports
explicit coordinates, not as an ambiguous runtime algorithm.

### Renderer libraries remain behind the visualization IR

The existing renderer-independent visualization architecture remains
authoritative. Dashboard authoring compiles to LeapView's typed Visual IR.
Current built-in adapters use ECharts for non-geographic analytical charts,
TanStack for tabular surfaces, MapLibre for geographic visuals as governed by
ADR-0002, and HTML for KPIs. This decision freezes the Visual IR and adapter
boundary, not a renderer vendor except where another ADR does so. Dashboard YAML
cannot contain renderer options, callbacks, transforms, data URLs, or
renderer-specific expressions.

Renderers own commodity mechanics such as marks, axes, scales, legends,
tooltips, hit testing, animation, and responsive drawing. They do not execute
queries, authorize data, resolve semantic members, aggregate metrics, apply
governed filters, or assign interaction meaning. Adding Vega, Vega-Lite, or
another declarative semantic layer between Visual IR and the existing adapters
is not part of this decision.

### Large documents compile from local fragments

A project-managed Dashboard may compose local visual and page fragments through
typed include collections. Includes are resolved relative to the Dashboard
resource, cannot escape the project, and are expanded before canonical
validation and fingerprinting.

Fragments are not project resources. They have no independent resource ID,
RBAC, deployment lifecycle, or runtime lookup. Local visual, filter, page, and
component IDs remain unique within the resulting Dashboard. Expansion uses one
normative composition algorithm:

- Mapping collections are unioned by key; a duplicate key is an error.
- Ordered sequences are concatenated in declared include order, followed by
  entries authored locally in the including document.
- Objects are never deep-merged.
- A fragment cannot patch, replace, or redefine content expanded earlier.
- ID uniqueness is checked after complete expansion.

Include cycles and missing files are rejected. CLI export can emit either the
canonical expanded document or the reviewable fragment layout without changing
document semantics.

Instance-managed drafts and immutable revisions store the expanded canonical
document. Fragment layout is a project-authoring concern and never becomes a
second runtime representation.

## Consequences

Dashboard YAML becomes more predictable and generated schemas become an exact
guide rather than a permissive approximation. Accepted fields cannot disappear
during compilation, filter concepts become clearer, and large project-managed
dashboards can be reviewed in smaller files without weakening stable identity or
authorization boundaries.

The refactor touches CUE and JSON Schema generation, YAML and JSON contracts,
dashboard authoring commands, builder signals, agent tools, compiler adapters,
revision fingerprints, API schemas, examples, tests, and documentation. Because
one canonical document is shared by all origins, partial rollout or dual
serialization would create divergent behavior and is prohibited.

TypeSpec and APIGen absorb the repeated structural work across those surfaces.
The generator becomes part of the Dashboard compatibility boundary: generated
artifacts are deterministic reviewable snapshots, and CI rejects stale output
or handwritten structural shadow contracts. CUE and compiler code remain
necessary for contextual and behavioral invariants that cannot be represented
as structural schemas, but do not duplicate property definitions.

Ordered reference sequences are slightly more verbose when many aliases are
needed. Explicit query tags add one field to every visual query, while making
execution semantics reviewable and preventing visual types or incidental keys
from selecting a different query plan.

Fragment composition adds filesystem validation and export choices. It must not
become a general templating language: no variables, conditionals, environment
lookups, arbitrary YAML merging, or executable preprocessing are allowed.

Existing snake_case fields, mapping query collections, null metric maps, filter
bindings, `filterControls`, dashboard `predicates`, implicit query variants,
physical-field aggregate dimensions, authored URL codecs, fixed canvas sizes,
open enum values, `sort.expr`, and presentation compatibility shorthands are
removed directly without aliases, readers, converters, migrations, or
deprecated fields.

## Confirmation

- One canonical schema fixture round-trips without semantic change through file
  loading, builder drafts, agent commands, immutable revision storage, compiler
  input, and CLI export.
- Generation tests prove one TypeSpec Dashboard declaration deterministically
  produces matching Go and TypeScript DTOs, sealed JSON Schema, imported CUE
  structure, API and signal projections, and reference documentation. Repository
  checks reject independently maintained structural shadow types.
- Generated CUE, JSON Schema, OpenAPI, Go, and TypeScript contracts expose the
  same camelCase names, closed unions, and required fields.
- Schema and compiler tests reject all removed names, mapping query
  collections, null-valued metrics, unknown enum values, inline expressions,
  `sort.expr`, implicit query variants, physical fields in aggregate dimension
  positions, and fields that do not apply to a visual or query type.
- Query tests cover every retained `aggregate`, `records`, `pivot`, `histogram`,
  and `distribution` shape and prove that a visual type cannot silently change
  query execution semantics. Result-frame tests prove deterministic unaliased
  names, explicit aliases, collision rejection, and exclusive use of result
  names by downstream sorts, presentation, calculations, interactions, IR,
  accessibility, and exports.
- Compilation tests prove that every accepted field either changes canonical
  compiled output or is retained as documented descriptive metadata; no field
  is silently discarded.
- Filter tests cover ordered filter-bar presentation, per-page in-canvas
  relocation, duplicate placement rejection, defaults, required state, typed
  values, semantic-member resolution, all-target compatibility failure,
  explicit target narrowing, and composition with semantic named filters
  without conflating their vocabularies. Distinct-option tests prove explicit
  dataset resolution, authorization and policy enforcement, dependency
  filtering, self-value exclusion, canonical identity and ordering, null
  policy, and post-order limiting.
- Shared-link tests version URL state independently from `apiVersion`, preserve
  supported codecs for their declared lifecycle, and prove that YAML cannot
  select a codec. Schema and compiler tests reject duplicate, invalid, and
  protocol-reserved `urlParameter` names.
- Fragment tests reject traversal, cycles, duplicate keys and IDs, deep merges,
  patches, and redefinitions; prove declared include order followed by local
  sequence order; and prove semantic equivalence between expanded and
  fragmented export.
- Layout tests reject authored canvas dimensions and breakpoints and prove
  dashboard defaults and page overrides compile to explicit, deterministic
  coordinates with derived height at every supported viewport contract.
- Renderer-boundary tests reject native renderer options and semantic
  transforms while proving the typed Visual IR remains the only dashboard-to-
  renderer contract.
- Repository dashboards, generated contracts, APIs, agent tools, documentation,
  and tests contain no removed compatibility shorthand before implementation is
  marked complete.
