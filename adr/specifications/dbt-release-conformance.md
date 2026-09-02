# dbt release conformance specification

Status: proposed

Profile: `leapview.dbt-release/v1`

Initial producer profile: `leapview.dbt-duckdb-parquet/v1`

Initial publication profile:
`leapview.dbt-duckdb-parquet-azure-publication/v1`

Initial ingress profile: `leapview.azure-parquet-ingress/v1`

Last updated: 2026-09-02

Owners: LeapView maintainers

Governing decision:
[ADR-0019](../0019-integrate-dbt-through-immutable-build-releases.md)

Related decisions:
[ADR-0007](../0007-adopt-plan-driven-project-delivery.md),
[ADR-0008](../0008-isolate-ducklake-candidate-physical-state.md),
[ADR-0009](../0009-separate-control-and-physical-transactions.md),
[ADR-0016](../0016-adopt-standards-aligned-data-contracts-and-interchange.md),
and [ADR-0018](../0018-retain-project-as-the-durable-deployment-namespace.md)

## Purpose

This implementation-facing specification defines the release envelope, dbt
artifact correlation, physical-output evidence, generated-resource mapping,
target binding, failure behavior, and initial production showcase required by
ADR-0019. The ADR owns the immutable-build-release architectural choice.

The terms **must**, **must not**, **should**, and **may** are normative. A
requirement that has not been implemented is pending, not implicitly waived.

## Profile stability

The `/v1` identifiers in this document are reserved while the specification is
proposed. Their wire semantics become immutable when the first conforming
producer release is admitted by a released LeapView version. After that point,
editorial clarification, additional fixtures, and implementation organization
may evolve, but any change to envelope shape, required fields,
canonicalization, schema normalization, digest inputs, selection semantics, or
acceptance and failure behavior requires a new profile identifier. Producers
and consumers must reject unknown profile identifiers rather than inferring
compatibility.

## Scope

The base `leapview.dbt-release/v1` profile covers:

- ingestion of one exact dbt release into one selected LeapView Project and
  environment;
- strict parsing and correlation of `manifest.json` and `run_results.json`;
- separate closed build and output selection policies, with complete executed
  model and test evidence;
- qualified full-replacement export, including true zero-row outputs;
- duplicate-preserving equivalence to the tested dbt relation;
- exact physical-object, integrity, row-count, and versioned schema evidence;
- portable storage aliases resolved through target-owned connection mappings;
- an envelope-owned publication profile and producer-guaranteed retention
  deadline;
- a request-owned ingress profile and explicit guaranteed or best-effort
  acquisition mode;
- verified target-owned ingress into the private DuckLake candidate followed by
  normal LeapView qualification, activation, retention, and rollback; and
- release provenance and failure behavior.

The initial `leapview.dbt-duckdb-parquet/v1` producer profile narrows the base
profile to one `dbt build`, dbt-duckdb, and one complete Parquet object per
output-selected model. The Azure publication profile defines the producer's
GitHub Actions and Azure Blob or ADLS publication obligations. The Azure ingress
profile independently defines LeapView's destination acquisition and decode
obligations. Only explicitly qualified publication-profile and ingress-profile
pairs are compatible.

The profiles do not define MetricFlow ingestion, incremental external models,
mutable warehouse relations, multi-file partition manifests, arbitrary Python
model execution inside LeapView, dbt package installation by LeapView, or
cross-Project consumption. A future Project-sharing ADR would govern
publication of a LeapView-governed output derived from a dbt release.

## Terms and identity

| Term                 | Meaning                                                                                 | Durable identity                                                  |
| -------------------- | --------------------------------------------------------------------------------------- | ----------------------------------------------------------------- |
| dbt invocation       | One execution that produced correlated dbt artifacts                                    | dbt `invocation_id`                                               |
| Build selection      | Canonical selectors and flags determining the complete dbt executable graph             | Envelope field plus correlated `run_results.args`                 |
| Output selection     | Canonical subset of successfully built relation-producing models exported to LeapView   | Envelope field and exact output ID set                            |
| Release envelope     | Canonical `release.json` committing a complete producer release                         | `ReleaseDigest`                                                   |
| Release ID           | Producer-assigned unique locator for one attempt                                        | Informational locator; never sufficient without `ReleaseDigest`   |
| Publication profile  | Envelope-owned producer storage, publication, and retention contract                    | Versioned profile identifier                                      |
| Publication evidence | Provider-confirmed successful creation instant and identity for the exact envelope      | Locked `publishedAt`, version, and/or ETag evidence               |
| Ingress profile      | Request-owned destination acquisition, decode, and materialization contract             | Versioned profile identifier                                      |
| Acquisition mode     | `guaranteed` before retention expiry or explicitly availability-dependent `best-effort` | Locked delivery-request field                                     |
| Artifact             | Exact dbt JSON file referenced by the release                                           | URI plus SHA-256                                                  |
| Output               | Exact producer data for one output-selected dbt model and candidate-build input         | Storage alias, URI, size, SHA-256, row count, and schema digest   |
| Local model ID       | Stable Project-local ID assigned to a generated Model                                   | `(ProjectUID, localModelId)` after activation                     |
| Object identity      | Provider evidence such as Azure Blob version ID and ETag                                | Optional provider-specific evidence; not a substitute for SHA-256 |
| Schema digest        | SHA-256 of the normalized physical schema projection                                    | `SchemaDigest`                                                    |
| Storage alias        | Portable producer label for one release-object location authority                       | Resolved only by the delivery request                             |
| Decode budget        | Destination limits for safe Parquet inspection and materialization                      | Locked target policy revision and values                          |
| Release lock         | Exact release, profiles, artifacts, outputs, and resolved target inputs used by a plan  | `ReleaseLockDigest`                                               |
| Verified staging     | Target-owned bounded bytes acquired and hashed once before candidate materialization    | Size and SHA-256 equal to the ReleaseLock                         |
| Serving state        | Materialized tables and files named by the sealed candidate catalog                     | Sealed catalog digest and physical-pool closure                   |

`ReleaseID`, repository URL, commit SHA, workflow run, dbt unique ID, relation
name, URI, ETag, and Blob version ID are provenance or location evidence. None
replaces LeapView Project UID, resource UID, generation ID, ReleaseDigest, or
the physical content digest.

## Ownership boundaries

