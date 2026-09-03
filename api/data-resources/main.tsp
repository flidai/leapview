import "@yacobolo/apigen";

@apigen.`package`(#{
  title: "LeapView v1 data-resource contracts",
  version: "1.0.0",
  description: "Strict authored Connection, Source, Model, and SemanticModel contracts.",
})
namespace LeapViewDataResources;

@pattern("^[A-Za-z_][A-Za-z0-9_]*$")
scalar Identifier extends string;

// Governed relationship checks always name an authored model and field explicitly.
@pattern("^[A-Za-z_][A-Za-z0-9_-]*(\\.[A-Za-z_][A-Za-z0-9_-]*)*\\.[A-Za-z_][A-Za-z0-9_]*$")
scalar ModelReference extends string;

@pattern("^[A-Za-z_][A-Za-z0-9_-]*(\\.[A-Za-z_][A-Za-z0-9_-]*)*$")
scalar ResourceName extends string;

// SemanticModel names retain the legacy control-plane grammar. In particular,
// dots are opaque name characters rather than segment separators, so values
// such as a..b, a.1, and a. remain valid here without widening other resources.
@pattern("^[A-Za-z_][A-Za-z0-9_.-]*$")
scalar SemanticModelResourceName extends string;

@pattern("^[A-Za-z0-9][A-Za-z0-9_.:-]*$")
scalar ResourceID extends string;

/**
 * Portable access is intentionally tiny. Endpoints, credentials, secret
 * providers, and resolved authentication are target-owned bindings.
 */
