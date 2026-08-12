import type { DecoratorContext, Enum, Interface, Model, ModelProperty, Namespace, Operation } from "@typespec/compiler";

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

export interface TypedAsyncExecutionOptions {
  mode?: "async";
  guarantee: "transactional";
  jobKind: string;
  resourceKind: string;
  initialEvent: string;
  initialState: string;
  cancellation: "supported" | "unsupported";
}

export interface CommandFailureOptions {
  kind: string;
  statusCode: number;
  code: string;
  publicDetail: string;
}

export interface CommandOptions {
  audit?: AuditOptions;
  auditAction?: string;
  guarantee?: "transactional" | "best-effort";
  execution?: AsyncExecutionOptions;
  failures?: CommandFailureOptions[];
  additionalExposures?: Array<"ui" | "agent" | "automation">;
  targetParameter?: string;
}

export interface CommandDefaultsOptions {
  guarantee?: "transactional" | "best-effort";
  failures?: CommandFailureOptions[];
  additionalExposures?: Array<"ui" | "agent" | "automation">;
}

export interface TypedAsyncExecutionDefinition {
  status: Operation;
  events: Operation;
  options: TypedAsyncExecutionOptions;
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
const commandDefaultsKey = Symbol.for("@yacobolo/apigen.commandDefaults");
const unauditedKey = Symbol.for("@yacobolo/apigen.unaudited");
const uiKey = Symbol.for("@yacobolo/apigen.ui");
const auditPayloadKey = Symbol.for("@yacobolo/apigen.auditPayload");
const auditSchemaKey = Symbol.for("@yacobolo/apigen.auditSchema");
const sensitivityKey = Symbol.for("@yacobolo/apigen.sensitivity");
const queryKey = Symbol.for("@yacobolo/apigen.query");
const authzKey = Symbol.for("@yacobolo/apigen.authz");
const targetKey = Symbol.for("@yacobolo/apigen.target");
const asyncExecutionKey = Symbol.for("@yacobolo/apigen.asyncExecution");
const failureDefinitionKey = Symbol.for("@yacobolo/apigen.failureDefinition");
const failsWithKey = Symbol.for("@yacobolo/apigen.failsWith");
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
  if (options.audit && (options.auditAction !== undefined || options.guarantee !== undefined)) {
    reportDiagnostic(context.program, {
      code: "invalid-command",
      format: { reason: "use legacy audit or concise auditAction/guarantee syntax, not both" },
      target,
    });
    return;
  }
  context.program.stateMap(commandKey).set(target, options);
}

export function $commandDefaults(
  context: DecoratorContext,
  target: Interface,
  options: CommandDefaultsOptions,
) {
  context.program.stateMap(commandDefaultsKey).set(target, options);
}

export function $unaudited(context: DecoratorContext, target: Operation, reason: string) {
  context.program.stateMap(unauditedKey).set(target, reason);
}

export function $ui(context: DecoratorContext, target: Operation, actionId: string) {
  const actions = context.program.stateMap(uiKey);
  if (actions.has(target)) {
    reportDiagnostic(context.program, {
      code: "invalid-command",
      format: { reason: "@apigen.ui must not be applied more than once" },
      target,
    });
    return;
  }
  actions.set(target, actionId);
}

