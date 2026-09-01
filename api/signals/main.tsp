import "@yacobolo/apigen";
import "../dashboard/main.tsp";
import "../visualization/main.tsp";
import "./personal-settings.tsp";
import "./product_settings.tsp";
import "./settings.tsp";

@apigen.`package`(#{
  title: "LeapView UI Signal Contracts",
  version: "1.0.0",
  description: "Typed Datastar payload contracts shared by the LeapView Go backend and Lit web components.",
})
namespace LeapViewUISignals;

/** Route surfaces supported by the shared UI signal transport. */
enum RouteKind {
	catalog: "catalog",
  dashboard: "dashboard",
  dashboardBuilder: "dashboard_builder",
  pipelines: "pipelines",
  chat: "chat",
  connections: "connections",
  connectionAsset: "connection_asset",
  data: "data",
  admin: "admin",
  login: "login",
}

/**
 * Typed authoring transport for the unreleased dashboard builder. This is
 * deliberately separate from DashboardEnvelope: the builder edits a draft
 * asset and must never be mistaken for the published runtime projection.
 */
@apigen.contract(#{ kind: "ui-envelope", tags: #["dashboard", "builder", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "dashboard_builder" })
model DashboardBuilderEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "builder" })
  `builder`: DashboardBuilderSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "builderVisuals" })
  `builderVisuals`: Record<DashboardVisualizationSignal>;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "builderFilterContract" })
  `builderFilterContract`: DashboardFilterContract;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "builderFilterState" })
  `builderFilterState`: DashboardFilterState;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "builderFilterOptionPages" })
  `builderFilterOptionPages`: Record<DashboardFilterOptionPage>;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "builderFilterValidation" })
  `builderFilterValidation`: DashboardFilterValidationResult;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "builderFilterCommand" })
  `builderFilterCommand`: DashboardFilterCommand;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "builderFilterOptionRequest" })
  `builderFilterOptionRequest`: DashboardFilterOptionRequest;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["dashboard", "builder", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "dashboard_builder" })
model DashboardBuilderSignal {
  `projectId`: string;
  `dashboardId`: string;
  `draftId`: string;
  `revision`: DashboardBuilderRevisionSignal;
  `title`: string;
  `lifecycle`: "draft" | "published" | "archived";
  `visibility`: "private" | "restricted" | "organization";
  `hasUnpublishedChanges`: boolean;
  `origin`: DashboardBuilderOriginSignal;
  `sourceEvidence`?: DashboardBuilderSourceEvidenceSignal;
  `semanticModel`: DashboardBuilderSemanticModelSignal;
  `visualCatalog`: DashboardBuilderVisualTypeSignal[];
  `filters`: DashboardBuilderFilterSignal[];
  `pages`: DashboardBuilderPageSignal[];
  `selectedPageId`?: string;
  `selectedVisualId`?: string;
  `capabilities`: DashboardBuilderCapabilitiesSignal;
  `diagnostics`: DashboardBuilderDiagnosticSignal[];
  `preview`: DashboardBuilderPreviewStateSignal;
  `save`: DashboardBuilderSaveStateSignal;
  `redirectTo`?: string;
}

model DashboardBuilderRevisionSignal {
  `id`: string;
  `number`: int64;
  `contentHash`: string;
}

model DashboardBuilderOriginSignal {
  `kind`: "file" | "ui" | "agent";
  `label`: string;
  `sourcePath`?: string;
}

@discriminator("kind")
model DashboardBuilderSourceEvidenceSignal {
  `kind`: string;
}

model DashboardBuilderProjectSourceEvidence extends DashboardBuilderSourceEvidenceSignal {
  `kind`: "project";
  `projectId`: string;
  `dashboardId`: string;
  `path`?: string;
  `generationId`: string;
}

model DashboardBuilderSemanticModelSignal {
  `id`: string;
  `title`: string;
  `datasets`: DashboardBuilderDatasetSignal[];
}

model DashboardBuilderDatasetSignal {
  `id`: string;
  `title`: string;
  `fields`: DashboardBuilderFieldSignal[];
}

model DashboardBuilderFieldSignal {
  `id`: string;
  /** Dataset that owns this projected field in the semantic catalog. */
  `datasetId`?: string;
  `label`: string;
  `kind`: "dimension" | "metric";
  `roles`: DashboardBuilderFieldRoleSignal[];
  `dataType`: string;
  `description`?: string;
}

model DashboardBuilderFilterSignal {
  `id`: string;
  `label`: string;
  `description`?: string;
  `dimension`: string;
  `controlType`: "singleSelect" | "multiSelect" | "text" | "numericRange" | "dateRange" | "relativePeriod";
  `required`: boolean;
  `readerEditable`: boolean;
  `targets`: string[];
  `bindings`: DashboardBuilderFilterBindingSignal[];
  `urlParameter`?: string;
}

model DashboardBuilderFilterBindingSignal {
  `id`: string;
  `scope`: "report" | "page";
  `pageId`?: string;
  `targets`: string[];
}

model DashboardBuilderPageSignal {
  `id`: string;
  `title`: string;
  `canvas`: DashboardPageCanvas;
  `grid`: DashboardPageGrid;
  `visuals`: DashboardBuilderVisualSignal[];
  `filterComponents`: DashboardBuilderFilterComponentSignal[];
}

model DashboardBuilderFilterComponentSignal {
  `id`: string;
  `filterId`: string;
  `label`: string;
  `controlType`: "singleSelect" | "multiSelect" | "text" | "numericRange" | "dateRange" | "relativePeriod";
  `placement`: DashboardPagePlacement;
}

model DashboardBuilderVisualTypeSignal {
  `type`: LeapViewDashboard.DashboardVisualType;
  `label`: string;
  `group`: string;
  `referenceHref`: string;
  `roles`: DashboardBuilderFieldRoleSignal[];
  `roleLimits`: DashboardBuilderVisualRoleLimitSignal[];
}

model DashboardBuilderVisualRoleLimitSignal {
  `role`: DashboardBuilderFieldRoleSignal;
  /** Required fields for a renderable target; zero means optional. */
  `minimum`: int32;
  /** Accepted fields before overflow; zero means unbounded. */
  `maximum`: int32;
}

enum DashboardBuilderFieldRoleSignal {
  dimension: "dimension",
  metric: "metric",
  detail: "detail",
}

model DashboardBuilderFormatChoiceSignal {
  `value`: string;
  `label`: string;
}

model DashboardBuilderFormatOptionSignal {
  `key`: string;
  `label`: string;
  `section`: string;
  `control`: "toggle" | "select" | "number" | "text";
  `value`: string;
  `placeholder`?: string;
  `choices`: DashboardBuilderFormatChoiceSignal[];
}

