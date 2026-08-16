// Settings surfaces are kept in a separate contract so they can evolve
// independently from the shared admin page envelope. The admin stream may
// embed these signals under `adminProjects`, `adminServiceAccounts`, and
// `adminAuditLog` without introducing ad-hoc browser fetches.
import "@yacobolo/apigen";

namespace LeapViewUISignals;

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model ProjectRegistrySignal {
  `items`: ProjectRegistryItemSignal[];
  `empty`?: string;
  `loading`: boolean;
  `error`?: string;
  `nextCursor`?: string;
  `hasMore`: boolean;
}

model ProjectRegistryItemSignal {
  `id`: string;
  `title`: string;
  `description`?: string;
  `href`: string;
  `createdAt`?: string;
  `updatedAt`?: string;
  `owner`?: ProjectSubjectSignal;
  `administrators`: ProjectSubjectSignal[];
  `environment`?: string;
  `activeServingStateId`?: string;
  `servingStateStatus`?: string;
  `servingStateSince`?: string;
  `projectId`?: string;
  `currentDeploymentId`?: string;
  `deploymentStatus`?: string;
  `deploymentSince`?: string;
  `currentReleaseId`?: string;
  `links`: ProjectLinksSignal;
}

model ProjectSubjectSignal {
  `subjectType`: string;
  `subjectId`: string;
  `email`?: string;
  `displayName`: string;
  `role`?: string;
}

model ProjectLinksSignal {
  `self`: string;
  `project`: string;
  `release`?: string;
  `deployment`?: string;
  `deployments`?: string;
  `connections`?: string;
  `publications`?: string;
  `agent`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model ServiceAccountsSignal {
  `items`: ServiceAccountSignal[];
  `selectedId`?: string;
  `secrets`?: ServiceAccountSecretSignal[];
  `createdSecret`?: string;
  `error`?: string;
  `loading`: boolean;
  `nextCursor`?: string;
  `hasMore`: boolean;
}

model ServiceAccountSignal { `id`: string; `displayName`: string; `email`?: string; `kind`: string; `createdAt`?: string; `updatedAt`?: string; `disabledAt`?: string; }
model ServiceAccountSecretSignal { `id`: string; `servicePrincipalId`: string; `name`: string; `expiresAt`?: string; `createdAt`?: string; `revokedAt`?: string; }

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AccessAdministrationSignal {
  `principals`: AccessPrincipalSignal[];
  `groups`: AccessGroupSignal[];
  `projects`?: AccessProjectSignal[];
  `sessions`: AccessSessionSignal[];
  `roleAssignments`: AccessRoleAssignmentSignal[];
  `activity`: AccessActivitySignal[];
  `selectedPrincipalId`?: string;
  `selectedGroupId`?: string;
  `temporaryPassword`?: string;
  `redirectTo`?: string;
  `message`?: string;
  `error`?: string;
  `loading`: boolean;
}

model AccessProjectSignal {
  `id`: string;
  `name`: string;
}

model AccessPrincipalCapabilitiesSignal {
  `canUpdateProfile`: boolean;
  `canResetPassword`: boolean;
  `canBlock`: boolean;
  `canUnblock`: boolean;
  `canDelete`: boolean;
  `canManageSessions`: boolean;
}

model AccessPrincipalSignal {
  `id`: string;
  `kind`: string;
  `email`: string;
  `displayName`: string;
  `identitySource`: string;
  `identityProvider`?: string;
  `hasLocalPassword`: boolean;
  `disabledAt`?: string;
  `blockedAt`?: string;
  `createdAt`?: string;
  `updatedAt`?: string;
  `lastSeenAt`?: string;
  `revision`?: string;
  `groups`: AccessGroupReferenceSignal[];
  `capabilities`: AccessPrincipalCapabilitiesSignal;
}

model AccessGroupCapabilitiesSignal {
  `canUpdate`: boolean;
  `canDelete`: boolean;
  `canManageMembers`: boolean;
}

model AccessGroupSignal {
  `id`: string;
  `name`: string;
  `provider`: string;
  `externalId`?: string;
  `createdAt`?: string;
  `revision`?: string;
  `members`: AccessPrincipalReferenceSignal[];
  `capabilities`: AccessGroupCapabilitiesSignal;
}

model AccessGroupReferenceSignal { `id`: string; `name`: string; `provider`: string; }
model AccessPrincipalReferenceSignal { `id`: string; `email`: string; `displayName`: string; }
model AccessSessionSignal { `id`: string; `kind`: string; `createdAt`?: string; `lastSeenAt`?: string; `expiresAt`?: string; }
model AccessRoleAssignmentSignal { `projectId`: string; `resourceId`?: string; `resourceKind`?: string; `role`: string; `capabilities`: string[]; `sourceType`: string; `sourceId`: string; `sourceName`: string; }
model AccessActivitySignal { `id`: string; `action`: string; `actorId`?: string; `actorName`?: string; `status`?: string; `createdAt`: string; }

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model AccessAdministrationCommand {
  `action`: string;
  `principalId`?: string;
  `principalIds`?: string[];
  `groupId`?: string;
  `sessionId`?: string;
  `email`?: string;
  `displayName`?: string;
  `revision`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AuditLogSignal {
  `items`: AuditEventSignal[];
  `filters`: AuditLogFilters;
  `nextCursor`?: string;
  `hasMore`: boolean;
  `loadedCount`: int32;
  `loading`: boolean;
  `error`?: string;
}

model AuditEventSignal { `id`: string; `projectId`?: string; `principalId`?: string; `action`: string; `resourceKind`: string; `resourceId`: string; `capability`?: string; `status`?: string; `requestId`?: string; `correlationId`?: string; `metadata`?: Record<unknown>; `createdAt`: string; }
model AuditLogFilters { `projectId`?: string; `principalId`?: string; `action`?: string; `resourceKind`?: string; `resourceId`?: string; `from`?: string; `to`?: string; }

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model ServiceAccountCommand {
  `action`: string;
  `accountId`?: string;
  `secretId`?: string;
  `displayName`?: string;
  `secretName`?: string;
  `expiresAt`?: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model AuditLogCommand {
  `action`: string;
  `filters`: AuditLogFilters;
  `pageToken`?: string;
  `limit`?: int32;
}
