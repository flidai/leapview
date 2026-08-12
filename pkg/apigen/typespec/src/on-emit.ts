import {
  emitFile,
  getAllTags,
  getDoc,
  getDiscriminatedUnion,
  getDiscriminatedUnionFromInheritance,
  getDiscriminator,
  getMaxLength,
  getMaxValue,
  getMinLength,
  getMinValue,
  getOverloadedOperation,
  getOverloads,
  getService,
  getSummary,
  isArrayModelType,
  isRecordModelType,
  type EmitContext,
  type Enum,
  type Model,
  type ModelProperty,
  type Namespace,
  type Operation,
  type Program,
  type Scalar,
  type Type,
  type Union,
} from "@typespec/compiler";
import {
  getAllHttpServices,
  getServers,
  isOverloadSameEndpoint,
  isSharedRoute,
  resolveAuthentication,
  type AuthenticationReference,
  type HttpAuth,
  type HttpAuthRef,
  type HttpOperation,
  type HttpOperationParameter,
  type HttpOperationPart,
  type HttpOperationResponse,
  type HttpOperationResponseContent,
  type HttpPayloadBody,
  type HttpService,
} from "@typespec/http";
import { getExtensions, getOperationId, getTagsMetadata, resolveInfo, resolveOperationId } from "@typespec/openapi";
import {
  getAuthz,
  getAuditPayload,
  getAuditSchema,
  getCLI,
  getCommand,
  getContracts,
  getMetadata,
  getPackages,
  getResponseShape,
  getSensitivity,
  getTool,
  getTransportErrors,
  getUI,
  isManual,
  isQuery,
} from "./decorators.js";
import { type EmitterOptions, reportDiagnostic } from "./lib.js";

interface Document {
  schema_version: "v4";
  api: { base_path: string };
  info: { title: string; version: string; description?: string; namespace?: string };
  openapi?: {
    version?: string;
    tag_order?: string[];
    security?: Record<string, string[]>[];
    security_schemes?: Record<string, SecurityScheme>;
  };
  servers?: Server[];
  tags?: Tag[];
  schemas?: Record<string, Schema>;
  contracts?: Contract[];
  endpoints?: Endpoint[];
  transport_errors?: {
    schema: SchemaRef;
    content_type: string;
    failures: Record<string, { status_code: number; code: string; public_detail: string }>;
  };
  extensions?: Record<string, unknown>;
}

interface Server {
  url: string;
  description?: string;
}

interface Tag {
  name: string;
  description?: string;
}

interface SecurityScheme {
  type: string;
  in?: string;
  name?: string;
  scheme?: string;
}

interface Endpoint {
  method: string;
  path: string;
  operation_id: string;
  kind: "command" | "query";
  namespace?: string;
  summary?: string;
  description?: string;
  tags?: string[];
  parameters?: Parameter[];
  request_body?: RequestBody;
  responses: Response[];
  cli?: unknown;
  command?: Command;
  tool?: unknown;
  security?: Record<string, string[]>[];
  extensions?: Record<string, unknown>;
}

interface Command {
  owner: string;
  audit: {
    required: boolean;
    success_action?: string;
    guarantee?: "transactional" | "best-effort";
    payload?: {
      schema: SchemaRef;
      schema_version: number;
      retention: "short" | "standard" | "security";
      fields: Array<{ name: string; sensitivity: "public" | "internal" | "pii" | "secret" }>;
    };
  };
  execution?: {
    mode: "async";
    guarantee: "transactional";
    job_kind: string;
    resource_kind: string;
    initial_event: string;
    initial_state: string;
    status_operation: string;
    events_operation: string;
    cancellation: "supported" | "unsupported";
  };
  failures?: Array<{ kind: string; status_code: number; code: string; public_detail: string }>;
  additional_exposures?: string[];
  ui?: { action_id: string };
  target?: { parameter: string; type: string };
  idempotency?: "required";
  concurrency?: "if-match";
  authz_mode?: string;
  privilege?: string;
}

interface Contract {
  name: string;
  schema: SchemaRef;
  kind?: string;
  tags?: string[];
  description?: string;
  extensions?: Record<string, unknown>;
}

interface DecoratorNodeLike {
  target?: MemberExpressionNodeLike | IdentifierNodeLike;
  arguments?: readonly ExtensionLiteralNodeLike[];
}

interface IdentifierNodeLike {
  sv?: unknown;
}

interface MemberExpressionNodeLike {
  id?: IdentifierNodeLike;
}

interface ExtensionLiteralNodeLike {
  value?: unknown;
  target?: IdentifierNodeLike;
  arguments?: readonly ExtensionLiteralNodeLike[];
  id?: IdentifierNodeLike;
  properties?: readonly ExtensionObjectPropertyNodeLike[];
  values?: readonly ExtensionLiteralNodeLike[];
}

interface ExtensionObjectPropertyNodeLike {
  id?: IdentifierNodeLike & { value?: unknown };
  value?: ExtensionLiteralNodeLike;
}

type LiteralConversionResult =
  | { ok: true; value: unknown }
  | { ok: false };

interface Parameter {
  name: string;
  in: string;
  required?: boolean;
  description?: string;
  explode?: boolean;
  schema: SchemaRef;
}

interface RequestBody {
  required?: boolean;
  description?: string;
  contents: BodyContent[];
}