model DashboardBuilderVisualSignal {
  `id`: string;
  `visualId`: string;
  `title`: string;
  `titleVisible`: boolean;
  `type`: LeapViewDashboard.DashboardVisualType;
  /** Bound records dataset for table/map visuals once a first column resolves it. */
  `datasetId`?: string;
  `legendVisible`: boolean;
  `axisVisible`: boolean;
  `dataLabelsVisible`: boolean;
  `formatOptions`: DashboardBuilderFormatOptionSignal[];
  `placement`: DashboardPagePlacement;
  `slots`: DashboardBuilderVisualSlotSignal[];
  /** Strict draft-compile failure attributed to this visual definition. */
  `previewError`?: string;
  `filters`: string[];
  /**
   * Bounded authored interaction metadata for the builder inspector. The
   * projection is intentionally optional: unsupported or malformed authored
   * interactions are represented only when the server can classify them
   * without exposing the authored union to the browser.
   */
  `interaction`?: DashboardBuilderInteractionSignal;
}

/**
 * Read-only interaction projection used by the visual builder. `mappings`
 * preserve the exact authored selection mapping (or a server-inferred default
 * for an unconfigured visual); target arrays contain visual definition IDs.
 */
model DashboardBuilderInteractionSignal {
  `configured`: boolean;
  `editable`: boolean;
  `message`?: string;
  `mode`?: "single" | "multiple";
  `toggle`: boolean;
  @maxItems(64)
  `mappings`: DashboardBuilderInteractionMappingSignal[];
  @maxItems(1024)
  `targets`: string[];
  @maxItems(1024)
  `highlightTargets`: string[];
  @maxItems(1024)
  `noneTargets`: string[];
}

model DashboardBuilderInteractionMappingSignal {
  `field`: string;
  `value`: string;
  `dataset`?: string;
  `grain`?: string;
  `label`?: string;
}

model DashboardBuilderVisualSlotSignal {
  `id`: string;
  `label`: string;
  `kind`: "dimension" | "metric" | "category" | "value" | "detail";
  `fieldId`?: string;
  `required`: boolean;
}

model DashboardBuilderCapabilitiesSignal {
  `canEdit`: boolean;
  `canShare`: boolean;
  `canPublish`: boolean;
  `canArchive`: boolean;
  `canPreview`: boolean;
  `canExport`: boolean;
  `canAddPage`: boolean;
  `canAddVisual`: boolean;
}

model DashboardBuilderDiagnosticSignal {
  `severity`: "error" | "warning" | "info";
  `code`: string;
  `message`: string;
  `targetId`?: string;
}

model DashboardBuilderPreviewStateSignal {
  `active`: boolean;
  `mode`: "draft" | "published";
  `loading`: boolean;
  `href`?: string;
  `error`?: string;
}

model DashboardBuilderSaveStateSignal {
  `state`: "saved" | "dirty" | "saving" | "error";
  `message`?: string;
  `lastSavedAt`?: utcDateTime;
}

