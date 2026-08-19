import { type DecoratorContext, type Enum, type Interface, type Model, type ModelProperty, type Namespace, type Operation, type Scalar } from "@typespec/compiler";
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
    failures: Array<{
        kind: string;
        statusCode: number;
        code: string;
        publicDetail: string;
    }>;
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
    cursor?: {
        source: string;
        target?: string;
        hasMoreTarget?: string;
    };
}
export interface ToolOptions {
    name: string;
    description?: string;
    effect: "read" | "idempotent-write" | "write" | "destructive";
    confirmation?: "never" | "policy" | "always";
    tags?: string[];
    input?: {
        fields?: ToolInputFieldOptions[];
    };
    output: ToolOutputOptions;
    metadata?: Record<string, unknown>;
}
export declare function $cli(context: DecoratorContext, target: Operation, options: CLIOptions): void;
export declare function $command(context: DecoratorContext, target: Operation, options: CommandOptions): void;
export declare function $commandDefaults(context: DecoratorContext, target: Interface, options: CommandDefaultsOptions): void;
export declare function $unaudited(context: DecoratorContext, target: Operation, reason: string): void;
export declare function $ui(context: DecoratorContext, target: Operation, actionId: string): void;
export declare function $auditPayload(context: DecoratorContext, target: Operation | Interface, schema: Model, options?: AuditPayloadOptions): void;
export declare function $auditSchema(context: DecoratorContext, target: Model, options: AuditPayloadOptions): void;
export declare function $sensitivity(context: DecoratorContext, target: ModelProperty, classification: AuditSensitivity): void;
export declare function $auditPublic(context: DecoratorContext, target: ModelProperty): void;
export declare function $auditInternal(context: DecoratorContext, target: ModelProperty): void;
export declare function $auditPii(context: DecoratorContext, target: ModelProperty): void;
export declare function $auditSecret(context: DecoratorContext, target: ModelProperty): void;
export declare function $query(context: DecoratorContext, target: Operation): void;
export declare function $authz(context: DecoratorContext, target: Operation | Interface, value: unknown): void;
export declare function $target(context: DecoratorContext, target: ModelProperty): void;
export declare function $asyncExecution(context: DecoratorContext, target: Operation, status: Operation, events: Operation, options: TypedAsyncExecutionOptions): void;
export declare function $failureDefinition(context: DecoratorContext, target: Model, options: CommandFailureOptions): void;
export declare function $failsWith(context: DecoratorContext, target: Operation, definition: Model): void;
export declare function $manual(context: DecoratorContext, target: Operation): void;
export declare function $responseShape(context: DecoratorContext, target: Model, options: ResponseShapeOptions): void;
export declare function $package(context: DecoratorContext, target: Namespace, options: PackageOptions): void;
export declare function $contract(context: DecoratorContext, target: Model | Enum, options?: ContractOptions): void;
export declare function $metadata(context: DecoratorContext, target: Model | ModelProperty | Enum, value: Record<string, unknown>): void;
export declare function $tool(context: DecoratorContext, target: Operation, options: ToolOptions): void;
export declare function $transportErrors(context: DecoratorContext, target: Namespace, schema: Model, options: TransportErrorsOptions): void;
export declare function $propertyNames(context: DecoratorContext, target: ModelProperty, key: Scalar): void;
export declare const $decorators: {
    apigen: {
        cli: typeof $cli;
        command: typeof $command;
        commandDefaults: typeof $commandDefaults;
        unaudited: typeof $unaudited;
        ui: typeof $ui;
        auditPayload: typeof $auditPayload;
        auditSchema: typeof $auditSchema;
        sensitivity: typeof $sensitivity;
        auditPublic: typeof $auditPublic;
        auditInternal: typeof $auditInternal;
        auditPii: typeof $auditPii;
        auditSecret: typeof $auditSecret;
        query: typeof $query;
        authz: typeof $authz;
        target: typeof $target;
        asyncExecution: typeof $asyncExecution;
        failureDefinition: typeof $failureDefinition;
        failsWith: typeof $failsWith;
        manual: typeof $manual;
        responseShape: typeof $responseShape;
        package: typeof $package;
        contract: typeof $contract;
        metadata: typeof $metadata;
        tool: typeof $tool;
        transportErrors: typeof $transportErrors;
        propertyNames: typeof $propertyNames;
    };
};
export declare function getCLI(context: {
    program: DecoratorContext["program"];
}, target: Operation): CLIOptions | undefined;
export declare function getCommand(context: {
    program: DecoratorContext["program"];
}, target: Operation): (CommandOptions & {
    audit: AuditOptions;
    failures: CommandFailureOptions[];
}) | undefined;
export declare function getAuthoredCommand(context: {
    program: DecoratorContext["program"];
}, target: Operation): CommandOptions | undefined;
export declare function getCommandDefaults(context: {
    program: DecoratorContext["program"];
}, target: Interface): CommandDefaultsOptions | undefined;
export declare function getUnauditedReason(context: {
    program: DecoratorContext["program"];
}, target: Operation): string | undefined;
export declare function getUI(context: {
    program: DecoratorContext["program"];
}, target: Operation): string | undefined;
export declare function getAuditPayload(context: {
    program: DecoratorContext["program"];
}, target: Operation): AuditPayloadDefinition | undefined;
export declare function getAuditSchema(context: {
    program: DecoratorContext["program"];
}, target: Model): AuditPayloadOptions | undefined;
export declare function getSensitivity(context: {
    program: DecoratorContext["program"];
}, target: ModelProperty): AuditSensitivity | undefined;
export declare function isQuery(context: {
    program: DecoratorContext["program"];
}, target: Operation): boolean;
export declare function getAuthz(context: {
    program: DecoratorContext["program"];
}, target: Operation): any;
export declare function isTarget(context: {
    program: DecoratorContext["program"];
}, target: ModelProperty): boolean;
export declare function getAsyncExecution(context: {
    program: DecoratorContext["program"];
}, target: Operation): TypedAsyncExecutionDefinition | undefined;
export declare function getNamedFailures(context: {
    program: DecoratorContext["program"];
}, target: Operation): {
    model: Model;
    options: CommandFailureOptions | undefined;
}[];
export declare function isManual(context: {
    program: DecoratorContext["program"];
}, target: Operation): boolean;
export declare function getResponseShape(context: {
    program: DecoratorContext["program"];
}, target: Model): ResponseShapeOptions | undefined;
export declare function getPackages(context: {
    program: DecoratorContext["program"];
}): [Namespace, PackageOptions][];
export declare function getContracts(context: {
    program: DecoratorContext["program"];
}): [Model | Enum, ContractOptions][];
export declare function getMetadata(context: {
    program: DecoratorContext["program"];
}, target: Model | ModelProperty | Enum): Record<string, unknown> | undefined;
export declare function getTool(context: {
    program: DecoratorContext["program"];
}, target: Operation): ToolOptions | undefined;
export declare function getTransportErrors(context: {
    program: DecoratorContext["program"];
}, target: Namespace): TransportErrorsDefinition | undefined;
export declare function getPropertyNames(context: {
    program: DecoratorContext["program"];
}, target: ModelProperty): Scalar | undefined;