| Concern                                                                                        | Owner                                          |
| ---------------------------------------------------------------------------------------------- | ---------------------------------------------- |
| dbt source access, packages, macros, compilation, execution, tests, and intermediate database  | Release producer                               |
| Immutable Parquet output and dbt artifact publication                                          | Release producer                               |
| Release availability through declared `retainUntil`                                            | Release producer                               |
| Project selection, envelope transport, and storage-alias mapping                               | LeapView deployment caller and control plane   |
| Credential material and endpoint resolution                                                    | Target-owned LeapView connection binding       |
| Release parsing, verified staging, correlation, generated resources, and schema reconciliation | LeapView importer and compiler                 |
| Target-local dbt output materialization and sealed physical state                              | LeapView candidate lifecycle and physical pool |
| SemanticModel, semantic access policy, dashboard, and governed queries                         | LeapView                                       |
| Candidate qualification, active pointer, rollback, leases, and audit                           | LeapView deployment lifecycle                  |

The release envelope and dbt artifacts must never contain LeapView connection
binding names, connection strings, account keys, SAS tokens, access tokens,
private keys, GitHub tokens, or other credential material. Portable storage
aliases and non-secret provider object locators are allowed.

## Canonical release envelope

The producer writes UTF-8 `release.json` matching generated strict JSON Schema.
Unknown properties, duplicate keys, invalid Unicode, non-integer JSON numbers,
and values outside the public bounds are rejected. The envelope is normalized
and serialized with RFC 8785 JSON Canonicalization Scheme. `ReleaseDigest` is
`sha256:` plus lowercase hexadecimal SHA-256 over those canonical bytes.

The delivery request supplies both an exact release-envelope URI and expected
ReleaseDigest. A provider object version may additionally pin the envelope. A
mutable discovery channel must resolve to these exact values before planning and
must not appear in a candidate or generation lock.

The normative shape is represented by this example; generated TypeSpec and JSON
Schema become the executable authority:

```json
{
  "profile": "leapview.dbt-release/v1",
  "producerProfile": "leapview.dbt-duckdb-parquet/v1",
  "publicationProfile": "leapview.dbt-duckdb-parquet-azure-publication/v1",
  "schemaProfile": "leapview.parquet-schema/v1",
  "releaseId": "4f2d91a-184322-1",
  "retainUntil": "2026-10-02T12:34:56Z",
  "provenanceAuthentication": "unverified-producer-assertion",
  "producer": {
    "repository": "https://github.com/acme/analytics",
    "commit": "4f2d91a09e2d2345678901234567890123456789",
    "workflow": "publish-analytics",
    "runId": "184322",
    "runAttempt": 1,
    "createdAt": "2026-09-02T12:34:56Z"
  },
  "toolchain": {
    "executionEngine": "dbt-core-python",
    "dbtCoreVersion": "1.x.y",
    "dbtDuckdbVersion": "1.x.y",
    "duckdbVersion": "1.x.y",
    "exporterVersion": "1.x.y",
    "azureExtensionVersion": "1.x.y",
    "azureLoginVersion": "2.x.y",
    "publisherVersion": "azure-sdk/x.y.z"
  },
  "dbt": {
    "projectName": "jaffle_shop",
    "adapterType": "duckdb",
    "invocationId": "3d12d6e8-6d49-4fcb-984c-037125f1fd3e",
    "command": "build",
    "buildSelection": {
      "select": ["+tag:leapview"],
      "exclude": [],
      "indirectSelection": "eager",
      "state": null,
      "defer": false,
      "favorState": false
    },
    "outputSelection": {
      "select": ["tag:leapview"],
      "exclude": []
    },
    "exitCode": 0
  },
  "artifacts": {
    "manifest": {
      "storageAlias": "release_objects",
      "uri": "az://analytics/releases/jaffle_shop/4f2d91a-184322-1/dbt/manifest.json",
      "sizeBytes": 125849,
      "sha256": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    },
    "runResults": {
      "storageAlias": "release_objects",
      "uri": "az://analytics/releases/jaffle_shop/4f2d91a-184322-1/dbt/run_results.json",
      "sizeBytes": 12584,
      "sha256": "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
    }
  },
  "outputs": [
    {
      "dbtUniqueId": "model.jaffle_shop.orders",
      "localModelId": "orders",
      "location": {
        "storageAlias": "release_objects",
        "uri": "az://analytics/releases/jaffle_shop/4f2d91a-184322-1/models/orders.parquet",
        "sizeBytes": 385042,
        "sha256": "sha256:89abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
        "etag": "0x8DC000000000000",
        "versionId": "2026-09-02T12:34:51.0000000Z"
      },
      "rowCount": 428,
      "schema": [
        {
          "name": "order_id",
          "nullable": false,
          "type": {
            "kind": "integer",
            "signed": true,
            "bitWidth": 64
          }
        }
      ],
      "schemaDigest": "sha256:456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123"
    }
  ]
}
```

Example version strings are illustrative, not support declarations. The
implementation publishes an explicit dbt artifact-schema allowlist and exact
tested execution-engine, dbt Core, dbt-duckdb, DuckDB, exporter, Azure
extension, and publisher tuple. Producer releases record the applicable tuple;
version ranges or an unqualified `latest` are not support declarations.

## Envelope bounds and provenance

- **ENV-01:** `profile` must equal `leapview.dbt-release/v1` and
  `producerProfile`, `publicationProfile`, and `schemaProfile` must name
  supported qualified profiles. The envelope must not select an ingress
  profile; that is a destination decision in the authenticated delivery request.
  A publication-profile claim selects validation rules but does not by itself
  authenticate publisher identity or producer provenance.
- **ENV-02:** `releaseId` is 1–160 visible canonical characters, is unique within
  the producer repository and publication scope, and denotes a create-only
  prefix. A retry uses a new attempt identity.
- **ENV-03:** Repository, exact immutable VCS commit, workflow run, attempt, and
  UTC creation timestamp are required. The initial GitHub profile accepts a
  40–64 character lowercase hexadecimal commit identifier. A dirty worktree or
  moving branch name is not valid commit provenance.
- **ENV-04:** The envelope contains one toolchain object, one dbt object,
  exactly two required artifacts, and 1–1,000 outputs. Public byte, nesting,
  string, integer, and collection bounds are enforced before allocation
  proportional to untrusted input.
- **ENV-05:** Outputs are sorted by `dbtUniqueId` in canonical bytes. Duplicate
  dbt IDs, local IDs, storage-alias/URI pairs, or canonical physical object
  identities are rejected.
- **ENV-06:** The expected ReleaseDigest is supplied out of band by the delivery
  request. A digest written only inside the envelope cannot authenticate itself.
- **ENV-07:** Provenance is recorded in the plan, candidate, generation, lineage,
  and authorization-filtered audit projection. It is never used as a mutable
  resolver.