export function $auditPayload(
  context: DecoratorContext,
  target: Operation | Interface,
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

export function $authz(context: DecoratorContext, target: Operation | Interface, value: unknown) {
  context.program.stateMap(authzKey).set(target, value);
}

export function $target(context: DecoratorContext, target: ModelProperty) {
  context.program.stateSet(targetKey).add(target);
}

export function $asyncExecution(
  context: DecoratorContext,
  target: Operation,
  status: Operation,
  events: Operation,
  options: TypedAsyncExecutionOptions,
) {
  context.program.stateMap(asyncExecutionKey).set(target, { status, events, options });
}

export function $failureDefinition(
  context: DecoratorContext,
  target: Model,
  options: CommandFailureOptions,
) {
  const definitions = context.program.stateMap(failureDefinitionKey);
  if (definitions.has(target)) {
    reportDiagnostic(context.program, {
      code: "invalid-command",
      format: { reason: "@apigen.failureDefinition must not be applied more than once" },
      target,
    });
    return;
  }
  for (const [model, authored] of definitions.entries() as Iterable<[Model, CommandFailureOptions]>) {
    if (authored.code === options.code && (
      authored.kind !== options.kind ||
      authored.statusCode !== options.statusCode ||
      authored.publicDetail !== options.publicDetail
    )) {
      reportDiagnostic(context.program, {
        code: "invalid-command",
        format: {
          reason: `failure code ${JSON.stringify(options.code)} conflicts with definition ${JSON.stringify(model.name)}`,
        },
        target,
      });
      return;
    }
  }
  definitions.set(target, options);
}

export function $failsWith(context: DecoratorContext, target: Operation, definition: Model) {
  const definitions = context.program.stateMap(failsWithKey);
  const current = (definitions.get(target) as Model[] | undefined) ?? [];
  definitions.set(target, [...current, definition]);
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
    commandDefaults: $commandDefaults,
    unaudited: $unaudited,
    ui: $ui,
    auditPayload: $auditPayload,
    auditSchema: $auditSchema,
    sensitivity: $sensitivity,
    auditPublic: $auditPublic,
    auditInternal: $auditInternal,
    auditPii: $auditPii,
    auditSecret: $auditSecret,
    query: $query,
    authz: $authz,
    target: $target,
    asyncExecution: $asyncExecution,
    failureDefinition: $failureDefinition,
    failsWith: $failsWith,
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
  const authored = context.program.stateMap(commandKey).get(target) as CommandOptions | undefined;
  if (!authored) {
    return undefined;
  }
  const defaults = target.interface
    ? context.program.stateMap(commandDefaultsKey).get(target.interface) as CommandDefaultsOptions | undefined
    : undefined;
  const audit = authored.audit ?? {
    required: context.program.stateMap(unauditedKey).has(target) ? false : true,
    successAction: authored.auditAction,
    guarantee: authored.guarantee ?? defaults?.guarantee,
  };
  return {
    ...authored,
    audit: {
      ...audit,
      guarantee: audit.guarantee ?? defaults?.guarantee,
    },
    failures: authored.failures ?? defaults?.failures ?? [],
    additionalExposures: authored.additionalExposures ?? defaults?.additionalExposures,
  } as CommandOptions & { audit: AuditOptions; failures: CommandFailureOptions[] };
}

export function getAuthoredCommand(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateMap(commandKey).get(target) as CommandOptions | undefined;
}

export function getCommandDefaults(
  context: { program: DecoratorContext["program"] },
  target: Interface,
) {
  return context.program.stateMap(commandDefaultsKey).get(target) as CommandDefaultsOptions | undefined;
}

export function getUnauditedReason(
  context: { program: DecoratorContext["program"] },
  target: Operation,
) {
  return context.program.stateMap(unauditedKey).get(target) as string | undefined;
}

export function getUI(context: { program: DecoratorContext["program"] }, target: Operation) {
  return context.program.stateMap(uiKey).get(target) as string | undefined;
}

export function getAuditPayload(context: { program: DecoratorContext["program"] }, target: Operation) {
  return (context.program.stateMap(auditPayloadKey).get(target) ??
    (target.interface ? context.program.stateMap(auditPayloadKey).get(target.interface) : undefined)) as
    AuditPayloadDefinition | undefined;
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
  return context.program.stateMap(authzKey).get(target) ??
    (target.interface ? context.program.stateMap(authzKey).get(target.interface) : undefined);
}

export function isTarget(context: { program: DecoratorContext["program"] }, target: ModelProperty) {
  return context.program.stateSet(targetKey).has(target);
}

export function getAsyncExecution(
  context: { program: DecoratorContext["program"] },
  target: Operation,
) {
  return context.program.stateMap(asyncExecutionKey).get(target) as TypedAsyncExecutionDefinition | undefined;
}

export function getNamedFailures(
  context: { program: DecoratorContext["program"] },
  target: Operation,
) {
  const models = context.program.stateMap(failsWithKey).get(target) as Model[] | undefined;
  return (models ?? []).map((model) => ({
    model,
    options: context.program.stateMap(failureDefinitionKey).get(model) as CommandFailureOptions | undefined,
  }));
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
