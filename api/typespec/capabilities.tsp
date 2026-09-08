using Http;
using TypeSpec.OpenAPI;
using LeapViewAPI.Protocol;

@route("/api/v1")
namespace LeapViewAPI;

enum AuthenticationMode {
  bearer,
}

enum QueryFormat {
  json: "application/json",
  arrow: "application/vnd.apache.arrow.stream",
}

enum UploadProtocol {
  tus,
  s3Multipart: "s3_multipart",
}

/**
 * Project authoring delivery transport exposed by this composed server.
 * Native PostgreSQL targets use the canonical plan/build APIs.
 */
enum DeliveryMode {
  nativePostgres: "native_postgres",
}

enum VisualizationRendererID {
  echarts,
  tanstack,
  html,
  maplibre,
}

enum VisualizationSpecKind {
  cartesian,
  point,
  proportional,
  hierarchy,
  polar,
  table,
  matrix,
  pivot,
  kpi,
  geographic,
}

model VisualizationRendererCapability {
  id: VisualizationRendererID;
  version: string;
  schemaVersion: 7;
  kinds: VisualizationSpecKind[];
}

model VisualizationCapabilities {
  schemaVersion: 7;
  renderers: VisualizationRendererCapability[];
}

model CapabilitiesResponse {
  apiVersion: string;
  buildVersion: string;
  buildRevision: string;
  buildTime: string;
  buildDirty: boolean;
  buildDevelopment: boolean;
  environment: string;
  authentication: AuthenticationMode[];
  deliveryMode: DeliveryMode;
  queryFormats: QueryFormat[];
  uploadProtocols: UploadProtocol[];
  visualization: VisualizationCapabilities;
}

@tag("System")
@summary("Describe server capabilities")
@route("/capabilities")
@get
@apigen.authz(#{ mode: "authenticated" })
op getCapabilities(): OkJson<CapabilitiesResponse> | CommonErrors;
