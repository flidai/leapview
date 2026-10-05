import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ArrowLeft, ChevronRight, Code2, Columns3, Database, Filter, Play, RotateCcw, Search, Sigma, Square, SquareCheckBig, X } from 'lucide'
import type {
  DataExploreCommand,
  DataExploreFieldSignal,
  DataExploreFilterSignal,
  DataExploreSignal,
  DataExplorerCommand,
  DataExplorerObjectSignal,
  DataExplorerPageSignal,
  DataExplorerSignal,
  DataPreviewSignal,
  SavedExplorationStateSignal,
} from '../../generated/signals'
import type { ExplorationSpec } from '../../generated/exploration'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DatastarLit } from '../shared/datastar-lit'
import { dataExplorerBrowserStyles } from './data-explorer-browser-styles'
import { dataExplorerResultStyles } from './data-explorer-result-styles'
import { explorerVisualization, type ExplorerVisualizationPage } from './data-explorer-visualization'
import { dataExplorerResponsiveStyles } from './data-explorer-responsive-styles'
import { dataExplorerHandoffStyles } from './data-explorer-handoff-styles'
import { domainEvents, emitDomainEvent } from '../shared/events'
import { agentIcon } from '../chat/agent-icon'
import { fieldTypeIcon } from '../shared/field-type-icon'
import { lucideIcon } from '../shared/lucide-icons'
import {
  DataExplorerAgentStateController,
  DataExplorerPanelController,
  DataExplorerQueryController,
  DataExplorerSelectionController,
  dataExplorerAgentSuggestions,
  prepareExplorationRun,
  prepareExplorationStop,
  exploreContextMatchesObject,
  fieldColumnID,
  objectDatasetID,
  toggleVisibleColumns,
} from './data-explorer-controller'
import { dashboardReturnPath, updateDataExplorerURL } from './data-explorer-url'
import { isOutsideSemanticFields, renderSelectedFieldRows, renderSemanticFieldPane, renderSemanticFilterDock, renderSemanticFilterTrigger, semanticLayoutStyles } from './data-explorer-semantic-layout'
import {
  emptySavedExplorations,
  renderExplorationShareMenu,
  renderSavedExplorations,
  SavedExplorationTracker,
  SavedExplorationViewController,
  savedExplorationSelectionIncludesArchived,
  savedExplorationStyles,
  explorationDisplayTitle,
  type SavedExplorationViewOptions,
} from './data-explorer-saved'
import '../chat/chat-drawer'
import './preview-table'
import './explore-table'
import './data-explorer-query-controls'
import './data-explorer-sql'
import '../dashboard/visualization/host'
import { DataExplorerClientState } from './data-explorer-client'
import type { DataExplorerFilterControlDetail } from './data-explorer-query-controls'
import { browserCommandFailure, ownsBrowserCommandFetch, type BrowserCommandFailure } from '../shared/command-failure'
import '../shared/command'
import { DashboardAppendController, dashboardAppendStyles } from './data-explorer-dashboard'
import {
  emptyExplorationSpec,
  explorationRunValidation,
  explorationSpecFor,
  explorationSpecFromCommand,
  filterOperator,
  filterValues,
  makeExplorationFilter,
  removeExplorationField,
  toggleExplorationField,
} from './data-explorer-spec'
import {
  datasetGrainLabel,
  fieldLabel,
  filterObjects,
  groupObjectsBySemanticModel,
  iconForLayer,
  label,
  layerLabel,
  localPreviewDimensions,
  objectColumnMatchesSearch,
  queryTargetLabel,
  type ResourceGroup,
} from './data-explorer-view-model'

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
    command: { spec: emptyExplorationSpec, semanticModelId: '', datasetId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0, columnWidths: {} },
    semanticModels: [], datasets: [], fields: [],
    result: { columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [] },
    status: { loading: false, stale: false, requestSeq: 0, state: 'idle' },
  },
  command: { mode: 'browse', objectKey: '', offset: 0, limit: 100, block: 'all', start: 0, count: 100, requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} },
  warnings: [],
}

type ExplorerColumn = { key: string, label?: string }

class DataExplorerPage extends DatastarLit(LitElement) {
  private readonly dashboardAppend = new DashboardAppendController(this, () => this.requestUpdate())
  private readonly savedViewController = new SavedExplorationViewController(this, () => this.requestUpdate())
  @property({ type: Boolean, reflect: true }) embedded = false
  @state() private search = ''
  @state() private semanticFieldSearch = ''
  @state() private semanticPanel: 'fields' | 'filters' | null = null
  @state() private showSQL = false
  @state() private filterField = ''
  @state() private filterOperator = 'equals'
  @state() private filterValue = ''
  @state() private filterSuggestionRequestSeq = 0
  @state() private optimisticExplore: DataExploreCommand | null = null
  @state() private agentDrawerOpen = false
  @state() private browserCollapsed = false
  @state() private browserWidth = 320
  @state() private browseVisibleColumns: { objectKey: string; keys: string[] } | null = null
  @state() private exploreVisibleColumns: string[] = []
  @state() private resultView: 'table' | 'chart' | 'pivot' | 'details' = 'table'
  @state() private chartPage?: ExplorerVisualizationPage
  @state() private exploreExecutionState: 'idle' | 'pending' | 'running' | 'stopped' | 'uncertain' = 'idle'
  @state() private exploreTransportFailure: BrowserCommandFailure | null = null
  private exploreTransportAction: 'run' | 'stop' | null = null
  private lastSearch = ''
  private expandedGroupIDs = new Set<string>()
  private exploreFrame = 0
  private filterSuggestionTimer = 0
  private latestExploreRequestSeq = 0
  private agentStateInitialized = false
  private agentRestoreDispatched = false
  private restoredAgentConversationId = ''
  private browserResizeCleanup?: () => void
  private readonly agentStateController = new DataExplorerAgentStateController()
  private readonly panelController = new DataExplorerPanelController()
  private readonly queryController = new DataExplorerQueryController()
  private readonly selectionController = new DataExplorerSelectionController()
  private readonly clientState = new DataExplorerClientState()
  private readonly savedExplorationTracker = new SavedExplorationTracker({
    onBaselineChanged: (current) => {
      this.savedViewController.baselineChanged(current)
    },
    onDirty: () => this.dispatchEvent(new CustomEvent('lv-saved-exploration-dirty', { bubbles: true, composed: true })),
  })

