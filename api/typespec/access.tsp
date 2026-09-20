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

model OwnedObject {
  kind: string;
  id: string;
  name?: string;
  ownerPrincipalId: string;
  lifecycle: string;
  transferable: boolean;
  tombstoneOnOffboard: boolean;
}

model OwnershipResolutionRequest {
  @doc("Use transfer to assign live objects to targetPrincipalId, or tombstone to retire them while retaining evidence.")
  action: string;
  targetPrincipalId: string;
}

model OwnershipResolutionResponse {
  principalId: string;
  action: string;
  objects: OwnedObject[];
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model OwnershipResolutionAuditPayload {
  @apigen.auditPii
  principalId: string;
  @apigen.auditPublic
  action: string;
  @apigen.auditPii
  targetPrincipalId: string;
  @apigen.auditInternal
  objectCount: int32;
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

model ServicePrincipalSecretRotateRequest {
  name: string;
  expiresAt?: utcDateTime;
  revokePrevious?: boolean;
}

model ServicePrincipalCredentialsRevokeAllRequest {
  reason?: string;
}

model ServicePrincipalSecretResponse {
  id: string;
  servicePrincipalId: string;
  name: string;
  secret?: string;
  expiresAt?: utcDateTime;
  createdAt?: utcDateTime;
  lastUsedAt?: utcDateTime;
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
  @doc("The immutable capability bundle derived from role by the target authority.")
  capabilities: Capability[];
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
  role: string;
  @doc("Compare-and-swap fence. Use 0 when creating the first policy revision.")
  expectedRevision: int64;
}

model RoleBindingDeleteRequest {
  @doc("Compare-and-swap fence. Use the current target-owned policy revision.")
  expectedRevision: int64;
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
  capabilities: GroupAdministrationCapabilities;
  createdAt: utcDateTime;
}

model GroupAdministrationCapabilities {
  canUpdate: boolean;
  canDelete: boolean;
  canManageMembers: boolean;
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
  kind: "conflict",
  statusCode: 409,
  code: "PRINCIPAL_CREDENTIAL_REVOCATION_CONFLICT",
  publicDetail: "The principal's credentials cannot be revoked in the current administrator state.",
})
model PrincipalCredentialRevocationConflictFailure {}

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
model PrincipalCredentialsRevokedAuditPayload {
  @apigen.auditPii
  actorPrincipalId: string;
  @apigen.auditPii
  targetPrincipalId: string;
  @apigen.auditInternal
  credentialClasses: string[];
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

model PlatformAdministratorResponse {
  bindingId: string;
  principalId: string;
  email: string;
  displayName: string;
  role: string;
  grantedAt: utcDateTime;
}

model PlatformAdministratorListResponse {
  items: PlatformAdministratorResponse[];
  page: PageInfo;
}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "PLATFORM_ADMIN_INVALID",
  publicDetail: "The platform administrator request is invalid.",
})
model PlatformAdministratorInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "PLATFORM_ADMIN_NOT_FOUND",
  publicDetail: "The platform administrator was not found.",
})
model PlatformAdministratorNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "conflict",
  statusCode: 409,
  code: "PLATFORM_ADMIN_CONFLICT",
  publicDetail: "The platform administrator change conflicts with current state.",
})
model PlatformAdministratorConflictFailure {}

@apigen.failureDefinition(#{
  kind: "unauthorized",
  statusCode: 401,
  code: "RECENT_AUTHENTICATION_REQUIRED",
  publicDetail: "Recent interactive authentication is required before changing platform authority.",
})
model RecentAuthenticationRequiredFailure {}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model PlatformAdministratorAuditPayload {
  @apigen.auditPii
  principalId: string;
  @apigen.auditInternal
  bindingId: string;
  @apigen.auditPublic
  role: string;
  @apigen.auditInternal
  revision: string;
}

model PlatformRoleApprovalResponse {
  id: string;
  action: string;
  principalId: string;
  requesterId: string;
  approverId?: string;
  canceledBy?: string;
  expiredBy?: string;
  status: string;
  expectedRevision: string;
  revision: int64;
  idempotencyKey: string;
  expiresAt: utcDateTime;
  createdAt: utcDateTime;
  approvedAt?: utcDateTime;
  canceledAt?: utcDateTime;
  expiredAt?: utcDateTime;
  executedAt?: utcDateTime;
  bindingId?: string;
  resultRevision?: string;
}

model PlatformRoleApprovalListResponse {
  items: PlatformRoleApprovalResponse[];
}

