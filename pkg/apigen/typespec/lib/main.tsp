import "@typespec/http";
import "@typespec/openapi";
import "../dist/src/index.js";

using TypeSpec.Reflection;

namespace apigen;

model CLIArg {
  source: "path" | "query" | "body";
  name: string;
  displayName?: string;
}

model CLIOutput {
  mode?: "detail" | "collection" | "empty" | "raw";
  tableColumns?: string[];
  quietFields?: string[];
}

model CLIPagination {
  itemsField?: string;
  nextPageTokenField?: string;
}

model CLIOptions {
  command: string[];
  args?: CLIArg[];
  bodyInput?: "none" | "json" | "flags" | "flags_or_json" | "text" | "binary" | "file" | "multipart";
  confirm?: "none" | "always";
  output?: CLIOutput;
  pagination?: CLIPagination;
}

model AuditOptions {
  required: boolean;
  successAction?: string;
  guarantee?: "transactional" | "best-effort";
}

model AuditPayloadOptions {
  schemaVersion: int32;
  retention: "short" | "standard" | "security";
}

model AsyncExecutionOptions {
  mode: "async";
  guarantee: "transactional";
  jobKind: string;
  resourceKind: string;
  initialEvent: string;
  initialState: string;
  statusOperation: string;
  eventsOperation: string;
  cancellation: "supported" | "unsupported";
}

model TypedAsyncExecutionOptions {
  mode?: "async";
  guarantee: "transactional";
  jobKind: string;
  resourceKind: string;
  initialEvent: string;
  initialState: string;
  cancellation: "supported" | "unsupported";
}

model CommandFailureOptions {
  kind: string;
  statusCode: int32;
  code: string;
  publicDetail: string;
}

model CommandOptions {
  audit?: AuditOptions;
  auditAction?: string;
  guarantee?: "transactional" | "best-effort";
  execution?: AsyncExecutionOptions;
  failures?: CommandFailureOptions[];
  additionalExposures?: ("ui" | "agent" | "automation")[];
  targetParameter?: string;
}

model CommandDefaultsOptions {
  guarantee?: "transactional" | "best-effort";
  failures?: CommandFailureOptions[];
  additionalExposures?: ("ui" | "agent" | "automation")[];
}

model ResponseShapeOptions {
  kind: "wrapped_json";
  bodyType?: string;
}

model PackageOptions {
  title: string;
  version: string;
  description?: string;
}

model ContractOptions {
  name?: string;
  kind?: string;
  tags?: string[];
}

model TransportFailureOptions {
  kind: string;
  statusCode: int32;
  code: string;
  publicDetail: string;
}

model TransportErrorsOptions {
  contentType: string;
  failures: TransportFailureOptions[];
}

union ToolInputMode {
  modelValue: "model";
  contextValue: "context";
  omitValue: "omit";
}

model ToolInputFieldOptions {
  source: "path" | "query" | "header" | "body";
  name: string;
  mode?: ToolInputMode;
  `alias`?: string;
  contextKey?: string;
  description?: string;
  `default`?: unknown;
}

model ToolProjectionOptions {
  source: string;
  target?: string;
  select?: ToolProjectionOptions[];
  countAs?: string;
}

model ToolCursorOptions {
  source: string;
  target?: string;
  hasMoreTarget?: string;
}

model ToolOutputOptions {
  mode: "raw" | "project" | "empty";
  select?: ToolProjectionOptions[];
  cursor?: ToolCursorOptions;
}

model ToolInputOptions {
  fields?: ToolInputFieldOptions[];
}

model ToolOptions {
  name: string;
  description?: string;
  effect: "read" | "idempotent-write" | "write" | "destructive";
  confirmation?: "never" | "policy" | "always";
  tags?: string[];
  input?: ToolInputOptions;
  output: ToolOutputOptions;
  metadata?: unknown;
}

extern dec cli(target: Operation, options: valueof CLIOptions);
extern dec command(target: Operation, options: valueof CommandOptions);
extern dec commandDefaults(target: Interface, options: valueof CommandDefaultsOptions);
extern dec unaudited(target: Operation, reason: valueof string);
extern dec ui(target: Operation, actionId: valueof string);
extern dec auditPayload(target: Operation | Interface, schema: Model, options?: valueof AuditPayloadOptions);
extern dec auditSchema(target: Model, options: valueof AuditPayloadOptions);
extern dec sensitivity(target: ModelProperty, classification: valueof ("public" | "internal" | "pii" | "secret"));
extern dec auditPublic(target: ModelProperty);
extern dec auditInternal(target: ModelProperty);
extern dec auditPii(target: ModelProperty);
extern dec auditSecret(target: ModelProperty);
extern dec query(target: Operation);
extern dec authz(target: Operation | Interface, value: valueof unknown);
extern dec target(target: ModelProperty);
extern dec asyncExecution(target: Operation, status: Operation, events: Operation, options: valueof TypedAsyncExecutionOptions);
extern dec failureDefinition(target: Model, options: valueof CommandFailureOptions);
extern dec failsWith(target: Operation, definition: Model);
extern dec manual(target: Operation);
extern dec responseShape(target: Model, options: valueof ResponseShapeOptions);
extern dec `package`(target: Namespace, options: valueof PackageOptions);
extern dec contract(target: Model | Enum, options?: valueof ContractOptions);
extern dec `metadata`(target: Model | ModelProperty, value: valueof unknown);
extern dec exactNumbers(target: Union);
extern dec tool(target: Operation, options: valueof ToolOptions);
extern dec transportErrors(target: Namespace, schema: Model, options: valueof TransportErrorsOptions);
extern dec propertyNames(target: ModelProperty, key: Scalar);
extern dec minProperties(target: ModelProperty, value: int32);
extern dec uniqueItems(target: ModelProperty);
