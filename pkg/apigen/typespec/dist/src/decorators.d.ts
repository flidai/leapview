import type { DecoratorContext, Enum, Model, ModelProperty, Namespace, Operation } from "@typespec/compiler";
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
export declare function $query(context: DecoratorContext, target: Operation): void;
export declare function $authz(context: DecoratorContext, target: Operation, value: unknown): void;
export declare function $manual(context: DecoratorContext, target: Operation): void;
export declare function $responseShape(context: DecoratorContext, target: Model, options: ResponseShapeOptions): void;
export declare function $package(context: DecoratorContext, target: Namespace, options: PackageOptions): void;
export declare function $contract(context: DecoratorContext, target: Model | Enum, options?: ContractOptions): void;
export declare function $metadata(context: DecoratorContext, target: Model | ModelProperty | Enum, value: Record<string, unknown>): void;
export declare function $tool(context: DecoratorContext, target: Operation, options: ToolOptions): void;
export declare function $transportErrors(context: DecoratorContext, target: Namespace, schema: Model, options: TransportErrorsOptions): void;
export declare const $decorators: {
    apigen: {
        cli: typeof $cli;
        command: typeof $command;
        query: typeof $query;
        authz: typeof $authz;
        manual: typeof $manual;
        responseShape: typeof $responseShape;
        package: typeof $package;
        contract: typeof $contract;
        metadata: typeof $metadata;
        tool: typeof $tool;
        transportErrors: typeof $transportErrors;
    };
};
export declare function getCLI(context: {
    program: DecoratorContext["program"];
}, target: Operation): CLIOptions | undefined;
export declare function getCommand(context: {
    program: DecoratorContext["program"];
}, target: Operation): CommandOptions | undefined;
export declare function isQuery(context: {
    program: DecoratorContext["program"];
}, target: Operation): boolean;
export declare function getAuthz(context: {
    program: DecoratorContext["program"];
}, target: Operation): any;
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
