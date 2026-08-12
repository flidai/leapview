using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Access;

model EffectivePrivilegesResponse {
  workspaceId: string;
  privileges: string[];
}

model APITokenCreateRequest {
  name: string;
  workspaceId?: string;
  privileges?: string[];
  expiresAt?: utcDateTime;
}

model APITokenResponse {
  id: string;
  name: string;
  workspaceId?: string;
  privileges: string[];
  expiresAt?: utcDateTime;
  revokedAt?: utcDateTime;
  createdAt: utcDateTime;
  lastUsedAt?: utcDateTime;
}

model APITokenCreateResponse {
  token: string;
  apiToken: APITokenResponse;
}

model APITokenListResponse {
  items: APITokenResponse[];
  page: PageInfo;
}

enum SessionKind {
  browser: "browser",
  desktop: "desktop",
}

model SessionResponse {
  id: string;
  kind: SessionKind;
  instanceId?: string;
  profileId?: string;
  clientId?: string;
  createdAt: utcDateTime;
  expiresAt: utcDateTime;
  absoluteExpiresAt?: utcDateTime;
  lastSeenAt?: utcDateTime;
  revokedAt?: utcDateTime;
}

model SessionListResponse {
  items: SessionResponse[];
  page: PageInfo;
}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "API_TOKEN_INVALID", publicDetail: "The API token request is invalid." })
model APITokenInvalidFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "API_TOKEN_NOT_FOUND", publicDetail: "API token not found." })
model APITokenNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "SESSION_NOT_FOUND", publicDetail: "Session not found." })
model SessionNotFoundFailure {}

alias GetCurrentPrincipalOK = OkJson<PrincipalResponse>;

@tag("Current User")
@summary("Get current principal")
@route("/me")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@extension("x-leapview-object-scope", "principal")
op getCurrentPrincipal(): GetCurrentPrincipalOK | CommonErrors;

alias ListCurrentAPITokensOK = OkJson<APITokenListResponse>;
alias CreateCurrentAPITokenCreated = CreatedJson<APITokenCreateResponse>;

@tag("Current User")
@route("/me/api-tokens")
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
@apigen.commandDefaults(#{ guarantee: "transactional" })
@apigen.auditPayload(APITokenAuditPayload)
interface CurrentAPITokens {
  @summary("List current API tokens")
  @get
  @extension("x-leapview-object-scope", "principal")
  listCurrentAPITokens(@query limit?: ListLimit, @query pageToken?: PageToken): ListCurrentAPITokensOK | CommonErrors;

  @summary("Create current API token")
  @post
  @extension("x-leapview-object-scope", "principal")
  @apigen.failsWith(APITokenInvalidFailure)
  @apigen.command(#{ auditAction: "api_token.created" })
  createCurrentAPIToken(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: APITokenCreateRequest): CreateCurrentAPITokenCreated | CommonErrors;

  @summary("Revoke current API token")
  @route("/{token}")
  @delete
  @extension("x-leapview-object-scope", "principal")
  @apigen.failsWith(APITokenNotFoundFailure)
  @apigen.command(#{ auditAction: "api_token.revoked" })
  revokeCurrentAPIToken(@path @apigen.target token: string): EmptyResponse | CommonErrors;
}

alias ListCurrentEffectivePrivilegesOK = OkJson<EffectivePrivilegesResponse>;

@tag("Current User")
@summary("List current effective privileges")
@route("/me/effective-privileges")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@extension("x-leapview-object-scope", "principal")
op listCurrentEffectivePrivileges(@query workspace?: string): ListCurrentEffectivePrivilegesOK | CommonErrors;

alias ListCurrentSessionsOK = OkJson<SessionListResponse>;

@tag("Current User")
@route("/me/sessions")
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
interface CurrentSessions {
  @summary("List current sessions")
  @get
  @extension("x-leapview-object-scope", "principal")
  listCurrentSessions(@query limit?: ListLimit, @query pageToken?: PageToken): ListCurrentSessionsOK | CommonErrors;

  @summary("Revoke current session")
  @route("/{session}")
  @delete
  @extension("x-leapview-object-scope", "principal")
  @apigen.failsWith(SessionNotFoundFailure)
  @apigen.command(#{ auditAction: "session.revoked", guarantee: "transactional" })
  @apigen.auditPayload(CurrentSessionRevokedAuditPayload)
  revokeCurrentSession(@path @apigen.target session: string): EmptyResponse | CommonErrors;
}
