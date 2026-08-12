using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

@route("/api/v1")
namespace LeapViewAPI;

model InstanceResponse {
  id: string;
  canonicalOrigin: string;
  environment: string;
}

model ProductLogoResponse {
  url: string;
  sha256: string;
  mediaType: string;
  sizeBytes: int64;
  width: int32;
  height: int32;
}

model ProductSettingsResponse {
  displayName: string;
  logo?: ProductLogoResponse;
  updatedAt: utcDateTime;
}

model ProductSettingsPatchRequest { displayName: string; }

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ProductIdentityUpdatedAuditPayload {
  @apigen.auditPublic
  fields: string[];
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ProductLogoUpdatedAuditPayload {
  @apigen.auditInternal
  sha256: string;
}

@apigen.auditSchema(#{ schemaVersion: 1, retention: "security" })
model ProductLogoDeletedAuditPayload {
  @apigen.auditPublic
  removed: boolean;
}

model ProductSettingsOK {
  ...OkResponse;
  @header etag: string;
  ...Body<ProductSettingsResponse>;
}

model ProductLogoImageResponse {
  ...OkResponse;
  @header contentType: string;
  @header etag: string;
  @header cacheControl: string;
  @header contentLength: int64;
  @body body: bytes;
}

model ProductAvailabilityResponse { available: boolean; enabled: boolean; }
model ProductNamedAvailabilityResponse { available: boolean; enabled: boolean; provider?: string; }

model ProductAuthenticationStatusResponse {
  browserEnabled: boolean;
  apiTokenOnly: boolean;
  local: ProductAvailabilityResponse;
  oidc: ProductNamedAvailabilityResponse;
  azure: ProductAvailabilityResponse;
  scim: ProductAvailabilityResponse;
  managedBy: "deployment";
}

model ProductAPIStatusResponse {
  bearerCredentials: ProductAvailabilityResponse;
  servicePrincipals: ProductAvailabilityResponse;
  oauth: ProductAvailabilityResponse;
  mcp: ProductAvailabilityResponse;
  externalMcpIssuer: boolean;
}

model ProductAgentStatusResponse { available: boolean; configured: boolean; provider?: string; modelConfigured: boolean; }
model ProductLimitsResponse {
  queryResultMaxRows: int32;
  queryResultMaxBytes: int64;
  managedDataMaxFiles: int32;
  managedDataMaxFileBytes: int64;
  managedDataMaxRevisionBytes: int64;
}
model ProductBuildResponse { version: string; revision: string; buildTime: string; dirty: boolean; development: boolean; }

model ProductSystemStatusResponse {
  instanceId: string;
  canonicalOrigin: string;
  environment: string;
  build: ProductBuildResponse;
  controlPlane: string;
  storageBackend: string;
  agent: ProductAgentStatusResponse;
  limits: ProductLimitsResponse;
}

alias PublicInstanceErrors = BadRequest | RateLimited | InternalServerError | ServiceUnavailable;

@tag("Instance")
@summary("Get this LeapView instance identity")
@route("/instance")
@get
@apigen.authz(#{ mode: "none" })
op getInstance(): OkJson<InstanceResponse> | PublicInstanceErrors;

@tag("Instance")
@summary("Get mutable product identity")
@route("/instance/settings")
@get
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
@apigen.manual
@extension("x-leapview-object-scope", "platform")
op getProductSettings(): ProductSettingsOK | CommonErrors;

@tag("Instance")
@summary("Update mutable product identity")
@route("/instance/settings")
@patch
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
@apigen.manual
@apigen.command(#{
  auditAction: "product.identity.updated",
  guarantee: "transactional",
  failures: #[
    #{ kind: "invalid", statusCode: 422, code: "INVALID_PRODUCT_IDENTITY", publicDetail: "The product identity is invalid." },
    #{ kind: "precondition", statusCode: 412, code: "PRODUCT_IDENTITY_PRECONDITION_FAILED", publicDetail: "The product identity changed before this request was applied." },
  ],
})
@apigen.auditPayload(ProductIdentityUpdatedAuditPayload)
@extension("x-leapview-object-scope", "platform")
@apigen.ui("product-settings.identity.update")
op updateProductSettings(@header("If-Match") ifMatch: string, @body body: ProductSettingsPatchRequest): ProductSettingsOK | CommonErrors;

@tag("Instance")
@summary("Reset mutable product identity to its defaults")
@route("/instance/settings/reset")
@post
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
@apigen.manual
@apigen.command(#{
  auditAction: "product.identity.reset",
  guarantee: "transactional",
  failures: #[
    #{ kind: "precondition", statusCode: 412, code: "PRODUCT_IDENTITY_PRECONDITION_FAILED", publicDetail: "The product identity changed before this request was applied." },
  ],
})
@apigen.auditPayload(ProductIdentityUpdatedAuditPayload)
@extension("x-leapview-object-scope", "platform")
@apigen.ui("product-settings.identity.reset")
op resetProductSettings(@header("Idempotency-Key") idempotencyKey: IdempotencyKey, @header("If-Match") ifMatch: string): ProductSettingsOK | CommonErrors;

