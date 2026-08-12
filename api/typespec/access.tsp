using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

namespace LeapViewAPI.Access;

model PrincipalResponse {
  id: string;
  kind: string;
  email: string;
  displayName: string;
  disabledAt?: utcDateTime;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model PrincipalListResponse {
  items: PrincipalResponse[];
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
  privileges: string[];
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
  name: string;
  displayName: string;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
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
  projectId: string;
  @apigen.auditPublic
  privileges: string[];
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
  projectId: string;
  @apigen.auditPublic
  privileges: string[];
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model APITokenAuditPayload {
  @apigen.auditInternal
  name: string;
  @apigen.auditPublic
  privileges: string[];
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
  objectId: string;
  @apigen.auditPublic
  objectType: string;
  @apigen.auditInternal
  projectId: string;
  @apigen.auditPublic
  environment: string;
  @apigen.auditPii
  subjectId: string;
  @apigen.auditPublic
  subjectType: string;
  @apigen.auditPublic
  privilege: string;
  @apigen.auditPublic
  surface: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model OwnershipTransferAuditPayload {
  @apigen.auditPii
  ownerPrincipalId: string;
  @apigen.auditPublic
  objectType: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model DataPolicyAuditPayload {
  @apigen.auditInternal
  objectId: string;
  @apigen.auditPublic
  policyType: string;
}

model RoleBindingResponse {
  id: string;
  workspaceId: string;
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
  objectType?: string;
  objectId?: string;
  subjectType: string;
  subjectId: string;
  privilege: string;
}

model GrantResponse {
  id: string;
  objectId: string;
  objectType: string;
  workspaceId: string;
  subjectType: string;
  subjectId: string;
  privilege: string;
  inherited?: boolean;
  parentId?: string;
  parentType?: string;
  parentObject?: string;
  createdAt: utcDateTime;
}

model GrantListResponse {
  items: GrantResponse[];
  page: PageInfo;
}

model DataPolicyRequest {
  objectType?: string;
  objectId?: string;
  subjectType?: string;
  subjectId?: string;
  policyType: string;
  expression: JSONObject;
}

model DataPolicyResponse {
  id: string;
  workspaceId: string;
  objectId: string;
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

model EffectivePrivilegeListResponse {
  workspaceId: string;
  objectType: string;
  objectId?: string;
  privileges: string[];
  effectiveGrants: AuthorizationDecisionResponse[];
}

model OwnershipTransferRequest {
  objectType?: string;
  objectId?: string;
  ownerPrincipalId: string;
}

model SecurableObjectResponse {
  id: string;
  type: string;
  workspaceId: string;
  parentId?: string;
  ownerPrincipalId?: string;
  displayName?: string;
  createdAt: utcDateTime;
  updatedAt: utcDateTime;
}

model AuthorizationDecisionResponse {

  allowed?: boolean;
  privilege: string;
  reason: string;
  objectType: string;
  objectId?: string;
  grantId?: string;
  grantObjectId?: string;
  subjectType?: string;
  subjectId?: string;
  inherited: boolean;
  owner: boolean;
  platform: boolean;
}

model AuthorizationCheckRequest {
  privilege: string;
  objectType?: string;
  objectId?: string;
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
  projectId: string;
  privileges: string[];
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
@apigen.command(#{
  audit: #{ required: true, successAction: "authoring.device.decided", guarantee: "transactional" },
  failures: #[
    #{ kind: "denied", statusCode: 400, code: "DEVICE_AUTHORIZATION_DENIED", publicDetail: "Device authorization was denied." },
    #{ kind: "expired", statusCode: 400, code: "DEVICE_AUTHORIZATION_EXPIRED", publicDetail: "Device authorization has expired." },
    #{ kind: "invalid_principal", statusCode: 400, code: "INVALID_PRINCIPAL", publicDetail: "The authorizing principal is invalid." },
    #{ kind: "not_found", statusCode: 404, code: "DEVICE_AUTHORIZATION_NOT_FOUND", publicDetail: "Device authorization was not found." },
  ],
})
@apigen.auditPayload(DeviceAuthorizationDecidedAuditPayload)
@operationId("decideDeviceAuthorization")
op decideDeviceAuthorization(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DeviceAuthorizationApprovalRequest): OkJson<StatusResponse> | CommonErrors;

@tag("Current User")
@route("/me/authoring-sessions")
interface CurrentAuthoringSessions {
  @summary("List current CLI and workload sessions")
  @get
  @apigen.authz(#{ mode: "authenticated" })
  @operationId("listCurrentAuthoringSessions")
  listCurrentAuthoringSessions(@query limit?: ListLimit, @query pageToken?: PageToken): OkJson<AuthoringSessionListResponse> | CommonErrors;

  @summary("Revoke a current CLI or workload session")
  @route("/{session}")
  @delete
  @apigen.authz(#{ mode: "authenticated" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "authoring.session.revoked", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid_credential", statusCode: 400, code: "INVALID_AUTHORING_CREDENTIAL", publicDetail: "The authoring session is invalid." },
      #{ kind: "not_found", statusCode: 404, code: "AUTHORING_SESSION_NOT_FOUND", publicDetail: "Authoring session not found." },
    ],
  })
  @apigen.auditPayload(AuthoringSessionRevokedAuditPayload)
  @operationId("revokeCurrentAuthoringSession")
  revokeCurrentAuthoringSession(@path session: string): OkJson<StatusResponse> | CommonErrors;
}

alias ListPrincipalsOK = OkJson<PrincipalListResponse>;
alias CreatePrincipalCreated = CreatedJson<LocalPasswordResetResponse>;
alias GetPrincipalOK = OkJson<PrincipalResponse>;
alias UpdatePrincipalOK = OkJson<PrincipalResponse>;
alias ResetPrincipalPasswordOK = OkJson<LocalPasswordResetResponse>;
alias ListPrincipalSessionsOK = OkJson<SessionListResponse>;

@tag("Access")
@route("/principals")
interface Principals {
  @summary("List principals")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @operationId("listPrincipals")
  listPrincipals(@query email?: string, @query q?: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListPrincipalsOK | CommonErrors;

  @summary("Create a local principal")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @apigen.command(#{
    audit: #{ required: true, successAction: "principal.local_user.created", guarantee: "transactional" },
    failures: #[
      #{ kind: "conflict", statusCode: 409, code: "PRINCIPAL_ALREADY_EXISTS", publicDetail: "A principal with this email already exists." },
    ],
  })
  @apigen.auditPayload(PrincipalCreatedAuditPayload)
  @operationId("createPrincipal")
  createPrincipal(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: PrincipalCreateRequest): CreatePrincipalCreated | CommonErrors;

  @summary("Get a principal")
  @route("/{principal}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @operationId("getPrincipal")
  getPrincipal(@path principal: string): GetPrincipalOK | CommonErrors;

  @summary("Update a principal")
  @route("/{principal}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @apigen.command(#{
    audit: #{ required: true, successAction: "principal.updated", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "PRINCIPAL_NOT_FOUND", publicDetail: "Principal not found." },
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_NOT_MUTABLE", publicDetail: "This principal cannot be modified by this operation." },
    ],
  })
  @apigen.auditPayload(PrincipalUpdatedAuditPayload)
  @operationId("updatePrincipal")
  updatePrincipal(@path principal: string, @header("If-Match") ifMatch: string, @body body: PrincipalPatchRequest): UpdatePrincipalOK | CommonErrors;

  @summary("Delete a principal")
  @route("/{principal}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @apigen.command(#{
    audit: #{ required: true, successAction: "principal.deleted", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "PRINCIPAL_NOT_FOUND", publicDetail: "Principal not found." },
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_NOT_MUTABLE", publicDetail: "This principal cannot be deleted by this operation." },
      #{ kind: "conflict", statusCode: 409, code: "PRINCIPAL_OWNS_OBJECTS", publicDetail: "Transfer owned objects before deleting this principal." },
    ],
  })
  @apigen.auditPayload(PrincipalDeletedAuditPayload)
  @operationId("deletePrincipal")
  deletePrincipal(@path principal: string): EmptyResponse | CommonErrors;

  @summary("Reset a local principal password")
  @route("/{principal}/password-reset")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @apigen.command(#{
    audit: #{ required: true, successAction: "principal.local_password.reset", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "PRINCIPAL_NOT_FOUND", publicDetail: "Principal not found." },
    ],
  })
  @apigen.auditPayload(PrincipalPasswordResetAuditPayload)
  @operationId("resetPrincipalPassword")
  resetPrincipalPassword(@path principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): ResetPrincipalPasswordOK | CommonErrors;

  @summary("Disable a principal and reject its credentials")
  @route("/{principal}/disable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @apigen.command(#{
    audit: #{ required: true, successAction: "principal.disabled", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "PRINCIPAL_NOT_FOUND", publicDetail: "Principal not found." },
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_NOT_MUTABLE", publicDetail: "This principal cannot be disabled by this operation." },
    ],
    targetParameter: "principal",
  })
  @apigen.auditPayload(PrincipalUpdatedAuditPayload)
  @operationId("disablePrincipal")
  disablePrincipal(@path principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<PrincipalResponse> | CommonErrors;

  @summary("Re-enable a disabled principal")
  @route("/{principal}/enable")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @apigen.command(#{
    audit: #{ required: true, successAction: "principal.enabled", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "PRINCIPAL_NOT_FOUND", publicDetail: "Principal not found." },
      #{ kind: "invalid", statusCode: 422, code: "PRINCIPAL_NOT_MUTABLE", publicDetail: "This principal cannot be enabled by this operation." },
    ],
    targetParameter: "principal",
  })
  @apigen.auditPayload(PrincipalUpdatedAuditPayload)
  @operationId("enablePrincipal")
  enablePrincipal(@path principal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey): OkJson<PrincipalResponse> | CommonErrors;

  @summary("List a principal's sessions")
  @route("/{principal}/sessions")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @operationId("listPrincipalSessions")
  listPrincipalSessions(@path principal: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListPrincipalSessionsOK | CommonErrors;

  @summary("Revoke a principal's session")
  @route("/{principal}/sessions/{session}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @extension("x-leapview-object-scope", "grant-management")
  @apigen.command(#{
    audit: #{ required: true, successAction: "session.revoked", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "SESSION_NOT_FOUND", publicDetail: "Session not found." },
    ],
    targetParameter: "principal",
  })
  @apigen.auditPayload(PrincipalSessionRevokedAuditPayload)
  @operationId("revokePrincipalSession")
  revokePrincipalSession(@path principal: string, @path session: string): EmptyResponse | CommonErrors;
}

