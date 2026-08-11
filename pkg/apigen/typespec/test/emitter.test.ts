import { execFile } from "node:child_process";
import { mkdir, mkdtemp, readFile, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { describe, expect, it } from "vitest";

const execFileAsync = promisify(execFile);
const root = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const tsp = join(root, "node_modules", "@typespec", "compiler", "cmd", "tsp.js");

describe("APIGen TypeSpec emitter", () => {
  it("emits JSON IR for the todo fixture", async () => {
    const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
    const irPath = join(outDir, "json-ir.json");

    await compileFixture("todo", irPath);

    const doc = JSON.parse(await readFile(irPath, "utf8"));
    expect(doc.schema_version).toBe("v4");
    expect(doc.info.title).toBe("APIGen Todo Example");
    expect(doc.endpoints.map((x: any) => x.operation_id)).toEqual([
      "listTodos",
      "createTodo",
      "getTodo",
      "completeTodo",
      "deleteTodo",
    ]);
    expect(doc.schemas.Todo.property_order).toEqual(["id", "title", "status"]);
    expect(doc.endpoints[1].request_body.contents[0]).toMatchObject({
      content_type: "application/json",
      body_kind: "json",
      schema: { ref: "CreateTodoRequest" },
    });
    expect(doc.endpoints[1].cli.command).toEqual(["todos", "create"]);
    expect(doc.openapi.security).toEqual([{ BearerAuth: [] }, { ApiKeyAuth: [] }]);
  });

  it("preserves fully qualified declaring namespaces on endpoints", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Partitioned API" })
      namespace PartitionedAPI;

      namespace Access {
        @route("/me")
        @get
        op getCurrentPrincipal(): string;
      }

      namespace Analytics {
        namespace Reports {
          @route("/reports")
          @get
          op listReports(): string;
        }
      }
    `);

    expect(doc.endpoints.map((endpoint: any) => ({
      operation_id: endpoint.operation_id,
      namespace: endpoint.namespace,
    }))).toEqual([
      { operation_id: "Access_getCurrentPrincipal", namespace: "PartitionedAPI.Access" },
      { operation_id: "Reports_listReports", namespace: "PartitionedAPI.Analytics.Reports" },
    ]);
  });

  it("fails when a request body cannot map to a named IR schema", async () => {
    const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
    const irPath = join(outDir, "json-ir.json");

    await expect(compileFixture("invalid", irPath)).rejects.toSatisfy((error: any) =>
      `${error.stdout}\n${error.stderr}`.includes(
        "requires request body to resolve to a named model schema",
      ),
    );
  });

  it("emits named enum schemas and inherited model properties", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Enum API" })
      namespace EnumAPI;

      enum WidgetStatus {
        active,
        archived,
      }

      model Resource {
        id: string;
      }

      model Widget extends Resource {
        status: WidgetStatus;
        name?: string;
      }

      @route("/widgets/{status}")
      @get
      op list(@path status: WidgetStatus): Widget;

      @route("/widgets/status")
      @post
      op setStatus(@body body: WidgetStatus): Widget;
    `);

    expect(doc.schemas.WidgetStatus).toEqual({
      type: "string",
      enum: ["active", "archived"],
      namespace: "EnumAPI",
    });
		expect(doc.schemas.Widget.base).toEqual({ ref: "Resource" });
		expect(doc.schemas.Widget.property_order).toEqual(["status", "name"]);
		expect(doc.schemas.Widget.required).toEqual(["status"]);
		expect(doc.schemas.Resource.required).toEqual(["id"]);
    expect(doc.schemas.Widget.properties.status.schema).toEqual({ ref: "WidgetStatus" });
    expect(doc.endpoints[0].parameters[0].schema).toEqual({ ref: "WidgetStatus" });
    expect(doc.endpoints[1].request_body.contents[0].schema).toEqual({ ref: "WidgetStatus" });
  });

  it("emits a normalized command contract through interface route and tag metadata", async () => {
    const doc = await compileSource(`
      using Http;
      using OpenAPI;

      @service(#{ title: "Command API" })
      namespace CommandAPI;

      @tag("Access")
      @route("/workspaces/{workspace}/role-bindings")
      interface RoleBindings {
        @post
        @operationId("createRoleBinding")
        @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
        @apigen.command(#{
          audit: #{ required: true, successAction: "role_binding.created", guarantee: "best-effort" },
          failures: #[],
          additionalExposures: #["ui", "automation"],
        })
        create(
          @path workspace: string,
          @header("Idempotency-Key") idempotencyKey: string,
        ): string;
      }
    `);

    expect(doc.endpoints[0]).toMatchObject({
      operation_id: "createRoleBinding",
      kind: "command",
      namespace: "CommandAPI",
      method: "post",
      path: "/workspaces/{workspace}/role-bindings",
      tags: ["Access"],
      command: {
        owner: "CommandAPI",
        audit: { required: true, success_action: "role_binding.created", guarantee: "best-effort" },
        additional_exposures: ["automation", "ui"],
        target: { parameter: "workspace", type: "workspace" },
        idempotency: "required",
        authz_mode: "privilege",
        privilege: "MANAGE_GRANTS",
      },
    });
  });

  it("emits a typed async execution contract", async () => {
    const doc = await compileSource(`
      using Http;
      using OpenAPI;

      @service(#{ title: "Release API" })
      namespace ReleaseAPI;

      model Accepted {
        @statusCode statusCode: 202;
        @body body: string;
      }

      @route("/releases/{release}")
      @get
      @operationId("getRelease")
      op getRelease(@path release: string): string;

      @route("/releases/{release}/events")
      @get
      @operationId("listReleaseEvents")
      op listReleaseEvents(@path release: string): string;

      @route("/releases/{release}/finalize")
      @post
      @operationId("finalizeRelease")
      @apigen.command(#{
        audit: #{ required: true, successAction: "release.validating", guarantee: "transactional" },
        failures: #[],
        execution: #{
          mode: "async",
          guarantee: "transactional",
          jobKind: "release.finalize",
          resourceKind: "release",
          initialEvent: "release.validating",
          initialState: "validating",
          statusOperation: "getRelease",
          eventsOperation: "listReleaseEvents",
          cancellation: "unsupported",
        },
      })
      op finalizeRelease(
        @path release: string,
        @header("Idempotency-Key") idempotencyKey: string,
      ): Accepted;
    `);

    expect(doc.endpoints.find((endpoint) => endpoint.operation_id === "finalizeRelease")).toMatchObject({
      kind: "command",
      responses: [{ status_code: 202 }],
      command: {
        execution: {
          mode: "async",
          guarantee: "transactional",
          job_kind: "release.finalize",
          resource_kind: "release",
          initial_event: "release.validating",
          initial_state: "validating",
          status_operation: "getRelease",
          events_operation: "listReleaseEvents",
          cancellation: "unsupported",
        },
      },
    });
  });

  it("requires explicit command/query classification for non-read operations in strict mode", async () => {
    const doc = await compileSource(`
      using Http;
      using OpenAPI;

      @service(#{ title: "Strict Operations API" })
      namespace StrictOperationsAPI;

      @route("/search")
      @post
      @operationId("searchWidgets")
      @apigen.query
      op searchWidgets(@body body: string): string;
    `, undefined, true);

    expect(doc.endpoints[0]).toMatchObject({ operation_id: "searchWidgets", kind: "query" });

    await expectCompileFails(`
      using Http;
      using OpenAPI;

      @service(#{ title: "Strict Operations API" })
      namespace StrictOperationsAPI;

      @route("/widgets")
      @post
      @operationId("createWidget")
      op createWidget(@body body: string): string;
    `, "require @apigen.command or an explicit @apigen.query exemption", true);
  });

  it("rejects invalid command contracts before writing IR", async () => {
    const cases = [
      {
        message: "explicit @operationId is required",
        operation: `
          @post
          @apigen.command(#{ audit: #{ required: true, successAction: "widget.created" }, failures: #[] })
          op create(@header("Idempotency-Key") key: string): string;
        `,
      },
      {
        message: "POST commands require a required Idempotency-Key header",
        operation: `
          @post
          @operationId("createWidget")
          @apigen.command(#{ audit: #{ required: true, successAction: "widget.created" }, failures: #[] })
          op create(): string;
        `,
      },
      {
        message: "PATCH commands require a required If-Match header",
        operation: `
          @patch
          @operationId("updateWidget")
          @apigen.command(#{ audit: #{ required: true, successAction: "widget.updated" }, failures: #[] })
          op update(): string;
        `,
      },
      {
        message: "targetParameter is required when a route has multiple path parameters",
        operation: `
          @route("/{workspace}/widgets/{widget}")
          @delete
          @operationId("deleteWidget")
          @apigen.command(#{ audit: #{ required: true, successAction: "widget.deleted" }, failures: #[] })
          op delete(@path workspace: string, @path widget: string): string;
        `,
      },
      {
        message: "must be a stable dotted lower_snake_case name",
        operation: `
          @delete
          @operationId("deleteWidget")
          @apigen.command(#{ audit: #{ required: true, successAction: "Widget Deleted" }, failures: #[] })
          op delete(): string;
        `,
      },
      {
        message: "execution.initialEvent must be a stable dotted lower_snake_case name",
        operation: `
          @post
          @operationId("finalizeWidget")
          @apigen.command(#{
            audit: #{ required: true, successAction: "widget.validating", guarantee: "best-effort" },
            failures: #[],
            execution: #{
              mode: "async",
              guarantee: "transactional",
              jobKind: "widget.finalize",
              resourceKind: "widget",
              initialEvent: "Widget Validating",
              initialState: "validating",
              statusOperation: "getWidget",
              eventsOperation: "listWidgetEvents",
              cancellation: "unsupported",
            },
          })
          op finalize(@header("Idempotency-Key") key: string): string;
        `,
      },
    ];
    for (const testCase of cases) {
      await expectCompileFails(`
        using Http;
        using OpenAPI;
        @service(#{ title: "Invalid Command API" })
        @route("/commands")
        namespace InvalidCommandAPI {
          ${testCase.operation}
        }
      `, testCase.message);
    }
  });

  it("emits closed discriminated inheritance with explicit composition", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Visual API" })
      namespace VisualAPI;

      @discriminator("shape")
      model Visual {
        shape: string;
      }

      model ChartVisual extends Visual {
        shape: "chart";
        points: int64[];
      }

      model TextVisual extends Visual {
        shape: "text";
        text: string;
      }

      @route("/visual")
      @get
      op getVisual(): Visual;
    `);

    expect(doc.schemas.Visual).toEqual({
      type: "union",
      namespace: "VisualAPI",
      one_of: [{ ref: "ChartVisual" }, { ref: "TextVisual" }],
      discriminator: {
        property_name: "shape",
        mapping: { chart: "ChartVisual", text: "TextVisual" },
      },
    });
    expect(doc.schemas.VisualBase.properties.shape.schema).toEqual({ type: "string" });
    expect(doc.schemas.ChartVisual.base).toEqual({ ref: "VisualBase" });
    expect(doc.schemas.ChartVisual.properties.shape.schema).toEqual({ type: "string", enum: ["chart"] });
  });

  it("emits named inline discriminated unions as reusable schemas", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Component API" })
      namespace ComponentAPI;

      model TextComponent { text: string; }
      model ChartComponent { points: int64[]; }

      @discriminated(#{ envelope: "none", discriminatorPropertyName: "kind" })
      union Component {
        text: TextComponent,
        chart: ChartComponent,
      }

      model Page { components: Component[]; }

      @route("/page")
      @get
      op getPage(): Page;
    `);

    expect(doc.schemas.Component.type).toBe("union");
    expect(doc.schemas.Component.discriminator.property_name).toBe("kind");
    expect(doc.schemas.Page.properties.components.schema).toEqual({
      type: "array",
      items: { ref: "Component" },
    });
    expect(doc.schemas.Component.one_of).toHaveLength(2);
  });

  it("emits named JSON scalar unions without a discriminator", async () => {
    const doc = await compileSource(`
      @apigen.\`package\`(#{ title: "Scalar Contracts", version: "1.0.0" })
      namespace ScalarContracts;

      union JsonScalar {
        text: string,
        integer: int64,
        number: float64,
        boolean: boolean,
        nil: null,
      }

      @apigen.contract model Mapping { value: JsonScalar; }
    `);

    expect(doc.schemas.JsonScalar).toEqual({
      type: "union",
      namespace: "ScalarContracts",
      one_of: [
        { type: "string" },
        { type: "integer", format: "int64" },
        { type: "number", format: "double" },
        { type: "boolean" },
        { type: "null" },
      ],
    });
  });

  it("emits an explicit generated transport error contract", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Problem API" })
      @apigen.transportErrors(ProblemDetails, #{
        contentType: "application/problem+json",
        failures: #[
          #{ kind: "malformed_body", statusCode: 400, code: "malformed_body", publicDetail: "The request body is malformed." },
          #{ kind: "internal", statusCode: 500, code: "internal", publicDetail: "Internal server error." },
        ],
      })
      namespace ProblemAPI;

      model ProblemDetails {
        type: string;
        title: string;
        status: int32;
      }

      @route("/ping")
      @get
      op ping(): string;
    `);

    expect(doc.transport_errors).toEqual({
      schema: { ref: "ProblemDetails" },
      content_type: "application/problem+json",
      failures: {
        malformed_body: { status_code: 400, code: "malformed_body", public_detail: "The request body is malformed." },
        internal: { status_code: 500, code: "internal", public_detail: "Internal server error." },
      },
    });
  });

  it("emits typed command failure contracts", async () => {
    const doc = await compileSource(`
      using Http;
      using TypeSpec.OpenAPI;

      @service(#{ title: "Command Failures" })
      namespace CommandFailures;

      model Accepted { @statusCode statusCode: 202; }
      model ConflictFailure { @statusCode statusCode: 409; }

      @route("/widgets/{widget}/finalize")
      @post
      @operationId("finalizeWidget")
      @apigen.command(#{
        audit: #{ required: false },
        failures: #[
          #{ kind: "conflict", statusCode: 409, code: "WIDGET_CONFLICT", publicDetail: "The widget conflicts with its current state." },
        ],
      })
      op finalizeWidget(@path widget: string, @header("Idempotency-Key") key: string): Accepted | ConflictFailure;
    `);

    expect(doc.endpoints[0].command.failures).toEqual([{
      kind: "conflict",
      status_code: 409,
      code: "WIDGET_CONFLICT",
      public_detail: "The widget conflicts with its current state.",
    }]);
  });

  it("requires every command to declare its failure vocabulary", async () => {
    await expectCompileFails(`
      using Http;
      using TypeSpec.OpenAPI;

      @service(#{ title: "Command Failures" })
      namespace CommandFailures;

      @route("/widgets")
      @post
      @operationId("createWidget")
      @apigen.command(#{ audit: #{ required: false } })
      op createWidget(@header("Idempotency-Key") key: string): string;
    `, "apigen.CommandOptions");
  });

  it("emits v4 IR for optimized TypeSpec HTTP authoring", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Optimized API" })
      namespace OptimizedAPI;

      model Error {
        code: int32;
        message: string;
      }

      model Artifact {
        id: string;
      }

      model ArtifactCreate {
        name: string;
      }

      model Metadata {
        name: string;
      }

      model OkJson<T> {
        ...OkResponse;
        ...Body<T>;
      }

      model CreatedJson<T> {
        ...CreatedResponse;
        ...Body<T>;
      }

      model BadRequest {
        ...BadRequestResponse;
        ...Body<Error>;
      }

      model RateLimited {
        ...Response<429>;
        ...Body<Error>;
      }

      alias CommonErrors = BadRequest | RateLimited;

      @route("/artifacts")
      namespace Artifacts {
        @route("/{id}")
        @get
        op get(@path id: string): OkJson<Artifact> | CommonErrors;

        @post
        op create(@body body?: ArtifactCreate): CreatedJson<Artifact> | CommonErrors;

        @route("/{id}/text")
        @put
        op replaceText(@path id: string, @header contentType: "text/plain", @body body: string): OkJson<Artifact> | CommonErrors;

        @route("/{id}/blob")
        @put
        op replaceBlob(@path id: string, @header contentType: "application/octet-stream", @body body: bytes): OkJson<Artifact> | CommonErrors;

        @route("/{id}/file")
        @put
        op replaceFile(@path id: string, @bodyRoot body: File<"application/octet-stream", bytes>): OkJson<Artifact> | CommonErrors;

        @route("/{id}/form")
        @put
        op replaceForm(@path id: string, @header contentType: "application/x-www-form-urlencoded", @body body: Metadata): OkJson<Artifact> | CommonErrors;

        @route("/{id}/multipart")
        @put
        op replaceMultipart(@path id: string, @multipartBody body: {
          metadata: HttpPart<Metadata>;
          artifact: HttpPart<File<"application/octet-stream", bytes>>;
        }): OkJson<Artifact> | CommonErrors;
      }
    `);

    expect(doc.schema_version).toBe("v4");
    expect(doc.endpoints.map((x: any) => x.path)).toEqual([
      "/artifacts/{id}",
      "/artifacts",
      "/artifacts/{id}/text",
      "/artifacts/{id}/blob",
      "/artifacts/{id}/file",
      "/artifacts/{id}/form",
      "/artifacts/{id}/multipart",
    ]);
    expect(doc.endpoints[0].responses.map((x: any) => x.status_code)).toEqual([200, 400, 429]);
    expect(doc.endpoints[1].request_body.required).toBe(false);
    expect(doc.endpoints[2].request_body.contents[0].body_kind).toBe("text");
    expect(doc.endpoints[3].request_body.contents[0]).toMatchObject({
      content_type: "application/octet-stream",
      body_kind: "binary",
      schema: { type: "string", format: "binary" },
    });
    expect(doc.endpoints[4].request_body.contents[0].body_kind).toBe("file");
    expect(doc.endpoints[5].request_body.contents[0].body_kind).toBe("form_urlencoded");
    expect(doc.endpoints[6].request_body.contents[0].body_kind).toBe("multipart");
    expect(doc.endpoints[6].request_body.contents[0].parts.map((x: any) => x.name)).toEqual([
      "metadata",
      "artifact",
    ]);
  });

  it("merges same-status response variants into ordered IR contents", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Multi Content API" })
      namespace MultiContentAPI;

      model Artifact {
        id: string;
      }

      model JSONArtifact {
        ...OkResponse;
        ...Body<Artifact>;
      }

      model BinaryArtifact {
        ...OkResponse;
        @header contentType: "application/octet-stream";
        @body body: bytes;
      }

      @route("/artifacts/{id}")
      @get
      op getArtifact(@path id: string): JSONArtifact | BinaryArtifact;
    `);

    const response = doc.endpoints[0].responses[0];
    expect(doc.endpoints[0].responses).toHaveLength(1);
    expect(response.status_code).toBe(200);
    expect(response.contents).toEqual([
      {
        content_type: "application/json",
        body_kind: "json",
        schema: { ref: "Artifact" },
      },
      {
        content_type: "application/octet-stream",
        body_kind: "binary",
        schema: { type: "string", format: "binary" },
      },
    ]);
  });

  it("coalesces shared-route content variants and literal accept headers", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Shared Route API" })
      namespace SharedRouteAPI;

      model Artifact {
        id: string;
      }

      model JsonArtifact {
        ...OkResponse;
        ...Body<Artifact>;
      }

      model BinaryArtifact {
        ...OkResponse;
        @header contentType: "application/octet-stream";
        @body body: bytes;
      }

      @route("/artifacts/{id}")
      @sharedRoute
      @get
      op getArtifactJson(@path id: string, @header accept: "application/json"): JsonArtifact;

      @route("/artifacts/{id}")
      @sharedRoute
      @get
      op getArtifactBinary(@path id: string, @header accept: "application/octet-stream"): BinaryArtifact;
    `);

    expect(doc.endpoints).toHaveLength(1);
    expect(doc.endpoints[0]).toMatchObject({
      method: "get",
      path: "/artifacts/{id}",
      operation_id: "getArtifactJson",
    });
    expect(doc.endpoints[0].parameters).toEqual([
      { name: "id", in: "path", required: true, schema: { type: "string" } },
      {
        name: "accept",
        in: "header",
        required: true,
        schema: { type: "string", enum: ["application/json", "application/octet-stream"] },
      },
    ]);
    expect(doc.endpoints[0].responses).toHaveLength(1);
    expect(doc.endpoints[0].responses[0].contents).toEqual([
      { content_type: "application/json", body_kind: "json", schema: { ref: "Artifact" } },
      {
        content_type: "application/octet-stream",
        body_kind: "binary",
        schema: { type: "string", format: "binary" },
      },
    ]);
  });

  it("coalesces overload content variants into the overload base operation", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Overload API" })
      namespace OverloadAPI;

      model Artifact {
        id: string;
      }

      model JsonArtifact {
        ...OkResponse;
        ...Body<Artifact>;
      }

      model BinaryArtifact {
        ...OkResponse;
        @header contentType: "application/octet-stream";
        @body body: bytes;
      }

      @route("/artifacts/{id}")
      @get
      op getArtifact(@path id: string, @header accept: "application/json" | "application/octet-stream"): JsonArtifact | BinaryArtifact;

      @overload(getArtifact)
      op getArtifactJson(@path id: string, @header accept: "application/json"): JsonArtifact;

      @overload(getArtifact)
      op getArtifactBinary(@path id: string, @header accept: "application/octet-stream"): BinaryArtifact;
    `);

    expect(doc.endpoints).toHaveLength(1);
    expect(doc.endpoints[0].operation_id).toBe("getArtifact");
    expect(doc.endpoints[0].parameters[1].schema).toEqual({
      type: "string",
      enum: ["application/json", "application/octet-stream"],
    });
    expect(doc.endpoints[0].responses[0].contents.map((x: any) => x.content_type)).toEqual([
      "application/json",
      "application/octet-stream",
    ]);
  });

  it("deduplicates identical same-status content variants", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Duplicate Content API" })
      namespace DuplicateContentAPI;

      model Widget {
        id: string;
      }

      model JsonWidget {
        ...OkResponse;
        ...Body<Widget>;
      }

      @route("/widgets/{id}")
      @sharedRoute
      @get
      op getWidgetA(@path id: string, @header accept: "application/json"): JsonWidget;

      @route("/widgets/{id}")
      @sharedRoute
      @get
      op getWidgetB(@path id: string, @header accept: "application/vnd.widget+json"): JsonWidget;
    `);

    expect(doc.endpoints[0].responses[0].contents).toEqual([
      { content_type: "application/json", body_kind: "json", schema: { ref: "Widget" } },
    ]);
  });

  it("fails without writing IR for incompatible same-status content variants with the same media type", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Duplicate Content API" })
        namespace DuplicateContentAPI;

        model Widget {
          id: string;
        }

        model OtherWidget {
          name: string;
        }

        model WidgetResponse {
          ...OkResponse;
          ...Body<Widget>;
        }

        model OtherWidgetResponse {
          ...OkResponse;
          ...Body<OtherWidget>;
        }

        @route("/widgets/{id}")
        @sharedRoute
        @get
        op getWidgetA(@path id: string, @header accept: "application/json"): WidgetResponse;

        @route("/widgets/{id}")
        @sharedRoute
        @get
        op getWidgetB(@path id: string, @header accept: "application/vnd.widget+json"): OtherWidgetResponse;
      `,
      "incompatible response content for status 200 and content type application/json",
    );
  });

  it("fails without writing IR when shared-route operations disagree on APIGen metadata", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Shared Metadata API" })
        namespace SharedMetadataAPI;

        @route("/widgets")
        @sharedRoute
        @get
        @apigen.cli(#{ command: #["widgets", "get"] })
        op getWidgetJson(@header accept: "application/json"): string;

        @route("/widgets")
        @sharedRoute
        @get
        @apigen.cli(#{ command: #["widgets", "download"] })
        op getWidgetBinary(@header accept: "application/octet-stream"): bytes;
      `,
      "incompatible cli metadata",
    );
  });

  it("fails without writing IR when shared-route operations have different namespaces", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Shared Namespace API" })
        namespace SharedNamespaceAPI;

        namespace Access {
          @route("/widgets")
          @sharedRoute
          @get
          op getWidgetJson(@header accept: "application/json"): string;
        }

        namespace Analytics {
          @route("/widgets")
          @sharedRoute
          @get
          op getWidgetBinary(@header accept: "application/octet-stream"): bytes;
        }
      `,
      "incompatible namespace",
    );
  });

  it("fails without writing IR when shared-route operations disagree on authz metadata", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Shared Authz API" })
        namespace SharedAuthzAPI;

        @route("/widgets")
        @sharedRoute
        @get
        @apigen.authz(#{ action: "read" })
        op getWidgetJson(@header accept: "application/json"): string;

        @route("/widgets")
        @sharedRoute
        @get
        @apigen.authz(#{ action: "download" })
        op getWidgetBinary(@header accept: "application/octet-stream"): bytes;
      `,
      "incompatible authz metadata",
    );
  });

  it("fails without writing IR when shared-route operations disagree on auth", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Shared Auth API" })
        namespace SharedAuthAPI;

        @route("/widgets")
        @sharedRoute
        @get
        @useAuth(BearerAuth)
        op getWidgetJson(@header accept: "application/json"): string;

        @route("/widgets")
        @sharedRoute
        @get
        @useAuth(ApiKeyAuth<ApiKeyLocation.header, "X-API-Key">)
        op getWidgetBinary(@header accept: "application/octet-stream"): bytes;
      `,
      "incompatible authentication",
    );
  });

  it("fails without writing IR when shared-route operations disagree on manual handling", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Shared Manual API" })
        namespace SharedManualAPI;

        @route("/widgets")
        @sharedRoute
        @get
        @apigen.manual
        op getWidgetJson(@header accept: "application/json"): string;

        @route("/widgets")
        @sharedRoute
        @get
        op getWidgetBinary(@header accept: "application/octet-stream"): bytes;
      `,
      "incompatible manual metadata",
    );
  });

  it("fails without writing IR when shared-route operations disagree on request bodies or non-literal parameters", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Shared Body API" })
        namespace SharedBodyAPI;

        model Widget {
          name: string;
        }

        @route("/widgets")
        @sharedRoute
        @post
        op createJson(@header contentType: "application/json", @query version: int32, @body body: Widget): string;

        @route("/widgets")
        @sharedRoute
        @post
        op createText(@header contentType: "text/plain", @query version: string, @body body: string): string;
      `,
      "incompatible parameter schema version",
    );
  });

  it("fails without writing IR when shared-route operations disagree on request bodies", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Shared Body API" })
        namespace SharedBodyAPI;

        model Widget {
          name: string;
        }

        @route("/widgets")
        @sharedRoute
        @post
        op createJson(@header contentType: "application/json", @body body: Widget): string;

        @route("/widgets")
        @sharedRoute
        @post
        op createText(@header contentType: "text/plain", @body body: string): string;
      `,
      "incompatible request bodies",
    );
  });

  it("expands TypeSpec status-code unions into concrete IR responses", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Status Union API" })
      namespace StatusUnionAPI;

      model Widget {
        id: string;
      }

      model CreatedOrAccepted {
        @statusCode status: 201 | 202;
        @body body: Widget;
      }

      @route("/widgets")
      @post
      op create(): CreatedOrAccepted;
    `);

    expect(doc.endpoints[0].responses.map((response: any) => response.status_code)).toEqual([
      201,
      202,
    ]);
    expect(doc.endpoints[0].responses[0].contents[0]).toEqual({
      content_type: "application/json",
      body_kind: "json",
      schema: { ref: "Widget" },
    });
  });

  it("fails without writing IR for cookie parameters", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Cookie API" })
        namespace CookieAPI;

        @route("/widgets")
        @get
        op list(@cookie session: string): string;
      `,
      "cookie parameters are not supported",
    );
  });

  it("fails without writing IR for unsupported advanced auth schemes", async () => {
    await expectCompileFails(
      `
        using Http;

        alias MyOAuth2<Scopes extends string[]> = OAuth2Auth<
          [
            {
              type: OAuth2FlowType.implicit;
              authorizationUrl: "https://api.example.com/oauth2/authorize";
            }
          ],
          Scopes
        >;

        @service(#{ title: "OAuth API" })
        @useAuth(MyOAuth2<["read"]>)
        namespace OAuthAPI {
          @route("/widgets")
          @get
          op list(): string;
        }
      `,
      "oauth2 authentication is not supported",
    );
  });

  it("fails without writing IR for non-runtime-compatible auth schemes", async () => {
    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Basic Auth API" })
        @useAuth(BasicAuth)
        namespace BasicAuthAPI {
          @route("/widgets")
          @get
          op list(): string;
        }
      `,
      "http Basic authentication is not supported",
    );

    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Custom API Key API" })
        @useAuth(ApiKeyAuth<ApiKeyLocation.header, "X-Custom-Key">)
        namespace CustomAPIKeyAPI {
          @route("/widgets")
          @get
          op list(): string;
        }
      `,
      "header API key name X-Custom-Key is not supported",
    );

    await expectCompileFails(
      `
        using Http;

        @service(#{ title: "Query API Key API" })
        @useAuth(ApiKeyAuth<ApiKeyLocation.query, "api_key">)
        namespace QueryAPIKeyAPI {
          @route("/widgets")
          @get
          op list(): string;
        }
      `,
      "apiKey authentication in query is not supported",
    );
  });

  it("emits TypeSpec-native file and multipart metadata", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Multipart API" })
      namespace MultipartAPI;

      model Metadata {
        name: string;
      }

      model OkJson<T> {
        ...OkResponse;
        ...Body<T>;
      }

      model Artifact {
        id: string;
      }

      @route("/blob")
      @put
      op uploadBlob(@body body: bytes): OkJson<Artifact>;

      @route("/file")
      @put
      op uploadFile(@bodyRoot body: File<"application/octet-stream", bytes>): OkJson<Artifact>;

      @route("/multipart")
      @post
      op uploadMultipart(@multipartBody body: {
        metadata: HttpPart<Metadata>;
        displayName?: HttpPart<string, #{ name: "display-name" }>;
        attachments: HttpPart<File<"application/octet-stream", bytes>>[];
        samples: HttpPart<string[]>;
      }): OkJson<Artifact>;

      @route("/mixed")
      @post
      op uploadMixed(@header contentType: "multipart/mixed", @multipartBody body: [
        HttpPart<string>,
        HttpPart<File<"application/octet-stream", bytes>, #{ name: "payload" }>,
      ]): OkJson<Artifact>;
    `);

    expect(doc.endpoints[0].request_body.contents[0]).toMatchObject({
      content_type: "application/octet-stream",
      body_kind: "binary",
      schema: { type: "string", format: "binary" },
    });
    expect(doc.endpoints[1].request_body.contents[0]).toMatchObject({
      content_type: "application/octet-stream",
      body_kind: "file",
      schema: { type: "string", format: "binary" },
    });
    expect(doc.endpoints[2].request_body.contents[0].parts).toEqual([
      {
        name: "metadata",
        wire_name: "metadata",
        part_kind: "model",
        required: true,
        content_type: "application/json",
        body_kind: "json",
        schema: { ref: "Metadata" },
      },
      {
        name: "displayName",
        wire_name: "display-name",
        part_kind: "model",
        required: false,
        content_type: "text/plain",
        body_kind: "text",
        schema: { type: "string" },
      },
      {
        name: "attachments",
        wire_name: "attachments",
        part_kind: "model",
        repeated: true,
        required: true,
        content_type: "application/octet-stream",
        body_kind: "file",
        filename: true,
        schema: { type: "string", format: "binary" },
      },
      {
        name: "samples",
        wire_name: "samples",
        part_kind: "model",
        required: true,
        content_type: "application/json",
        body_kind: "json",
        schema: { type: "array", items: { type: "string" } },
      },
    ]);
    expect(doc.endpoints[3].request_body.contents[0]).toMatchObject({
      content_type: "multipart/mixed",
      body_kind: "multipart",
    });
    expect(doc.endpoints[3].request_body.contents[0].parts).toEqual([
      {
        name: "part1",
        part_kind: "tuple",
        required: true,
        content_type: "text/plain",
        body_kind: "text",
        schema: { type: "string" },
      },
      {
        name: "part2",
        wire_name: "payload",
        part_kind: "tuple",
        required: true,
        content_type: "application/octet-stream",
        body_kind: "file",
        filename: true,
        schema: { type: "string", format: "binary" },
      },
    ]);
  });

  it("emits inline string literal unions as enum schemas", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Inline Union API" })
      namespace InlineUnionAPI;

      model Widget {
        status: "active" | "archived";
      }

      @route("/widgets")
      @get
      op list(): Widget;
    `);

    expect(doc.schemas.Widget.properties.status.schema).toEqual({
      type: "string",
      enum: ["active", "archived"],
    });
  });

  it("fails without writing IR for response status ranges", async () => {
    const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
    const irPath = join(outDir, "json-ir.json");

    await expect(
      compileSource(
        `
          using Http;

          @service(#{ title: "Range API" })
          namespace RangeAPI;

          model ServerError {
            @statusCode statusCode: "*";
          }

          @route("/widgets")
          @get
          op list(): ServerError;
        `,
        irPath,
      ),
    ).rejects.toSatisfy((error: any) =>
      `${error.stdout}\n${error.stderr}`.includes("statusCode value must be a three digit code"),
    );
    await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
  });

  it("emits service and operation auth requirements", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Auth API" })
      @useAuth(BearerAuth | ApiKeyAuth<ApiKeyLocation.header, "X-API-Key">)
      namespace AuthAPI;

      model Widget {
        id: string;
      }

      @route("/default")
      @get
      op byDefault(): Widget;

      @useAuth([BearerAuth, ApiKeyAuth<ApiKeyLocation.header, "X-API-Key">])
      @route("/both")
      @get
      op both(): Widget;
    `);

    expect(doc.openapi.security_schemes).toEqual({
      ApiKeyAuth: { type: "apiKey", in: "header", name: "X-API-Key" },
      BearerAuth: { type: "http", scheme: "Bearer" },
    });
    expect(doc.openapi.security).toEqual([{ BearerAuth: [] }, { ApiKeyAuth: [] }]);
    expect(doc.endpoints[0].security).toBeUndefined();
    expect(doc.endpoints[1].security).toEqual([{ BearerAuth: [], ApiKeyAuth: [] }]);
  });

  it("emits typed endpoint tools", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Tools API" })
      namespace ToolsAPI;

      model PageInfo {
        nextCursor?: string;
      }

      model WorkspaceList {
        items: Workspace[];
        page: PageInfo;
      }

      model Workspace {
        id: string;
      }

      @minValue(1)
      @maxValue(100)
      scalar ToolLimit extends int32;

      @apigen.tool(#{
        name: "list_workspace_assets",
        effect: "read",
        tags: #["workspace", "lineage"],
        input: #{ fields: #[
          #{ source: "path", name: "workspace", mode: "context", contextKey: "workspace" },
          #{ source: "query", name: "limit", default: 25 },
        ] },
        output: #{
          mode: "project",
          select: #[#{ source: "/items", countAs: "count", select: #[#{ source: "/id" }] }],
          cursor: #{ source: "/page/nextCursor" },
        },
        metadata: #{ \`x-product-surface\`: "catalog" },
      })
      @route("/workspaces/{workspace}/assets")
      @get
      op listWorkspaces(@path workspace: string, @query limit?: ToolLimit): WorkspaceList;
    `);

    expect(doc.endpoints[0].tool).toEqual({
      name: "list_workspace_assets",
      effect: "read",
      confirmation: "never",
      tags: ["workspace", "lineage"],
      input: {
        fields: [
          { source: "path", name: "workspace", mode: "context", context_key: "workspace" },
          { source: "query", name: "limit", default: 25 },
        ],
      },
      output: {
        mode: "project",
        select: [{ source: "/items", count_as: "count", select: [{ source: "/id" }] }],
        cursor: { source: "/page/nextCursor" },
      },
      metadata: { "x-product-surface": "catalog" },
    });
    expect(doc.endpoints[0].parameters[1].schema).toEqual({
      type: "integer",
      format: "int32",
      minimum: 1,
      maximum: 100,
    });
  });

  it("rejects legacy x-agent extensions", async () => {
    await expectCompileFails(`
      using Http;
      using TypeSpec.OpenAPI;
      @service(#{ title: "Legacy Tool" }) namespace LegacyTool;
      @extension("x-agent", #{ name: "legacy" })
      @route("/legacy") @get op legacy(): string;
    `, "reserved for APIGen-owned metadata");
  });

  it("fails without writing IR for APIGen-reserved generic operation extensions", async () => {
    const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
    const irPath = join(outDir, "json-ir.json");

    await expect(
      compileSource(
        `
          using Http;
          using TypeSpec.OpenAPI;

          @service(#{ title: "Reserved Extension API" })
          namespace ReservedExtensionAPI;

          @extension("x-authz", #{ mode: "none" })
          @route("/widgets")
          @get
          op list(): string;
        `,
        irPath,
      ),
    ).rejects.toSatisfy((error: any) =>
      `${error.stdout}\n${error.stderr}`.includes("reserved for APIGen-owned metadata"),
    );
    await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
  });

  it("fails without writing IR for non-vendor operation extensions", async () => {
    const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
    const irPath = join(outDir, "json-ir.json");

    await expect(
      compileSource(
        `
          using Http;
          using TypeSpec.OpenAPI;

          @service(#{ title: "Invalid Extension API" })
          namespace InvalidExtensionAPI;

          @extension("agent", true)
          @route("/widgets")
          @get
          op list(): string;
        `,
        irPath,
      ),
    ).rejects.toSatisfy((error: any) =>
      `${error.stdout}\n${error.stderr}`.includes("must start with 'x-'"),
    );
    await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
  });

  it("fails without writing IR for x-apigen generic operation extensions", async () => {
    const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
    const irPath = join(outDir, "json-ir.json");

    await expect(
      compileSource(
        `
          using Http;
          using TypeSpec.OpenAPI;

          @service(#{ title: "Reserved Extension API" })
          namespace ReservedExtensionAPI;

          @extension("x-apigen-tool", true)
          @route("/widgets")
          @get
          op list(): string;
        `,
        irPath,
      ),
    ).rejects.toSatisfy((error: any) =>
      `${error.stdout}\n${error.stderr}`.includes("reserved for APIGen-owned metadata"),
    );
    await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
  });

  it("fails for no-auth operation overrides on secured services", async () => {
    const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
    const irPath = join(outDir, "json-ir.json");

    await expect(
      compileSource(
        `
          using Http;

          @service(#{ title: "NoAuth API" })
          @useAuth(BearerAuth)
          namespace NoAuthAPI;

          model Widget {
            id: string;
          }

          @useAuth(NoAuth)
          @route("/public")
          @get
          op publicOp(): Widget;
        `,
        irPath,
      ),
    ).rejects.toSatisfy((error: any) =>
      `${error.stdout}\n${error.stderr}`.includes("does not support NoAuth operation overrides"),
    );
    await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
  });

  it("treats service-level no-auth as unsecured", async () => {
    const doc = await compileSource(`
      using Http;

      @service(#{ title: "Public API" })
      @useAuth(NoAuth)
      namespace PublicAPI;

      @route("/ping")
      @get
      op ping(): string;
    `);

    expect(doc.openapi.security).toBeUndefined();
    expect(doc.openapi.security_schemes).toBeUndefined();
    expect(doc.endpoints[0].security).toBeUndefined();
  });

  it("emits contract-only IR with roots and metadata", async () => {
    const doc = await compileSource(`
      @apigen.\`package\`(#{ title: "LibreDash Signal Contracts", version: "1.0.0", description: "UI contracts" })
      namespace SignalContracts;

      model DashboardPageSignal {
        dashboardId: string;
        pageId: string;
      }

      model DashboardVisual {
        id: string;
        data: Record<unknown>;
      }

      @apigen.contract(#{ kind: "ui-signal", tags: #["dashboard"] })
      @apigen.\`metadata\`(#{ \`x-libredash-surface\`: "dashboard" })
      model DashboardEnvelope {
        @apigen.\`metadata\`(#{ \`x-libredash-signal-key\`: "page" })
        page: DashboardPageSignal;

        @apigen.\`metadata\`(#{ \`x-libredash-signal-key\`: "visuals", \`x-libredash-patch-mode\`: "merge" })
        visuals: Record<DashboardVisual>;
      }
    `);

    expect(doc.schema_version).toBe("v4");
    expect(doc.info).toEqual({
      title: "LibreDash Signal Contracts",
      version: "1.0.0",
      description: "UI contracts",
      namespace: "SignalContracts",
    });
    expect(doc.endpoints).toBeUndefined();
    expect(doc.contracts).toEqual([
      {
        name: "DashboardEnvelope",
        schema: { ref: "DashboardEnvelope" },
        kind: "ui-signal",
        tags: ["dashboard"],
        extensions: { "x-libredash-surface": "dashboard" },
      },
    ]);
    expect(doc.schemas.DashboardEnvelope.properties.page.extensions).toEqual({
      "x-libredash-signal-key": "page",
    });
    expect(doc.schemas.DashboardEnvelope.properties.visuals).toMatchObject({
      schema: {
        type: "object",
        additional_properties: { schema: { ref: "DashboardVisual" } },
      },
      extensions: {
        "x-libredash-signal-key": "visuals",
        "x-libredash-patch-mode": "merge",
      },
    });
  });

  it("preserves producer namespace ownership for imported contract references", async () => {
    const doc = await compileSource(`
      namespace VisualizationContracts {
        @apigen.contract
        model VisualizationEnvelope {
          revision: int64;
        }
      }

      @apigen.\`package\`(#{ title: "Signal Contracts", version: "1.0.0" })
      namespace SignalContracts {
        @apigen.contract(#{ kind: "ui-signal" })
        model DashboardEnvelope {
          visual: VisualizationContracts.VisualizationEnvelope;
        }
      }
    `);

    expect(doc.info.namespace).toBe("SignalContracts");
    expect(doc.schemas.DashboardEnvelope.namespace).toBe("SignalContracts");
    expect(doc.schemas.VisualizationEnvelope.namespace).toBe("VisualizationContracts");
    expect(doc.schemas.DashboardEnvelope.properties.visual.schema).toEqual({
      ref: "VisualizationEnvelope",
    });
    expect(doc.contracts.map((contract: any) => contract.name)).toEqual(["DashboardEnvelope"]);
    expect(doc.schemas.VisualizationEnvelope).toBeDefined();
  });

  it("fails without writing IR for invalid contract metadata keys", async () => {
    await expectCompileFails(
      `
        @apigen.contract
        @apigen.\`metadata\`(#{ invalid: "metadata" })
        model DashboardEnvelope {
          page: string;
        }
      `,
      "must start with 'x-'",
    );
  });

  it("fails clearly when multiple services are declared", async () => {
    const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
    const irPath = join(outDir, "json-ir.json");

    await expect(
      compileSource(
        `
          using Http;

          @service(#{ title: "One" })
          namespace One {
            @route("/one")
            @get
            op one(): string;
          }

          @service(#{ title: "Two" })
          namespace Two {
            @route("/two")
            @get
            op two(): string;
          }
        `,
        irPath,
      ),
    ).rejects.toSatisfy((error: any) =>
      `${error.stdout}\n${error.stderr}`.includes("exactly one TypeSpec service"),
    );
    await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
  });
});

