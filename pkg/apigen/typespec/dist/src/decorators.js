import { isRecordModelType } from "@typespec/compiler";
import { reportDiagnostic } from "./lib.js";
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
const propertyNamesKey = Symbol.for("@yacobolo/apigen.propertyNames");
export function $cli(context, target, options) {
    context.program.stateMap(cliKey).set(target, options);
}
export function $command(context, target, options) {
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
export function $commandDefaults(context, target, options) {
    context.program.stateMap(commandDefaultsKey).set(target, options);
}
export function $unaudited(context, target, reason) {
    context.program.stateMap(unauditedKey).set(target, reason);
}
export function $ui(context, target, actionId) {
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
export function $auditPayload(context, target, schema, options) {
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
export function $auditSchema(context, target, options) {
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
export function $sensitivity(context, target, classification) {
    setSensitivity(context, target, classification);
}
export function $auditPublic(context, target) {
    setSensitivity(context, target, "public");
}
export function $auditInternal(context, target) {
    setSensitivity(context, target, "internal");
}
export function $auditPii(context, target) {
    setSensitivity(context, target, "pii");
}
export function $auditSecret(context, target) {
    setSensitivity(context, target, "secret");
}
function setSensitivity(context, target, classification) {
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
export function $query(context, target) {
    context.program.stateSet(queryKey).add(target);
}
export function $authz(context, target, value) {
    context.program.stateMap(authzKey).set(target, value);
}
export function $target(context, target) {
    context.program.stateSet(targetKey).add(target);
}
export function $asyncExecution(context, target, status, events, options) {
    context.program.stateMap(asyncExecutionKey).set(target, { status, events, options });
}
export function $failureDefinition(context, target, options) {
    const definitions = context.program.stateMap(failureDefinitionKey);
    if (definitions.has(target)) {
        reportDiagnostic(context.program, {
            code: "invalid-command",
            format: { reason: "@apigen.failureDefinition must not be applied more than once" },
            target,
        });
        return;
    }
    for (const [model, authored] of definitions.entries()) {
        if (authored.code === options.code && (authored.kind !== options.kind ||
            authored.statusCode !== options.statusCode ||
            authored.publicDetail !== options.publicDetail)) {
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
export function $failsWith(context, target, definition) {
    const definitions = context.program.stateMap(failsWithKey);
    const current = definitions.get(target) ?? [];
    definitions.set(target, [...current, definition]);
}
export function $manual(context, target) {
    context.program.stateSet(manualKey).add(target);
}
export function $responseShape(context, target, options) {
    context.program.stateMap(responseShapeKey).set(target, options);
}
export function $package(context, target, options) {
    context.program.stateMap(packageKey).set(target, options);
}
export function $contract(context, target, options = {}) {
    context.program.stateMap(contractKey).set(target, options);
}
export function $metadata(context, target, value) {
    context.program.stateMap(metadataKey).set(target, value);
}
export function $tool(context, target, options) {
    context.program.stateMap(toolKey).set(target, options);
}
export function $transportErrors(context, target, schema, options) {
    context.program.stateMap(transportErrorsKey).set(target, { schema, ...options });
}
export function $propertyNames(context, target, key) {
    if (context.program.stateMap(propertyNamesKey).has(target)) {
        reportDiagnostic(context.program, {
            code: "invalid-property-names",
            format: { reason: "the decorator may only be applied once" },
            target,
        });
        return;
    }
    if (target.type.kind !== "Model" || !isRecordModelType(target.type)) {
        reportDiagnostic(context.program, {
            code: "invalid-property-names",
            format: { reason: "target must be a string-indexed map (for example, extends Record<T>)" },
            target,
        });
        return;
    }
    let current = key;
    while (current && current.name !== "string") {
        current = current.baseScalar;
    }
    if (!current) {
        reportDiagnostic(context.program, {
            code: "invalid-property-names",
            format: { reason: "key must be a scalar derived from string" },
            target,
        });
        return;
    }
    context.program.stateMap(propertyNamesKey).set(target, key);
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
        propertyNames: $propertyNames,
    },
};
export function getCLI(context, target) {
    return context.program.stateMap(cliKey).get(target);
}
export function getCommand(context, target) {
    const authored = context.program.stateMap(commandKey).get(target);
    if (!authored) {
        return undefined;
    }
    const defaults = target.interface
        ? context.program.stateMap(commandDefaultsKey).get(target.interface)
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
    };
}
export function getAuthoredCommand(context, target) {
    return context.program.stateMap(commandKey).get(target);
}
export function getCommandDefaults(context, target) {
    return context.program.stateMap(commandDefaultsKey).get(target);
}
export function getUnauditedReason(context, target) {
    return context.program.stateMap(unauditedKey).get(target);
}
export function getUI(context, target) {
    return context.program.stateMap(uiKey).get(target);
}
export function getAuditPayload(context, target) {
    return (context.program.stateMap(auditPayloadKey).get(target) ??
        (target.interface ? context.program.stateMap(auditPayloadKey).get(target.interface) : undefined));
}
export function getAuditSchema(context, target) {
    return context.program.stateMap(auditSchemaKey).get(target);
}
export function getSensitivity(context, target) {
    return context.program.stateMap(sensitivityKey).get(target);
}
export function isQuery(context, target) {
    return context.program.stateSet(queryKey).has(target);
}
export function getAuthz(context, target) {
    return context.program.stateMap(authzKey).get(target) ??
        (target.interface ? context.program.stateMap(authzKey).get(target.interface) : undefined);
}
export function isTarget(context, target) {
    return context.program.stateSet(targetKey).has(target);
}
export function getAsyncExecution(context, target) {
    return context.program.stateMap(asyncExecutionKey).get(target);
}
export function getNamedFailures(context, target) {
    const models = context.program.stateMap(failsWithKey).get(target);
    return (models ?? []).map((model) => ({
        model,
        options: context.program.stateMap(failureDefinitionKey).get(model),
    }));
}
export function isManual(context, target) {
    return context.program.stateSet(manualKey).has(target);
}
export function getResponseShape(context, target) {
    return context.program.stateMap(responseShapeKey).get(target);
}
export function getPackages(context) {
    return [...context.program.stateMap(packageKey).entries()];
}
export function getContracts(context) {
    return [...context.program.stateMap(contractKey).entries()];
}
export function getMetadata(context, target) {
    return context.program.stateMap(metadataKey).get(target);
}
export function getTool(context, target) {
    return context.program.stateMap(toolKey).get(target);
}
export function getTransportErrors(context, target) {
    return context.program.stateMap(transportErrorsKey).get(target);
}
export function getPropertyNames(context, target) {
    return context.program.stateMap(propertyNamesKey).get(target);
}
