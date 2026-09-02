namespace LeapViewDataResources;

// leapview.contract/v1 is a deliberately separate, allowlist-only external
// identity surface. These DTOs do not extend authored or runtime resources.

model ContractProjectionContract {
  version: SemanticVersion;
  compatibility: "backward";
}

model ContractProjectionMetadata {
  id: ResourceID;
  name: string;
  contract: ContractProjectionContract;
}

model ContractProjectionAuthoritativeDefinition {
  type: "businessDefinition" | "transformationImplementation";
  url: AuthoritativeURL;
}

model ContractProjectionDeprecation {
  since: SemanticVersion;
  reason: string;
  replacement?: Identifier;
}

model ContractProjectionField {
  datatype?: "String" | "Integer" | "Decimal" | "Float" | "Boolean" | "Date" | "Time" | "DateTime" | "DateTimeTz" | "Opaque";
  nullable?: boolean;
  criticalDataElement?: boolean;
  classification?: Identifier;
  authoritativeDefinitions?: ContractProjectionAuthoritativeDefinition[];
  deprecation?: ContractProjectionDeprecation;
}

model ContractProjectionDuration {
  amount: int64;
  unit: "second" | "minute" | "hour" | "day";
}

model ContractProjectionSourceFreshness {
  basis: "field" | "revision";
  field?: Identifier;
  revision?: utcDateTime;
  warningAfter?: ContractProjectionDuration;
  errorAfter?: ContractProjectionDuration;
}

model ContractProjectionSourceSchema {
  mode: "inferred" | "compatible" | "strict";
  @apigen.propertyNames(Identifier)
  fields?: Record<ContractProjectionField>;
}

model ContractProjectionSourceBody {
  schema: ContractProjectionSourceSchema;
  freshness?: ContractProjectionSourceFreshness;
}

