import "@yacobolo/apigen";

namespace LeapViewUISignals;

/** The authenticated user's personal settings surface. */
@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalSettingsSignal {
  `active`: string;
  `profile`: PersonalProfileSignal;
  `security`: PersonalSecuritySignal;
  `tokens`: PersonalTokensSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalProfileSignal {
  `id`: string;
  `email`: string;
  `displayName`: string;
  `theme`: string;
  `avatarUrl`?: string;
  `identitySource`: string;
  `canEditDisplayName`: boolean;
  `hasLocalPassword`: boolean;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalSecuritySignal {
  `localPasswordEnabled`: boolean;
  `sessions`: PersonalSessionSignal[];
  `authoringSessions`: PersonalAuthoringSessionSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalSessionSignal {
  `id`: string;
  `kind`: string;
  `clientLabel`: string;
  `current`: boolean;
  `createdAt`: string;
  `lastSeenAt`: string;
  `expiresAt`: string;
  `absoluteExpiresAt`: string;
  `revokedAt`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalAuthoringSessionSignal {
  `id`: string;
  `kind`: string;
  `clientId`: string;
  `targetId`: string;
  `projectId`: string;
  `capabilities`: string[];
  `createdAt`: string;
  `lastUsedAt`: string;
  `expiresAt`: string;
  `revokedAt`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalTokensSignal {
  `items`: PersonalTokenSignal[];
  /** Typed action-target options authorized for the active principal. */
  `capabilities`: PersonalCapabilityOptionSignal[];
  /** True when capabilities is the authoritative typed option set, including an explicit empty set. */
  `permissionOptionsReady`: boolean;
  `newToken`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalPermissionTargetSignal {
  `scope`: string;
  `instanceId`?: string;
  `projectId`?: string;
  `resourceKind`?: string;
  `resourceId`?: string;
  `includeFuture`?: boolean;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalPermissionPairSignal {
  `action`: string;
  `target`: PersonalPermissionTargetSignal;
  `profile`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalCapabilityOptionSignal {
  `value`: string;
  `label`: string;
  `description`: string;
  `category`: string;
  /** Every issuance option carries the exact action-target pair(s) it persists. */
  `permissions`?: PersonalPermissionPairSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "personal-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model PersonalTokenSignal {
  `id`: string;
  `name`: string;
  `permissionProfile`?: string;
  `permissions`: PersonalPermissionPairSignal[];
  /** Legacy capability rows remain readable during migration; new issuance uses permissions. */
  `capabilities`: string[];
  `createdAt`: string;
  `lastUsedAt`: string;
  `expiresAt`: string;
  `revokedAt`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "personal-settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model PersonalProfileCommand {
  `action`: string;
  `displayName`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "personal-settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model PersonalThemeCommand {
  `action`: string;
  `theme`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "personal-settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model PersonalPasswordCommand {
  `currentPassword`: string;
  `newPassword`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "personal-settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model PersonalSessionCommand {
  `action`: string;
  `sessionId`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "personal-settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model PersonalAuthoringSessionCommand {
  `action`: string;
  `sessionId`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "personal-settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model PersonalTokenCommand {
  `action`: string;
  `tokenId`: string;
  `name`: string;
  `permissions`: PersonalPermissionPairSignal[];
  `expiresAt`: string;
}