- **ENV-08:** Reusing a release URI, ID, or provider object identity with
  different bytes is a conflict. LeapView never accepts last-writer-wins repair.
- **ENV-09:** Base profile v1 requires
  `provenanceAuthentication: unverified-producer-assertion`. Repository, commit,
  workflow, run, and toolchain claims are audit provenance only. They must not
  grant authorization or satisfy an admission rule requiring authenticated
  provenance.
- **ENV-10:** The authenticated and authorized delivery principal that supplies
  the expected ReleaseDigest is recorded as the release-byte admission
  authority. Azure OIDC publisher identity proves permission to create storage
  objects but does not authenticate the envelope's repository, commit,
  workflow, or toolchain claims.
- **ENV-11:** A policy that relies on those claims requires a separately
  admitted signed-attestation profile binding the ReleaseDigest to the claims
  and a configured verifier trust root. An absent, invalid, or untrusted
  attestation fails that policy; base v1 does not infer authenticity.
- **ENV-12:** `retainUntil` is required, is later than `producer.createdAt`, is
  an RFC 3339 UTC timestamp with whole-second precision, and is part of
  ReleaseDigest. The Azure publication profile requires provider evidence that
  the successfully committed envelope became readable at `publishedAt` and
  requires `retainUntil` to be at least 30 complete days after that observed
  instant. A producer-authored clock or `createdAt` cannot shorten the
  guarantee.

## Compatibility profile

- **CMP-01:** Producer profile v1 admits the classic Python `dbt-core` execution
  engine only. dbt Fusion or another engine requires a separately named and
  qualified producer profile even when it emits artifacts with familiar names.
- **CMP-02:** LeapView publishes exact tested tuples containing execution
  engine, dbt Core, dbt-duckdb, DuckDB, exporter, artifact schema URIs, and, for
  the Azure publication and ingress profiles, Azure extension, publisher,
  connector, and Parquet decoder versions.
- **CMP-03:** The release records one exact tuple. Versions outside a published
  tuple fail closed; compatible-looking semantic versions are not admitted by
  inference.
- **CMP-04:** Artifact schema URI selects the parser and status vocabulary. The
  runtime version is separately correlated with artifact metadata and never
  substitutes for the schema URI.
- **CMP-05:** A tuple upgrade must pass artifact golden files, non-empty and
  zero-row export fixtures, Azure conditional-publication tests, verified
  ingress, schema reconciliation, candidate seal, and rollback before support is
  published.
- **CMP-06:** Local development and production use a tuple with the same dbt and
  export semantics. Storage-alias mapping and authentication mechanism may
  differ without changing the base release profile.
- **CMP-07:** LeapView publishes an explicit compatibility matrix over base,
  producer, publication, schema, and ingress profiles. Planning rejects an
  unknown profile or unqualified publication/ingress pair; neither side infers
  compatibility from URI scheme, provider name, or similar version strings.

## dbt artifact acceptance

- **ART-01:** The manifest and run-results descriptors contain portable storage
  aliases and name exact JSON objects, not LeapView bindings, directories,
  globs, query results, symlinks, or mutable channel aliases.
- **ART-02:** LeapView verifies descriptor byte size and SHA-256 before parsing.
  The target connection must not transparently substitute another object.
- **ART-03:** `metadata.dbt_schema_version` in each artifact must match an
  explicit supported artifact-schema URI. dbt product version alone never
  selects a parser.
- **ART-04:** Manifest and run results must report the same non-empty
  `metadata.invocation_id`, equal to `dbt.invocationId` in the release.
- **ART-05:** The manifest's project name, adapter type, and dbt version must
  equal the release and admitted-toolchain claims. Every output `dbtUniqueId`
  must identify one enabled manifest node of resource type `model` owned by the
  root project or an explicitly admitted dependency package.
- **ART-06:** Each output node must appear exactly once in `run_results.results`
  with the model-success status admitted by the artifact and runtime tuple. For
  the initial tuple that status is `success`; any other value, including a
  missing, error, fail, skipped, or runtime-error result, rejects the release.
- **ART-07:** The producer profile uses one `dbt build` invocation. The release
  records exit code zero. The initial tuple accepts `pass` for a required test
  and may accept `warn` only when Project deployment policy explicitly allows
  warnings. Error, fail, skipped, unknown, or reused results reject the release;
  warnings remain visible evidence. Exit code zero is necessary but never
  substitutes for inspecting every result status.
- **ART-08:** `dbt.buildSelection` is required and canonical. Producer profile
  v1 requires a non-empty `select` array, an empty `exclude` array,
  `indirectSelection: eager`, `state: null`, `defer: false`, and
  `favorState: false`. The build selection may use admitted parent graph
  operators so a fresh target builds upstream models needed by the outputs.
  Named selectors, CLI or environment overrides, result reuse, retry artifacts,
  and any other stateful selection input are rejected. The profile publishes the
  admitted selector-method and graph-operator grammar and pins its evaluation to
  the admitted dbt tuple; an expression outside that grammar fails closed.
- **ART-09:** `dbt.outputSelection` is separately required and canonical. It has
  a non-empty `select` array and empty `exclude` array and uses an admitted
  selector grammar without graph expansion, named selectors, state, or indirect
  selection. LeapView evaluates it against the same manifest. Its selected set
  must contain only enabled relation-producing models, must be a subset of the
  successfully built model set, and must equal the envelope output IDs exactly.
  Build-selected ancestor models may remain internal and must not become
  generated LeapView Models merely because dbt needed to execute them.
- **ART-10:** LeapView evaluates the build selection against the admitted
  manifest and derives the exact admitted executable and required-test sets
  under the pinned dbt tuple. The v1 build graph admits relation-producing
  models and their test nodes; ephemeral dependencies may be compiled into
  downstream nodes but cannot be outputs or independently claimed as physical
  results. Direct selection of a seed, snapshot, saved query, analysis,
  exposure, metric, semantic model, or unsupported executable kind rejects the
  release. Zero tests is valid only when the derived test set is empty.
- **ART-11:** LeapView normalizes the pinned tuple's `run_results.args` and
  compares `which` or command, `select`, `exclude`, `indirect_selection`,
  `state`, `defer`, and `favor_state` to `dbt.command` and the locked build
  selection. Normalization handles only the tuple's documented absent/null and
  default encodings. Any selector, named-selector, indirect-selection, state,
  defer, favor-state, or command mismatch rejects the release even when the
  manifest-derived result set happens to look compatible.