/** Relative instance-wide dashboard usage over the rolling popularity window. */
enum PopularityLevel {
  low: "low",
  medium: "medium",
  high: "high",
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminAgentSignal {
  `canWrite`: boolean;
  `enabled`: boolean;
  `model`?: string;
  `systemPrompt`: string;
  `tools`: AdminAgentToolSignal[];
  `updatePath`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminAgentToolSignal {
  `defaults`: Record<unknown>;
  `description`: string;
  `effect`: string;
  `inputSchema`: Record<unknown>;
  `name`: string;
  `outputSchema`: Record<unknown>;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminContentSectionSignal {
  `facts`?: DefinitionFactSignal[];
  `table`?: RecordTableSignal;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminMetricSignal {
  `detail`?: string;
  `label`: string;
  `value`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminPublicationSignal {
  `projectId`: string;
  `name`: string;
  `dashboard`: string;
  `defaultPage`: string;
  `status`: string;
  `origins`: string[];
  `generation`?: string;
  `publicUrl`: string;
  `embedUrl`: string;
  `iframeSnippet`: string;
  `configuredAt`?: string;
  `suspendedAt`?: string;
  `disabledAt`?: string;
  `rotatedAt`?: string;
  `history`: string[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminPublicationCommand {
  `projectId`: string;
  `publication`: string;
  `action`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminDirectoryListItemSignal {
  `avatarUrl`?: string;
  `email`: string;
  `groupCount`: int64;
  `href`: string;
  `id`: string;
  `joinedAt`: string;
  `lastSeenAt`: string;
  `name`: string;
  `status`: string;
  `username`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminDirectoryListSignal {
  `filterLabel`: string;
  `items`: AdminDirectoryListItemSignal[];
  `searchPlaceholder`: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["admin", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "admin" })
model AdminPageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: AdminPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminPageSignal {
	`active`: string;
	`agent`?: AdminAgentSignal;
	`directoryList`?: AdminDirectoryListSignal;
	`empty`?: string;
	`headerDetail`: string;
	`headerTitle`: string;
	`kind`: RouteKind;
	`listFilter`?: string;
	`listFilterOptions`?: string[];
	`listQuery`?: string;
	`metrics`?: AdminMetricSignal[];
	`publications`?: AdminPublicationSignal[];
  `sections`?: AdminContentSectionSignal[];
  `storage`?: AdminStorageSignal;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminQueryDetailSignal {
  `connectionWaitMs`: int64;
  `correlationId`?: string;
  `createdAt`?: string;
  `durationMs`: int64;
  `error`?: string;
  `eventId`?: string;
  `loading`: boolean;
  `modelId`?: string;
  `databaseMs`: int64;
  `objectId`?: string;
  `objectType`?: string;
  `operation`?: string;
  `planText`?: string;
  `planningMs`: int64;
  `principalId`?: string;
  `queryError`?: string;
  `queryJson`?: string;
  `queryKind`?: string;
  `requestId`?: string;
  `rowsReturned`: int64;
  `sql`?: string;
  `status`?: string;
  `statusLabel`?: string;
  `surface`?: string;
  `target`?: string;
  `projectId`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminQueryEventSignal {
  `correlationId`: string;
  `connectionWaitMs`: int64;
  `createdAt`: string;
  `databaseMs`: int64;
  `durationMs`: int64;
  `error`: string;
  `id`: string;
  `modelId`: string;
  `objectId`: string;
  `objectType`: string;
  `operation`: string;
  `planText`: string;
  `planningMs`: int64;
  `principalId`: string;
  `queryJson`: string;
  `queryKind`: string;
  `requestId`: string;
  `rowsReturned`: int64;
  `sql`: string;
  `status`: string;
  `surface`: string;
  `target`: string;
  `projectId`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model AdminQueryHistoryCommand {
  `action`: string;
  `eventId`?: string;
  `filterMenu`?: FilterMenuCommand;
  `filters`: AdminQueryHistoryFilters;
  `limit`?: int64;
  `pageToken`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminQueryHistoryFilters {
  `from`?: string;
  `kinds`?: string[];
  `principals`?: string[];
  `search`?: string;
  `statuses`?: string[];
  `surfaces`?: string[];
  `target`?: string;
  `to`?: string;
  `projects`?: string[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminQueryHistorySignal {
  `error`: string;
  `filterMenus`?: FilterMenuSignal[];
  `filters`: AdminQueryHistoryFilters;
  `hasMore`: boolean;
  `limit`: int64;
  `loadedCountLabel`: string;
  `loading`: boolean;
  `nextCursor`: string;
  `table`: RecordTableSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminStorageSignal {
  `status`: string;
  `summary`: AdminStorageSummary;
  `tables`: AdminStorageTableSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminStorageSummary {
  `dataFileCount`: int64;
  `tableCount`: int64;
  `totalDataSizeLabel`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminStorageTableSignal {
  `beginSnapshot`: int64;
  `columnCount`: int64;
  `fileCount`: int64;
  `key`: string;
  `name`: string;
  `rowCount`: int64;
  `rowCountLabel`: string;
  `schema`: string;
  `sizeBytes`: int64;
  `sizeLabel`: string;
  `type`: string;
}

model AssetLineageEdgeSignal {
  `id`: string;
  `kind`: "visual" | "filter" | "header";
  `label`?: string;
  `source`: string;
  `target`: string;
}

model AssetLineageGraphSignal {
  `edges`: AssetLineageEdgeSignal[];
  `nodes`: AssetLineageNodeSignal[];
}

model AssetLineageNodeSignal {
  `containedCount`?: int64;
  `containedSummary`?: string;
  `href`?: string;
  `id`: string;
  `kind`: string;
  `label`: string;
  `meta`?: string;
  `rank`: int64;
  `selected`?: boolean;
  `side`: string;
  `usedByCount`?: int64;
  `usesCount`?: int64;
  `visibleDownstreamCount`?: int64;
  `visibleUpstreamCount`?: int64;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["catalog", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "catalog" })
model CatalogDashboardSignal {
	`appearanceColor`: string;
	`appearanceIcon`: string;
	`catalogScope`: "mine" | "shared" | "managed";
	`description`?: string;
	`dashboardId`: string;
  `href`: string;
  `id`: string;
  `lastRefreshedAt`?: string;
	`owner`?: string;
	`pageCount`: int64;
	`popularity`?: PopularityLevel;
	`semanticModel`?: string;
	`status`: "private_draft" | "published" | "unpublished_changes";
	`tags`?: string[];
	`title`: string;
	`updatedAt`?: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["catalog", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "catalog" })
model CatalogPageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: CatalogPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["catalog", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "catalog" })
model CatalogPageSignal {
	`dashboards`: CatalogDashboardSignal[];
	`description`: string;
	`kind`: RouteKind;
	`listFilter`?: string;
	`listQuery`?: string;
	`title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["project", "pipelines", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "pipelines" })
model PipelineMetricSignal {
  `detail`?: string;
  `label`: string;
  `tone`?: string;
  `value`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["project", "pipelines", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "pipelines" })
model PipelineListItemSignal {
  `assetId`: string;
  `canRun`: boolean;
  `description`?: string;
  `duration`?: string;
  `href`: string;
  `id`: string;
  `lastSuccessful`?: string;
  `nextRun`?: string;
  `pipelineId`: string;
  `running`: boolean;
  `schedule`: string;
  `semanticModel`: string;
  `status`: string;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["project", "pipelines", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "pipelines" })
model PipelineCommandSignal {
  `action`: string;
  `assetId`: string;
  `pipelineId`: string;
  `runId`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["project", "pipelines", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "pipelines" })
model PipelineCommandStatusSignal {
  `error`: string;
  `loading`: boolean;
  `message`: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["project", "pipelines", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "pipelines" })
model PipelinePageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: PipelinePageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "pipelineCommand" })
  `pipelineCommand`: PipelineCommandSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "pipelineCommandStatus" })
  `pipelineCommandStatus`: PipelineCommandStatusSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["project", "pipelines", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "pipelines" })
model PipelinePageSignal {
  `activeTab`: string;
  `description`: string;
  `environment`: string;
  `kind`: RouteKind;
  `metrics`: PipelineMetricSignal[];
  `pipelines`: PipelineListItemSignal[];
  `runsTable`: RecordTableSignal;
  `title`: string;
}

model ChatArtifactSignal {
  `id`: string;
  `type`: string;
  `summary`?: string;
}

model AgentReferenceKeySignal {
	`kind`: string;
	`id`: string;
}

model AgentReferenceLocationSignal {
  `dashboardId`?: string;
  `dashboardName`?: string;
  `pageId`?: string;
  `pageName`?: string;
  `href`: string;
}

model AgentReferenceSignal {
  `reference`: AgentReferenceKeySignal;
  `name`: string;
  `description`?: string;
	`visualType`?: string;
	`hierarchy`: string[];
  `href`: string;
  `locations`: AgentReferenceLocationSignal[];
  `context`: string[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["agent", "search", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "agent" })
model AgentReferenceSearchSignal {
  `query`: string;
  `requestId`: int64;
  `results`: AgentReferenceSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["agent", "dashboard", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "dashboard" })
model AgentContextSignal {
  `surface`: string;
  `dashboardId`: string;
  `dashboardTitle`: string;
  `pageId`: string;
  `pageTitle`: string;
  `modelId`: string;
  `datasetId`?: string;
  `exploration`?: DataExploreAgentContextSignal;
  `generation`: int64;
  `filters`: DashboardFilterState;
  `referenceLimit`: int32;
  `references`: AgentReferenceSignal[];
}

model ChatConversationSummary {
  `archivedAt`?: string;
  `createdAt`: string;
  `id`: string;
  `lastMessageText`?: string;
  `messageCount`: int64;
  `principalId`: string;
  `status`: string;
  `title`: string;
  `titlePending`?: boolean;
  `updatedAt`: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["chat", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "chat" })
model ChatEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agent" })
  `agent`: ChatSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agentContext" })
  `agentContext`: AgentContextSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agentReferenceSearch" })
  `agentReferenceSearch`: AgentReferenceSearchSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: ChatPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "merge", `x-leapview-signal-key`: "visuals" })
  `visuals`: Record<LeapViewVisualization.VisualizationEnvelope>;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["chat", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "chat" })
model ChatPageSignal {
  `description`: string;
  `kind`: RouteKind;
  `title`: string;
  `view`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["chat", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "chat" })
model ChatSignal {
  `activeConversationId`: string;
  `composer`: ComposerSignal;
  `conversations`: ChatConversationSummary[];
  `status`: ChatStatus;
  `transcript`: ChatTranscriptItemSignal[];
}

model ChatStatus {
  `enabled`: boolean;
  `error`?: string;
  `running`: boolean;
}

model ChatTranscriptItemSignal {
  `argumentsJson`?: string;
  `artifact`?: ChatArtifactSignal;
  `conversationId`?: string;
  `createdAt`?: string;
  `error`?: string;
  `id`: string;
  `inputFormat`?: string;
  `inputJson`?: string;
  `kind`: string;
  `markdown`?: string;
  `name`?: string;
  `outputOrdinal`?: int64;
  `parentMessageId`?: string;
  `references`?: AgentReferenceSignal[];
  `resultFormat`?: string;
  `resultJson`?: string;
  `resultSummary`?: string;
  `runId`?: string;
  `status`?: string;
  `summary`?: string;
  `text`?: string;
  `title`?: string;
  `toolCallId`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model ChromeSignal {
  `sidebar`: SidebarSignal;
}

model ComposerSignal {
  `disabled`: boolean;
  `placeholder`: string;
  `value`: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["connections", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "connections" })
model ConnectionsPageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "connectionAdmin" })
  `connectionAdmin`: ConnectionAdministrationSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: ConnectionsPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["connections", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "connections" })
model ConnectionsPageSignal {
  `connections`: ConnectionSummarySignal[];
  `description`?: string;
  `environment`?: string;
  `kind`: RouteKind;
  `query`?: string;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["connections", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "connections" })
model ConnectionSummarySignal {
  `credentialStatus`: string;
  `description`?: string;
  `detailHref`: string;
  `id`: string;
  `kind`: string;
  `lifecycle`: ConnectionLifecycleSignal;
  `scope`: string;
  `sourceCount`: int64;
  `title`: string;
}

model ConnectionAdministrationCommandSignal {
  `action`: string;
  `assetId`: string;
  `authenticationMode`: string;
  `confirmationToken`: string;
  `connectorKind`: string;
  `credentialEnvironment`: string;
  `credentialProjectId`: string;
  `database`: string;
  `expectedRevision`: int64;
  `host`: string;
  `logicalConnection`: string;
  `objectScope`: string;
  `options`: string;
  `port`: string;
  `secretKey`: string;
  `secretPath`: string;
  `sourceIdentity`: string;
  `surface`: string;
  `tlsMode`: string;
}

model ConnectionAdministrationSignal {
  `command`: ConnectionAdministrationCommandSignal;
  `status`: ConnectionAdministrationStatusSignal;
}

model ConnectionAdministrationStatusSignal {
  `error`: string;
  `loading`: boolean;
  `message`: string;
}

model ConnectionLifecycleActionSignal {
  `destructive`: boolean;
  `id`: string;
  `label`: string;
  `primary`: boolean;
}

model ConnectionLifecycleSignal {
  `actions`: ConnectionLifecycleActionSignal[];
  `assetId`: string;
  `authenticationMode`: string;
  `bindingId`: string;
  `canManage`: boolean;
  `canTest`: boolean;
  `connectorKind`: string;
  `credentialEnvironment`: string;
  `credentialProjectId`: string;
  `database`: string;
  `diagnosticCode`: string;
  `enabled`: boolean;
  `exists`: boolean;
  `health`: string;
  `host`: string;
  `lastValidatedAt`: string;
  `logicalConnection`: string;
  `objectScope`: string;
  `options`: string;
  `port`: string;
  `revision`: int64;
  `secretKey`: string;
  `secretPath`: string;
  `sourceIdentity`: string;
  `state`: string;
  `statusLabel`: string;
  `tlsMode`: string;
  `tone`: string;
  `validatedVersion`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["dashboard", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "dashboard" })
model DashboardComponentSignal {
  `badges`?: string[];
  `binding`?: DashboardFilterBindingRef;
  `description`?: string;
  `eyebrow`?: string;
  `height`: float64;
  `id`: string;
  `kind`: string;
  `placement`: DashboardPagePlacement;
  `presentation`?: DashboardFilterPresentation;
  `subtitle`?: string;
  `title`?: string;
  `visual`?: string;
  `width`: float64;
  `x`: float64;
  `y`: float64;
}

model DashboardFilterBindingRef {
  `scope`: "report" | "page";
  `id`: string;
}

model DashboardFilterPresentation {
  `style`: "dropdown" | "list" | "buttons" | "input" | "numeric_range" | "date_range" | "relative_period";
  `search`: boolean;
  `selectAll`: boolean;
  `showCounts`: boolean;
  `showSummary`: boolean;
  `compact`: boolean;
  `title`?: string;
  `description`?: string;
  `ariaLabel`?: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["dashboard", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "dashboard" })
model DashboardEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agent" })
  `agent`: ChatSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agentContext" })
  `agentContext`: AgentContextSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agentReferenceSearch" })
  `agentReferenceSearch`: AgentReferenceSearchSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "merge", `x-leapview-signal-key`: "agentVisuals" })
  `agentVisuals`: Record<LeapViewVisualization.VisualizationEnvelope>;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "filterContract" })
  `filterContract`: DashboardFilterContract;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "filterState" })
  `filterState`: DashboardFilterState;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "merge", `x-leapview-signal-key`: "filterOptionPages" })
  `filterOptionPages`: Record<DashboardFilterOptionPage>;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "filterCommand" })
  `filterCommand`: DashboardFilterCommand;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "filterOptionRequest" })
  `filterOptionRequest`: DashboardFilterOptionRequest;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "filterValidation" })
  `filterValidation`: DashboardFilterValidationResult;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "interactionCommand" })
  `interactionCommand`: DashboardInteractionCommand;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "interactionRevision" })
  `interactionRevision`: int64;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "navigationCommand" })
  `navigationCommand`: DashboardNavigationCommand;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "interactionSelections" })
  `interactionSelections`: DashboardInteractionSelection[];
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "spatialInteractionCommand" })
  `spatialInteractionCommand`: LeapViewVisualization.VisualizationSpatialSelectionCommand;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "spatialSelections" })
  `spatialSelections`: LeapViewVisualization.VisualizationSpatialSelectionState[];
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: DashboardPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "visualWindowCommand" })
  `visualWindowCommand`: LeapViewVisualization.VisualizationWindowRequest;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "urlParams" })
  `urlParams`: Record<unknown>;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "merge", `x-leapview-signal-key`: "visuals" })
  `visuals`: Record<DashboardVisualizationSignal>;
}

@discriminator("kind")
model DashboardFilterValue {
  `kind`: string;
}

model DashboardStringFilterValue extends DashboardFilterValue {
  `kind`: "string";
  `value`: string;
}

model DashboardBooleanFilterValue extends DashboardFilterValue {
  `kind`: "boolean";
  `value`: boolean;
}

model DashboardIntegerFilterValue extends DashboardFilterValue {
  `kind`: "integer";
  /** Canonical precision-safe base-10 integer. */
  `value`: string;
}

model DashboardDecimalFilterValue extends DashboardFilterValue {
  `kind`: "decimal";
  /** Canonical precision-safe base-10 decimal. */
  `value`: string;
}

model DashboardDateFilterValue extends DashboardFilterValue {
  `kind`: "date";
  /** Canonical ISO 8601 calendar date. */
  `value`: string;
}

model DashboardTimestampFilterValue extends DashboardFilterValue {
  `kind`: "timestamp";
  /** Canonical RFC 3339 timestamp. */
  `value`: string;
}

model DashboardFilterBound {
  `value`: DashboardFilterValue;
  `inclusive`: boolean;
}

@discriminator("kind")
model DashboardFilterExpression {
  `kind`: string;
}

model DashboardUnfilteredExpression extends DashboardFilterExpression {
  `kind`: "unfiltered";
}

model DashboardNullCheckExpression extends DashboardFilterExpression {
  `kind`: "null_check";
  `operator`: "is_null" | "is_not_null";
}

model DashboardSetExpression extends DashboardFilterExpression {
  `kind`: "set";
  `operator`: "in" | "not_in";
  `values`: DashboardFilterValue[];
}

model DashboardComparisonExpression extends DashboardFilterExpression {
  `kind`: "comparison";
  `operator`: "equals" | "not_equals" | "contains" | "not_contains" | "starts_with" | "ends_with" | "greater_than" | "greater_than_or_equal" | "less_than" | "less_than_or_equal";
  `value`: DashboardFilterValue;
}

model DashboardRangeExpression extends DashboardFilterExpression {
  `kind`: "range";
  `lower`?: DashboardFilterBound;
  `upper`?: DashboardFilterBound;
}

model DashboardRelativePeriodExpression extends DashboardFilterExpression {
  `kind`: "relative_period";
  `direction`: "previous" | "current" | "next";
  `count`: int32;
  `unit`: "minute" | "hour" | "day" | "week" | "month" | "quarter" | "year";
  `includeCurrent`: boolean;
  `anchor`: "current_time" | "first_available" | "last_available" | "fixed";
  `anchorValue`?: DashboardFilterValue;
}

model DashboardAppliedFilterState {
  `expression`: DashboardFilterExpression;
  `resolvedExpression`: DashboardFilterExpression;
  `evaluatedAt`?: utcDateTime;
}

model DashboardFilterState {
  `revision`: int64;
  `appliedControls`: Record<DashboardAppliedFilterState>;
  `draftControls`: Record<DashboardFilterExpression>;
  `dirtyBindings`: string[];
  `defaultsRevision`: string;
}

model DashboardFilterPredicatePolicy {
  `kind`: "null_check" | "set" | "comparison" | "range" | "relative_period";
  `operators`: string[];
}

model DashboardFilterOptionSource {
  `kind`: "none" | "static" | "distinct";
  `limit`: int32;
  `includeNull`: boolean;
  `values`: DashboardFilterStaticOption[];
}

model DashboardFilterStaticOption {
  `value`: DashboardFilterValue;
  `label`: string;
}

model DashboardCompiledFilterDefinition {
  `id`: string;
  `label`: string;
  `description`?: string;
  `field`: string;
  `dataset`?: string;
  `valueKind`: "string" | "boolean" | "integer" | "decimal" | "date" | "timestamp";
  `predicates`: DashboardFilterPredicatePolicy[];
  `options`: DashboardFilterOptionSource;
  `formatPattern`?: string;
  `formatUnit`?: string;
  `timezone`: string;
  `calendar`: string;
  `weekStart`: string;
}

model DashboardCompiledFilterBinding {
  `key`: string;
  `id`: string;
  `filter`: string;
  `scope`: "report" | "page";
  `pageID`?: string;
  `default`: DashboardFilterExpression;
  `selectionMode`: "single" | "multiple";
  `maxSelectedValues`: int32;
  `required`: boolean;
  `readerEditable`: boolean;
  `urlParam`?: string;
  `paneVisible`: boolean;
  `paneOrder`: int32;
  `paneLabel`?: string;
  `targets`: string[];
  `optionDependencies`: DashboardFilterBindingRef[];
}

model DashboardFilterContract {
  `applicationMode`: "immediate" | "deferred";
  `definitions`: Record<DashboardCompiledFilterDefinition>;
  `bindings`: Record<DashboardCompiledFilterBinding>;
}

model DashboardFilterOptionItem {
  `value`?: DashboardFilterValue;
  `label`: string;
  `null`: boolean;
  `count`?: int64;
  `selected`: boolean;
  `available`: boolean;
}

model DashboardFilterOptionPage {
  `bindingKey`: string;
  `servingStateID`: string;
  `streamGeneration`: int64;
  `filterRevision`: int64;
  `requestGeneration`: int64;
  `items`: DashboardFilterOptionItem[];
  `complete`: boolean;
  `nextCursor`?: string;
  `consumerIdentity`: string;
}

model DashboardFilterValidationResult {
  `accepted`: boolean;
  `message`: string;
  `currentRevision`: int64;
  `clientMutationID`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["dashboard", "filter", "command"] })
@discriminator("kind")
model DashboardFilterCommand {
  `kind`: string;
  `baseRevision`: int64;
  `clientMutationID`: string;
}

model DashboardFilterMutateCommand extends DashboardFilterCommand {
  `kind`: "mutate";
  `bindingKey`: string;
  `operation`: "set" | "clear" | "reset_binding";
  `expression`?: DashboardFilterExpression;
}

model DashboardFilterApplyCommand extends DashboardFilterCommand {
  `kind`: "apply";
}

model DashboardFilterCancelCommand extends DashboardFilterCommand {
  `kind`: "cancel";
}

model DashboardFilterResetCommand extends DashboardFilterCommand {
  `kind`: "reset";
  `resetScope`: "page" | "dashboard";
  `bindingKeys`: string[];
}

@apigen.contract(#{ kind: "ui-command", tags: #["dashboard", "navigation", "command"] })
model DashboardNavigationCommand {
  `pageID`: string;
  `baseFilterRevision`: int64;
  `clientMutationID`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["dashboard", "filter", "options"] })
model DashboardFilterOptionRequest {
  `bindingKey`: string;
  `search`: string;
  `cursor`?: string;
  `limit`: int32;
  `servingStateID`: string;
  `filterRevision`: int64;
  `requestGeneration`: int64;
}

model DashboardFilterOption {
  `label`: string;
  `value`: string;
}

model DashboardInteractionCommand {
  `action`: string;
  `dataRevision`: int64;
  `filterRevision`: int64;
  @doc("Stable interaction ID from the source visualization specification. The legacy field name is retained for wire compatibility.")
  `interactionKind`: string;
  `interactionRevision`: int64;
  `mappings`: DashboardInteractionCommandMapping[];
  `servingStateID`: string;
  `sourceId`: string;
  `sourceKind`: "visual";
  `specRevision`: string;
  `toggle`: boolean;
}

/**
 * Dashboard visualization transport that keeps large renderer data in one
 * opaque payload outside Datastar's recursively reactive object graph. The
 * browser validates the typed transport header and reconstructs the canonical
 * VisualizationEnvelope before passing it to the visualization host.
 */
@apigen.contract(#{ kind: "ui-signal", tags: #["dashboard", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "dashboard" })
model DashboardVisualizationSignal {
  `schemaVersion`: 4;
  `visualID`: string;
  `rendererID`: string;
  `specRevision`: string;
  `spec`: LeapViewVisualization.VisualizationSpec;
  `dataRevision`: int64;
  `dataState`: LeapViewVisualization.VisualizationDataStateTransport;
  `selection`: LeapViewVisualization.VisualizationSelectionEntry[];
  `highlights`: LeapViewVisualization.VisualizationHighlightState[];
  `spatialSelection`?: LeapViewVisualization.VisualizationSpatialSelectionState;
  `status`: LeapViewVisualization.VisualizationStatus;
  `diagnostics`: LeapViewVisualization.VisualizationDiagnostic[];
  `servingStateID`: string;
  `streamGeneration`: int64;
  `filterRevision`: int64;
  `interactionRevision`: int64;
  `consumerIdentity`: string;
}

model DashboardInteractionCommandMapping {
  `dataset`?: string;
  `field`: string;
  `grain`?: string;
  `label`?: string;
  /** A typed JSON scalar. Runtime command validation rejects arrays and objects. */
  `value`: JSONScalar;
}

union JSONScalar {
  text: string,
  number: float64,
  boolean: boolean,
  nil: null,
}

model DashboardInteractionConfig {
  `kind`: string;
  `mappings`: DashboardInteractionConfigMapping[];
  `targets`?: string[];
  `toggle`: boolean;
}

model DashboardInteractionConfigMapping {
  `dataset`?: string;
  `field`: string;
  `grain`?: string;
  `label`?: string;
  `value`: string;
}

model DashboardInteractionSelection {
  `entries`: DashboardInteractionSelectionEntry[];
  `id`: string;
  @doc("Stable interaction ID from the source visualization specification. The legacy field name is retained for wire compatibility.")
  `interactionKind`: string;
  `label`: string;
  `order`: int64;
  `sourceId`: string;
  `sourceKind`: "visual";
}

model DashboardInteractionSelectionEntry {
  `label`?: string;
  `mappings`: DashboardInteractionSelectionMapping[];
}

model DashboardInteractionSelectionMapping {
  `dataset`?: string;
  `field`: string;
  `grain`?: string;
  `label`?: string;
  /** A typed JSON scalar. Runtime selection validation rejects arrays and objects. */
  `value`: JSONScalar;
}

model DashboardPageCanvas {
  `height`: int64;
  `width`: int64;
}

model DashboardPageGrid {
  `columns`: int64;
  `gap`: int64;
  `padding`: int64;
  `rowHeight`: int64;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["dashboard", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "dashboard" })
model DashboardPageNavSignal {
  `active`: boolean;
  `href`: string;
  `id`: string;
  `title`: string;
}

model DashboardPagePlacement {
  `col`: int64;
  `colSpan`: int64;
  `row`: int64;
  `rowSpan`: int64;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["dashboard", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "dashboard" })
model DashboardPageSignal {
  `appearanceColor`: string;
  `appearanceIcon`: string;
  `canvas`: DashboardPageCanvas;
  `components`: DashboardComponentSignal[];
  `dashboardId`: string;
  `dashboardTitle`: string;
  `description`?: string;
  `grid`: DashboardPageGrid;
  `headerDetail`: string;
  `kind`: RouteKind;
  `modelId`: string;
  `modelTitle`: string;
  `pageId`: string;
  `pageTitle`: string;
  `pages`: DashboardPageNavSignal[];
  `presentation`: "app" | "public" | "embed";
  `title`: string;
}

model DashboardStatus {
  `error`: string;
  `generation`: int64;
  `lastUpdated`: string;
  `loading`: boolean;
  /** Backend-owned dashboard refresh completion percentage from 0 through 100. */
  `progressPercent`: float64;
  `refreshId`: string;
  `setupRequired`: boolean;
}

@apigen.contract(#{ kind: "ui-command", tags: #["data", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "data" })
model DataExplorerCommand {
  `explore`?: DataExploreCommand;
  `mode`?: "browse" | "explore";
  `block`?: string;
  `columnWidths`?: Record<float64>;
  `count`: int64;
  `limit`: int64;
  `objectKey`?: string;
  `offset`: int64;
  `requestSeq`: int64;
  `resetVersion`: int64;
  `sort`: DataPreviewSortSignal;
  `start`: int64;
  `visibleColumns`?: string[];
}

model DataExploreAgentContextSignal {
  `dimensions`: string[];
  `filters`: DataExploreFilterSignal[];
  `limit`: int64;
  `metrics`: string[];
  `sort`: DataExploreSortSignal[];
  `time`?: DataExploreTimeSignal;
}

@apigen.contract(#{ kind: "ui-command", tags: #["data", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "data" })
model DataExploreCommand {
  `columnWidths`?: Record<float64>;
  `datasetId`?: string;
  `dimensions`: string[];
  `filters`: DataExploreFilterSignal[];
  `limit`: int64;
  `metrics`: string[];
  `modelId`?: string;
  `requestSeq`: int64;
  `resetVersion`: int64;
  `sort`: DataExploreSortSignal[];
  `time`?: DataExploreTimeSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExploreDatasetSignal {
  `description`?: string;
  `entities`: SemanticModelGraphEntitySignal[];
  `fieldCount`: int64;
  `grainEntity`: string;
  `grainFields`: string[];
  `id`: string;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExploreFieldSignal {
  `compatible`: boolean;
  `compatibilityReason`?: string;
  `description`?: string;
  `dataset`?: string;
  `id`: string;
  `kind`: "dimension" | "metric";
  `label`: string;
  `modelTable`: string;
  `rebaseDatasetId`?: string;
  `relationshipPath`?: string[];
  `selected`: boolean;
  `type`?: string;
}

model DataExploreFilterSignal {
  `dataset`?: string;
  `field`: string;
  `operator`: string;
  `values`: string[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExploreModelSignal {
  `datasets`: DataExploreDatasetSignal[];
  `description`?: string;
  `id`: string;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExploreResultSignal {
  `columns`: DataPreviewColumnSignal[];
  `durationMs`: int64;
  `error`?: string;
  `plan`?: string;
  `requestSeq`: int64;
  `rows`: Record<unknown>[];
  `rowsReturned`: int64;
  `sql`?: string;
  `truncated`: boolean;
  `warnings`: string[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExploreSignal {
  `command`: DataExploreCommand;
  `datasets`: DataExploreDatasetSignal[];
  `fields`: DataExploreFieldSignal[];
  `models`: DataExploreModelSignal[];
  `result`: DataExploreResultSignal;
  `selectedDataset`?: DataExploreDatasetSignal;
  `selectedModel`?: DataExploreModelSignal;
}

model DataExploreSortSignal {
  `direction`: "asc" | "desc";
  `field`: string;
}

model DataExploreTimeSignal {
  `alias`?: string;
  `field`: string;
  `grain`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExplorerObjectSignal {
  `assetId`?: string;
  `columnCount`: int64;
  `columns`?: DataPreviewColumnSignal[];
  `description`?: string;
  `detailHref`?: string;
  `grain`?: string;
  `key`: string;
  `layer`: string;
  `modelId`?: string;
  `rowCountLabel`?: string;
  `source`?: string;
  `table`?: string;
  `title`: string;
  `resourceId`: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["data", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "data" })
model DataExplorerPageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agent" })
  `agent`: ChatSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agentContext" })
  `agentContext`: AgentContextSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "agentReferenceSearch" })
  `agentReferenceSearch`: AgentReferenceSearchSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "merge", `x-leapview-signal-key`: "agentVisuals" })
  `agentVisuals`: Record<LeapViewVisualization.VisualizationEnvelope>;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "dataExplorer" })
  `dataExplorer`: DataExplorerSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: DataExplorerPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExplorerPageSignal {
  `description`?: string;
  `kind`: RouteKind;
  `selectedObject`?: string;
  `tabs`: ResourceTabSignal[];
  `title`: string;
  `context`: DataExplorerContextSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExplorerSignal {
  `command`: DataExplorerCommand;
  `explore`: DataExploreSignal;
  `objects`: DataExplorerObjectSignal[];
  `preview`: DataPreviewSignal;
  `selectedKey`?: string;
  `selectedObject`?: DataExplorerObjectSignal;
  `warnings`?: string[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExplorerContextSignal {
  `active`: boolean;
  `environment`: string;
  `generationId`: string;
  `objectCount`: int64;
  `projectId`: string;
  `projectTitle`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataPreviewBlockSignal {
  `requestSeq`: int64;
  `resetVersion`: int64;
  `rows`: Record<unknown>[];
  `sort`: DataPreviewSortSignal;
  `start`: int64;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataPreviewColumnSignal {
  `defaultValue`?: string;
  `description`?: string;
  `key`: string;
  `label`: string;
  `nullable`?: boolean;
  `primaryKey`?: boolean;
  `type`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataPreviewSignal {
  `availableRows`: int64;
  `blocks`: Record<DataPreviewBlockSignal>;
  `chunkSize`: int64;
  `columns`: DataPreviewColumnSignal[];
  `error`?: string;
  `loadingBlock`?: string;
  `resetVersion`: int64;
  `rowHeight`: int64;
  `sort`: DataPreviewSortSignal;
  `sql`?: string;
  `totalRowLabel`?: string;
  `totalRows`: int64;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataPreviewSortSignal {
  `column`?: string;
  `direction`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model DefinitionFactSignal {
  `code`?: boolean;
  `label`: string;
  `value`: string;
  `wide`?: boolean;
}

@apigen.contract(#{ kind: "ui-command", tags: #["shared", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "shared" })
model FilterMenuCommand {
  `action`?: string;
  `menuId`?: string;
  `search`?: string;
  `selected`?: string[];
  `value`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model FilterMenuOptionSignal {
  `countLabel`?: string;
  `description`?: string;
  `disabled`: boolean;
  `icon`?: string;
  `label`: string;
  `selected`: boolean;
  `value`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model FilterMenuSignal {
  `emptyLabel`?: string;
  `error`?: string;
  `id`: string;
  `label`: string;
  `loading`: boolean;
  `mode`?: string;
  `options`?: FilterMenuOptionSignal[];
  `placeholder`?: string;
  `search`?: string;
  `selected`?: string[];
  `summaryLabel`?: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["login", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "login" })
model LoginPageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: LoginPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["login", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "login" })
model LoginPageSignal {
  `backgroundModuleSrc`: string;
  `kind`: RouteKind;
  `localAuth`: boolean;
  `mustChangePassword`: boolean;
  `providerLabel`: string;
  `ssoAuth`: boolean;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model RecordTableBadgeSignal {
  `label`: string;
  `tone`?: string;
}

model RecordTableColumnSelector {
  `defaultColumns`?: string[];
  `enabled`: boolean;
  `label`?: string;
  `storageKey`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model RecordTableColumnSignal {
  `align`?: string;
  `header`: string;
  `hrefKey`?: string;
  `id`: string;
  `kind`?: string;
  `toggleable`?: boolean;
  `width`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model RecordTableSignal {
  `columnSelector`?: RecordTableColumnSelector;
  `columns`: RecordTableColumnSignal[];
  `density`?: string;
  `empty`: string;
  `minWidth`?: string;
  `rowAction`?: string;
  `rows`: Record<unknown>[];
}

model ReportFilterConfig {
  `custom`?: boolean;
  `default`: ReportFilterDefault;
  `defaultOperator`?: string;
  `description`?: string;
  `dimension`: string;
  `dataset`?: string;
  `fromURLParam`?: string;
  `id`: string;
  `label`: string;
  `operator`?: string;
  `operatorURLParam`?: string;
  `operators`?: string[];
  `options`?: ReportFilterOption[];
  `presets`?: ReportFilterPreset[];
  `targets`?: ReportFilterTargets;
  `toURLParam`?: string;
  `type`: string;
  `urlParam`?: string;
  `values`?: ReportFilterValues;
}

model ReportFilterDefault {
  `from`?: string;
  `operator`?: string;
  `preset`?: string;
  `to`?: string;
  `value`?: string;
  `values`?: string[];
}

model ReportFilterDefinition {
  `custom`?: boolean;
  `default`: ReportFilterDefault;
  `defaultOperator`?: string;
  `description`?: string;
  `dimension`: string;
  `dataset`?: string;
  `fromURLParam`?: string;
  `label`: string;
  `operator`?: string;
  `operatorURLParam`?: string;
  `operators`?: string[];
  `options`?: ReportFilterOption[];
  `presets`?: ReportFilterPreset[];
  `targets`?: ReportFilterTargets;
  `toURLParam`?: string;
  `type`: string;
  `urlParam`?: string;
  `values`?: ReportFilterValues;
}

model ReportFilterOption {
  `label`: string;
  `value`: string;
}

model ReportFilterPreset {
  `from`?: string;
  `label`: string;
  `relativeDays`?: int64;
  `to`?: string;
  `value`: string;
}

model ReportFilterTargets {
  `visuals`?: string[];
}

model ReportFilterValues {
  `limit`?: int64;
  `source`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model RouteRuntimeSignal {
  `clientId`?: string;
  `dashboardId`?: string;
  `kind`: RouteKind;
  `modelId`?: string;
  `pageId`?: string;
  `streamInstanceId`?: string;
  `servingStateId`?: string;
  `projectId`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model SemanticModelGraphEdgeSignal {
  `cardinality`: string;
  `id`: string;
  `label`: string;
  `source`: string;
  `sourceField`: string;
  `target`: string;
  `targetField`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model SemanticModelGraphFieldSignal {
  `entities`?: string[];
  `grain`?: boolean;
  `join`?: boolean;
  `label`?: string;
  `name`: string;
  `relationships`?: string[];
  `type`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model SemanticModelGraphEntitySignal {
  `fields`: string[];
  `grain`?: boolean;
  `name`: string;
  `type`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model SemanticModelGraphNodeSignal {
  `badges`?: string[];
  `description`?: string;
  `entities`?: SemanticModelGraphEntitySignal[];
  `fields`: SemanticModelGraphFieldSignal[];
  `grainEntity`?: string;
  `id`: string;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model SemanticModelGraphSignal {
  `edges`: SemanticModelGraphEdgeSignal[];
  `datasets`?: string[];
  `nodes`: SemanticModelGraphNodeSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SidebarActionSignal {
  `href`: string;
  `icon`: string;
  `label`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SidebarAreaSignal {
  `href`: string;
  `icon`: string;
  `id`: string;
  `label`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SidebarGroupSignal {
  `items`: SidebarItemSignal[];
  `label`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SidebarHistoryItemSignal {
  `active`: boolean;
  `href`: string;
  `id`: string;
  `pending`?: boolean;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SidebarHistorySignal {
  `emptyText`?: string;
  `items`: SidebarHistoryItemSignal[];
  `label`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SidebarItemSignal {
  `active`?: boolean;
  `href`: string;
  `icon`: string;
  `id`: string;
  `label`: string;
  `meta`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SidebarSignal {
  `active`: string;
  `admin`?: boolean;
  `area`?: string;
  `areas`?: SidebarAreaSignal[];
  `compact`: boolean;
  `dashboardId`?: string;
  `dashboardTitle`: string;
  `groups`: SidebarGroupSignal[];
  `history`?: SidebarHistorySignal;
  `modelId`?: string;
  `modelTitle`?: string;
  `pageTitle`: string;
  `primaryAction`?: SidebarActionSignal;
  `productLogoUrl`?: string;
  `productName`: string;
  `userAvatarUrl`?: string;
  `userName`?: string;
  `userRole`?: string;
  `userSettingsHref`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SubSidebarItemSignal {
  `active`: boolean;
  `href`: string;
  `id`: string;
  `pending`?: boolean;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["shared", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "shared" })
model SubSidebarSignal {
  `activeId`: string;
  `ariaLabel`: string;
  `collapsible`: boolean;
  `disabled`?: boolean;
  `emptyText`?: string;
  `items`: SubSidebarItemSignal[];
  `label`: string;
  `numbered`: boolean;
  `railLabel`: string;
  `storageKey`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceActionSignal {
  `command`?: string;
  `disabled`?: boolean;
  `href`?: string;
  `icon`?: string;
  `label`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceAssetDetailsSignal {
  `overview`: DefinitionFactSignal[];
  `sections`: ResourceDetailSectionSignal[];
  `semanticModelGraph`?: SemanticModelGraphSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceAssetDefinitionSignal {
  `sections`: ResourceDetailSectionSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceAssetLineageSignal {
  `count`: int64;
  `graph`: AssetLineageGraphSignal;
  `usedByTable`: RecordTableSignal;
  `usesTable`: RecordTableSignal;
}

model ResourceAssetListSignal {
  `activeType`?: string;
  `assets`: ResourceAssetSummarySignal[];
  `empty`: string;
  `query`?: string;
  `searchHref`: string;
  `tabs`: ResourceTabSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ModelFieldDrawerSignal {
  `fieldKey`: string;
  `open`: boolean;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model RefreshRunDrawerSignal {
  `open`: boolean;
  `runId`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model AssetVersionDrawerSignal {
  `open`: boolean;
  `versionId`: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["resource", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "resource" })
model ResourceAssetPageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "connectionAdmin" })
  `connectionAdmin`?: ConnectionAdministrationSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "modelFieldDrawer" })
  `modelFieldDrawer`?: ModelFieldDrawerSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "refreshRunDrawer" })
  `refreshRunDrawer`?: RefreshRunDrawerSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "assetVersionDrawer" })
  `assetVersionDrawer`?: AssetVersionDrawerSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: ResourceAssetPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceAssetPageSignal {
  `actions`?: ResourceActionSignal[];
  `activeSection`: string;
  `asset`: ResourceAssetSummarySignal;
  `assetId`: string;
  `breadcrumbs`: ResourceBreadcrumbSignal[];
  `connectionLifecycle`?: ConnectionLifecycleSignal;
  `dashboardAppearance`?: DashboardAppearanceSignal;
  `definition`?: ResourceAssetDefinitionSignal;
  `details`?: ResourceAssetDetailsSignal;
  `drawerParent`?: ResourceAssetPageSignal;
  `environment`?: string;
  `kind`: RouteKind;
  `lineage`?: ResourceAssetLineageSignal;
  `refresh`?: ResourceAssetRefreshSignal;
  `tabs`: ResourceTabSignal[];
  `title`: string;
  `versions`?: ResourceAssetVersionsSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["dashboard", "appearance", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model DashboardAppearanceSignal {
  `color`: string;
  `icon`: string;
  `revision`: int64;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceAssetRefreshSignal {
  `facts`?: DefinitionFactSignal[];
  `lastSuccessful`: string;
  `running`: boolean;
  `runsTable`?: RecordTableSignal;
  `status`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceAssetSummarySignal {
  `description`?: string;
  `detailHref`: string;
  `id`: string;
  `key`: string;
  `openHref`: string;
  `parentHref`?: string;
  `parentTitle`?: string;
  `title`: string;
  `type`: string;
  `typeLabel`: string;
}

model ResourceAssetVersionsSignal {
  `currentContentHash`: string;
  `table`: RecordTableSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceBreadcrumbSignal {
  `current`?: boolean;
  `href`?: string;
  `label`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceCatalogItemSignal {
  `description`: string;
  `href`: string;
  `id`: string;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceDetailSectionSignal {
  `code`?: string;
  `facts`?: DefinitionFactSignal[];
  `lang`?: string;
  `table`?: RecordTableSignal;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["resource", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "resource" })
model ResourcePageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: ResourcePageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourcePageSignal {
	`assetList`?: ResourceAssetListSignal;
	`description`?: string;
	`environment`?: string;
	`kind`: RouteKind;
	`listFilter`?: string;
	`listQuery`?: string;
	`title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["resource", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "resource" })
model ResourceTabSignal {
  `active`: boolean;
  `count`?: int64;
  `href`: string;
  `id`: string;
  `label`: string;
}