model PlatformRoleApprovalCreateRequest {
  action: string;
  principalId: string;
  expectedRevision: string;
}

model PlatformRoleApprovalDecisionRequest {
  expectedRevision: int64;
}

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "PLATFORM_ROLE_APPROVAL_INVALID",
  publicDetail: "The platform-role approval request is invalid.",
})
model PlatformRoleApprovalInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "PLATFORM_ROLE_APPROVAL_NOT_FOUND",
  publicDetail: "Platform-role approval request not found.",
})
model PlatformRoleApprovalNotFoundFailure {}

@apigen.failureDefinition(#{
  kind: "conflict",
  statusCode: 409,
  code: "PLATFORM_ROLE_APPROVAL_CONFLICT",
  publicDetail: "The platform-role approval conflicts with current state.",
})
model PlatformRoleApprovalConflictFailure {}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model PlatformRoleApprovalAuditPayload {
  @apigen.auditInternal
  approvalId: string;
  @apigen.auditPublic
  action: string;
  @apigen.auditPii
  principalId: string;
  @apigen.auditInternal
  status: string;
  @apigen.auditInternal
  revision: int64;
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listPrincipals(@query email?: string, @query q?: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListPrincipalsOK | CommonErrors;

  @summary("Create a local principal")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "principal.local_user.created" })
  @apigen.failsWith(PrincipalAlreadyExistsFailure)
  @apigen.auditPayload(PrincipalCreatedAuditPayload)
  @apigen.ui("principal.create")
  createPrincipal(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: PrincipalCreateRequest): CreatePrincipalCreated | CommonErrors;

  @summary("Get a principal")
  @route("/{principal}")
  @get
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  getPrincipal(@path principal: string): GetPrincipalOK | CommonErrors;

  @summary("Update a principal")
  @route("/{principal}")
  @patch
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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

  @summary("Transfer or tombstone a principal's owned product objects")
  @doc("Resolve live dashboard-authoring and agent-conversation ownership before deleting the principal. Tombstones retain domain evidence and are safe to retry.")
  @route("/{principal}/ownership")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{
    auditAction: "principal.ownership.resolved",
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "OWNERSHIP_RESOLUTION_INVALID", publicDetail: "The ownership resolution request is invalid." },
    ],
  })
  @apigen.failsWith(PrincipalNotFoundFailure)
  @apigen.auditPayload(OwnershipResolutionAuditPayload)
  @apigen.ui("principal.ownership.resolve")
  resolvePrincipalOwnership(@path @apigen.target principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: OwnershipResolutionRequest): OkJson<OwnershipResolutionResponse> | CommonErrors;

  @summary("Reset a local principal password")
  @route("/{principal}/password-reset")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "principal.local_password.reset" })
  @apigen.failsWith(PrincipalNotFoundFailure)
  @apigen.auditPayload(PrincipalPasswordResetAuditPayload)
  @apigen.ui("principal.password.reset")
  resetPrincipalPassword(@path principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): ResetPrincipalPasswordOK | CommonErrors;

  @summary("Block a principal in LeapView and reject its credentials")
  @doc("Sets an independent LeapView administrator block. External provisioning cannot clear this block.")
  @route("/{principal}/disable")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listPrincipalSessions(@path principal: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListPrincipalSessionsOK | CommonErrors;

  @summary("Revoke a principal's session")
  @route("/{principal}/sessions/{session}")
  @delete
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "session.revoked" })
  @apigen.failsWith(SessionNotFoundFailure)
  @apigen.auditPayload(PrincipalSessionRevokedAuditPayload)
  @apigen.ui("principal.session.revoke")
  revokePrincipalSession(@path @apigen.target principal: string, @path session: string): EmptyResponse | CommonErrors;

  @summary("Revoke every credential owned by a principal")
  @doc("Invalidates all active sessions, personal API tokens, service secrets, desktop authorization codes, approved CLI device authorizations, authoring sessions, and OAuth sessions without disabling the principal. Pending CLI device authorizations are unbound until approval and require blocking the principal to prevent later approval.")
  @route("/{principal}/credentials/revoke-all")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "principal.credentials.revoked_all" })
  @apigen.failsWith(PrincipalNotFoundFailure)
  @apigen.failsWith(PrincipalCredentialRevocationConflictFailure)
  @apigen.auditPayload(PrincipalCredentialsRevokedAuditPayload)
  @apigen.ui("principal.credentials.revoke-all")
  revokeAllPrincipalCredentials(@path @apigen.target principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): EmptyResponse | CommonErrors;
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listServicePrincipals(@query limit?: ListLimit, @query pageToken?: PageToken): ListServicePrincipalsOK | CommonErrors;

  @summary("Create a service principal")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal.created" })
  @apigen.failsWith(ServicePrincipalInvalidFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.create")
  createServicePrincipal(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalCreateRequest): CreateServicePrincipalCreated | CommonErrors;

  @summary("Get a service principal")
  @route("/{servicePrincipal}")
  @get
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  getServicePrincipal(@path servicePrincipal: string): OkJson<PrincipalResponse> | CommonErrors;

  @summary("Update a service principal")
  @route("/{servicePrincipal}")
  @patch
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal.updated" })
  @apigen.failsWith(ServicePrincipalNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  updateServicePrincipal(@path servicePrincipal: string, @header("If-Match") ifMatch: string, @body body: ServicePrincipalPatchRequest): UpdateServicePrincipalOK | CommonErrors;

  @summary("Delete a service principal")
  @route("/{servicePrincipal}")
  @delete
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{
    auditAction: "service_principal.deleted",
    failures: #[
      #{ kind: "conflict", statusCode: 409, code: "SERVICE_PRINCIPAL_OWNS_OBJECTS", publicDetail: "Transfer owned objects before deleting this service principal." },
    ],
  })
  @apigen.failsWith(ServicePrincipalNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.delete")
  deleteServicePrincipal(@path @apigen.target servicePrincipal: string): EmptyResponse | CommonErrors;

  @summary("Disable a service principal and revoke its credentials")
  @route("/{servicePrincipal}/disable")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal.disabled" })
  @apigen.failsWith(ServicePrincipalNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.disable")
  disableServicePrincipal(@path @apigen.target servicePrincipal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<PrincipalResponse> | CommonErrors;

  @summary("Enable a service principal")
  @route("/{servicePrincipal}/enable")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal.enabled" })
  @apigen.failsWith(ServicePrincipalNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.enable")
  enableServicePrincipal(@path @apigen.target servicePrincipal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<PrincipalResponse> | CommonErrors;

  @summary("Create a service principal secret")
  @route("/{servicePrincipal}/secrets")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal_secret.created" })
  @apigen.failsWith(ServicePrincipalSecretInvalidFailure)
  @apigen.auditPayload(ServicePrincipalSecretAuditPayload)
  @apigen.ui("service-account.secret.create")
  createServicePrincipalSecret(@path @apigen.target servicePrincipal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalSecretCreateRequest): CreateServicePrincipalSecretCreated | CommonErrors;

  @summary("List service-principal secrets")
  @route("/{servicePrincipal}/secrets")
  @get
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listServicePrincipalSecrets(@path servicePrincipal: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ServicePrincipalSecretListResponse> | CommonErrors;

  @summary("Get service-principal secret metadata")
  @route("/{servicePrincipal}/secrets/{secret}")
  @get
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  getServicePrincipalSecret(@path servicePrincipal: string, @path secret: string): OkJson<ServicePrincipalSecretResponse> | CommonErrors;

  @summary("Revoke a service principal secret")
  @route("/{servicePrincipal}/secrets/{secret}")
  @delete
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal_secret.revoked" })
  @apigen.failsWith(ServicePrincipalSecretNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalSecretAuditPayload)
  @apigen.ui("service-account.secret.revoke")
  revokeServicePrincipalSecret(@path @apigen.target servicePrincipal: string, @path secret: string): EmptyResponse | CommonErrors;

  @summary("Rotate a service principal secret")
  @route("/{servicePrincipal}/secrets/{secret}/rotate")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal_secret.rotated" })
  @apigen.failsWith(ServicePrincipalSecretInvalidFailure)
  @apigen.auditPayload(ServicePrincipalSecretAuditPayload)
  @apigen.ui("service-account.secret.rotate")
  rotateServicePrincipalSecret(@path @apigen.target servicePrincipal: string, @path secret: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalSecretRotateRequest): CreateServicePrincipalSecretCreated | CommonErrors;

  @summary("Revoke every service principal credential")
  @route("/{servicePrincipal}/credentials/revoke-all")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal_credentials.revoked_all" })
  @apigen.failsWith(ServicePrincipalNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.credentials.revoke-all")
  revokeAllServicePrincipalCredentials(@path @apigen.target servicePrincipal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalCredentialsRevokeAllRequest): EmptyResponse | CommonErrors;
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listGroups(@query limit?: ListLimit, @query pageToken?: PageToken): ListGroupsOK | CommonErrors;

  @summary("Create a group")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "group.created" })
  @apigen.failsWith(GroupInvalidFailure)
  @apigen.auditPayload(GroupAuditPayload)
  @apigen.ui("group.create")
  createGroup(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: GroupCreateRequest): CreateGroupCreated | CommonErrors;

  @summary("Delete a group")
  @route("/{group}")
  @delete
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "group.deleted" })
  @apigen.failsWith(GroupNotFoundFailure)
  @apigen.failsWith(GroupNotMutableFailure)
  @apigen.auditPayload(GroupAuditPayload)
  @apigen.ui("group.delete")
  deleteGroup(@path @apigen.target group: string): EmptyResponse | CommonErrors;

  @summary("Get a group")
  @route("/{group}")
  @get
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  getGroup(@path group: string): GetGroupOK | CommonErrors;

  @summary("Update a group")
  @route("/{group}")
  @patch
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "group.updated" })
  @apigen.failsWith(GroupNotFoundFailure)
  @apigen.failsWith(GroupNotMutableFailure)
  @apigen.auditPayload(GroupAuditPayload)
  @apigen.ui("group.update")
  updateGroup(@path @apigen.target group: string, @header("If-Match") ifMatch: string, @body body: GroupPatchRequest): UpdateGroupOK | CommonErrors;

  @summary("List group members")
  @route("/{group}/members")
  @get
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listGroupMembers(@path group: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListGroupMembersOK | CommonErrors;

  @summary("Remove a group member")
  @route("/{group}/members/{principal}")
  @delete
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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

alias ListPlatformAdministratorsOK = OkJson<PlatformAdministratorListResponse>;
alias GrantPlatformAdministratorOK = OkJson<PlatformAdministratorResponse>;

@tag("Access")
@summary("List active platform administrators")
@route("/platform-administrators")
@get
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
op listPlatformAdministrators(@query limit?: ListLimit, @query pageToken?: PageToken): ListPlatformAdministratorsOK | CommonErrors;

@tag("Access")
@summary("Grant the platform administrator role")
@route("/platform-administrators/{principal}")
@put
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
@apigen.command(#{ auditAction: "platform_admin.granted", guarantee: "transactional" })
@apigen.ui("platform-administrator.grant")
@apigen.failsWith(PlatformAdministratorInvalidFailure)
@apigen.failsWith(PlatformAdministratorConflictFailure)
@apigen.failsWith(PlatformAdministratorNotFoundFailure)
@apigen.failsWith(RecentAuthenticationRequiredFailure)
@apigen.auditPayload(PlatformAdministratorAuditPayload)
op grantPlatformAdministrator(@path @apigen.target principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string): GrantPlatformAdministratorOK | CommonErrors;

@tag("Access")
@summary("Revoke the platform administrator role")
@route("/platform-administrators/{principal}")
@delete
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
@apigen.command(#{ auditAction: "platform_admin.revoked", guarantee: "transactional" })
@apigen.ui("platform-administrator.revoke")
@apigen.failsWith(PlatformAdministratorInvalidFailure)
@apigen.failsWith(PlatformAdministratorConflictFailure)
@apigen.failsWith(PlatformAdministratorNotFoundFailure)
@apigen.failsWith(RecentAuthenticationRequiredFailure)
@apigen.auditPayload(PlatformAdministratorAuditPayload)
op revokePlatformAdministrator(@path @apigen.target principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string): EmptyResponse | CommonErrors;

alias ListPlatformRoleApprovalsOK = OkJson<PlatformRoleApprovalListResponse>;
alias PlatformRoleApprovalOK = OkJson<PlatformRoleApprovalResponse>;
alias RequestPlatformRoleApprovalCreated = CreatedJson<PlatformRoleApprovalResponse>;

@tag("Access")
@summary("List platform-role approval requests")
@route("/platform-role-approvals")
@get
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
op listPlatformRoleApprovals(): ListPlatformRoleApprovalsOK | CommonErrors;

@tag("Access")
@summary("Get a platform-role approval request")
@route("/platform-role-approvals/{approval}")
@get
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
op getPlatformRoleApproval(@path approval: string): PlatformRoleApprovalOK | CommonErrors;

@tag("Access")
@summary("Request a platform-role change for second-admin approval")
@route("/platform-role-approvals")
@post
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
@apigen.command(#{ auditAction: "platform_role_approval.requested", guarantee: "transactional" })
@apigen.failsWith(PlatformRoleApprovalInvalidFailure)
@apigen.failsWith(PlatformRoleApprovalConflictFailure)
@apigen.auditPayload(PlatformRoleApprovalAuditPayload)
op requestPlatformRoleApproval(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: PlatformRoleApprovalCreateRequest): RequestPlatformRoleApprovalCreated | CommonErrors;

@tag("Access")
@summary("Approve a platform-role approval request")
@route("/platform-role-approvals/{approval}/approve")
@post
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
@apigen.command(#{ auditAction: "platform_role_approval.approved", guarantee: "transactional" })
@apigen.failsWith(PlatformRoleApprovalInvalidFailure)
@apigen.failsWith(PlatformRoleApprovalNotFoundFailure)
@apigen.failsWith(PlatformRoleApprovalConflictFailure)
@apigen.auditPayload(PlatformRoleApprovalAuditPayload)
op approvePlatformRoleApproval(@path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: PlatformRoleApprovalDecisionRequest): PlatformRoleApprovalOK | CommonErrors;

@tag("Access")
@summary("Cancel a platform-role approval request")
@route("/platform-role-approvals/{approval}/cancel")
@post
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
@apigen.command(#{ auditAction: "platform_role_approval.canceled", guarantee: "transactional" })
@apigen.failsWith(PlatformRoleApprovalInvalidFailure)
@apigen.failsWith(PlatformRoleApprovalNotFoundFailure)
@apigen.failsWith(PlatformRoleApprovalConflictFailure)
@apigen.auditPayload(PlatformRoleApprovalAuditPayload)
op cancelPlatformRoleApproval(@path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: PlatformRoleApprovalDecisionRequest): PlatformRoleApprovalOK | CommonErrors;

@tag("Access")
@summary("Expire a due platform-role approval request")
@route("/platform-role-approvals/{approval}/expire")
@post
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
@apigen.command(#{ auditAction: "platform_role_approval.expired", guarantee: "transactional" })
@apigen.failsWith(PlatformRoleApprovalInvalidFailure)
@apigen.failsWith(PlatformRoleApprovalNotFoundFailure)
@apigen.failsWith(PlatformRoleApprovalConflictFailure)
@apigen.auditPayload(PlatformRoleApprovalAuditPayload)
op expirePlatformRoleApproval(@path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: PlatformRoleApprovalDecisionRequest): PlatformRoleApprovalOK | CommonErrors;

@tag("Access")
@summary("Execute an approved platform-role change")
@route("/platform-role-approvals/{approval}/execute")
@post
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
@apigen.command(#{ auditAction: "platform_role_approval.executed", guarantee: "transactional" })
@apigen.failsWith(PlatformRoleApprovalInvalidFailure)
@apigen.failsWith(PlatformRoleApprovalNotFoundFailure)
@apigen.failsWith(PlatformRoleApprovalConflictFailure)
@apigen.auditPayload(PlatformRoleApprovalAuditPayload)
op executePlatformRoleApproval(@path approval: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): PlatformRoleApprovalOK | CommonErrors;

@tag("Access")
@summary("List project roles")
@route("/projects/{project}/roles")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
op listProjectRoles(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): ListProjectRolesOK | CommonErrors;

alias ListProjectRoleBindingsOK = OkJson<RoleBindingListResponse>;

model CreateProjectRoleBindingCreated {
  ...Response<201>;
  ...Body<RoleBindingResponse>;
}

@tag("Access")
@summary("List target-owned project role bindings")
@route("/projects/{project}/role-bindings")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
op listProjectRoleBindings(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): ListProjectRoleBindingsOK | CommonErrors;

@tag("Access")
@summary("Create a target-owned project role binding")
@route("/projects/{project}/role-bindings")
@post
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
@apigen.command(#{ auditAction: "role_binding.created", guarantee: "transactional" })
@apigen.ui("role-binding.create")
@apigen.failsWith(RoleBindingInvalidFailure)
@apigen.failsWith(RoleBindingConflictFailure)
@apigen.failsWith(RoleBindingNotFoundFailure)
@apigen.auditPayload(RoleBindingAuditPayload)
op createProjectRoleBinding(@path @apigen.target project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: RoleBindingCreateRequest): CreateProjectRoleBindingCreated | CommonErrors;

@tag("Access")
@summary("Delete a target-owned project role binding")
@route("/projects/{project}/role-bindings/{binding}")
@delete
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
@apigen.command(#{ auditAction: "role_binding.deleted", guarantee: "transactional" })
@apigen.ui("role-binding.delete")
@apigen.failsWith(RoleBindingInvalidFailure)
@apigen.failsWith(RoleBindingConflictFailure)
@apigen.failsWith(RoleBindingNotFoundFailure)
@apigen.auditPayload(RoleBindingAuditPayload)
op deleteProjectRoleBinding(@path @apigen.target project: ResourceId, @path binding: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: RoleBindingDeleteRequest): EmptyResponse | CommonErrors;

alias ListEffectiveCapabilitiesOK = OkJson<EffectiveCapabilityListResponse>;

@tag("Access")
@summary("List effective capabilities")
@route("/projects/{project}/effective-capabilities")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
op listEffectiveCapabilities(@path project: ResourceId, @query resourceKind?: ResourceKind, @query resourceId?: ResourceId): ListEffectiveCapabilitiesOK | CommonErrors;

// Fine-grained grants are authored in the project access declaration and
// compiled into the immutable serving generation. Runtime grant CRUD is not a
// supported API surface; keep these shared models for compiled/effective
// authorization contracts that may consume them.

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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listSemanticAttributeDefinitions(@query q?: string, @query ownerKind?: SemanticAttributeOwnerKindValue, @query limit?: ListLimit, @query pageToken?: PageToken): ListSemanticAttributeDefinitionsOK | CommonErrors;

  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  getSemanticAttributeDefinition(@path attribute: string): GetSemanticAttributeDefinitionOK | CommonErrors;

  @route("/{attribute}")
  @patch
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "semantic_attribute.metadata_update" })
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.auditPayload(SemanticAttributeDefinitionAuditPayload)
  updateSemanticAttributeMetadata(@path attribute: string, @header("If-Match") ifMatch: string, @body body: SemanticAttributeMetadataRequest): UpdateSemanticAttributeMetadataOK | CommonErrors;

  @route("/{attribute}/disable")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "semantic_attribute.disable" })
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeDefinitionAuditPayload)
  disableSemanticAttribute(@path @apigen.target attribute: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string): GetSemanticAttributeDefinitionOK | CommonErrors;

  @route("/{attribute}/restore")
  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listPrincipalSemanticAttributeAssignments(@path principal: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticAttributeAssignmentListResponse> | CommonErrors;

  @route("/{attribute}")
  @put
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "semantic_attribute.assignment.set" })
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeAssignmentAuditPayload)
  upsertPrincipalSemanticAttributeAssignment(@path @apigen.target principal: string, @path attribute: string, @header("If-Match") ifMatch: string, @body body: SemanticAttributeAssignmentRequest): SemanticAttributeAssignmentOK | CommonErrors;

  @route("/{attribute}")
  @delete
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listGroupSemanticAttributeAssignments(@path group: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticAttributeAssignmentListResponse> | CommonErrors;

  @route("/{attribute}")
  @put
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "semantic_attribute.assignment.set" })
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeAssignmentAuditPayload)
  upsertGroupSemanticAttributeAssignment(@path @apigen.target group: string, @path attribute: string, @header("If-Match") ifMatch: string, @body body: SemanticAttributeAssignmentRequest): SemanticAttributeAssignmentOK | CommonErrors;

  @route("/{attribute}")
  @delete
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  listSemanticAttributeClaimMappings(@path attribute: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<SemanticAttributeClaimMappingListResponse> | CommonErrors;

  @post
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "semantic_attribute.claim_mapping.set" })
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
  @apigen.auditPayload(SemanticAttributeClaimMappingAuditPayload)
  upsertSemanticAttributeClaimMapping(@path @apigen.target attribute: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string, @body body: SemanticAttributeClaimMappingRequest): SemanticAttributeClaimMappingOK | CommonErrors;

  @route("/{mapping}")
  @delete
  @apigen.authz(#{ mode: "platform_admin" })
  @extension("x-leapview-object-scope", "platform")
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
@apigen.authz(#{ mode: "platform_admin" })
@extension("x-leapview-object-scope", "platform")
  @apigen.failsWith(SemanticAttributeInvalidFailure)
  @apigen.failsWith(SemanticAttributeNotFoundFailure)
op previewSemanticAttributeImpact(@path @apigen.target attribute: string, @body body: SemanticAttributeImpactPreviewRequest): SemanticAttributeImpactPreviewOK | CommonErrors;
