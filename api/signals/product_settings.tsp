import "@yacobolo/apigen";

/**
 * Product/platform administration signals.  This contract intentionally lives
 * beside (rather than inside) the shared admin envelope: platform settings are
 * mounted by the admin shell as a typed signal subtree and can evolve without
 * changing the workspace administration contract.
 */
namespace LeapViewUISignals;

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "product-settings", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model ProductSettingsSignal {
  `active`: string;
  `canManage`: boolean;
  `general`: ProductGeneralSignal;
  `authentication`: ProductAuthenticationSignal;
  `api`: ProductAPIStatusSignal;
  `system`: ProductSystemSignal;
  `error`?: string;
}

model ProductGeneralSignal {
  `displayName`: string;
  `revision`: int64;
  `updatedAt`: string;
  `logo`?: ProductLogoSignal;
  `instanceId`: string;
  `canonicalOrigin`: string;
  `environment`: string;
}

model ProductLogoSignal {
  `url`: string;
  `sha256`: string;
  `mediaType`: string;
  `sizeBytes`: int64;
  `width`: int32;
  `height`: int32;
}

model ProductAvailabilitySignal {
  `available`: boolean;
  `enabled`: boolean;
}

model ProductNamedAvailabilitySignal {
  `available`: boolean;
  `enabled`: boolean;
  `provider`?: string;
}

model ProductAuthenticationSignal {
  `browserEnabled`: boolean;
  `apiTokenOnly`: boolean;
  `local`: ProductAvailabilitySignal;
  `oidc`: ProductNamedAvailabilitySignal;
  `azure`: ProductAvailabilitySignal;
  `scim`: ProductAvailabilitySignal;
  `managedBy`: "deployment";
}

model ProductAPIStatusSignal {
  `bearerCredentials`: ProductAvailabilitySignal;
  `servicePrincipals`: ProductAvailabilitySignal;
  `oauth`: ProductAvailabilitySignal;
  `mcp`: ProductAvailabilitySignal;
  `externalMcpIssuer`: boolean;
}

model ProductBuildSignal {
  `version`: string;
  `revision`: string;
  `buildTime`: string;
  `dirty`: boolean;
  `development`: boolean;
}

model ProductAgentSignal {
  `available`: boolean;
  `configured`: boolean;
  `provider`?: string;
  `modelConfigured`: boolean;
}

model ProductLimitsSignal {
  `queryResultMaxRows`: int32;
  `queryResultMaxBytes`: int64;
  `managedDataMaxFiles`: int32;
  `managedDataMaxFileBytes`: int64;
  `managedDataMaxRevisionBytes`: int64;
}

model ProductRuntimeSignal {
  `health`: string;
  `controlPlane`: string;
  `environment`: string;
}

model ProductSystemSignal {
  `instanceId`: string;
  `canonicalOrigin`: string;
  `environment`: string;
  `build`: ProductBuildSignal;
  `storageBackend`: string;
  `agent`: ProductAgentSignal;
  `limits`: ProductLimitsSignal;
  `runtime`: ProductRuntimeSignal;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "product-settings", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model ProductSettingsCommand {
  `action`: string;
  `displayName`?: string;
  `revision`: int64;
}
