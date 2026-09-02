# ADR-0016: Adopt standards-aligned data contracts and interchange

Status: accepted

Decision date: 2026-09-01

Implementation: pending

Deciders: LeapView maintainers

Supersedes: none

Amends: [ADR-0005](0005-use-project-wide-resource-graph.md), public Project
and control-plane authoring boundaries;
[ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md), structural
authority only;
[ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md), contract
evolution, quality identity, and governance metadata

Related: [ADR-0005](0005-use-project-wide-resource-graph.md);
[ADR-0006](0006-adopt-ossie-aligned-semantic-contract.md);
[ADR-0007](0007-adopt-plan-driven-project-delivery.md);
[ADR-0010](0010-adopt-strict-typed-data-resource-contracts.md);
[ADR-0011](0011-adopt-a-canonical-dashboard-document.md);
[ADR-0014](0014-adopt-an-asset-selected-refresh-pipeline-contract.md);
[ADR-0015](0015-adopt-durable-audit-and-compliance-controls.md);
[ADR-0017](0017-adopt-a-looker-aligned-semantic-access-contract.md);
[Data-contract versioning conformance](specifications/data-contract-versioning-conformance.md);
[Open Data Contract Standard 3.1.0](https://github.com/bitol-io/open-data-contract-standard/tree/v3.1.0);
[Bitol Open Data Product Standard 1.0.0](https://github.com/bitol-io/open-data-product-standard/tree/v1.0.0);
[W3C DCAT 3](https://www.w3.org/TR/2024/REC-vocab-dcat-3-20240822/);
[OpenLineage](https://openlineage.io/docs/spec/);
[Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html);
[Kubernetes RBAC v1](https://kubernetes.io/docs/reference/kubernetes-api/authorization-resources/role-binding-v1/);
[SCIM Core Schema](https://www.rfc-editor.org/rfc/rfc7643.html);
[Cedar](https://docs.cedarpolicy.com/);
[Perses dashboard specification](https://github.com/perses/spec);
[Grafana Git Sync permissions](https://grafana.com/docs/grafana/latest/as-code/observability-as-code/git-sync/permissions-grafana/);
[Looker access control](https://docs.cloud.google.com/looker/docs/access-control-and-permission-management);
[Lightdash user attributes](https://docs.lightdash.com/workspace-admin/user-attributes);
[Rill data access control](https://docs.rilldata.com/developers/build/metrics-view/security)

## Context and problem statement

LeapView already has executable data-contract behavior without a resource named
`DataContract`. Source owns an optional inferred, compatible, or strict schema
and typed freshness expectations. Model owns exact output fields, entities,
grain, and a closed quality-check vocabulary. The compiler derives lineage,
validates model output, and evaluates schema, freshness, identity, and quality
claims against a candidate before activation. Immutable gate evidence records
the exact source, runtime, binding, and candidate identities used in that
decision.

ADR-0006 established the pattern for external specifications: LeapView retains
one native typed semantic contract and one executable internal representation,
while Apache Ossie is a pinned import, export, validation, and extension
boundary. ADR-0010 subsequently made Connection, Source, and Model strict
native resources and deliberately separated portable authoring from
target-owned endpoints, credentials, and connector implementation details.
ADR-0014 selected OpenLineage as the runtime-lineage interoperability model but
did not make it an execution contract.

The Open Data Contract Standard (ODCS) now supplies a credible vendor-neutral
document for dataset identity, schema, quality, service levels, ownership, and
physical server descriptions. The related Bitol Open Data Product Standard
(ODPS) describes products through contract-addressed input and output ports.
W3C DCAT 3 describes catalogs, datasets, distributions, and data services for
federated discovery. OpenLineage describes runtime and design lineage and has
standard facets for schema, dataset versions, quality assertions, quality
metrics, and column lineage. OpenTelemetry, CloudEvents, OpenAPI, SCIM, OAuth,
OIDC, SPDX, SLSA provenance, and W3C Trace Context cover adjacent protocol and
operational boundaries.

Adopting any of those complete document shapes as LeapView's native YAML would
weaken existing invariants. ODCS server blocks commonly contain physical
coordinates that LeapView keeps in target bindings. ODCS roles are descriptive
contract metadata, not LeapView authorization grants. Arbitrary SQL quality
rules are broader than LeapView's closed, parser-governed check vocabulary.
ODPS does not determine whether one LeapView deployment bundle is one product
or a container for several products. DCAT is an RDF catalog vocabulary rather than
an executable BI model. OpenLineage, OpenTelemetry, and CloudEvents describe
observations or messages, not authored query behavior.

The native YAML also lacks several useful concepts made visible by these
standards. Quality checks do not have authored stable identity. Resource
versions do not express consumer compatibility intent, and deployment planning
does not classify a proposed contract change relative to the active resource.
Field metadata is too small to carry portable governance labels,
authoritative-definition links, or deprecation guidance. Product inputs and
published outputs are not exposed through a standard data-product projection.

The current authoring registry also crosses a product boundary. Group,
RoleBinding, Grant, and DashboardPublication are instance control-plane state,
not data-engineering definitions. DataPolicy combines durable query-governance
logic with instance-bound subjects. The Project resource is only a manifest of
directory globs plus public metadata for a deployment unit already implied by
the source root. Keeping these resources in the same YAML graph makes identity,
sharing, and publication changes look like data builds and makes a repository
deployment responsible for replacing live administrative state.

The question is how LeapView should become standards-compliant, use those
standards to strengthen its native YAML, and state conformance precisely without
creating parallel authorities, leaking target secrets, or turning an
interchange document into an execution bypass.

## Decision drivers

- Preserve one authoritative native resource graph and one executable internal
  representation for each concern.
- Make ODCS and related standards first-class, version-pinned interoperability
  boundaries comparable to the existing Ossie boundary.
- Improve native authoring where a standard reveals a durable product concept,
  but retain LeapView naming, closed unions, and capability ownership.
- Detect breaking changes against the exact active resource and expose their
  downstream impact before publication.
- Give quality rules and emitted evidence stable identity across revisions.
- Keep target endpoints, credentials, identities, memberships, role and grant
  assignments, publication state, runtime plans, and derived lineage out of
  portable authored documents.
- Reject unsupported or lossy conversions instead of silently weakening a
  contract.
- Distinguish document conformance, round-trip fidelity, and executable support
  so the product does not claim more than it proves.
- Keep evolving external schemas and SDKs out of core compiler and runtime
  domain types.
- Reuse upstream schemas and independent tools as conformance oracles without
  making their language runtimes product dependencies.

## Considered options

### Keep native resources only

LeapView could document Source and Model as data contracts and avoid external
formats. This preserves the strongest internal boundary with the least mapping
work, but makes contract exchange, catalog federation, lineage integration, and
data-product packaging proprietary. It also misses useful evolution and stable
quality-identity concepts highlighted by the standards.

### Adopt ODCS, ODPS, or DCAT as native analytics YAML

LeapView could accept those documents directly as the authoritative Source,
Model, deployment bundle, or catalog representation. This would maximize superficial
format compatibility, but each standard combines concerns differently from the
LeapView compiler. Native adoption would either expose target coordinates and
open extension bags or require a large implicit profile whose actual behavior
could not be read from the standard document alone. It would also make an
external specification's release cycle part of the `leapview.dev/v1` execution
contract.

### Add a parallel DataContract resource

A new resource could reference Source or Model while repeating schema,
freshness, quality, ownership, and version metadata. This gives the concept a
clear name but creates two places for the same guarantee. Compilation would
need precedence, synchronization, or conflict rules, and downstream consumers
could no longer tell which declaration is authoritative.

### Delegate contract handling to Data Contract CLI or another service

LeapView could invoke a Python CLI or remote contract manager for linting,
testing, conversion, and publication. That supplies a broad connector and
quality ecosystem, but moves candidate admission, credential handling, and
arbitrary executable rules across a new process or network trust boundary. It
also couples the Go product lifecycle to another tool's dependency graph and
semantics.

### Retain native authority with standards-aligned metadata and adapters

LeapView can strengthen its native contracts, compile them once, and project
the resulting typed graph and evidence into exact pinned external formats.
Imports can lower through explicit adapters into the same generated authoring
DTOs and compiler validation as native YAML. This retains one execution path
while providing independently testable interoperability.

## Whole authored-contract review

The review covers every resource currently accepted by the authoring schema
registry, but acceptance by the current compiler does not imply that the
resource belongs in the authoring product. The industry comparison separates
three concerns:

1. **Analytics source** is reviewed, versioned, tested, and deployed as code.
   It contains Connection, Source, Model, SemanticModel, Pipeline, and
   Dashboard resources.
2. **Consumer data-governance semantics** that change query results are also
   reviewed as code, but live only on the SemanticModel consumption contract.
   ADR-0017 defines the Looker-aligned access-grant and access-filter model.
3. **Control-plane state** is administered per instance through authenticated
   UI and API surfaces. It contains identities, group membership, roles, role
   assignments, grants, sharing, publication, embedding configuration, and the
   mapping of IdP claims to principal attributes.

This is the common separation in mature dashboard-as-code products.
[Grafana Git Sync](https://grafana.com/docs/grafana/latest/as-code/observability-as-code/git-sync/permissions-grafana/)
explicitly does not synchronize folder or dashboard permissions to Git.
[Looker](https://docs.cloud.google.com/looker/docs/access-control-and-permission-management)
keeps access filters and access grants in LookML while users, groups, roles, and
content access are administered separately.
[Lightdash](https://docs.lightdash.com/workspace-admin/user-attributes) keeps
row and column restrictions in model YAML while admins assign user attributes;
its [optional content-as-code export](https://changelog.lightdash.com/) also
supports users, groups, roles, and permissions for backup, migration, or full
GitOps. [Rill](https://docs.rilldata.com/developers/build/metrics-view/security)
keeps row and column security in metrics-view YAML while project access is a
prerequisite managed in Rill Cloud.
[Perses](https://perses.dev/perses/docs/api/rolebinding/) exposes Project,
Role, and RoleBinding as uniform API YAML resources, which is appropriate for
its Kubernetes-style control-plane API but is not evidence that those resources
belong in LeapView's data-engineering source contract.

| LeapView contract | Strongest external contract reviewed | Whole-shape adoption decision |
|---|---|---|
| Common `apiVersion`/`kind`/`metadata`/`spec` envelope | Kubernetes API conventions and JSON Schema 2020-12 | Retain. The envelope is useful and already consistent across LeapView resources, but LeapView resources are not Kubernetes objects and must not copy cluster metadata, status, namespace, or reconciliation fields. |
| Project | dbt project files, Rill `rill.yaml`, Perses Project API resources, and Bitol ODPS 1.0.0 | Remove completely from the public authored contract. The source root and conventional resource directories are the compilation input; deployment creates one atomic release without a `kind: Project` document. Instance, environment, deployment, and data-product identities are supplied or derived at their owning API boundary. Other tools' project files contain real defaults or package behavior; LeapView's include-only manifest does not justify a public resource. |
| Connection | ODCS 3.1.0 servers, dbt profiles, and connector-specific source documents | Retain. Those shapes describe physical endpoints and tool-specific credentials or options; LeapView deliberately separates portable connector intent from target-owned binding and secrets. |
| Source | ODCS 3.1.0 schema, properties, servers, and SLA properties; dbt sources | Retain the Source location and schema-mode contract. ODCS contributes contract identity, field governance, and interchange vocabulary, but a whole ODCS document can contain several schema objects and target coordinates and has weaker executable requirements. |
| Model | ODCS 3.1.0 schema and quality rules; dbt models, contracts, and tests | Retain the governed definition, named entities, grain, exact decimal type, and closed checks. ODCS quality allows text, arbitrary SQL, and vendor custom rules; its fields do not express LeapView's named identity and grain semantics. |
| SemanticModel | Apache Ossie and dbt MetricFlow | Keep the ADR-0006 approach: a stricter LeapView execution contract aligned with Ossie vocabulary and MetricFlow behavior, with exact Ossie documents at the adapter boundary. Ossie's current core remains a draft and permits open vendor extensions. |
| Pipeline | Argo CronWorkflow and dbt ancestor selection | Keep the ADR-0014 approach. The scheduling sub-contract already copies pinned Argo field names and behavior where meanings match, while selection, candidate identity, evidence, and publication remain LeapView-owned. A whole Argo workflow would expose containers, scripts, secrets, and infrastructure. |
| Dashboard and nested visuals | Perses open dashboard specification and Vega-Lite | Retain the governed BI document. Perses is observability- and plugin-oriented, while Vega-Lite permits data loading, transformations, and expression behavior that would bypass the semantic query boundary. Both are useful migration or visualization references, not native authority. |
| Group | SCIM 2.0 Group, SAML/OIDC claims, and dashboard product team APIs | Remove from analytics YAML. LeapView's SCIM service owns provisioned users and groups; SAML/OIDC or the admin API supplies group and attribute mappings. SCIM deliberately leaves authorization meaning to the service provider. |
| RoleBinding and Grant | Product RBAC APIs, SAML/OIDC role mapping, Terraform providers, Kubernetes RBAC, OpenFGA, and Cedar | Remove from analytics YAML. Keep LeapView's closed capabilities and authorization engine, but manage assignments through the control-plane UI/API or a future IaC provider. Repository deployment must not replace live role or grant state. |
| DataPolicy | Looker access filters/grants, Lightdash user-attribute filters, Rill metrics-view security, Cedar, and database row/column policies | Remove the standalone, subject-bound resource. SemanticModel is the only authored policy target; ADR-0017 defines its Looker-aligned `accessGrants`, `requiredAccessGrants`, and `accessFilters`. Attribute values and assignments remain in the control plane. |
| DashboardPublication | Grafana dashboard/folder permissions, Looker content access, W3C CSP `frame-ancestors`, and product sharing APIs | Remove from analytics YAML. Publication, sharing state, embed origins, URLs, and revision activation are environment-specific security and lifecycle state managed through UI/API. Headless automation uses a service principal and API or a future IaC provider. |

The resulting public authoring registry has six top-level resource kinds:
Connection, Source, Model, SemanticModel, Pipeline, and Dashboard. There is no
root manifest resource; the compiler discovers these resources from fixed
conventional directories beneath the selected source root. A later need for
package-wide executable defaults requires a narrowly typed root configuration
decision, not the return of a generic Project object or include registry.

No reviewed external specification is a safe one-for-one replacement for any
of those six complete LeapView resources. Exact reuse is nevertheless required
at a matched boundary: ODCS and Ossie documents, ODPS and DCAT exports,
OpenLineage events, SCIM API resources, OpenAPI descriptions, and emitted CSP
syntax must conform to their standards without a proprietary wrapper. Native
YAML may copy a version-pinned sub-contract one-for-one only when its concern,
defaults, security boundary, and observable behavior are the same. Pipeline's
Argo scheduling profile is the existing example.

ODCS illustrates why matching YAML appearance is insufficient. Version 3.1.0
requires the document version, standard version, kind, ID, and status, but a
schema object requires only its name and a property requires only its name.
Its portable types have `number` but no exact decimal type; SLA property names
are open; server blocks contain target coordinates; and quality rules include
arbitrary SQL and vendor-custom execution. ODCS is broader than LeapView's
Source and Model contracts, but it is not stronger as an executable analytical
contract. LeapView therefore adopts exact ODCS documents for interchange and a
documented executable profile, not as its internal DTO or authored resource
envelope.

## Decision outcome

LeapView adopts the standards-aligned native-authority option. Source and Model
together remain the native executable data-contract boundary; no parallel
`DataContract` resource is introduced. Native YAML, imported standard
documents, interactive authoring, APIs, and agents must all lower through the
same generated contracts, compiler, graph validation, candidate gates, and
publication lifecycle.

The public source contract contains only Connection, Source, Model,
SemanticModel, Pipeline, and Dashboard. `Project`, `Group`, `RoleBinding`,
`Grant`, `DataPolicy`, and `DashboardPublication` cease to be accepted authored
resource kinds. `Project` also ceases to be a public API, identity, route, and
authorization resource: repository root, deployment, active generation, and
instance are sufficient boundaries. Internally, code may temporarily use
`project` to name the compiled graph during migration, but it is not a public
concept or stable contract.

This ADR amends ADR-0005 by removing Project as a public resource while
retaining one atomic compiled graph per instance and by moving authorization
and publication state wholly to the control plane. It amends ADR-0006's
structural-authority boundary for SemanticModel and ADR-0010's check and
metadata shapes. Their remaining graph, semantic, security, and compilation
decisions remain in force. ADR-0017 owns the executable SemanticModel access
contract; this ADR owns only the authored/control-plane separation.

External specifications are owned by capability-specific adapters. An adapter
may validate, import, export, or emit a standard document, but its DTOs and
version-specific vocabulary cannot become core analytics, compiled graph,
deployment, or runtime types. Every adapter pins an exact supported standard
version and the digest of any vendored schema. A mutable `latest`, default
branch, or unversioned schema URL is not a conformance boundary.

### Exact reuse is semantic, not cosmetic

A standard shape is copied into native YAML only if LeapView can inherit its
complete normative meaning. The copied unit must have a pinned version, a
closed or explicitly governed extension policy, compatible defaults, one
capability owner, and conformance fixtures against an independent
implementation. LeapView does not rename a standard field while claiming exact
reuse, and it does not copy a familiar field name while assigning different
behavior.

When those conditions do not hold, the standard stays in an adapter and the
native contract is deliberately aligned or mapped. A valid standard document
must remain valid at the adapter boundary: LeapView does not wrap an ODCS,
Ossie, ODPS, DCAT, OpenLineage, SCIM, OpenAPI, or CloudEvents document in the
`leapview.dev/v1` resource envelope and call the result compliant.

### Authored structural authority converges on TypeSpec

Each of the six authored public resource shapes will have one generated TypeSpec
structural authority. The common resource envelope, metadata, identity scalars,
provenance, and descriptive governance types become shared declarations rather
than copies in data-resource TypeSpec, Pipeline TypeSpec, Dashboard TypeSpec,
and handwritten CUE.

SemanticModel moves from handwritten CUE structural definitions to TypeSpec.
Generated JSON Schema 2020-12 remains the editor and structural validation
artifact. CUE consumes generated schemas and owns only cross-resource and
contextual constraints that TypeSpec cannot express. Removed resource kinds do
not receive new authoring schemas.

The generated structural schemas express constraints that currently fail only
after decoding whenever JSON Schema can represent them. Entity and unique-check
field lists and accepted-value lists are non-empty. Row-count checks require at
least one bound. Source freshness requires at least one warning or error
threshold. The compiler retains contextual rules such as reference resolution,
compatible field datatypes, ordered thresholds, non-cyclic graphs, and
authorization.

### Control-plane state is not analytics source

Group, role, grant, sharing, and publication changes are durable audited
control-plane mutations. The UI and the same generated OpenAPI surface support
them. Service principals provide headless automation, and a future Terraform or
OpenTofu provider may offer declarative reconciliation without adding those
objects to analytics YAML.

SCIM is the preferred automated user and group lifecycle boundary. SAML or OIDC
is the authentication and claims boundary. Neither standard defines LeapView's
complete authorization or publication lifecycle: SCIM does not define the
authorization meaning of group membership, and SAML/OIDC does not publish a
dashboard. Role mappings, explicit grants, sharing, and publication therefore
remain LeapView API operations even when identity is fully automated.

A repository deployment never deletes, replaces, or implicitly widens
control-plane state. References from control-plane grants or publications to
source-managed resources use stable resource IDs and are revalidated against a
candidate generation before activation.

### Stable identity does not depend on Project

An instance has at most one active compiled source bundle. A different source
root is a candidate replacement for that graph, not a second namespace. Two
independently active source bundles require separate instances.

Authored `metadata.id` is portable across repository, source-root, directory,
filename, branch, and symbolic-name changes. First activation binds the tuple
of instance identity and authored ID to an immutable resource UID and kind.
Control-plane references retain that UID, authored ID, and expected kind; they
never resolve by name, path, or whichever candidate currently contains a
matching string. Candidate-wide ID collisions, including cross-kind collisions,
block graph construction.

Removal tombstones the UID and suspends its grants and publications. Normal
deployment cannot reuse the ID or silently rebind those references. An explicit
audited restore may recover the same logical resource and UID after recompiling
all dependencies, but control-plane grants and publications remain suspended
until separately reauthorized. External standard projections qualify the
portable authored ID with a stable instance or tenant URI rather than claiming
that the raw ID is globally unique.

The linked data-contract versioning conformance specification owns the exact
collision, tombstone, restore, rollback, and projection requirements.

### Semantic access policy has a separate decision

The standalone `DataPolicy` resource is removed because it combines authored
query-governance logic with instance-bound subjects. The rule that changes a
consumer query result is SemanticModel code; the assignment of identity
attributes is control-plane state.

ADR-0017 defines the exact Looker-aligned semantic access contract, its
fail-closed behavior, and its deliberate initial limits. Source, Model,
Dashboard, Pipeline, and Connection do not gain authored policy blocks. This
ADR does not create a second policy grammar or restate ADR-0017's executable
semantics.

### Native resource contracts gain evolution metadata

Contract-bearing resources may declare an authored Semantic Versioning 2.0.0
version and one closed compatibility policy in common resource metadata. The
initial policy vocabulary is `none` and `backward`. Absence means that LeapView
reports the change classification but does not infer a compatibility promise.

The authored shape is:

```yaml
metadata:
  contract:
    version: 2.1.0
    compatibility: backward
```

`contract` is absent on resources that do not expose a versioned consumer
contract. It is not a generic metadata bag.

The initial contract-bearing resource set is Source, Model, and SemanticModel.
Adding contract-version semantics to another resource kind requires an explicit
extension of this decision.

Canonical contract bytes use the versioned `leapview.contract/v1` projection
defined by the linked data-contract versioning conformance specification. The
projection is generated from validated TypeSpec DTOs and includes resource
identity, authored contract metadata, public schema and semantics, executable
guarantees, governance and deprecation declarations, normalized
result-affecting Model logic, and the complete SemanticModel consumption and
access contract. It excludes prose descriptions, display metadata, tags,
ownership and domain labels, documentation, provenance, AI context, source
paths, credentials, target bindings, runtime state, and derived observations.
Changing excluded metadata does not require a contract version.

The typed projection is normalized and serialized as RFC 8785 canonical JSON,
then identified by a SHA-256 digest. The profile defines map, set, and ordered
collection behavior; Unicode, number, date, timestamp, URL, default, null, and
logical-type normalization; and independent cross-language golden fixtures.
Every public field must be classified in TypeSpec as contract, descriptive,
operational, secret, or derived so an unreviewed field cannot silently enter or
leave the digest.

Once a contract version has been published, its profile, canonical bytes, and
digest are immutable. A candidate that reuses the version with different bytes
is rejected; a change to included content requires a new version. Build
metadata does not establish a distinct compatibility baseline. Changing the
canonical algorithm requires a new profile and preserves historical bytes.
LeapView's classified public contract is the public API to which Semantic
Versioning rules apply; a version number alone does not define what is
compatible.

`backward` means that consumers valid against the selected active contract
continue to receive compatible names, logical types, nullability, identities,
grain, and executable semantic members from the candidate. It does not mean
that arbitrary physical connector types or display metadata are frozen.
Compatibility is evaluated against the exact active resource with the same
stable resource ID, never against whichever file happens to be present in a
working tree.

Planning classifies at least the following changes:

- additive, such as an optional compatible field or descriptive metadata;
- behavioral, such as a quality threshold or freshness-policy change whose
  effect is not a wire-shape change;
- breaking, such as field removal or rename, new required output, incompatible
  type or nullability, entity or grain change, unsafe relationship change, or
  removal or result-affecting redefinition of a published semantic member; and
- indeterminate, when the current adapter or compiler cannot prove a safe
  classification.

An indeterminate change cannot satisfy `backward`. A breaking change requires
a new major semantic version before it can satisfy version policy, but a major
version does not itself authorize publication or bypass impact review. The
plan retains the baseline identity, normalized diff, affected-resource graph,
classification, and policy outcome as candidate evidence. Deployment policy
decides whether a reported breaking change needs approval or must be rejected;
the compatibility engine does not silently rewrite the resource.

Authored lifecycle status is not added to these resources. Draft, active,
superseded, and retained state remain facts of authoring, deployment,
publication, and serving lifecycles. Standard exports derive status from the
authorized lifecycle projection instead of accepting a YAML word that can
contradict active state.

### Quality rules gain stable authored identity

Every authored Model check gains a required stable ID plus optional description
and tags. The ID is unique within the Model and remains the evidence identity
across threshold, severity, or description changes. Check type continues to
determine the executable evaluator and quality dimension; authors do not repeat
an engine name, free-form expression language, or generic dimension that can
contradict it.

Every evaluation binds the check ID to the Model contract version, candidate,
dataset or relation version, evaluation time, configured severity, outcome,
expected value, and safely reportable observation. Raw failing rows, sensitive
values, credentials, and unrestricted SQL are not evidence fields. These
records map to OpenLineage quality facets without making OpenLineage the local
evidence authority.

### Fields gain portable governance metadata

Source schema fields and Model fields may carry tags, a critical-data-element
marker, an organization-defined classification label, typed authoritative
definition links, and typed deprecation guidance with an optional replacement
field. Existing labels, descriptions, AI context, logical types, and
Source-field nullability keep their current meaning. Model fields gain the same
optional `nullable` declaration so compatibility analysis does not infer
nullability from a quality check or a physical engine observation. Absence
means unspecified; it does not mean nullable.

Governance metadata is descriptive unless the ADR-0017 SemanticModel access
contract references it through an explicitly defined compiler feature. A
classification label cannot create, remove, or imply a grant, mask, or row
policy. Imported ODCS roles and team members likewise cannot mutate LeapView
principals, groups, roles, bindings, or grants.

Authoritative definitions use typed URL entries rather than a generic
`customProperties` bag. Unknown imported extension fields are rejected unless
the selected adapter owns a versioned, namespaced preservation rule.

Native field collections remain identifier-keyed maps so duplicate names are
structurally impossible. Native `datatype` retains LeapView's Ossie-aligned
portable vocabulary, including the distinction between exact `Decimal` and
approximate `Float`; it is not renamed to ODCS `logicalType` merely to resemble
an ODCS document. ODCS property arrays and logical types are mapped explicitly,
with an extension or loss diagnostic when a value has no exact standard form.

An illustrative Model fragment is:

```yaml
spec:
  fields:
    legacy_customer_id:
      datatype: String
      nullable: false
      tags: [identifier]
      criticalDataElement: true
      classification: restricted
      authoritativeDefinitions:
        - type: businessDefinition
          url: https://catalog.example/terms/customer-id
      deprecation:
        since: 2.1.0
        reason: Replaced by the canonical customer key.
        replacement: customer_key
  checks:
    - id: legacy_customer_id_present
      type: non_null
      field: legacy_customer_id
      severity: error
      description: Every published row carries its legacy customer identity.
      tags: [contract]
```

Deprecation `since` is a semantic version no later than the containing contract
version. A replacement resolves to another field in the same contract and
cannot form a self-reference or replacement cycle.

### Guarantees stay with the capability that can enforce them

LeapView does not add a generic SLA or quality map. Source continues to own
source schema and freshness. Model owns row and relationship assertions.
Pipeline owns refresh selection and scheduling. Managed data and publication
policy own retention. A published service owns its availability objective.

A new guarantee enters native YAML only with a closed generated shape, one
capability owner, a compiler rule, a bounded evaluator, normalized evidence,
and documented warning, blocking, unavailable, and empty behavior. ODCS fields
that do not meet those conditions may round-trip as standard metadata but are
not described as LeapView-executable guarantees.

### ODCS is the data-contract interchange standard

The initial ODCS profile pins version 3.1.0 and provides validation, import, and
export. The adapter maps stable resource identity, schema, field semantics,
quality checks, freshness, ownership metadata, and authoritative definitions
where the mapping is lossless. LeapView-only execution semantics use a
versioned namespaced ODCS extension only when the standard permits one.

Portable export omits target endpoints and secrets. A separately authorized
target-specific test projection may include non-secret connection coordinates
when the caller can inspect that binding, but credentials are always supplied
out of band and never serialized. Imported server blocks resolve only through
an explicit existing Connection and target-binding workflow; they do not create
credentials, widen network authority, or bypass connector admission.

Imported arbitrary SQL quality rules are never executed. The adapter either
maps a rule to the closed native check vocabulary, preserves it as explicitly
non-executable standard metadata, or rejects the import with a precise
diagnostic. It never reports successful import after dropping the rule.

### Bitol ODPS is the data-product interchange target

The selected data-product standard is specifically the Linux Foundation
Bitol Open Data Product Standard 1.0.0. LeapView does not use the ambiguous
acronym `ODPS` without the publisher and version in public conformance claims,
because another unrelated Open Data Product Specification uses the same
acronym.

The initial projection treats one deployed source bundle and its active
generation as one data-product envelope. Sources selected as promised
dependencies become input ports. Only outputs explicitly selected by the
authorized export request or publication state become output ports; internal
Models and dashboards do not become product promises merely because they exist
in the graph. Ports reference the stable IDs and versions of their ODCS
contracts. Authorized catalog, observability, and control endpoints may become
management ports without embedding credentials.

LeapView does not introduce a `DataProduct` resource in this decision. If one
deployed source bundle must contain multiple independently owned, versioned,
and published products, that requirement needs a separate data-product decision
rather than an implicit grouping convention or the return of Project.

### DCAT 3 is the catalog export target

The initial DCAT 3 boundary is authorization-filtered export, not import. A
LeapView catalog maps to `dcat:Catalog`; eligible Sources and published outputs
map to `dcat:Dataset`; authorized files or representations map to
`dcat:Distribution`; and headless or public data endpoints map to
`dcat:DataService`. The initial serialization is deterministic JSON-LD.

DCAT output includes only metadata and access coordinates visible to the
caller. It does not expose target credentials, private object keys, internal
attachment names, compiled SQL, or inaccessible graph nodes. RDF vocabulary
and JSON-LD framing remain adapter concerns and do not appear in native YAML.

### Runtime standards are projections, not authoring formats

OpenLineage remains the lineage and data-quality event boundary selected by
ADR-0014. Pipeline maps to Job, PipelineRun maps to Run, and Sources and
materialized outputs map to Datasets. Standard schema, version, quality,
statistics, and lineage facets are preferred; immutable versioned LeapView
facets carry only evidence that has no lossless standard field. The append-only
LeapView ledger remains authoritative.

OpenTelemetry/OTLP is the target for process telemetry when LeapView creates
local spans or exports metrics and logs. W3C Trace Context remains the
propagation format. Telemetry may carry stable instance, deployment, plan,
candidate, generation, pipeline, check, and query identities, but not raw result values,
credentials, access tokens, unrestricted SQL, or unbounded principal labels.
No OpenTelemetry field becomes authored analytics YAML.

CloudEvents 1.0.2 is reserved for a future external lifecycle-event or webhook
surface. It does not replace Pagestream SSE, the audit ledger, or OpenLineage.
Adding that surface requires a separate API and delivery decision, but its
event envelope and type naming must be CloudEvents-compatible rather than a
new proprietary wrapper.

OpenAPI remains authoritative for LeapView HTTP APIs and should move from the
current 3.0 output to a generator-supported 3.1 or later profile in a separately
tested generator change. dbt artifacts remain a valuable de facto import
source identified by ADR-0005, but are described as dbt compatibility rather
than standards compliance. AsyncAPI, Arrow Flight SQL, Substrait, and W3C DQV
are not adopted by this decision: LeapView has no matching public asynchronous
API, database-SQL service, portable execution-plan boundary, or need for a
second quality vocabulary today.

### Conformance claims are explicit and machine-readable

LeapView publishes a generated conformance matrix for every external profile.
Each entry identifies the publisher, standard, exact version, schema digest,
supported directions, extension version, and one or more proven levels:

- **document** — emitted or accepted bytes conform structurally to the pinned
  standard;
- **round-trip** — the declared profile survives native-to-standard-to-native
  conversion without semantic loss;
- **execution** — the listed standard concepts are enforced by the native
  compiler and bounded runtime evaluator; and
- **emission** — runtime events or telemetry conform to the pinned protocol and
  facet schemas.

The word `compliant` must name the standard version, profile, direction, and
level. Passing an upstream JSON Schema alone proves document shape, not
round-trip fidelity or execution. An adapter returns a machine-readable loss
and unsupported-feature report for every conversion; success requires that the
report satisfy the requested conformance level.

## Implementation sequencing

This decision is delivered as independently qualified milestones rather than
one indivisible migration:

1. **Identity and authoring boundary:** establish the instance resource-UID
   registry, collision and tombstone behavior, remove the public Project and
   control-plane resource kinds from authoring, and migrate durable references.
   No new control-plane reference may depend on authored resources before this
   milestone qualifies.
2. **Structural and version authority:** move the six authored structures to
   generated TypeSpec authority, implement `leapview.contract/v1`, contract
   diffing, quality IDs, governance, and immutable publication evidence.
3. **Semantic access:** qualify ADR-0017, the typed attribute registry, planner
   security barriers, discovery behavior, caches, audit, and every semantic
   consumer before any path relies on the new policy semantics.
4. **Data-contract interchange:** implement and qualify the pinned ODCS profile
   independently of native contract execution.
5. **Product and catalog projections:** implement Bitol ODPS and DCAT exports
   after stable identity, versioning, publication selection, and authorization
   filtering qualify.
6. **Runtime emissions:** qualify OpenLineage and later telemetry projections
   against the already stable native identities and evidence.

Each milestone has its own generated checks, fixtures, failure behavior, and
evidence. Completion of a later milestone cannot compensate for an unqualified
earlier dependency.

## Consequences

LeapView gains a coherent standards portfolio without weakening native
execution. Customers can exchange data contracts, package a deployed source
bundle as a data product, federate catalog metadata, and emit lineage and
quality evidence using recognized formats. Native authors gain stable check identity, contract
evolution policy, impact-aware planning, richer field governance, and precise
deprecation guidance even when they never export a standard document.

Analytics deployments become safer and less surprising: changing a dashboard
or model cannot replace group membership, grants, or publication state, and an
identity change does not require rebuilding the analytical graph. Removing the
include-only Project manifest also makes the repository root the obvious unit
of compilation. The migration must remove public Project IDs and routes rather
than preserve a compatibility alias, because the product is not yet public.

Semantic access rules remain reviewable and testable in the SemanticModel,
while administrators can change user and group attribute values immediately in
the control plane. ADR-0017 owns candidate validation, evaluation, composition,
enforcement, cache identity, and fail-closed behavior for that boundary.

The compiler and deployment lifecycle take on meaningful new work. Contract
diffing must be deterministic and version-aware. Imported documents require
source-positioned diagnostics and explicit mapping reports. Adapters need
versioned schemas, fixtures, extension registries, and upgrade review. A
standard upgrade cannot be treated as a dependency-only change because it may
change accepted documents or emitted meaning.

One deployed source bundle initially maps to one Bitol data product, which is
simple and portable but does not model multiple product ownership boundaries in
one deployment. DCAT is export-only initially. Some valid ODCS features remain
non-executable in LeapView. These restrictions are documented profile limits,
not silent partial support.

The new metadata increases authoring surface area. Generated schemas,
documentation, builder forms, CLI export, examples, and agents must expose one
consistent shape. Organization-defined classifications need governance to
remain useful, but do not become hidden authorization behavior.

Independent conformance tooling remains a development and CI oracle. It may
increase CI setup and update cost, but does not enter the production binary or
receive production credentials by default.

## Confirmation

- The authoring registry accepts exactly Connection, Source, Model,
  SemanticModel, Pipeline, and Dashboard. Fixtures prove `Project`, `Group`,
  `RoleBinding`, `Grant`, `DataPolicy`, and `DashboardPublication` are rejected
  as authored kinds with migration diagnostics.
- Compilation selects a source root, discovers only conventional resource
  directories, and produces one atomic graph without `leapview.yaml`, a public
  Project ID, or include-glob behavior. Public routes, API schemas, grants, and
  audit subjects contain no Project resource.
- Identity fixtures prove candidate-wide cross-kind ID uniqueness, stable UIDs
  across source-root and file moves, kind-change rejection, tombstone
  non-reuse, explicit restore behavior, rollback identity, and durable
  control-plane references that cannot silently rebind.
- TypeSpec owns the six authored structures, including the shared envelope,
  metadata, contract evolution, quality identity, field governance,
  deprecation, and the ADR-0017 SemanticModel access contract. It generates Go
  DTOs, JSON Schema, documentation, and any browser types from those
  declarations.
- Architecture tests reject handwritten public structural fields in CUE and
  prove that CUE validation consumes the generated schemas before applying only
  contextual graph constraints.
- Architecture fixtures reject authored access-policy fields outside
  SemanticModel and delegate the access-rule schema and compiler corpus to the
  ADR-0017 conformance specification.
- Control-plane tests prove SCIM and admin APIs own users and groups; role,
  grant, sharing, and publication APIs own assignments and lifecycle state; and
  analytics deployment cannot create, update, or delete any of them.
- Schema and compiler tests reject duplicate check IDs, invalid semantic
  versions, unknown compatibility policies, malformed authoritative links,
  contradictory deprecation replacements, and generic extension bags.
- Compatibility fixtures classify additive, behavioral, breaking, and
  indeterminate changes across Source, Model, and published SemanticModel
  contracts. Candidate evidence binds the exact active baseline, affected graph,
  classification, and policy result.
- Canonicalization fixtures classify every public DTO field, prove the exact
  `leapview.contract/v1` projection and defaults, produce byte-identical RFC
  8785 and SHA-256 results in Go and an independent implementation, and reject
  reuse of a published version with different bytes.
- Pinned ODCS 3.1.0 fixtures pass the upstream schema and an independent CLI
  linter. Import/export golden tests cover supported types, nested-field
  diagnostics, identities, relationships, checks, freshness, metadata,
  extensions, unknown fields, and loss reports.
- Security tests prove ODCS import/export never serializes credentials, never
  creates a target binding or Access grant, never executes imported arbitrary
  SQL, and cannot widen connector, filesystem, object-store, or network scope.
- Bitol ODPS 1.0.0 fixtures prove that only selected Source dependencies and
  explicit publications become ports, every port refers to a stable ODCS
  contract ID and version, and internal resources remain absent.
- DCAT 3 JSON-LD fixtures normalize to deterministic RDF graphs and contain
  only authorization-visible catalogs, datasets, distributions, and services.
- OpenLineage contract tests validate standard schema, version, quality,
  statistics, parent, and lineage facets plus immutable canonical schema URLs
  for every LeapView extension facet.
- The generated conformance matrix is checked against registered adapters,
  pinned schemas, documentation, CLI commands, and test fixtures. No adapter or
  public compliance claim can exist without a matching matrix entry.
- Architecture tests keep standards-version DTOs inside interchange or
  telemetry adapters and prevent core compiled-graph, analytics, deployment,
  release, and runtime packages from importing them.
- Documentation describes the exact standard publisher, version, direction,
  conformance level, unsupported features, extension policy, and credential
  boundary for every advertised profile.