alias ListServicePrincipalsOK = OkJson<ServicePrincipalListResponse>;
alias CreateServicePrincipalCreated = CreatedJson<PrincipalResponse>;
alias UpdateServicePrincipalOK = OkJson<PrincipalResponse>;
alias CreateServicePrincipalSecretCreated = CreatedJson<ServicePrincipalSecretCreatedResponse>;

@tag("Access")
@route("/service-principals")
interface ServicePrincipals {
  @summary("List service principals")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @operationId("listServicePrincipals")
  listServicePrincipals(@query limit?: ListLimit, @query pageToken?: PageToken): ListServicePrincipalsOK | CommonErrors;

  @summary("Create a service principal")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "service_principal.created", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "SERVICE_PRINCIPAL_INVALID", publicDetail: "The service principal request is invalid." },
    ],
  })
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @operationId("createServicePrincipal")
  createServicePrincipal(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalCreateRequest): CreateServicePrincipalCreated | CommonErrors;

  @summary("Get a service principal")
  @route("/{servicePrincipal}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @operationId("getServicePrincipal")
  getServicePrincipal(@path servicePrincipal: string): OkJson<PrincipalResponse> | CommonErrors;

  @summary("Update a service principal")
  @route("/{servicePrincipal}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "service_principal.updated", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "SERVICE_PRINCIPAL_NOT_FOUND", publicDetail: "Service principal not found." },
      #{ kind: "conflict", statusCode: 409, code: "SERVICE_PRINCIPAL_OWNS_OBJECTS", publicDetail: "Transfer owned objects before deleting this service principal." },
    ],
  })
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @operationId("updateServicePrincipal")
  updateServicePrincipal(@path servicePrincipal: string, @header("If-Match") ifMatch: string, @body body: ServicePrincipalPatchRequest): UpdateServicePrincipalOK | CommonErrors;

  @summary("Delete a service principal")
  @route("/{servicePrincipal}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "service_principal.deleted", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "SERVICE_PRINCIPAL_NOT_FOUND", publicDetail: "Service principal not found." },
    ],
  })
  @apigen.auditPayload(ServicePrincipalAuditPayload)
  @operationId("deleteServicePrincipal")
  deleteServicePrincipal(@path servicePrincipal: string): EmptyResponse | CommonErrors;

  @summary("Create a service principal secret")
  @route("/{servicePrincipal}/secrets")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "service_principal_secret.created", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "SERVICE_PRINCIPAL_SECRET_INVALID", publicDetail: "The service principal secret request is invalid." },
    ],
  })
  @apigen.auditPayload(ServicePrincipalSecretAuditPayload)
  @operationId("createServicePrincipalSecret")
  createServicePrincipalSecret(@path servicePrincipal: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: ServicePrincipalSecretCreateRequest): CreateServicePrincipalSecretCreated | CommonErrors;

  @summary("List service-principal secrets")
  @route("/{servicePrincipal}/secrets")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @operationId("listServicePrincipalSecrets")
  listServicePrincipalSecrets(@path servicePrincipal: string, @query limit?: ListLimit, @query pageToken?: PageToken): OkJson<ServicePrincipalSecretListResponse> | CommonErrors;

  @summary("Get service-principal secret metadata")
  @route("/{servicePrincipal}/secrets/{secret}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @operationId("getServicePrincipalSecret")
  getServicePrincipalSecret(@path servicePrincipal: string, @path secret: string): OkJson<ServicePrincipalSecretResponse> | CommonErrors;

  @summary("Revoke a service principal secret")
  @route("/{servicePrincipal}/secrets/{secret}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "service_principal_secret.revoked", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "SERVICE_PRINCIPAL_SECRET_NOT_FOUND", publicDetail: "Service principal secret not found." },
    ],
    targetParameter: "servicePrincipal",
  })
  @apigen.auditPayload(ServicePrincipalSecretAuditPayload)
  @operationId("revokeServicePrincipalSecret")
  revokeServicePrincipalSecret(@path servicePrincipal: string, @path secret: string): EmptyResponse | CommonErrors;
}

