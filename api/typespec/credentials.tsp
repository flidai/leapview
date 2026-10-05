using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Credential;

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CredentialDraftAuditPayload {
  @apigen.auditInternal
  version_id: string;

  @apigen.auditInternal
  purpose: string;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "INVALID_CREDENTIAL_DRAFT", publicDetail: "Credential draft request is invalid." })
model InvalidCredentialDraftFailure {}

@apigen.failureDefinition(#{ kind: "unauthorized", statusCode: 403, code: "CREDENTIAL_DRAFT_FORBIDDEN", publicDetail: "Credential draft access is forbidden." })
model ForbiddenCredentialDraftFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "CREDENTIAL_DRAFT_NOT_FOUND", publicDetail: "Credential draft was not found." })
model CredentialDraftNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "CREDENTIAL_DRAFT_CONFLICT", publicDetail: "Credential draft conflicts with stored state." })
model CredentialDraftConflictFailure {}

@apigen.failureDefinition(#{ kind: "provider_unavailable", statusCode: 503, code: "CREDENTIAL_SERVICE_UNAVAILABLE", publicDetail: "Credential draft service is unavailable." })
model CredentialDraftServiceUnavailableFailure {}

@minLength(36)
@maxLength(36)
@pattern("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")
scalar CredentialDraftVersionId extends string;

@minValue(1)
@maxValue(100)
scalar CredentialDraftListLimit extends int32;

@doc("Encrypted draft metadata only. Credential values, ciphertext, provider references and digests are never returned.")
model CredentialDraftResponse {
  versionId: CredentialDraftVersionId;
  createdAt: utcDateTime;
  state: "draft";
}

model CredentialDraftListResponse {
  items: CredentialDraftResponse[];
  nextBeforeVersionId?: CredentialDraftVersionId;
}

@doc("The secret field values accepted for the connector resolved from the connection binding. The encoded JSON request body is limited to 32 KiB and the typed secret bundle to 16 KiB. Names and values are secret input and are never reflected in responses, audit payloads or logs.")
model CredentialDraftSaveRequest {
  @apigen.sensitivity("secret")
  fields: Record<string>;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CredentialValidationAuditPayload {
  @apigen.auditInternal receipt_id: string;
  @apigen.auditInternal version_id: string;
  @apigen.auditInternal binding_id: string;
  @apigen.auditInternal binding_revision: int64;
  @apigen.auditInternal configuration_digest: string;
  @apigen.auditInternal expires_at: utcDateTime;
}

@apigen.failureDefinition(#{ kind: "credential_invalid", statusCode: 422, code: "CREDENTIAL_VALIDATION_FAILED", publicDetail: "Credential validation failed." })
model CredentialValidationFailedFailure {}

model CredentialValidationRequest {
  @minValue(1)
  expectedBindingRevision: int64;
}

scalar CredentialValidationReceiptId extends CredentialDraftVersionId;

@doc("Observation from an isolated probe of this saved version and binding revision. Validation does not activate the credential or permit revoking the previous credential. The receipt expires after five minutes and can become stale sooner if configuration or authority changes.")
model CredentialValidationResponse {
  receiptId: CredentialValidationReceiptId;
  versionId: CredentialDraftVersionId;
  bindingRevision: int64;
  validatedAt: utcDateTime;
  expiresAt: utcDateTime;
  state: "validated";
}

@route("/projects/{project}/targets/{target}/connection-bindings/{connection}/credential-drafts")
@tag("Credentials")
interface ConnectionCredentialDrafts {
  @summary("List connection credential draft metadata")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.read", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  listConnectionCredentialDrafts(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @query limit?: CredentialDraftListLimit,
    @query beforeVersionId?: CredentialDraftVersionId,
  ): OkJson<CredentialDraftListResponse> | CommonErrors;

  @summary("Save a connection credential draft")
  @doc("Stores a new encrypted, immutable draft only. Saving does not validate, publish or activate the credential. A lost response can leave an unreturned draft; explicit resubmission may create a second draft.")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidCredentialDraftFailure)
  @apigen.failsWith(ForbiddenCredentialDraftFailure)
  @apigen.failsWith(CredentialDraftNotFoundFailure)
  @apigen.failsWith(CredentialDraftConflictFailure)
  @apigen.failsWith(CredentialDraftServiceUnavailableFailure)
  @apigen.command(#{
    auditAction: "credential.draft.saved",
    guarantee: "transactional",
    nonReplayable: true,
  })
  @apigen.auditPayload(CredentialDraftAuditPayload)
  saveCredentialDraft(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @body body: CredentialDraftSaveRequest,
  ): CreatedJson<CredentialDraftResponse> | CommonErrors;

  @summary("Read connection credential draft metadata")
  @route("/{version}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.read", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  getConnectionCredentialDraft(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path version: CredentialDraftVersionId,
  ): OkJson<CredentialDraftResponse> | CommonErrors;

  @summary("Validate a saved connection credential draft")
  @doc("Requires both connection.manage and connection.use on the exact connection. Probes only the saved PostgreSQL password version against the server-owned binding at expectedBindingRevision; it never changes running pools. Every explicit submission performs a fresh probe; Idempotency-Key is forbidden. A lost response may leave a receipt without returning it. Success means validated, not in use.")
  @route("/{version}/validate")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_MANAGE", action: "connection.manage", resolver: "connection" })
  @extension("x-leapview-object-scope", "connection")
  @apigen.failsWith(InvalidCredentialDraftFailure)
  @apigen.failsWith(ForbiddenCredentialDraftFailure)
  @apigen.failsWith(CredentialDraftNotFoundFailure)
  @apigen.failsWith(CredentialDraftConflictFailure)
  @apigen.failsWith(CredentialDraftServiceUnavailableFailure)
  @apigen.failsWith(CredentialValidationFailedFailure)
  @apigen.command(#{ auditAction: "credential.draft.validated", guarantee: "transactional", nonReplayable: true })
  @apigen.auditPayload(CredentialValidationAuditPayload)
  validateCredentialDraft(
    @path project: ResourceId,
    @path target: ResourceId,
    @path @apigen.target connection: ResourceId,
    @path version: CredentialDraftVersionId,
    @body body: CredentialValidationRequest,
  ): OkJson<CredentialValidationResponse> | CommonErrors;

}