interface Response {
  status_code: number;
  description: string;
  headers?: Header[];
  contents?: BodyContent[];
  extensions?: Record<string, unknown>;
}

interface BodyContent {
  content_type: string;
  body_kind: "json" | "text" | "binary" | "file" | "form_urlencoded" | "multipart";
  schema?: SchemaRef;
  any_of?: SchemaRef[];
  parts?: MultipartPart[];
}

interface MultipartPart {
  name: string;
  wire_name?: string;
  part_kind?: "model" | "tuple";
  repeated?: boolean;
  required?: boolean;
  description?: string;
  content_type?: string;
  body_kind?: "json" | "text" | "binary" | "file";
  filename?: boolean;
  schema?: SchemaRef;
}

interface Header {
  name: string;
  required?: boolean;
  description?: string;
  schema: SchemaRef;
}

interface Schema {
  type: string;
  namespace?: string;
  title?: string;
  description?: string;
  properties?: Record<string, SchemaProperty>;
  property_order?: string[];
  required?: string[];
  items?: SchemaRef;
  base?: SchemaRef;
  one_of?: SchemaRef[];
  discriminator?: { property_name: string; mapping: Record<string, string> };
  enum?: string[];
  extensions?: Record<string, unknown>;
}

interface SchemaProperty {
  description?: string;
  schema: SchemaRef;
  extensions?: Record<string, unknown>;
}

interface SchemaRef {
  ref?: string;
  type?: string;
  format?: string;
  enum?: string[];
  minimum?: number;
  maximum?: number;
  min_length?: number;
  max_length?: number;
  items?: SchemaRef;
  additional_properties?: { any?: boolean; schema?: SchemaRef };
}

class IRBuilder {
  readonly schemas = new Map<string, Model>();
  readonly enums = new Map<string, Enum>();
  readonly unions = new Map<string, Union>();
  readonly syntheticSchemas = new Map<string, Schema>();
  private readonly emittedSchemas = new Set<string>();
  private readonly emittedEnums = new Set<string>();
  private readonly emittedUnions = new Set<string>();
  private readonly emittedSyntheticSchemas = new Set<string>();
  private failed = false;

  constructor(readonly program: Program, readonly requireExplicitOperationKind = false) {}

  hasFailed() {
    return this.failed;
  }