@tag("Instance")
@summary("Upload the product logo")
@route("/instance/logo")
@put
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
@apigen.manual
@apigen.command(#{
  auditAction: "product.logo.updated",
  guarantee: "transactional",
  failures: #[
    #{ kind: "invalid", statusCode: 422, code: "INVALID_PRODUCT_LOGO", publicDetail: "The product logo is invalid." },
    #{ kind: "precondition", statusCode: 412, code: "PRODUCT_IDENTITY_PRECONDITION_FAILED", publicDetail: "The product identity changed before this request was applied." },
  ],
})
@apigen.auditPayload(ProductLogoUpdatedAuditPayload)
@extension("x-leapview-object-scope", "platform")
@apigen.ui("product-settings.logo.upload")
op uploadProductLogo(@header("If-Match") ifMatch: string, @header contentType: string, @body body: bytes): ProductSettingsOK | CommonErrors;

@tag("Instance")
@summary("Remove the product logo")
@route("/instance/logo")
@delete
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
@apigen.manual
@apigen.command(#{
  auditAction: "product.logo.deleted",
  guarantee: "transactional",
  failures: #[
    #{ kind: "precondition", statusCode: 412, code: "PRODUCT_IDENTITY_PRECONDITION_FAILED", publicDetail: "The product identity changed before this request was applied." },
  ],
})
@apigen.auditPayload(ProductLogoDeletedAuditPayload)
@extension("x-leapview-object-scope", "platform")
@apigen.ui("product-settings.logo.delete")
op deleteProductLogo(@header("If-Match") ifMatch: string): ProductSettingsOK | CommonErrors;

@tag("Instance")
@summary("Read an immutable product logo")
@route("/instance/logo/{digest}")
@get
@apigen.authz(#{ mode: "authenticated" })
@apigen.manual
@extension("x-leapview-object-scope", "platform")
op getProductLogo(@path digest: string): ProductLogoImageResponse | CommonErrors;

@tag("Instance")
@summary("Get redacted authentication configuration status")
@route("/instance/authentication")
@get
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
@apigen.manual
@extension("x-leapview-object-scope", "platform")
op getProductAuthenticationStatus(): OkJson<ProductAuthenticationStatusResponse> | CommonErrors;

@tag("Instance")
@summary("Get redacted system status")
@route("/instance/system")
@get
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
@apigen.manual
@extension("x-leapview-object-scope", "platform")
op getProductSystemStatus(): OkJson<ProductSystemStatusResponse> | CommonErrors;

@tag("Instance")
@summary("Get safe API and protocol availability")
@route("/instance/api-status")
@get
@apigen.authz(#{ mode: "privilege", privilege: "MANAGE_PLATFORM" })
@apigen.manual
@extension("x-leapview-object-scope", "platform")
op getProductAPIStatus(): OkJson<ProductAPIStatusResponse> | CommonErrors;