enum PublicAccess {
  public: "public";
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "csv", extensions: #[".csv", ".csv.gz"], scanKind: "table_function", scanFunction: "read_csv", sourceDataIdentityCapability: "unavailable", defaults: #{ header: false, delimiter: ",", quote: "\"", escape: "\"" } } })
model CSVReaderOptions {
  header?: boolean;
  delimiter?: string;
  quote?: string;
  escape?: string;
  nullString?: string;
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "json", extensions: #[".json", ".jsonl", ".ndjson"], scanKind: "table_function", scanFunction: "read_json", sourceDataIdentityCapability: "unavailable", defaults: #{ format: "auto" } } })
model JSONReaderOptions {
  format?: "auto" | "array" | "newline_delimited";
  maximumDepth?: int32;
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "parquet", extensions: #[".parquet"], scanKind: "table_function", scanFunction: "read_parquet", sourceDataIdentityCapability: "unavailable", defaults: #{ hivePartitioning: false, unionByName: false } } })
model ParquetReaderOptions {
  hivePartitioning?: boolean;
  unionByName?: boolean;
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "excel", extensions: #[".xlsx"], scanKind: "table_function", scanFunction: "read_xlsx", requiredExtension: "excel", sourceDataIdentityCapability: "unavailable", defaults: #{ header: true } } })
model ExcelReaderOptions {
  sheet?: string;
  header?: boolean;
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "text", extensions: #[".txt"], scanKind: "table_function", scanFunction: "read_text", sourceDataIdentityCapability: "unavailable", defaults: #{ delimiter: "\t", quote: "\"", header: false } } })
model TextReaderOptions {
  delimiter?: string;
  quote?: string;
  header?: boolean;
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "blob", extensions: #[".blob"], scanKind: "table_function", scanFunction: "read_blob", sourceDataIdentityCapability: "unavailable", defaults: #{ compression: "auto" } } })
model BlobReaderOptions {
  compression?: "auto" | "none" | "gzip" | "zstd";
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "vortex", extensions: #[".vortex"], scanKind: "table_function", scanFunction: "read_vortex", requiredExtension: "vortex", sourceDataIdentityCapability: "unavailable", defaults: #{} } })
model VortexReaderOptions {
  version?: string;
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "delta", extensions: #[], scanKind: "table_function", scanFunction: "delta_scan", requiredExtension: "delta", sourceDataIdentityCapability: "unavailable", tableLike: true, defaults: #{} } })
model DeltaReaderOptions {
  version?: string;
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "iceberg", extensions: #[], scanKind: "table_function", scanFunction: "iceberg_scan", requiredExtension: "iceberg", sourceDataIdentityCapability: "unavailable", tableLike: true, defaults: #{} } })
model IcebergReaderOptions {
  snapshot?: string;
}

/** Connection defaults are closed by reader format; arbitrary option maps are not public. */
model ReaderDefaults {
  csv?: CSVReaderOptions;
  json?: JSONReaderOptions;
  parquet?: ParquetReaderOptions;
  excel?: ExcelReaderOptions;
  text?: TextReaderOptions;
  blob?: BlobReaderOptions;
  vortex?: VortexReaderOptions;
  delta?: DeltaReaderOptions;
  iceberg?: IcebergReaderOptions;
}

@discriminator("type")
model ConnectionSpec {
  type: string;
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "managed",
  activationMode: "managed",
  locationCapabilities: #["path"],
  approvedExtensions: #[],
  secretType: "none",
  supportStatus: "stable",
  adapterKey: "managed",
  sourceDataIdentityCapability: "content_revision",
} })
model ManagedConnection extends ConnectionSpec {
  type: "managed";
  access?: PublicAccess;
  defaults?: ReaderDefaults;
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "s3",
  activationMode: "target_binding",
  locationCapabilities: #["path"],
  approvedExtensions: #["httpfs"],
  secretType: "s3",
  supportStatus: "stable",
  adapterKey: "s3",
  sourceDataIdentityCapability: "unavailable",
} })
model S3Connection extends ConnectionSpec {
  type: "s3";
  access?: PublicAccess;
  defaults?: ReaderDefaults;
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "r2",
  activationMode: "target_binding",
  locationCapabilities: #["path"],
  approvedExtensions: #["httpfs"],
  secretType: "r2",
  supportStatus: "stable",
  adapterKey: "r2",
  sourceDataIdentityCapability: "unavailable",
} })
model R2Connection extends ConnectionSpec {
  type: "r2";
  access?: PublicAccess;
  defaults?: ReaderDefaults;
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "gcs",
  activationMode: "target_binding",
  locationCapabilities: #["path"],
  approvedExtensions: #["httpfs"],
  secretType: "gcs",
  supportStatus: "stable",
  adapterKey: "gcs",
  sourceDataIdentityCapability: "unavailable",
} })
model GCSConnection extends ConnectionSpec {
  type: "gcs";
  access?: PublicAccess;
  defaults?: ReaderDefaults;
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "http",
  activationMode: "authored",
  locationCapabilities: #["path"],
  approvedExtensions: #["httpfs"],
  secretType: "http",
  supportStatus: "stable",
  adapterKey: "http",
  sourceDataIdentityCapability: "unavailable",
} })
model HTTPConnection extends ConnectionSpec {
  type: "http";
  access?: PublicAccess;
  defaults?: ReaderDefaults;
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "azure_blob",
  activationMode: "target_binding",
  locationCapabilities: #["path"],
  approvedExtensions: #["azure"],
  secretType: "azure",
  supportStatus: "stable",
  adapterKey: "azure_blob",
  sourceDataIdentityCapability: "unavailable",
} })
model AzureBlobConnection extends ConnectionSpec {
  type: "azure_blob";
  access?: PublicAccess;
  defaults?: ReaderDefaults;
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "postgres",
  activationMode: "target_binding",
  locationCapabilities: #["relation"],
  approvedExtensions: #["postgres"],
  secretType: "postgres",
  supportStatus: "stable",
  adapterKey: "postgres",
  sourceDataIdentityCapability: "unavailable",
} })
model PostgresConnection extends ConnectionSpec {
  type: "postgres";
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "mysql",
  activationMode: "target_binding",
  locationCapabilities: #["relation"],
  approvedExtensions: #["mysql"],
  secretType: "mysql",
  supportStatus: "stable",
  adapterKey: "mysql",
  sourceDataIdentityCapability: "unavailable",
} })
model MySQLConnection extends ConnectionSpec {
  type: "mysql";
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "sqlite",
  activationMode: "target_binding",
  locationCapabilities: #["relation"],
  approvedExtensions: #["sqlite"],
  secretType: "sqlite",
  supportStatus: "stable",
  adapterKey: "sqlite",
  sourceDataIdentityCapability: "unavailable",
} })
model SQLiteConnection extends ConnectionSpec {
  type: "sqlite";
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "ducklake",
  activationMode: "target_binding",
  locationCapabilities: #["relation"],
  approvedExtensions: #["ducklake"],
  secretType: "ducklake",
  supportStatus: "stable",
  adapterKey: "ducklake",
  sourceDataIdentityCapability: "unavailable",
} })
model DuckLakeConnection extends ConnectionSpec {
  type: "ducklake";
}