alias ListGroupsOK = OkJson<GroupListResponse>;
alias CreateGroupCreated = CreatedJson<GroupResponse>;
alias GetGroupOK = OkJson<GroupResponse>;
alias UpdateGroupOK = OkJson<GroupResponse>;
alias ListGroupMembersOK = OkJson<GroupMemberListResponse>;
alias AddGroupMemberOK = OkJson<StatusResponse>;

@tag("Access")
@route("/workspaces/{workspace}/groups")
interface Groups {
  @summary("List groups")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("listGroups")
  listGroups(@path workspace: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListGroupsOK | CommonErrors;

  @summary("Create a group")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "group.created", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "GROUP_INVALID", publicDetail: "The group request is invalid." },
    ],
  })
  @apigen.auditPayload(GroupAuditPayload)
  @operationId("createGroup")
  createGroup(@path workspace: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: GroupCreateRequest): CreateGroupCreated | CommonErrors;

  @summary("Delete a group")
  @route("/{group}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "group.deleted", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "GROUP_NOT_FOUND", publicDetail: "Group not found." },
    ],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(GroupAuditPayload)
  @operationId("deleteGroup")
  deleteGroup(@path workspace: string, @path group: string): EmptyResponse | CommonErrors;

  @summary("Get a group")
  @route("/{group}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("getGroup")
  getGroup(@path workspace: string, @path group: string): GetGroupOK | CommonErrors;

  @summary("Update a group")
  @route("/{group}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "group.updated", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "GROUP_NOT_FOUND", publicDetail: "Group not found." },
    ],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(GroupAuditPayload)
  @operationId("updateGroup")
  updateGroup(@path workspace: string, @path group: string, @header("If-Match") ifMatch: string, @body body: GroupPatchRequest): UpdateGroupOK | CommonErrors;

  @summary("List group members")
  @route("/{group}/members")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("listGroupMembers")
  listGroupMembers(@path workspace: string, @path group: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListGroupMembersOK | CommonErrors;

  @summary("Remove a group member")
  @route("/{group}/members/{principal}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "group.member_removed", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "GROUP_MEMBER_NOT_FOUND", publicDetail: "Group member not found." },
    ],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(GroupMemberAuditPayload)
  @operationId("removeGroupMember")
  removeGroupMember(@path workspace: string, @path group: string, @path principal: string): EmptyResponse | CommonErrors;

  @summary("Add a group member")
  @route("/{group}/members/{principal}")
  @put
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "group.member_added", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "GROUP_MEMBER_NOT_FOUND", publicDetail: "Group or principal not found." },
    ],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(GroupMemberAuditPayload)
  @operationId("addGroupMember")
  addGroupMember(@path workspace: string, @path group: string, @path principal: string): AddGroupMemberOK | CommonErrors;
}