  static styles = [css`
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

    .route.saved-enabled {
      grid-template-rows: auto auto minmax(0, 1fr);
    }

    .route.agent-open {
      grid-template-columns: minmax(0, 1fr) minmax(20rem, 28rem);
    }

    .route.agent-open > .header,
    .route.agent-open > .saved-explorations,
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

    ${dataExplorerHandoffStyles}

    .header-actions,
    .query-actions,
    .selection-shelf,
    .filter-pills {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
    }

    .mode-switch {
      display: inline-flex;
      align-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: var(--base-size-2);
    }

    .mode-switch button {
      min-height: calc(var(--control-small-size) - var(--base-size-4));
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      padding: 0 var(--base-size-8);
      cursor: pointer;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
    }

    .mode-switch button[aria-pressed='true'] {
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      box-shadow: var(--lv-shadow-floating-sm);
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

    .selected-fields-heading .query-actions {
      flex-wrap: wrap;
      min-width: 0;
      max-width: 100%;
      justify-content: flex-end;
    }

    .text-button.run-button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: var(--base-size-6);
      border: var(--borderWidth-default) solid var(--lv-button-accent-border-rest);
      background: var(--lv-button-accent-bg-rest);
      color: var(--lv-button-accent-fg-rest);
      white-space: nowrap;
    }

    .text-button.run-button:hover:not(:disabled) { background: var(--lv-button-accent-bg-hover); }
    .text-button.run-button:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
    .text-button.run-button:disabled { opacity: 0.5; cursor: not-allowed; }
    .execution-state[data-state="success"] { color: var(--lv-fg-success); }
    .execution-state[data-state="running"] { color: var(--lv-fg-accent); }

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

    ${dataExplorerBrowserStyles}

    .main {
      display: grid;
      grid-template-rows: minmax(0, 1fr);
      background: var(--lv-bg-app);
    }

    .diagnostics {
      display: grid;
      max-height: 16rem;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      overflow: auto;
      border-top: var(--lv-border-muted);
      background: var(--lv-bg-app);
    }

    .diagnostic-block {
      min-width: 0;
      padding: var(--base-size-12) var(--base-size-16);
    }

    .diagnostic-block + .diagnostic-block {
      border-left: var(--lv-border-muted);
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
    ${dashboardAppendStyles}
    ${semanticLayoutStyles}

    .content {
      display: grid;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    .browse-layout { --explorer-filter-width: 38px; grid-template-columns: minmax(0, 1fr) var(--explorer-filter-width); }
    .browse-layout.filters-open { --explorer-filter-width: 320px; }
    @media (max-width: 720px) {
      .browse-layout { position: relative; }
      .browse-layout.filters-open .semantic-filter-dock { position: absolute; z-index: var(--zIndex-sticky, 50); inset: 0 0 0 auto; width: min(320px, 85vw); box-shadow: var(--lv-shadow-floating-sm); }
    }

    lv-data-preview-table {
      min-height: 0;
    }

    .schema-view,
    .query-view {
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

    .query-heading {
      display: flex;
      align-items: center;
      gap: var(--base-size-8);
      margin-bottom: var(--base-size-8);
      font: var(--lv-type-section-title);
    }

    .query-copy {
      margin-bottom: var(--base-size-16);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .query-code {
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-16);
    }

    .sql-panel {
      max-height: 14rem;
      overflow: auto;
      border-top: var(--lv-border-muted);
      background: var(--lv-bg-panel);
      padding: var(--base-size-12) var(--base-size-16);
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

  `, dataExplorerResultStyles, dataExplorerResponsiveStyles]

  private readonly handleDatastarFetch = (event: Event) => {
    const action = this.exploreTransportAction
    const lifecycleActive = ['running', 'pending', 'stopped', 'uncertain'].includes(this.exploreExecutionState) || action !== null
    if (!lifecycleActive || !ownsBrowserCommandFetch(this, event)) return
    // Datastar identifies the handler element, not which of its overlapping
    // commands failed. Keep the outcome deliberately unknown until a current
    // semantic status arrives.
    const failure = browserCommandFailure(event, 'Exploration command')
    if (!failure) return
    this.exploreExecutionState = 'uncertain'
    this.exploreTransportAction = null
    this.exploreTransportFailure = {
      ...failure,
      message: `${failure.message} The exploration outcome is unknown. Choose Stop or Run latest to recover.`,
    }
    this.requestUpdate()
  }

  private handleSemanticFieldsOutsidePointer = (event: PointerEvent) => {
    if (this.semanticPanel === 'fields' && isOutsideSemanticFields(event, this.renderRoot)) this.semanticPanel = null
  }

  connectedCallback(): void {
    if (!this.embedded && window.matchMedia('(max-width: 760px)').matches && !this.browserCollapsed) this.browserCollapsed = this.panelController.toggleBrowser().browserCollapsed
    if (!this.agentStateInitialized) {
      const stored = this.agentStateController.initialize()
      this.agentDrawerOpen = stored.open
      this.restoredAgentConversationId = stored.conversationId
      this.agentStateInitialized = true
    }
    if (typeof document !== 'undefined') document.addEventListener('datastar-fetch', this.handleDatastarFetch)
    document.addEventListener('pointerdown', this.savedViewController.handleOutsidePointer)
    document.addEventListener('pointerdown', this.handleSemanticFieldsOutsidePointer)
    super.connectedCallback()
  }

  disconnectedCallback(): void {
    window.cancelAnimationFrame(this.exploreFrame)
    window.clearTimeout(this.filterSuggestionTimer)
    this.browserResizeCleanup?.()
    if (typeof document !== 'undefined') document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    document.removeEventListener('pointerdown', this.savedViewController.handleOutsidePointer)
    document.removeEventListener('pointerdown', this.handleSemanticFieldsOutsidePointer)
    super.disconnectedCallback()
  }

  updated(): void {
    const observedExploreRequestSeq = this.dataExplorer.explore?.command?.requestSeq ?? 0
    if (observedExploreRequestSeq > this.latestExploreRequestSeq) this.latestExploreRequestSeq = observedExploreRequestSeq
    const exploreCommand = this.dataExplorer.explore?.command
    const status = this.dataExplorer.explore?.status
    const suggestionOnlyConfigure = exploreCommand?.action === 'configure' && Boolean(exploreCommand.filterSuggestions)
    const currentSemanticRequestSeq = Math.max(this.latestExploreRequestSeq, this.optimisticExplore?.requestSeq ?? 0)
    const terminalSemanticStatus = status?.state === 'success' || status?.state === 'error' || status?.state === 'stale' || status?.state === 'cancelled'
    if (!suggestionOnlyConfigure && terminalSemanticStatus && status && (status.requestSeq ?? 0) >= currentSemanticRequestSeq) {
      this.exploreTransportAction = null
      this.exploreTransportFailure = null
      this.clientState.clearRunID()
      if (status.state === 'cancelled') this.exploreExecutionState = 'stopped'
      else this.exploreExecutionState = 'idle'
    }
    const selectedKey = this.dataExplorer.selectedKey ?? ''
    if (this.selectionController.observe(selectedKey)) {
      this.showSQL = false
      requestAnimationFrame(() => {
        this.renderRoot.querySelector<HTMLElement>('.object-button.is-selected')?.scrollIntoView({ block: 'nearest' })
      })
    }
    this.selectionController.revealPendingSelection(this.renderRoot, this.expandedGroupIDs)
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
      if (!this.embedded) this.replaceDataExplorerURL(this.dataExplorer.command)
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
    this.savedExplorationTracker.observe(this.savedExplorations, this.activeExplorationSpec())
  }

