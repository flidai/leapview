using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Access;

model PrincipalResponse {
  id: string;
  kind: string;
  email: string;
  displayName: string;
  @doc("Lifecycle disable owned by the identity provisioning authority, such as SCIM active=false.")
  disabledAt?: utcDateTime;
  @doc("LeapView administrator block. This is independent from externally managed lifecycle state.")
  blockedAt?: utcDateTime;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model PrincipalAdministrationCapabilities {
  canUpdateProfile: boolean;
  canResetPassword: boolean;
  canBlock: boolean;
  canUnblock: boolean;
  canDelete: boolean;
  canManageSessions: boolean;
  canManageAuthorization: boolean;
}

model PrincipalAdministrationResponse {
  ...PrincipalResponse;
  identityManagement: IdentityManagementResponse;
  capabilities: PrincipalAdministrationCapabilities;
}

model PrincipalListResponse {
  items: PrincipalAdministrationResponse[];
  page: PageInfo;
}

model PrincipalPatchRequest {
  displayName?: string;
}

model PrincipalCreateRequest {
  email: string;
  displayName?: string;
}

model LocalPasswordResetResponse {
  principal: PrincipalResponse;
  temporaryPassword: string;
}

model ServicePrincipalCreateRequest {
  id?: string;
  displayName: string;
}

model ServicePrincipalPatchRequest {
  displayName?: string;
}

model ServicePrincipalListResponse {
  items: PrincipalResponse[];
  page: PageInfo;
}

model ServicePrincipalSecretCreateRequest {
  name: string;
  expiresAt?: utcDateTime;
}

model ServicePrincipalSecretResponse {
  id: string;
  servicePrincipalId: string;
  name: string;
  secret?: string;
  expiresAt?: utcDateTime;
  createdAt?: utcDateTime;
  revokedAt?: utcDateTime;
}

model ServicePrincipalSecretCreatedResponse {
  secret: string;
  clientSecret: ServicePrincipalSecretResponse;
}

model ServicePrincipalSecretListResponse {
  items: ServicePrincipalSecretResponse[];
  page: PageInfo;
}

model RoleResponse {
  name: string;
  capabilities: Capability[];
}

model RoleListResponse {
  items: RoleResponse[];
  page: PageInfo;
}

model RoleBindingResponse {
  id: string;
  name: string;
  subjectType: string;
  subjectId: string;
  role: string;
  @doc("The versioned typed permission profile captured with this assignment.")
  permissionProfile?: PermissionCatalogProfile;
  @doc("The exact immutable action-target expansion captured at assignment time.")
  permissions?: PermissionPair[];
  @doc("Legacy read-only capability evidence. New role bindings never populate this field.")
  capabilities?: Capability[];
  policyRevision: int64;
  policyDigest: string;
}

model RoleBindingListResponse {
  items: RoleBindingResponse[];
  page: PageInfo;
  targetId: string;
  projectId: ResourceId;
  environment: string;
  policyRevision: int64;
  policyDigest: string;
}

model RoleBindingCreateRequest {
  id: string;
  name?: string;
  subjectType: string;
  subjectId: string;
  @doc("A versioned named role from the typed permission catalog. The server captures its exact expansion.")
  role: string;
  @doc("A live bounded envelope authorizing this exact recipient and role expansion. Omission is accepted only for the three canonical bindings of the durable project-claim bootstrap principal while claim bootstrap remains open.")
  grantAdminEnvelopeId?: string;
  @doc("Compare-and-swap fence. Use 0 when creating the first policy revision.")
  expectedRevision: int64;
}

model RoleBindingDeleteRequest {
  @doc("Compare-and-swap fence for the policy revision containing the binding.")
  expectedRevision: int64;
}

model RoleBindingMutationResponse {
  targetId: string;
  projectId: ResourceId;
  environment: string;
  policyRevision: int64;
  policyDigest: string;
}

model GroupCreateRequest {
  name: string;
  displayName?: string;
}

model GroupPatchRequest {
  displayName?: string;
}

model GroupResponse {
  id: string;
  provider: string;
  externalId: string;
  name: string;
  displayName: string;
  capabilities: GroupAdministrationCapabilities;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model GroupAdministrationCapabilities {
  canUpdate: boolean;
  canDelete: boolean;
  canManageMembers: boolean;
  canManageAuthorization: boolean;
}

model GroupListResponse {
  items: GroupResponse[];
  page: PageInfo;
}

model GroupMemberListResponse {
  items: PrincipalResponse[];
  page: PageInfo;
}

@apigen.failureDefinition(#{
  kind: "denied",
  statusCode: 400,
  code: "DEVICE_AUTHORIZATION_DENIED",
  publicDetail: "Device authorization was denied.",
})
model DeviceAuthorizationDeniedFailure {}

@apigen.failureDefinition(#{
  kind: "expired",
  statusCode: 400,
  code: "DEVICE_AUTHORIZATION_EXPIRED",
  publicDetail: "Device authorization has expired.",
})
model DeviceAuthorizationExpiredFailure {}

@apigen.failureDefinition(#{
  kind: "invalid_principal",
  statusCode: 400,
  code: "INVALID_PRINCIPAL",
  publicDetail: "The authorizing principal is invalid.",
})
model InvalidPrincipalFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "DEVICE_AUTHORIZATION_NOT_FOUND",
  publicDetail: "Device authorization was not found.",
})
model DeviceAuthorizationNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "invalid_credential",
  statusCode: 400,
  code: "INVALID_AUTHORING_CREDENTIAL",
  publicDetail: "The authoring session is invalid.",
})
model InvalidAuthoringCredentialFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "AUTHORING_SESSION_NOT_FOUND",
  publicDetail: "Authoring session not found.",
})
model AuthoringSessionNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "conflict",
  statusCode: 409,
  code: "PRINCIPAL_ALREADY_EXISTS",
  publicDetail: "A principal with this email already exists.",
})
model PrincipalAlreadyExistsFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "PRINCIPAL_NOT_FOUND",
  publicDetail: "Principal not found.",
})
model PrincipalNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "SERVICE_PRINCIPAL_INVALID",
  publicDetail: "The service principal request is invalid.",
})
model ServicePrincipalInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "SERVICE_PRINCIPAL_NOT_FOUND",
  publicDetail: "Service principal not found.",
})
model ServicePrincipalNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "SERVICE_PRINCIPAL_SECRET_INVALID",
  publicDetail: "The service principal secret request is invalid.",
})
model ServicePrincipalSecretInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "SERVICE_PRINCIPAL_SECRET_NOT_FOUND",
  publicDetail: "Service principal secret not found.",
})
model ServicePrincipalSecretNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "GROUP_INVALID",
  publicDetail: "The group request is invalid.",
})
model GroupInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "GROUP_NOT_FOUND",
  publicDetail: "Group not found.",
})
model GroupNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 422,
  code: "GROUP_NOT_MUTABLE",
  publicDetail: "This group is managed by an external provisioning system.",
})
model GroupNotMutableFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "GRANT_NOT_FOUND",
  publicDetail: "Grant not found.",
})
model GrantNotFoundFailure {}

