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
  @doc("Omit to keep the token dynamically constrained by the principal's effective privileges at request time. An explicit list must be a subset of the principal's current effective privileges.")
  privileges?: string[];
  expiresAt?: utcDateTime;
}

model APITokenResponse {
  id: string;
  name: string;
  workspaceId?: string;
  @doc("Present when the token has an explicit privilege allowlist. When omitted, the principal's effective privileges are evaluated dynamically at request time.")
  privileges?: string[];
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
  current: boolean;
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

model AvatarResponse {
  principalId: string;
  sha256: string;
  mediaType: "image/png";
  sizeBytes: int64;
  width: int32;
  height: int32;
  updatedAt: utcDateTime;
  url: string;
}

model AvatarImageResponse {
  ...OkResponse;
  @header contentType: "image/png";
  @header etag: string;
  @header cacheControl: string;
  @header contentLength: int64;
  @body body: bytes;
}

enum IdentityManagementSource {
  local: "local",
  external: "external",
  system: "system",
}

model IdentityManagementResponse {
  source: IdentityManagementSource;
  provider?: string;
}

model CurrentPrincipalCapabilities {
  canUpdateDisplayName: boolean;
  canChangePassword: boolean;
  canManageAvatar: boolean;
  canManageSessions: boolean;
  canManageAuthoringSessions: boolean;
  canManageApiTokens: boolean;
}

model CurrentPrincipalResponse {
  ...PrincipalResponse;
  identityManagement: IdentityManagementResponse;
  capabilities: CurrentPrincipalCapabilities;
  avatar?: AvatarResponse;
}

model CurrentPrincipalPatchRequest {
  displayName?: string;
}

model CurrentPasswordChangeRequest {
  currentPassword: string;
  newPassword: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CurrentPrincipalUpdatedAuditPayload {
  @apigen.auditPii
  email: string;
  @apigen.auditPii
  displayName: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CurrentPasswordChangedAuditPayload {
  @apigen.auditPii
  email: string;
  @apigen.auditPublic
  provider: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CurrentAvatarUploadedAuditPayload {
  @apigen.auditInternal
  principalId: string;
  @apigen.auditInternal
  sha256: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CurrentAvatarDeletedAuditPayload {
  @apigen.auditInternal
  principalId: string;
}

alias GetCurrentPrincipalOK = OkJson<CurrentPrincipalResponse>;

@tag("Current User")
@summary("Get current principal")
@route("/me")
@get
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@operationId("getCurrentPrincipal")
op getCurrentPrincipal(): GetCurrentPrincipalOK | CommonErrors;

alias UpdateCurrentPrincipalOK = OkJson<CurrentPrincipalResponse>;

@tag("Current User")
@summary("Update the current principal's self-managed profile fields")
@route("/me")
@patch
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{
    audit: #{ required: true, successAction: "principal.profile.updated", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_PROFILE_NOT_MUTABLE", publicDetail: "The current principal profile cannot be modified." },
    ],
})
@apigen.auditPayload(CurrentPrincipalUpdatedAuditPayload)
@operationId("updateCurrentPrincipal")
op updateCurrentPrincipal(@header("If-Match") ifMatch: string, @body body: CurrentPrincipalPatchRequest): UpdateCurrentPrincipalOK | CommonErrors;

@tag("Current User")
@summary("Change the current principal's local password")
@route("/me/password")
@post
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{
    audit: #{ required: true, successAction: "password.changed", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "PASSWORD_CHANGE_INVALID", publicDetail: "The password change request is invalid." },
    ],
})
@apigen.auditPayload(CurrentPasswordChangedAuditPayload)
@operationId("changeCurrentPassword")
op changeCurrentPassword(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CurrentPasswordChangeRequest): EmptyResponse | CommonErrors;

alias ListCurrentAPITokensOK = OkJson<APITokenListResponse>;
alias CreateCurrentAPITokenCreated = CreatedJson<APITokenCreateResponse>;

@tag("Current User")
@route("/me/api-tokens")
interface CurrentAPITokens {
  @summary("List current API tokens")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "principal")
  @operationId("listCurrentAPITokens")
  listCurrentAPITokens(@query limit?: ListLimit, @query pageToken?: PageToken): ListCurrentAPITokensOK | CommonErrors;

  @summary("Create current API token")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "principal")
  @apigen.command(#{
    audit: #{ required: true, successAction: "api_token.created", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "API_TOKEN_INVALID", publicDetail: "The API token request is invalid." },
    ],
  })
  @apigen.auditPayload(APITokenAuditPayload)
  @operationId("createCurrentAPIToken")
  createCurrentAPIToken(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: APITokenCreateRequest): CreateCurrentAPITokenCreated | CommonErrors;

  @summary("Revoke current API token")
  @route("/{token}")
  @delete
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "principal")
  @apigen.command(#{
    audit: #{ required: true, successAction: "api_token.revoked", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "API_TOKEN_NOT_FOUND", publicDetail: "API token not found." },
    ],
  })
  @apigen.auditPayload(APITokenAuditPayload)
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
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "principal")
  @operationId("listCurrentSessions")
  listCurrentSessions(@query limit?: ListLimit, @query pageToken?: PageToken): ListCurrentSessionsOK | CommonErrors;

  @summary("Revoke current session")
  @route("/{session}")
  @delete
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "principal")
  @apigen.command(#{
    audit: #{ required: true, successAction: "session.revoked", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "SESSION_NOT_FOUND", publicDetail: "Session not found." },
    ],
  })
  @apigen.auditPayload(CurrentSessionRevokedAuditPayload)
  @operationId("revokeCurrentSession")
  revokeCurrentSession(@path session: string): EmptyResponse | CommonErrors;
}

@tag("Current User")
@summary("Upload and normalize the current user's avatar")
@route("/me/avatar")
@put
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{
    audit: #{ required: true, successAction: "principal.avatar.updated", guarantee: "best-effort" },
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "INVALID_AVATAR_IMAGE", publicDetail: "The avatar image is invalid." },
      #{ kind: "unsupported_media_type", statusCode: 415, code: "UNSUPPORTED_AVATAR_MEDIA_TYPE", publicDetail: "The avatar media type is unsupported." },
    ],
})
@apigen.auditPayload(CurrentAvatarUploadedAuditPayload)
@operationId("uploadCurrentAvatar")
op uploadCurrentAvatar(@header contentType: string, @body body: bytes): OkJson<AvatarResponse> | CommonErrors;

@tag("Current User")
@summary("Delete the current user's avatar")
@route("/me/avatar")
@delete
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{
    audit: #{ required: true, successAction: "principal.avatar.deleted", guarantee: "best-effort" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "AVATAR_NOT_FOUND", publicDetail: "The current principal has no avatar." },
    ],
})
@apigen.auditPayload(CurrentAvatarDeletedAuditPayload)
@operationId("deleteCurrentAvatar")
op deleteCurrentAvatar(): EmptyResponse | CommonErrors;

@tag("Current User")
@summary("Read a content-addressed principal avatar")
@route("/principals/{principal}/avatar/{digest}")
@get
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@operationId("getPrincipalAvatar")
op getPrincipalAvatar(@path principal: string, @path digest: string): AvatarImageResponse | CommonErrors;