  private activeExplorationSpec(): ExplorationSpec {
    const command = this.optimisticExplore ?? this.dataExplorer.explore?.command
    if (!command) return emptyExplorationSpec
    return this.optimisticExplore ? explorationSpecFor(command) : explorationSpecFromCommand(command)
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

  render() {
    const page = this.page
    const loading = page === null
    const explorer = this.dataExplorer ?? emptyExplorer
    const selected = explorer.selectedObject
    const semanticActive = explorer.command?.mode === 'explore' || this.optimisticExplore !== null
    const filtered = filterObjects(explorer.objects ?? [], this.search)
    const grouped = groupObjectsBySemanticModel(filtered, explorer.explore?.semanticModels ?? [])
    const agentEnabled = this.signal<unknown | null>('agent', null) !== null
    const columns = this.headerColumns(explorer, semanticActive)
    const visibleColumnKeys = this.headerVisibleColumnKeys(explorer, columns, semanticActive)
    const returnPath = !this.embedded && typeof window !== 'undefined' ? dashboardReturnPath(window.location.search) : ''
    const savedExplorations = this.savedExplorations
    const activeSpec = this.activeExplorationSpec()
    this.dashboardAppend.syncModel(activeSpec.modelId?.trim() ?? '')
    const canSaveCurrent = semanticActive && Boolean(activeSpec.modelId?.trim())
    const currentExplore = explorer.explore
    const expectedExploreRequest = this.optimisticExplore?.requestSeq ?? currentExplore?.command?.requestSeq ?? 0
    const dashboardAppendEnabled = this.dashboardAppend.isAvailable(
      !this.embedded && canSaveCurrent,
      Boolean(this.getAttribute('data-dashboard-append-operation-id')),
      expectedExploreRequest,
      currentExplore?.status,
      currentExplore?.result,
      Boolean(currentExplore && explorationRunValidation(activeSpec, currentExplore.fields).length === 0),
    )
    const savedVisible = savedExplorations.enabled && !this.embedded && (
      canSaveCurrent
      || Boolean(savedExplorations.current)
      || Boolean(savedExplorations.list?.items?.length || savedExplorations.list?.legacyItems?.length)
      || savedExplorations.save?.state === 'error'
    )
    const savedViewOptions: SavedExplorationViewOptions = this.savedViewController.options(
      canSaveCurrent, activeSpec,
      dashboardAppendEnabled ? () => this.dashboardAppend.render(true, activeSpec) : undefined,
      currentExplore?.fields ?? [],
    )
    const shareVisible = savedVisible || dashboardAppendEnabled
    return html`
      <section class=${`route${semanticActive ? ' semantic' : ''}${savedVisible ? ' saved-enabled' : ''}${agentEnabled && this.agentDrawerOpen ? ' agent-open' : ''}`} aria-label="Data Explorer">
        <header class="header">
          <div class="header-title">
            ${returnPath ? html`<a class="return-link" href=${returnPath} title="Back to dashboard">${lucideIcon(ArrowLeft)}<span>Dashboard</span></a><span class="header-divider" aria-hidden="true">/</span>` : nothing}
            <h1 title=${semanticActive ? savedExplorations.current?.title ?? '' : ''}>${semanticActive ? explorationDisplayTitle(savedExplorations.current, activeSpec, currentExplore?.fields ?? []) : page?.title ?? 'Data Explorer'}</h1>
          </div>
          <div class="header-actions">
            ${selected ? html`
              <div class="mode-switch" role="group" aria-label="Exploration mode">
                <button class="mode-button" type="button" aria-pressed=${String(!semanticActive)} @click=${() => this.setMode('browse')}>Rows</button>
                <button class="mode-button" type="button" aria-pressed=${String(semanticActive)} @click=${() => this.setMode('explore')}>Analyze</button>
              </div>
            ` : nothing}
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
            ${shareVisible ? renderExplorationShareMenu(savedExplorations, savedViewOptions, Boolean(explorer.explore?.result?.truncated)) : nothing}
            ${agentEnabled ? html`<button type="button" class="icon-button ask-button" aria-label="Ask about this data" aria-expanded=${String(this.agentDrawerOpen)} title="Ask about this data" @click=${() => this.setAgentDrawerOpen(!this.agentDrawerOpen)}>${agentIcon()}<span>Ask</span></button>` : nothing}
          </div>
        </header>
        ${savedVisible ? renderSavedExplorations(savedExplorations, savedViewOptions) : nothing}
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
                  : html`<p class="empty">${loading ? 'Loading data…' : 'No data objects match this search.'}</p>`}
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
                : loading ? 'Loading Data Explorer…' : 'No data objects are available.'}</p>`}
          </main>
        </div>
        ${agentEnabled && this.agentDrawerOpen ? html`<lv-chat-drawer
          open
          .suggestions=${dataExplorerAgentSuggestions(explorer, this.optimisticExplore ?? explorer.explore.command, this.page?.context)}
          @lv-chat-drawer-close=${() => this.setAgentDrawerOpen(false)}
          @lv-chat-new=${this.handleAgentNew}
        ></lv-chat-drawer>` : nothing}
      </section>
    `
  }

  private editExploreFilter(filter: ExplorationSpec['filters'][number], fields: DataExploreFieldSignal[], command?: DataExploreCommand) {
    const field = fields.find((candidate) => candidate.id === filter.field)
    if (!field) return
    this.openFilter(field)
    this.filterOperator = this.panelController.setFilterOperator(filterOperator(filter)).filterOperator
    this.filterValue = this.panelController.setFilterValue(filterValues(filter).join(', ')).filterValue
    if (command) this.requestFilterSuggestions(this.optimisticExplore ?? command, this.filterValue)
  }

  private renderExecutionState(command: DataExploreCommand, result: DataExploreSignal['result'], status?: DataExploreSignal['status'], transportError?: string, displayedResult = result) {
    const expected = Math.max(command.requestSeq ?? 0, this.latestExploreRequestSeq)
    const currentSemanticRequestSeq = Math.max(this.latestExploreRequestSeq, this.optimisticExplore?.requestSeq ?? 0)
    const currentStatus = status && (status.requestSeq ?? 0) >= currentSemanticRequestSeq ? status : undefined
    const currentResultError = (result.requestSeq ?? 0) >= currentSemanticRequestSeq ? result.error : undefined
    const hasResult = Boolean(displayedResult.columns.length || displayedResult.rows.length || displayedResult.sql || displayedResult.plan)
    if (this.exploreExecutionState === 'uncertain') {
      return html`<span class="execution-state" data-state="uncertain" role="status">${transportError || 'The exploration outcome is unknown. Choose Stop or Run latest to recover.'}</span>`
    }
    if (currentStatus?.state === 'cancelled' || this.exploreExecutionState === 'stopped') {
      return html`<span class="execution-state" data-state="stopped" role="status">Run stopped; draft is preserved</span>`
    }
    if (transportError || currentResultError || currentStatus?.error || currentStatus?.state === 'error') {
      return html`<span class="execution-state" data-state="error" role="status">${currentStatus?.error || transportError || currentResultError || 'Query failed'}</span>`
    }
    if (currentStatus?.loading || this.exploreExecutionState === 'pending' || this.exploreExecutionState === 'running') {
      const progress = currentStatus?.progressPercent === undefined ? '' : ` ${Math.round(currentStatus.progressPercent)}%`
      return html`<span class="execution-state" data-state="running" role="status">${currentStatus?.message || (this.exploreExecutionState === 'pending' ? 'Waiting to run…' : 'Running exploration…')}${progress}</span>`
    }
    if (hasResult && expected > displayedResult.requestSeq) {
      return html`<span class="execution-state" data-state="stale" role="status">Query edited · run to update results</span>`
    }
    if (hasResult && (currentStatus?.stale || currentStatus?.state === 'stale')) {
      return html`<span class="execution-state" data-state="stale" role="status">Results are stale — run to refresh</span>`
    }
    if (hasResult && currentStatus?.state === 'success' && displayedResult.requestSeq === expected && currentStatus.requestSeq === expected) {
      return html`<span class="execution-state" data-state="success" role="status">Results up to date</span>`
    }
    return html`<span class="execution-state" data-state="ready" role="status">Ready to run</span>`
  }

  private renderExploreFailure(error: string, command: DataExploreCommand) {
    return html`
      <span class="result-failure" role="alert">
        <span class="result-error">${error}</span>
        <button type="button" class="text-button" @click=${() => this.runExplore(command)}>Retry</button>
        <button type="button" class="text-button" @click=${() => this.resetExplore(command)}>Reset query</button>
      </span>
    `
  }

  private handleExploreSpecChange(event: CustomEvent<ExplorationSpec>, command: DataExploreCommand): void {
    this.emitExploreSpec({ ...event.detail, time: event.detail.time }, command)
  }

  private handleExploreFilterOpen(event: CustomEvent<string>, command: DataExploreCommand): void {
    const field = this.dataExplorer.explore.fields.find((candidate) => candidate.id === event.detail)
    if (!field || field.kind !== 'dimension' || field.compatible === false) return
    this.openFilter(field)
    this.requestFilterSuggestions(this.optimisticExplore ?? command, '')
  }

  private handleExploreFilterChange(event: CustomEvent<DataExplorerFilterControlDetail>, command: DataExploreCommand): void {
    const detail = event.detail
    if (detail.action === 'cancel') return this.closeFilter()
    if (detail.action === 'operator') {
      this.filterOperator = this.panelController.setFilterOperator(detail.operator ?? 'equals').filterOperator
      return
    }
    if (detail.action === 'value') {
      this.filterValue = this.panelController.setFilterValue(detail.value ?? '').filterValue
      this.requestFilterSuggestions(this.optimisticExplore ?? command, this.filterValue)
      return
    }
    this.applyExploreFilter(this.optimisticExplore ?? command, this.dataExplorer.explore.fields)
  }

  private setMode(mode: 'browse' | 'explore') {
    const current = this.dataExplorer?.command ?? emptyExplorer.command
    if (current.mode === mode && (mode !== 'browse' || this.optimisticExplore === null)) return
    this.showSQL = false
    this.resultView = 'table'
    const explore = this.optimisticExplore ?? current.explore ?? this.dataExplorer.explore.command
    if (mode === 'browse') {
      this.optimisticExplore = null
      this.closeFilter()
      this.exploreExecutionState = 'idle'
      this.exploreTransportFailure = null
    } else this.browseVisibleColumns = null
    this.emitCommand({ mode, explore })
  }

  private toggleUnifiedField(
    field: DataExploreFieldSignal,
    object: DataExplorerObjectSignal,
    explore: DataExploreSignal,
    semanticActive: boolean,
  ) {
    if (field.compatible === false && !field.rebaseDatasetId) return
    const selected = this.dataExplorer.selectedObject
    if (!semanticActive && field.kind !== 'metric') {
      if (selected?.key !== object.key) {
        this.selectObject(object)
        return
      }
      const columns = object.columns ?? []
      const key = fieldColumnID(field)
      if (!columns.some((column) => column.key === key)) return
      const visible = this.headerVisibleColumnKeys(this.dataExplorer, columns, false)
      if (visible.length === 1 && visible.includes(key)) return
      this.toggleHeaderColumn(key, !visible.includes(key), columns, false)
      return
    }
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
      semanticModelId: baseObject.semanticModelId ?? '',
      datasetId: objectDatasetID(baseObject),
      dimensions: baseDimensions.length ? baseDimensions : fallbackDimensions,
      metrics: [],
      filters: [],
      sort: [],
      time: undefined,
      columnWidths: {},
    }
    const key = field.kind === 'metric' ? 'metrics' : 'dimensions'
    const values = command[key] ?? []
    const selectedByDefault = !activeCommand && field.kind !== 'metric' && field.datasetId === objectDatasetID(baseObject)
    const selectedNow = values.includes(field.id) || selectedByDefault
    const next = selectedNow ? values.filter((id) => id !== field.id) : [...values, field.id]
    this.emitExplore({ ...command, [key]: next, sort: (command.sort ?? []).filter((sort) => sort.field !== field.id) })
  }

  private resetExplore(command: DataExploreCommand) {
    this.closeFilter()
    const spec = explorationSpecFor(command)
    this.emitExploreSpec({ ...spec, dimensions: [], metrics: [], filters: [], sort: [], time: undefined, pivot: undefined, table: undefined, visualization: undefined }, { ...command, columnWidths: {} }, true)
  }

  private openFilter(field: DataExploreFieldSignal) {
    this.semanticPanel = 'filters'
    window.clearTimeout(this.filterSuggestionTimer)
    this.clientState.invalidateSuggestions()
    this.filterSuggestionRequestSeq = 0
    const state = this.panelController.openFilter(field.id)
    this.filterField = state.filterField
    this.filterOperator = state.filterOperator
    this.filterValue = state.filterValue
  }

  private closeFilter() {
    window.clearTimeout(this.filterSuggestionTimer)
    this.clientState.invalidateSuggestions()
    this.filterSuggestionRequestSeq = 0
    const state = this.panelController.closeFilter()
    this.filterField = state.filterField
    this.filterOperator = state.filterOperator
    this.filterValue = state.filterValue
  }

  private applyExploreFilter(command: DataExploreCommand, fields: DataExploreFieldSignal[]) {
    if (!this.filterField) return
    const needsValue = this.filterOperator !== 'is_null' && this.filterOperator !== 'is_not_null'
    const values = needsValue
      ? this.filterValue.split(',').map((value) => value.trim()).filter(Boolean)
      : []
    if (needsValue && !values.length) return
    const field = fields.find((candidate) => candidate.id === this.filterField)
    const spec = explorationSpecFor(command)
    const hasMultiRootMetric = spec.metrics.some((metric) => {
      const metricField = fields.find((candidate) => candidate.id === metric.field)
      return metricField?.kind === 'metric' && !metricField.datasetId.trim()
    })
    const canonicalFilter = makeExplorationFilter(field ?? this.filterField, this.filterOperator, values, field?.type, hasMultiRootMetric ? undefined : spec.datasetId)
    if (!canonicalFilter) return
    this.closeFilter()
    this.emitExploreSpec({ ...spec, filters: [...spec.filters.filter((current) => current.field !== canonicalFilter.field), canonicalFilter] }, command)
  }

  private emitBrowseFilters(spec: ExplorationSpec, command: DataExploreCommand) {
    const explore = this.queryController.exploreSpec(command, { ...spec, time: undefined })
    explore.action = 'run'
    this.closeFilter()
    this.emitCommand({ action: 'run', mode: 'browse', explore, block: 'all', start: 0, offset: 0, resetVersion: (this.dataExplorer.command.resetVersion ?? 0) + 1 })
  }

  private applyBrowseFilter(command: DataExploreCommand, fields: DataExploreFieldSignal[]) {
    if (!this.filterField) return
    const needsValue = this.filterOperator !== 'is_null' && this.filterOperator !== 'is_not_null'
    const values = needsValue ? this.filterValue.split(',').map((value) => value.trim()).filter(Boolean) : []
    if (needsValue && !values.length) return
    const field = fields.find((candidate) => candidate.id === this.filterField)
    if (!field || field.kind !== 'dimension' || field.compatible === false) return
    const spec = explorationSpecFor(command)
    const filter = makeExplorationFilter(field, this.filterOperator, values, field.type, spec.datasetId)
    if (!filter) return
    this.emitBrowseFilters({ ...spec, filters: [...spec.filters.filter((current) => current.field !== filter.field), filter] }, command)
  }

  private handleBrowseFilterChange(detail: DataExplorerFilterControlDetail, command: DataExploreCommand, fields: DataExploreFieldSignal[]) {
    if (detail.action === 'cancel') return this.closeFilter()
    if (detail.action === 'operator') { this.filterOperator = this.panelController.setFilterOperator(detail.operator ?? 'equals').filterOperator; return }
    if (detail.action === 'value') { this.filterValue = this.panelController.setFilterValue(detail.value ?? '').filterValue; return }
    this.applyBrowseFilter(command, fields)
  }

  private removeExploreFilter(index: number, command: DataExploreCommand) {
    const spec = explorationSpecFor(command)
    this.emitExploreSpec({ ...spec, filters: spec.filters.filter((_, current) => current !== index) }, command)
  }

  private emitExplore(next: DataExploreCommand, immediate = false) {
    window.cancelAnimationFrame(this.exploreFrame)
    const current = this.optimisticExplore ?? this.dataExplorer.explore.command ?? emptyExplorer.explore.command
    const command = this.queryController.explore(current, next, immediate)
    command.action = 'configure'
    delete command.filterSuggestions
    this.optimisticExplore = command
    if (this.exploreExecutionState !== 'uncertain') this.exploreTransportFailure = null
    if (!this.embedded) this.replaceDataExplorerURL({ ...this.dataExplorer.command, mode: 'explore', explore: command })
    const dispatch = () => this.emitCommand({ action: 'configure', mode: 'explore', explore: command })
    if (immediate) dispatch()
    else this.exploreFrame = window.requestAnimationFrame(dispatch)
  }

  private emitExploreSpec(next: Partial<ExplorationSpec>, baseCommand?: DataExploreCommand, immediate = false): void {
    window.cancelAnimationFrame(this.exploreFrame)
    const current = baseCommand ?? this.optimisticExplore ?? this.dataExplorer.explore.command ?? emptyExplorer.explore.command
    const command = this.queryController.exploreSpec(current, next)
    delete command.filterSuggestions
    this.optimisticExplore = command
    this.requestUpdate()
    if (this.exploreExecutionState !== 'uncertain') this.exploreTransportFailure = null
    if (!this.embedded) this.replaceDataExplorerURL({ ...this.dataExplorer.command, mode: 'explore', explore: command })
    const dispatch = () => this.emitCommand({ action: 'configure', mode: 'explore', explore: command })
    if (immediate) dispatch()
    else this.exploreFrame = window.requestAnimationFrame(dispatch)
  }

  private requestFilterSuggestions(command: DataExploreCommand, search: string): void {
    if (!this.filterField) return
    window.clearTimeout(this.filterSuggestionTimer)
    const requestSeq = this.clientState.nextSuggestionSequence(this.dataExplorer.command.clientId)
    this.filterSuggestionRequestSeq = requestSeq
    const searchValue = this.filterOperator === 'in' || this.filterOperator === 'not_in' ? search.split(',').at(-1) ?? '' : search
    const suggestionCommand: DataExploreCommand = {
      ...command,
      action: 'configure',
      filterSuggestions: { field: this.filterField, limit: 50, search: searchValue.trim(), suggestionRequestSeq: requestSeq },
    }
    this.filterSuggestionTimer = window.setTimeout(() => {
      if (!this.clientState.isSuggestionCurrent(requestSeq)) return
      this.emitCommand({ action: 'configure', mode: 'explore', explore: suggestionCommand })
    }, 180)
  }

  private runExplore(command: DataExploreCommand): void {
    window.cancelAnimationFrame(this.exploreFrame)
    if (explorationRunValidation(explorationSpecFor(command), this.dataExplorer.explore.fields).length) return
    const recoveringUnknownOutcome = this.exploreExecutionState === 'uncertain'
    // A retry is a distinct run. If an earlier Stop is delayed in transport,
    // its old run ID (and lower request sequence) must not target this run.
    const runID = this.clientState.nextRunID()
    const runCommand = prepareExplorationRun(command)
    this.latestExploreRequestSeq = runCommand.requestSeq
    this.exploreTransportAction = 'run'
    this.exploreExecutionState = 'running'
    if (!recoveringUnknownOutcome) this.exploreTransportFailure = null
    this.optimisticExplore = runCommand
    this.emitCommand({ action: 'run', mode: 'explore', runId: runID, explore: runCommand })
  }

  private stopExplore(command: DataExploreCommand): void {
    window.cancelAnimationFrame(this.exploreFrame)
    const uncertain = this.exploreExecutionState === 'uncertain'
    const latestRequestSeq = Math.max(
      command.requestSeq ?? 0,
      this.latestExploreRequestSeq,
      this.optimisticExplore?.requestSeq ?? 0,
    )
    const stopCommand = { ...prepareExplorationStop(command), requestSeq: latestRequestSeq }
    this.exploreTransportAction = 'stop'
    this.optimisticExplore = stopCommand
    // In an unknown-outcome state either the old or retry run may have reached
    // the server. A monotonic unnamed Stop safely addresses whichever remains.
    this.emitCommand({ action: 'stop', mode: 'explore', runId: uncertain ? undefined : this.clientState.runID(), explore: stopCommand })
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
    if (semanticActive) {
      const explore = explorer.explore
      return explore?.result ? this.clientState.semanticResult(this.optimisticExplore ?? explore.command, explore.result, explore.status, this.page?.context).columns ?? [] : []
    }
    const previewColumns = explorer.preview?.columns ?? []
    return previewColumns.length ? previewColumns : explorer.selectedObject?.columns ?? []
  }

  private headerVisibleColumnKeys(explorer: DataExplorerSignal, columns: ExplorerColumn[], semanticActive: boolean): string[] {
    const local = this.browseVisibleColumns
    const configured = semanticActive
      ? this.exploreVisibleColumns
      : local && local.objectKey === explorer.selectedKey ? local.keys : explorer.command?.visibleColumns ?? []
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
    this.browseVisibleColumns = { objectKey: this.dataExplorer.selectedKey ?? '', keys: configured }
    this.emitCommand({ visibleColumns: configured })
  }

  private replaceDataExplorerURL(command: DataExplorerCommand): void {
    const saved = this.savedExplorations
    updateDataExplorerURL(command, 'replace', saved.list?.selectedId, savedExplorationSelectionIncludesArchived(saved))
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
      const command = this.optimisticExplore ?? explore.command
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
      const fields = semanticActive ? [...dimensions, ...metrics] : dimensions
      const queryFields = new Set([...(command.dimensions ?? []), ...(command.metrics ?? [])])
      const browseVisibleColumns = selected ? this.headerVisibleColumnKeys(this.dataExplorer, object.columns ?? [], false) : []
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
                ? compatible && contextMatches && queryFields.has(field.id)
                : selected && browseVisibleColumns.includes(fieldColumnID(field))
              const canToggle = selectable && (semanticActive || !selected || !fieldSelected || browseVisibleColumns.length > 1)
              const compatibilityTitle = compatible
                ? relationshipPath.length
                  ? `Related through ${relationshipPath.join(' to ')}`
                  : field.description || field.id
                : field.compatibilityReason || `Not compatible with ${command.datasetId || objectDatasetID(object)}`
              return html`
              <div class=${`${field.kind === 'metric' ? 'column-item metric-field' : 'column-item'}${selectable ? '' : ' is-unavailable'}${rebaseable ? ' is-rebaseable' : ''}`} title=${compatibilityTitle}>
                <button
                  type="button"
                  class=${fieldSelected ? 'field-button is-selected' : 'field-button'}
                  aria-pressed=${String(fieldSelected)}
                  aria-disabled=${String(!canToggle)}
                  ?disabled=${!canToggle}
                  title=${compatible ? `${fieldSelected ? 'Remove' : 'Add'} ${field.label}${relationshipPath.length ? ` · ${compatibilityTitle}` : ''}` : compatibilityTitle}
                  @click=${() => this.toggleUnifiedField(field, object, explore, semanticActive)}
                >
                  <span class="field-check" aria-hidden="true">${lucideIcon(fieldSelected ? SquareCheckBig : Square, { size: 13 })}</span>
                  <span title=${field.type ? `Field type ${field.type}` : field.kind === 'metric' ? 'Metric' : 'Field type unknown'} aria-label=${field.type ? `Field type ${field.type}` : field.kind === 'metric' ? 'Metric' : 'Field type unknown'}>${lucideIcon(field.kind === 'metric' ? Sigma : fieldTypeIcon(field.type), { size: 13 })}</span>
                  <span>${field.label || field.id}</span>
                  <code>${compatible ? field.kind === 'metric' ? 'metric' : relationshipPath.length ? 'related' : field.type || '' : rebaseable ? 'changes grain' : 'unavailable'}</code>
                </button>
                ${field.kind === 'dimension' && compatible && semanticActive && contextMatches
                  ? html`<button type="button" class="field-action" title="Filter ${field.label}" aria-label="Filter ${field.label}" @click=${() => this.openFilter(field)}>${lucideIcon(Filter, { size: 13 })}</button>`
                  : nothing}
              </div>
            `})}
          </div>
        </details>
      `
    })
  }

  private renderSelected(object: DataExplorerObjectSignal, preview: DataPreviewSignal, command: DataExplorerCommand) {
    const explore = this.dataExplorer.explore ?? emptyExplorer.explore
    const filterable = object.layer === 'model' && Boolean(object.semanticModelId && object.datasetId)
    const browseFields = filterable ? explore.fields.filter((field) => field.kind === 'dimension' && field.compatible !== false && field.datasetId === object.datasetId && field.id.startsWith(`${object.datasetId}.`)) : []
    const browseExplore = { ...explore, fields: browseFields }
    const base = command.explore ?? explore.command
    const spec = { ...explorationSpecFor(base), modelId: object.semanticModelId ?? '', datasetId: objectDatasetID(object) }
    const filterCommand = { ...base, spec }
    return html`
      <div class=${`content${filterable ? ` browse-layout${this.semanticPanel === 'filters' ? ' filters-open' : ''}` : ''}`} aria-label="Data preview">
        <lv-data-preview-table
          .preview=${preview}
          .command=${this.browseVisibleColumns?.objectKey === object.key
            ? { ...command, visibleColumns: this.browseVisibleColumns.keys }
            : command}
          @lv-data-preview-table-command=${(event: CustomEvent<Partial<DataExplorerCommand>>) => this.emitCommand(event.detail)}
        ></lv-data-preview-table>
        ${filterable ? renderSemanticFilterDock(spec, browseExplore, filterCommand, this.semanticPanel === 'filters',
          { field: this.filterField, operator: this.filterOperator, value: this.filterValue }, {
            toggle: () => this.semanticPanel = this.semanticPanel === 'filters' ? null : 'filters',
            add: (id) => { const field = browseFields.find((candidate) => candidate.id === id); if (field) this.openFilter(field) },
            clear: () => this.emitBrowseFilters({ ...spec, filters: [] }, filterCommand),
            editFilter: (filter) => this.editExploreFilter(filter, browseFields),
            removeFilter: (index) => this.emitBrowseFilters({ ...spec, filters: spec.filters.filter((_, current) => current !== index) }, filterCommand),
            changeFilter: (detail) => this.handleBrowseFilterChange(detail, filterCommand, browseFields),
            changeSpec: () => {},
          }, false) : nothing}
      </div>
    `
  }

  private renderExploreSelected(object: DataExplorerObjectSignal, exploreSignal: DataExploreSignal) {
    const explore = exploreSignal ?? emptyExplorer.explore
    const authoredCommand = this.optimisticExplore ?? explore.command
    const authoredSpec = this.optimisticExplore ? explorationSpecFor(authoredCommand) : explorationSpecFromCommand(authoredCommand)
    const spec = {
      ...authoredSpec,
      modelId: authoredSpec.modelId || object.semanticModelId || '',
      datasetId: authoredSpec.datasetId || objectDatasetID(object),
    }
    const command = { ...authoredCommand, spec }
    const selectedSemanticModel = explore.semanticModels.find((model) => model.id === spec.modelId) ?? explore.selectedSemanticModel
    const datasets = selectedSemanticModel?.datasets ?? explore.datasets ?? []
    const selectedDataset = datasets.find((dataset) => dataset.id === spec.datasetId) ?? explore.selectedDataset
    const queryFields = new Set([...spec.dimensions, ...spec.metrics].map((field) => field.field))
    const groupedFields = spec.dimensions.map((dimension) => fieldLabel(dimension.field, explore.fields))
    if (spec.time && !spec.dimensions.some((dimension) => dimension.field === spec.time?.field)) {
      groupedFields.push(`${fieldLabel(spec.time.field, explore.fields)} (${spec.time.grain})`)
    }
    const resultGrainLabel = spec.metrics.length > 0
      ? groupedFields.length > 0 ? `Grouped by: ${groupedFields.join(', ')}` : 'Overall total'
      : selectedDataset?.grainEntity ? `Grain: ${datasetGrainLabel(selectedDataset)}` : ''
    const rawResult = explore.result
    const result = this.clientState.semanticResult(command, rawResult, explore.status, this.page?.context)
    const hasQuery = queryFields.size > 0 || Boolean(spec.time)
    const runValidation = explorationRunValidation(spec, explore.fields)
    const suggestionOnlyStatus = explore.command?.action === 'configure' && Boolean(explore.command.filterSuggestions)
    const currentSemanticRequestSeq = Math.max(command.requestSeq ?? 0, this.latestExploreRequestSeq, this.optimisticExplore?.requestSeq ?? 0)
    // Suggestion-only replies do not invalidate a result from the same query request.
    const currentStatus = !suggestionOnlyStatus && explore.status && (explore.status.requestSeq ?? 0) >= currentSemanticRequestSeq
      ? explore.status
      : suggestionOnlyStatus && explore.status?.state === 'stale' && result.columns.length > 0 && result.requestSeq === currentSemanticRequestSeq && !rawResult.error
        ? { state: 'success' as const, requestSeq: result.requestSeq, loading: false, stale: false }
        : undefined
    const exploreRunning = currentStatus?.loading === true || this.exploreExecutionState === 'pending' || this.exploreExecutionState === 'running'
    const views: Record<string, VisualizationEnvelope> = {}
    if (currentStatus?.state === 'success' && result.requestSeq === currentStatus.requestSeq) {
      for (const [view, envelope] of Object.entries(explore.views ?? {})) {
        // A recursive signal patch may retain an old map key after a newer
        // run omits that view. Never present an envelope from another run.
        if (envelope.dataRevision === result.requestSeq) views[view] = envelope
      }
    }
    const canShowChart = Boolean(views.chart)
    const canShowPivot = Boolean(views.pivot)
    const canShowDetails = currentStatus?.state === 'success' && result.requestSeq === currentStatus.requestSeq && Boolean(result.sql || result.plan)
    const resultView = (this.resultView === 'chart' && canShowChart) || (this.resultView === 'pivot' && canShowPivot) || (this.resultView === 'details' && canShowDetails)
      ? this.resultView : 'table'
    const resultVisualization = resultView === 'chart' || resultView === 'pivot'
      ? explorerVisualization(views[resultView]!, explorationDisplayTitle(this.savedExplorations.current, spec, explore.fields), this.chartPage)
      : undefined
    const categoryPage = resultVisualization?.categoryPage
    return html`
      <div class=${`content semantic-layout${this.semanticPanel === 'filters' ? ' filters-open' : ''}`} aria-label="Data exploration">
        ${renderSemanticFieldPane(spec, explore, queryFields, this.semanticFieldSearch, {
          expanded: this.semanticPanel === 'fields',
          togglePane: () => this.semanticPanel = this.semanticPanel === 'fields' ? null : 'fields',
          model: (modelId) => {
            const next = this.dataExplorer.objects.find((candidate) => candidate.semanticModelId === modelId)
            if (next) this.selectObject(next)
          },
          search: (value) => this.semanticFieldSearch = value,
          toggle: (field, kind) => {
            const next = toggleExplorationField(spec, field.id, kind)
            this.emitExploreSpec({ ...next, datasetId: queryFields.has(field.id) ? spec.datasetId : field.rebaseDatasetId || spec.datasetId }, command)
          },
          filter: (id) => this.handleExploreFilterOpen(new CustomEvent('filter', { detail: id }), command),
        })}
        <section class="semantic-result" aria-label="Governed exploration results">
            <section class="query-bar" aria-label="Table controls">
              <div class="selected-fields-heading">
                <strong>Selected fields</strong>
                <span class="query-summary">${queryFields.size} ${queryFields.size === 1 ? 'column' : 'columns'}</span>
                ${this.renderExecutionState(command, rawResult, currentStatus, this.exploreExecutionState === 'uncertain' ? this.exploreTransportFailure?.message : undefined, result)}
                <div class="query-actions">
                  ${renderSemanticFilterTrigger(spec.filters.length, this.semanticPanel === 'filters', () => this.semanticPanel = this.semanticPanel === 'filters' ? null : 'filters')}
                  <select aria-label="Result limit" .value=${String(spec.limit)} @change=${(event: Event) => this.emitExploreSpec({ ...spec, limit: Number((event.target as HTMLSelectElement).value) }, command)}>
                    ${Array.from(new Set([50, 100, 250, 500, 1000, spec.limit])).map((limit) => html`<option value=${limit} .selected=${limit === spec.limit}>${limit} rows</option>`)}
                  </select>
                  ${this.exploreExecutionState === 'uncertain'
                    ? html`<button type="button" class="text-button" title="Stop the possibly running exploration" @click=${() => this.stopExplore(command)}>${lucideIcon(X, { size: 14 })} Stop</button>
                      <button type="button" class="text-button run-button" title="Run the latest query draft" ?disabled=${Boolean(runValidation.length)} @click=${() => this.runExplore(command)}>${lucideIcon(Play, { size: 14 })} Run latest</button>`
                    : exploreRunning
                    ? html`<button type="button" class="text-button" title="Stop the running exploration" @click=${() => this.stopExplore(command)}>${lucideIcon(X, { size: 14 })} Stop</button>`
                    : html`<button type="button" class="text-button run-button" title=${runValidation.length ? runValidation.join(' ') : 'Run exploration'} ?disabled=${Boolean(runValidation.length)} @click=${() => this.runExplore(command)}>${lucideIcon(Play, { size: 14 })} Run</button>`}
                  <button type="button" class="icon-button" title="Return to all table columns" aria-label="Return to all table columns" @click=${() => this.selectObject(object)}>${lucideIcon(RotateCcw, { size: 16 })}</button>
                </div>
              </div>
              ${renderSelectedFieldRows(spec, explore.fields, (id, kind) => this.emitExploreSpec(removeExplorationField(spec, id, kind), command))}
            </section>
            <div class="result-meta" aria-live="polite">
              <span><strong>${selectedSemanticModel?.title ?? label(command.semanticModelId)}</strong>${selectedDataset ? ` · ${selectedDataset.title}` : ''}</span>
              ${resultGrainLabel ? html`<span>${resultGrainLabel}</span>` : nothing}
              ${hasQuery && !rawResult.error ? html`<span>${result.rowsReturned} rows · ${result.durationMs} ms${result.truncated ? ' · truncated' : ''}</span>` : nothing}
              ${rawResult.error && this.exploreExecutionState !== 'uncertain' ? this.renderExploreFailure(rawResult.error, command) : nothing}
              ${(result.warnings ?? []).map((warning) => html`<span>${warning}</span>`)}
              ${hasQuery && (canShowChart || canShowPivot || canShowDetails) ? html`<div class="result-view-switch" role="group" aria-label="Result views">
                <button type="button" aria-pressed=${String(resultView === 'table')} @click=${() => this.resultView = 'table'}>Table</button>
                ${canShowChart ? html`<button type="button" aria-pressed=${String(resultView === 'chart')} @click=${() => this.resultView = 'chart'}>Chart</button>` : nothing}
                ${canShowPivot ? html`<button type="button" aria-pressed=${String(resultView === 'pivot')} @click=${() => this.resultView = 'pivot'}>Pivot</button>` : nothing}
                ${canShowDetails ? html`<button type="button" aria-pressed=${String(resultView === 'details')} @click=${() => this.resultView = 'details'}>SQL / Details</button>` : nothing}
              </div>` : nothing}
            </div>
            ${hasQuery
              ? resultView === 'details' ? this.renderExploreQueryDetails(object, { ...explore, result }, command)
              : resultVisualization ? html`
                  <div class=${`result-visual-layout${categoryPage ? ' paginated' : ''}`}>
                    ${categoryPage ? html`
                      <nav class="chart-pagination" aria-label="Chart category pages">
                        <span role="status" aria-live="polite">Showing ${categoryPage.first}–${categoryPage.last} of ${categoryPage.total} categories</span>
                        <div class="chart-page-actions">
                          <button type="button" class="text-button" aria-label="Previous chart categories" ?disabled=${categoryPage.index === 0}
                            @click=${() => this.setChartPage({ key: categoryPage.key, index: categoryPage.index - 1 })}>Previous</button>
                          <button type="button" class="text-button" aria-label="Next chart categories" ?disabled=${categoryPage.index + 1 >= categoryPage.count}
                            @click=${() => this.setChartPage({ key: categoryPage.key, index: categoryPage.index + 1 })}>Next</button>
                        </div>
                      </nav>` : nothing}
                    <div class="result-visual" role="region" tabindex="0" aria-label=${resultView === 'chart' ? 'Chart results' : 'Pivot results'}>
                      <lv-visualization-host
                        style=${`--explorer-visual-min-height: ${resultVisualization.minimumHeight}px`}
                        .envelope=${resultVisualization.envelope}
                        .actionsEnabled=${false}
                      ></lv-visualization-host>
                    </div>
                  </div>`
              : html`<lv-data-explore-table
                  .command=${command}
                  .result=${result}
                  .visibleColumns=${this.exploreVisibleColumns}
                  @lv-data-explore-table-command=${(event: CustomEvent<Partial<DataExploreCommand>>) => this.emitExplore({ ...command, ...event.detail })}
                ></lv-data-explore-table>`
              : html`<p class="empty">Select at least one field to build a governed result table.</p>`}
        </section>
        ${renderSemanticFilterDock(spec, explore, command, this.semanticPanel === 'filters',
          { field: this.filterField, operator: this.filterOperator, value: this.filterValue, suggestionRequestSeq: this.filterSuggestionRequestSeq }, {
            toggle: () => this.semanticPanel = this.semanticPanel === 'filters' ? null : 'filters',
            add: (id) => this.handleExploreFilterOpen(new CustomEvent('filter', { detail: id }), command),
            clear: () => this.emitExploreSpec({ ...spec, filters: [] }, command),
            editFilter: (filter) => this.editExploreFilter(filter, explore.fields, command),
            removeFilter: (index) => this.removeExploreFilter(index, command),
            changeFilter: (detail) => this.handleExploreFilterChange(new CustomEvent('filter-change', { detail }), command),
            changeSpec: (next) => this.emitExploreSpec({ ...next, time: next.time }, command),
          })}
      </div>
    `
  }

  private setChartPage(page: ExplorerVisualizationPage) {
    this.chartPage = page
    this.renderRoot.querySelector('.result-visual')?.scrollTo({ top: 0 })
  }

  private renderExploreQueryDetails(object: DataExplorerObjectSignal, explore: DataExploreSignal, command: DataExploreCommand) {
    const result = explore.result
    return html`
      <section class="query-view" aria-label="Query details">
        <dl class="metadata-grid">
          <div class="metadata-card"><dt>Query target</dt><dd>${label(command.semanticModelId)} / ${label(command.datasetId)}</dd></div>
          <div class="metadata-card"><dt>Fields</dt><dd>${command.dimensions.length + command.metrics.length}</dd></div>
          <div class="metadata-card"><dt>Filters</dt><dd>${command.filters.length}</dd></div>
          <div class="metadata-card"><dt>Rows returned</dt><dd>${result.rowsReturned}</dd></div>
        </dl>
        <h3 class="query-heading">${lucideIcon(Code2, { size: 17 })} Generated SQL</h3>
        <p class="query-copy">This is the governed query generated from the selected fields, relationships, filters, and metrics.</p>
        <lv-data-explorer-sql .sql=${result.sql ?? ''} emptyMessage="Run an exploration to generate SQL."></lv-data-explorer-sql>
        ${result.plan ? html`<h3 class="query-heading">Query plan</h3><pre class="query-code">${result.plan}</pre>` : nothing}
        ${object.description ? html`<p class="query-copy">${object.description}</p>` : nothing}
      </section>
    `
  }

  private selectObject(object: DataExplorerObjectSignal): void {
    this.browseVisibleColumns = null
    if (window.matchMedia('(max-width: 760px)').matches && !this.browserCollapsed) this.browserCollapsed = this.panelController.toggleBrowser().browserCollapsed
    this.optimisticExplore = null
    this.resultView = 'table'
    this.closeFilter()
    const currentExplore = this.dataExplorer?.explore?.command ?? emptyExplorer.explore.command
    const datasetID = objectDatasetID(object)
    const localDimensions = localPreviewDimensions(object, this.dataExplorer?.explore?.fields ?? [])
    const semanticActive = this.dataExplorer?.command?.mode === 'explore'
    const explore = this.queryController.explore(currentExplore, {
      semanticModelId: object.semanticModelId ?? '',
      datasetId: datasetID,
      dimensions: localDimensions,
      metrics: [],
      filters: [],
      sort: [],
      time: undefined,
      columnWidths: {},
    }, true)
    this.emitCommand({
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

  private renderSchema(object: DataExplorerObjectSignal, columns: NonNullable<DataExplorerObjectSignal['columns']>) {
    return html`
      <section class="schema-view" aria-label="Schema">
        <dl class="metadata-grid">
          <div class="metadata-card"><dt>Data layer</dt><dd>${layerLabel(object.layer)}</dd></div>
          <div class="metadata-card"><dt>Project generation</dt><dd>${label(this.page?.context?.projectId)} · ${label(this.page?.context?.generationId)}</dd></div>
          <div class="metadata-card"><dt>Semantic Model</dt><dd>${label(object.semanticModelId)}</dd></div>
          <div class="metadata-card"><dt>Grain</dt><dd>${object.grain ? label(object.grain) : 'Not declared'}</dd></div>
          ${object.description ? html`<div class="metadata-card"><dt>Description</dt><dd>${object.description}</dd></div>` : nothing}
        </dl>
        <table class="schema-table">
          <thead><tr><th>Column</th><th>Type</th><th>Nullable</th><th>Key</th><th>Default</th><th>Description</th></tr></thead>
          <tbody>
            ${columns.map((column) => html`
              <tr>
                <td><code>${column.label || column.key}</code>${column.label !== column.key ? html`<div class="schema-muted">${column.key}</div>` : nothing}</td>
                <td><code>${column.type || '—'}</code></td>
                <td>${column.nullable === undefined ? 'Unknown' : column.nullable ? 'Yes' : 'No'}</td>
                <td>${column.primaryKey ? 'Primary key' : '—'}</td>
                <td><code>${column.defaultValue || '—'}</code></td>
                <td class=${column.description ? '' : 'schema-muted'}>${column.description || 'No description'}</td>
              </tr>
            `)}
          </tbody>
        </table>
      </section>
    `
  }

  private renderQueryDetails(object: DataExplorerObjectSignal, preview: DataPreviewSignal) {
    return html`
      <section class="query-view" aria-label="Query details">
        <dl class="metadata-grid">
          <div class="metadata-card"><dt>Query target</dt><dd>${queryTargetLabel(object)}</dd></div>
          <div class="metadata-card"><dt>Rows</dt><dd>${label(preview.totalRowLabel || object.rowCountLabel)}</dd></div>
          <div class="metadata-card"><dt>Sort</dt><dd>${preview.sort?.column ? `${preview.sort.column} ${preview.sort.direction || 'ascending'}` : 'Source order'}</dd></div>
        </dl>
        <h3 class="query-heading">${lucideIcon(Code2, { size: 17 })} Generated SQL</h3>
        <p class="query-copy">This is the governed query executed for the current preview. Sorting and pagination are applied by the explorer.</p>
        <lv-data-explorer-sql .sql=${preview.sql ?? ''} emptyMessage="No SQL is available for this preview."></lv-data-explorer-sql>
      </section>
    `
  }

  private emitCommand(partial: Partial<DataExplorerCommand>) {
    const current = this.dataExplorer?.command ?? emptyExplorer.command
    const local = this.browseVisibleColumns
    const next = this.queryController.command({
      ...current,
      visibleColumns: local?.objectKey === (this.dataExplorer?.selectedKey ?? '') && current.mode !== 'explore'
        ? local.keys
        : current.visibleColumns,
      explore: current.explore ?? this.dataExplorer?.explore?.command,
      objectKey: current.objectKey ?? this.dataExplorer?.selectedKey ?? '',
    }, partial)
    next.clientId = this.clientState.clientID(next.clientId)
    if (!this.embedded && (partial.objectKey !== undefined || partial.mode !== undefined || partial.explore !== undefined)) {
      this.replaceDataExplorerURL(next)
    }
    this.dispatchEvent(new CustomEvent('lv-data-explorer-command', { bubbles: true, composed: true, detail: next }))
  }
}

if (!customElements.get('lv-data-explorer')) customElements.define('lv-data-explorer', DataExplorerPage)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explorer': DataExplorerPage
  }
}