// Durable, typed payloads for Access command audit records.  Every field is
// required so the generated encoder can reject incomplete audit events before
// they reach the repository.
@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DeviceAuthorizationDecidedAuditPayload {
  @apigen.auditInternal
  clientId: string;
  @apigen.auditInternal
  targetId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditPublic
  capabilities: Capability[];
  @apigen.auditPublic
  decision: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model AuthoringSessionRevokedAuditPayload {
  @apigen.auditInternal
  kind: string;
  @apigen.auditInternal
  clientId: string;
  @apigen.auditInternal
  targetId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditPublic
  capabilities: Capability[];
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model APITokenAuditPayload {
  @apigen.auditInternal
  name: string;
  @apigen.auditPublic
  capabilities: Capability[];
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model CurrentSessionRevokedAuditPayload {
  @apigen.auditInternal
  sessionId: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model PrincipalCreatedAuditPayload {
  @apigen.auditPii
  email: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model PrincipalDeletedAuditPayload {
  @apigen.auditPii
  email: string;
  @apigen.auditInternal
  kind: string;
  @apigen.auditPii
  displayName: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model PrincipalUpdatedAuditPayload {
  @apigen.auditPii
  email: string;
  @apigen.auditInternal
  kind: string;
  @apigen.auditPii
  displayName: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model PrincipalPasswordResetAuditPayload {
  @apigen.auditPii
  email: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model PrincipalSessionRevokedAuditPayload {
  @apigen.auditPii
  targetPrincipalId: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ServicePrincipalAuditPayload {
  @apigen.auditInternal
  servicePrincipalId: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ServicePrincipalSecretAuditPayload {
  @apigen.auditInternal
  secretId: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model GroupAuditPayload {
  @apigen.auditPublic
  provider: string;
  @apigen.auditInternal
  externalId: string;
  @apigen.auditPii
  displayName: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model GroupMemberAuditPayload {
  @apigen.auditInternal
  groupId: string;
  @apigen.auditPii
  memberPrincipalId: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model GrantAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  resourceId: ResourceId;
  @apigen.auditPublic
  resourceKind: ResourceKind;
  @apigen.auditPublic
  environment: string;
  @apigen.auditPii
  subjectId: string;
  @apigen.auditPublic
  subjectType: string;
  @apigen.auditPublic
  capability: Capability;
  @apigen.auditPublic
  surface: string;
}

model GrantRequest {
  resourceKind: ResourceKind;
  resourceId: ResourceId;
  subjectType: string;
  subjectId: string;
  capability: Capability;
}

model GrantResponse {
  id: string;
  resourceId: ResourceId;
  resourceKind: ResourceKind;
  subjectType: string;
  subjectId: string;
  capability: Capability;
  inherited?: boolean;
  parentId?: string;
  parentResourceKind?: ResourceKind;
  parentResourceId?: ResourceId;
  createdAt: utcDateTime;
}

model GrantListResponse {
  items: GrantResponse[];
  page: PageInfo;
}

// Durable resource shares are exact, server-bound grants. Issuer identity,
// issuance ceiling, instance identity, and the request digest are all derived
// by the trusted service and are intentionally absent from this request.
model ResourceShareGrantIssueRequest {
  resourceUid: string;
  resourceKind: ResourceKind;
  resourceId: ResourceId;
  recipientType: string;
  recipientId: string;
  permissions: PermissionPair[];
  @doc("Optional bounded lifetime in seconds. The server derives issuance and expiry timestamps.")
  ttlSeconds?: int64;
}

model ResourceShareGrantRevokeRequest {
  reason?: string;
}

model ResourceShareGrantResponse {
  id: string;
  profile: string;
  instanceId: string;
  projectId: ResourceId;
  resourceUid: string;
  resourceKind: ResourceKind;
  resourceId: ResourceId;
  recipientType: string;
  recipientId: string;
  permissions: PermissionPair[];
  issuedAt: utcDateTime;
  expiresAt?: utcDateTime;
  fingerprint: string;
  idempotencyKey: string;
  requestDigest: string;
  revokedAt?: utcDateTime;
  revokedByPrincipalId?: string;
  revocationReason?: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ResourceShareGrantAuditPayload {
  @apigen.auditInternal
  grantId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditPublic
  resourceKind: ResourceKind;
  @apigen.auditInternal
  resourceId: ResourceId;
  @apigen.auditInternal
  resourceUid: string;
  @apigen.auditPublic
  recipientType: string;
  @apigen.auditPii
  recipientId: string;
  @apigen.auditInternal
  fingerprint: string;
}

alias ResourceShareGrantCreated = CreatedJson<ResourceShareGrantResponse>;
alias ResourceShareGrantRevoked = OkJson<ResourceShareGrantResponse>;

// Grant-administration envelopes are project-bound, exact delegation
// authorities. The server derives issuer, bound principal, credential
// evidence, and the coherent manage+delegate ceiling; none are caller input.
model GrantAdminEnvelopeIssueRequest {
  permissions: PermissionPair[];
  recipientSelector: string;
  roleVersion: string;
  targetResourceKind?: ResourceKind;
  targetResourceId?: ResourceId;
  @doc("Required bounded lifetime in seconds; the server derives issuance and expiry timestamps.")
  ttlSeconds: int64;
}

model GrantAdminEnvelopeRevokeRequest {
  reason?: string;
}

model GrantAdminEnvelopeResponse {
  id: string;
  profile: string;
  projectId: ResourceId;
  permissions: PermissionPair[];
  targetResourceKind?: ResourceKind;
  targetResourceId?: ResourceId;
  recipientSelector: string;
  roleVersion: string;
  issuedAt: utcDateTime;
  expiresAt: utcDateTime;
  fingerprint: string;
  idempotencyKey: string;
  requestDigest: string;
  revokedAt?: utcDateTime;
  revokedByPrincipalId?: string;
  revocationReason?: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model GrantAdminEnvelopeAuditPayload {
  @apigen.auditInternal
  envelopeId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditPublic
  recipientSelector: string;
  @apigen.auditPublic
  roleVersion: string;
  @apigen.auditInternal
  fingerprint: string;
}

alias GrantAdminEnvelopeCreated = CreatedJson<GrantAdminEnvelopeResponse>;
alias GrantAdminEnvelopeRevoked = OkJson<GrantAdminEnvelopeResponse>;

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "ROLE_BINDING_INVALID",
  publicDetail: "The role binding request is invalid.",
})
model RoleBindingInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "ROLE_BINDING_NOT_FOUND",
  publicDetail: "The authorization policy was not found.",
})
model RoleBindingNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "conflict",
  statusCode: 409,
  code: "ROLE_BINDING_CONFLICT",
  publicDetail: "The authorization policy conflicts with current state.",
})
model RoleBindingConflictFailure {}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "RESOURCE_SHARE_GRANT_INVALID",
  publicDetail: "The durable resource-share request is invalid.",
})
model ResourceShareGrantInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "RESOURCE_SHARE_GRANT_NOT_FOUND",
  publicDetail: "The durable resource-share grant was not found.",
})
model ResourceShareGrantNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "conflict",
  statusCode: 409,
  code: "RESOURCE_SHARE_GRANT_CONFLICT",
  publicDetail: "The durable resource-share request conflicts with current state.",
})
model ResourceShareGrantConflictFailure {}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "GRANT_ADMIN_ENVELOPE_INVALID",
  publicDetail: "The grant-administration envelope request is invalid.",
})
model GrantAdminEnvelopeInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "GRANT_ADMIN_ENVELOPE_NOT_FOUND",
  publicDetail: "The grant-administration envelope was not found.",
})
model GrantAdminEnvelopeNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "conflict",
  statusCode: 409,
  code: "GRANT_ADMIN_ENVELOPE_CONFLICT",
  publicDetail: "The grant-administration envelope request conflicts with current state.",
})
model GrantAdminEnvelopeConflictFailure {}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model RoleBindingAuditPayload {
  @apigen.auditInternal
  targetId: string;
  @apigen.auditInternal
  projectId: ResourceId;
  @apigen.auditInternal
  environment: string;
  @apigen.auditInternal
  bindingId: string;
  @apigen.auditPublic
  subjectType: string;
  @apigen.auditPii
  subjectId: string;
  @apigen.auditPublic
  role: string;
  @apigen.auditInternal
  policyRevision: int64;
  @apigen.auditInternal
  policyDigest: string;
}

