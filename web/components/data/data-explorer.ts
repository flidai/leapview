import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronRight, Columns3, Database, Filter, Play, Plus, RotateCcw, Search, Sigma, Square, SquareCheckBig, X } from 'lucide'
import type {
  AgentReferenceSignal,
  DataExploreCommand,
  DataExploreDatasetSignal,
  DataExploreFieldSignal,
  DataExploreResultSignal,
  DataExploreSignal,
  DataExplorerCommand,
  DataExplorerDashboardSignal,
  DataExplorerObjectSignal,
  DataExplorerPageSignal,
  DataExplorerSignal,
  DataPreviewSignal,
  SavedExplorationStateSignal,
} from '../../generated/signals'
import type { ExplorationSpec } from '../../generated/exploration'
import type { VisualizationEnvelope, VisualizationWindowRequest } from '../../generated/visualization'
import type { OptimisticInteractionCommand } from '../dashboard/interaction-selection'
import { DatastarLit } from '../shared/datastar-lit'
import { domainEvents, emitDomainEvent } from '../shared/events'
import { agentIcon } from '../chat/agent-icon'
import { fieldTypeIcon } from '../shared/field-type-icon'
import { lucideIcon } from '../shared/lucide-icons'
import {
  DataExplorerAgentStateController,
  DataExplorerPanelController,
  DataExplorerQueryController,
  DataExplorerSelectionController,
  prepareExplorationRun,
  prepareExplorationStop,
  toggleVisibleColumns,
} from './data-explorer-controller'
import {
  datasetGrainLabel,
  emptyDataExploreCommand,
  exploreContextMatchesObject,
  fieldColumnID,
  fieldLabel,
  filterOperator,
  filterValues,
  localPreviewDimensions,
  makeExplorationFilter,
  objectDatasetID,
  explorationRunValidation,
  removeExplorationField,
  explorationSortsWithoutField,
  explorationSpecFor,
} from './data-explorer-spec'
import { dataExplorerURL } from './data-explorer-url'
import { exploreReturnLink } from './explore-return'
import { iconForLayer, label } from './data-explorer-object-labels'
import { DataExplorerClientState } from './data-explorer-client'
import { browserCommandFailure, ownsBrowserCommandFetch, type BrowserCommandFailure } from '../shared/command-failure'
import { filterObjects, objectColumnMatchesSearch } from './data-explorer-search'
import { groupObjectsBySemanticModel, type ResourceGroup } from './data-explorer-groups'
import {
  emptySavedExplorations,
  renderSavedExplorations,
  SavedExplorationTracker,
  type SavedExplorationCurrent,
  type SavedExplorationVisibility,
  savedExplorationStyles,
  synchronizeSavedExplorationURL,
  updateSavedExplorationURL,
} from './data-explorer-saved'
import '../chat/chat-drawer'
import './preview-table'
import './explore-table'
import './data-explorer-query-controls'
import './data-explorer-results'
import './data-explorer-dashboard-picker'
import { type ExplorationInteractionMode } from './data-explorer-drill'
import { handleDataExploreInteraction, handleDataExploreWindowRequest } from './data-explorer-window'
import { releaseDataExplorerTransport } from './data-explorer-transport'
import { dataExplorerResultStyles, renderExploreFailure } from './data-explorer-result-styles'

const emptyPreview: DataPreviewSignal = {
  columns: [],
  totalRows: 0,
  availableRows: 0,
  chunkSize: 100,
  rowHeight: 32,
  loading: false,
  stale: false,
  resetVersion: 0,
  blocks: {},
  totalRowLabel: 'Unknown',
  sort: {},
  sql: '',
  error: '',
}

const emptyExplorer: DataExplorerSignal = {
  objects: [],
  selectedKey: '',
  selectedObject: undefined,
  preview: emptyPreview,
  explore: {
    command: emptyDataExploreCommand,
    views: {}, recommendedView: 'table', defaultView: 'table',
    semanticModels: [], datasets: [], fields: [],
    result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] },
    status: { loading: false, stale: false, requestSeq: 0, state: 'idle' },
  },
  command: { mode: 'browse', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {}, explore: emptyDataExploreCommand },
  warnings: [],
}

type ExplorerColumn = { key: string, label?: string }

class DataExplorerPage extends DatastarLit(LitElement) {
  @property({ type: Boolean, reflect: true }) embedded = false
  @state() private search = ''
  @state() private filterField = ''
  @state() private filterOperator = 'equals'
  @state() private filterValue = ''
  @state() private optimisticExplore: DataExploreCommand | null = null
  @state() private agentDrawerOpen = false
  @state() private browserCollapsed = false
  @state() private browserWidth = 320
  @state() private exploreVisibleColumns: string[] = []
  @state() private exploreExecutionState: 'idle' | 'pending' | 'running' | 'stopped' = 'idle'
  @state() private exploreTransportFailure: BrowserCommandFailure | null = null
  @state() private exploreInteractionError = ''
  private exploreTransportAction: 'run' | 'stop' | null = null
  @state() private savedTitle = ''
  @state() private savedDuplicateTitle = ''
  @state() private savedVisibility: SavedExplorationVisibility = 'private'
  @state() private savedVisibilityOverride: SavedExplorationVisibility | undefined
  @state() private savedShareStatus = ''
  @state() private savedShareFallbackURL = ''
  private lastSearch = ''
  private expandedGroupIDs = new Set<string>()
  private exploreTimer = 0
  private filterSuggestionTimer = 0
  private latestExploreRequestSeq = 0
  private agentStateInitialized = false
  private agentRestoreDispatched = false
  private restoredAgentConversationId = ''
  private initialURLCanonicalized = false
  private browserResizeCleanup?: () => void
  private readonly agentStateController = new DataExplorerAgentStateController()
  private readonly panelController = new DataExplorerPanelController()
  private readonly queryController = new DataExplorerQueryController()
  private readonly selectionController = new DataExplorerSelectionController()
  private readonly clientState = new DataExplorerClientState()
  private readonly savedExplorationTracker = new SavedExplorationTracker({
    onBaselineChanged: (current) => {
      this.savedDuplicateTitle = ''
      this.savedVisibilityOverride = current ? (current.visibility === 'organization' ? 'organization' : 'private') : undefined
      this.savedShareStatus = ''
      this.savedShareFallbackURL = ''
    },
    onDirty: () => this.dispatchEvent(new CustomEvent('lv-saved-exploration-dirty', { bubbles: true, composed: true })),
  })

  static styles = [dataExplorerResultStyles, css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      color: var(--lv-fg-default);
      background: var(--lv-bg-app);
      font-family: var(--fontStack-system);
    }

    :host([embedded]) {
      height: 100%;
      min-height: 0;
    }

    :host([embedded]) .route {
      height: 100%;
      min-height: 32rem;
      grid-template-rows: minmax(0, 1fr);
    }

    :host([embedded]) .header {
      display: none;
    }

    :host([embedded]) .route:not(.semantic) .explorer {
      grid-template-columns: minmax(0, 1fr);
    }

    :host([embedded]) .route:not(.semantic) .browser,
    :host([embedded]) .route:not(.semantic) .browser-resizer {
      display: none;
    }

    .route {
      display: grid;
      height: 100svh;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
      overflow: hidden;
    }

    .route.agent-open {
      grid-template-columns: minmax(0, 1fr) minmax(20rem, 28rem);
    }

    .route.agent-open > .header,
    .route.agent-open > .explorer {
      grid-column: 1;
    }

    lv-chat-drawer {
      grid-row: 1 / -1;
      grid-column: 2;
      min-height: 0;
    }

