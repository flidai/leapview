import type { DecoratorContext, Enum, Model, ModelProperty, Namespace, Operation } from "@typespec/compiler";

import { reportDiagnostic } from "./lib.js";

export interface CLIArg {
  source: "path" | "query" | "body";
  name: string;
  displayName?: string;
}

export interface CLIOutput {
  mode?: "detail" | "collection" | "empty" | "raw";
  tableColumns?: string[];
  quietFields?: string[];
}

export interface CLIPagination {
  itemsField?: string;
  nextPageTokenField?: string;
}

export interface CLIOptions {
  command: string[];
  args?: CLIArg[];
  bodyInput?: "none" | "json" | "flags" | "flags_or_json" | "text" | "binary" | "file" | "multipart";
  confirm?: "none" | "always";
  output?: CLIOutput;
  pagination?: CLIPagination;
}

export interface AuditOptions {
  required: boolean;
  successAction?: string;
  guarantee?: "transactional" | "best-effort";
}

export interface AuditPayloadOptions {
  schemaVersion: number;
  retention: "short" | "standard" | "security";
}

export interface AuditPayloadDefinition {
  schema: Model;
  options?: AuditPayloadOptions;
}

export type AuditSensitivity = "public" | "internal" | "pii" | "secret";

export interface AsyncExecutionOptions {
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

export interface CommandFailureOptions {
  kind: string;
  statusCode: number;
  code: string;
  publicDetail: string;
}

export interface CommandOptions {
  audit: AuditOptions;
  execution?: AsyncExecutionOptions;
  failures: CommandFailureOptions[];
  additionalExposures?: Array<"ui" | "agent" | "automation">;
  targetParameter?: string;
}

export interface ResponseShapeOptions {
  kind: "wrapped_json";
  bodyType?: string;
}

export interface PackageOptions {
  title: string;
  version: string;
  description?: string;
}

export interface ContractOptions {
  name?: string;
  kind?: string;
  tags?: string[];
}

export interface TransportErrorsOptions {
  contentType: string;
  failures: Array<{ kind: string; statusCode: number; code: string; publicDetail: string }>;
}

export interface TransportErrorsDefinition extends TransportErrorsOptions {
  schema: Model;
}

export interface ToolInputFieldOptions {
  source: "path" | "query" | "header" | "body";
  name: string;
  mode?: "model" | "context" | "omit";
  alias?: string;
  contextKey?: string;
  description?: string;
  default?: unknown;
}

export interface ToolProjectionOptions {
  source: string;
  target?: string;
  select?: ToolProjectionOptions[];
  countAs?: string;
}

export interface ToolOutputOptions {
  mode: "raw" | "project" | "empty";
  select?: ToolProjectionOptions[];
  cursor?: { source: string; target?: string; hasMoreTarget?: string };
}

export interface ToolOptions {
  name: string;
  description?: string;
  effect: "read" | "idempotent-write" | "write" | "destructive";
  confirmation?: "never" | "policy" | "always";
  tags?: string[];
  input?: { fields?: ToolInputFieldOptions[] };
  output: ToolOutputOptions;
  metadata?: Record<string, unknown>;
}

const cliKey = Symbol.for("@yacobolo/apigen.cli");
const commandKey = Symbol.for("@yacobolo/apigen.command");
const auditPayloadKey = Symbol.for("@yacobolo/apigen.auditPayload");
const auditSchemaKey = Symbol.for("@yacobolo/apigen.auditSchema");
const sensitivityKey = Symbol.for("@yacobolo/apigen.sensitivity");
const queryKey = Symbol.for("@yacobolo/apigen.query");
const authzKey = Symbol.for("@yacobolo/apigen.authz");
const manualKey = Symbol.for("@yacobolo/apigen.manual");
const responseShapeKey = Symbol.for("@yacobolo/apigen.responseShape");
const packageKey = Symbol.for("@yacobolo/apigen.package");
const contractKey = Symbol.for("@yacobolo/apigen.contract");
const metadataKey = Symbol.for("@yacobolo/apigen.metadata");
const toolKey = Symbol.for("@yacobolo/apigen.tool");
const transportErrorsKey = Symbol.for("@yacobolo/apigen.transportErrors");

export function $cli(context: DecoratorContext, target: Operation, options: CLIOptions) {
  context.program.stateMap(cliKey).set(target, options);
}

export function $command(context: DecoratorContext, target: Operation, options: CommandOptions) {
  context.program.stateMap(commandKey).set(target, options);
}

export function $auditPayload(
  context: DecoratorContext,
  target: Operation,
  schema: Model,
  options?: AuditPayloadOptions,
) {
  const definitions = context.program.stateMap(auditPayloadKey);
  if (definitions.has(target)) {
    reportDiagnostic(context.program, {
      code: "invalid-command",
      format: { reason: "@apigen.auditPayload must not be applied more than once" },
      target,
    });
    return;
  }
  definitions.set(target, { schema, options });
}

export function $auditSchema(
  context: DecoratorContext,
  target: Model,
  options: AuditPayloadOptions,
) {
  const definitions = context.program.stateMap(auditSchemaKey);
  if (definitions.has(target)) {
    reportDiagnostic(context.program, {
      code: "invalid-command",
      format: { reason: "@apigen.auditSchema must not be applied more than once" },
      target,
    });
    return;
  }
  definitions.set(target, options);
}

export function $sensitivity(
  context: DecoratorContext,
  target: ModelProperty,
  classification: AuditSensitivity,
) {
  setSensitivity(context, target, classification);
}

export function $auditPublic(context: DecoratorContext, target: ModelProperty) {
  setSensitivity(context, target, "public");
}

export function $auditInternal(context: DecoratorContext, target: ModelProperty) {
  setSensitivity(context, target, "internal");
}

export function $auditPii(context: DecoratorContext, target: ModelProperty) {
  setSensitivity(context, target, "pii");
}

export function $auditSecret(context: DecoratorContext, target: ModelProperty) {
  setSensitivity(context, target, "secret");
}

function setSensitivity(
  context: DecoratorContext,
  target: ModelProperty,
  classification: AuditSensitivity,
) {
  const classifications = context.program.stateMap(sensitivityKey);
  if (classifications.has(target)) {
    reportDiagnostic(context.program, {
      code: "invalid-command",
      format: { reason: "@apigen.sensitivity must not be applied more than once per field" },
      target,
    });
    return;
  }
  classifications.set(target, classification);
}

export function $query(context: DecoratorContext, target: Operation) {
  context.program.stateSet(queryKey).add(target);
}

export function $authz(context: DecoratorContext, target: Operation, value: unknown) {
  context.program.stateMap(authzKey).set(target, value);
}

export function $manual(context: DecoratorContext, target: Operation) {
  context.program.stateSet(manualKey).add(target);
}

export function $responseShape(
  context: DecoratorContext,
  target: Model,
  options: ResponseShapeOptions,
) {
  context.program.stateMap(responseShapeKey).set(target, options);
}

export function $package(context: DecoratorContext, target: Namespace, options: PackageOptions) {
  context.program.stateMap(packageKey).set(target, options);
}

export function $contract(
  context: DecoratorContext,
  target: Model | Enum,
  options: ContractOptions = {},
) {
  context.program.stateMap(contractKey).set(target, options);
}

export function $metadata(
  context: DecoratorContext,
  target: Model | ModelProperty | Enum,
  value: Record<string, unknown>,
) {
  context.program.stateMap(metadataKey).set(target, value);
}

export function $tool(context: DecoratorContext, target: Operation, options: ToolOptions) {
  context.program.stateMap(toolKey).set(target, options);
}

export function $transportErrors(
  context: DecoratorContext,
  target: Namespace,
  schema: Model,
  options: TransportErrorsOptions,
) {
  context.program.stateMap(transportErrorsKey).set(target, { schema, ...options });
}

export const $decorators = {
  apigen: {
    cli: $cli,
    command: $command,
    auditPayload: $auditPayload,
    auditSchema: $auditSchema,
    sensitivity: $sensitivity,
    auditPublic: $auditPublic,
    auditInternal: $auditInternal,
    auditPii: $auditPii,
    auditSecret: $auditSecret,
    query: $query,
    authz: $authz,
    manual: $manual,
    responseShape: $responseShape,
    package: $package,
    contract: $contract,
    metadata: $metadata,
    tool: $tool,
    transportErrors: $transportErrors,
  },
};

export function getCLI(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateMap(cliKey).get(target) as CLIOptions | undefined;
}

export function getCommand(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateMap(commandKey).get(target) as CommandOptions | undefined;
}

export function getAuditPayload(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateMap(auditPayloadKey).get(target) as AuditPayloadDefinition | undefined;
}

export function getAuditSchema(context: { program: DecoratorContext["program"] }, target: Model) {
  return context.program.stateMap(auditSchemaKey).get(target) as AuditPayloadOptions | undefined;
}

export function getSensitivity(
  context: { program: DecoratorContext["program"] },
  target: ModelProperty,
) {
  return context.program.stateMap(sensitivityKey).get(target) as AuditSensitivity | undefined;
}

export function isQuery(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateSet(queryKey).has(target);
}

export function getAuthz(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateMap(authzKey).get(target);
}

export function isManual(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateSet(manualKey).has(target);
}

export function getResponseShape(
  context: { program: DecoratorContext["program"] },
  target: Model,
) {
  return context.program.stateMap(responseShapeKey).get(target) as ResponseShapeOptions | undefined;
}

export function getPackages(context: { program: DecoratorContext["program"] }) {
  return [...context.program.stateMap(packageKey).entries()] as [Namespace, PackageOptions][];
}

export function getContracts(context: { program: DecoratorContext["program"] }) {
  return [...context.program.stateMap(contractKey).entries()] as [Model | Enum, ContractOptions][];
}

export function getMetadata(
  context: { program: DecoratorContext["program"] },
  target: Model | ModelProperty | Enum,
) {
  return context.program.stateMap(metadataKey).get(target) as Record<string, unknown> | undefined;
}

export function getTool(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateMap(toolKey).get(target) as ToolOptions | undefined;
}

export function getTransportErrors(
  context: { program: DecoratorContext["program"] },
  target: Namespace,
) {
  return context.program.stateMap(transportErrorsKey).get(target) as
    | TransportErrorsDefinition
    | undefined;
}