model EffectiveCapabilityListResponse {
  resourceId: ResourceId;
  resourceKind: ResourceKind;
  capabilities: Capability[];
  effectiveGrants: AuthorizationDecisionResponse[];
}

model AuthorizationDecisionResponse {

  allowed?: boolean;
  capability: Capability;
  reason: string;
  resourceKind: ResourceKind;
  resourceId?: ResourceId;
  grantId?: string;
  grantResourceId?: ResourceId;
  subjectType?: string;
  subjectId?: string;
  inherited: boolean;
  owner: boolean;
  platform: boolean;
}

model AuthorizationCheckRequest {
  capability: Capability;
  resourceKind: ResourceKind;
  resourceId: ResourceId;
}

model AuthorizationBatchCheckRequest {
  checks: AuthorizationCheckRequest[];
}

model AuthorizationBatchCheckResponse {
  decisions: AuthorizationDecisionResponse[];
}

model DeviceAuthorizationApprovalRequest {
  userCode: string;
  approved: boolean;
}

model AuthoringSessionResponse {
  id: string;
  kind: string;
  current: boolean;
  clientId: string;
  targetId: string;
  projectId: ResourceId;
  capabilities: Capability[];
  createdAt: utcDateTime;
  lastUsedAt?: utcDateTime;
  expiresAt: utcDateTime;
  revokedAt?: utcDateTime;
}

model AuthoringSessionListResponse {
  items: AuthoringSessionResponse[];
  page: PageInfo;
}