  schemaRef(type: Type, context: string): SchemaRef {
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

  namedSchemaRef(type: Type, context: string): SchemaRef {
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

  unsupportedType(type: Type, context: string) {
    this.unsupported(type, context);
  }

  unsupportedAuth(context: string, reason: string, target: Type | Operation | Namespace) {
    this.report("unsupported-auth", { context, reason }, target);
  }

  unsupportedSharedRoute(operation: Operation, reason: string) {
    this.report("unsupported-shared-route", { reason }, operation);
  }

  unsupportedCookie(target: Type | Operation) {
    this.report("unsupported-cookie", {}, target);
  }

  invalidCommand(reason: string, target: Operation) {
    this.report("invalid-command", { reason }, target);
  }

  invalidOperationKind(reason: string, target: Operation) {
    this.report("invalid-operation-kind", { reason }, target);
  }

  unsupportedResponseStatus(response: HttpOperationResponse) {
    this.report(
      "unsupported-response-status",
      { status: JSON.stringify(response.statusCodes), operation: response.type.kind },
      response.type,
    );
  }

  unsupportedResponseContent(response: HttpOperationResponse, status: number, contentType: string) {
    this.report(
      "unsupported-response-content",
      { operation: response.type.kind, status: String(status), contentType },
      response.type,
    );
  }

  reservedExtension(key: string, target: Operation | Model | ModelProperty | Enum | Namespace) {
    this.report("reserved-extension", { key }, target);
  }

  invalidExtensionKey(key: string, target: Operation | Model | ModelProperty | Enum | Namespace) {
    this.report("invalid-extension-key", { key }, target);
  }

  invalidExtensionValue(key: string, path: string, target: Operation | Model | ModelProperty | Enum | Namespace) {
    this.report("invalid-extension-value", { key, path }, target);
  }

  emitSchemas(): Record<string, Schema> | undefined {
    const output: Record<string, Schema> = {};
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
      const nextUnion = [...this.unions.values()].find(
        (type) => type.name && !this.emittedUnions.has(type.name),
      );
      if (nextUnion?.name) {
        this.emittedUnions.add(nextUnion.name);
        output[nextUnion.name] = this.unionSchema(nextUnion);
        continue;
      }
      const nextSynthetic = [...this.syntheticSchemas.entries()].find(
        ([name]) => !this.emittedSyntheticSchemas.has(name),
      );
      if (nextSynthetic) {
        this.emittedSyntheticSchemas.add(nextSynthetic[0]);
        output[nextSynthetic[0]] = nextSynthetic[1];
        continue;
      }
      break;
    }
    return Object.keys(output).length > 0 ? output : undefined;
  }

  private schema(model: Model): Schema {
    const discriminator = getDiscriminator(this.program, model);
    if (discriminator) {
      const [union, diagnostics] = getDiscriminatedUnionFromInheritance(model, discriminator);
      this.program.reportDiagnostics(diagnostics);
      const baseName = `${model.name}Base`;
      this.syntheticSchemas.set(baseName, this.objectSchema(model));
      const oneOf: SchemaRef[] = [];
      const mapping: Record<string, string> = {};
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

  private objectSchema(model: Model): Schema {
    const schema: Schema = {
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

  private unionSchema(type: Union): Schema {
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

    const oneOf: SchemaRef[] = [];
    const mapping: Record<string, string> = {};
    for (const [value, variant] of union.variants) {
      const name = `${type.name}${schemaNamePart(value)}Variant`;
      const variantRef = this.schemaRef(variant, `union ${type.name} variant ${value}`);
      const properties: Record<string, SchemaProperty> = {
        [union.options.discriminatorPropertyName]: {
          schema: { type: "string", enum: [value] },
        },
      };
      const required = [union.options.discriminatorPropertyName];
      const schema: Schema = {
        type: "object",
        namespace: namespaceName(type.namespace),
        properties,
        property_order: [...required],
        required: [...required],
      };
      if (union.options.envelope === "none") {
        schema.base = variantRef;
      } else {
        properties[union.options.envelopePropertyName] = { schema: variantRef };
        schema.property_order!.push(union.options.envelopePropertyName);
        schema.required!.push(union.options.envelopePropertyName);
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

  private enumSchema(type: Enum): Schema {
    const schema: Schema = {
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

  private schemaProperty(property: ModelProperty): SchemaProperty {
    const schemaProperty: SchemaProperty = {
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

  private inlineObjectRef(model: Model, context: string): SchemaRef {
    if (model.name === "") {
      this.report("unnamed-schema", { context }, model);
    } else {
      this.unsupported(model, context);
    }
    return { type: "object" };
  }

  private unsupported(type: Type, context: string) {
    this.report("unsupported-type", { kind: type.kind, context }, type);
  }

  private report(code: Parameters<typeof reportDiagnostic>[1]["code"], format: any, target: Type | Namespace) {
    this.failed = true;
    reportDiagnostic(this.program, {
      code,
      format,
      target,
    } as any);
  }
}

function isJSONScalarType(type: Type): boolean {
  return type.kind === "Scalar" || type.kind === "String" || type.kind === "Boolean" || type.kind === "Number" || (type.kind === "Intrinsic" && type.name === "null");
}

export async function $onEmit(context: EmitContext<EmitterOptions>) {
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

  let doc: Document;
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
  } else {
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

function buildContractDocument(
  program: Program,
  contracts: Contract[],
  options: EmitterOptions,
): Document {
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
  }) as Document;
}

function packageMetadata(program: Program): { title: string; version: string; description?: string; namespace?: string } {
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

function namespaceName(namespace: Namespace | undefined): string | undefined {
  const parts: string[] = [];
  let current = namespace;
  while (current) {
    if (current.name) parts.unshift(current.name);
    current = current.namespace;
  }
  return parts.length > 0 ? parts.join(".") : undefined;
}

function contractRoots(program: Program, builder: IRBuilder, localNamespace: string | undefined): Contract[] {
  const roots: Contract[] = [];
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
    }) as Contract;
    roots.push(root);
  }
  roots.sort((left, right) => left.name.localeCompare(right.name));
  return roots;
}

function validatedMetadata(
  program: Program,
  builder: IRBuilder,
  target: Model | ModelProperty | Enum,
): Record<string, unknown> | undefined {
  const metadata = getMetadata({ program }, target);
  if (!metadata) {
    return undefined;
  }
  const output: Record<string, unknown> = {};
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

function buildDocument(
  program: Program,
  builder: IRBuilder,
  service: HttpService,
  options: EmitterOptions,
): Document {
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
  let transportErrorContract: Document["transport_errors"];
  if (transportErrors) {
    const schema = builder.namedSchemaRef(transportErrors.schema, "transport error schema");
    const failures: NonNullable<Document["transport_errors"]>["failures"] = {};
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
  }) as Document;
}

function mergedEndpoints(
  program: Program,
  builder: IRBuilder,
  operations: HttpOperation[],
  operationsAuth: Map<Operation, AuthenticationReference>,
  defaultSecurity: Record<string, string[]>[] | undefined,
): Endpoint[] {
  const groups = operationGroups(program, operations);
  return groups.map((group) => endpoint(program, builder, group, operationsAuth, defaultSecurity));
}

interface OperationGroup {
  operations: HttpOperation[];
  canonical: HttpOperation;
}

function operationGroups(program: Program, operations: HttpOperation[]): OperationGroup[] {
  const byRoute = new Map<string, HttpOperation[]>();
  const order: string[] = [];
  for (const operation of operations) {
    const key = `${operation.verb.toLowerCase()} ${operation.path}`;
    if (!byRoute.has(key)) {
      byRoute.set(key, []);
      order.push(key);
    }
    byRoute.get(key)!.push(operation);
  }

  const groups: OperationGroup[] = [];
  for (const key of order) {
    const routeOperations = byRoute.get(key)!;
    if (routeOperations.length === 1) {
      groups.push({ operations: routeOperations, canonical: routeOperations[0] });
      continue;
    }

    const coalescable = routeOperations.some((operation) => isSharedRoute(program, operation.operation)) ||
      routeOperations.some((operation) => operation.overloading && isOverloadSameEndpoint(operation as HttpOperation & { overloading: HttpOperation }));
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

function canonicalOperation(program: Program, operations: HttpOperation[]): HttpOperation {
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

function endpoint(
  program: Program,
  builder: IRBuilder,
  group: OperationGroup,
  operationsAuth: Map<Operation, AuthenticationReference>,
  defaultSecurity: Record<string, string[]>[] | undefined,
): Endpoint {
  const operation = group.canonical;
  validateSharedRouteMetadata(program, builder, group.operations, operation);
  const parameters = mergedParameters(program, builder, group.operations);
  const extensions: Record<string, unknown> = {};
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
  }) as Endpoint;
  if (Object.keys(extensions).length > 0) {
    output.extensions = extensions;
  }
  return output;
}

function validateSharedRouteMetadata(
  program: Program,
  builder: IRBuilder,
  operations: HttpOperation[],
  canonical: HttpOperation,
) {
  const canonicalNamespace = namespaceName(canonical.operation.namespace);
  const canonicalCLI = stableJSONString(cliMetadata(program, canonical));
  const canonicalCommand = stableJSONString(getCommand({ program }, canonical.operation));
  const canonicalUI = stableJSONString(getUI({ program }, canonical.operation));
  const canonicalAuditPayload = stableJSONString(auditPayloadIdentity(program, canonical.operation));
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
    if (stableJSONString(getUI({ program }, operation.operation)) !== canonicalUI) {
      builder.unsupportedSharedRoute(operation.operation, "incompatible ui metadata");
    }
    if (stableJSONString(auditPayloadIdentity(program, operation.operation)) !== canonicalAuditPayload) {
      builder.unsupportedSharedRoute(operation.operation, "incompatible audit payload metadata");
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

function operationKind(
  program: Program,
  builder: IRBuilder,
  operation: HttpOperation,
): "command" | "query" {
  const command = getCommand({ program }, operation.operation);
  const ui = getUI({ program }, operation.operation);
  const query = isQuery({ program }, operation.operation);
  const method = operation.verb.toLowerCase();
  if (builder.requireExplicitOperationKind && getOperationId(program, operation.operation) === undefined) {
    builder.invalidOperationKind("an explicit @operationId is required", operation.operation);
  }
  if (command && query) {
    builder.invalidOperationKind("@apigen.command and @apigen.query are mutually exclusive", operation.operation);
    return "command";
  }
  if (ui !== undefined && !command) {
    builder.invalidCommand("@apigen.ui requires @apigen.command", operation.operation);
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
    builder.invalidOperationKind(
      `${method.toUpperCase()} operations require @apigen.command or an explicit @apigen.query exemption`,
      operation.operation,
    );
  }
  return "query";
}

const auditActionPattern = /^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)+$/;
const stableNamePattern = /^[a-z][a-z0-9_]*$/;
const jobKindPattern = /^[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*$/;
const failureCodePattern = /^[A-Z][A-Z0-9_]*$/;
const uiActionPattern = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*(?:\.[a-z][a-z0-9]*(?:-[a-z0-9]+)*)+$/;

function commandMetadata(
  program: Program,
  builder: IRBuilder,
  operation: HttpOperation,
  parameters: Parameter[] | undefined,
): Command | undefined {
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
    builder.invalidCommand(
      `audit.successAction ${JSON.stringify(successAction)} must be a stable dotted lower_snake_case name`,
      operation.operation,
    );
  }

  let auditPayload: NonNullable<Command["audit"]["payload"]> | undefined;
  const authoredAuditPayload = getAuditPayload({ program }, operation.operation);
  if (options.audit.required && !authoredAuditPayload) {
    builder.invalidCommand("required audit must declare @apigen.auditPayload", operation.operation);
  }
  if (authoredAuditPayload) {
    const authored = authoredAuditPayload;
    const schema = authored.schema;
    const payloadOptions = authored.options ?? getAuditSchema({ program }, schema);
    if (!schema.name) {
      builder.invalidCommand("audit.payload.schema must be a named model", operation.operation);
    }
    if (!payloadOptions) {
      builder.invalidCommand(
        "@apigen.auditPayload requires inline options or model-owned @apigen.auditSchema",
        operation.operation,
      );
    } else if (payloadOptions.schemaVersion < 1 || !Number.isInteger(payloadOptions.schemaVersion)) {
      builder.invalidCommand("audit.payload.schemaVersion must be a positive integer", operation.operation);
    }
    if (schema.baseModel) {
      builder.invalidCommand("audit.payload.schema must not inherit fields", operation.operation);
    }
    const fields: NonNullable<Command["audit"]["payload"]>["fields"] = [];
    for (const property of schema.properties.values()) {
      if (property.optional) {
        builder.invalidCommand(`audit payload field ${JSON.stringify(property.name)} must be required`, operation.operation);
      }
      const sensitivity = getSensitivity({ program }, property);
      if (!sensitivity) {
        builder.invalidCommand(
          `audit payload field ${JSON.stringify(property.name)} requires an explicit audit sensitivity decorator`,
          operation.operation,
        );
        continue;
      }
      fields.push({ name: property.name, sensitivity });
    }
    fields.sort((left, right) => left.name.localeCompare(right.name));
    if (payloadOptions) {
      auditPayload = {
        schema: builder.namedSchemaRef(schema, `audit payload for ${operation.operation.name}`),
        schema_version: payloadOptions.schemaVersion,
        retention: payloadOptions.retention,
        fields,
      };
    }
  }

  let execution: Command["execution"];
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

  const failures: NonNullable<Command["failures"]> = [];
  const failureKinds = new Set<string>();
  const failureCodes = new Set<string>();
  for (const authored of options.failures ?? []) {
    const kind = authored.kind.trim();
    const code = authored.code.trim();
    const publicDetail = authored.publicDetail.trim();
    if (!stableNamePattern.test(kind)) {
      builder.invalidCommand("failure kind must be a stable lower_snake_case name", operation.operation);
    }
    if (authored.statusCode < 400 || authored.statusCode > 599) {
      builder.invalidCommand(`failure ${JSON.stringify(kind)} statusCode must be between 400 and 599`, operation.operation);
    }
    if (!failureCodePattern.test(code)) {
      builder.invalidCommand(`failure ${JSON.stringify(kind)} code must be stable UPPER_SNAKE_CASE`, operation.operation);
    }
    if (!publicDetail) {
      builder.invalidCommand(`failure ${JSON.stringify(kind)} publicDetail is required`, operation.operation);
    }
    if (failureKinds.has(kind)) {
      builder.invalidCommand(`failure kind ${JSON.stringify(kind)} is duplicated`, operation.operation);
    }
    if (failureCodes.has(code)) {
      builder.invalidCommand(`failure code ${JSON.stringify(code)} is duplicated`, operation.operation);
    }
    failureKinds.add(kind);
    failureCodes.add(code);
    failures.push({ kind, status_code: authored.statusCode, code, public_detail: publicDetail });
  }
  failures.sort((left, right) => left.kind.localeCompare(right.kind));

  const authoredUIAction = getUI({ program }, operation.operation);
  const uiAction = authoredUIAction?.trim();
  if (authoredUIAction !== undefined && !uiAction) {
    builder.invalidCommand("@apigen.ui actionId is required", operation.operation);
  } else if (uiAction && !uiActionPattern.test(uiAction)) {
    builder.invalidCommand(
      `@apigen.ui actionId ${JSON.stringify(uiAction)} must be a stable dotted lower-kebab-case name`,
      operation.operation,
    );
  }

  const additionalExposures = [...(options.additionalExposures ?? [])];
  if (uiAction && additionalExposures.includes("ui")) {
    builder.invalidCommand("@apigen.ui already declares the ui exposure; remove it from additionalExposures", operation.operation);
  }
  if (uiAction) {
    additionalExposures.push("ui");
  }
	if (!uiAction && additionalExposures.includes("ui")) {
		builder.invalidCommand("ui exposure requires @apigen.ui with a stable actionId", operation.operation);
	}
  if (new Set(additionalExposures).size !== additionalExposures.length) {
    builder.invalidCommand("additionalExposures must not contain duplicates", operation.operation);
  }
  additionalExposures.sort();

  const emittedParameters = parameters ?? [];
  const pathParameters = emittedParameters.filter((parameter) => parameter.in === "path");
  let targetParameter = options.targetParameter?.trim();
  if (!targetParameter && pathParameters.length === 1) {
    targetParameter = pathParameters[0].name;
  } else if (!targetParameter && pathParameters.length > 1) {
    builder.invalidCommand("targetParameter is required when a route has multiple path parameters", operation.operation);
  }
  let target: Command["target"];
  if (targetParameter) {
    const parameter = pathParameters.find((candidate) => candidate.name === targetParameter);
    if (!parameter || !parameter.required) {
      builder.invalidCommand(
        `targetParameter ${JSON.stringify(targetParameter)} must name a required path parameter`,
        operation.operation,
      );
    } else {
      target = { parameter: parameter.name, type: parameter.name };
    }
  }

  const hasRequiredHeader = (name: string) =>
    emittedParameters.some(
      (parameter) => parameter.in === "header" && parameter.required && parameter.name.toLowerCase() === name.toLowerCase(),
    );
  const method = operation.verb.toLowerCase();
  const idempotency = hasRequiredHeader("Idempotency-Key") ? "required" as const : undefined;
  const concurrency = hasRequiredHeader("If-Match") ? "if-match" as const : undefined;
  if (method === "post" && idempotency === undefined) {
    builder.invalidCommand("POST commands require a required Idempotency-Key header", operation.operation);
  }
  if (method === "patch" && concurrency === undefined) {
    builder.invalidCommand("PATCH commands require a required If-Match header", operation.operation);
  }

  const authz = getAuthz({ program }, operation.operation) as Record<string, unknown> | undefined;
  const authzMode = typeof authz?.mode === "string" ? authz.mode : undefined;
  const privilege = typeof authz?.privilege === "string" ? authz.privilege : undefined;
  return prune({
    owner: namespaceName(operation.operation.namespace) ?? "",
    audit: prune({ required: options.audit.required, success_action: successAction, guarantee, payload: auditPayload }),
    execution,
    failures,
    additional_exposures: additionalExposures.length > 0 ? additionalExposures : undefined,
    ui: uiAction ? { action_id: uiAction } : undefined,
    target,
    idempotency,
    concurrency,
    authz_mode: authzMode,
    privilege,
  }) as Command;
}

function auditPayloadIdentity(program: Program, operation: Operation): unknown {
  const payload = getAuditPayload({ program }, operation);
  if (!payload) {
    return undefined;
  }
  const options = payload.options ?? getAuditSchema({ program }, payload.schema);
  return {
    schema: `${namespaceName(payload.schema.namespace) ?? ""}.${payload.schema.name}`,
    schemaVersion: options?.schemaVersion,
    retention: options?.retention,
  };
}

function stableJSONString(value: unknown): string {
  return JSON.stringify(sortJSONValue(value));
}

function sortJSONValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((item) => sortJSONValue(item));
  }
  if (value && typeof value === "object") {
    const output: Record<string, unknown> = {};
    for (const key of Object.keys(value as Record<string, unknown>).sort()) {
      output[key] = sortJSONValue((value as Record<string, unknown>)[key]);
    }
    return output;
  }
  return value;
}

function operationVendorExtensions(
  program: Program,
  builder: IRBuilder,
  operation: Operation,
): [string, unknown][] {
  const extensions = new Map<string, unknown>(getExtensions(program, operation).entries());
  for (const [key, value] of operationExtensionDecoratorLiterals(operation)) {
    extensions.set(key, value);
  }

  const entries = [...extensions.entries()].sort(([left], [right]) => left.localeCompare(right));
  const output: [string, unknown][] = [];
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

function operationExtensionDecoratorLiterals(operation: Operation): [string, unknown][] {
  const decorators = ((operation.node as unknown) as { decorators?: readonly DecoratorNodeLike[] }).decorators ?? [];
  const output: [string, unknown][] = [];
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

function decoratorName(target: DecoratorNodeLike["target"]): string | undefined {
  if (target !== undefined && "sv" in target && typeof target.sv === "string") {
    return target.sv;
  }
  if (target !== undefined && "id" in target && typeof target.id?.sv === "string") {
    return target.id.sv;
  }
  return undefined;
}

function extensionLiteralValue(node: ExtensionLiteralNodeLike): LiteralConversionResult {
  if (Array.isArray(node.values)) {
    const values: unknown[] = [];
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
    const output: Record<string, unknown> = {};
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

function extensionObjectPropertyName(property: ExtensionObjectPropertyNodeLike): string | undefined {
  if (typeof property.id?.sv === "string") {
    return property.id.sv;
  }
  if (typeof property.id?.value === "string") {
    return property.id.value;
  }
  return undefined;
}

function isReservedExtensionKey(key: string): boolean {
  return key === "x-authz" || key === "x-agent" || key.startsWith("x-apigen-");
}

function isJSONCompatible(value: unknown): boolean {
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
      return Object.values(value as Record<string, unknown>).every((item) => isJSONCompatible(item));
    default:
      return false;
  }
}

function cliMetadata(program: Program, operation: HttpOperation): unknown {
  const cli = getCLI({ program }, operation.operation);
  if (!cli) {
    return undefined;
  }
  return prune({
    command: cli.command,
    args: cli.args?.map((arg) =>
      prune({
        source: arg.source,
        name: arg.name,
        display_name: arg.displayName,
      }),
    ),
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

function toolMetadata(program: Program, operation: HttpOperation): unknown {
  const tool = getTool({ program }, operation.operation);
  if (!tool) {
    return undefined;
  }
  const metadata: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(tool.metadata ?? {}).sort(([left], [right]) => left.localeCompare(right))) {
    if (!key.startsWith("x-") || isReservedExtensionKey(key) || !isJSONCompatible(value)) {
      reportDiagnostic(program, {
        code: !key.startsWith("x-") ? "invalid-extension-key" : isReservedExtensionKey(key) ? "reserved-extension" : "invalid-extension-value",
        target: operation.operation,
        format: !key.startsWith("x-") || isReservedExtensionKey(key) ? { key } : { key, path: key },
      } as any);
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
          fields: tool.input.fields?.map((field) =>
            prune({
              source: field.source,
              name: field.name,
              mode: field.mode,
              alias: field.alias,
              context_key: field.contextKey,
              description: field.description,
              default: field.default,
            }),
          ),
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

function toolProjectionMetadata(projection: import("./decorators.js").ToolProjectionOptions): unknown {
  return prune({
    source: projection.source,
    target: projection.target,
    select: projection.select?.map(toolProjectionMetadata),
    count_as: projection.countAs,
  });
}

function defaultToolConfirmation(effect: string): string {
  switch (effect) {
    case "read":
      return "never";
    case "destructive":
      return "always";
    default:
      return "policy";
  }
}

function endpointParameter(
  program: Program,
  builder: IRBuilder,
  parameter: HttpOperationParameter,
): Parameter {
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
  }) as Parameter;
}

function mergedParameters(
  program: Program,
  builder: IRBuilder,
  operations: HttpOperation[],
): Parameter[] | undefined {
  const output: Parameter[] = [];
  const byKey = new Map<string, Parameter>();
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

function mergeParameter(builder: IRBuilder, left: Parameter, right: Parameter, operation: Operation): Parameter {
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
  }) as Parameter;
}

function mergeParameterSchemas(left: SchemaRef, right: SchemaRef): SchemaRef | undefined {
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

function literalSchemaEnumValues(schema: SchemaRef): string[] | undefined {
  if (schema.type !== "string" || schema.ref || schema.format || schema.items || schema.additional_properties) {
    return undefined;
  }
  return schema.enum ? schema.enum : [];
}

function parameterSchemaRef(builder: IRBuilder, type: Type, context: string): SchemaRef {
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

function shouldEmitExplode(
  builder: IRBuilder,
  type: Type,
  parameter: HttpOperationParameter,
): parameter is HttpOperationParameter & { explode: boolean } {
  if (!("explode" in parameter) || parameter.explode === undefined) {
    return false;
  }
  if (parameter.type !== "query" && parameter.type !== "header") {
    return false;
  }
  const schema = parameterSchemaRef(builder, type, `parameter ${parameter.param.name}`);
  return schema.type === "array" || parameter.explode === true;
}

function requestBody(builder: IRBuilder, body: HttpPayloadBody | undefined): RequestBody | undefined {
  if (!body) {
    return undefined;
  }
  return prune({
    required: body.property ? !body.property.optional : true,
    contents: bodyContents(builder, body, "request body"),
  }) as RequestBody;
}

function mergedRequestBody(builder: IRBuilder, operations: HttpOperation[]): RequestBody | undefined {
  let output: RequestBody | undefined;
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

function endpointResponses(
  program: Program,
  builder: IRBuilder,
  responses: HttpOperationResponse[],
): Response[] {
  const byStatus = new Map<number, Response>();
  const order: number[] = [];

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

  return order.map((status) => byStatus.get(status)!);
}

function endpointResponse(
  program: Program,
  builder: IRBuilder,
  response: HttpOperationResponse,
): Response {
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
  }) as Response;
}

function mergeHeaders(left: Header[] | undefined, right: Header[] | undefined): Header[] | undefined {
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

function mergeContents(
  builder: IRBuilder,
  response: HttpOperationResponse,
  statusCode: number,
  left: BodyContent[] | undefined,
  right: BodyContent[] | undefined,
): BodyContent[] | undefined {
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

function sameContentType(left: string, right: string): boolean {
  return left.trim().toLowerCase() === right.trim().toLowerCase();
}

function mergeResponseExtensions(
  left: Record<string, unknown> | undefined,
  right: Record<string, unknown> | undefined,
): Record<string, unknown> | undefined {
  if (!left || Object.keys(left).length === 0) {
    return right;
  }
  if (!right || Object.keys(right).length === 0) {
    return left;
  }
  return { ...left, ...right };
}

function responseContents(builder: IRBuilder, contents: HttpOperationResponseContent[]): BodyContent[] | undefined {
  const output: BodyContent[] = [];
  for (const content of contents) {
    if (!content.body) {
      continue;
    }
    output.push(...bodyContents(builder, content.body, "response body"));
  }
  return output.length > 0 ? output : undefined;
}

function bodyContents(builder: IRBuilder, body: HttpPayloadBody, context: string): BodyContent[] {
  switch (body.bodyKind) {
    case "single":
      return body.contentTypes.map((contentType) =>
        prune({
          content_type: contentType,
          body_kind: bodyKindForSingle(body.type, contentType),
          schema: schemaRefForContent(builder, body.type, contentType, context),
        }) as BodyContent,
      );
    case "file":
      return body.contentTypes.map((contentType) =>
        prune({
          content_type: contentType,
          body_kind: "file",
          schema: fileSchemaRef(body.isText),
        }) as BodyContent,
      );
    case "multipart":
      return body.contentTypes.map((contentType) =>
        prune({
          content_type: contentType,
          body_kind: "multipart",
          parts: body.parts.map((part, idx) => multipartPart(builder, part, idx)),
        }) as BodyContent,
      );
  }
}

function multipartPart(builder: IRBuilder, part: HttpOperationPart, idx: number): MultipartPart {
  const bodyKind = part.body.bodyKind === "file" ? "file" : bodyKindForSingle(part.body.type, part.body.contentTypes[0] ?? "application/json");
  const schema =
    part.body.bodyKind === "file"
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
  }) as MultipartPart;
}

function bodyKindForSingle(type: Type, contentType: string): BodyContent["body_kind"] {
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

function schemaRefForContent(builder: IRBuilder, type: Type, contentType: string, context: string): SchemaRef {
  const kind = bodyKindForSingle(type, contentType);
  if ((kind === "binary" || kind === "file") && isBytesType(type)) {
    return { type: "string", format: "binary" };
  }
  return builder.schemaRef(type, context);
}

function fileSchemaRef(isText: boolean): SchemaRef {
  return isText ? { type: "string" } : { type: "string", format: "binary" };
}

function responseHeaders(
  program: Program,
  builder: IRBuilder,
  content: HttpOperationResponseContent,
): Header[] | undefined {
  if (!content.headers) {
    return undefined;
  }
  const headers = Object.entries(content.headers).map(([name, property]) =>
    prune({
      name,
      required: !property.optional,
      description: getDoc(program, property),
      schema: withSchemaConstraints(program, property, builder.schemaRef(property.type, `response header ${name}`)),
    }),
  ) as Header[];
  return headers.length > 0 ? headers : undefined;
}

function collectSecuritySchemes(
  builder: IRBuilder,
  auths: readonly HttpAuth[],
  target: Namespace,
): Record<string, SecurityScheme> {
  const schemes: Record<string, SecurityScheme> = {};
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

function operationSecurity(
  builder: IRBuilder,
  operation: HttpOperation,
  operationAuth: AuthenticationReference | undefined,
  defaultSecurity: Record<string, string[]>[] | undefined,
): Record<string, string[]>[] | undefined {
  if (!operationAuth) {
    return undefined;
  }
  const security = authRequirements(
    builder,
    operationAuth,
    operation.operation,
    `operation ${operation.operation.name} authentication`,
    defaultSecurity === undefined,
  );
  if (sameSecurity(security, defaultSecurity)) {
    return undefined;
  }
  return security;
}

function mergedOperationSecurity(
  builder: IRBuilder,
  operations: HttpOperation[],
  operationsAuth: Map<Operation, AuthenticationReference>,
  defaultSecurity: Record<string, string[]>[] | undefined,
): Record<string, string[]>[] | undefined {
  let output: Record<string, string[]>[] | undefined;
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

function authRequirements(
  builder: IRBuilder,
  auth: AuthenticationReference,
  target: Type | Operation | Namespace,
  context: string,
  allowNoAuth: boolean,
): Record<string, string[]>[] | undefined {
  const requirements: Record<string, string[]>[] = [];
  for (const option of auth.options) {
    const requirement: Record<string, string[]> = {};
    for (const ref of option.all) {
      if (ref.kind === "noAuth") {
        if (allowNoAuth) {
          continue;
        }
        builder.unsupportedAuth(
          context,
          "APIGen IR v4 does not support NoAuth operation overrides for secured services.",
          target,
        );
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

function isSupportedAuth(auth: HttpAuth): boolean {
  if (auth.type === "http") {
    return auth.scheme.toLowerCase() === "bearer";
  }
  if (auth.type === "apiKey") {
    return auth.in === "header" && auth.name === "X-API-Key";
  }
  return false;
}

function unsupportedAuthReason(auth: HttpAuth): string {
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

function authScopes(ref: HttpAuthRef): string[] {
  if (ref.kind === "oauth2") {
    return [...ref.scopes];
  }
  return [];
}

function sameSecurity(
  left: Record<string, string[]>[] | undefined,
  right: Record<string, string[]>[] | undefined,
): boolean {
  return JSON.stringify(left ?? []) === JSON.stringify(right ?? []);
}

function securityScheme(scheme: HttpAuth): SecurityScheme {
  switch (scheme.type) {
    case "http":
      return { type: "http", scheme: scheme.scheme };
    case "apiKey":
      return { type: "apiKey", in: scheme.in, name: scheme.name };
    default:
      return { type: scheme.type };
  }
}

function withSchemaConstraints(program: Program, target: Type, schema: SchemaRef): SchemaRef {
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
  }) as SchemaRef;
}

function schemaConstraintCandidates(target: Type): Type[] {
  const candidates: Type[] = [target];
  let current: Type | undefined = target.kind === "ModelProperty" ? target.type : target;
  if (current !== target) {
    candidates.push(current);
  }
  while (current?.kind === "Scalar" && current.baseScalar) {
    current = current.baseScalar;
    candidates.push(current);
  }
  return candidates;
}

function firstSchemaConstraint(candidates: Type[], read: (candidate: Type) => number | undefined): number | undefined {
  for (const candidate of candidates) {
    const value = read(candidate);
    if (value !== undefined) {
      return value;
    }
  }
  return undefined;
}

function scalarSchemaRef(scalar: Scalar): SchemaRef {
  for (let current: Scalar | undefined = scalar; current; current = current.baseScalar) {
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

function isBytesType(type: Type): boolean {
  if (type.kind !== "Scalar") {
    return false;
  }
  for (let current: Scalar | undefined = type; current; current = current.baseScalar) {
    if (current.name === "bytes") {
      return true;
    }
  }
  return false;
}

function enumValues(type: Enum): string[] {
  return [...type.members.values()].map((member) => String(member.value ?? member.name));
}

function stringLiteralUnionValues(type: Union): string[] | undefined {
  const values: string[] = [];
  for (const variant of type.variants.values()) {
    if (variant.type.kind !== "String") {
      return undefined;
    }
    values.push(variant.type.value);
  }
  return values;
}

function uniqueStrings(values: string[]): string[] {
  const output: string[] = [];
  const seen = new Set<string>();
  for (const value of values) {
    if (seen.has(value)) {
      continue;
    }
    seen.add(value);
    output.push(value);
  }
  return output;
}

function schemaNamePart(value: string): string {
  const parts = value.split(/[^A-Za-z0-9]+/).filter(Boolean);
  const name = parts.map((part) => part[0].toUpperCase() + part.slice(1)).join("");
  return name || "Variant";
}

function isNamedUserModel(type: Model): boolean {
  return type.name !== "" && !isArrayModelType(type) && !isRecordModelType(type);
}

function prune<T>(value: T): T {
  if (Array.isArray(value)) {
    return value.filter((item) => item !== undefined).map((item) => prune(item)) as T;
  }
  if (value && typeof value === "object") {
    if (isSecurityRequirementObject(value)) {
      return value;
    }
    const output: Record<string, unknown> = {};
    for (const [key, child] of Object.entries(value)) {
      if (child === undefined) {
        continue;
      }
      if (Array.isArray(child) && child.length === 0 && key !== "failures") {
        continue;
      }
      if (key === "extensions") {
        output[key] = child;
        continue;
      }
      output[key] = prune(child);
    }
    return output as T;
  }
  return value;
}

function isSecurityRequirementObject(value: object): value is Record<string, string[]> {
  const entries = Object.entries(value);
  return entries.length > 0 && entries.every(([, child]) => Array.isArray(child));
}
