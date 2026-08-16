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

model RoleBindingRequest {
  subjectType: string;
  subjectId: string;
  role: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model RoleBindingCreatedAuditPayload {
  @apigen.auditInternal
  operationId: string;

  @apigen.auditInternal
  role: string;

  @apigen.auditPii
  subjectId: string;

  @apigen.auditInternal
  subjectType: string;

  @apigen.auditPublic
  surface: string;
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
  publicDetail: "Role binding not found.",
})
model RoleBindingNotFoundFailure {}

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

@apigen.failureDefinition(#{
  kind: "invalid",
  statusCode: 400,
  code: "OWNERSHIP_TRANSFER_INVALID",
  publicDetail: "The ownership transfer request is invalid.",
})
model OwnershipTransferInvalidFailure {}

@apigen.failureDefinition(#{
  kind: "not_found",
  statusCode: 404,
  code: "DATA_POLICY_NOT_FOUND",
  publicDetail: "Data policy not found.",
})
model DataPolicyNotFoundFailure {}

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
model RoleBindingAuditPayload {
  @apigen.auditInternal
  operationId: string;
  @apigen.auditInternal
  role: string;
  @apigen.auditPii
  subjectId: string;
  @apigen.auditInternal
  subjectType: string;
  @apigen.auditPublic
  surface: string;
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

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model OwnershipTransferAuditPayload {
  @apigen.auditPii
  ownerPrincipalId: string;
  @apigen.auditPublic
  resourceKind: ResourceKind;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DataPolicyAuditPayload {
  @apigen.auditInternal
  resourceId: ResourceId;
  @apigen.auditPublic
  resourceKind: ResourceKind;
  @apigen.auditPublic
  policyType: string;
}

model RoleBindingResponse {
  id: string;
  projectId: ResourceId;
  subjectType: string;
  subjectId: string;
  email?: string;
  displayName?: string;
  role: string;
  createdAt: utcDateTime;
}

model RoleBindingListResponse {
  items: RoleBindingResponse[];
  page: PageInfo;
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

model DataPolicyRequest {
  resourceKind: ResourceKind;
  resourceId: ResourceId;
  subjectType?: string;
  subjectId?: string;
  policyType: string;
  expression: JSONObject;
}

model DataPolicyResponse {
  id: string;
  resourceId: ResourceId;
  resourceKind: ResourceKind;
  subjectType?: string;
  subjectId?: string;
  policyType: string;
  expression: JSONObject;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model DataPolicyListResponse {
  items: DataPolicyResponse[];
  page: PageInfo;
}

model EffectiveCapabilityListResponse {
  resourceId: ResourceId;
  resourceKind: ResourceKind;
  capabilities: Capability[];
  effectiveGrants: AuthorizationDecisionResponse[];
}

model OwnershipTransferRequest {
  resourceKind: ResourceKind;
  resourceId: ResourceId;
  ownerPrincipalId: string;
}

model SecurableObjectResponse {
  id: ResourceId;
  resourceKind: ResourceKind;
  parentId?: ResourceId;
  ownerPrincipalId?: string;
  displayName?: string;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
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
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  listPrincipals(@query email?: string, @query q?: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListPrincipalsOK | CommonErrors;

  @summary("Create a local principal")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "principal.local_user.created" })
  @apigen.failsWith(PrincipalAlreadyExistsFailure)
  @apigen.auditPayload(PrincipalCreatedAuditPayload)
  @apigen.ui("principal.create")
  createPrincipal(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: PrincipalCreateRequest): CreatePrincipalCreated | CommonErrors;

  @summary("Get a principal")
  @route("/{principal}")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  getPrincipal(@path principal: string): GetPrincipalOK | CommonErrors;

  @summary("Update a principal")
  @route("/{principal}")
  @patch
  @apigen.authz(#{ mode: "authenticated" })
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
  @apigen.authz(#{ mode: "authenticated" })
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

  @summary("Reset a local principal password")
  @route("/{principal}/password-reset")
  @post
  @apigen.authz(#{ mode: "authenticated" })
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
  @apigen.authz(#{ mode: "authenticated" })
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
  @apigen.authz(#{ mode: "authenticated" })
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
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  listPrincipalSessions(@path principal: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListPrincipalSessionsOK | CommonErrors;

  @summary("Revoke a principal's session")
  @route("/{principal}/sessions/{session}")
  @delete
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  listServicePrincipals(@query limit?: ListLimit, @query pageToken?: PageToken): ListServicePrincipalsOK | CommonErrors;

  @summary("Create a service principal")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal.created" })
  @apigen.failsWith(ServicePrincipalInvalidFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.create")
  createServicePrincipal(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalCreateRequest): CreateServicePrincipalCreated | CommonErrors;

  @summary("Get a service principal")
  @route("/{servicePrincipal}")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  getServicePrincipal(@path servicePrincipal: string): OkJson<PrincipalResponse> | CommonErrors;

  @summary("Update a service principal")
  @route("/{servicePrincipal}")
  @patch
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal.deleted" })
  @apigen.failsWith(ServicePrincipalNotFoundFailure)
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @apigen.ui("service-account.delete")
  deleteServicePrincipal(@path @apigen.target servicePrincipal: string): EmptyResponse | CommonErrors;

  @summary("Create a service principal secret")
  @route("/{servicePrincipal}/secrets")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "service_principal_secret.created" })
  @apigen.failsWith(ServicePrincipalSecretInvalidFailure)
  @apigen.auditPayload(ServicePrincipalSecretAuditPayload)
  @apigen.ui("service-account.secret.create")
  createServicePrincipalSecret(@path @apigen.target servicePrincipal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalSecretCreateRequest): CreateServicePrincipalSecretCreated | CommonErrors;

  @summary("List service-principal secrets")
  @route("/{servicePrincipal}/secrets")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  listServicePrincipalSecrets(@path servicePrincipal: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ServicePrincipalSecretListResponse> | CommonErrors;

  @summary("Get service-principal secret metadata")
  @route("/{servicePrincipal}/secrets/{secret}")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  getServicePrincipalSecret(@path servicePrincipal: string, @path secret: string): OkJson<ServicePrincipalSecretResponse> | CommonErrors;

  @summary("Revoke a service principal secret")
  @route("/{servicePrincipal}/secrets/{secret}")
  @delete
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
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
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  listGroups(@query limit?: ListLimit, @query pageToken?: PageToken): ListGroupsOK | CommonErrors;

  @summary("Create a group")
  @post
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  @apigen.command(#{ auditAction: "group.created" })
  @apigen.failsWith(GroupInvalidFailure)
  @apigen.auditPayload(GroupAuditPayload)
  @apigen.ui("group.create")
  createGroup(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: GroupCreateRequest): CreateGroupCreated | CommonErrors;

  @summary("Delete a group")
  @route("/{group}")
  @delete
  @apigen.authz(#{ mode: "authenticated" })
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
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  getGroup(@path group: string): GetGroupOK | CommonErrors;

  @summary("Update a group")
  @route("/{group}")
  @patch
  @apigen.authz(#{ mode: "authenticated" })
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
  @apigen.authz(#{ mode: "authenticated" })
  @extension("x-leapview-object-scope", "platform")
  listGroupMembers(@path group: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListGroupMembersOK | CommonErrors;

  @summary("Remove a group member")
  @route("/{group}/members/{principal}")
  @delete
  @apigen.authz(#{ mode: "authenticated" })
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
  @apigen.authz(#{ mode: "authenticated" })
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

alias ListRoleBindingsOK = OkJson<RoleBindingListResponse>;
alias CreateRoleBindingCreated = CreatedJson<RoleBindingResponse>;
alias UpdateRoleBindingOK = OkJson<RoleBindingResponse>;

@tag("Access")
@route("/projects/{project}/role-bindings")
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@apigen.commandDefaults(#{ guarantee: "transactional" })
@apigen.auditPayload(RoleBindingAuditPayload)
interface RoleBindings {
  @summary("List role bindings")
  @get
  @extension("x-leapview-object-scope", "project")
  listRoleBindings(@path project: ResourceId, @query subjectType?: string, @query subjectId?: string, @query role?: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListRoleBindingsOK | CommonErrors;

  @summary("Create a role binding")
  @post
  @extension("x-leapview-object-scope", "project")
  @apigen.ui("project.access.role-binding.create")
  @apigen.failsWith(RoleBindingInvalidFailure)
  @apigen.command(#{ auditAction: "role_binding.created" })
  @apigen.auditPayload(RoleBindingCreatedAuditPayload)
  createRoleBinding(@path project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: RoleBindingRequest): CreateRoleBindingCreated | CommonErrors;

  @summary("Get a project role binding")
  @route("/{binding}")
  @get
  @extension("x-leapview-object-scope", "project")
  getRoleBinding(@path project: ResourceId, @path binding: string): OkJson<RoleBindingResponse> | CommonErrors;

  @summary("Delete a role binding")
  @route("/{binding}")
  @delete
  @extension("x-leapview-object-scope", "project")
  @apigen.ui("project.access.role-binding.delete")
  @apigen.failsWith(RoleBindingNotFoundFailure)
  @apigen.failsWith(RoleBindingInvalidFailure)
  @apigen.command(#{ auditAction: "role_binding.deleted" })
  deleteRoleBinding(@path @apigen.target project: ResourceId, @path binding: string): EmptyResponse | CommonErrors;

  @summary("Update a role binding")
  @route("/{binding}")
  @patch
  @extension("x-leapview-object-scope", "project")
  @apigen.ui("project.access.role-binding.update")
  @apigen.failsWith(RoleBindingNotFoundFailure)
  @apigen.command(#{ auditAction: "role_binding.updated" })
  updateRoleBinding(@path @apigen.target project: ResourceId, @path binding: string, @header("If-Match") ifMatch: string, @body body: RoleBindingRequest): UpdateRoleBindingOK | CommonErrors;
}

alias ListProjectRolesOK = OkJson<RoleListResponse>;

@tag("Access")
@summary("List project roles")
@route("/projects/{project}/roles")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
op listProjectRoles(@path project: ResourceId, @query limit?: ListLimit, @query pageToken?: PageToken): ListProjectRolesOK | CommonErrors;

alias ListEffectiveCapabilitiesOK = OkJson<EffectiveCapabilityListResponse>;

@tag("Access")
@summary("List effective capabilities")
@route("/projects/{project}/effective-capabilities")
@get
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
op listEffectiveCapabilities(@path project: ResourceId, @query resourceKind?: ResourceKind, @query resourceId?: ResourceId): ListEffectiveCapabilitiesOK | CommonErrors;

alias ListGrantsOK = OkJson<GrantListResponse>;
alias CreateGrantCreated = CreatedJson<GrantResponse>;

@tag("Access")
@route("/projects/{project}/grants")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface Grants {
  @summary("List grants")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  listGrants(@path project: ResourceId, @query resourceKind?: ResourceKind, @query resourceId?: ResourceId, @query includeInherited?: boolean, @query limit?: ListLimit, @query pageToken?: PageToken): ListGrantsOK | CommonErrors;

  @summary("Create a grant")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.ui("project.access.grant.create")
  @apigen.command(#{
    auditAction: "grant.created",
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "GRANT_INVALID", publicDetail: "The grant request is invalid." },
    ],
  })
  @apigen.auditPayload(GrantAuditPayload)
  createGrant(@path project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: GrantRequest): CreateGrantCreated | CommonErrors;

  @summary("Get a resource capability grant")
  @route("/{grant}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  getGrant(@path project: ResourceId, @path grant: string): OkJson<GrantResponse> | CommonErrors;

  @summary("Update a resource capability grant")
  @route("/{grant}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{
    auditAction: "grant.updated",
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "GRANT_INVALID", publicDetail: "The grant is invalid." },
    ],
  })
  @apigen.failsWith(GrantNotFoundFailure)
  @apigen.auditPayload(GrantAuditPayload)
  updateGrant(@path @apigen.target project: ResourceId, @path grant: string, @header("If-Match") ifMatch: string, @body body: GrantRequest): OkJson<GrantResponse> | CommonErrors;

  @summary("Delete a grant")
  @route("/{grant}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.ui("project.access.grant.delete")
  @apigen.command(#{ auditAction: "grant.deleted" })
  @apigen.failsWith(GrantNotFoundFailure)
  @apigen.auditPayload(GrantAuditPayload)
  deleteGrant(@path @apigen.target project: ResourceId, @path grant: string): EmptyResponse | CommonErrors;
}

alias TransferOwnershipOK = OkJson<SecurableObjectResponse>;

@tag("Access")
@summary("Transfer object ownership")
@route("/projects/{project}/ownership")
@post
@apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
@extension("x-leapview-object-scope", "project")
@apigen.command(#{ auditAction: "ownership.transferred", guarantee: "transactional" })
@apigen.failsWith(OwnershipTransferInvalidFailure)
@apigen.auditPayload(OwnershipTransferAuditPayload)
op transferOwnership(@path project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: OwnershipTransferRequest): TransferOwnershipOK | CommonErrors;

alias ListDataPoliciesOK = OkJson<DataPolicyListResponse>;
alias CreateDataPolicyCreated = CreatedJson<DataPolicyResponse>;

@tag("Access")
@route("/projects/{project}/data-policies")
@apigen.commandDefaults(#{ guarantee: "transactional" })
interface DataPolicies {
  @summary("List data policies")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  listDataPolicies(@path project: ResourceId, @query resourceKind?: ResourceKind, @query resourceId?: ResourceId, @query includeInherited?: boolean, @query limit?: ListLimit, @query pageToken?: PageToken): ListDataPoliciesOK | CommonErrors;

  @summary("Create a data policy")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{
    auditAction: "data_policy.created",
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "DATA_POLICY_INVALID", publicDetail: "The data policy request is invalid." },
    ],
  })
  @apigen.auditPayload(DataPolicyAuditPayload)
  createDataPolicy(@path project: ResourceId, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DataPolicyRequest): CreateDataPolicyCreated | CommonErrors;

  @summary("Get a data policy")
  @route("/{policy}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  getDataPolicy(@path project: ResourceId, @path policy: string): OkJson<DataPolicyResponse> | CommonErrors;

  @summary("Update a data policy")
  @route("/{policy}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{
    auditAction: "data_policy.updated",
    failures: #[
      #{ kind: "invalid", statusCode: 422, code: "DATA_POLICY_INVALID", publicDetail: "The data policy is invalid." },
    ],
  })
  @apigen.failsWith(DataPolicyNotFoundFailure)
  @apigen.auditPayload(DataPolicyAuditPayload)
  updateDataPolicy(@path @apigen.target project: ResourceId, @path policy: string, @header("If-Match") ifMatch: string, @body body: DataPolicyRequest): OkJson<DataPolicyResponse> | CommonErrors;

  @summary("Delete a data policy")
  @route("/{policy}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "PROJECT_ADMIN" })
  @extension("x-leapview-object-scope", "project")
  @apigen.command(#{ auditAction: "data_policy.deleted" })
  @apigen.failsWith(DataPolicyNotFoundFailure)
  @apigen.auditPayload(DataPolicyAuditPayload)
  deleteDataPolicy(@path @apigen.target project: ResourceId, @path policy: string): EmptyResponse | CommonErrors;
}

@tag("Access")
@summary("Check multiple authorization decisions")
@route("/projects/{project}/authorization-checks")
@post
@apigen.authz(#{ mode: "authenticated" })
@extension("x-leapview-object-scope", "project")
@apigen.query
op checkAuthorizationBatch(@path project: ResourceId, @body body: AuthorizationBatchCheckRequest): OkJson<AuthorizationBatchCheckResponse> | CommonErrors;