- **ART-12:** Every derived required model and test result must appear exactly
  as required by the pinned tuple in the same invocation. Every entry actually
  present in `run_results.results`, including additional tests and operations,
  is correlated to the manifest and its status is validated explicitly.
  Missing, duplicate, skipped, unknown, or disallowed results reject the
  release; no result is accepted merely because the process exited zero.
- **ART-13:** Producer profile v1 rejects every root-project or dependency
  `on-run-end` hook. LeapView identifies the hook's `operation.*` manifest node
  using the pinned artifact representation and rejects both the declaration and
  any corresponding run result. If the parser cannot distinguish hook phase it
  fails closed. This ensures no hook can mutate a tested relation after its test
  result but before export. Admitted `on-run-start` operations must have one
  explicitly acceptable result under ART-12.
- **ART-14:** Manifest checksums, compiled SQL, database/schema/alias, contracts,
  columns, descriptions, tags, owners, dependencies, and test edges are retained
  as bounded provenance. They do not authorize SQL execution inside LeapView.
- **ART-15:** The parser ignores no security-relevant field because it is
  unknown. Unknown artifact schema versions and unsupported node variants fail
  with a version error and upgrade guidance.

## Producer export acceptance

- **EXP-01:** Export begins only after the one recorded `dbt build` invocation
  exits zero, artifact acceptance admits every required build result, and
  ART-13 proves that no `on-run-end` hook ran or could run. Export does not
  change those dbt artifacts or claim to be part of their invocation.
- **EXP-02:** The exporter resolves each output-selected node to the relation produced
  by that invocation using the admitted adapter and manifest metadata. It never
  recompiles model SQL, follows a mutable selector, or substitutes a current
  relation from another target.
- **EXP-03:** Each output-selected relation is exported as one complete local Parquet
  object. The exporter records its exact row count and normalized schema before
  publication. Zero is a valid row count.
- **EXP-04:** The Parquet row set must equal the dbt relation row set. A zero-row
  relation produces a readable zero-row Parquet object carrying its physical
  schema; sentinel, synthetic all-NULL, or otherwise filtered records are
  forbidden.
- **EXP-05:** The exporter reopens the completed local Parquet object and proves
  logical-value equality before it can be uploaded. Producer profile v1 opens
  the source relation and `read_parquet(...)` in the same pinned DuckDB process,
  projects columns in canonical schema order without coercion, and runs
  duplicate-preserving multiset differences in both directions using
  `EXCEPT ALL`. Both differences must be empty. It then proves row-count and
  SchemaDigest equality and records size and SHA-256 over the exact local bytes.
- **EXP-06:** dbt-duckdb's stock `external` materialization is not qualified for
  producer profile v1 because its empty-relation behavior can write a synthetic
  all-NULL row that is hidden only by a DuckDB view. It may be admitted by a
  later tuple only if the stored Parquet itself passes EXP-03 through EXP-05.
- **EXP-07:** Incremental export, append, merge, partition mutation, and
  direct-to-final remote writes are outside producer profile v1. A later tuple
  may admit a direct remote writer only after proving equivalent zero-row,
  exclusive-create, partial-failure, hashing, and schema behavior.
- **EXP-08:** Parquet byte serialization need not be reproducible across two
  independent releases. Within one committed release, the exact byte digest,
  row count, and SchemaDigest are immutable and authoritative input evidence.
- **EXP-09:** A type whose equality semantics cannot be preserved by the pinned
  comparison tuple is unsupported rather than omitted or string-coerced. Golden
  fixtures cover duplicates, NULLs, decimals, every admitted timestamp mode,
  floating-point NaNs, and admitted nested values; tuple qualification records
  the exact equality behavior used for NaN and nested comparison.

## Canonical schema profile

- **SCH-01:** `leapview.parquet-schema/v1` identifies one immutable canonical
  schema type algebra and Parquet mapping. The digest input is the RFC 8785
  canonical JSON object `{"profile":"leapview.parquet-schema/v1","fields":[...]}`;
  hashing only the field array or omitting the profile is invalid.
- **SCH-02:** Each field contains `name`, `nullable`, and `type`. Names are exact
  NFC-normalized, case-sensitive UTF-8; empty names and duplicate names within
  one struct scope are rejected. Field and nested-struct order is preserved and
  participates in SchemaDigest.
- **SCH-03:** Primitive `type.kind` values are `boolean`, `integer`, `float`,
  `decimal`, `string`, `binary`, `date`, `time`, and `timestamp`. Integer adds
  `signed` and `bitWidth` in `8|16|32|64`; float adds `bitWidth` in `32|64`;
  decimal adds positive `precision` not greater than 38 and `scale` from zero
  through precision. No integer width or decimal scale is inferred.
- **SCH-04:** `time` and `timestamp` add an exact `unit` from
  `millisecond|microsecond|nanosecond` and an `adjustedToUtc` Boolean;
  `timezone` is `UTC` only when adjusted and otherwise `null`. These values map
  the Parquet logical annotation without session-zone conversion. The profile
  rejects local-zone names, legacy INT96 timestamps, unannotated temporal
  integers, and an annotation whose UTC-adjustment semantics cannot be
  preserved.
- **SCH-05:** Nested `type.kind` values are `list`, `map`, and `struct`. A list
  contains one `element` with `nullable` and recursive `type`; a map contains a
  non-null `key` type and a `value` with `nullable` and recursive `type`; a
  struct contains ordered recursive `fields`. Only standard Parquet logical
  LIST and MAP encodings are admitted; ambiguous legacy encodings fail closed.
- **SCH-06:** Parquet logical annotations are authoritative: STRING/UTF8 maps
  to `string`; unannotated BYTE_ARRAY/FIXED_LEN_BYTE_ARRAY maps to `binary`;
  DATE, TIME, TIMESTAMP, INTEGER, DECIMAL, LIST, and MAP map only when every
  required parameter is present and within the algebra. Unannotated INT32 and
  INT64 map to signed 32- and 64-bit integers respectively; an INTEGER
  annotation supplies its exact sign and width. BOOLEAN, FLOAT, and DOUBLE map
  to boolean and exact 32- and 64-bit float variants. Unsupported annotations
  including UUID, JSON, BSON, ENUM, INTERVAL, and FLOAT16, and unsupported
  physical types including INT96, fail v1 rather than being silently coerced.
- **SCH-07:** Parquet REQUIRED/OPTIONAL repetition defines field and element
  nullability. REPEATED is accepted only through the admitted LIST or MAP
  encoding. Nullability that is unavailable from a relation description is
  checked against the completed Parquet and the enforced dbt contract; a
  disagreement fails export or qualification.