alias ListRoleBindingsOK = OkJson<RoleBindingListResponse>;
alias CreateRoleBindingCreated = CreatedJson<RoleBindingResponse>;
alias UpdateRoleBindingOK = OkJson<RoleBindingResponse>;

@tag("Access")
@route("/workspaces/{workspace}/role-bindings")
interface RoleBindings {
  @summary("List role bindings")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("listRoleBindings")
  listRoleBindings(@path workspace: string, @query subjectType?: string, @query subjectId?: string, @query role?: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListRoleBindingsOK | CommonErrors;

  @summary("Create a role binding")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "role_binding.created", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "ROLE_BINDING_INVALID", publicDetail: "The role binding request is invalid." },
    ],
    additionalExposures: #["ui"],
  })
  @apigen.auditPayload(RoleBindingCreatedAuditPayload)
  @operationId("createRoleBinding")
  createRoleBinding(@path workspace: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: RoleBindingRequest): CreateRoleBindingCreated | CommonErrors;

  @summary("Get a workspace role binding")
  @route("/{binding}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("getRoleBinding")
  getRoleBinding(@path workspace: string, @path binding: string): OkJson<RoleBindingResponse> | CommonErrors;

  @summary("Delete a role binding")
  @route("/{binding}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "role_binding.deleted", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "ROLE_BINDING_NOT_FOUND", publicDetail: "Role binding not found." },
      #{ kind: "invalid", statusCode: 400, code: "ROLE_BINDING_INVALID", publicDetail: "The role binding request is invalid." },
    ],
    additionalExposures: #["ui"],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(RoleBindingAuditPayload)
  @operationId("deleteRoleBinding")
  deleteRoleBinding(@path workspace: string, @path binding: string): EmptyResponse | CommonErrors;

  @summary("Update a role binding")
  @route("/{binding}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "role_binding.updated", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "ROLE_BINDING_NOT_FOUND", publicDetail: "Role binding not found." },
    ],
    additionalExposures: #["ui"],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(RoleBindingAuditPayload)
  @operationId("updateRoleBinding")
  updateRoleBinding(@path workspace: string, @path binding: string, @header("If-Match") ifMatch: string, @body body: RoleBindingRequest): UpdateRoleBindingOK | CommonErrors;
}

