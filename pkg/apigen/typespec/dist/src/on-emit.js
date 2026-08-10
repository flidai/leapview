import { emitFile, getAllTags, getDoc, getDiscriminatedUnion, getDiscriminatedUnionFromInheritance, getDiscriminator, getMaxLength, getMaxValue, getMinLength, getMinValue, getOverloadedOperation, getOverloads, getService, getSummary, isArrayModelType, isRecordModelType, } from "@typespec/compiler";
import { getAllHttpServices, getServers, isOverloadSameEndpoint, isSharedRoute, resolveAuthentication, } from "@typespec/http";
import { getExtensions, getOperationId, getTagsMetadata, resolveInfo, resolveOperationId } from "@typespec/openapi";
import { getAuthz, getCLI, getCommand, getContracts, getMetadata, getPackages, getResponseShape, getTool, getTransportErrors, isManual, isQuery, } from "./decorators.js";
import { reportDiagnostic } from "./lib.js";
class IRBuilder {
    program;
    requireExplicitOperationKind;
    schemas = new Map();
    enums = new Map();
    unions = new Map();
    syntheticSchemas = new Map();
    emittedSchemas = new Set();
    emittedEnums = new Set();
    emittedUnions = new Set();
    emittedSyntheticSchemas = new Set();
    failed = false;
    constructor(program, requireExplicitOperationKind = false) {
        this.program = program;
        this.requireExplicitOperationKind = requireExplicitOperationKind;
    }
    hasFailed() {
        return this.failed;
    }
    schemaRef(type, context) {
        if (type.kind === "Model") {
            if (isArrayModelType(type)) {
                return { type: "array", items: this.schemaRef(type.indexer.value, `${context} items`) };
            }
            if (isRecordModelType(type)) {
                return {
                    type: "object",
                    additional_properties: { schema: this.schemaRef(type.indexer.value, `${context} value`) },
                };
            }
            if (isNamedUserModel(type)) {
                this.schemas.set(type.name, type);
                return { ref: type.name };
            }
            return this.inlineObjectRef(type, context);
        }
        if (type.kind === "Scalar") {
            return withSchemaConstraints(this.program, type, scalarSchemaRef(type));
        }
        if (type.kind === "Enum") {
            if (type.name !== "") {
                this.enums.set(type.name, type);
                return { ref: type.name };
            }
            this.unsupported(type, context);
            return { type: "string" };
        }
        if (type.kind === "Union") {
            const enumValuesForUnion = stringLiteralUnionValues(type);
            if (enumValuesForUnion) {
                return { type: "string", enum: enumValuesForUnion };
            }
            if (type.name) {
                this.unions.set(type.name, type);
                return { ref: type.name };
            }
        }
        if (type.kind === "String") {
            return { type: "string", enum: [type.value] };
        }
        if (type.kind === "Boolean") {
            return { type: "boolean" };
        }
        if (type.kind === "Number") {
            return { type: "integer" };
        }
        if (type.kind === "Intrinsic" && type.name === "unknown") {
            return {};
        }
        if (type.kind === "Intrinsic" && type.name === "null") {
            return { type: "null" };
        }
        this.unsupported(type, context);
        return { type: "string" };
    }
    namedSchemaRef(type, context) {
        if (type.kind === "Model" && isNamedUserModel(type)) {
            this.schemas.set(type.name, type);
            return { ref: type.name };
        }
        if (type.kind === "Enum" && type.name !== "") {
            this.enums.set(type.name, type);
            return { ref: type.name };
        }
        this.report("unnamed-schema", { context }, type);
        return { type: "object" };
    }
    unsupportedType(type, context) {
        this.unsupported(type, context);
    }
    unsupportedAuth(context, reason, target) {
        this.report("unsupported-auth", { context, reason }, target);
    }
    unsupportedSharedRoute(operation, reason) {
        this.report("unsupported-shared-route", { reason }, operation);
    }
    unsupportedCookie(target) {
        this.report("unsupported-cookie", {}, target);
    }
    invalidCommand(reason, target) {
        this.report("invalid-command", { reason }, target);
    }
    invalidOperationKind(reason, target) {
        this.report("invalid-operation-kind", { reason }, target);
    }
    unsupportedResponseStatus(response) {
        this.report("unsupported-response-status", { status: JSON.stringify(response.statusCodes), operation: response.type.kind }, response.type);
    }
    unsupportedResponseContent(response, status, contentType) {
        this.report("unsupported-response-content", { operation: response.type.kind, status: String(status), contentType }, response.type);
    }
    reservedExtension(key, target) {
        this.report("reserved-extension", { key }, target);
    }
    invalidExtensionKey(key, target) {
        this.report("invalid-extension-key", { key }, target);
    }
    invalidExtensionValue(key, path, target) {
        this.report("invalid-extension-value", { key, path }, target);
    }
    emitSchemas() {
        const output = {};
        while (true) {
            const nextModel = [...this.schemas.values()].find((model) => !this.emittedSchemas.has(model.name));
            if (nextModel) {
                this.emittedSchemas.add(nextModel.name);
                output[nextModel.name] = this.schema(nextModel);
                continue;
            }
            const nextEnum = [...this.enums.values()].find((type) => !this.emittedEnums.has(type.name));
            if (nextEnum) {
                this.emittedEnums.add(nextEnum.name);
                output[nextEnum.name] = this.enumSchema(nextEnum);
                continue;
            }
            const nextUnion = [...this.unions.values()].find((type) => type.name && !this.emittedUnions.has(type.name));
            if (nextUnion?.name) {
                this.emittedUnions.add(nextUnion.name);
                output[nextUnion.name] = this.unionSchema(nextUnion);
                continue;
            }
            const nextSynthetic = [...this.syntheticSchemas.entries()].find(([name]) => !this.emittedSyntheticSchemas.has(name));
            if (nextSynthetic) {
                this.emittedSyntheticSchemas.add(nextSynthetic[0]);
                output[nextSynthetic[0]] = nextSynthetic[1];
                continue;
            }
            break;
        }
        return Object.keys(output).length > 0 ? output : undefined;
    }
    schema(model) {
        const discriminator = getDiscriminator(this.program, model);
        if (discriminator) {
            const [union, diagnostics] = getDiscriminatedUnionFromInheritance(model, discriminator);
            this.program.reportDiagnostics(diagnostics);
            const baseName = `${model.name}Base`;
            this.syntheticSchemas.set(baseName, this.objectSchema(model));
            const oneOf = [];
            const mapping = {};
            for (const [value, variant] of union.variants) {
                this.schemas.set(variant.name, variant);
                oneOf.push({ ref: variant.name });
                mapping[value] = variant.name;
            }
            return {
                type: "union",
                namespace: namespaceName(model.namespace),
                one_of: oneOf,
                discriminator: { property_name: union.propertyName, mapping },
            };
        }
        return this.objectSchema(model);
    }
    objectSchema(model) {
        const schema = {
            type: "object",
            namespace: namespaceName(model.namespace),
        };
        if (model.baseModel) {
            const baseName = getDiscriminator(this.program, model.baseModel)
                ? `${model.baseModel.name}Base`
                : model.baseModel.name;
            this.schemas.set(model.baseModel.name, model.baseModel);
            schema.base = { ref: baseName };
        }
        const doc = getDoc(this.program, model);
        if (doc) {
            schema.description = doc;
        }
        const extensions = validatedMetadata(this.program, this, model);
        if (extensions) {
            schema.extensions = extensions;
        }
        const properties = [...model.properties.values()];
        if (properties.length > 0) {
            schema.properties = {};
            schema.property_order = [];
            schema.required = [];
            for (const property of properties) {
                schema.properties[property.name] = this.schemaProperty(property);
                schema.property_order.push(property.name);
                if (!property.optional) {
                    schema.required.push(property.name);
                }
            }
            if (schema.required.length === 0) {
                delete schema.required;
            }
        }
        return schema;
    }
    unionSchema(type) {
        const scalarVariants = [...type.variants.values()];
        if (scalarVariants.length > 0 && scalarVariants.every((variant) => isJSONScalarType(variant.type))) {
            return {
                type: "union",
                namespace: namespaceName(type.namespace),
                one_of: scalarVariants.map((variant) => this.schemaRef(variant.type, `union ${type.name} variant`)),
            };
        }
        const [union, diagnostics] = getDiscriminatedUnion(this.program, type);
        this.program.reportDiagnostics(diagnostics);
        if (!union || !type.name) {
            this.unsupported(type, `union ${type.name ?? "(anonymous)"}`);
            return { type: "object" };
        }
        const oneOf = [];
        const mapping = {};
        for (const [value, variant] of union.variants) {
            const name = `${type.name}${schemaNamePart(value)}Variant`;
            const variantRef = this.schemaRef(variant, `union ${type.name} variant ${value}`);
            const properties = {
                [union.options.discriminatorPropertyName]: {
                    schema: { type: "string", enum: [value] },
                },
            };
            const required = [union.options.discriminatorPropertyName];
            const schema = {
                type: "object",
                namespace: namespaceName(type.namespace),
                properties,
                property_order: [...required],
                required: [...required],
            };
            if (union.options.envelope === "none") {
                schema.base = variantRef;
            }
            else {
                properties[union.options.envelopePropertyName] = { schema: variantRef };
                schema.property_order.push(union.options.envelopePropertyName);
                schema.required.push(union.options.envelopePropertyName);
            }
            this.syntheticSchemas.set(name, schema);
            oneOf.push({ ref: name });
            mapping[value] = name;
        }
        return {
            type: "union",
            namespace: namespaceName(type.namespace),
            one_of: oneOf,
            discriminator: {
                property_name: union.options.discriminatorPropertyName,
                mapping,
            },
        };
    }
    enumSchema(type) {
        const schema = {
            type: "string",
            namespace: namespaceName(type.namespace),
            enum: enumValues(type),
        };
        const doc = getDoc(this.program, type);
        if (doc) {
            schema.description = doc;
        }
        const extensions = validatedMetadata(this.program, this, type);
        if (extensions) {
            schema.extensions = extensions;
        }
        return schema;
    }
    schemaProperty(property) {
        const schemaProperty = {
            schema: withSchemaConstraints(this.program, property, this.schemaRef(property.type, `property ${property.name}`)),
        };
        const doc = getDoc(this.program, property);
        if (doc) {
            schemaProperty.description = doc;
        }
        const extensions = validatedMetadata(this.program, this, property);
        if (extensions) {
            schemaProperty.extensions = extensions;
        }
        return schemaProperty;
    }
    inlineObjectRef(model, context) {
        if (model.name === "") {
            this.report("unnamed-schema", { context }, model);
        }
        else {
            this.unsupported(model, context);
        }
        return { type: "object" };
    }
    unsupported(type, context) {
        this.report("unsupported-type", { kind: type.kind, context }, type);
    }
    report(code, format, target) {
        this.failed = true;
        reportDiagnostic(this.program, {
            code,
            format,
            target,
        });
    }
}
function isJSONScalarType(type) {
    return type.kind === "Scalar" || type.kind === "String" || type.kind === "Boolean" || type.kind === "Number" || (type.kind === "Intrinsic" && type.name === "null");
}
export async function $onEmit(context) {
    const outputFile = context.options["output-file"];
    if (!outputFile) {
        reportDiagnostic(context.program, { code: "missing-output-file", target: context.program.getGlobalNamespaceType() });
        return;
    }
    const [services, diagnostics] = getAllHttpServices(context.program);
    context.program.reportDiagnostics(diagnostics);
    if (diagnostics.some((diagnostic) => diagnostic.severity === "error")) {
        return;
    }
    if (services.length > 1) {
        reportDiagnostic(context.program, {
            code: "multiple-services",
            format: { count: String(services.length) },
            target: context.program.getGlobalNamespaceType(),
        });
        return;
    }
    let doc;
    const httpServices = services.filter((service) => service.operations.length > 0);
    const localNamespace = httpServices.length === 1
        ? namespaceName(httpServices[0].namespace)
        : packageMetadata(context.program).namespace;
    const builder = new IRBuilder(context.program, context.options["require-explicit-operation-kind"] ?? false);
    const contracts = contractRoots(context.program, builder, localNamespace);
    if (httpServices.length === 1) {
        doc = buildDocument(context.program, builder, httpServices[0], context.options);
        if (contracts.length > 0) {
            doc.contracts = contracts;
        }
    }
    else {
        if (contracts.length === 0 || httpServices.length > 1) {
            reportDiagnostic(context.program, {
                code: "multiple-services",
                format: { count: String(httpServices.length) },
                target: context.program.getGlobalNamespaceType(),
            });
            return;
        }
        doc = buildContractDocument(context.program, contracts, context.options);
    }
    doc.schemas = builder.emitSchemas();
    if (builder.hasFailed()) {
        return;
    }
    await emitFile(context.program, {
        path: outputFile,
        content: `${JSON.stringify(doc, null, 2)}\n`,
    });
}
function buildContractDocument(program, contracts, options) {
    const pkg = packageMetadata(program);
    return prune({
        schema_version: "v4",
        api: { base_path: options["base-path"] ?? "/" },
        info: prune({
            title: pkg.title,
            version: pkg.version,
            description: pkg.description,
            namespace: pkg.namespace,
        }),
        contracts,
    });
}
function packageMetadata(program) {
    const packages = getPackages({ program });
    if (packages.length > 0) {
        const [namespace, pkg] = packages[0];
        return {
            title: pkg.title,
            version: pkg.version,
            description: pkg.description,
            namespace: namespaceName(namespace),
        };
    }
    return {
        title: "Data Contracts",
        version: "0.1.0",
    };
}
function namespaceName(namespace) {
    const parts = [];
    let current = namespace;
    while (current) {
        if (current.name)
            parts.unshift(current.name);
        current = current.namespace;
    }
    return parts.length > 0 ? parts.join(".") : undefined;
}
function contractRoots(program, builder, localNamespace) {
    const roots = [];
    for (const [target, options] of getContracts({ program })) {
        const namespace = namespaceName(target.namespace);
        if (localNamespace && namespace !== localNamespace && !namespace?.startsWith(`${localNamespace}.`)) {
            continue;
        }
        const schema = builder.namedSchemaRef(target, `contract ${target.name}`);
        const root = prune({
            name: options.name ?? target.name,
            schema,
            kind: options.kind,
            tags: options.tags,
            description: getDoc(program, target),
            extensions: validatedMetadata(program, builder, target),
        });
        roots.push(root);
    }
    roots.sort((left, right) => left.name.localeCompare(right.name));
    return roots;
}
function validatedMetadata(program, builder, target) {
    const metadata = getMetadata({ program }, target);
    if (!metadata) {
        return undefined;
    }
    const output = {};
    for (const [key, value] of Object.entries(metadata).sort(([left], [right]) => left.localeCompare(right))) {
        if (!key.startsWith("x-")) {
            builder.invalidExtensionKey(key, target);
            continue;
        }
        if (isReservedExtensionKey(key)) {
            builder.reservedExtension(key, target);
            continue;
        }
        if (!isJSONCompatible(value)) {
            builder.invalidExtensionValue(key, key, target);
            continue;
        }
        output[key] = value;
    }
    return Object.keys(output).length > 0 ? output : undefined;
}
function buildDocument(program, builder, service, options) {
    const namespace = service.namespace;
    const info = resolveInfo(program, namespace) ?? {};
    const serviceInfo = getService(program, namespace);
    const tags = getTagsMetadata(program, namespace) ?? [];
    const servers = (getServers(program, namespace) ?? []).map((server) => ({
        url: server.url,
        ...(server.description ? { description: server.description } : {}),
    }));
    const authentication = resolveAuthentication(service);
    const defaultSecurity = authRequirements(builder, authentication.defaultAuth, namespace, "service authentication", true);
    const securitySchemes = collectSecuritySchemes(builder, authentication.schemes, namespace);
    const endpoints = mergedEndpoints(program, builder, service.operations, authentication.operationsAuth, defaultSecurity);
    const transportErrors = getTransportErrors({ program }, namespace);
    let transportErrorContract;
    if (transportErrors) {
        const schema = builder.namedSchemaRef(transportErrors.schema, "transport error schema");
        const failures = {};
        for (const failure of transportErrors.failures) {
            failures[failure.kind] = {
                status_code: failure.statusCode,
                code: failure.code,
                public_detail: failure.publicDetail,
            };
        }
        transportErrorContract = {
            schema,
            content_type: transportErrors.contentType,
            failures,
        };
    }
    return prune({
        schema_version: "v4",
        api: { base_path: options["base-path"] ?? "/" },
        info: prune({
            title: info.title ?? serviceInfo?.title ?? "API",
            version: info.version ?? "0.1.0",
            description: info.description,
            namespace: namespaceName(namespace),
        }),
        openapi: prune({
            version: "3.0.0",
            tag_order: tags.map((tag) => tag.name),
            security: defaultSecurity,
            security_schemes: Object.keys(securitySchemes).length > 0 ? securitySchemes : undefined,
        }),
        servers: servers.length > 0 ? servers : undefined,
        tags: tags.map((tag) => prune({ name: tag.name, description: tag.description })),
        endpoints,
        transport_errors: transportErrorContract,
    });
}
function mergedEndpoints(program, builder, operations, operationsAuth, defaultSecurity) {
    const groups = operationGroups(program, operations);
    return groups.map((group) => endpoint(program, builder, group, operationsAuth, defaultSecurity));
}
function operationGroups(program, operations) {
    const byRoute = new Map();
    const order = [];
    for (const operation of operations) {
        const key = `${operation.verb.toLowerCase()} ${operation.path}`;
        if (!byRoute.has(key)) {
            byRoute.set(key, []);
            order.push(key);
        }
        byRoute.get(key).push(operation);
    }
    const groups = [];
    for (const key of order) {
        const routeOperations = byRoute.get(key);
        if (routeOperations.length === 1) {
            groups.push({ operations: routeOperations, canonical: routeOperations[0] });
            continue;
        }
        const coalescable = routeOperations.some((operation) => isSharedRoute(program, operation.operation)) ||
            routeOperations.some((operation) => operation.overloading && isOverloadSameEndpoint(operation));
        if (!coalescable) {
            groups.push(...routeOperations.map((operation) => ({ operations: [operation], canonical: operation })));
            continue;
        }
        groups.push({
            operations: routeOperations,
            canonical: canonicalOperation(program, routeOperations),
        });
    }
    return groups;
}
function canonicalOperation(program, operations) {
    for (const operation of operations) {
        if (getOverloads(program, operation.operation)?.length) {
            return operation;
        }
    }
    for (const operation of operations) {
        if (getOverloadedOperation(program, operation.operation) === undefined) {
            return operation;
        }
    }
    return operations[0];
}
function endpoint(program, builder, group, operationsAuth, defaultSecurity) {
    const operation = group.canonical;
    validateSharedRouteMetadata(program, builder, group.operations, operation);
    const parameters = mergedParameters(program, builder, group.operations);
    const extensions = {};
    for (const [key, value] of operationVendorExtensions(program, builder, operation.operation)) {
        extensions[key] = value;
    }
    const authz = getAuthz({ program }, operation.operation);
    if (authz !== undefined) {
        extensions["x-authz"] = authz;
    }
    if (isManual({ program }, operation.operation)) {
        extensions["x-apigen-manual"] = true;
    }
    const output = prune({
        method: operation.verb,
        path: operation.path,
        operation_id: getOperationId(program, operation.operation) ?? resolveOperationId(program, operation.operation),
        kind: operationKind(program, builder, operation),
        namespace: namespaceName(operation.operation.namespace),
        summary: getSummary(program, operation.operation),
        description: getDoc(program, operation.operation),
        tags: getAllTags(program, operation.operation),
        parameters,
        request_body: mergedRequestBody(builder, group.operations),
        responses: endpointResponses(program, builder, group.operations.flatMap((item) => item.responses)),
        cli: cliMetadata(program, operation),
        command: commandMetadata(program, builder, operation, parameters),
        tool: toolMetadata(program, operation),
        security: mergedOperationSecurity(builder, group.operations, operationsAuth, defaultSecurity),
    });
    if (Object.keys(extensions).length > 0) {
        output.extensions = extensions;
    }
    return output;
}
function validateSharedRouteMetadata(program, builder, operations, canonical) {
    const canonicalNamespace = namespaceName(canonical.operation.namespace);
    const canonicalCLI = stableJSONString(cliMetadata(program, canonical));
    const canonicalCommand = stableJSONString(getCommand({ program }, canonical.operation));
    const canonicalQuery = isQuery({ program }, canonical.operation);
    const canonicalTool = stableJSONString(toolMetadata(program, canonical));
    const canonicalAuthz = stableJSONString(getAuthz({ program }, canonical.operation));
    const canonicalManual = isManual({ program }, canonical.operation);
    const canonicalExtensions = stableJSONString(operationVendorExtensions(program, builder, canonical.operation));
    for (const operation of operations) {
        if (namespaceName(operation.operation.namespace) !== canonicalNamespace) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible namespace");
        }
        if (stableJSONString(cliMetadata(program, operation)) !== canonicalCLI) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible cli metadata");
        }
        if (stableJSONString(getCommand({ program }, operation.operation)) !== canonicalCommand) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible command metadata");
        }
        if (isQuery({ program }, operation.operation) !== canonicalQuery) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible query metadata");
        }
        if (stableJSONString(toolMetadata(program, operation)) !== canonicalTool) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible tool metadata");
        }
        if (stableJSONString(getAuthz({ program }, operation.operation)) !== canonicalAuthz) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible authz metadata");
        }
        if (isManual({ program }, operation.operation) !== canonicalManual) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible manual metadata");
        }
        if (stableJSONString(operationVendorExtensions(program, builder, operation.operation)) !== canonicalExtensions) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible operation extensions");
        }
    }
}
function operationKind(program, builder, operation) {
    const command = getCommand({ program }, operation.operation);
    const query = isQuery({ program }, operation.operation);
    const method = operation.verb.toLowerCase();
    if (builder.requireExplicitOperationKind && getOperationId(program, operation.operation) === undefined) {
        builder.invalidOperationKind("an explicit @operationId is required", operation.operation);
    }
    if (command && query) {
        builder.invalidOperationKind("@apigen.command and @apigen.query are mutually exclusive", operation.operation);
        return "command";
    }
    if (command) {
        if (method === "get" || method === "head") {
            builder.invalidOperationKind(`${method.toUpperCase()} operations cannot be commands`, operation.operation);
        }
        return "command";
    }
    if (query) {
        if (method !== "get" && method !== "head" && method !== "post") {
            builder.invalidOperationKind("explicit queries may use only GET, HEAD, or POST", operation.operation);
        }
        return "query";
    }
    if (method === "get" || method === "head") {
        return "query";
    }
    if (builder.requireExplicitOperationKind) {
        builder.invalidOperationKind(`${method.toUpperCase()} operations require @apigen.command or an explicit @apigen.query exemption`, operation.operation);
    }
    return "query";
}
const auditActionPattern = /^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$/;
const stableNamePattern = /^[a-z][a-z0-9_]*$/;
const jobKindPattern = /^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*$/;
function commandMetadata(program, builder, operation, parameters) {
    const options = getCommand({ program }, operation.operation);
    if (!options) {
        return undefined;
    }
    if (getOperationId(program, operation.operation) === undefined) {
        builder.invalidCommand("an explicit @operationId is required", operation.operation);
    }
    const successAction = options.audit.successAction?.trim();
    const guarantee = options.audit.guarantee;
    if (options.audit.required && !successAction) {
        builder.invalidCommand("audit.successAction is required when audit.required is true", operation.operation);
    }
    if (successAction && !auditActionPattern.test(successAction)) {
        builder.invalidCommand(`audit.successAction ${JSON.stringify(successAction)} must be a stable dotted lower_snake_case name`, operation.operation);
    }
    let execution;
    if (options.execution) {
        const jobKind = options.execution.jobKind.trim();
        const resourceKind = options.execution.resourceKind.trim();
        const initialEvent = options.execution.initialEvent.trim();
        const initialState = options.execution.initialState.trim();
        const statusOperation = options.execution.statusOperation.trim();
        const eventsOperation = options.execution.eventsOperation.trim();
        if (!jobKindPattern.test(jobKind)) {
            builder.invalidCommand("execution.jobKind must be a stable lower_snake_case identifier", operation.operation);
        }
        if (!stableNamePattern.test(resourceKind) || !stableNamePattern.test(initialState)) {
            builder.invalidCommand("execution resourceKind and initialState must be stable lower_snake_case names", operation.operation);
        }
        if (!auditActionPattern.test(initialEvent)) {
            builder.invalidCommand("execution.initialEvent must be a stable dotted lower_snake_case name", operation.operation);
        }
        if (!statusOperation || !eventsOperation || statusOperation === eventsOperation) {
            builder.invalidCommand("execution statusOperation and eventsOperation must be distinct operation IDs", operation.operation);
        }
        execution = {
            mode: options.execution.mode,
            guarantee: options.execution.guarantee,
            job_kind: jobKind,
            resource_kind: resourceKind,
            initial_event: initialEvent,
            initial_state: initialState,
            status_operation: statusOperation,
            events_operation: eventsOperation,
            cancellation: options.execution.cancellation,
        };
    }
    const additionalExposures = [...(options.additionalExposures ?? [])];
    if (new Set(additionalExposures).size !== additionalExposures.length) {
        builder.invalidCommand("additionalExposures must not contain duplicates", operation.operation);
    }
    additionalExposures.sort();
    const emittedParameters = parameters ?? [];
    const pathParameters = emittedParameters.filter((parameter) => parameter.in === "path");
    let targetParameter = options.targetParameter?.trim();
    if (!targetParameter && pathParameters.length === 1) {
        targetParameter = pathParameters[0].name;
    }
    else if (!targetParameter && pathParameters.length > 1) {
        builder.invalidCommand("targetParameter is required when a route has multiple path parameters", operation.operation);
    }
    let target;
    if (targetParameter) {
        const parameter = pathParameters.find((candidate) => candidate.name === targetParameter);
        if (!parameter || !parameter.required) {
            builder.invalidCommand(`targetParameter ${JSON.stringify(targetParameter)} must name a required path parameter`, operation.operation);
        }
        else {
            target = { parameter: parameter.name, type: parameter.name };
        }
    }
    const hasRequiredHeader = (name) => emittedParameters.some((parameter) => parameter.in === "header" && parameter.required && parameter.name.toLowerCase() === name.toLowerCase());
    const method = operation.verb.toLowerCase();
    const idempotency = hasRequiredHeader("Idempotency-Key") ? "required" : undefined;
    const concurrency = hasRequiredHeader("If-Match") ? "if-match" : undefined;
    if (method === "post" && idempotency === undefined) {
        builder.invalidCommand("POST commands require a required Idempotency-Key header", operation.operation);
    }
    if (method === "patch" && concurrency === undefined) {
        builder.invalidCommand("PATCH commands require a required If-Match header", operation.operation);
    }
    const authz = getAuthz({ program }, operation.operation);
    const authzMode = typeof authz?.mode === "string" ? authz.mode : undefined;
    const privilege = typeof authz?.privilege === "string" ? authz.privilege : undefined;
    return prune({
        owner: namespaceName(operation.operation.namespace) ?? "",
        audit: prune({ required: options.audit.required, success_action: successAction, guarantee }),
        execution,
        additional_exposures: additionalExposures.length > 0 ? additionalExposures : undefined,
        target,
        idempotency,
        concurrency,
        authz_mode: authzMode,
        privilege,
    });
}
function stableJSONString(value) {
    return JSON.stringify(sortJSONValue(value));
}
function sortJSONValue(value) {
    if (Array.isArray(value)) {
        return value.map((item) => sortJSONValue(item));
    }
    if (value && typeof value === "object") {
        const output = {};
        for (const key of Object.keys(value).sort()) {
            output[key] = sortJSONValue(value[key]);
        }
        return output;
    }
    return value;
}
function operationVendorExtensions(program, builder, operation) {
    const extensions = new Map(getExtensions(program, operation).entries());
    for (const [key, value] of operationExtensionDecoratorLiterals(operation)) {
        extensions.set(key, value);
    }
    const entries = [...extensions.entries()].sort(([left], [right]) => left.localeCompare(right));
    const output = [];
    for (const [key, value] of entries) {
        if (!key.startsWith("x-")) {
            builder.invalidExtensionKey(key, operation);
            continue;
        }
        if (isReservedExtensionKey(key)) {
            builder.reservedExtension(key, operation);
            continue;
        }
        if (!isJSONCompatible(value)) {
            builder.invalidExtensionValue(key, key, operation);
            continue;
        }
        output.push([key, value]);
    }
    return output;
}
function operationExtensionDecoratorLiterals(operation) {
    const decorators = operation.node.decorators ?? [];
    const output = [];
    for (const decorator of decorators) {
        if (decoratorName(decorator.target) !== "extension") {
            continue;
        }
        const args = decorator.arguments ?? [];
        const key = args[0]?.value;
        if (typeof key !== "string" || args[1] === undefined) {
            continue;
        }
        const value = extensionLiteralValue(args[1]);
        if (value.ok) {
            output.push([key, value.value]);
        }
    }
    return output;
}
function decoratorName(target) {
    if (target !== undefined && "sv" in target && typeof target.sv === "string") {
        return target.sv;
    }
    if (target !== undefined && "id" in target && typeof target.id?.sv === "string") {
        return target.id.sv;
    }
    return undefined;
}
function extensionLiteralValue(node) {
    if (Array.isArray(node.values)) {
        const values = [];
        for (const item of node.values) {
            const value = extensionLiteralValue(item);
            if (!value.ok) {
                return value;
            }
            values.push(value.value);
        }
        return { ok: true, value: values };
    }
    if (Array.isArray(node.properties)) {
        const output = {};
        for (const property of node.properties) {
            const key = extensionObjectPropertyName(property);
            if (key === undefined || property.value === undefined) {
                return { ok: false };
            }
            const value = extensionLiteralValue(property.value);
            if (!value.ok) {
                return value;
            }
            output[key] = value.value;
        }
        return { ok: true, value: output };
    }
    switch (typeof node.value) {
        case "string":
        case "boolean":
            return { ok: true, value: node.value };
        case "number":
            return Number.isFinite(node.value) ? { ok: true, value: node.value } : { ok: false };
    }
    if (node.target?.sv === "null" && node.arguments?.length === 0) {
        return { ok: true, value: null };
    }
    return { ok: false };
}
function extensionObjectPropertyName(property) {
    if (typeof property.id?.sv === "string") {
        return property.id.sv;
    }
    if (typeof property.id?.value === "string") {
        return property.id.value;
    }
    return undefined;
}
function isReservedExtensionKey(key) {
    return key === "x-authz" || key === "x-agent" || key.startsWith("x-apigen-");
}
function isJSONCompatible(value) {
    if (value === null) {
        return true;
    }
    switch (typeof value) {
        case "string":
        case "boolean":
            return true;
        case "number":
            return Number.isFinite(value);
        case "object":
            if (Array.isArray(value)) {
                return value.every((item) => isJSONCompatible(item));
            }
            return Object.values(value).every((item) => isJSONCompatible(item));
        default:
            return false;
    }
}
function cliMetadata(program, operation) {
    const cli = getCLI({ program }, operation.operation);
    if (!cli) {
        return undefined;
    }
    return prune({
        command: cli.command,
        args: cli.args?.map((arg) => prune({
            source: arg.source,
            name: arg.name,
            display_name: arg.displayName,
        })),
        body_input: cli.bodyInput,
        confirm: cli.confirm,
        output: cli.output
            ? prune({
                mode: cli.output.mode,
                table_columns: cli.output.tableColumns,
                quiet_fields: cli.output.quietFields,
            })
            : undefined,
        pagination: cli.pagination
            ? prune({
                items_field: cli.pagination.itemsField,
                next_page_token_field: cli.pagination.nextPageTokenField,
            })
            : undefined,
    });
}
function toolMetadata(program, operation) {
    const tool = getTool({ program }, operation.operation);
    if (!tool) {
        return undefined;
    }
    const metadata = {};
    for (const [key, value] of Object.entries(tool.metadata ?? {}).sort(([left], [right]) => left.localeCompare(right))) {
        if (!key.startsWith("x-") || isReservedExtensionKey(key) || !isJSONCompatible(value)) {
            reportDiagnostic(program, {
                code: !key.startsWith("x-") ? "invalid-extension-key" : isReservedExtensionKey(key) ? "reserved-extension" : "invalid-extension-value",
                target: operation.operation,
                format: !key.startsWith("x-") || isReservedExtensionKey(key) ? { key } : { key, path: key },
            });
            continue;
        }
        metadata[key] = value;
    }
    return prune({
        name: tool.name,
        description: tool.description,
        effect: tool.effect,
        confirmation: tool.confirmation ?? defaultToolConfirmation(tool.effect),
        tags: tool.tags,
        input: tool.input
            ? {
                fields: tool.input.fields?.map((field) => prune({
                    source: field.source,
                    name: field.name,
                    mode: field.mode,
                    alias: field.alias,
                    context_key: field.contextKey,
                    description: field.description,
                    default: field.default,
                })),
            }
            : undefined,
        output: {
            mode: tool.output.mode,
            select: tool.output.select?.map(toolProjectionMetadata),
            cursor: tool.output.cursor
                ? prune({
                    source: tool.output.cursor.source,
                    target: tool.output.cursor.target,
                    has_more_target: tool.output.cursor.hasMoreTarget,
                })
                : undefined,
        },
        metadata: Object.keys(metadata).length > 0 ? metadata : undefined,
    });
}
function toolProjectionMetadata(projection) {
    return prune({
        source: projection.source,
        target: projection.target,
        select: projection.select?.map(toolProjectionMetadata),
        count_as: projection.countAs,
    });
}
function defaultToolConfirmation(effect) {
    switch (effect) {
        case "read":
            return "never";
        case "destructive":
            return "always";
        default:
            return "policy";
    }
}
function endpointParameter(program, builder, parameter) {
    const param = parameter.param;
    if (parameter.type === "cookie") {
        builder.unsupportedCookie(param);
    }
    return prune({
        name: "name" in parameter ? parameter.name : param.name,
        in: parameter.type,
        required: parameter.type === "path" ? true : !param.optional,
        description: getDoc(program, param),
        explode: shouldEmitExplode(builder, param.type, parameter) ? parameter.explode : undefined,
        schema: withSchemaConstraints(program, param, parameterSchemaRef(builder, param.type, `parameter ${param.name}`)),
    });
}
function mergedParameters(program, builder, operations) {
    const output = [];
    const byKey = new Map();
    for (const operation of operations) {
        for (const parameter of operation.parameters.parameters) {
            const next = endpointParameter(program, builder, parameter);
            const key = `${next.in.toLowerCase()}:${next.name.toLowerCase()}`;
            const existing = byKey.get(key);
            if (!existing) {
                byKey.set(key, next);
                output.push(next);
                continue;
            }
            const merged = mergeParameter(builder, existing, next, operation.operation);
            Object.assign(existing, merged);
        }
    }
    return output.length > 0 ? output : undefined;
}
function mergeParameter(builder, left, right, operation) {
    if (left.in !== right.in || left.required !== right.required || left.explode !== right.explode) {
        builder.unsupportedSharedRoute(operation, `incompatible parameter ${left.name}`);
        return left;
    }
    const schema = mergeParameterSchemas(left.schema, right.schema);
    if (!schema) {
        builder.unsupportedSharedRoute(operation, `incompatible parameter schema ${left.name}`);
        return left;
    }
    return prune({
        ...left,
        description: left.description ?? right.description,
        schema,
    });
}
function mergeParameterSchemas(left, right) {
    if (JSON.stringify(left) === JSON.stringify(right)) {
        return left;
    }
    const leftValues = literalSchemaEnumValues(left);
    const rightValues = literalSchemaEnumValues(right);
    if (leftValues && rightValues) {
        return { type: "string", enum: uniqueStrings([...leftValues, ...rightValues]) };
    }
    return undefined;
}
function literalSchemaEnumValues(schema) {
    if (schema.type !== "string" || schema.ref || schema.format || schema.items || schema.additional_properties) {
        return undefined;
    }
    return schema.enum ? schema.enum : [];
}
function parameterSchemaRef(builder, type, context) {
    if (type.kind === "String") {
        return { type: "string", enum: [type.value] };
    }
    if (type.kind === "Union") {
        const enumValues = stringLiteralUnionValues(type);
        if (enumValues) {
            return { type: "string", enum: enumValues };
        }
    }
    return builder.schemaRef(type, context);
}
function shouldEmitExplode(builder, type, parameter) {
    if (!("explode" in parameter) || parameter.explode === undefined) {
        return false;
    }
    if (parameter.type !== "query" && parameter.type !== "header") {
        return false;
    }
    const schema = parameterSchemaRef(builder, type, `parameter ${parameter.param.name}`);
    return schema.type === "array" || parameter.explode === true;
}
function requestBody(builder, body) {
    if (!body) {
        return undefined;
    }
    return prune({
        required: body.property ? !body.property.optional : true,
        contents: bodyContents(builder, body, "request body"),
    });
}
function mergedRequestBody(builder, operations) {
    let output;
    for (const operation of operations) {
        const next = requestBody(builder, operation.parameters.body);
        if (!next) {
            continue;
        }
        if (!output) {
            output = next;
            continue;
        }
        if (JSON.stringify(output) !== JSON.stringify(next)) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible request bodies");
        }
    }
    return output;
}
function endpointResponses(program, builder, responses) {
    const byStatus = new Map();
    const order = [];
    for (const httpResponse of responses) {
        const response = endpointResponse(program, builder, httpResponse);
        const existing = byStatus.get(response.status_code);
        if (!existing) {
            byStatus.set(response.status_code, response);
            order.push(response.status_code);
            continue;
        }
        existing.description = existing.description || response.description;
        existing.headers = mergeHeaders(existing.headers, response.headers);
        existing.contents = mergeContents(builder, httpResponse, response.status_code, existing.contents, response.contents);
        existing.extensions = mergeResponseExtensions(existing.extensions, response.extensions);
    }
    return order.map((status) => byStatus.get(status));
}
function endpointResponse(program, builder, response) {
    if (typeof response.statusCodes !== "number") {
        builder.unsupportedResponseStatus(response);
    }
    const firstContent = response.responses[0];
    const shape = response.type.kind === "Model" ? getResponseShape({ program }, response.type) : undefined;
    const extensions = shape
        ? {
            "x-apigen-response-shape": prune({
                kind: shape.kind,
                body_type: shape.bodyType,
            }),
        }
        : undefined;
    return prune({
        status_code: typeof response.statusCodes === "number" ? response.statusCodes : 0,
        description: response.description ?? "The request has completed.",
        headers: firstContent ? responseHeaders(program, builder, firstContent) : undefined,
        contents: responseContents(builder, response.responses),
        extensions,
    });
}
function mergeHeaders(left, right) {
    if (!left || left.length === 0) {
        return right;
    }
    if (!right || right.length === 0) {
        return left;
    }
    const output = [...left];
    const seen = new Set(left.map((header) => header.name.toLowerCase()));
    for (const header of right) {
        const key = header.name.toLowerCase();
        if (seen.has(key)) {
            continue;
        }
        seen.add(key);
        output.push(header);
    }
    return output;
}
function mergeContents(builder, response, statusCode, left, right) {
    if (!left || left.length === 0) {
        return right;
    }
    if (!right || right.length === 0) {
        return left;
    }
    const output = [...left];
    for (const content of right) {
        if (output.some((existing) => JSON.stringify(existing) === JSON.stringify(content))) {
            continue;
        }
        if (output.some((existing) => sameContentType(existing.content_type, content.content_type))) {
            builder.unsupportedResponseContent(response, statusCode, content.content_type);
            continue;
        }
        output.push(content);
    }
    return output;
}
function sameContentType(left, right) {
    return left.trim().toLowerCase() === right.trim().toLowerCase();
}
function mergeResponseExtensions(left, right) {
    if (!left || Object.keys(left).length === 0) {
        return right;
    }
    if (!right || Object.keys(right).length === 0) {
        return left;
    }
    return { ...left, ...right };
}
function responseContents(builder, contents) {
    const output = [];
    for (const content of contents) {
        if (!content.body) {
            continue;
        }
        output.push(...bodyContents(builder, content.body, "response body"));
    }
    return output.length > 0 ? output : undefined;
}
function bodyContents(builder, body, context) {
    switch (body.bodyKind) {
        case "single":
            return body.contentTypes.map((contentType) => prune({
                content_type: contentType,
                body_kind: bodyKindForSingle(body.type, contentType),
                schema: schemaRefForContent(builder, body.type, contentType, context),
            }));
        case "file":
            return body.contentTypes.map((contentType) => prune({
                content_type: contentType,
                body_kind: "file",
                schema: fileSchemaRef(body.isText),
            }));
        case "multipart":
            return body.contentTypes.map((contentType) => prune({
                content_type: contentType,
                body_kind: "multipart",
                parts: body.parts.map((part, idx) => multipartPart(builder, part, idx)),
            }));
    }
}
function multipartPart(builder, part, idx) {
    const bodyKind = part.body.bodyKind === "file" ? "file" : bodyKindForSingle(part.body.type, part.body.contentTypes[0] ?? "application/json");
    const schema = part.body.bodyKind === "file"
        ? fileSchemaRef(part.body.isText)
        : schemaRefForContent(builder, part.body.type, part.body.contentTypes[0] ?? "application/json", `multipart part ${part.name ?? idx}`);
    return prune({
        name: part.partKind === "model" ? part.property.name : `part${idx + 1}`,
        wire_name: part.name,
        part_kind: part.partKind,
        repeated: part.multi ? true : undefined,
        required: !part.optional,
        description: "property" in part && part.property ? getDoc(builder.program, part.property) : undefined,
        content_type: part.body.contentTypes[0],
        body_kind: bodyKind,
        filename: part.filename !== undefined ? true : undefined,
        schema,
    });
}
function bodyKindForSingle(type, contentType) {
    const normalized = contentType.toLowerCase();
    if (normalized === "application/x-www-form-urlencoded") {
        return "form_urlencoded";
    }
    if (normalized.startsWith("text/")) {
        return "text";
    }
    if (normalized === "application/octet-stream" || isBytesType(type)) {
        return "binary";
    }
    return "json";
}
function schemaRefForContent(builder, type, contentType, context) {
    const kind = bodyKindForSingle(type, contentType);
    if ((kind === "binary" || kind === "file") && isBytesType(type)) {
        return { type: "string", format: "binary" };
    }
    return builder.schemaRef(type, context);
}
function fileSchemaRef(isText) {
    return isText ? { type: "string" } : { type: "string", format: "binary" };
}
function responseHeaders(program, builder, content) {
    if (!content.headers) {
        return undefined;
    }
    const headers = Object.entries(content.headers).map(([name, property]) => prune({
        name,
        required: !property.optional,
        description: getDoc(program, property),
        schema: withSchemaConstraints(program, property, builder.schemaRef(property.type, `response header ${name}`)),
    }));
    return headers.length > 0 ? headers : undefined;
}
function collectSecuritySchemes(builder, auths, target) {
    const schemes = {};
    for (const auth of auths) {
        if (auth.type === "noAuth") {
            continue;
        }
        if (!isSupportedAuth(auth)) {
            builder.unsupportedAuth("service authentication", unsupportedAuthReason(auth), target);
            continue;
        }
        schemes[auth.id] = securityScheme(auth);
    }
    return schemes;
}
function operationSecurity(builder, operation, operationAuth, defaultSecurity) {
    if (!operationAuth) {
        return undefined;
    }
    const security = authRequirements(builder, operationAuth, operation.operation, `operation ${operation.operation.name} authentication`, defaultSecurity === undefined);
    if (sameSecurity(security, defaultSecurity)) {
        return undefined;
    }
    return security;
}
function mergedOperationSecurity(builder, operations, operationsAuth, defaultSecurity) {
    let output;
    for (const operation of operations) {
        const security = operationSecurity(builder, operation, operationsAuth.get(operation.operation), defaultSecurity);
        if (output === undefined) {
            output = security;
            continue;
        }
        if (!sameSecurity(output, security)) {
            builder.unsupportedSharedRoute(operation.operation, "incompatible authentication");
        }
    }
    return output;
}
function authRequirements(builder, auth, target, context, allowNoAuth) {
    const requirements = [];
    for (const option of auth.options) {
        const requirement = {};
        for (const ref of option.all) {
            if (ref.kind === "noAuth") {
                if (allowNoAuth) {
                    continue;
                }
                builder.unsupportedAuth(context, "APIGen IR v4 does not support NoAuth operation overrides for secured services.", target);
                continue;
            }
            if (ref.kind === "oauth2") {
                builder.unsupportedAuth(context, "oauth2 authentication is not supported by APIGen.", target);
                continue;
            }
            if (!isSupportedAuth(ref.auth)) {
                builder.unsupportedAuth(context, unsupportedAuthReason(ref.auth), target);
                continue;
            }
            requirement[ref.auth.id] = authScopes(ref);
        }
        if (Object.keys(requirement).length > 0) {
            requirements.push(requirement);
        }
    }
    return requirements.length > 0 ? requirements : undefined;
}
function isSupportedAuth(auth) {
    if (auth.type === "http") {
        return auth.scheme.toLowerCase() === "bearer";
    }
    if (auth.type === "apiKey") {
        return auth.in === "header" && auth.name === "X-API-Key";
    }
    return false;
}
function unsupportedAuthReason(auth) {
    if (auth.type === "http") {
        return `http ${auth.scheme} authentication is not supported by APIGen. Use Bearer HTTP auth.`;
    }
    if (auth.type === "apiKey") {
        if (auth.in !== "header") {
            return `apiKey authentication in ${auth.in} is not supported by APIGen. Use ApiKeyAuth<ApiKeyLocation.header, "X-API-Key">.`;
        }
        if (auth.name !== "X-API-Key") {
            return `header API key name ${auth.name} is not supported by APIGen. Use X-API-Key.`;
        }
    }
    return `${auth.type} authentication is not supported by APIGen.`;
}
function authScopes(ref) {
    if (ref.kind === "oauth2") {
        return [...ref.scopes];
    }
    return [];
}
function sameSecurity(left, right) {
    return JSON.stringify(left ?? []) === JSON.stringify(right ?? []);
}
function securityScheme(scheme) {
    switch (scheme.type) {
        case "http":
            return { type: "http", scheme: scheme.scheme };
        case "apiKey":
            return { type: "apiKey", in: scheme.in, name: scheme.name };
        default:
            return { type: scheme.type };
    }
}
function withSchemaConstraints(program, target, schema) {
    const candidates = schemaConstraintCandidates(target);
    const minimum = firstSchemaConstraint(candidates, (candidate) => getMinValue(program, candidate));
    const maximum = firstSchemaConstraint(candidates, (candidate) => getMaxValue(program, candidate));
    const minLength = firstSchemaConstraint(candidates, (candidate) => getMinLength(program, candidate));
    const maxLength = firstSchemaConstraint(candidates, (candidate) => getMaxLength(program, candidate));
    return prune({
        ...schema,
        minimum,
        maximum,
        min_length: minLength,
        max_length: maxLength,
    });
}
function schemaConstraintCandidates(target) {
    const candidates = [target];
    let current = target.kind === "ModelProperty" ? target.type : target;
    if (current !== target) {
        candidates.push(current);
    }
    while (current?.kind === "Scalar" && current.baseScalar) {
        current = current.baseScalar;
        candidates.push(current);
    }
    return candidates;
}
function firstSchemaConstraint(candidates, read) {
    for (const candidate of candidates) {
        const value = read(candidate);
        if (value !== undefined) {
            return value;
        }
    }
    return undefined;
}
function scalarSchemaRef(scalar) {
    for (let current = scalar; current; current = current.baseScalar) {
        switch (current.name) {
            case "string":
                return { type: "string" };
            case "boolean":
                return { type: "boolean" };
            case "int8":
            case "int16":
            case "int32":
                return { type: "integer", format: "int32" };
            case "integer":
            case "safeint":
            case "int64":
                return { type: "integer", format: "int64" };
            case "float32":
                return { type: "number", format: "float" };
            case "float64":
            case "decimal":
                return { type: "number", format: "double" };
            case "utcDateTime":
            case "offsetDateTime":
                return { type: "string", format: "date-time" };
            case "plainDate":
                return { type: "string", format: "date" };
            case "bytes":
                return { type: "string", format: "byte" };
        }
    }
    return { type: "string" };
}
function isBytesType(type) {
    if (type.kind !== "Scalar") {
        return false;
    }
    for (let current = type; current; current = current.baseScalar) {
        if (current.name === "bytes") {
            return true;
        }
    }
    return false;
}
function enumValues(type) {
    return [...type.members.values()].map((member) => String(member.value ?? member.name));
}
function stringLiteralUnionValues(type) {
    const values = [];
    for (const variant of type.variants.values()) {
        if (variant.type.kind !== "String") {
            return undefined;
        }
        values.push(variant.type.value);
    }
    return values;
}
function uniqueStrings(values) {
    const output = [];
    const seen = new Set();
    for (const value of values) {
        if (seen.has(value)) {
            continue;
        }
        seen.add(value);
        output.push(value);
    }
    return output;
}
function schemaNamePart(value) {
    const parts = value.split(/[^A-Za-z0-9]+/).filter(Boolean);
    const name = parts.map((part) => part[0].toUpperCase() + part.slice(1)).join("");
    return name || "Variant";
}
function isNamedUserModel(type) {
    return type.name !== "" && !isArrayModelType(type) && !isRecordModelType(type);
}
function prune(value) {
    if (Array.isArray(value)) {
        return value.filter((item) => item !== undefined).map((item) => prune(item));
    }
    if (value && typeof value === "object") {
        if (isSecurityRequirementObject(value)) {
            return value;
        }
        const output = {};
        for (const [key, child] of Object.entries(value)) {
            if (child === undefined) {
                continue;
            }
            if (Array.isArray(child) && child.length === 0) {
                continue;
            }
            if (key === "extensions") {
                output[key] = child;
                continue;
            }
            output[key] = prune(child);
        }
        return output;
    }
    return value;
}
function isSecurityRequirementObject(value) {
    const entries = Object.entries(value);
    return entries.length > 0 && entries.every(([, child]) => Array.isArray(child));
}