- **SCH-08:** The pinned producer and importer share golden schema fixtures for
  every primitive, decimal boundary, timestamp unit and adjustment mode, nested
  list/map/struct, field order, name case, and nullability. Any DuckDB or Parquet
  library upgrade that changes normalization requires a new tested tuple; a
  digest-affecting mapping change requires a new schema profile.

## Physical output acceptance

- **OUT-01:** Each output has exactly one `location` in producer profile v1. Its
  URI names one `.parquet` object and contains no wildcard, directory-only key,
  `latest` segment used as a channel, or provider query that changes identity.
- **OUT-02:** `storageAlias` is a portable producer-defined label, not a
  LeapView connection binding. The delivery request supplies one destination
  mapping from every used alias to an authorized target-owned connection
  binding. An alias is resolved only through that mapping; no envelope value,
  URI scheme, default connection, or envelope-transport binding can override
  it.
- **OUT-03:** `sizeBytes`, SHA-256, `rowCount`, normalized `schema`, and schema
  digest are required. Row count is a bounded non-negative integer. ETag and
  provider version ID are recorded when available. ETag is never interpreted as
  a cryptographic digest unless a future provider profile proves that property.
- **OUT-04:** Planning requires an exact, duplicate-free mapping for all and
  only the storage aliases used by the envelope. It resolves and authorizes each
  mapped binding and records the alias, target binding identity, and revision in
  the ReleaseLock. The acquisition phase opens the remote object once through
  that resolved mapping and streams it into target-owned bounded staging while
  computing size and SHA-256. Acquisition occurs during candidate construction
  in guaranteed mode and before plan success in best-effort mode. A provider
  version or conditional read is used when the admitted connector supports it.
- **OUT-05:** `schemaProfile`, normalized `schema`, and SchemaDigest follow
  SCH-01 through SCH-08 exactly. Planning may use this declared schema for graph
  compilation, but candidate verification remains authoritative for
  qualification.
- **OUT-06:** Only after staged size and SHA-256 equal the ReleaseLock does the
  importer open the staged Parquet bytes. Its row count and observed
  SchemaDigest must equal the release values. LeapView reconciles the observed
  physical schema with an enforced dbt contract when present. Unsupported or
  lossy type mappings fail before candidate activation.
- **OUT-07:** A manifest relation name is provenance only. The generated Source
  uses the exact staged release input during candidate construction and must not
  re-resolve the manifest's database, schema, alias, or current warehouse
  relation. Runtime queries use the candidate materialization, not that Source
  location.
- **OUT-08:** Output creation is full replacement. Incremental external models,
  append-to-prefix behavior, mutable partition discovery, deletes, and partial
  replacement are outside producer profile v1.
- **OUT-09:** The producer guarantees that every committed release object
  remains readable and unchanged through `retainUntil`. The guarantee starts at
  the provider-observed successful envelope publication instant required by the
  publication profile. Missing or changed objects fail planning or candidate
  construction without changing the active generation; there is no fallback to
  a newer release or current relation.
- **OUT-10:** Candidate construction materializes the verified Parquet into the
  private DuckLake catalog as full replacement. After seal, the catalog and its
  physical-pool closure are the only serving, lease, publication, and rollback
  authority. Producer release objects are not generation or query-lease roots.
- **OUT-11:** Every ingress profile defines finite hard limits for compressed
  bytes per object and release, declared and observed rows, columns, nesting
  depth, row groups, pages, individual encoded and decoded value length,
  decoded logical bytes, compression or expansion ratio, memory, temporary
  disk, CPU time, and wall time. The target policy may lower those limits. The
  exact effective decode budget and policy revision are locked before any
  untrusted allocation proportional to object metadata or contents.
- **OUT-12:** Footer inspection, schema normalization, row counting, decoding,
  and target materialization run under the locked decode budget and a resource
  governor. The importer validates bounded footer and structural metadata before
  allocating per-column, per-page, or per-value state. Exceeding any limit,
  timeout, cancellation, decompression bound, memory limit, or temporary-disk
  limit fails the plan or candidate, cleans bounded staging according to policy,
  and leaves the active generation unchanged.

## Generated Project-local resources

- **MAP-01:** Each accepted output lowers to one generated Source and one
  generated thin Model in the selected Project candidate. They pass through the
  same compiler, identity registry, contract, lineage, and activation checks as
  authored resources.
- **MAP-02:** `localModelId` must satisfy the public resource-ID grammar and be
  stable in repository configuration. It becomes the generated Model's authored
  ID. The generated Source ID is `dbt-source:<localModelId>`; profile validation
  applies the final public length bound after prefixing.
- **MAP-03:** The Model depends only on its generated Source and represents no
  additional dbt transformation SQL. The Source contains the locked release
  input and observed physical fields; candidate construction materializes its
  verified rows into a target-local DuckLake table. The sealed Model never
  delegates serving reads to the producer location.
- **MAP-04:** The default adapter derives `localModelId` from an explicit
  `meta.leapview.id` when present, otherwise from the dbt node name after strict
  validation. It does not silently normalize punctuation, truncate, or resolve
  a collision. The release records the resulting ID.
- **MAP-05:** Any collision with an authored or generated resource ID, existing
  kind binding, tombstone, or another output fails planning. A caller cannot use
  display names or file paths to retarget an existing UID.
- **MAP-06:** Generated resources record dbt unique ID, package, node checksum,
  contract, selected descriptive metadata, invocation ID, ReleaseDigest,
  artifact digests, output digest, SchemaDigest, and provider object identity as
  provenance outside portable semantic canonical bytes except where the
  governing data-contract profile includes a field.
- **MAP-07:** Authored SemanticModels and Dashboards reference the generated
  Model by `localModelId` exactly as they reference an authored Model. No
  dbt-qualified foreign edge enters the compiled graph.
- **MAP-08:** Removing or renaming an output follows ADR-0016/ADR-0018 resource
  identity and tombstone rules. A dbt rename does not implicitly retarget the old
  local ID; an intentional migration is explicit and reviewable.
- **MAP-09:** Generated Source and Model definitions appear in plan diff,
  lineage, Develop inspection, contract evidence, and audit projections with
  their generated origin clearly distinguished from authored resources.
- **MAP-10:** The producer dbt project may contain package or dbt Mesh
  dependencies. All selected outputs must be materialized and evidenced by the
  one consumer-project release; upstream projects remain provenance and never
  become live LeapView Project dependencies.