alias ListWorkspaceRolesOK = OkJson<RoleListResponse>;

@tag("Access")
@summary("List workspace roles")
@route("/workspaces/{workspace}/roles")
@get
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
@operationId("listWorkspaceRoles")
op listWorkspaceRoles(@path workspace: string, @query limit?: ListLimit, @query pageToken?: PageToken): ListWorkspaceRolesOK | CommonErrors;

alias ListEffectivePrivilegesOK = OkJson<EffectivePrivilegeListResponse>;

@tag("Access")
@summary("List effective privileges")
@route("/workspaces/{workspace}/effective-privileges")
@get
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@operationId("listEffectivePrivileges")
op listEffectivePrivileges(@path workspace: string, @query objectType?: string, @query objectId?: string): ListEffectivePrivilegesOK | CommonErrors;

alias ListGrantsOK = OkJson<GrantListResponse>;
alias CreateGrantCreated = CreatedJson<GrantResponse>;

@tag("Access")
@route("/workspaces/{workspace}/grants")
interface Grants {
  @summary("List grants")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("listGrants")
  listGrants(@path workspace: string, @query objectType?: string, @query objectId?: string, @query includeInherited?: boolean, @query limit?: ListLimit, @query pageToken?: PageToken): ListGrantsOK | CommonErrors;

  @summary("Create a grant")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "grant.created", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "GRANT_INVALID", publicDetail: "The grant request is invalid." },
    ],
    additionalExposures: #["ui"],
  })
  @apigen.auditPayload(GrantAuditPayload)
  @operationId("createGrant")
  createGrant(@path workspace: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: GrantRequest): CreateGrantCreated | CommonErrors;

  @summary("Get an object privilege grant")
  @route("/{grant}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("getGrant")
  getGrant(@path workspace: string, @path grant: string): OkJson<GrantResponse> | CommonErrors;

  @summary("Update an object privilege grant")
  @route("/{grant}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "grant.updated", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "GRANT_NOT_FOUND", publicDetail: "Grant not found." },
      #{ kind: "invalid", statusCode: 422, code: "GRANT_INVALID", publicDetail: "The grant is invalid." },
    ],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(GrantAuditPayload)
  @operationId("updateGrant")
  updateGrant(@path workspace: string, @path grant: string, @header("If-Match") ifMatch: string, @body body: GrantRequest): OkJson<GrantResponse> | CommonErrors;

  @summary("Delete a grant")
  @route("/{grant}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "grant.deleted", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "GRANT_NOT_FOUND", publicDetail: "Grant not found." },
    ],
    additionalExposures: #["ui"],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(GrantAuditPayload)
  @operationId("deleteGrant")
  deleteGrant(@path workspace: string, @path grant: string): EmptyResponse | CommonErrors;
}