@tag("Access")
@summary("Approve or deny CLI device authorization")
@route("/access/device-authorizations/approval")
@post
@apigen.authz(#{ mode: "authenticated" })
@apigen.command(#{ auditAction: "authoring.device.decided", guarantee: "transactional" })
@apigen.failsWith(DeviceAuthorizationDeniedFailure)
@apigen.failsWith(DeviceAuthorizationExpiredFailure)
@apigen.failsWith(InvalidPrincipalFailure)
@apigen.failsWith(DeviceAuthorizationNotFoundFailure)
@apigen.auditPayload(DeviceAuthorizationDecidedAuditPayload)
op decideDeviceAuthorization(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeviceAuthorizationApprovalRequest): OkJson<StatusResponse> | CommonErrors;

@tag("Current User")
@route("/me/authoring-sessions")
@apigen.authz(#{ mode: "authenticated" })
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface CurrentAuthoringSessions {
  @summary("List current CLI and workload sessions")
  @get
  listCurrentAuthoringSessions(@query limit?: ListLimit, @query pageToken?: PageToken): OkJson<AuthoringSessionListResponse> | CommonErrors;

  @summary("Revoke a current CLI or workload session")
  @route("/{session}")
  @delete
  @apigen.command(#{ auditAction: "authoring.session.revoked" })
  @apigen.failsWith(InvalidAuthoringCredentialFailure)
  @apigen.failsWith(AuthoringSessionNotFoundFailure)
  @apigen.auditPayload(AuthoringSessionRevokedAuditPayload)
  @apigen.ui("current-user.authoring-session.revoke")
  revokeCurrentAuthoringSession(@path @apigen.target session: string): OkJson<StatusResponse> | CommonErrors;
}

alias ListPrincipalsOK = OkJson<PrincipalListResponse>;
alias CreatePrincipalCreated = CreatedJson<LocalPasswordResetResponse>;
alias GetPrincipalOK = OkJson<PrincipalAdministrationResponse>;
alias UpdatePrincipalOK = OkJson<PrincipalAdministrationResponse>;
alias ResetPrincipalPasswordOK = OkJson<LocalPasswordResetResponse>;
alias ListPrincipalSessionsOK = OkJson<SessionListResponse>;

@tag("Access")
@route("/principals")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface Principals {
  @summary("List principals")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listPrincipals(@query email?: string, @query q?: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListPrincipalsOK | CommonErrors;

  @summary("Create a local principal")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "principal.local_user.created" })
  @apigen.failsWith(PrincipalAlreadyExistsFailure)
  @apigen.auditPayload(PrincipalCreatedAuditPayload)
  @apigen.ui("principal.create")
  createPrincipal(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: PrincipalCreateRequest): CreatePrincipalCreated | CommonErrors;

  @summary("Get a principal")
  @route("/{principal}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  getPrincipal(@path principal: string): GetPrincipalOK | CommonErrors;

  @summary("Update a principal")
  @route("/{principal}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{
    auditAction: "principal.updated",
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_NOT_MUTABLE", publicDetail: "This principal cannot be modified by this operation." },
    ],
  })
  @apigen.failsWith(PrincipalNotFoundFailure)
  @apigen.auditPayload(PrincipalUpdatedAuditPayload)
  @apigen.ui("principal.update")
  updatePrincipal(@path principal: string, @header("If-Match") ifMatch: string, @body body: PrincipalPatchRequest): UpdatePrincipalOK | CommonErrors;

  @summary("Delete a principal")
  @route("/{principal}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{
    auditAction: "principal.deleted",
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_NOT_MUTABLE", publicDetail: "This principal cannot be deleted by this operation." },
      #{ kind: "conflict", statusCode: 409, code: "PRINCIPAL_OWNS_OBJECTS", publicDetail: "Transfer owned objects before deleting this principal." },
    ],
  })
  @apigen.failsWith(PrincipalNotFoundFailure)
  @apigen.auditPayload(PrincipalDeletedAuditPayload)
  @apigen.ui("principal.delete")
  deletePrincipal(@path principal: string): EmptyResponse | CommonErrors;

  @summary("Reset a local principal password")
  @route("/{principal}/password-reset")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "principal.local_password.reset" })
  @apigen.failsWith(PrincipalNotFoundFailure)
  @apigen.auditPayload(PrincipalPasswordResetAuditPayload)
  @apigen.ui("principal.password.reset")
  resetPrincipalPassword(@path principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): ResetPrincipalPasswordOK | CommonErrors;

  @summary("Block a principal in LeapView and reject its credentials")
  @doc("Sets an independent LeapView administrator block. External provisioning cannot clear this block.")
  @route("/{principal}/disable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{
    auditAction: "principal.blocked",
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_NOT_MUTABLE", publicDetail: "This principal cannot be disabled by this operation." },
    ],
  })
  @apigen.failsWith(PrincipalNotFoundFailure)
  @apigen.auditPayload(PrincipalUpdatedAuditPayload)
  @apigen.ui("principal.block")
  disablePrincipal(@path @apigen.target principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<PrincipalAdministrationResponse> | CommonErrors;

  @summary("Remove a principal's LeapView administrator block")
  @doc("Clears only the LeapView administrator block. An externally disabled principal remains disabled.")
  @route("/{principal}/enable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{
    auditAction: "principal.unblocked",
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_NOT_MUTABLE", publicDetail: "This principal cannot be enabled by this operation." },
    ],
  })
  @apigen.failsWith(PrincipalNotFoundFailure)
  @apigen.auditPayload(PrincipalUpdatedAuditPayload)
  @apigen.ui("principal.unblock")
  enablePrincipal(@path @apigen.target principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<PrincipalAdministrationResponse> | CommonErrors;

  @summary("List a principal's sessions")
  @route("/{principal}/sessions")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listPrincipalSessions(@path principal: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListPrincipalSessionsOK | CommonErrors;

  @summary("Revoke a principal's session")
  @route("/{principal}/sessions/{session}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "session.revoked" })
  @apigen.failsWith(SessionNotFoundFailure)
  @apigen.auditPayload(PrincipalSessionRevokedAuditPayload)
  @apigen.ui("principal.session.revoke")
  revokePrincipalSession(@path @apigen.target principal: string, @path session: string): EmptyResponse | CommonErrors;
}

alias ListServicePrincipalsOK = OkJson<ServicePrincipalListResponse>;
alias CreateServicePrincipalCreated = CreatedJson<PrincipalResponse>;
alias UpdateServicePrincipalOK = OkJson<PrincipalResponse>;
alias CreateServicePrincipalSecretCreated = CreatedJson<ServicePrincipalSecretCreatedResponse>;

@tag("Access")
@route("/service-principals")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface ServicePrincipals {
  @summary("List service principals")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listServicePrincipals(@query limit?: ListLimit, @query pageToken?: PageToken): ListServicePrincipalsOK | CommonErrors;

  @summary("Create a service principal")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "service_principal.created" })
  @apigen.failsWith(ServicePrincipalInvalidFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.create")
  createServicePrincipal(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalCreateRequest): CreateServicePrincipalCreated | CommonErrors;

  @summary("Get a service principal")
  @route("/{servicePrincipal}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  getServicePrincipal(@path servicePrincipal: string): OkJson<PrincipalResponse> | CommonErrors;

  @summary("Update a service principal")
  @route("/{servicePrincipal}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{
    auditAction: "service_principal.updated",
    failures: #[
      #{ kind: "conflict", statusCode: 409, code: "SERVICE_PRINCIPAL_OWNS_OBJECTS", publicDetail: "Transfer owned objects before deleting this service principal." },
    ],
  })
  @apigen.failsWith(ServicePrincipalNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  updateServicePrincipal(@path servicePrincipal: string, @header("If-Match") ifMatch: string, @body body: ServicePrincipalPatchRequest): UpdateServicePrincipalOK | CommonErrors;

  @summary("Delete a service principal")
  @route("/{servicePrincipal}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "service_principal.deleted" })
  @apigen.failsWith(ServicePrincipalNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.delete")
  deleteServicePrincipal(@path @apigen.target servicePrincipal: string): EmptyResponse | CommonErrors;

  @summary("Create a service principal secret")
  @route("/{servicePrincipal}/secrets")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "service_principal_secret.created" })
  @apigen.failsWith(ServicePrincipalSecretInvalidFailure)
  @apigen.auditPayload(ServicePrincipalSecretAuditPayload)
  @apigen.ui("service-account.secret.create")
  createServicePrincipalSecret(@path @apigen.target servicePrincipal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalSecretCreateRequest): CreateServicePrincipalSecretCreated | CommonErrors;

  @summary("List service-principal secrets")
  @route("/{servicePrincipal}/secrets")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listServicePrincipalSecrets(@path servicePrincipal: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ServicePrincipalSecretListResponse> | CommonErrors;

  @summary("Get service-principal secret metadata")
  @route("/{servicePrincipal}/secrets/{secret}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  getServicePrincipalSecret(@path servicePrincipal: string, @path secret: string): OkJson<ServicePrincipalSecretResponse> | CommonErrors;

  @summary("Revoke a service principal secret")
  @route("/{servicePrincipal}/secrets/{secret}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "service_principal_secret.revoked" })
  @apigen.failsWith(ServicePrincipalSecretNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalSecretAuditPayload)
  @apigen.ui("service-account.secret.revoke")
  revokeServicePrincipalSecret(@path @apigen.target servicePrincipal: string, @path secret: string): EmptyResponse | CommonErrors;
}

alias ListGroupsOK = OkJson<GroupListResponse>;
alias CreateGroupCreated = CreatedJson<GroupResponse>;
alias GetGroupOK = OkJson<GroupResponse>;
alias UpdateGroupOK = OkJson<GroupResponse>;
alias ListGroupMembersOK = OkJson<GroupMemberListResponse>;
alias AddGroupMemberOK = OkJson<StatusResponse>;

@tag("Access")
@route("/groups")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface Groups {
  @summary("List groups")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listGroups(@query limit?: ListLimit, @query pageToken?: PageToken): ListGroupsOK | CommonErrors;

  @summary("Create a group")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "group.created" })
  @apigen.failsWith(GroupInvalidFailure)
  @apigen.auditPayload(GroupAuditPayload)
  @apigen.ui("group.create")
  createGroup(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: GroupCreateRequest): CreateGroupCreated | CommonErrors;

  @summary("Delete a group")
  @route("/{group}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "group.deleted" })
  @apigen.failsWith(GroupNotFoundFailure)
  @apigen.failsWith(GroupNotMutableFailure)
  @apigen.auditPayload(GroupAuditPayload)
  @apigen.ui("group.delete")
  deleteGroup(@path @apigen.target group: string): EmptyResponse | CommonErrors;

  @summary("Get a group")
  @route("/{group}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  getGroup(@path group: string): GetGroupOK | CommonErrors;

  @summary("Update a group")
  @route("/{group}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "group.updated" })
  @apigen.failsWith(GroupNotFoundFailure)
  @apigen.failsWith(GroupNotMutableFailure)
  @apigen.auditPayload(GroupAuditPayload)
  @apigen.ui("group.update")
  updateGroup(@path @apigen.target group: string, @header("If-Match") ifMatch: string, @body body: GroupPatchRequest): UpdateGroupOK | CommonErrors;

  @summary("List group members")
  @route("/{group}/members")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listGroupMembers(@path group: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListGroupMembersOK | CommonErrors;

  @summary("Remove a group member")
  @route("/{group}/members/{principal}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{
    auditAction: "group.member_removed",
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "GROUP_MEMBER_NOT_FOUND", publicDetail: "Group member not found." },
    ],
  })
  @apigen.auditPayload(GroupMemberAuditPayload)
  @apigen.failsWith(GroupNotMutableFailure)
  @apigen.ui("group.member.remove")
  removeGroupMember(@path @apigen.target group: string, @path principal: string): EmptyResponse | CommonErrors;

  @summary("Add a group member")
  @route("/{group}/members/{principal}")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{
    auditAction: "group.member_added",
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "GROUP_MEMBER_NOT_FOUND", publicDetail: "Group or principal not found." },
    ],
  })
  @apigen.auditPayload(GroupMemberAuditPayload)
  @apigen.failsWith(GroupNotMutableFailure)
  @apigen.ui("group.member.add")
  addGroupMember(@path @apigen.target group: string, @path principal: string): AddGroupMemberOK | CommonErrors;
}