- **MAP-11:** Profile v1 accepts exactly one committed dbt release per LeapView
  deployment plan. A request containing independently published releases must
  fail before resource mapping. A future multi-release profile must define one
  canonical release-set lock and deterministic resource identity and collision
  behavior.
- **MAP-12:** Every SemanticModel dataset resolves to an authored or generated
  Model in the same Project candidate and generation. Neither a dbt
  project-qualified node ID nor upstream lineage may be used as a runtime
  foreign reference.

## Repository layout

The reference repository keeps each tool's conventional source beneath a
separate root so dbt's `models/` directory is never confused with an optional
LeapView `models/` directory:

```text
analytics/
├── dbt_project.yml
├── models/                       # dbt SQL, sources, tests, and contracts
└── leapview/                     # LeapView portable source root
    ├── connections/              # logical, credential-free bindings
    ├── semantic-models/
    └── dashboards/
```

`target/` and the release envelope are generated evidence, not authored source
and not committed as the current deployment head. An authored LeapView
`models/` directory is added only for transformations LeapView owns. The dbt
project name may seed a Project slug during initial control-plane creation, but
it is not Project UID and a later rename cannot retarget resource identity.

## Release planning and activation

- **DEP-01:** The delivery request binds an envelope-transport connection, exact
  release URI, expected ReleaseDigest, request-owned `ingressProfile`,
  `acquisitionMode: guaranteed|best-effort`, and exact
  `storageAlias`-to-connection mapping to one Project UID, target, environment,
  portable LeapView bundle digest, and every referenced connection-binding
  revision. The transport connection fetches only `release.json` unless it is
  also named explicitly in the alias mapping; it is never an implicit
  data-object authority.
- **DEP-02:** Planning acquires the bounded envelope and dbt artifacts into
  content-addressed plan evidence while verifying their digests, then performs
  artifact parsing and correlation, resource mapping, connection authorization,
  declared-schema contract comparison, and complete graph compilation. It does
  not claim that large Parquet outputs have been physically verified in
  `guaranteed` mode. In `best-effort` mode, a successful plan additionally
  acquires and verifies every output into retained target-owned staging, so a
  reviewed plan never promises later availability that the producer no longer
  guarantees.
- **DEP-03:** The canonical ReleaseLock contains ReleaseDigest, artifact digests,
  base, producer, publication, schema, and ingress profiles, canonical build and
  output selections, normalized invocation arguments, derived executable and
  required-test IDs, generated IDs, exact expected object evidence, row counts,
  declared schemas, SchemaDigests, the exact alias-to-target-binding map with
  target IDs and revisions, acquisition mode and deadline or verified-staging
  evidence, provider-observed envelope publication evidence, effective decode
  budget and policy revision, `retainUntil`, Project UID, and environment. Its
  digest enters plan review and generation evidence.
- **DEP-04:** A retry with the same idempotency key and identical canonical
  inputs returns the prior result. Any release, target, source-bundle, policy,
  alias mapping, or binding-revision drift conflicts and requires a new plan.
- **DEP-05:** Candidate construction and qualification use only the ReleaseLock.
  For every output the importer performs OUT-04 through OUT-12: one remote
  acquisition into verified staging followed by target-local materialization,
  or materialization from the exact retained staging already locked by a
  best-effort plan. It never repeats channel, branch, selector, dbt relation, or
  prefix resolution and never reacquires an object whose staged identity is
  already locked.
- **DEP-06:** Successful dbt publication does not move a LeapView active pointer.
  Activation occurs only through ADR-0007's authorized plan, candidate,
  qualification, and publication lifecycle.
- **DEP-07:** The same release may be bound to another environment and qualified
  again. The destination independently verifies and materializes the same exact
  release; it does not follow an unverified mutable location or rerun dbt.
- **DEP-08:** Rollback restores the prior LeapView generation and sealed catalog.
  Its ReleaseLock remains provenance, but rollback neither invokes dbt, resolves
  a producer head, nor reads release objects.
- **DEP-09:** In `guaranteed` mode, planning requires current time before
  `retainUntil` and records a future acquisition deadline no later than
  `retainUntil`. A pending plan, retry, promotion, or rebuild is guaranteed only
  through that deadline; earlier deletion or mutation is a producer-contract
  violation and candidate-input availability fault. Planning rejects guaranteed
  mode when no such future window remains.
- **DEP-10:** In `best-effort` mode, planning may begin before or after
  `retainUntil` but makes no producer-availability claim. Plan success requires
  immediate exact acquisition and verification of all release outputs under the
  locked ingress profile and decode budget. Missing, changed, or over-budget
  objects fail the plan without changing the active generation. Candidate
  construction uses the locked staging and does not reopen producer storage.
- **DEP-11:** Legal holds require a separately coordinated storage retention
  policy. After candidate seal, producer deletion does not change the candidate
  or generation; catalog and physical-pool retention follow ADR-0009.

## Producer transaction

The producer profile treats release creation as a create-only transaction:

1. Compute a globally unique release prefix from exact commit, workflow run,
   and attempt before dbt execution.
2. Refuse to reuse a prefix containing any committed release envelope.
3. Run one pinned dbt Core and adapter toolchain with a fresh target directory
   and ephemeral DuckDB intermediate database.
4. Use the canonical build selection in ART-08 and run `dbt build` so required
   ancestors, output models, and tests share one run-results artifact.
5. Evaluate the separate output selection, derive the required executable and
   test IDs from the generated manifest, correlate normalized invocation
   arguments, prove that every actual result is admitted, and prove that no
   `on-run-end` hook is declared or executed. Stop on non-zero exit or any
   missing, duplicate, or disallowed status. Partial output is not a release.
6. Run the qualified exporter against each output-selected relation and write
   one local temporary Parquet object. Reopen it, prove duplicate-preserving
   multiset equality in both directions, and verify exact row count, schema,
   size, and SHA-256, including the zero-row case. Internal ancestor relations
   need not be exported or survive the job after export completes.
7. Conditionally create each final Azure output from the verified local bytes.
   The admitted publisher uses provider create-only preconditions and never
   truncates, repairs, or overwrites an existing key.
8. Conditionally create the exact manifest and run-results artifacts from that
   invocation, recording their digests, sizes, and returned provider evidence.
9. Generate the canonical envelope with a conservative `retainUntil` intended
   to satisfy the publication profile and conditionally create `release.json`
   last.
10. Read back or conditionally inspect the committed objects as required by the
    admitted publisher tuple, obtain provider-confirmed envelope `publishedAt`
    evidence, and prove `retainUntil` is at least 30 complete days later. Submit
    the exact envelope URI and digest, ingress profile, acquisition mode, and
    provider evidence to LeapView deployment automation only after that check.