    .header {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-12);
      align-items: center;
      box-sizing: border-box;
      border-bottom: var(--lv-border-muted);
      min-height: 3.25rem;
      padding: var(--base-size-8) var(--base-size-12);
      background: var(--lv-bg-app);
    }

    .header-actions,
    .query-actions,
    .selection-shelf,
    .filter-pills {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
    }

    .return-link {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-decoration: none;
      white-space: nowrap;
    }

    .return-link:hover,
    .return-link:focus-visible {
      color: var(--lv-fg-default);
      text-decoration: underline;
    }

    .mode-switch {
      display: inline-flex;
      align-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: var(--base-size-2);
    }

    .mode-switch-button {
      min-height: calc(var(--control-small-size) - var(--base-size-4));
      border: 0;
      border-radius: calc(var(--lv-radius-default) - 1px);
      background: transparent;
      color: var(--lv-fg-muted);
      padding: 0 var(--base-size-8);
      cursor: pointer;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    .mode-switch-button[aria-pressed="true"] {
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
    }

    .mode-switch-button:hover,
    .mode-switch-button:focus-visible {
      color: var(--lv-fg-default);
      outline: 0;
    }

    .header-columns {
      position: relative;
    }

    .header-columns summary {
      display: flex;
      height: var(--control-small-size);
      align-items: center;
      gap: var(--base-size-6);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8);
      cursor: pointer;
      list-style: none;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      text-transform: none;
    }

    .header-columns summary::-webkit-details-marker {
      display: none;
    }

    .header-columns summary:hover,
    .header-columns summary:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .header-column-menu {
      position: absolute;
      top: calc(100% + var(--base-size-4));
      right: 0;
      z-index: var(--zIndex-overlay);
      display: grid;
      min-width: 14rem;
      max-height: 22rem;
      gap: var(--base-size-4);
      overflow: auto;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
      padding: var(--base-size-8);
    }

    .header-column-menu label {
      display: flex;
      min-height: var(--control-xsmall-size);
      align-items: center;
      gap: var(--base-size-8);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-caption);
    }

    .text-button,
    .chip,
    .field-button,
    .field-action {
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      font: inherit;
      cursor: pointer;
    }

    .text-button {
      min-height: var(--control-medium-size);
      padding: 0 var(--base-size-12);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    h1,
    h2,
    h3,
    p {
      margin: 0;
    }

    h1 {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
    }

    .explorer {
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-columns: auto 4px minmax(0, 1fr);
      overflow: hidden;
    }

    .browser,
    .main {
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    .browser {
      display: grid;
      grid-template-rows: auto minmax(0, 1fr);
      background: var(--lv-bg-app);
    }

    .browser-tools {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-6);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-6) var(--base-size-8);
    }

    .sidebar-toggle {
      display: grid;
      width: var(--control-small-size);
      height: var(--control-small-size);
      flex: none;
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
    }

    .sidebar-toggle:hover,
    .sidebar-toggle:focus-visible {
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .browser-resizer {
      position: relative;
      min-width: 4px;
      border-right: var(--lv-border-muted);
      cursor: col-resize;
      touch-action: none;
    }

    .browser-resizer::after {
      position: absolute;
      inset-block: 0;
      left: 1px;
      width: 2px;
      background: transparent;
      content: '';
    }

    .browser-resizer:hover::after,
    .browser-resizer:focus-visible::after {
      background: var(--lv-fg-link);
    }

    .browser-resizer:focus-visible {
      outline: 0;
    }

    .browser-collapsed .browser {
      grid-template-rows: max-content;
      align-content: start;
    }

    .explorer.browser-collapsed {
      grid-template-columns: auto minmax(0, 1fr);
    }

    .browser-collapsed .browser-tools {
      justify-content: start;
      padding-inline: var(--base-size-8);
    }

    .browser-collapsed .browser-resizer {
      display: none;
    }

    .explore-browser {
      grid-template-rows: auto auto minmax(0, 1fr);
    }

    .selectors {
      display: grid;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-12);
    }

    .selectors label {
      display: grid;
      gap: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    select {
      min-width: 0;
      height: var(--control-medium-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8);
      font: var(--lv-type-body);
    }

    .field-groups {
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-8);
    }

    .field-group {
      margin-bottom: var(--base-size-8);
    }

    .field-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      border-radius: var(--lv-radius-default);
    }

    .field-row:hover,
    .field-row:focus-within {
      background: var(--lv-bg-control-hover);
    }

    .field-button {
      display: grid;
      min-width: 0;
      grid-template-columns: 1rem minmax(0, 1fr);
      gap: var(--base-size-8);
      align-items: center;
      padding: var(--base-size-8);
      text-align: left;
    }

    .field-button strong,
    .field-button small {
      display: block;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-button strong {
      font: var(--lv-type-body);
    }

    .field-button small {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .field-button.is-selected strong {
      color: var(--lv-fg-accent);
    }

    .field-action {
      display: grid;
      width: var(--control-small-size);
      height: var(--control-small-size);
      place-items: center;
      color: var(--lv-fg-muted);
    }

    .search {
      position: relative;
      min-width: 0;
      flex: 1;
    }

    .search input {
      width: 100%;
      min-width: 0;
      height: var(--control-small-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8) 0 var(--base-size-32);
      font: var(--lv-type-body);
    }

    .search-icon {
      position: absolute;
      left: var(--base-size-8);
      top: 50%;
      display: grid;
      color: var(--lv-fg-muted);
      transform: translateY(-50%);
    }

    .tree {
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-6);
    }

    details {
      min-width: 0;
    }

    summary {
      display: grid;
      grid-template-columns: 1rem 1rem minmax(0, 1fr);
      gap: var(--base-size-6);
      align-items: center;
      border-radius: var(--lv-radius-default);
      min-height: var(--control-small-size);
      padding: var(--base-size-4) var(--base-size-6);
      color: var(--lv-fg-muted);
      cursor: pointer;
      list-style: none;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    summary::-webkit-details-marker {
      display: none;
    }

    details[open] > summary .chevron {
      transform: rotate(90deg);
    }

    .object-list {
      display: grid;
      gap: var(--base-size-2);
      padding: var(--base-size-2) 0 var(--base-size-8) var(--base-size-16);
    }

    .object-node > summary {
      display: grid;
      grid-template-columns: 1rem 1rem minmax(0, 1fr);
      gap: var(--base-size-6);
      min-height: var(--control-small-size);
      padding: var(--base-size-4) var(--base-size-6);
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      text-transform: none;
    }

    .object-expand {
      display: grid;
      width: 1.5rem;
      height: 1.5rem;
      place-items: center;
      margin: calc((1.5rem - 1rem) / -2);
      border-radius: var(--lv-radius-default);
      cursor: pointer;
    }

    .object-expand:hover {
      background: var(--lv-bg-control-hover);
    }

    .object-node[open] > summary .chevron {
      transform: rotate(90deg);
    }

    .object-button {
      display: grid;
      min-width: 0;
      width: 100%;
      grid-template-columns: 1rem 1rem minmax(0, 1fr);
      gap: var(--base-size-6);
      align-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      min-height: var(--control-small-size);
      padding: var(--base-size-4) var(--base-size-6);
      text-align: left;
      cursor: pointer;
      font: inherit;
    }

    .object-button:hover,
    .object-button:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .object-button.is-selected {
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
    }

    .object-button strong {
      display: block;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .object-button strong {
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .object-label {
      min-width: 0;
    }

    .object-label small {
      display: block;
      overflow: hidden;
      color: var(--lv-fg-muted);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-normal);
    }

    .object-button.is-selected .object-label small {
      color: var(--lv-fg-accent);
    }

    .column-list {
      display: grid;
      gap: var(--base-size-2);
      padding: var(--base-size-2) 0 var(--base-size-8) var(--base-size-32);
    }

    .column-item {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      min-height: var(--control-small-size);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .column-item:hover,
    .column-item:focus-within {
      background: var(--lv-bg-control-hover);
    }

    .column-item.is-unavailable {
      opacity: 0.58;
    }

    .column-item.is-unavailable:hover,
    .column-item.is-unavailable:focus-within {
      background: transparent;
    }

    .column-item .field-button {
      display: grid;
      min-width: 0;
      grid-template-columns: 1rem 1rem minmax(0, 1fr) auto;
      gap: var(--base-size-6);
      align-items: center;
      padding: var(--base-size-4) var(--base-size-8);
      text-align: left;
    }

    .column-item .field-button > span:nth-child(3) {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .column-item .field-button.is-selected > span:nth-child(3) {
      color: var(--lv-fg-accent);
    }

    .column-item .field-button:disabled {
      cursor: not-allowed;
    }

    .field-check {
      display: grid;
      place-items: center;
      color: var(--lv-fg-muted);
    }

    .field-button.is-selected .field-check {
      color: var(--lv-fg-accent);
    }

    .metric-field code {
      color: var(--lv-fg-accent);
    }

    .column-item code {
      overflow: hidden;
      color: var(--lv-fg-muted);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
    }

    .main {
      display: grid;
      grid-template-rows: minmax(0, 1fr);
      background: var(--lv-bg-app);
    }

    .explore-main {
      grid-template-rows: auto auto minmax(0, 1fr) auto;
    }

    .query-row {
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      gap: var(--base-size-8);
      align-items: start;
    }

    .query-label {
      min-width: 5rem;
      padding-top: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .selection-shelf,
    .filter-pills {
      min-width: 0;
      flex-wrap: wrap;
    }

    .chip {
      display: inline-flex;
      max-width: 18rem;
      align-items: center;
      gap: var(--base-size-4);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-control);
      padding: var(--base-size-4) var(--base-size-8);
      font: var(--lv-type-caption);
    }

    .chip.metric {
      border-color: var(--lv-line-accent, var(--lv-line-muted));
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
    }

    .icon-button {
      display: inline-grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      cursor: pointer;
    }

    .icon-button:hover,
    .icon-button:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .ask-button {
      display: inline-flex;
      width: auto;
      gap: var(--base-size-6);
      padding: 0 var(--base-size-12);
    }

    .header .icon-button {
      width: var(--control-small-size);
      height: var(--control-small-size);
    }

    .header .ask-button {
      width: auto;
    }

    ${savedExplorationStyles}

    .content {
      display: grid;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    lv-data-preview-table {
      min-height: 0;
    }

    .schema-view {
      min-width: 0;
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-16);
    }

    .metadata-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr));
      gap: var(--base-size-12);
      margin-bottom: var(--base-size-16);
    }

    .metadata-card {
      min-width: 0;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-12);
    }

    .metadata-card dt {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .metadata-card dd {
      margin: var(--base-size-4) 0 0;
      overflow-wrap: anywhere;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
    }

    .schema-table {
      width: 100%;
      border-collapse: collapse;
      border: var(--lv-border-muted);
      background: var(--lv-bg-panel);
      font: var(--lv-type-body);
    }

    .schema-table th,
    .schema-table td {
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-12);
      text-align: left;
      vertical-align: top;
    }

    .schema-table th {
      position: sticky;
      top: 0;
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .schema-table code {
      color: var(--lv-fg-default);
      font: var(--lv-type-code-inline);
    }

    .schema-muted {
      color: var(--lv-fg-muted);
    }

    pre {
      margin: 0;
      white-space: pre-wrap;
      word-break: break-word;
      color: var(--lv-fg-muted);
      font: var(--lv-type-code-block);
    }

    .empty {
      color: var(--lv-fg-muted);
      padding: var(--base-size-16);
      font: var(--lv-type-body);
    }

    @media (max-width: 760px) {
      .route {
        height: auto;
        min-height: 100svh;
        overflow: visible;
      }

      .explorer {
        grid-template-columns: 1fr;
      }

      .browser-resizer {
        display: none;
      }

      .explorer.browser-collapsed {
        grid-template-columns: 1fr;
      }

      .browser,
      .main {
        min-height: 22rem;
      }

      .browser {
        width: auto !important;
      }

    }
  `]

  private readonly handleDatastarFetch = (event: Event) => {
    const actionInFlight = this.exploreTransportAction
    const executionActive = this.exploreExecutionState === 'running' || this.exploreExecutionState === 'pending'
    if ((!executionActive && !actionInFlight) || !ownsBrowserCommandFetch(this, event)) return
    const failure = browserCommandFailure(event, actionInFlight === 'stop' ? 'Exploration stop' : 'Exploration run')
    if (!failure) return
    this.exploreExecutionState = 'idle'
    this.exploreTransportAction = null
    this.exploreTransportFailure = failure
    this.requestUpdate()
  }

  connectedCallback(): void {
    if (!this.agentStateInitialized) {
      const stored = this.agentStateController.initialize()
      this.agentDrawerOpen = stored.open
      this.restoredAgentConversationId = stored.conversationId
      this.agentStateInitialized = true
    }
    if (typeof window !== 'undefined') window.addEventListener('popstate', this.handleHistoryPopState)
    if (typeof document !== 'undefined') document.addEventListener('datastar-fetch', this.handleDatastarFetch)
    super.connectedCallback()
  }

  disconnectedCallback(): void {
    window.clearTimeout(this.exploreTimer)
    window.clearTimeout(this.filterSuggestionTimer)
    releaseDataExplorerTransport(this.clientState.clientID(this.dataExplorer.command?.clientId))
    this.browserResizeCleanup?.()
    if (typeof window !== 'undefined') window.removeEventListener('popstate', this.handleHistoryPopState)
    if (typeof document !== 'undefined') document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    super.disconnectedCallback()
  }

  updated(): void {
    this.canonicalizeInitialURL()
    const observedExploreRequestSeq = this.dataExplorer.explore?.command?.requestSeq ?? 0
    if (observedExploreRequestSeq > this.latestExploreRequestSeq) this.latestExploreRequestSeq = observedExploreRequestSeq
    const selectedKey = this.dataExplorer.selectedKey ?? ''
    if (this.selectionController.observe(selectedKey)) {
      requestAnimationFrame(() => {
        this.renderRoot.querySelector<HTMLElement>('.object-button.is-selected')?.scrollIntoView({ block: 'nearest' })
      })
    }
    const search = this.search.trim().toLowerCase()
    if (search !== this.lastSearch) {
      this.lastSearch = search
      if (search) {
        requestAnimationFrame(() => {
          for (const node of this.renderRoot.querySelectorAll<HTMLDetailsElement>('.object-node[data-column-match="true"]')) {
            node.open = true
          }
        })
      }
    }
    if (this.optimisticExplore && (this.dataExplorer.explore?.command?.requestSeq ?? 0) >= this.optimisticExplore.requestSeq) {
      this.optimisticExplore = null
      this.exploreTransportAction = null
      if (this.exploreExecutionState !== 'stopped') this.exploreExecutionState = 'idle'
      if (!this.embedded) updateSavedExplorationURL(this.dataExplorer.command, 'replace', this.savedExplorations)
    }
    const agent = this.signal<{ activeConversationId?: string } | null>('agent', null)
    const activeConversationId = agent?.activeConversationId?.trim() ?? ''
    if (activeConversationId) {
      this.restoredAgentConversationId = activeConversationId
      this.agentStateController.syncConversation(activeConversationId)
      this.persistAgentState()
    }
    if (agent && this.restoredAgentConversationId && !this.agentRestoreDispatched) {
      this.agentRestoreDispatched = true
      emitDomainEvent(this, domainEvents.chatRestore, { conversationId: this.restoredAgentConversationId })
    }
    const saved = this.savedExplorations
    // A saved-state patch can arrive while a query command is still in flight.
    // Keep the optimistic URL until the matching explorer response is observed;
    // otherwise a metadata-only save update can revert the authored query.
    if (!this.optimisticExplore) {
      synchronizeSavedExplorationURL(this.dataExplorer.command, saved, this.embedded, Object.prototype.hasOwnProperty.call(this.signals, 'savedExplorations'))
    }
    this.savedExplorationTracker.observe(saved, this.optimisticExplore?.spec ?? this.dataExplorer.explore?.command?.spec)
  }

  get page(): DataExplorerPageSignal | null {
    return this.signal<DataExplorerPageSignal | null>('page', null)
  }

  get dataExplorer(): DataExplorerSignal {
    return this.signal<DataExplorerSignal>('dataExplorer', emptyExplorer)
  }

  get savedExplorations(): SavedExplorationStateSignal {
    return this.signal<SavedExplorationStateSignal>('savedExplorations', emptySavedExplorations)
  }

  get dashboardAuthoring(): DataExplorerDashboardSignal {
    return this.signal<DataExplorerDashboardSignal>('dataExplorerDashboard', { enabled: false, targets: [] })
  }

  render() {
    const page = this.page
    const explorer = this.dataExplorer ?? emptyExplorer
    const selected = explorer.selectedObject
    const semanticActive = explorer.command?.mode === 'explore' || this.optimisticExplore !== null
    const filtered = filterObjects(explorer.objects ?? [], this.search)
    const grouped = groupObjectsBySemanticModel(filtered, explorer.explore?.semanticModels ?? [])
    const agentEnabled = this.signal<unknown | null>('agent', null) !== null
    // Governed result tables expose their own shared table column chooser.
    // Keep the route-level chooser for raw browse previews only; showing both
    // would leave a second control that cannot change the IR envelope.
    const columns = semanticActive ? [] : this.headerColumns(explorer, false)
    const visibleColumnKeys = this.headerVisibleColumnKeys(explorer, columns, semanticActive)
    const savedExplorations = this.savedExplorations
    const returnLink = exploreReturnLink()
    return html`
      <section class=${`route${semanticActive ? ' semantic' : ''}${agentEnabled && this.agentDrawerOpen ? ' agent-open' : ''}`} aria-label="Data Explorer">
        <header class="header">
          <h1>${page?.title ?? 'Data Explorer'}</h1>
          <div class="header-actions">
            ${returnLink ? html`<a class="return-link" href=${returnLink.href}>${returnLink.label}</a>` : nothing}
            <div class="mode-switch" role="group" aria-label="Data view">
              <button type="button" class="mode-switch-button mode-button" aria-pressed=${String(!semanticActive)} @click=${() => this.setMode('browse', selected)}>Rows</button>
              <button type="button" class="mode-switch-button mode-button" aria-pressed=${String(semanticActive)} @click=${() => this.setMode('explore', selected)}>Analyze</button>
            </div>
            ${columns.length ? html`
              <details class="header-columns">
                <summary title="Choose visible columns" aria-label="Choose visible columns">
                  ${lucideIcon(Columns3, { size: 15 })}<span>Columns</span><span aria-hidden="true">${visibleColumnKeys.length}/${columns.length}</span>
                </summary>
                <div class="header-column-menu">
                  ${columns.map((column) => {
                    const checked = visibleColumnKeys.includes(column.key)
                    return html`
                      <label>
                        <input
                          type="checkbox"
                          aria-label=${column.label || column.key}
                          .checked=${checked}
                          ?disabled=${checked && visibleColumnKeys.length <= 1}
                          @change=${(event: Event) => this.toggleHeaderColumn(column.key, (event.target as HTMLInputElement).checked, columns, semanticActive)}
                        />
                        ${column.label || column.key}
                      </label>
                    `
                  })}
                </div>
              </details>
            ` : nothing}
            ${agentEnabled ? html`<button type="button" class="icon-button ask-button" aria-label="Ask about this data" aria-expanded=${String(this.agentDrawerOpen)} title="Ask about this data" @click=${() => this.setAgentDrawerOpen(!this.agentDrawerOpen)}>${agentIcon()}<span>Ask</span></button>` : nothing}
          </div>
        </header>
        ${renderSavedExplorations(savedExplorations, {
          savedTitle: () => this.savedTitle,
          savedDuplicateTitle: () => this.savedDuplicateTitle,
          savedVisibility: () => this.savedVisibility,
          currentSavedVisibility: (current: SavedExplorationCurrent) => this.savedVisibilityOverride ?? (current.visibility === 'organization' ? 'organization' : 'private'),
          activeSpec: () => this.optimisticExplore?.spec ?? this.dataExplorer.explore?.command?.spec ?? emptyExplorer.explore.command.spec,
          onSavedTitleInput: (value) => this.savedTitle = value,
          onDuplicateTitleInput: (value) => this.savedDuplicateTitle = value,
          onSavedVisibilityInput: (value) => this.savedVisibility = value,
          onCurrentSavedVisibilityInput: (value) => this.savedVisibilityOverride = value,
          shareStatus: () => this.savedShareStatus,
          shareFallbackURL: () => this.savedShareFallbackURL,
          onShareStatus: (message, fallbackURL = '') => {
            this.savedShareStatus = message
            this.savedShareFallbackURL = fallbackURL
          },
          onCommand: (command) => this.dispatchEvent(new CustomEvent('lv-saved-exploration-command', { bubbles: true, composed: true, detail: command })),
          onReopen: (current) => this.dispatchEvent(new CustomEvent('lv-saved-exploration-reopen', {
            bubbles: true, composed: true, detail: { explorationId: current.id, includeArchived: current.status === 'archived' },
          })),
        })}
        <div
          class=${`explorer${this.browserCollapsed ? ' browser-collapsed' : ''}`}
        >
          <aside class="browser" aria-label="Data objects" style=${`width:${this.browserCollapsed ? 44 : this.browserWidth}px`}>
            <div class="browser-tools">
              <button
                type="button"
                class="sidebar-toggle"
                aria-label=${this.browserCollapsed ? 'Open data browser' : 'Close data browser'}
                aria-expanded=${String(!this.browserCollapsed)}
                title=${this.browserCollapsed ? 'Open data browser' : 'Close data browser'}
                @click=${() => {
                  this.browserCollapsed = this.panelController.toggleBrowser().browserCollapsed
                }}
              >${lucideIcon(Database, { size: 16 })}</button>
              ${this.browserCollapsed ? nothing : html`
                <label class="search">
                  <span class="search-icon" aria-hidden="true">${lucideIcon(Search, { size: 15 })}</span>
                  <input
                    type="search"
                    aria-label="Search data"
                    .value=${this.search}
                    @input=${(event: Event) => this.search = (event.target as HTMLInputElement).value}
                    placeholder="Search data"
                    autocomplete="off"
                  />
                </label>
              `}
            </div>
            ${this.browserCollapsed ? nothing : html`
              <div class="tree">
                ${filtered.length
                  ? html`
                    ${this.renderResourceGroups(grouped, explorer.selectedKey ?? '', explorer.explore ?? emptyExplorer.explore, semanticActive)}
                  `
                  : html`<p class="empty">No data objects match this search.</p>`}
              </div>
            `}
          </aside>
          <div
            class="browser-resizer"
            role="separator"
            aria-label="Resize data browser"
            aria-orientation="vertical"
            aria-valuemin="280"
            aria-valuemax="440"
            aria-valuenow=${String(this.browserWidth)}
            tabindex="0"
            @pointerdown=${this.beginBrowserResize}
            @keydown=${this.resizeBrowserFromKeyboard}
          ></div>
          <main class="main" aria-label="Data results">
            ${selected
              ? semanticActive
                ? this.renderExploreSelected(selected, explorer.explore ?? emptyExplorer.explore)
                : this.renderSelected(selected, explorer.preview ?? emptyPreview, explorer.command ?? emptyExplorer.command)
              : html`<p class="empty">${(explorer.objects ?? []).length
                ? 'Select a data object to begin.'
                : 'No data objects are available.'}</p>`}
          </main>
        </div>
        ${agentEnabled && this.agentDrawerOpen ? html`<lv-chat-drawer
          open
          .suggestions=${this.agentSuggestions(explorer)}
          @lv-chat-drawer-close=${() => this.setAgentDrawerOpen(false)}
          @lv-chat-new=${this.handleAgentNew}
        ></lv-chat-drawer>` : nothing}
      </section>
    `
  }

  private renderQueryChip(id: string, kind: 'dimension' | 'metric', fields: DataExploreFieldSignal[], command: DataExploreCommand) {
    return html`<button type="button" class=${`chip ${kind}`} title="Remove field" @click=${() => this.removeExploreField(id, kind, command)}>
      ${fieldLabel(id, fields)} ${lucideIcon(X, { size: 12 })}
    </button>`
  }

  private renderExecutionState(command: DataExploreCommand, result: DataExploreSignal['result'], status?: DataExploreSignal['status'], rawError?: string) {
    const expected = Math.max(command.requestSeq ?? 0, this.latestExploreRequestSeq)
    const actual = Math.max(result.requestSeq ?? 0, status?.requestSeq ?? 0)
    if (status?.state === 'cancelled' || this.exploreExecutionState === 'stopped') {
      return html`<span class="execution-state" role="status">Run stopped; draft is preserved</span>`
    }
    if (rawError || result.error || status?.error || status?.state === 'error') return html`<span class="execution-state" data-state="error" role="status">${status?.error || rawError || 'Query failed'}</span>`
    if (status?.loading || this.exploreExecutionState === 'pending' || this.exploreExecutionState === 'running') {
      const progress = status?.progressPercent === undefined ? '' : ` ${Math.round(status.progressPercent)}%`
      return html`<span class="execution-state" role="status">${status?.message || (this.exploreExecutionState === 'pending' ? 'Waiting to run…' : 'Running exploration…')}${progress}</span>`
    }
    if (status?.stale || status?.state === 'stale' || expected > actual) {
      return html`<span class="execution-state" data-state="stale" role="status">Results are stale — run to refresh</span>`
    }
    return nothing
  }

  private handleExploreSpecChange(event: CustomEvent<ExplorationSpec>, command: DataExploreCommand): void {
    this.emitExplore(event.detail, this.optimisticExplore ?? command)
  }

  private handleExploreFilterOpen(event: CustomEvent<string>, command: DataExploreCommand): void {
    const field = this.dataExplorer.explore.fields.find((candidate) => candidate.id === event.detail)
    if (!field || field.kind !== 'dimension' || field.compatible === false) return
    this.openFilter(field)
    this.requestFilterSuggestions(this.optimisticExplore ?? command, '')
  }

  private handleExploreFilterChange(
    event: CustomEvent<{ action: 'apply' | 'cancel' | 'operator' | 'value'; operator?: string; value?: string }>,
    command: DataExploreCommand,
  ): void {
    const detail = event.detail
    if (detail.action === 'cancel') {
      this.closeFilter()
      return
    }
    if (detail.action === 'operator') {
      this.filterOperator = this.panelController.setFilterOperator(detail.operator ?? 'equals').filterOperator
      return
    }
    if (detail.action === 'value') {
      this.filterValue = this.panelController.setFilterValue(detail.value ?? '').filterValue
      this.requestFilterSuggestions(this.optimisticExplore ?? command, this.filterValue)
      return
    }
    this.applyExploreFilter(this.optimisticExplore ?? command)
  }

  private setMode(mode: 'browse' | 'explore', selectedObject?: DataExplorerObjectSignal) {
    const current = this.dataExplorer?.command ?? emptyExplorer.command
    if (current.mode === mode) return
    if (mode === 'browse') this.latestExploreRequestSeq = 0
    if (mode === 'explore') {
      const currentExplore = this.optimisticExplore ?? current.explore ?? this.dataExplorer.explore.command
      const currentSpec = explorationSpecFor(currentExplore)
      if (currentSpec.modelId || !selectedObject) {
        const explore = { ...currentExplore, action: 'configure' as const }
        this.emitCommand({ mode, action: 'configure', explore })
        return
      }
      const datasetID = objectDatasetID(selectedObject)
      const dimensions = localPreviewDimensions(selectedObject, this.dataExplorer.explore.fields ?? [])
      const explore: DataExploreCommand = {
        ...currentExplore,
        spec: {
          ...currentSpec,
          modelId: selectedObject.semanticModelId ?? '',
          datasetId: datasetID,
          dimensions: dimensions.map((field) => ({ field })),
          metrics: [],
          filters: [],
          sort: [],
        },
        columnWidths: {},
      }
      this.emitCommand({ mode, action: 'configure', explore: { ...explore, action: 'configure' } })
      return
    }
    const baseExplore = this.optimisticExplore ?? current.explore ?? this.dataExplorer.explore.command
    const explore = { ...baseExplore, action: undefined }
    this.emitCommand({ mode, action: undefined, explore })
    this.optimisticExplore = null
    this.exploreExecutionState = 'idle'
    this.exploreTransportFailure = null
  }

  private toggleUnifiedField(
    field: DataExploreFieldSignal,
    object: DataExplorerObjectSignal,
    explore: DataExploreSignal,
    semanticActive: boolean,
  ) {
    if (field.compatible === false && !field.rebaseDatasetId) return
    const selected = this.dataExplorer.selectedObject
    const baseObject = selected && selected.semanticModelId === object.semanticModelId
      ? selected
      : object
    const current = this.optimisticExplore ?? explore.command ?? emptyExplorer.explore.command
    const contextMatches = exploreContextMatchesObject(current, baseObject)
    const activeCommand = semanticActive && contextMatches
    const baseDimensions = (explore.fields ?? [])
      .filter((candidate) => candidate.compatible !== false && candidate.kind !== 'metric' && candidate.datasetId === objectDatasetID(baseObject))
      .map((candidate) => candidate.id)
    const fallbackDimensions = (baseObject.columns ?? []).map((column) => `${objectDatasetID(baseObject)}.${column.key}`)
    const command: DataExploreCommand = activeCommand ? current : {
      ...current,
      spec: {
        ...explorationSpecFor(current),
        modelId: baseObject.semanticModelId ?? '',
        datasetId: objectDatasetID(baseObject),
        dimensions: baseDimensions.length ? baseDimensions.map((field) => ({ field })) : fallbackDimensions.map((field) => ({ field })),
        metrics: [],
        filters: [],
        sort: [],
      },
      columnWidths: {},
    }
    const key = field.kind === 'metric' ? 'metrics' : 'dimensions'
    const spec = explorationSpecFor(command)
    const selectedByDefault = !activeCommand && field.kind !== 'metric' && field.datasetId === objectDatasetID(baseObject)
    const values = spec[key] ?? []
    const selectedNow = values.some((value) => value.field === field.id) || selectedByDefault
    const next = selectedNow ? values.filter((value) => value.field !== field.id) : [...values, { field: field.id }]
    this.emitExplore({ ...spec, [key]: next, sort: explorationSortsWithoutField(spec, field.id) }, command)
  }

  private removeExploreField(id: string, kind: 'dimension' | 'metric', command: DataExploreCommand) {
    this.emitExplore(removeExplorationField(explorationSpecFor(command), id, kind), command)
  }

  private resetExplore(command: DataExploreCommand) {
    this.closeFilter()
    const current = this.optimisticExplore ?? command
    this.emitExplore(
      { ...explorationSpecFor(current), dimensions: [], metrics: [], filters: [], sort: [], time: undefined },
      { ...current, columnWidths: {} },
      undefined,
      true,
    )
  }

  private openFilter(field: DataExploreFieldSignal) {
    window.clearTimeout(this.filterSuggestionTimer)
    this.clientState.invalidateSuggestions()
    const state = this.panelController.openFilter(field.id)
    this.filterField = state.filterField
    this.filterOperator = state.filterOperator
    this.filterValue = state.filterValue
  }

  private closeFilter() {
    window.clearTimeout(this.filterSuggestionTimer)
    this.clientState.invalidateSuggestions()
    const state = this.panelController.closeFilter()
    this.filterField = state.filterField
    this.filterOperator = state.filterOperator
    this.filterValue = state.filterValue
  }

  private applyExploreFilter(command: DataExploreCommand) {
    if (!this.filterField) return
    const needsValue = this.filterOperator !== 'is_null' && this.filterOperator !== 'is_not_null'
    const values = needsValue
      ? this.filterValue.split(',').map((value) => value.trim()).filter(Boolean)
      : []
    if (needsValue && !values.length) return
    const field = this.dataExplorer.explore.fields.find((candidate) => candidate.id === this.filterField)
    const spec = explorationSpecFor(command)
    const hasMultiRootMetric = spec.metrics.some((metric) => {
      const metricField = this.dataExplorer.explore.fields.find((candidate) => candidate.id === metric.field)
      return metricField?.kind === 'metric' && !metricField.datasetId.trim()
    })
    const filterScope = hasMultiRootMetric ? undefined : spec.datasetId
    const filter = makeExplorationFilter(field ?? this.filterField, this.filterOperator, values, field?.type, filterScope)
    if (!filter) return
    this.closeFilter()
    this.emitExplore({ ...spec, filters: [...spec.filters.filter((current) => current.field !== filter.field), filter] }, command)
  }

  private removeExploreFilter(index: number, command: DataExploreCommand) { const spec = explorationSpecFor(command)
    this.emitExplore({ ...spec, filters: spec.filters.filter((_, current) => current !== index) }, command)
  }

  private emitExplore(
    next: Partial<ExplorationSpec>,
    baseCommand?: DataExploreCommand,
    filterSuggestions?: NonNullable<DataExploreCommand['filterSuggestions']>,
    immediate = false,
  ) {
    window.clearTimeout(this.exploreTimer)
    const current = baseCommand ?? this.optimisticExplore ?? this.dataExplorer.explore.command ?? emptyExplorer.explore.command
    const command = this.queryController.explore(current, next, immediate)
    command.action = 'configure'
    if (filterSuggestions) command.filterSuggestions = filterSuggestions
    else delete command.filterSuggestions
    this.optimisticExplore = command; this.exploreTransportFailure = null
    // A configure event only updates the authored draft. Keep Run available
    // until the user explicitly starts execution; the server may receive this
    // command after the debounce window, but it is not a query run.
    this.exploreExecutionState = 'idle'
    if (!this.embedded && !filterSuggestions && command.spec.modelId.trim()) {
      updateSavedExplorationURL({ ...this.dataExplorer.command, mode: 'explore', explore: command }, 'replace', this.savedExplorations)
    }
    const dispatch = () => this.emitCommand({ action: 'configure', mode: 'explore', explore: command })
    if (immediate) dispatch()
    else this.exploreTimer = window.setTimeout(dispatch, 320)
  }

  private requestFilterSuggestions(command: DataExploreCommand, search: string): void {
    if (!this.filterField) return
    window.clearTimeout(this.filterSuggestionTimer)
    const requestSeq = this.nextFilterSuggestionRequestSeq()
    const suggestionCommand: DataExploreCommand = {
      ...command,
      action: 'configure',
      // Suggestions are a read-only side lane. Keep the authored query's
      // request sequence unchanged so typing never cancels a running query.
      filterSuggestions: { field: this.filterField, limit: 50, search: search.trim(), suggestionRequestSeq: requestSeq },
    }
    this.filterSuggestionTimer = window.setTimeout(() => {
      if (!this.clientState.isSuggestionCurrent(requestSeq)) return
      this.emitCommand({ action: 'configure', mode: 'explore', explore: suggestionCommand })
    }, 180)
  }

  private nextFilterSuggestionRequestSeq(): number {
    return this.clientState.nextSuggestionSequence(this.dataExplorer?.command?.clientId)
  }

  private runExplore(command: DataExploreCommand): void {
    window.clearTimeout(this.exploreTimer)
    const spec = explorationSpecFor(command)
    if (explorationRunValidation(spec, this.dataExplorer.explore.fields).length) return
    const runID = this.clientState.nextRunID()
    const runCommand = prepareExplorationRun(command)
    this.exploreTransportAction = 'run'
    this.exploreExecutionState = 'running'
    this.exploreTransportFailure = null
    this.optimisticExplore = runCommand
    if (!this.embedded) updateSavedExplorationURL({ ...this.dataExplorer.command, mode: 'explore', explore: runCommand }, 'push', this.savedExplorations)
    this.emitCommand({ action: 'run', mode: 'explore', runId: runID, explore: runCommand })
  }

  private stopExplore(command: DataExploreCommand): void {
    window.clearTimeout(this.exploreTimer)
    const stopCommand = prepareExplorationStop(command)
    const runID = this.clientState.runID()
    this.clientState.clearRunID()
    this.exploreTransportAction = 'stop'
    this.exploreExecutionState = 'stopped'
    this.optimisticExplore = stopCommand
    this.emitCommand({ action: 'stop', mode: 'explore', runId: runID, explore: stopCommand })
  }

  private handleExploreInteraction(
    event: CustomEvent<{ command: OptimisticInteractionCommand; mode: ExplorationInteractionMode }>,
    command: DataExploreCommand,
    fields: readonly DataExploreFieldSignal[],
    grainFields: readonly string[],
  ): void {
    handleDataExploreInteraction(event, command, fields, grainFields, {
      setError: (message) => { this.exploreInteractionError = message },
      clearError: () => { this.exploreInteractionError = '' },
      run: (spec, source) => { const next = this.queryController.explore(source, spec); next.action = 'configure'; this.runExplore(next) },
      emit: (spec, source) => this.emitExplore(spec, source, undefined, true),
    })
  }

  private handleExploreWindowRequest(
    event: CustomEvent<VisualizationWindowRequest>,
    command: DataExploreCommand,
    views: Record<string, VisualizationEnvelope>,
    result: DataExploreResultSignal,
  ): void {
    handleDataExploreWindowRequest(event, command, views, result, {
      setError: (message) => { this.exploreInteractionError = message; this.requestUpdate() },
      clearError: () => { this.exploreInteractionError = '' },
      run: (spec, source) => { const next = this.queryController.explore(source, spec); next.action = 'configure'; this.runExplore(next) },
    })
  }

  private handleExploreTableCommand(detail: Partial<ExplorationSpec> | Pick<DataExploreCommand, 'columnWidths'>, command: DataExploreCommand): void {
    if (Object.prototype.hasOwnProperty.call(detail, 'columnWidths')) {
      this.emitCommand({ explore: { ...command, columnWidths: (detail as Pick<DataExploreCommand, 'columnWidths'>).columnWidths } })
      return
    }
    this.emitExplore(detail as Partial<ExplorationSpec>, command)
  }

  private agentSuggestions(explorer: DataExplorerSignal): AgentReferenceSignal[] {
    const command = this.optimisticExplore ?? explorer.explore.command; const spec = explorationSpecFor(command)
    const context = this.page?.context
    const projectId = context?.projectId ?? ''
    const generationId = context?.generationId ?? ''
    const semanticModelId = spec.modelId ?? ''
    const datasetId = spec.datasetId ?? ''
    if (!projectId || !generationId || !semanticModelId || !datasetId) return []
    const dataset = explorer.explore.datasets.find((candidate) => candidate.id === datasetId)
    const href = dataExplorerURL({ mode: 'explore', objectKey: '', explore: command, count: 100, limit: 100, offset: 0, start: 0, sort: {}, requestSeq: command.requestSeq, resetVersion: command.resetVersion })
    return [{
      reference: { kind: 'dataset', id: `${semanticModelId}/${datasetId}` },
      name: dataset?.title ?? datasetId,
      description: dataset?.description,
      hierarchy: [projectId, semanticModelId], href, locations: [], context: ['active_project_generation'],
    }]
  }

  private handleAgentNew = () => {
    this.restoredAgentConversationId = ''
    this.agentRestoreDispatched = true
    this.agentStateController.newConversation()
    this.persistAgentState()
  }

  private setAgentDrawerOpen(open: boolean): void {
    this.agentDrawerOpen = open
    this.agentStateController.setOpen(open)
    this.persistAgentState()
  }

  private beginBrowserResize = (event: PointerEvent): void => {
    if (this.browserCollapsed) return
    event.preventDefault()
    this.browserResizeCleanup?.()
    const startX = event.clientX
    const startWidth = this.browserWidth
    const move = (next: PointerEvent) => {
      this.browserWidth = this.panelController.setBrowserWidth(startWidth + next.clientX - startX).browserWidth
    }
    const finish = () => this.browserResizeCleanup?.()
    this.browserResizeCleanup = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', finish)
      window.removeEventListener('pointercancel', finish)
      this.browserResizeCleanup = undefined
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', finish)
    window.addEventListener('pointercancel', finish)
  }

  private resizeBrowserFromKeyboard = (event: KeyboardEvent): void => {
    if (event.key !== 'ArrowLeft' && event.key !== 'ArrowRight') return
    event.preventDefault()
    this.browserWidth = this.panelController.setBrowserWidth(this.browserWidth + (event.key === 'ArrowRight' ? 16 : -16)).browserWidth
  }

  private headerColumns(explorer: DataExplorerSignal, semanticActive: boolean): ExplorerColumn[] {
    if (semanticActive) return explorer.explore?.result?.columns ?? []
    const previewColumns = explorer.preview?.columns ?? []
    return previewColumns.length ? previewColumns : explorer.selectedObject?.columns ?? []
  }

  private headerVisibleColumnKeys(explorer: DataExplorerSignal, columns: ExplorerColumn[], semanticActive: boolean): string[] {
    const configured = semanticActive ? this.exploreVisibleColumns : explorer.command?.visibleColumns ?? []
    if (!configured.length) return columns.map((column) => column.key)
    const allowed = new Set(configured)
    const visible = columns.filter((column) => allowed.has(column.key)).map((column) => column.key)
    return visible.length ? visible : columns.map((column) => column.key)
  }

  private toggleHeaderColumn(key: string, checked: boolean, columns: ExplorerColumn[], semanticActive: boolean): void {
    const visible = this.headerVisibleColumnKeys(this.dataExplorer, columns, semanticActive)
    const configured = toggleVisibleColumns(visible, key, checked, columns.map((column) => column.key))
    if (semanticActive) {
      this.exploreVisibleColumns = configured
      return
    }
    this.emitCommand({ visibleColumns: configured })
  }

  private persistAgentState(): void {
    this.agentStateController.setOpen(this.agentDrawerOpen)
  }

  private renderResourceGroups(
    groups: ResourceGroup[],
    selectedKey: string,
    explore: DataExploreSignal,
    semanticActive: boolean,
  ) {
    const revealMatches = Boolean(this.search.trim())
    return groups.map((group) => html`
      <details
        ?open=${revealMatches || this.expandedGroupIDs.has(group.id)}
        class="resource-group"
        data-group-id=${group.id}
        @toggle=${(event: Event) => this.handleGroupToggle(event, group.id)}
      >
        <summary>
          <span class="chevron" aria-hidden="true">${lucideIcon(ChevronRight, { size: 14 })}</span>
          <span class="resource-icon" aria-hidden="true" title="Project resource">${lucideIcon(Database, { size: 14 })}</span>
          <span title=${`${group.objects.length} Models`}>${label(group.title)} (${group.objects.length})</span>
        </summary>
        <div class="object-list">
          ${this.renderObjectNodes(group.objects, selectedKey, explore, semanticActive)}
        </div>
      </details>
    `)
  }

  private handleGroupToggle(event: Event, groupID: string): void {
    const details = event.currentTarget as HTMLDetailsElement
    if (details.open) this.expandedGroupIDs.add(groupID)
    else this.expandedGroupIDs.delete(groupID)
  }

  private renderObjectNodes(
    objects: DataExplorerObjectSignal[],
    selectedKey: string,
    explore: DataExploreSignal,
    semanticActive: boolean,
  ) {
    const titleCounts = new Map<string, number>()
    for (const object of objects) {
      const title = object.title.trim().toLowerCase()
      titleCounts.set(title, (titleCounts.get(title) ?? 0) + 1)
    }
    return objects.map((object) => {
      const selected = object.key === selectedKey
      const duplicateTitle = (titleCounts.get(object.title.trim().toLowerCase()) ?? 0) > 1
      const displayTitle = duplicateTitle && object.semanticModelId ? `${object.semanticModelId}.${object.title}` : object.title
      const columnMatch = objectColumnMatchesSearch(object, this.search)
      const command = this.optimisticExplore ?? explore.command; const spec = explorationSpecFor(command)
      const contextMatches = exploreContextMatchesObject(command, object)
      const semanticFields = contextMatches
        ? (explore.fields ?? []).filter((field) => field.datasetId === objectDatasetID(object))
        : []
      const dimensionByColumn = new Map(
        semanticFields.filter((field) => field.kind !== 'metric').map((field) => [fieldColumnID(field), field]),
      )
      const dimensions = (object.columns ?? []).map((column): DataExploreFieldSignal => dimensionByColumn.get(column.key) ?? {
        id: `${objectDatasetID(object)}.${column.key}`,
        label: column.label || column.key,
        kind: 'dimension',
        datasetId: objectDatasetID(object),
        type: column.type,
        description: column.description,
        compatible: true,
        selected: false,
      })
      const metrics = semanticFields.filter((field) => field.kind === 'metric')
      const fields = [...dimensions, ...metrics]
      const queryFields = new Set([
        ...(spec.dimensions ?? []).map((field) => field.field),
        ...(spec.metrics ?? []).map((field) => field.field),
      ])
      return html`
        <details class="object-node" data-column-match=${String(columnMatch)}>
          <summary
            class=${selected ? 'object-button is-selected' : 'object-button'}
            @click=${(event: MouseEvent) => this.handleObjectNodeClick(event, object, selected)}
          >
            <span class="chevron object-expand" title="Expand columns" aria-label="Expand columns">${lucideIcon(ChevronRight, { size: 13 })}</span>
            <span aria-hidden="true">${lucideIcon(iconForLayer(object.layer), { size: 14 })}</span>
            <span class="object-label">
              <strong title=${`${object.columnCount || 0} columns`}>${label(displayTitle)}</strong>
              ${object.datasetId ? html`<small>${label(object.datasetId)}</small>` : nothing}
            </span>
          </summary>
          <div class="column-list" aria-label=${`${object.title} fields`}>
            ${fields.map((field) => {
              const compatible = field.compatible !== false
              const rebaseable = !compatible && Boolean(field.rebaseDatasetId)
              const selectable = compatible || rebaseable
              const relationshipPath = field.relationshipPath ?? []
              const fieldSelected = semanticActive
                ? contextMatches && compatible && queryFields.has(field.id)
                : selected && field.kind !== 'metric'
              const compatibilityTitle = compatible
                ? relationshipPath.length
                  ? `Related through ${relationshipPath.join(' → ')}`
                  : field.description || field.id
                : field.compatibilityReason || `Not compatible with ${spec.datasetId || objectDatasetID(object)}`
              return html`
              <div class=${`${field.kind === 'metric' ? 'column-item metric-field' : 'column-item'}${selectable ? '' : ' is-unavailable'}${rebaseable ? ' is-rebaseable' : ''}`} title=${compatibilityTitle}>
                <button
                  type="button"
                  class=${fieldSelected ? 'field-button is-selected' : 'field-button'}
                  aria-pressed=${String(fieldSelected)}
                  aria-disabled=${String(!selectable)}
                  ?disabled=${!selectable}
                  title=${compatible ? `${fieldSelected ? 'Remove' : 'Add'} ${field.label}${relationshipPath.length ? ` · ${compatibilityTitle}` : ''}` : compatibilityTitle}
                  @click=${() => this.toggleUnifiedField(field, object, explore, semanticActive)}
                >
                  <span class="field-check" aria-hidden="true">${lucideIcon(fieldSelected ? SquareCheckBig : Square, { size: 13 })}</span>
                  <span title=${field.type ? `Field type ${field.type}` : field.kind === 'metric' ? 'Metric' : 'Field type unknown'} aria-label=${field.type ? `Field type ${field.type}` : field.kind === 'metric' ? 'Metric' : 'Field type unknown'}>${lucideIcon(field.kind === 'metric' ? Sigma : fieldTypeIcon(field.type), { size: 13 })}</span>
                  <span>${field.label || field.id}</span>
                  <code>${compatible ? field.kind === 'metric' ? 'metric' : relationshipPath.length ? 'related' : field.type || '' : rebaseable ? 'changes grain' : 'unavailable'}</code>
                </button>
                ${field.kind === 'dimension' && compatible && semanticActive && contextMatches
                  ? html`<button type="button" class="field-action" title="Filter ${field.label}" aria-label="Filter ${field.label}" @click=${() => {
                      this.openFilter(field)
                      this.requestFilterSuggestions(this.optimisticExplore ?? explore.command, '')
                    }}>${lucideIcon(Filter, { size: 13 })}</button>`
                  : nothing}
              </div>
            `})}
          </div>
        </details>
      `
    })
  }

  private renderSelected(_object: DataExplorerObjectSignal, preview: DataPreviewSignal, command: DataExplorerCommand) {
    return html`
      <div class="content" aria-label="Data preview">
        <lv-data-preview-table
          .preview=${preview}
          .command=${command}
          @lv-data-preview-table-command=${(event: CustomEvent<Partial<DataExplorerCommand>>) => this.emitCommand(event.detail)}
        ></lv-data-preview-table>
      </div>
    `
  }

  private renderExploreSelected(object: DataExplorerObjectSignal, exploreSignal: DataExploreSignal) {
    const explore = exploreSignal ?? emptyExplorer.explore
    const command = this.optimisticExplore ?? explore.command
    const spec = explorationSpecFor(command)
    const dashboardAuthoring = this.dashboardAuthoring
    const selectedSemanticModel = (explore.semanticModels ?? []).find((model) => model.id === spec.modelId) ?? explore.selectedSemanticModel
    const datasets = selectedSemanticModel?.datasets ?? explore.datasets ?? []
    const selectedDataset = datasets.find((dataset) => dataset.id === spec.datasetId) ?? explore.selectedDataset
    const queryFields = new Set([...(spec.dimensions ?? []).map((field) => field.field), ...(spec.metrics ?? []).map((field) => field.field)])
    const rawResult = explore.result ?? emptyExplorer.explore.result; const result = this.clientState.semanticResult(command, rawResult, explore.status, this.page?.context, this.exploreExecutionState)
    const presentation = this.clientState.semanticViews(command, rawResult, explore.views, explore.recommendedView, explore.defaultView, explore.status, this.page?.context, this.exploreExecutionState)
    const status = explore.status
    const suggestionSignal = explore.filterSuggestions && this.clientState.isSuggestionCurrentOrNewer(explore.filterSuggestions.suggestionRequestSeq)
      ? explore.filterSuggestions
      : undefined
    const hasQuery = queryFields.size > 0 || Boolean(spec.time)
    const runValidation = explorationRunValidation(spec, explore.fields)
    const runDisabled = runValidation.length > 0
    const exploreError = rawResult.error || explore.status?.error || this.exploreTransportFailure?.message || (explore.status?.state === 'error' ? 'Query failed' : '')
    const exploreRunning = status?.loading === true || this.exploreExecutionState === 'pending' || this.exploreExecutionState === 'running'
    return html`
      <div class="content" aria-label="Data exploration">
        <section class="semantic-result" aria-label="Governed result table">
            <section class="query-bar" aria-label="Query">
              <div class="query-row">
                <span class="query-label">Fields</span>
                <div class="selection-shelf">
                  ${(spec.dimensions ?? []).map((field) => this.renderQueryChip(field.field, 'dimension', explore.fields, command))}
                  ${(spec.metrics ?? []).map((field) => this.renderQueryChip(field.field, 'metric', explore.fields, command))}
                  ${!queryFields.size ? html`<span class="empty">Select fields from the expanded Models.</span>` : nothing}
                </div>
                <div class="query-actions">
                  ${exploreRunning
                    ? html`<button type="button" class="text-button" title="Stop the pending exploration" @click=${() => this.stopExplore(command)}>${lucideIcon(X, { size: 14 })} Stop</button>`
                    : html`<button type="button" class="text-button" title=${runDisabled ? runValidation.join(' ') : 'Run exploration'} ?disabled=${runDisabled} @click=${() => this.runExplore(command)}>${lucideIcon(Play, { size: 14 })} Run</button>`}
                  <button type="button" class="icon-button" title="Return to all table columns" aria-label="Return to all table columns" @click=${() => this.selectObject(object)}>${lucideIcon(RotateCcw, { size: 16 })}</button>
                  <lv-data-explorer-dashboard-picker .state=${dashboardAuthoring} .spec=${spec}></lv-data-explorer-dashboard-picker>
                </div>
              </div>
              <div class="query-row">
                <span class="query-label">Filters</span>
                <div class="filter-pills">
                  ${(spec.filters ?? []).map((filter, index) => html`
                    <button type="button" class="chip" title="Remove filter" @click=${() => this.removeExploreFilter(index, command)}>
                      ${fieldLabel(filter.field, explore.fields)} ${filterOperator(filter).replaceAll('_', ' ')} ${filterValues(filter).join(', ')} ${lucideIcon(X, { size: 12 })}
                    </button>
                  `)}
                  ${!spec.filters?.length ? html`<span class="empty">No filters</span>` : nothing}
                </div>
                <label>Rows
                  <select .value=${String(spec.limit)} @change=${(event: Event) => this.emitExplore({ ...spec, limit: Number((event.target as HTMLSelectElement).value) }, command)}>
                    ${[50, 100, 250, 500, 1000].map((limit) => html`<option value=${limit}>${limit}</option>`)}
                  </select>
                </label>
              </div>
            </section>
            <lv-data-explorer-query-controls
              .command=${command}
              .fields=${explore.fields}
              .suggestions=${suggestionSignal}
              .filterField=${this.filterField}
              .filterOperator=${this.filterOperator}
              .filterValue=${this.filterValue}
              @lv-data-explorer-spec-change=${(event: CustomEvent<ExplorationSpec>) => this.handleExploreSpecChange(event, command)}
              @lv-data-explorer-filter-open=${(event: CustomEvent<string>) => this.handleExploreFilterOpen(event, command)}
              @lv-data-explorer-filter-change=${(event: CustomEvent<{ action: 'apply' | 'cancel' | 'operator' | 'value'; operator?: string; value?: string }>) => this.handleExploreFilterChange(event, command)}
            ></lv-data-explorer-query-controls>
            <section class="result-notices" aria-label="Query status">
              ${this.renderExecutionState(command, result, status, rawResult.error || this.exploreTransportFailure?.message)}
              ${exploreError ? renderExploreFailure(exploreError, runValidation.length === 0, () => this.runExplore(command), () => this.resetExplore(command)) : nothing}
              ${this.exploreInteractionError ? html`<p class="result-error" role="alert">${this.exploreInteractionError}</p>` : nothing}
            </section>
            <section class="result-body" aria-label="Result body">
              ${hasQuery
                ? html`<lv-data-explorer-results
                    .command=${command}
                    .result=${result}
                    .status=${status}
                    .views=${presentation.views}
                    .recommendedView=${presentation.recommendedView}
                    .selectedDataset=${selectedDataset ? { id: selectedDataset.id, title: selectedDataset.title, grainEntity: selectedDataset.grainEntity, grainLabel: datasetGrainLabel(selectedDataset) } : undefined}
                    .executionState=${this.exploreExecutionState}
                    aria-busy=${String(exploreRunning)}
                    @lv-data-explore-interaction=${(event: CustomEvent<{ command: OptimisticInteractionCommand; mode: ExplorationInteractionMode }>) => this.handleExploreInteraction(event, command, explore.fields, selectedDataset?.grainFields ?? [])}
                    @lv-visualization-window-request=${(event: CustomEvent<VisualizationWindowRequest>) => this.handleExploreWindowRequest(event, command, presentation.views, result)}
                  ></lv-data-explorer-results>`
                : exploreError ? nothing : html`<p class="empty">Select at least one field to build a governed result table.</p>`}
            </section>
        </section>
      </div>
    `
  }

  private selectObject(object: DataExplorerObjectSignal): void {
    this.optimisticExplore = null
    this.latestExploreRequestSeq = 0
    this.closeFilter()
    const currentExplore = this.dataExplorer?.explore?.command ?? emptyExplorer.explore.command
    const datasetID = objectDatasetID(object)
    const localDimensions = localPreviewDimensions(object, this.dataExplorer?.explore?.fields ?? [])
    const semanticActive = this.dataExplorer?.command?.mode === 'explore'
    const explore: DataExploreCommand = {
      ...currentExplore,
      action: semanticActive ? 'configure' : undefined,
      spec: {
        ...explorationSpecFor(currentExplore),
        modelId: object.semanticModelId ?? '',
        datasetId: datasetID,
        dimensions: localDimensions.map((field) => ({ field })),
        metrics: [],
        filters: [],
        sort: [],
      },
      columnWidths: {},
    }
    this.emitCommand({
      action: semanticActive ? 'configure' : undefined,
      mode: semanticActive ? 'explore' : 'browse',
      explore,
      objectKey: object.key,
      offset: 0,
      limit: 100,
      block: 'all',
      start: 0,
      count: 100,
      requestSeq: 0,
      resetVersion: (this.dataExplorer?.command?.resetVersion ?? 0) + 1,
      sort: {},
      visibleColumns: [],
      columnWidths: {},
    })
  }

  private handleObjectNodeClick(event: MouseEvent, object: DataExplorerObjectSignal, selected: boolean): void {
    const path = event.composedPath()
    if (path.some((target) => target instanceof HTMLElement && target.classList.contains('object-expand'))) return
    event.preventDefault()
    if (!selected || this.dataExplorer.command?.mode === 'explore' || this.optimisticExplore) this.selectObject(object)
  }

  private emitCommand(partial: Partial<DataExplorerCommand>) {
    const current = this.dataExplorer?.command ?? emptyExplorer.command
    const hydratedClientID = typeof current.clientId === 'string' ? current.clientId.trim() : ''
    const partialClientID = typeof partial.clientId === 'string' ? partial.clientId.trim() : ''
    const clientId = partialClientID || this.clientState.clientID(hydratedClientID)
    const next = this.queryController.command({
      ...current,
      clientId,
      explore: current.explore ?? this.dataExplorer?.explore?.command,
      objectKey: current.objectKey ?? this.dataExplorer?.selectedKey ?? '',
    }, { ...partial, clientId })
    // Semantic command sequences are durable for the tab, not for this
    // component instance. A navigation can hydrate requestSeq back to zero
    // while the server lifecycle still remembers a higher request. Suggestions
    // intentionally stay on their independent lane and must not advance this
    // sequence or cancel a semantic request. Stop keeps the addressed run's
    // sequence so its server-side tombstone can acquire the response lease.
    const suggestionAction = partial.explore?.action ?? partial.action
    const suggestionCommand = partial.explore?.filterSuggestions !== undefined && suggestionAction === 'configure'
    const commandAction = next.explore?.action ?? next.action
    const stopCommand = commandAction === 'stop'
    if (!suggestionCommand && !stopCommand) {
      const hydratedSequence = Math.max(
        current.requestSeq ?? 0,
        next.requestSeq ?? 0,
        next.explore?.requestSeq ?? 0,
      )
      const requestSeq = this.clientState.nextRequestSequence(clientId, hydratedSequence)
      next.requestSeq = requestSeq
      if (next.explore) next.explore.requestSeq = requestSeq
    }
    // Any semantic draft/run/stop command supersedes in-flight suggestions.
    // This is a browser-side freshness guard for the interval before the
    // corresponding semantic request reaches the server.
    if (partial.explore && partial.explore.filterSuggestions === undefined) this.clientState.invalidateSuggestions()
    if (!this.embedded) {
      const action = partial.action ?? next.action
      const exploreAction = partial.explore?.action ?? next.explore?.action
      const modeChange = partial.mode !== undefined
      const browseSelection = partial.objectKey !== undefined && next.mode !== 'explore'
      const explicitRun = action === 'run' || exploreAction === 'run'
      const transientConfigure = exploreAction === 'configure'
      if (explicitRun || modeChange || browseSelection) {
        updateSavedExplorationURL(next, 'push', this.savedExplorations)
      } else if (!transientConfigure && partial.explore !== undefined && next.mode !== 'explore') {
        updateSavedExplorationURL(next, 'push', this.savedExplorations)
      }
    }
    this.dispatchEvent(new CustomEvent('lv-data-explorer-command', { bubbles: true, composed: true, detail: next }))
  }

  private canonicalizeInitialURL(): void {
    if (this.embedded || this.initialURLCanonicalized || this.optimisticExplore) return
    if (!Object.prototype.hasOwnProperty.call(this.signals, 'dataExplorer')) return
    this.initialURLCanonicalized = true
    updateSavedExplorationURL(this.dataExplorer.command, 'replace', this.savedExplorations)
  }

  /**
   * Let the canonical server updates route decode and hydrate history entries.
   * Posting the old in-memory command here would execute the wrong entry and
   * would make a Back/Forward action execute twice.
   */
  private readonly handleHistoryPopState = (): void => {
    if (this.embedded || typeof window === 'undefined') return
    window.clearTimeout(this.exploreTimer)
    this.optimisticExplore = null
    this.closeFilter()
    window.location.reload()
  }
}

if (!customElements.get('lv-data-explorer')) customElements.define('lv-data-explorer', DataExplorerPage)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explorer': DataExplorerPage
  }
}
