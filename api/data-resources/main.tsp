import "@yacobolo/apigen";

@apigen.`package`(#{
  title: "LeapView v1 data-resource contracts",
  version: "1.0.0",
  description: "Strict authored Connection, Source, and Model-definition contracts.",
})
namespace LeapViewDataResources;

/**
 * Portable access is intentionally tiny. Endpoints, credentials, secret
 * providers, and resolved authentication are target-owned bindings.
 */
enum PublicAccess {
  public: "public";
}

model CSVReaderOptions {
  header?: boolean;
  delimiter?: string;
  quote?: string;
  escape?: string;
  nullString?: string;
}

model JSONReaderOptions {
  format?: "auto" | "array" | "newline_delimited";
  maximumDepth?: int32;
}

model ParquetReaderOptions {
  hivePartitioning?: boolean;
  unionByName?: boolean;
}

model ExcelReaderOptions {
  sheet?: string;
  header?: boolean;
}

model TextReaderOptions {
  delimiter?: string;
  quote?: string;
  header?: boolean;
}

model BlobReaderOptions {
  compression?: "auto" | "none" | "gzip" | "zstd";
}

model VortexReaderOptions {
  version?: string;
}

model DeltaReaderOptions {
  version?: string;
}

model IcebergReaderOptions {
  snapshot?: string;
}

model LanceReaderOptions {
  version?: string;
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
  lance?: LanceReaderOptions;
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
  fields: Record<SourceSchemaField>;
}

model StrictSourceSchema {
  mode: "strict";
  fields: Record<SourceSchemaField>;
}

@discriminated(#{ envelope: "none", discriminatorPropertyName: "mode" })
union SourceSchema {
  inferred: InferredSourceSchema,
  compatible: CompatibleSourceSchema,
  strict: StrictSourceSchema,
}

model FreshnessDuration {
  amount: int64;
  unit: "second" | "minute" | "hour" | "day";
}

model FieldFreshness {
  basis: "field";
  field: string;
  warningAfter?: FreshnessDuration;
  errorAfter?: FreshnessDuration;
}

model RevisionFreshness {
  basis: "revision";
  revision: string;
  warningAfter?: FreshnessDuration;
  errorAfter?: FreshnessDuration;
}

@discriminated(#{ envelope: "none", discriminatorPropertyName: "basis" })
union SourceFreshness {
  field: FieldFreshness,
  revision: RevisionFreshness,
}

// Path location retains the ADR's scalar format plus sibling options surface.
// Generator validation seals the known option keys to the selected format.
model SourcePathOptions {
  header?: boolean;
  delimiter?: string;
  quote?: string;
  escape?: string;
  nullString?: string;
  format?: "auto" | "array" | "newline_delimited";
  maximumDepth?: int32;
  hivePartitioning?: boolean;
  unionByName?: boolean;
  sheet?: string;
  compression?: "auto" | "none" | "gzip" | "zstd";
  version?: string;
  snapshot?: string;
}

model PathSourceLocation {
  type: "path";
  path: string;
  format: "csv" | "json" | "parquet" | "excel" | "text" | "blob" | "vortex" | "delta" | "iceberg" | "lance";
  options?: SourcePathOptions;
}

model RelationSourceLocation {
  catalog?: string;
  schema?: string;
  name: string;
}

@discriminated(#{ envelope: "none", discriminatorPropertyName: "type" })
union SourceLocation {
  path: PathSourceLocation,
  relation: RelationSourceLocation,
}

model SourceSpec {
  connection: string;
  location: SourceLocation;
  schema?: SourceSchema;
  freshness?: SourceFreshness;
}

model DirectModelDefinition extends ModelDefinition {
  type: "direct";
  source: string;
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
  datatype: "String" | "Integer" | "Decimal" | "Float" | "Boolean" | "Date" | "Time" | "DateTime" | "DateTimeTz" | "Opaque";
  label?: string;
  description?: string;
  aiContext?: AIContext;
}

model ModelEntity {
  type: "primary" | "unique" | "foreign" | "natural";
  fields: string[];
  description?: string;
  aiContext?: AIContext;
}

model ModelGrain {
  entity: string;
}

model NonNullModelCheck {
  type: "non_null";
  field: string;
  severity?: "warning" | "error";
}

model UniqueModelCheck {
  type: "unique";
  fields: string[];
  severity?: "warning" | "error";
}

model AcceptedValuesModelCheck {
  type: "accepted_values";
  field: string;
  values: string[];
  severity?: "warning" | "error";
}

model RelationshipModelCheck {
  type: "relationship";
  field: string;
  to: string;
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
  entities: Record<ModelEntity>;
  grain: ModelGrain;
  fields: Record<ModelField>;
  checks?: ModelCheck[];
}

model ResourceProvenance {
  origin?: string;
  path?: string;
  source?: string;
}

model ResourceMetadata {
  id: string;
  name: string;
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