Abandoned prefixes without a valid `release.json` may be garbage-collected after
a configured grace period. A committed prefix and its objects are never
completed, repaired, or replaced in place.

## Azure and GitHub Actions production profile

The reference production showcase uses immutable source data in Azure Blob or
ADLS, GitHub Actions as the producer, dbt-duckdb as the adapter, Parquet release
outputs, a qualified create-only Azure publisher, and LeapView's native Azure
connection as the ingress transport. The ordinary direct-read Azure Source is
not the serving relation and does not by itself satisfy this profile's pinned
identity contract.

`leapview.dbt-duckdb-parquet-azure-publication/v1` owns producer authentication,
create-only publication, provider evidence, and retention obligations below.
`leapview.azure-parquet-ingress/v1` owns destination connection scope,
single-stream verified staging, decode budgets, and materialization. Requirements
that compare both sides qualify their compatibility pair; neither profile may
silently impose fields or credentials on the other.

### Storage layout

```text
az://analytics/
├── sources/jaffle_shop/v1/
│   ├── customers.parquet
│   └── orders.parquet
└── releases/jaffle_shop/<commit>-<run>-<attempt>/
    ├── models/
    │   ├── customers.parquet
    │   └── orders.parquet
    ├── dbt/
    │   ├── manifest.json
    │   └── run_results.json
    └── release.json
```

The source fixture prefix is immutable for the showcase. The workflow records
its exact source object inventory, versions or ETags, and digests as producer
provenance even though LeapView serving does not depend on the raw inputs. A
real upstream system may supply an equivalent immutable dataset revision.

### Authentication and authorization

- **AZR-01:** GitHub Actions authenticates to Azure through GitHub OIDC and an
  Azure federated workload identity using `azure/login`. The workflow declares
  only the required `id-token: write` and repository-content permissions, and
  the federated credential subject and audience exactly match the protected
  workflow environment.
- **AZR-02:** The producer identity has data-reader access to the source
  container and the narrowest available data-write access scoped to the release
  container. The workflow still enforces provider conditional create on every
  committed key. It has no LeapView control-plane administrator role.
- **AZR-03:** DuckDB uses the Azure extension and a bounded Azure credential
  chain explicitly configured for the Azure CLI context established by
  `azure/login`. Other implicit workload-identity chains are not part of the
  initial tuple. Static storage keys and SAS tokens are absent from dbt profiles
  and logs.
- **AZR-04:** LeapView uses a separate target-owned managed or workload identity
  with read-only access to committed release objects for envelope, artifact, and
  candidate-input acquisition. It does not receive the producer identity or
  access to raw source data unless separately required, and serving does not
  retain an Azure read dependency after candidate seal.
- **AZR-05:** The delivery request maps each portable storage alias in
  `release.json` to a LeapView connection binding for the non-secret Azure
  account and container endpoint. Each exact `az://`, `azure://`, or `abfss://`
  object location must be within the mapped binding's admitted scope. The
  envelope contains no LeapView binding name.
- **AZR-06:** Azure Blob version ID and ETag are recorded when available, but
  streaming SHA-256 of the exact bytes staged by LeapView remains mandatory.
  Storage-level versioning or an immutable-storage policy is recommended defense
  in depth.

### Workflow gates

- **AZR-07:** dbt, exporter, DuckDB, Azure extension, `azure/login`, and publisher
  versions are pinned and recorded. A dependency upgrade runs the complete
  producer and consumer conformance suite before changing the reference
  workflow.
- **AZR-08:** Pull requests may run dbt against an isolated attempt prefix and
  ask LeapView for a non-activating plan. Only the protected production workflow
  may commit a production release and request activation.
- **AZR-09:** GitHub Environment approval may gate LeapView production
  activation after plan review. Approval never changes the ReleaseDigest or
  rebuilds the release.
- **AZR-10:** Workflow logs and uploaded diagnostics pass secret scanning and do
  not publish Azure tokens, DuckDB secret SQL, signed URLs, or LeapView
  credentials.
- **AZR-11:** A mutable `channels/production.json` may be written after
  successful activation for discovery. LeapView generation provenance continues
  to contain the exact ReleaseDigest and input object set, while its serving
  references point only to the sealed catalog.
- **AZR-12:** The reference workflow exports Parquet locally and uploads with an
  Azure conditional-create precondition equivalent to `If-None-Match: *`.
  Preflight existence checks are advisory and never replace the atomic provider
  condition. Direct DuckDB writes to final release keys are not in the initial
  tuple.
- **AZR-13:** The release container's lifecycle and immutable-storage settings,
  together with producer cleanup permissions, must not delete or alter a
  committed object before its envelope's `retainUntil`. After conditionally
  creating the envelope, the workflow and LeapView obtain the exact object's
  provider-confirmed creation or last-modified instant as `publishedAt` evidence
  and verify that `retainUntil` is at least 30 complete days later. The showcase
  records that evidence and configuration; `producer.createdAt` is not the
  retention clock.
- **AZR-14:** The published compatibility matrix names the exact Azure
  publication/ingress pair and supported locator forms. An envelope with the
  Azure publication profile delivered under a different ingress profile, or a
  request for Azure ingress with a different publication profile, fails unless
  that exact pair has been independently qualified.

## Failure behavior

