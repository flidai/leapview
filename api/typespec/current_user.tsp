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

alias GetCurrentPrincipalOK = OkJson<PrincipalResponse>;

@tag("Current User")
@summary("Get current principal")
@route("/me")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@extension("x-leapview-object-scope", "principal")
@operationId("getCurrentPrincipal")
op getCurrentPrincipal(): GetCurrentPrincipalOK | CommonErrors;

alias ListCurrentAPITokensOK = OkJson<APITokenListResponse>;
alias CreateCurrentAPITokenCreated = CreatedJson<APITokenCreateResponse>;

@tag("Current User")
@route("/me/api-tokens")
interface CurrentAPITokens {
  @summary("List current API tokens")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "principal")
  @operationId("listCurrentAPITokens")
  listCurrentAPITokens(@query limit?: ListLimit, @query pageToken?: PageToken): ListCurrentAPITokensOK | CommonErrors;

  @summary("Create current API token")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "principal")
  @apigen.command(#{ audit: #{ required: true, successAction: "api_token.created", guarantee: "transactional" } })
  @operationId("createCurrentAPIToken")
  createCurrentAPIToken(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: APITokenCreateRequest): CreateCurrentAPITokenCreated | CommonErrors;

  @summary("Revoke current API token")
  @route("/{token}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "principal")
  @apigen.command(#{ audit: #{ required: true, successAction: "api_token.revoked", guarantee: "transactional" } })
  @operationId("revokeCurrentAPIToken")
  revokeCurrentAPIToken(@path token: string): EmptyResponse | CommonErrors;
}

alias ListCurrentEffectivePrivilegesOK = OkJson<EffectivePrivilegesResponse>;

@tag("Current User")
@summary("List current effective privileges")
@route("/me/effective-privileges")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@extension("x-leapview-object-scope", "principal")
@operationId("listCurrentEffectivePrivileges")
op listCurrentEffectivePrivileges(@query workspace?: string): ListCurrentEffectivePrivilegesOK | CommonErrors;

alias ListCurrentSessionsOK = OkJson<SessionListResponse>;

@tag("Current User")
@route("/me/sessions")
interface CurrentSessions {
  @summary("List current sessions")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
  @extension("x-leapview-object-scope", "principal")
  @operationId("listCurrentSessions")
  listCurrentSessions(@query limit?: ListLimit, @query pageToken?: PageToken): ListCurrentSessionsOK | CommonErrors;

  @summary("Revoke current session")
  @route("/{session}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
  @extension("x-leapview-object-scope", "principal")
  @apigen.command(#{ audit: #{ required: true, successAction: "session.revoked", guarantee: "transactional" } })
  @operationId("revokeCurrentSession")
  revokeCurrentSession(@path session: string): EmptyResponse | CommonErrors;
}
