import { describe, expect, it } from "vitest";
import { compileSource, expectCompileFails } from "./compiler.js";

describe("APIGen command idempotency authoring", () => {
  it("emits forbidden for a protected synchronous JSON POST", async () => {
    const doc = await compileSource(`
      using Http;
      using OpenAPI;

      @service(#{ title: "Credential API" })
      namespace CredentialAPI {
        model CredentialDraftRequest { value: string; }
        model CredentialDraftAudit {
          @apigen.auditInternal draftId: string;
        }

        @route("/credential-drafts")
        @apigen.authz(#{ mode: "authenticated" })
        @apigen.auditPayload(CredentialDraftAudit, #{ schemaVersion: 1, retention: "security" })
        interface CredentialDrafts {
          @post
          @operationId("createCredentialDraft")
          @apigen.command(#{
            nonReplayable: true,
            audit: #{ required: true, successAction: "credential_draft.created", guarantee: "transactional" },
            failures: #[],
          })
          create(@body body: CredentialDraftRequest): string;
        }
      }
    `);

    expect(doc.endpoints[0]).toMatchObject({
      method: "post",
      request_body: {
        required: true,
        contents: [{ content_type: "application/json", body_kind: "json" }],
      },
      command: {
        idempotency: "forbidden",
        audit: { required: true, guarantee: "transactional" },
        authz_mode: "authenticated",
      },
    });
  });

  it.each([
    {
      name: "method",
      message: "nonReplayable commands must use POST",
      operation: `
        @put
        @operationId("replaceWidget")
        @apigen.authz(#{ mode: "authenticated" })
        @apigen.command(#{ nonReplayable: true, audit: #{ required: true, successAction: "widget.replaced", guarantee: "transactional" }, failures: #[] })
        op replace(@body body: string): string;
      `,
    },
    {
      name: "optional idempotency header",
      message: "nonReplayable commands cannot declare an Idempotency-Key header",
      operation: `
        @post
        @operationId("createWidget")
        @apigen.authz(#{ mode: "authenticated" })
        @apigen.command(#{ nonReplayable: true, audit: #{ required: true, successAction: "widget.created", guarantee: "transactional" }, failures: #[] })
        op create(@header("Idempotency-Key") key?: string, @body body: string): string;
      `,
    },
    {
      name: "missing body",
      message: "nonReplayable commands require a required JSON request body",
      operation: `
        @post
        @operationId("createWidget")
        @apigen.authz(#{ mode: "authenticated" })
        @apigen.command(#{ nonReplayable: true, audit: #{ required: true, successAction: "widget.created", guarantee: "transactional" }, failures: #[] })
        op create(): string;
      `,
    },
    {
      name: "optional body",
      message: "nonReplayable commands require a required JSON request body",
      operation: `
        @post
        @operationId("createWidget")
        @apigen.authz(#{ mode: "authenticated" })
        @apigen.command(#{ nonReplayable: true, audit: #{ required: true, successAction: "widget.created", guarantee: "transactional" }, failures: #[] })
        op create(@body body?: string): string;
      `,
    },
    {
      name: "non JSON body",
      message: "nonReplayable commands require a required JSON request body",
      operation: `
        @post
        @operationId("createWidget")
        @apigen.authz(#{ mode: "authenticated" })
        @apigen.command(#{ nonReplayable: true, audit: #{ required: true, successAction: "widget.created", guarantee: "transactional" }, failures: #[] })
        op create(@header contentType: "text/plain", @body body: string): string;
      `,
    },
    {
      name: "async execution",
      message: "nonReplayable commands cannot use async execution",
      operation: `
        @post
        @operationId("finalizeWidget")
        @apigen.authz(#{ mode: "authenticated" })
        @apigen.command(#{
          nonReplayable: true,
          audit: #{ required: true, successAction: "widget.finalizing", guarantee: "transactional" },
          failures: #[],
          execution: #{ mode: "async", guarantee: "transactional", jobKind: "widget.finalize", resourceKind: "widget", initialEvent: "widget.finalizing", initialState: "finalizing", statusOperation: "getWidget", eventsOperation: "listWidgetEvents", cancellation: "unsupported" },
        })
        op finalize(@body body: string): string;
      `,
    },
    {
      name: "transactional audit",
      message: "nonReplayable commands require required transactional audit",
      operation: `
        @post
        @operationId("createWidget")
        @apigen.authz(#{ mode: "authenticated" })
        @apigen.command(#{ nonReplayable: true, audit: #{ required: false, successAction: "widget.created", guarantee: "best-effort" }, failures: #[] })
        op create(@body body: string): string;
      `,
    },
    {
      name: "authorization",
      message: "nonReplayable commands require authenticated or privilege authorization",
      operation: `
        @post
        @operationId("createWidget")
        @apigen.command(#{ nonReplayable: true, audit: #{ required: true, successAction: "widget.created", guarantee: "transactional" }, failures: #[] })
        op create(@body body: string): string;
      `,
    },
  ])("rejects invalid policy combination: $name", async (testCase) => {
    await expectCompileFails(`
      using Http;
      using OpenAPI;
      @service(#{ title: "Invalid Command API" })
      @route("/commands")
      namespace InvalidCommandAPI {
        ${testCase.operation}
      }
    `, testCase.message);
  });
});