| Condition                                                                                 | Required result                                                            |
| ----------------------------------------------------------------------------------------- | -------------------------------------------------------------------------- |
| dbt build exits non-zero                                                                  | No release envelope and no LeapView deployment request                     |
| Build or output selection is absent, unsupported, or differs from the locked v1 policy    | Release rejection; do not infer what dbt built or published                |
| Normalized `run_results.args` differs from the locked build selection or command          | Release rejection despite compatible-looking results                       |
| Required model or test is missing, duplicated, fails, warns without policy, or is skipped | No committed release or plan rejection, depending on detection point       |
| Root or dependency project declares or executes an `on-run-end` hook                      | Producer failure or plan rejection before export admission                 |
| Exported Parquet contains a sentinel row or differs as a multiset from its dbt relation   | Producer failure; no output publication or release envelope                |
| Conditional create reports an existing final key                                          | Publication conflict; never overwrite, truncate, or repair that key        |
| Artifact digest, size, invocation, project, adapter, or schema version differs            | Plan rejection before generated graph construction                         |
| Selected node is missing, disabled, not a model, or lacks a successful result             | Plan rejection identifying the bounded dbt unique ID                       |
| Release URI resolves but expected digest differs                                          | Integrity conflict; never accept current bytes                             |
| Output path is a glob, directory, mutable channel, or unsupported provider                | Plan rejection                                                             |
| Output is missing, unreadable, changed, or has wrong size or SHA-256                      | Plan or candidate failure; active generation unchanged                     |
| Staged row count or physical SchemaDigest differs from release evidence                   | Producer corruption or input race; candidate failure                       |
| Parquet metadata, decode, or materialization exceeds the locked resource budget           | Plan or candidate failure; bounded staging cleanup; active unchanged       |
| Physical schema conflicts with dbt contract or LeapView type support                      | Compatibility rejection with field-level diagnostics                       |
| Storage-alias mapping is missing, extra, duplicated, or unauthorized                      | Plan rejection without credential or hidden binding disclosure             |
| Publication or ingress profile is unknown or the pair is not qualified                    | Profile compatibility rejection before object acquisition                  |
| Alias mapping or binding revision changes after planning                                  | Target-revision conflict and replan                                        |
| Generated ID collides or changes kind                                                     | Identity rejection; never retarget an existing UID                         |
| Deployment supplies several independently published releases                              | Profile rejection before resource mapping                                  |
| SemanticModel dataset carries a dbt or LeapView Project-qualified foreign reference       | Compile rejection without foreign metadata disclosure                      |
| Producer publishes a newer release                                                        | Existing plans and generations remain pinned                               |
| Release object disappears before verified acquisition                                     | Candidate-input availability failure; active generation unchanged          |
| Guaranteed acquisition is planned after `retainUntil`                                     | Plan rejection; retry only as best effort with immediate exact acquisition |
| Best-effort acquisition cannot immediately verify every exact object                      | Plan failure; no deferred availability promise and active unchanged        |
| Release object disappears after candidate seal                                            | No serving or rollback effect; retained sealed state remains authoritative |
| Mutable channel points elsewhere during retry                                             | Ignored after exact resolution; canonical-input drift conflicts            |
| Release contains secret-like fields or values                                             | Strict rejection and security audit without echoing the value              |
| Policy requires authenticated provenance but attestation is absent or untrusted           | Admission rejection; producer assertions never satisfy the policy          |

## Production showcase acceptance

The demonstration is conformant only when it proves all of the following:

- one repository contains `dbt_project.yml`, dbt SQL and tests, and LeapView
  SemanticModel and Dashboard source without `kind: Project`, handwritten dbt
  mirror Sources, or handwritten dbt mirror Models;
- source Parquet is read from an immutable Azure prefix;
- a protected GitHub Actions workflow authenticates without a long-lived Azure
  secret, runs one pinned classic dbt Core and dbt-duckdb build, exports locally,
  and conditionally creates a new immutable release;
- the recorded build selection creates upstream staging models on a fresh
  DuckDB target, the separate output selection publishes only intended models,
  normalized invocation arguments match, and omission of one required test is
  rejected;
- an `on-run-end` hook that mutates an output after tests, including a hook from
  a dependency package, is rejected before export;
- the fixture includes both a non-empty model and a valid zero-row model, and
  proves that neither producer Parquet nor the imported DuckLake table contains
  a synthetic record;
- duplicate, NULL, decimal, timestamp, NaN, and nested-value fixtures prove
  duplicate-preserving multiset equality between each tested relation and its
  exported Parquet under the pinned tuple;
- LeapView plans against its read-only native Azure connection, displays
  generated resources and provenance, acquires and hashes each output once,
  materializes it into the private DuckLake candidate, qualifies that candidate,
  and atomically activates the dashboard;
- an intentionally failing dbt test produces no committed release and leaves
  the currently served dashboard unchanged;
- an intentionally incompatible Parquet schema is rejected before activation;
- adversarial Parquet objects that exceed structural, expansion, memory,
  temporary-disk, CPU, or wall-time budgets are rejected without changing the
  active generation;
- a second successful release does not change the active generation until its
  own plan is activated;
- Azure provider evidence and lifecycle configuration prove at least 30 days
  between observed envelope publication and `retainUntil`; a post-deadline
  guaranteed plan is rejected, while best effort succeeds only after immediate
  exact acquisition;
- removing or changing Azure release objects after seal does not change the
  served generation; and
- rollback restores the first sealed LeapView generation without rerunning dbt,
  copying data, or reading Azure release objects.

Local developer documentation may run the same project with a local filesystem
binding and dbt-duckdb. Local convenience is not evidence for the Azure
production profile; both paths must generate the same base release contract.

## Evidence and conformance gates

| Requirement range | Required maintained evidence                                                                                                                            | Status  |
| ----------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------- |
| ENV-01–ENV-12     | Generated release schema, RFC 8785 golden corpus, strict/bounded parser, trust-root/attestation policy, provider-timed retention, and idempotency tests | Pending |
| CMP-01–CMP-07     | Published exact compatibility matrix, profile-pair and version rejection, and upgrade fixtures                                                          | Pending |
| ART-01–ART-15     | Build/output selection, invocation-argument and result correlation, hook rejection, required tests, malformed and version fixtures                      | Pending |
| EXP-01–EXP-09     | Non-empty and zero-row relations, value multiset equivalence fixtures, no-sentinel proof, and stock-external rejection                                  | Pending |
| SCH-01–SCH-08     | Executable canonical type algebra, Parquet mapping, schema golden corpus, and digest compatibility fixtures                                             | Pending |
| OUT-01–OUT-12     | Single-stream staging, SHA-256, row count, bounded Parquet decode, schema normalization, materialization, and post-seal independence                    | Pending |
| MAP-01–MAP-12     | Deterministic generated resources, dependency provenance, single-release enforcement, collision, lineage, and local semantic-consumer tests             | Pending |
| DEP-01–DEP-11     | Profile and mode locks, target revisions, guaranteed/best-effort ingress, candidate failure, activation, sealed rollback, lease, and GC tests           | Pending |
| AZR-01–AZR-14     | OIDC/RBAC review, credential chain, conditional create, provider-timed retention, profile compatibility, secret scan, and channel isolation             | Pending |
| Showcase          | Reproducible repository, recorded workflow evidence, failure demonstrations, activation, and rollback runbook                                           | Pending |

Implementation must add TypeSpec or an equivalent generated contract source for
the release envelope, publish supported compatibility tuples, and update the
project-delivery conformance inventory. Every normative JSON example must parse
against generated JSON Schema and have canonical golden bytes. The final
combined implementation change must pass:

```sh
task generate
task generated:check
task ci
```