@apigen.contract(#{ kind: "leapview-contract-v1", tags: #["source", "contract-projection"] })
model SourceContractProjection {
  profile: "leapview.contract/v1";
  apiVersion: "leapview.dev/v1";
  kind: "Source";
  metadata: ContractProjectionMetadata;
  contract: ContractProjectionSourceBody;
}

model ContractProjectionModelDefinition {
  type: "direct" | "sql";
  source?: ResourceID;
  // Canonical JSON of the compiler-admitted, pinned DuckDB AST projection.
  // Authored SQL text, locations, comments, and runtime plans never fit.
  sqlAst?: string;
}

model ContractProjectionModelEntity {
  type: "primary" | "unique" | "foreign" | "natural";
  fields: Identifier[];
}

model ContractProjectionModelGrain {
  entity: Identifier;
}

model ContractProjectionModelCheck {
  id: Identifier;
  type: "non_null" | "unique" | "accepted_values" | "relationship" | "row_count";
  field?: Identifier;
  fields?: Identifier[];
  values?: string[];
  to?: ModelReference;
  minimum?: int64;
  maximum?: int64;
  severity?: "warning" | "error";
}

model ContractProjectionModelBody {
  definition: ContractProjectionModelDefinition;
  @apigen.propertyNames(Identifier)
  entities: Record<ContractProjectionModelEntity>;
  grain: ContractProjectionModelGrain;
  @apigen.propertyNames(Identifier)
  fields: Record<ContractProjectionField>;
  checks?: ContractProjectionModelCheck[];
}

@apigen.contract(#{ kind: "leapview-contract-v1", tags: #["model", "contract-projection"] })
model ModelContractProjection {
  profile: "leapview.contract/v1";
  apiVersion: "leapview.dev/v1";
  kind: "Model";
  metadata: ContractProjectionMetadata;
  contract: ContractProjectionModelBody;
}

model ContractProjectionCanonicalTextValue {
  type: "String" | "Integer" | "Decimal" | "Number" | "Date" | "Time" | "DateTime" | "Timestamp";
  value: string;
}

model ContractProjectionCanonicalBooleanValue {
  type: "Boolean";
  value: boolean;
}

union ContractProjectionCanonicalValue {
  text: ContractProjectionCanonicalTextValue,
  boolean: ContractProjectionCanonicalBooleanValue,
}

model ContractProjectionSemanticAccessFilter {
  field: Identifier;
  userAttribute: Identifier;
}

model ContractProjectionSemanticDataset {
  `model`: SemanticModelResourceName;
  defaultTimeDimension?: Identifier;
  requiredAccessGrants?: Identifier[];
  accessFilters?: ContractProjectionSemanticAccessFilter[];
}

model ContractProjectionSemanticAccessGrant {
  userAttribute: Identifier;
  allowedValues: ContractProjectionCanonicalValue[];
}

model ContractProjectionRelationshipEndpoint {
  dataset: Identifier;
  entity?: Identifier;
  fields?: Identifier[];
}

model ContractProjectionSemanticRelationship {
  from: ContractProjectionRelationshipEndpoint;
  to: ContractProjectionRelationshipEndpoint;
}

model ContractProjectionSemanticTime {
  nativeGrain: "second" | "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year";
  grains: ("second" | "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year")[];
  calendar?: string;
  timezone?: string;
}

model ContractProjectionSemanticBinding {
  field: SemanticFieldReference;
  path?: Identifier[];
}

model ContractProjectionSemanticDimension {
  datatype: "String" | "Integer" | "Decimal" | "Float" | "Boolean" | "Date" | "Time" | "DateTime" | "DateTimeTz" | "Opaque";
  time?: ContractProjectionSemanticTime;
  @apigen.propertyNames(Identifier)
  bindings: Record<ContractProjectionSemanticBinding>;
  requiredAccessGrants?: Identifier[];
}

model ContractProjectionSemanticFilter {
  field?: SemanticFieldReference;
  operator?: "equals" | "not_equals" | "in" | "not_in" | "less_than" | "less_than_or_equal" | "greater_than" | "greater_than_or_equal" | "is_null" | "is_not_null";
  value?: ContractProjectionCanonicalValue;
  values?: ContractProjectionCanonicalValue[];
  path?: Identifier[];
  all?: ContractProjectionSemanticFilter[];
  any?: ContractProjectionSemanticFilter[];
  not?: ContractProjectionSemanticFilter;
}

model ContractProjectionSemanticMetricInput {
  field: SemanticFieldReference;
}

model ContractProjectionSemanticMetric {
  type: "aggregate" | "derived" | "ratio";
  dataset?: Identifier;
  aggregation?: "sum" | "count" | "count_distinct" | "avg" | "min" | "max";
  input?: ContractProjectionSemanticMetricInput;
  where?: Identifier[];
  empty?: "zero" | "null";
  timeDimension?: Identifier;
  expression?: string;
  numerator?: Identifier;
  denominator?: Identifier;
  unit?: string;
  format?: string;
  requiredAccessGrants?: Identifier[];
}

model ContractProjectionSemanticModelBody {
  @apigen.propertyNames(Identifier)
  datasets: Record<ContractProjectionSemanticDataset>;
  @apigen.propertyNames(Identifier)
  accessGrants?: Record<ContractProjectionSemanticAccessGrant>;
  @apigen.propertyNames(Identifier)
  relationships?: Record<ContractProjectionSemanticRelationship>;
  @apigen.propertyNames(Identifier)
  dimensions?: Record<ContractProjectionSemanticDimension>;
  @apigen.propertyNames(Identifier)
  filters?: Record<ContractProjectionSemanticFilter>;
  @apigen.propertyNames(Identifier)
  metrics: Record<ContractProjectionSemanticMetric>;
}

@apigen.contract(#{ kind: "leapview-contract-v1", tags: #["semantic-model", "contract-projection"] })
model SemanticModelContractProjection {
  profile: "leapview.contract/v1";
  apiVersion: "leapview.dev/v1";
  kind: "SemanticModel";
  metadata: ContractProjectionMetadata;
  contract: ContractProjectionSemanticModelBody;
}