alias ListProjectRolesOK = OkJson<RoleListResponse>;

@tag("Access")
@summary("List project roles")
@route("/projects/{project}/roles")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN", action: "project.access.read", resolver: "project" })
@extension("x-leapview-object-scope", "project")
op listProjectRoles(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): ListProjectRolesOK | CommonErrors;

alias ListProjectRoleBindingsOK = OkJson<RoleBindingListResponse>;
alias DeleteProjectRoleBindingOK = OkJson<RoleBindingMutationResponse>;

model CreateProjectRoleBindingCreated {
  ...Response<201>;
  ...Body<RoleBindingResponse>;
}

@tag("Access")
@summary("List target-owned project role bindings")
@route("/projects/{project}/role-bindings")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN", action: "project.access.read", resolver: "project" })
@extension("x-leapview-object-scope", "project")
op listProjectRoleBindings(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): ListProjectRoleBindingsOK | CommonErrors;

@tag("Access")
@summary("Create a target-owned project role binding")
@route("/projects/{project}/role-bindings")
@post
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN", action: "project.access.manage", resolver: "project" })
@extension("x-leapview-object-scope", "project")
@apigen.command(#{ auditAction: "role_binding.created", guarantee: "transactional" })
@apigen.failsWith(RoleBindingInvalidFailure)
@apigen.failsWith(RoleBindingConflictFailure)
@apigen.failsWith(RoleBindingNotFoundFailure)
@apigen.auditPayload(RoleBindingAuditPayload)
op createProjectRoleBinding(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: RoleBindingCreateRequest): CreateProjectRoleBindingCreated | CommonErrors;

@tag("Access")
@summary("Remove a target-owned project role binding")
@route("/projects/{project}/role-bindings/{binding}")
@delete
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN", action: "project.access.manage", resolver: "project" })
@extension("x-leapview-object-scope", "project")
@apigen.command(#{ auditAction: "role_binding.removed", guarantee: "transactional" })
@apigen.failsWith(RoleBindingInvalidFailure)
@apigen.failsWith(RoleBindingConflictFailure)
@apigen.failsWith(RoleBindingNotFoundFailure)
@apigen.auditPayload(RoleBindingAuditPayload)
op deleteProjectRoleBinding(@path @apigen.target project: ResourceId, @path binding: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: RoleBindingDeleteRequest): DeleteProjectRoleBindingOK | CommonErrors;

alias ListEffectiveCapabilitiesOK = OkJson<EffectiveCapabilityListResponse>;

@tag("Access")
@summary("List effective capabilities")
@route("/projects/{project}/effective-capabilities")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN", action: "project.access.read", resolver: "project" })
@extension("x-leapview-object-scope", "project")
op listEffectiveCapabilities(@path project: ResourceId, @query resourceKind?: ResourceKind, @query resourceId?: ResourceId): ListEffectiveCapabilitiesOK | CommonErrors;

@tag("Access")
@summary("Issue an exact durable resource-share grant")
@route("/projects/{project}/resource-share-grants")
@post
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_SHARE", action: "resource.share", resolver: "resource-share" })
@extension("x-leapview-object-scope", "resource-share")
@apigen.command(#{ auditAction: "resource_share_grant.issued", guarantee: "transactional" })
@apigen.failsWith(ResourceShareGrantInvalidFailure)
@apigen.failsWith(ResourceShareGrantConflictFailure)
@apigen.auditPayload(ResourceShareGrantAuditPayload)
op issueResourceShareGrant(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ResourceShareGrantIssueRequest): ResourceShareGrantCreated | CommonErrors;

@tag("Access")
@summary("Revoke a durable resource-share grant")
@route("/resource-share-grants/{grant}")
@delete
@apigen.authz(#{ mode: "privilege", privilege: "RESOURCE_SHARE", action: "resource.share", resolver: "resource-share" })
@extension("x-leapview-object-scope", "resource-share")
@apigen.command(#{ auditAction: "resource_share_grant.revoked", guarantee: "transactional" })
@apigen.failsWith(ResourceShareGrantInvalidFailure)
@apigen.failsWith(ResourceShareGrantNotFoundFailure)
@apigen.auditPayload(ResourceShareGrantAuditPayload)
op revokeResourceShareGrant(@path @apigen.target grant: string, @body body: ResourceShareGrantRevokeRequest): ResourceShareGrantRevoked | CommonErrors;

@tag("Access")
@summary("Issue a project-bound grant-administration envelope")
@route("/projects/{project}/grant-admin-envelopes")
@post
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN", action: "project.access.manage", resolver: "project" })
@extension("x-leapview-object-scope", "project")
@apigen.command(#{ auditAction: "grant_admin_envelope.issued", guarantee: "transactional" })
@apigen.failsWith(GrantAdminEnvelopeInvalidFailure)
@apigen.failsWith(GrantAdminEnvelopeConflictFailure)
@apigen.auditPayload(GrantAdminEnvelopeAuditPayload)
op issueGrantAdminEnvelope(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: GrantAdminEnvelopeIssueRequest): GrantAdminEnvelopeCreated | CommonErrors;

@tag("Access")
@summary("Revoke a project-bound grant-administration envelope")
@route("/projects/{project}/grant-admin-envelopes/{envelope}")
@delete
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN", action: "project.access.manage", resolver: "project" })
@extension("x-leapview-object-scope", "project")
@apigen.command(#{ auditAction: "grant_admin_envelope.revoked", guarantee: "transactional" })
@apigen.failsWith(GrantAdminEnvelopeInvalidFailure)
@apigen.failsWith(GrantAdminEnvelopeNotFoundFailure)
@apigen.auditPayload(GrantAdminEnvelopeAuditPayload)
op revokeGrantAdminEnvelope(@path @apigen.target project: ResourceId, @path envelope: string, @body body: GrantAdminEnvelopeRevokeRequest): GrantAdminEnvelopeRevoked | CommonErrors;

@tag("Access")
@summary("Check multiple authorization decisions")
@route("/projects/{project}/authorization-checks")
@post
@apigen.authz(#{ mode: "authenticated" })
@apigen.query
op checkAuthorizationBatch(@path project: ResourceId, @body body: AuthorizationBatchCheckRequest): OkJson<AuthorizationBatchCheckResponse> | CommonErrors;

// Semantic attributes are a platform-owned control-plane resource. The
// request models intentionally use a closed scalar union: callers cannot
// smuggle arbitrary JSON objects, maps, or nested lists into an assignment.
enum SemanticAttributeValueType {
  stringValue: "String";
  booleanValue: "Boolean";
  integerValue: "Integer";
  decimalValue: "Decimal";
  dateValue: "Date";
  timestampValue: "Timestamp";
}

enum SemanticAttributeShapeValue {
  scalarShape: "scalar";
  listShape: "list";
}

enum SemanticAttributeOwnerKindValue {
  instanceOwner: "instance";
  principalOwner: "principal";
  groupOwner: "group";
}

enum SemanticAttributeAssignmentTargetKindValue {
  principalTarget: "principal";
  groupTarget: "group";
}

enum SemanticAttributeClaimSourceKindValue {
  saml;
  oidc;
  embed;
  serviceToken: "service_token";
}

model SemanticAttributeValue {
  type: SemanticAttributeValueType;
  stringValue?: string;
  booleanValue?: boolean;
  // Integers and decimals remain strings on the wire to preserve precision.
  integerValue?: string;
  decimalValue?: string;
  dateValue?: string;
  timestampValue?: string;
}

model SemanticAttributeMetadataRequest {
  ownerKind: SemanticAttributeOwnerKindValue;
  ownerId?: string;
  displayName: string;
  description: string;
  documentationUrl?: string;
}

model SemanticAttributeDefinitionResponse {
  id: string;
  name: string;
  type: SemanticAttributeValueType;
  shape: SemanticAttributeShapeValue;
  profile: string;
  definitionVersion: int64;
  ownerKind: SemanticAttributeOwnerKindValue;
  ownerId?: string;
  displayName: string;
  description: string;
  documentationUrl?: string;
  lifecycleState: string;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
  disabledAt?: utcDateTime;
}

model SemanticAttributeDefinitionListResponse {
  items: SemanticAttributeDefinitionResponse[];
  page: PageInfo;
}

model SemanticAttributeRegisterRequest {
  name: string;
  type: SemanticAttributeValueType;
  shape: SemanticAttributeShapeValue;
  metadata: SemanticAttributeMetadataRequest;
}

model SemanticAttributeAssignmentRequest {
  values: SemanticAttributeValue[];
}

model SemanticAttributeAssignmentResponse {
  id: string;
  attributeName: string;
  targetKind: SemanticAttributeAssignmentTargetKindValue;
  targetId: string;
  values: SemanticAttributeValue[];
  assignmentVersion: int64;
  lifecycleState: string;
  removedAt?: utcDateTime;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model SemanticAttributeAssignmentListResponse {
  items: SemanticAttributeAssignmentResponse[];
  page: PageInfo;
}

model SemanticAttributeClaimMappingRequest {
  sourceKind: SemanticAttributeClaimSourceKindValue;
  provider: string;
  issuer: string;
  audience: string;
  claim: string;
}

model SemanticAttributeClaimMappingResponse {
  id: string;
  attributeName: string;
  sourceKind: SemanticAttributeClaimSourceKindValue;
  provider: string;
  issuer: string;
  audience: string;
  claim: string;
  mappingVersion: int64;
  lifecycleState: string;
  removedAt?: utcDateTime;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model SemanticAttributeClaimMappingListResponse {
  items: SemanticAttributeClaimMappingResponse[];
  page: PageInfo;
}

model SemanticAttributeImpactPreviewRequest {
  targetKind: SemanticAttributeAssignmentTargetKindValue;
  targetId: string;
  // An empty values array previews removal of the active assignment. A
  // non-empty array previews adding or changing the target's assignment.
  values: SemanticAttributeValue[];
}

model SemanticAttributeImpactPreviewResponse {
  attributeName: string;
  targetKind: SemanticAttributeAssignmentTargetKindValue;
  targetId: string;
  affectedPrincipalCount: int32;
  affectedGroupCount: int32;
  warnings: string[];
}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "SEMANTIC_ATTRIBUTE_NOT_FOUND", publicDetail: "Semantic attribute was not found." })
model SemanticAttributeNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "invalid", statusCode: 400, code: "SEMANTIC_ATTRIBUTE_INVALID", publicDetail: "Semantic attribute request is invalid." })
model SemanticAttributeInvalidFailure {}

@apigen.failureDefinition(#{ kind: "conflict", statusCode: 409, code: "SEMANTIC_ATTRIBUTE_CONFLICT", publicDetail: "Semantic attribute conflicts with current state." })
model SemanticAttributeConflictFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "SEMANTIC_ATTRIBUTE_ASSIGNMENT_NOT_FOUND", publicDetail: "Semantic attribute assignment was not found." })
model SemanticAttributeAssignmentNotFoundFailure {}

@apigen.failureDefinition(#{ kind: "not_found", statusCode: 404, code: "SEMANTIC_ATTRIBUTE_CLAIM_MAPPING_NOT_FOUND", publicDetail: "Semantic attribute claim mapping was not found." })
model SemanticAttributeClaimMappingNotFoundFailure {}

// These payloads mirror the repository's durable audit metadata projections.
// Assignment values and value digests are intentionally absent.
@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model SemanticAttributeDefinitionAuditPayload {
  @apigen.auditInternal
  profile: string;
  @apigen.auditInternal
  type: string;
  @apigen.auditInternal
  shape: string;
  @apigen.auditInternal
  definitionVersion: int64;
  @apigen.auditInternal
  registryRevision: int64;
  @apigen.auditInternal
  registryDigest: string;
  @apigen.auditInternal
  ownerKind: string;
  @apigen.auditPii
  ownerId: string;
  @apigen.auditPublic
  lifecycleState: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model SemanticAttributeAssignmentAuditPayload {
  @apigen.auditInternal
  definitionId: string;
  @apigen.auditInternal
  definitionName: string;
  @apigen.auditInternal
  subjectKind: string;
  @apigen.auditPii
  subjectId: string;
  @apigen.auditInternal
  definitionVersion: int64;
  @apigen.auditInternal
  assignmentVersion: int64;
  @apigen.auditInternal
  controlRevision: int64;
  @apigen.auditInternal
  valueCount: int32;
  @apigen.auditInternal
  tombstoned: boolean;
  @apigen.auditInternal
  controlDigest: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model SemanticAttributeClaimMappingAuditPayload {
  @apigen.auditPublic
  sourceKind: string;
  @apigen.auditPublic
  provider: string;
  @apigen.auditInternal
  issuer: string;
  @apigen.auditInternal
  audience: string;
  @apigen.auditPublic
  claim: string;
  @apigen.auditInternal
  definitionId: string;
  @apigen.auditInternal
  definitionName: string;
  @apigen.auditInternal
  definitionVersion: int64;
  @apigen.auditInternal
  mappingVersion: int64;
  @apigen.auditInternal
  controlRevision: int64;
  @apigen.auditInternal
  tombstoned: boolean;
  @apigen.auditInternal
  controlDigest: string;
}

alias ListSemanticAttributeDefinitionsOK = OkJson<SemanticAttributeDefinitionListResponse>;
alias RegisterSemanticAttributeCreated = CreatedJson<SemanticAttributeDefinitionResponse>;
alias GetSemanticAttributeDefinitionOK = OkJson<SemanticAttributeDefinitionResponse>;
alias UpdateSemanticAttributeMetadataOK = OkJson<SemanticAttributeDefinitionResponse>;
alias SemanticAttributeAssignmentOK = OkJson<SemanticAttributeAssignmentResponse>;
alias SemanticAttributeClaimMappingOK = OkJson<SemanticAttributeClaimMappingResponse>;
alias SemanticAttributeImpactPreviewOK = OkJson<SemanticAttributeImpactPreviewResponse>;

@tag("Access")
@summary("List semantic attribute definitions")
@route("/semantic-attributes")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface SemanticAttributeDefinitions {
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listSemanticAttributeDefinitions(@query q?: string, @query ownerKind?: SemanticAttributeOwnerKindValue, @query limit?: ListLimit, @query pageToken?: PageToken): ListSemanticAttributeDefinitionsOK | CommonErrors;

  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  // The repository's durable audit event is semantic_attribute.register.
  // Replays use a distinct durable replay action but retain this canonical
  // operation action in the generated contract.
  @apigen.command(#{ auditAction: "semantic_attribute.register" })
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeConflictFailure)
  @apigen.auditPayload(SemanticAttributeDefinitionAuditPayload)
  registerSemanticAttribute(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: SemanticAttributeRegisterRequest): RegisterSemanticAttributeCreated | CommonErrors;

  @route("/{attribute}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  getSemanticAttributeDefinition(@path attribute: string): GetSemanticAttributeDefinitionOK | CommonErrors;

  @route("/{attribute}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.metadata_update" })
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.auditPayload(SemanticAttributeDefinitionAuditPayload)
  updateSemanticAttributeMetadata(@path attribute: string, @header("If-Match") ifMatch: string, @body body: SemanticAttributeMetadataRequest): UpdateSemanticAttributeMetadataOK | CommonErrors;

  @route("/{attribute}/disable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.disable" })
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeDefinitionAuditPayload)
  disableSemanticAttribute(@path @apigen.target attribute: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string): GetSemanticAttributeDefinitionOK | CommonErrors;

  @route("/{attribute}/restore")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.enable" })
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeDefinitionAuditPayload)
  restoreSemanticAttribute(@path @apigen.target attribute: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string): GetSemanticAttributeDefinitionOK | CommonErrors;
}

@tag("Access")
@summary("List and mutate principal semantic attribute assignments")
@route("/principals/{principal}/semantic-attributes")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface PrincipalSemanticAttributeAssignments {
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listPrincipalSemanticAttributeAssignments(@path principal: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticAttributeAssignmentListResponse> | CommonErrors;

  @route("/{attribute}")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.assignment.set" })
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeAssignmentAuditPayload)
  upsertPrincipalSemanticAttributeAssignment(@path @apigen.target principal: string, @path attribute: string, @header("If-Match") ifMatch: string, @body body: SemanticAttributeAssignmentRequest): SemanticAttributeAssignmentOK | CommonErrors;

  @route("/{attribute}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.assignment.tombstone" })
  @apigen.failsWith(SemanticAttributeAssignmentNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeAssignmentAuditPayload)
  removePrincipalSemanticAttributeAssignment(@path @apigen.target principal: string, @path attribute: string, @header("If-Match") ifMatch: string): EmptyResponse | CommonErrors;
}

@tag("Access")
@summary("List and mutate group semantic attribute assignments")
@route("/groups/{group}/semantic-attributes")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface GroupSemanticAttributeAssignments {
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listGroupSemanticAttributeAssignments(@path group: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticAttributeAssignmentListResponse> | CommonErrors;

  @route("/{attribute}")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.assignment.set" })
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeAssignmentAuditPayload)
  upsertGroupSemanticAttributeAssignment(@path @apigen.target group: string, @path attribute: string, @header("If-Match") ifMatch: string, @body body: SemanticAttributeAssignmentRequest): SemanticAttributeAssignmentOK | CommonErrors;

  @route("/{attribute}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.assignment.tombstone" })
  @apigen.failsWith(SemanticAttributeAssignmentNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeAssignmentAuditPayload)
  removeGroupSemanticAttributeAssignment(@path @apigen.target group: string, @path attribute: string, @header("If-Match") ifMatch: string): EmptyResponse | CommonErrors;
}

@tag("Access")
@summary("List and mutate trusted semantic attribute claim mappings")
@route("/semantic-attributes/{attribute}/claim-mappings")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface SemanticAttributeClaimMappings {
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  listSemanticAttributeClaimMappings(@path attribute: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticAttributeClaimMappingListResponse> | CommonErrors;

  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.claim_mapping.set" })
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeClaimMappingAuditPayload)
  upsertSemanticAttributeClaimMapping(@path @apigen.target attribute: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string, @body body: SemanticAttributeClaimMappingRequest): SemanticAttributeClaimMappingOK | CommonErrors;

  @route("/{mapping}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.manage", resolver: "instance" })
  @extension("x-leapview-object-scope", "instance")
  @apigen.command(#{ auditAction: "semantic_attribute.claim_mapping.tombstone" })
  @apigen.failsWith(SemanticAttributeClaimMappingNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeClaimMappingAuditPayload)
  removeSemanticAttributeClaimMapping(@path @apigen.target attribute: string, @path mapping: string, @header("If-Match") ifMatch: string): EmptyResponse | CommonErrors;
}

@tag("Access")
@summary("Preview semantic attribute assignment impact")
@route("/semantic-attributes/{attribute}/impact-preview")
@post
@apigen.query
@apigen.authz(#{ mode: "privilege", privilege: "PLATFORM_ADMIN", action: "platform.access.read", resolver: "instance" })
@extension("x-leapview-object-scope", "instance")
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
op previewSemanticAttributeImpact(@path @apigen.target attribute: string, @body body: SemanticAttributeImpactPreviewRequest): SemanticAttributeImpactPreviewOK | CommonErrors;
