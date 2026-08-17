using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Access;

model EffectiveCapabilitiesResponse {
  capabilities: Capability[];
}

model APITokenCreateRequest {
  name: string;
  @doc("Omit to keep the token dynamically constrained by the principal's effective capabilities at request time. An explicit list must be a subset of the principal's current effective capabilities.")
  capabilities?: Capability[];
  expiresAt?: utcDateTime;
}

model APITokenResponse {
  id: string;
  name: string;
  @doc("Present when the token has an explicit capability allowlist. When omitted, the principal's effective capabilities are evaluated dynamically at request time.")
  capabilities?: Capability[];
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

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "API_TOKEN_INVALID", publicDetail: "The API token request is invalid." })
model APITokenInvalidFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "API_TOKEN_NOT_FOUND", publicDetail: "API token not found." })
model APITokenNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "SESSION_NOT_FOUND", publicDetail: "Session not found." })
model SessionNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_PROFILE_NOT_MUTABLE", publicDetail: "The current principal profile cannot be modified." })
model PrincipalProfileNotMutableFailure {}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "PASSWORD_CHANGE_INVALID", publicDetail: "The password change request is invalid." })
model PasswordChangeInvalidFailure {}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 422, code: "INVALID_AVATAR_IMAGE", publicDetail: "The avatar image is invalid." })
model InvalidAvatarImageFailure {}

@apigen.failureDefinition(#{ kind: "unsupported_media_type", statusCode: 415, code: "UNSUPPORTED_AVATAR_MEDIA_TYPE", publicDetail: "The avatar media type is unsupported." })
model UnsupportedAvatarMediaTypeFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "AVATAR_NOT_FOUND", publicDetail: "The current principal has no avatar." })
model AvatarNotFoundFailure {}

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

model CurrentThemeUpdateRequest {
  theme: string;
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

@apigen.auditSchema(#{ schemaVersion: 1, retention: "standard" })
model CurrentThemeUpdatedAuditPayload {
  @apigen.auditPublic
  theme: string;
}

alias GetCurrentPrincipalOK = OkJson<CurrentPrincipalResponse>;

@tag("Current User")
@summary("Get current principal")
@route("/me")
@get
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
op getCurrentPrincipal(): GetCurrentPrincipalOK | CommonErrors;

alias UpdateCurrentPrincipalOK = OkJson<CurrentPrincipalResponse>;

@tag("Current User")
@summary("Update the current principal's self-managed profile fields")
@route("/me")
@patch
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{ auditAction: "principal.profile.updated", guarantee: "transactional" })
@apigen.failsWith(PrincipalProfileNotMutableFailure)
@apigen.auditPayload(CurrentPrincipalUpdatedAuditPayload)
@apigen.ui("current-user.profile.update")
op updateCurrentPrincipal(@header("If-Match") ifMatch: string, @body body: CurrentPrincipalPatchRequest): UpdateCurrentPrincipalOK | CommonErrors;

@tag("Current User")
@summary("Change the current principal's local password")
@route("/me/password")
@post
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{ auditAction: "password.changed", guarantee: "transactional" })
@apigen.failsWith(PasswordChangeInvalidFailure)
@apigen.auditPayload(CurrentPasswordChangedAuditPayload)
@apigen.ui("current-user.password.change")
op changeCurrentPassword(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: CurrentPasswordChangeRequest): EmptyResponse | CommonErrors;

@tag("Current User")
@summary("Update the current user's display theme")
@route("/me/theme")
@put
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{ auditAction: "principal.theme.updated", guarantee: "transactional", failures: #[
  #{ kind: "invalid", statusCode: 422, code: "INVALID_THEME", publicDetail: "The selected theme is invalid." },
] })
@apigen.auditPayload(CurrentThemeUpdatedAuditPayload)
@apigen.ui("current-user.theme.update")
op updateCurrentTheme(@body body: CurrentThemeUpdateRequest): EmptyResponse | CommonErrors;

alias ListCurrentAPITokensOK = OkJson<APITokenListResponse>;
alias CreateCurrentAPITokenCreated = CreatedJson<APITokenCreateResponse>;

@tag("Current User")
@route("/me/api-tokens")
@apigen.authz(#{ mode: "authenticated" })
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
  @apigen.ui("current-user.api-token.create")
  createCurrentAPIToken(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: APITokenCreateRequest): CreateCurrentAPITokenCreated | CommonErrors;

  @summary("Revoke current API token")
  @route("/{token}")
  @delete
  @extension("x-leapview-object-scope", "principal")
  @apigen.failsWith(APITokenNotFoundFailure)
  @apigen.command(#{ auditAction: "api_token.revoked" })
  @apigen.ui("current-user.api-token.revoke")
  revokeCurrentAPIToken(@path @apigen.target token: string): EmptyResponse | CommonErrors;
}

alias ListCurrentEffectiveCapabilitiesOK = OkJson<EffectiveCapabilitiesResponse>;

@tag("Current User")
@summary("List current effective capabilities")
@route("/me/effective-capabilities")
@get
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
op listCurrentEffectiveCapabilities(): ListCurrentEffectiveCapabilitiesOK | CommonErrors;

alias ListCurrentSessionsOK = OkJson<SessionListResponse>;

@tag("Current User")
@route("/me/sessions")
@apigen.authz(#{ mode: "authenticated" })
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
  @apigen.ui("current-user.session.revoke")
  revokeCurrentSession(@path @apigen.target session: string): EmptyResponse | CommonErrors;
}

@tag("Current User")
@summary("Upload and normalize the current user's avatar")
@route("/me/avatar")
@put
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{ auditAction: "principal.avatar.updated", guarantee: "best-effort" })
@apigen.failsWith(InvalidAvatarImageFailure)
@apigen.failsWith(UnsupportedAvatarMediaTypeFailure)
@apigen.auditPayload(CurrentAvatarUploadedAuditPayload)
@apigen.ui("current-user.avatar.upload")
op uploadCurrentAvatar(@header contentType: string, @body body: bytes): OkJson<AvatarResponse> | CommonErrors;

@tag("Current User")
@summary("Delete the current user's avatar")
@route("/me/avatar")
@delete
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
@apigen.command(#{ auditAction: "principal.avatar.deleted", guarantee: "best-effort" })
@apigen.failsWith(AvatarNotFoundFailure)
@apigen.auditPayload(CurrentAvatarDeletedAuditPayload)
@apigen.ui("current-user.avatar.delete")
op deleteCurrentAvatar(): EmptyResponse | CommonErrors;

@tag("Current User")
@summary("Read a content-addressed principal avatar")
@route("/principals/{principal}/avatar/{digest}")
@get
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "principal")
op getPrincipalAvatar(@path principal: string, @path digest: string): AvatarImageResponse | CommonErrors;