@apigen.metadata(#{ `x-leapview-connector`: #{
  key: "quack",
  activationMode: "target_binding",
  locationCapabilities: #["relation"],
  approvedExtensions: #["httpfs", "quack"],
  secretType: "quack",
  supportStatus: "experimental",
  adapterKey: "quack",
  sourceDataIdentityCapability: "unavailable",
} })
model QuackConnection extends ConnectionSpec {
  type: "quack";
}

model SourceSchemaField {
  datatype: "String" | "Integer" | "Decimal" | "Float" | "Boolean" | "Date" | "Time" | "DateTime" | "DateTimeTz" | "Opaque";
  nullable?: boolean;
  description?: string;
}

model AIContext {
  instructions?: string;
  synonyms?: string[];
  examples?: string[];
}

model InferredSourceSchema {
  mode: "inferred";
}

model CompatibleSourceSchema {
  mode: "compatible";
  @apigen.propertyNames(Identifier)
  fields: Record<SourceSchemaField>;
}

model StrictSourceSchema {
  mode: "strict";
  @apigen.propertyNames(Identifier)
  fields: Record<SourceSchemaField>;
}

@discriminated(#{ envelope: "none", discriminatorPropertyName: "mode" })
union SourceSchema {
  inferred: InferredSourceSchema,
  compatible: CompatibleSourceSchema,
  strict: StrictSourceSchema,
}

model FreshnessDuration {
  @minValue(1)
  amount: int64;
  unit: "second" | "minute" | "hour" | "day";
}

model FieldFreshness {
  basis: "field";
  field: Identifier;
  warningAfter?: FreshnessDuration;
  errorAfter?: FreshnessDuration;
}

model RevisionFreshness {
  basis: "revision";
  revision: utcDateTime;
  warningAfter?: FreshnessDuration;
  errorAfter?: FreshnessDuration;
}

@discriminated(#{ envelope: "none", discriminatorPropertyName: "basis" })
union SourceFreshness {
  field: FieldFreshness,
  revision: RevisionFreshness,
}

// Path location is a closed union over reader formats. Each variant carries
// only the options accepted by that reader; a cross-format options bag is not
// part of the authoring contract. The outer location discriminator is modeled
// through inheritance so the scalar ADR shape (format plus sibling options)
// remains intact.
@discriminator("format")
model PathSourceLocation {
  type: "path";
  path: string;
  format: string;
}

model CSVPathSourceLocation extends PathSourceLocation {
  format: "csv";
  options?: CSVReaderOptions;
}

model JSONPathSourceLocation extends PathSourceLocation {
  format: "json";
  options?: JSONReaderOptions;
}

model ParquetPathSourceLocation extends PathSourceLocation {
  format: "parquet";
  options?: ParquetReaderOptions;
}

model ExcelPathSourceLocation extends PathSourceLocation {
  format: "excel";
  options?: ExcelReaderOptions;
}

model TextPathSourceLocation extends PathSourceLocation {
  format: "text";
  options?: TextReaderOptions;
}

model BlobPathSourceLocation extends PathSourceLocation {
  format: "blob";
  options?: BlobReaderOptions;
}

model VortexPathSourceLocation extends PathSourceLocation {
  format: "vortex";
  options?: VortexReaderOptions;
}

model DeltaPathSourceLocation extends PathSourceLocation {
  format: "delta";
  options?: DeltaReaderOptions;
}

model IcebergPathSourceLocation extends PathSourceLocation {
  format: "iceberg";
  options?: IcebergReaderOptions;
}

@apigen.metadata(#{ `x-leapview-format`: #{ name: "lance", extensions: #[".lance"], scanKind: "replacement", requiredExtension: "lance", sourceSecretType: "lance", sourceDataIdentityCapability: "unavailable", tableLike: true, allowsOptions: false, defaults: #{} } })
model LancePathSourceLocation extends PathSourceLocation {
  format: "lance";
}

model RelationSourceLocation {
  type: "relation";
  catalog?: Identifier;
  schema?: Identifier;
  name: Identifier;
}

@discriminated(#{ envelope: "none", discriminatorPropertyName: "type" })
union SourceLocation {
  path: PathSourceLocation,
  relation: RelationSourceLocation,
}

model SourceSpec {
  connection: ResourceID;
  location: SourceLocation;
  schema?: SourceSchema;
  freshness?: SourceFreshness;
}

model DirectModelDefinition extends ModelDefinition {
  type: "direct";
  source: ResourceID;
}

model SQLModelDefinition extends ModelDefinition {
  type: "sql";
  sql: string;
}

@discriminator("type")
model ModelDefinition {
  type: string;
}

model ModelField {
  datatype?: "String" | "Integer" | "Decimal" | "Float" | "Boolean" | "Date" | "Time" | "DateTime" | "DateTimeTz" | "Opaque";
  label?: string;
  description?: string;
  aiContext?: AIContext;
}

model ModelEntity {
  type: "primary" | "unique" | "foreign" | "natural";
  fields: Identifier[];
  description?: string;
  aiContext?: AIContext;
}

model ModelGrain {
  entity: Identifier;
}

model NonNullModelCheck {
  type: "non_null";
  field: Identifier;
  severity?: "warning" | "error";
}

model UniqueModelCheck {
  type: "unique";
  fields: Identifier[];
  severity?: "warning" | "error";
}

model AcceptedValuesModelCheck {
  type: "accepted_values";
  field: Identifier;
  values: string[];
  severity?: "warning" | "error";
}

model RelationshipModelCheck {
  type: "relationship";
  field: Identifier;
  to: ModelReference;
  severity?: "warning" | "error";
}

model RowCountModelCheck {
  type: "row_count";
  minimum?: int64;
  maximum?: int64;
  severity?: "warning" | "error";
}

@discriminated(#{ envelope: "none", discriminatorPropertyName: "type" })
union ModelCheck {
  non_null: NonNullModelCheck,
  unique: UniqueModelCheck,
  accepted_values: AcceptedValuesModelCheck,
  relationship: RelationshipModelCheck,
  row_count: RowCountModelCheck,
}

model ModelSpec {
  definition: ModelDefinition;
  @apigen.propertyNames(Identifier)
  entities: Record<ModelEntity>;
  grain: ModelGrain;
  @apigen.propertyNames(Identifier)
  fields?: Record<ModelField>;
  checks?: ModelCheck[];
}

@pattern("^[A-Za-z_][A-Za-z0-9_]*\\.[A-Za-z_][A-Za-z0-9_]*$")
scalar SemanticFieldReference extends string;

model SemanticAccessFilter {
  field: Identifier;
  userAttribute: Identifier;
}

model SemanticDataset {
  `model`: SemanticModelResourceName;
  defaultTimeDimension?: Identifier;
  displayName?: string;
  description?: string;
  aiContext?: AIContext;
  @minItems(1)
  @apigen.uniqueItems
  requiredAccessGrants?: Identifier[];
  accessFilters?: SemanticAccessFilter[];
}

model NamedSemanticRelationshipEndpoint {
  dataset: Identifier;
  entity: Identifier;
}

model FieldsSemanticRelationshipEndpoint {
  dataset: Identifier;
  @minItems(1)
  fields: Identifier[];
}

/** Relationship endpoints retain the authored entity-or-fields structural union. */
union SemanticRelationshipEndpoint {
  named: NamedSemanticRelationshipEndpoint,
  fields: FieldsSemanticRelationshipEndpoint,
}

model SemanticRelationship {
  from: SemanticRelationshipEndpoint;
  to: SemanticRelationshipEndpoint;
  description?: string;
  aiContext?: AIContext;
}

model SemanticDimensionBinding {
  field: SemanticFieldReference;
  path?: Identifier[];
}

model SemanticTimeSemantics {
  nativeGrain: "second" | "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year";
  @minItems(1)
  grains: ("second" | "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year")[];
  calendar?: string;
  timezone?: string;
}

model SemanticDimension {
  label?: string;
  description?: string;
  aiContext?: AIContext;
  datatype: "String" | "Integer" | "Decimal" | "Float" | "Boolean" | "Date" | "Time" | "DateTime" | "DateTimeTz" | "Opaque";
  time?: SemanticTimeSemantics;
  @apigen.propertyNames(Identifier)
  bindings: Record<SemanticDimensionBinding>;
  @minItems(1)
  @apigen.uniqueItems
  requiredAccessGrants?: Identifier[];
}

/** Authored filter literals deliberately exclude null, arrays, and objects. */
union SemanticLiteral {
  text: string,
  number: float64,
  boolean: boolean,
}

@minItems(1)
@maxItems(1024)
model SemanticTextAllowedValues is string[];

@minItems(1)
@maxItems(1024)
model SemanticNumberAllowedValues is float64[];

@minItems(1)
@maxItems(1024)
model SemanticBooleanAllowedValues is boolean[];

/** Access-grant values are homogeneous by JSON scalar category. */
union SemanticAllowedValues {
  text: SemanticTextAllowedValues,
  number: SemanticNumberAllowedValues,
  boolean: SemanticBooleanAllowedValues,
}

model SemanticAccessGrant {
  userAttribute: Identifier;
  @apigen.uniqueItems
  allowedValues: SemanticAllowedValues;
}

model EqualsSemanticFilter {
  field: SemanticFieldReference;
  operator: "equals";
  value: SemanticLiteral;
  path?: Identifier[];
  aiContext?: AIContext;
}

model NotEqualsSemanticFilter {
  field: SemanticFieldReference;
  operator: "not_equals";
  value: SemanticLiteral;
  path?: Identifier[];
  aiContext?: AIContext;
}

model InSemanticFilter {
  field: SemanticFieldReference;
  operator: "in";
  @minItems(1)
  value: SemanticLiteral[];
  path?: Identifier[];
  aiContext?: AIContext;
}

model NotInSemanticFilter {
  field: SemanticFieldReference;
  operator: "not_in";
  @minItems(1)
  value: SemanticLiteral[];
  path?: Identifier[];
  aiContext?: AIContext;
}

model LessThanSemanticFilter {
  field: SemanticFieldReference;
  operator: "less_than";
  value: SemanticLiteral;
  path?: Identifier[];
  aiContext?: AIContext;
}

model LessThanOrEqualSemanticFilter {
  field: SemanticFieldReference;
  operator: "less_than_or_equal";
  value: SemanticLiteral;
  path?: Identifier[];
  aiContext?: AIContext;
}

model GreaterThanSemanticFilter {
  field: SemanticFieldReference;
  operator: "greater_than";
  value: SemanticLiteral;
  path?: Identifier[];
  aiContext?: AIContext;
}

model GreaterThanOrEqualSemanticFilter {
  field: SemanticFieldReference;
  operator: "greater_than_or_equal";
  value: SemanticLiteral;
  path?: Identifier[];
  aiContext?: AIContext;
}

model IsNullSemanticFilter {
  field: SemanticFieldReference;
  operator: "is_null";
  path?: Identifier[];
  aiContext?: AIContext;
}

model IsNotNullSemanticFilter {
  field: SemanticFieldReference;
  operator: "is_not_null";
  path?: Identifier[];
  aiContext?: AIContext;
}

model AllSemanticFilter {
  @minItems(1)
  all: SemanticFilter[];
}

model AnySemanticFilter {
  @minItems(1)
  any: SemanticFilter[];
}

model NotSemanticFilter {
  not: SemanticFilter;
}

/** Recursive filters remain wrapper-free and select exactly one leaf or boolean node. */
union SemanticFilter {
  equals: EqualsSemanticFilter,
  not_equals: NotEqualsSemanticFilter,
  in: InSemanticFilter,
  not_in: NotInSemanticFilter,
  less_than: LessThanSemanticFilter,
  less_than_or_equal: LessThanOrEqualSemanticFilter,
  greater_than: GreaterThanSemanticFilter,
  greater_than_or_equal: GreaterThanOrEqualSemanticFilter,
  is_null: IsNullSemanticFilter,
  is_not_null: IsNotNullSemanticFilter,
  all: AllSemanticFilter,
  any: AnySemanticFilter,
  not: NotSemanticFilter,
}

model SemanticMetricCommon {
  label?: string;
  description?: string;
  aiContext?: AIContext;
  unit?: string;
  format?: string;
  hidden?: boolean;
  @minItems(1)
  @apigen.uniqueItems
  requiredAccessGrants?: Identifier[];
}

model SemanticMetricInput {
  field: SemanticFieldReference;
}

model AggregateSemanticMetric {
  type: "aggregate";
  dataset: Identifier;
  aggregation: "sum" | "count" | "count_distinct" | "avg" | "min" | "max";
  input: SemanticMetricInput;
  @minItems(1)
  where?: Identifier[];
  empty?: "zero" | "null";
  timeDimension?: Identifier;
  ...SemanticMetricCommon;
}

model DerivedSemanticMetric {
  type: "derived";
  expression: string;
  ...SemanticMetricCommon;
}

model RatioSemanticMetric {
  type: "ratio";
  numerator: Identifier;
  denominator: Identifier;
  ...SemanticMetricCommon;
}

@discriminated(#{ envelope: "none", discriminatorPropertyName: "type" })
union SemanticMetric {
  aggregate: AggregateSemanticMetric,
  derived: DerivedSemanticMetric,
  ratio: RatioSemanticMetric,
}

model SemanticModelSpec {
  @apigen.minProperties(1)
  @apigen.propertyNames(Identifier)
  datasets: Record<SemanticDataset>;
  @apigen.propertyNames(Identifier)
  accessGrants?: Record<SemanticAccessGrant>;
  @apigen.propertyNames(Identifier)
  relationships?: Record<SemanticRelationship>;
  @apigen.propertyNames(Identifier)
  dimensions?: Record<SemanticDimension>;
  @apigen.propertyNames(Identifier)
  filters?: Record<SemanticFilter>;
  @apigen.propertyNames(Identifier)
  metrics: Record<SemanticMetric>;
}

model ResourceProvenance {
  origin?: string;
  path?: string;
  source?: string;
}

model ResourceMetadata {
  id: ResourceID;
  name: ResourceName;
  displayName?: string;
  description?: string;
  owner?: string;
  domain?: string;
  tags?: string[];
  documentation?: string;
  provenance?: ResourceProvenance;
}

// SemanticModel retains the legacy ResourceName grammar while other authored
// resources use the stricter shared ResourceName contract.
model SemanticModelMetadata {
  id: ResourceID;
  name: SemanticModelResourceName;
  displayName?: string;
  description?: string;
  owner?: string;
  domain?: string;
  tags?: string[];
  documentation?: string;
  provenance?: ResourceProvenance;
}

@apigen.contract(#{ kind: "resource", tags: #["connection", "data-resource"] })
model Connection {
  apiVersion: "leapview.dev/v1";
  kind: "Connection";
  metadata: ResourceMetadata;
  spec: ConnectionSpec;
}

@apigen.contract(#{ kind: "resource", tags: #["source", "data-resource"] })
model Source {
  apiVersion: "leapview.dev/v1";
  kind: "Source";
  metadata: ResourceMetadata;
  spec: SourceSpec;
}

@apigen.contract(#{ kind: "resource", tags: #["model", "data-resource"] })
model Model {
  apiVersion: "leapview.dev/v1";
  kind: "Model";
  metadata: ResourceMetadata;
  aiContext?: AIContext;
  spec: ModelSpec;
}

@apigen.metadata(#{ `x-leapview-semantic-access-profile`: "leapview.semantic-access/v1" })
@apigen.contract(#{ kind: "resource", tags: #["semantic-model", "data-resource"] })
model SemanticModel {
  apiVersion: "leapview.dev/v1";
  kind: "SemanticModel";
  metadata: SemanticModelMetadata;
  aiContext?: AIContext;
  spec: SemanticModelSpec;
}