alias TransferOwnershipOK = OkJson<SecurableObjectResponse>;

@tag("Access")
@summary("Transfer object ownership")
@route("/workspaces/{workspace}/ownership")
@post
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_ITEM" })
@apigen.command(#{
  audit: #{ required: true, successAction: "ownership.transferred", guarantee: "transactional" },
  failures: #[
    #{ kind: "invalid", statusCode: 400, code: "OWNERSHIP_TRANSFER_INVALID", publicDetail: "The ownership transfer request is invalid." },
  ],
})
@apigen.auditPayload(OwnershipTransferAuditPayload)
@operationId("transferOwnership")
op transferOwnership(@path workspace: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: OwnershipTransferRequest): TransferOwnershipOK | CommonErrors;

alias ListDataPoliciesOK = OkJson<DataPolicyListResponse>;
alias CreateDataPolicyCreated = CreatedJson<DataPolicyResponse>;

@tag("Access")
@route("/workspaces/{workspace}/data-policies")
interface DataPolicies {
  @summary("List data policies")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("listDataPolicies")
  listDataPolicies(@path workspace: string, @query objectType?: string, @query objectId?: string, @query includeInherited?: boolean, @query limit?: ListLimit, @query pageToken?: PageToken): ListDataPoliciesOK | CommonErrors;

  @summary("Create a data policy")
  @post
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "data_policy.created", guarantee: "transactional" },
    failures: #[
      #{ kind: "invalid", statusCode: 400, code: "DATA_POLICY_INVALID", publicDetail: "The data policy request is invalid." },
    ],
  })
  @apigen.auditPayload(DataPolicyAuditPayload)
  @operationId("createDataPolicy")
  createDataPolicy(@path workspace: string, @header("Idempotency-Key") idempotencyKey: IdempotencyKey, @body body: DataPolicyRequest): CreateDataPolicyCreated | CommonErrors;

  @summary("Get a data policy")
  @route("/{policy}")
  @get
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @operationId("getDataPolicy")
  getDataPolicy(@path workspace: string, @path policy: string): OkJson<DataPolicyResponse> | CommonErrors;

  @summary("Update a data policy")
  @route("/{policy}")
  @patch
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "data_policy.updated", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "DATA_POLICY_NOT_FOUND", publicDetail: "Data policy not found." },
      #{ kind: "invalid", statusCode: 422, code: "DATA_POLICY_INVALID", publicDetail: "The data policy is invalid." },
    ],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(DataPolicyAuditPayload)
  @operationId("updateDataPolicy")
  updateDataPolicy(@path workspace: string, @path policy: string, @header("If-Match") ifMatch: string, @body body: DataPolicyRequest): OkJson<DataPolicyResponse> | CommonErrors;

  @summary("Delete a data policy")
  @route("/{policy}")
  @delete
  @apigen.authz(#{ mode: "privilege", privilege: "MANAGE_GRANTS" })
  @apigen.command(#{
    audit: #{ required: true, successAction: "data_policy.deleted", guarantee: "transactional" },
    failures: #[
      #{ kind: "not_found", statusCode: 404, code: "DATA_POLICY_NOT_FOUND", publicDetail: "Data policy not found." },
    ],
    targetParameter: "workspace",
  })
  @apigen.auditPayload(DataPolicyAuditPayload)
  @operationId("deleteDataPolicy")
  deleteDataPolicy(@path workspace: string, @path policy: string): EmptyResponse | CommonErrors;
}

@tag("Access")
@summary("Check multiple authorization decisions")
@route("/workspaces/{workspace}/authorization-checks")
@post
@apigen.authz(#{ mode: "privilege", privilege: "USE_WORKSPACE" })
@apigen.query
@operationId("checkAuthorizationBatch")
op checkAuthorizationBatch(@path workspace: string, @body body: AuthorizationBatchCheckRequest): OkJson<AuthorizationBatchCheckResponse> | CommonErrors;