async function compileFixture(name: string, outputFile: string) {
  await compileDirectory(join(root, "test", "fixtures", name), outputFile);
}

async function compileSource(source: string, outputFile?: string, strictOperationKinds = false) {
  const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
  const fixtureDir = join(outDir, "source");
  const irPath = outputFile ?? join(outDir, "json-ir.json");
  await mkdir(fixtureDir, { recursive: true });
  await writeFile(join(fixtureDir, "main.tsp"), source);
  await compileDirectory(fixtureDir, irPath, strictOperationKinds);
  return JSON.parse(await readFile(irPath, "utf8"));
}

async function expectCompileFails(source: string, message: string, strictOperationKinds = false) {
  const outDir = await mkdtemp(join(tmpdir(), "apigen-typespec-"));
  const irPath = join(outDir, "json-ir.json");
  await expect(compileSource(source, irPath, strictOperationKinds)).rejects.toSatisfy((error: any) =>
    `${error.stdout}\n${error.stderr}`.includes(message),
  );
  await expect(stat(irPath)).rejects.toMatchObject({ code: "ENOENT" });
}

async function compileDirectory(sourceDir: string, outputFile: string, strictOperationKinds = false) {
	const strictOptions = strictOperationKinds
		? ["--option", "@yacobolo/apigen.require-explicit-operation-kind=true"]
		: [];
  await execFileAsync(
    process.execPath,
    [
      tsp,
      "compile",
      sourceDir,
      "--import",
      root,
      "--emit",
      root,
      "--option",
      `@yacobolo/apigen.output-file=${outputFile}`,
      "--option",
      "@yacobolo/apigen.base-path=/",
      ...strictOptions,
    ],
    { cwd: root },
  );
}
