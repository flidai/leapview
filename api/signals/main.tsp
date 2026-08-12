import "@yacobolo/apigen";
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
  dashboard: "dashboard",
  catalog: "catalog",
  chat: "chat",
  workspace: "workspace",
  workspaceAsset: "workspace_asset",
  connections: "connections",
  connectionAsset: "connection_asset",
  data: "data",
  admin: "admin",
  login: "login",
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
  `workspaceId`: string;
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
  `workspaceId`: string;
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
  `workspaceId`?: string;
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
  `workspaceId`: string;
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
  `workspaces`?: string[];
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
model AdminStorageColumnSignal {
  `beginSnapshot`: int64;
  `containsNan`: string;
  `containsNull`: string;
  `default`: string;
  `defaultValueDialect`: string;
  `defaultValueType`: string;
  `extraStats`: string;
  `id`: int64;
  `initialDefault`: string;
  `maxValue`: string;
  `minValue`: string;
  `name`: string;
  `nullable`: string;
  `ordinal`: int64;
  `type`: string;
}

@apigen.contract(#{ kind: "ui-command", tags: #["admin", "command"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "command", `x-leapview-surface`: "admin" })
model AdminStorageCommand {
  `databaseId`: string;
  `schema`: string;
  `table`: string;
}

model AdminStorageFileSignal {
  `beginSnapshot`: int64;
  `endSnapshot`: int64;
  `format`: string;
  `id`: int64;
  `path`: string;
  `recordCount`: int64;
  `recordCountLabel`: string;
  `sizeBytes`: int64;
  `sizeLabel`: string;
}

model AdminStorageServingStateSignal {
  `activatedAt`: string;
  `active`: boolean;
  `digest`: string;
  `environment`: string;
  `servingStateId`: string;
  `snapshotId`: int64;
  `status`: string;
  `workspaceId`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminStorageSignal {
  `selectedKey`: string;
  `selectedTable`?: AdminStorageTableSignal;
  `servingStates`: AdminStorageServingStateSignal[];
  `snapshots`: AdminStorageSnapshotSignal[];
  `status`: string;
  `summary`: AdminStorageSummary;
  `tables`: AdminStorageTableSignal[];
  `warnings`: string[];
}

model AdminStorageSnapshotSignal {
  `author`: string;
  `changes`: string;
  `extraInfo`: string;
  `id`: int64;
  `message`: string;
  `protected`: boolean;
  `schemaVersion`: int64;
  `servingStateCount`: int64;
  `time`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminStorageSummary {
  `catalogPath`: string;
  `catalogSizeLabel`: string;
  `dataFileCount`: int64;
  `dataPath`: string;
  `dataSizeLabel`: string;
  `databaseCount`: int64;
  `snapshotCount`: int64;
  `tableCount`: int64;
  `totalDataSizeLabel`: string;
  `totalSizeLabel`: string;
}

model AdminStorageTableHistorySignal {
  `author`: string;
  `changes`: string;
  `extraInfo`: string;
  `message`: string;
  `schemaVersion`: int64;
  `snapshotId`: int64;
  `source`: string;
  `time`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["admin", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "admin" })
model AdminStorageTableSignal {
  `beginSnapshot`: int64;
  `columnCount`: int64;
  `columns`?: AdminStorageColumnSignal[];
  `databaseId`: string;
  `databaseName`: string;
  `databasePath`: string;
  `duckLakePath`: string;
  `endSnapshot`: int64;
  `fileCount`: int64;
  `files`?: AdminStorageFileSignal[];
  `history`?: AdminStorageTableHistorySignal[];
  `key`: string;
  `modelId`: string;
  `modelName`: string;
  `name`: string;
  `rowCount`: int64;
  `rowCountLabel`: string;
  `schema`: string;
  `servingStates`?: AdminStorageServingStateSignal[];
  `sizeBytes`: int64;
  `sizeLabel`: string;
  `tableId`: int64;
  `tableUuid`: string;
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
  `description`?: string;
  `href`: string;
  `id`: string;
  `lastRefreshedAt`?: string;
  `pageCount`: int64;
  `popularity`?: PopularityLevel;
  `semanticModel`?: string;
  `tags`?: string[];
  `title`: string;
  `workspace`: string;
  `workspaceId`: string;
}

model CatalogWorkspaceFilterSignal {
  `id`: string;
  `title`: string;
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
	`workspaceFilters`: CatalogWorkspaceFilterSignal[];
}

model ChatArtifactSignal {
  `id`: string;
  `type`: string;
  `summary`?: string;
}

model AgentReferenceKeySignal {
  `workspaceId`: string;
  `type`: string;
  `id`: string;
}

model AgentReferenceWorkspaceSignal {
  `id`: string;
  `name`: string;
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
  `workspace`: AgentReferenceWorkspaceSignal;
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
  `workspaceId`: string;
  `dashboardId`: string;
  `dashboardTitle`: string;
  `pageId`: string;
  `pageTitle`: string;
  `modelId`: string;
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
  `assetList`?: WorkspaceAssetListSignal;
  `description`?: string;
  `environment`?: string;
  `kind`: RouteKind;
  `title`: string;
  `workspaceId`?: string;
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
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "visualSpatialWindowCommand" })
  `visualSpatialWindowCommand`: LeapViewVisualization.VisualizationSpatialWindowRequest;
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
  `fact`?: string;
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
  `readerEditable`: boolean;
  `urlParam`?: string;
  `urlEncoding`?: "typed_v1";
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
  `value`: DashboardFilterValue;
  `label`: string;
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
  `fact`?: string;
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
  `fact`?: string;
  `field`: string;
  `grain`?: string;
  `label`?: string;
  `value`: string;
}

model DashboardInteractionSelection {
  `entries`: DashboardInteractionSelectionEntry[];
  `id`: string;
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
  `fact`?: string;
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
  `workspaceId`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExplorerObjectSignal {
  `assetId`?: string;
  `columnCount`: int64;
  `columns`?: DataPreviewColumnSignal[];
  `description`?: string;
  `detailHref`?: string;
  `key`: string;
  `layer`: string;
  `modelId`?: string;
  `rowCountLabel`?: string;
  `source`?: string;
  `table`?: string;
  `title`: string;
  `workspaceId`: string;
  `workspaceTitle`?: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["data", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "data" })
model DataExplorerPageEnvelope {
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
  `selectedWorkspaceId`?: string;
  `tabs`: WorkspaceTabSignal[];
  `title`: string;
  `workspaceId`?: string;
  `workspaces`: DataExplorerWorkspaceSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExplorerSignal {
  `command`: DataExplorerCommand;
  `objects`: DataExplorerObjectSignal[];
  `preview`: DataPreviewSignal;
  `selectedKey`?: string;
  `selectedObject`?: DataExplorerObjectSignal;
  `selectedWorkspaceId`?: string;
  `warnings`?: string[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["data", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "data" })
model DataExplorerWorkspaceSignal {
  `active`: boolean;
  `href`: string;
  `id`: string;
  `objectCount`: int64;
  `title`: string;
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
  `key`: string;
  `label`: string;
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
  `fact`?: string;
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
  `fact`?: string;
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
  `workspaceId`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model SemanticModelGraphEdgeSignal {
  `cardinality`: string;
  `id`: string;
  `label`: string;
  `source`: string;
  `sourceField`: string;
  `target`: string;
  `targetField`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model SemanticModelGraphFieldSignal {
  `join`?: boolean;
  `label`?: string;
  `name`: string;
  `primaryKey`?: boolean;
  `relationships`?: string[];
  `type`?: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model SemanticModelGraphNodeSignal {
  `badges`?: string[];
  `description`?: string;
  `fields`: SemanticModelGraphFieldSignal[];
  `id`: string;
  `primaryKey`?: string;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model SemanticModelGraphSignal {
  `edges`: SemanticModelGraphEdgeSignal[];
  `facts`?: string[];
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
  `workspaceTitle`: string;
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

model WorkspaceAccessCommand {
  `bindingId`: string;
  `email`: string;
  `principalId`: string;
  `privilege`: string;
  `role`: string;
  `subjectId`: string;
  `subjectType`: string;
}

model WorkspaceAccessCandidate {
  `detail`: string;
  `label`: string;
  `subjectId`: string;
  `subjectType`: string;
}

model WorkspaceAccessSearchStatus {
  `error`: string;
  `loading`: boolean;
}

model WorkspaceAccessResponse {
  `bindings`: unknown[];
  `candidates`: WorkspaceAccessCandidate[];
  `canManage`: boolean;
  `mode`?: string;
  `objectId`?: string;
  `objectTitle`?: string;
  `objectType`?: string;
  `roles`: unknown[];
  `search`: string;
  `searchStatus`: WorkspaceAccessSearchStatus;
  `status`: WorkspaceAccessStatus;
  `workspace`: unknown;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceAccessSignal {
  `bindings`: unknown[];
  `candidates`: WorkspaceAccessCandidate[];
  `canManage`: boolean;
  `command`: WorkspaceAccessCommand;
  `mode`?: string;
  `objectId`?: string;
  `objectTitle`?: string;
  `objectType`?: string;
  `roles`: unknown[];
  `search`: string;
  `searchStatus`: WorkspaceAccessSearchStatus;
  `status`: WorkspaceAccessStatus;
  `workspace`: unknown;
}

model WorkspaceAccessStatus {
  `error`: string;
  `loading`: boolean;
  `message`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceActionSignal {
  `command`?: string;
  `disabled`?: boolean;
  `href`?: string;
  `icon`?: string;
  `label`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceAssetDetailsSignal {
  `overview`: DefinitionFactSignal[];
  `sections`: WorkspaceDetailSectionSignal[];
  `semanticModelGraph`?: SemanticModelGraphSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceAssetLineageSignal {
  `count`: int64;
  `graph`: AssetLineageGraphSignal;
  `usedByTable`: RecordTableSignal;
  `usesTable`: RecordTableSignal;
}

model WorkspaceAssetListSignal {
  `activeType`?: string;
  `assets`: WorkspaceAssetSummarySignal[];
  `empty`: string;
  `query`?: string;
  `searchHref`: string;
  `tabs`: WorkspaceTabSignal[];
  `workspaceId`?: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["workspace", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "workspace" })
model WorkspaceAssetPageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: WorkspaceAssetPageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceAssetPageSignal {
  `actions`?: WorkspaceActionSignal[];
  `activeSection`: string;
  `asset`: WorkspaceAssetSummarySignal;
  `assetId`: string;
  `breadcrumbs`: WorkspaceBreadcrumbSignal[];
  `details`?: WorkspaceAssetDetailsSignal;
  `environment`?: string;
  `kind`: RouteKind;
  `lineage`?: WorkspaceAssetLineageSignal;
  `refresh`?: WorkspaceAssetRefreshSignal;
  `tabs`: WorkspaceTabSignal[];
  `title`: string;
  `versions`?: WorkspaceAssetVersionsSignal;
  `workspaceId`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceAssetRefreshSignal {
  `lastSuccessful`: string;
  `running`: boolean;
  `runsTable`?: RecordTableSignal;
  `status`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceAssetSummarySignal {
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

model WorkspaceAssetVersionsSignal {
  `currentContentHash`: string;
  `table`: RecordTableSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceBreadcrumbSignal {
  `current`?: boolean;
  `href`?: string;
  `label`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceCatalogItemSignal {
  `description`: string;
  `href`: string;
  `id`: string;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceDetailSectionSignal {
  `code`?: string;
  `facts`?: DefinitionFactSignal[];
  `lang`?: string;
  `table`?: RecordTableSignal;
  `title`: string;
}

@apigen.contract(#{ kind: "ui-envelope", tags: #["workspace", "envelope"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "envelope", `x-leapview-surface`: "workspace" })
model WorkspacePageEnvelope {
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "chrome" })
  `chrome`: ChromeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "page" })
  `page`: WorkspacePageSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "runtime" })
  `runtime`: RouteRuntimeSignal;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "status" })
  `status`: DashboardStatus;
  @apigen.`metadata`(#{ `x-leapview-patch-mode`: "replace", `x-leapview-signal-key`: "workspaceAccess" })
  `workspaceAccess`?: WorkspaceAccessSignal;
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspacePageSignal {
	`assetList`?: WorkspaceAssetListSignal;
	`description`?: string;
	`environment`?: string;
	`kind`: RouteKind;
	`listFilter`?: string;
	`listQuery`?: string;
	`title`: string;
  `workspaceId`?: string;
  `workspaces`?: WorkspaceCatalogItemSignal[];
}

@apigen.contract(#{ kind: "ui-signal", tags: #["workspace", "signal"] })
@apigen.`metadata`(#{ `x-leapview-contract-role`: "signal", `x-leapview-surface`: "workspace" })
model WorkspaceTabSignal {
  `active`: boolean;
  `count`?: int64;
  `href`: string;
  `id`: string;
  `label`: string;
}
