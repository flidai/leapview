import { builderCanvasDesktopWidth, builderCanvasMinimumHeight } from './dashboard-builder-canvas-size'
import { dashboardBuilderSurfaceStyles } from './dashboard-builder-surface-styles'
import { dashboardBuilderControlsStyles } from './dashboard-builder-controls-styles'
import { dashboardBuilderCanvasStyles } from './dashboard-builder-canvas-styles'
import { dashboardBuilderFieldsStyles } from './dashboard-builder-fields-styles'
import { dashboardBuilderDialogsStyles } from './dashboard-builder-dialogs-styles'
import { dashboardBuilderDensityStyles } from './dashboard-builder-density-styles'
import { savedVisualComponentId, savedVisualSourceId } from '../chat/dashboard-membership'
import { savedVisualDragType, submitVisualForm, type SavedVisualLibraryMessage } from '../chat/visual-library-bridge'
import type { ChatDashboardMessage, SavedVisualImportMessage } from '../chat/dashboard-preview-contract'
import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { GridStack, type GridItemHTMLElement, type GridStackNode } from 'gridstack'
import { Archive, ArrowDown, ArrowLeftRight, ArrowUp, ChartColumn, ChevronDown, ChevronLeft, ChevronRight, Copy, Database, Grid2X2, GripHorizontal, ListFilter, Minus, Moon, MoreHorizontal, PanelRightClose, PanelRightOpen, Plus, Redo2, Search, Settings2, Sun, Trash2, Undo2, WandSparkles, X } from 'lucide'
import { repeat } from 'lit/directives/repeat.js'
import { keyed } from 'lit/directives/keyed.js'
import { styleMap } from 'lit/directives/style-map.js'
import { hasCompiledBuilderPreview, isBuilderVisualTypeSwitchPending } from './builder-preview-readiness'
import { builderCatalogEntities, builderFieldCatalogGroup, type BuilderCatalogField } from './builder-field-catalog'
import { BuilderRenderCache } from './builder-render-cache'
import { canRequireFilter, filterControlChoices, filterControlLabel } from './builder-filter-settings'
import { applyCanonicalGridAttributes, builderGridOccupiedRows, refreshBuilderGridDragHandles, setBuilderPreviewResizeSuspended, syncGridStackNodesToCanonical } from './builder-grid-sync'
import { createBuilderGridDragHelper, styleBuilderGridPlaceholder } from './builder-grid-drag-preview'
import { DashboardBuilderAgentMutationTracker } from './dashboard-builder-agent-refresh'
import type {
  DashboardBuilderDiagnosticSignal,
  DashboardBuilderFieldSignal,
  DashboardBuilderFilterComponentSignal,
  DashboardBuilderFilterSignal,
  DashboardBuilderFormatOptionSignal,
  DashboardBuilderInteractionSignal,
  DashboardBuilderHeaderSignal,
  DashboardBuilderPageSignal,
  DashboardBuilderPlaceholderSignal,
  DashboardBuilderSignal,
  DashboardBuilderDatasetSignal,
  DashboardBuilderVisualSignal,
  DashboardBuilderVisualSlotSignal,
  DashboardBuilderVisualTypeSignal,
  DashboardCompiledFilterBinding,
  DashboardFilterCommand,
  DashboardFilterContract,
  DashboardFilterOptionPage,
  DashboardFilterState,
  DashboardFilterValidationResult,
  DashboardVisualizationSignal,
  DashboardStatus,
  RouteRuntimeSignal,
} from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DatastarLit } from '../shared/datastar-lit'
import { loadDatastarRuntime } from '../shared/datastar-runtime'
import { uuidv7 } from '../shared/command-identity'
import { lucideIconByCanonicalName } from '../shared/lucide-catalog'
import { lucideIcon } from '../shared/lucide-icons'
import { checkSignalContract } from '../shared/signal-contract'
import { emptyDashboardStatus } from '../shared/signal-defaults'
import { browserCommandFailure, ownsBrowserCommandFetch, type BrowserCommandFailure } from '../shared/command-failure'
import './visualization/host'
import { BuilderVisualizationState } from './builder-visualization-state'
import { arrangeDashboardVisuals } from './builder-auto-layout'
import { renderVisualTypeIcon } from './visual-type-icon'
import './filters/filter-control'
import { DashboardFilterController } from './filters/filter-controller'
import type { FilterMutationDetail, FilterOptionsNeededDetail } from './filters/filter-control'
import '../app/dashboard-icon-picker'
import '../chat/chat-drawer'
import { agentIcon } from '../chat/agent-icon'
import './visual-modal'

type BuilderVisualType = string
type BuilderFieldRole = 'dimension' | 'metric' | 'detail'
type BuilderFieldFilter = 'all' | 'metric' | 'dimension' | 'time'
type BuilderFilterControl = DashboardBuilderFilterSignal['controlType']
type BuilderFilterScope = 'report' | 'page' | 'visual' | 'custom'
type BuilderPane = 'filters' | 'visuals' | 'data' | 'agent'
type BuilderInteractionEffect = 'filter' | 'highlight' | 'none'
type BuilderResolvedTheme = 'light' | 'dark'

const builderPaneStorageKey = 'leapview-dashboard-builder-collapsed-panes'
const defaultCollapsedPanes: Record<BuilderPane, boolean> = { filters: false, visuals: false, data: false, agent: true }
const builderCanvasRunwayRows = 3

type DashboardBuilderVisualWithPreview = DashboardBuilderVisualSignal & { visualId?: string }

type DashboardBuilderVisualWithInteraction = DashboardBuilderVisualWithPreview & { interaction?: DashboardBuilderInteractionSignal }

type GridPlacement = {
  componentId: string
  placement: {
    column: number
    row: number
    columnSpan: number
    rowSpan: number
  }
}

type BuilderVisualFormatPatch = {
  title?: string
  titleVisible?: boolean
  legendVisible?: boolean
  axisVisible?: boolean
  dataLabelsVisible?: boolean
}

type BuilderRevisionReference = {
  id: string
  number: number
  contentHash: string
}

type BuilderHistorySnapshot = {
  undo: BuilderRevisionReference[]
  redo: BuilderRevisionReference[]
}

type BuilderVisualTypeSwitch = {
  pageID: string
  visualID: string
  fromType: BuilderVisualType
  toType: BuilderVisualType
  fromRevision: BuilderRevisionReference
  toRevision?: BuilderRevisionReference
}

type BuilderClipboard = {
  pageId: string
  visualId: string
}

/** Draft dashboard authoring surface. Runtime dashboard rendering remains a
 * separate component and envelope; this component only edits the bounded
 * builder projection delivered by the stream. */
class LeapViewDashboardBuilder extends DatastarLit(LitElement) {
  private readonly embeddedInChat = window.parent !== window && new URL(window.location.href).searchParams.get('embed') === 'chat'
  private chatProjectionKey = ''
  private importedVisualSources = new Map<string, string>()
  private pendingVisualSource: { componentID: string; savedID: string } | null = null
  private pendingFixVisuals: { pageID: string; visualIDs: Set<string> } | null = null
  @state() private fixVisualsMessage = ''

  private backToChat = (): void => {
    window.parent.postMessage({ type: 'lv-builder-back-to-chat' } satisfies ChatDashboardMessage, window.location.origin)
  }

  private publishChatProjection(): void {
    const builder = this.builder
    if (!this.embeddedInChat || !builder) return
    const page = this.selectedPage(builder)
    const visuals = this.builderVisuals
    const ordered = [...(page?.visuals ?? [])].sort((a, b) => a.placement.row - b.placement.row || a.placement.col - b.placement.col)
    const key = JSON.stringify([builder.revision, page?.id, builder.preview.loading, builder.capabilities.canEdit, this.commandPending, this.builderFilterController.pending, Boolean(this.builderFilterCommandInFlight), Boolean(this.pendingFixVisuals), this.fixVisualsMessage, this.toolsHidden, ordered.map(visual => {
      const envelope = visuals[this.visualSignalID(visual)]
      return [visual.id, envelope?.dataRevision, envelope?.specRevision, envelope?.status.kind]
    })])
    if (key === this.chatProjectionKey) return
    this.chatProjectionKey = key
    const href = new URL(window.location.href)
    href.searchParams.set('embed', 'chat')
    href.searchParams.delete('mode')
    if (page) href.searchParams.set('page', page.id)
    const artifactID = (pageId: string, componentId: string) => `dashboard:${builder.dashboardId}:${pageId}:${componentId}`
    window.parent.postMessage({
      type: 'lv-builder-saved', updating: this.commandPending || this.importingSavedVisual, revisionId: builder.revision.id, fixingVisuals: Boolean(this.pendingFixVisuals), fixMessage: this.fixVisualsMessage, canArrange: Boolean(builder.capabilities.canEdit && !this.commandPending && !this.builderFilterController.pending && !this.builderFilterCommandInFlight && page?.visuals.length), pageId: page?.id ?? '', pageTitle: page?.title ?? '', pages: builder.pages.map(page => ({id: page.id, title: page.title})), modelId: builder.semanticModel.id, href: href.pathname + href.search,
      reference: {
        reference: { kind: 'dashboard', id: builder.dashboardId }, name: page ? `${builder.title} · ${page.title}` : builder.title,
        hierarchy: [], href: href.pathname + href.search, locations: page ? [{dashboardId: builder.dashboardId, dashboardName: builder.title, pageId: page.id, pageName: page.title, href: href.pathname + href.search}] : [], context: ['Editable dashboard draft'],
      },
      components: builder.pages.flatMap(page => page.visuals.map(visual => ({ id: visual.id, pageId: page.id, artifactId: artifactID(page.id, visual.id), savedVisualId: (this.importedVisualSources.get(visual.id) ?? savedVisualSourceId(visual.id)) }))),
      artifacts: ordered.map(visual => ({ id: artifactID(page!.id, visual.id), type: visual.type, summary: visual.title })),
      visuals: Object.fromEntries(ordered.flatMap(visual => { const envelope = visuals[this.visualSignalID(visual)]; return envelope ? [[artifactID(page!.id, visual.id), envelope]] : [] })),
    } satisfies ChatDashboardMessage, window.location.origin)
  }

  @property({ attribute: 'back-href' }) backHref = ''
  @property({ attribute: 'fork-href' }) forkHref = ''
  @property({ attribute: 'page-base-href' }) pageBaseHref = ''
  @property({ attribute: 'preview-href' }) previewHref = ''
  @property({ attribute: 'export-yaml-href' }) exportYAMLHref = ''

  @state() private savedVisualsOpen = false
  @state() private savedVisualLibraryLoaded = false
  @state() private importingSavedVisual = false
  private readonly importFrameName = `builder-visual-import-${crypto.randomUUID()}`
  private importTimer = 0
  private importRevision: BuilderRevisionReference | null = null
  private refreshingBuilder = false
  private refreshQueued = false
  private wasAgentRunning = false
  @state() private fieldQuery = ''
  @state() private localPageID = ''
  // null follows the server's initial selection; an empty string records an
  // explicit canvas deselection without falling back to the first visual.
  @state() private localVisualID: string | null = null
  @state() private visualType: BuilderVisualType = 'bar'
  @state() private editingPage = false
  @state() private fieldFilter: BuilderFieldFilter = 'all'
  @state() private selectedFilterID = ''
  @state() private addFilterMenuOpen = false
  @state() private addFilterQuery = ''
  @state() private selectedFilterComponentID = ''
  @state() private selectedHeaderID = ''
  @state() private addingSlicer = false
  @state() private gridInteractionMessage = ''
  @state() private visualActionMessage = ''
  @state() private draggedFieldID = ''
  @state() private draggedPageID = ''
  @state() private pageDropTargetID = ''
  @state() private undoStack: BuilderRevisionReference[] = []
  @state() private redoStack: BuilderRevisionReference[] = []
  @state() private visualTypeOverrides: Record<string, BuilderVisualType> = {}
  @state() private interactionEffectOverrides: Record<string, BuilderInteractionEffect> = {}
  @state() private terminalFailure: BrowserCommandFailure | null = null
  @state() private collapsedPanes: Record<BuilderPane, boolean> = { ...defaultCollapsedPanes }
  @state() private toolsHidden = false
  @state() private appearanceOpen = false
  @state() private resolvedTheme: BuilderResolvedTheme = currentResolvedTheme()
  @state() private canvasScale = 1
  private canvasZoom: number | null = null
  private commandPending = false
  private activeCommandAction = ''
  private interactionOverridesRevision = ''
  private pendingHistorySnapshot: BuilderHistorySnapshot | null = null
  private pendingVisualTypeSwitch: BuilderVisualTypeSwitch | null = null
  private reversibleVisualTypeSwitch: BuilderVisualTypeSwitch | null = null
  private copiedVisual: BuilderClipboard | null = null
  private readonly visualizationDecoder = new BuilderVisualizationState()
  private gridInteracting = false
  private previewResizeSuspended = false
  private gridResizeSavePending = false
  private updatingBuilder = false
  private builderUpdateSnapshot: DashboardBuilderSignal | null | undefined
  private builderVisualUpdateSnapshot?: Record<string, VisualizationEnvelope>
  private builderRenderCache?: BuilderRenderCache
  private builderFilterStateFingerprint = ''
  private builderFilterValidationMutationID = ''
  private readonly filterOptionGenerations = new Map<string, number>()
  private readonly filterOptionRequestContexts = new Map<string, Map<number, string>>()
  private readonly filterOptionInFlight = new Map<string, { context: string, generation: number, startedAt: number }>()
  private readonly retainedFilterOptionPages = new Map<string, DashboardFilterOptionPage>()
  private retainedFilterOptionServingStateID = ''
  private builderFilterCommandInFlight: DashboardFilterCommand | null = null
  private builderFilterTransportError = ''
  private readonly builderFilterController = new DashboardFilterController((command) => {
    this.builderFilterCommandInFlight = command
    this.builderFilterTransportError = ''
    this.dispatchEvent(new CustomEvent('lv-builder-filter-command', { bubbles: true, composed: true, detail: command }))
    this.requestUpdate()
  })
  private gridStack: GridStack | null = null
  private gridElement: HTMLElement | null = null
  private gridLayoutKey = ''
  private gridIsMobile = false
  private gridCommitQueued = false
  private gridEditingEnabled?: boolean
  private readonly gridDragHandles = new Map<GridItemHTMLElement, Element[]>()
  private viewportMediaQuery: MediaQueryList | null = null
  private canvasResizeObserver: ResizeObserver | null = null
  private canvasViewportElement: HTMLElement | null = null
  private canvasPage: DashboardBuilderPageSignal | undefined
  private readonly agentMutationTracker = new DashboardBuilderAgentMutationTracker()

  // Add-page uses server-generated identifiers. Keep the page set that was
  // visible when the intent was sent so the authoritative response can select
  // the page created by that intent, even when the response's selectedPageId
  // still reflects the page that was active before the mutation.
  private pendingAddPage: { revision: string; pageIDs: Set<string> } | null = null
  private pendingRemovePage: { revision: string; pageID: string; visualID: string | null } | null = null
  private activeCommandRevisionKey = ''
  private autoArrangePageID = ''
  private autoArrangePreservedIDs = new Set<string>()
  private pendingAddVisual: { revision: string; visualIDs: Set<string>; pageID: string; autoArrange?: boolean } | null = null
  private pendingAddFilter: { revision: string; filterIDs: Set<string> } | null = null
  private pendingAddFilterComponent: { revision: string; componentIDs: Set<string>; pageID: string } | null = null
  private pendingAddSlicer: { revision: string; filterIDs: Set<string>; componentIDs: Set<string>; pageID: string } | null = null

  override connectedCallback(): void {
    super.connectedCallback()
    this.restoreCollapsedPanes()
    window.addEventListener('message', this.handleSavedVisualMessage)
    window.addEventListener('message', this.handleVisualImportMessage)
    this.addEventListener('lv-add-agent-visual', this.handleAgentVisualAdd as EventListener)
    this.addEventListener('lv-saved-visuals-changed', this.refreshSavedVisualLibrary)
    this.addEventListener('drop', this.dropSavedVisual, { capture: true })
    this.addEventListener('dragover', this.allowSavedVisualDrop, { capture: true })
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
    document.addEventListener('datastar-signal-patch', this.handleVisualSignalPatch)
    document.addEventListener('leapview-theme-applied', this.handleThemeApplied)
    document.addEventListener('pointerdown', this.handleToolbarPointerDown)
    this.addEventListener('lv-filter-mutate', this.handleBuilderFilterMutation as EventListener, { capture: true })
    this.addEventListener('lv-filter-options-needed', this.handleBuilderFilterOptionsNeeded as EventListener, { capture: true })
    if (typeof window !== 'undefined') {
      window.addEventListener('keydown', this.handleBuilderKeydown)
      this.viewportMediaQuery = window.matchMedia('(max-width: 640px)')
      this.viewportMediaQuery.addEventListener('change', this.handleViewportChange)
    }
  }

  override disconnectedCallback(): void {
    window.removeEventListener('message', this.handleSavedVisualMessage)
    window.removeEventListener('message', this.handleVisualImportMessage)
    window.clearTimeout(this.importTimer)
    this.removeEventListener('lv-add-agent-visual', this.handleAgentVisualAdd as EventListener)
    this.removeEventListener('lv-saved-visuals-changed', this.refreshSavedVisualLibrary)
    this.removeEventListener('drop', this.dropSavedVisual, { capture: true })
    this.removeEventListener('dragover', this.allowSavedVisualDrop, { capture: true })
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    document.removeEventListener('datastar-signal-patch', this.handleVisualSignalPatch)
    document.removeEventListener('leapview-theme-applied', this.handleThemeApplied)
    document.removeEventListener('pointerdown', this.handleToolbarPointerDown)
    this.removeEventListener('lv-filter-mutate', this.handleBuilderFilterMutation as EventListener, { capture: true })
    this.removeEventListener('lv-filter-options-needed', this.handleBuilderFilterOptionsNeeded as EventListener, { capture: true })
    if (typeof window !== 'undefined') window.removeEventListener('keydown', this.handleBuilderKeydown)
    this.viewportMediaQuery?.removeEventListener('change', this.handleViewportChange)
    this.viewportMediaQuery = null
    this.destroyCanvasViewportObserver()
    this.destroyGridStack()
    this.canvasPage = undefined
    super.disconnectedCallback()
  }

  static styles = [dashboardBuilderSurfaceStyles, dashboardBuilderControlsStyles, dashboardBuilderCanvasStyles, dashboardBuilderFieldsStyles, dashboardBuilderDialogsStyles, dashboardBuilderDensityStyles]

  override performUpdate(): void {
    // Share one materialized dashboard snapshot across helpers for this update.
    // Never retain it: Datastar can patch nested fields without replacing the root.
    this.updatingBuilder = true
    this.builderUpdateSnapshot = undefined
    this.builderVisualUpdateSnapshot = undefined
    this.builderRenderCache = new BuilderRenderCache()
    try {
      super.performUpdate()
    } finally {
      this.updatingBuilder = false
      this.builderUpdateSnapshot = undefined
      this.builderVisualUpdateSnapshot = undefined
      this.builderRenderCache = undefined
    }
  }

  updated(): void {
    // Persist the server-validated chat origin so reload keeps the same Back target.
    if (this.backHref.startsWith('/chats/')) {
      const current = new URL(window.location.href)
      const conversation = this.backHref.slice('/chats/'.length)
      if (current.searchParams.get('returnChat') !== conversation) {
        current.searchParams.set('returnChat', conversation)
        window.history.replaceState(window.history.state, '', current)
      }
    }
    if (this.agentMutationTracker.observe(this.signal('agent', {}))) this.dispatchEvent(new CustomEvent('lv-builder-agent-run-complete', { bubbles: true, composed: true }))
    const builder = this.builder
    const agentRunning = Boolean(this.signal<{ status?: { running?: boolean } }>('agent', {}).status?.running)
    if (this.wasAgentRunning && !agentRunning) this.refreshBuilderSignals()
    this.wasAgentRunning = agentRunning
    if (builder?.redirectTo) {
      if (this.embeddedInChat) { this.backToChat(); return }
      const target = new URL(builder.redirectTo, window.location.href)
      if (target.origin === window.location.origin && target.href !== window.location.href) window.location.assign(target.href)
      return
    }
    checkSignalContract('dashboard builder', builder, {
      projectId: 'required',
      dashboardId: 'required',
      draftId: 'required',
      revision: 'required',
      semanticModel: 'required',
      pages: 'required',
      capabilities: 'required',
      diagnostics: 'required',
      preview: 'required',
      save: 'required',
    })
    this.reconcileVisualTypeOverrides(builder)
    this.reconcileVisualTypeSwitch(builder)
    this.reconcileInteractionEffectOverrides(builder)
    this.selectPendingAddedPage(builder)
    this.reconcilePendingRemovedPage(builder)
    this.selectPendingAddedVisual(builder)
    this.selectPendingAddedFilter(builder)
    this.selectPendingAddedFilterComponent(builder)
    this.selectPendingAddedSlicer(builder)
    this.reconcileBuilderFilterController()
    const page = builder ? this.selectedPage(builder) : undefined
    this.canvasPage = page
    this.syncGridStack(builder, page)
    this.syncCanvasViewport(page)
    if (this.embeddedInChat) {
      for (const link of this.shadowRoot?.querySelectorAll<HTMLAnchorElement>('a[href]') ?? []) {
        const target = new URL(link.href, window.location.href)
        if (target.origin === window.location.origin && target.pathname.endsWith('/edit')) {
          target.searchParams.set('embed', 'chat')
          link.href = target.href
        }
      }
    }
    if (this.autoArrangePageID && !this.commandPending && !this.importingSavedVisual) {
      const pageID = this.autoArrangePageID
      const preservedIDs = this.autoArrangePreservedIDs
      this.autoArrangePageID = ''
      this.autoArrangePreservedIDs = new Set()
      if (!this.status.error && page?.id === pageID) this.applyBalancedLayout(false, false, preservedIDs)
    }
    this.publishChatProjection()
  }

  private readonly handleViewportChange = (): void => {
    this.requestUpdate()
  }

  private isMobileViewport(): boolean {
    return this.viewportMediaQuery?.matches ?? (typeof window !== 'undefined' && window.innerWidth <= 640)
  }

  private syncGridStack(builder: DashboardBuilderSignal | null, page: DashboardBuilderPageSignal | undefined): void {
    const canvas = this.shadowRoot?.querySelector('.canvas.grid-stack') as HTMLElement | null
    const mobile = this.isMobileViewport()
    const layoutKey = page ? JSON.stringify([page.id, page.grid.columns, page.grid.rowHeight, page.grid.gap,
      [page.visuals.length, page.filterComponents?.length ?? 0, page.headers?.length ?? 0, page.placeholders?.length ?? 0],
      this.pagePlacedComponents(page).map((component) => component.id),
    ]) : ''
    const placementKey = page ? JSON.stringify(this.pagePlacedComponents(page).map(({ id, placement }) => [id, placement.col, placement.row, placement.colSpan, placement.rowSpan])) : ''
    if (!canvas || !page || mobile) {
      this.destroyGridStack()
      this.gridIsMobile = mobile
      return
    }
    if (canvas !== this.gridElement || layoutKey !== this.gridLayoutKey || mobile !== this.gridIsMobile) {
      this.destroyGridStack()
      applyCanonicalGridAttributes(this.shadowRoot, this.pagePlacedComponents(page))
      this.gridElement = canvas
      this.gridLayoutKey = layoutKey
      Object.assign(canvas.dataset, { builderRevisionKey: this.revisionKey(builder!), builderPlacementKey: placementKey })
      this.gridIsMobile = mobile
      // Destroying the old grid restores its mutable attributes after Lit has
      // rendered the new revision. Seed the new grid from canonical geometry.
      const placements = new Map(this.pagePlacedComponents(page).map(component => [component.id, component.placement]))
      const seen = new Set<string>()
      for (const item of canvas.querySelectorAll<HTMLElement>(':scope > .grid-stack-item')) {
        const id = item.getAttribute('gs-id') ?? ''
        const placement = placements.get(id)
        // Drag/resize teardown can reinsert a tile Lit already removed. Keep
        // exactly one DOM tile for each component in the current document.
        if (!placement || seen.has(id)) { item.remove(); continue }
        seen.add(id)
        item.setAttribute('gs-x', String(Math.max(0, placement.col - 1)))
        item.setAttribute('gs-y', String(Math.max(0, placement.row - 1)))
        item.setAttribute('gs-w', String(Math.max(1, placement.colSpan)))
        item.setAttribute('gs-h', String(Math.max(1, placement.rowSpan)))
      }
      this.gridStack = GridStack.init({
        column: Math.max(1, page.grid.columns || 12),
        // GridStack's cell height is the authored row plus its following gap.
        cellHeight: Math.max(1, (page.grid.rowHeight || 48) + (page.grid.gap || 0)),
        margin: Math.max(0, Math.round((page.grid.gap ?? 16) / 2)),
        animate: false,
        mode: 'float',
        disableDrag: !builder?.capabilities.canEdit || this.commandPending,
        disableResize: !builder?.capabilities.canEdit || this.commandPending,
        draggable: { handle: '.component-drag-handle', helper: createBuilderGridDragHelper, appendTo: 'parent' },
        resizable: { handles: 'all', autoHide: false },
      }, canvas as GridItemHTMLElement)
      if (this.gridStack) {
        syncGridStackNodesToCanonical(this.gridStack, this.shadowRoot, this.pagePlacedComponents(page))
        this.gridStack.on('dragstart resizestart', (event: Event) => {
          this.gridInteracting = true
          if (event.type === 'resizestart') this.setPreviewResizeSuspended(true)
          if (event.type === 'dragstart') queueMicrotask(() => styleBuilderGridPlaceholder(this.shadowRoot))
          this.syncCanvasViewport(this.canvasPage)
        })
        this.gridStack.on('dragstop resizestop', (event: Event, element: GridItemHTMLElement) => this.onGridInteractionStop(element, event.type === 'resizestop'))
        this.gridStack.on('drag', () => this.syncCanvasViewport(this.canvasPage))
        this.gridStack.on('resize', () => this.syncCanvasViewport(this.canvasPage))
        this.gridStack.on('change', (event: Event, nodes: GridStackNode[]) => this.onGridChange(event, nodes))
      }
    } else if (this.gridStack && !this.gridInteracting && (this.revisionKey(builder!) !== canvas.dataset.builderRevisionKey || placementKey !== canvas.dataset.builderPlacementKey)) {
      syncGridStackNodesToCanonical(this.gridStack, this.shadowRoot, this.pagePlacedComponents(page))
      Object.assign(canvas.dataset, { builderRevisionKey: this.revisionKey(builder!), builderPlacementKey: placementKey })
    }
    this.setGridEditingEnabled(Boolean(builder?.capabilities.canEdit && !this.commandPending))
    this.syncGridDragHandles()
  }

  private syncGridDragHandles(): void {
    if (this.gridStack && !this.gridInteracting) refreshBuilderGridDragHandles(this.gridStack, this.gridDragHandles)
  }

  private destroyGridStack(): void {
    this.gridResizeSavePending = false
    this.setPreviewResizeSuspended(false)
    this.gridInteracting = false
    if (this.gridStack) this.gridStack.destroy(false)
    this.gridStack = null
    this.gridElement = null
    this.gridLayoutKey = ''
    this.gridCommitQueued = false
    this.gridEditingEnabled = undefined
    this.gridDragHandles.clear()
  }

  private setGridEditingEnabled(enabled: boolean): void {
    if (!this.gridStack || enabled === this.gridEditingEnabled) return
    this.gridStack.enableMove(enabled)
    this.gridStack.enableResize(enabled)
    this.gridEditingEnabled = enabled
  }

  private onGridInteractionStop(_element: GridItemHTMLElement, resized: boolean): void {
    this.gridInteracting = false
    this.syncGridDragHandles()
    this.syncCanvasViewport(this.canvasPage)
    // The server may adjust neighboring placements before acknowledging the
    // save. Keep the charts paused until that final geometry is in the DOM so
    // one gesture cannot trigger two expensive renderer resizes in succession.
    this.gridResizeSavePending = resized
    this.gridInteractionMessage = 'Layout updated.'
    this.scheduleGridCommit()
  }

  private resumeGridPreviewAfterSave(): void {
    if (!this.gridResizeSavePending) return
    this.gridResizeSavePending = false
    this.setPreviewResizeSuspended(false)
  }

  private setPreviewResizeSuspended(suspended: boolean): void {
    this.previewResizeSuspended = suspended
    setBuilderPreviewResizeSuspended(this.shadowRoot, suspended)
  }

  private onGridChange(_event: Event, _nodes: GridStackNode[]): void {
    if (this.gridInteracting) return
    this.syncCanvasViewport(this.canvasPage)
    this.scheduleGridCommit()
  }

  private syncCanvasViewport(page: DashboardBuilderPageSignal | undefined): void {
    const scroll = this.shadowRoot?.querySelector('.canvas-scroll') as HTMLElement | null
    const fit = this.shadowRoot?.querySelector('.canvas-fit') as HTMLElement | null
    const canvas = this.shadowRoot?.querySelector('.canvas') as HTMLElement | null
    if (!scroll || !fit || !canvas || !page || this.isMobileViewport()) {
      this.destroyCanvasViewportObserver()
      fit?.style.removeProperty('--builder-canvas-fitted-width')
      fit?.style.removeProperty('--builder-canvas-fitted-height')
      canvas?.style.removeProperty('--builder-canvas-scale')
      canvas?.style.removeProperty('min-height')
      canvas?.style.removeProperty('height')
      if (this.canvasScale !== 1) this.canvasScale = 1
      return
    }
    if (scroll !== this.canvasViewportElement && typeof ResizeObserver !== 'undefined') {
      this.destroyCanvasViewportObserver()
      this.canvasViewportElement = scroll
      this.canvasResizeObserver = new ResizeObserver(() => this.syncCanvasViewport(this.canvasPage))
      this.canvasResizeObserver.observe(scroll)
    }
    const availableWidth = scroll.clientWidth
    if (availableWidth <= 0) return
    const logicalWidth = page.canvas.width > 0 ? page.canvas.width : builderCanvasDesktopWidth
    const minimumHeight = page.canvas.height > 0 ? page.canvas.height : builderCanvasMinimumHeight
    const fitScale = Math.min(1, availableWidth / logicalWidth)
    const scale = Math.min(2, Math.max(0.25, this.canvasZoom ?? fitScale))
    const occupiedRows = builderGridOccupiedRows(this.gridStack, this.gridStack ? undefined : this.pagePlacedComponents(page))
    const workingRows = occupiedRows + (this.gridInteracting || this.draggedFieldID ? builderCanvasRunwayRows : 0)
    const rowHeight = Math.max(1, page.grid.rowHeight || 48)
    const gap = Math.max(0, page.grid.gap || 0)
    const padding = Math.max(0, page.grid.padding || 0)
    const contentHeight = workingRows > 0
      ? padding * 2 + workingRows * (rowHeight + gap)
      : padding * 2
    const logicalHeight = Math.max(minimumHeight, contentHeight)
    const inset = Math.min(padding, Math.max(0, (Math.min(logicalWidth, logicalHeight) - 1) / 2))
    const gridWidth = Math.max(1, logicalWidth - inset * 2)
    const innerHeight = Math.max(1, logicalHeight - inset * 2)
    const setStyle = (element: HTMLElement, name: string, value: string): void => {
      if (element.style.getPropertyValue(name) !== value) element.style.setProperty(name, value)
    }
    setStyle(fit, '--builder-canvas-fitted-width', `${logicalWidth * scale}px`)
    setStyle(fit, '--builder-canvas-fitted-height', `${logicalHeight * scale}px`)
    setStyle(canvas, '--builder-canvas-scale', String(scale))
    setStyle(canvas, '--builder-grid-offset', `${inset * scale}px`)
    setStyle(canvas, '--builder-grid-width', `${gridWidth}px`)
    setStyle(canvas, '--builder-grid-columns', String(Math.max(1, page.grid.columns || 12)))
    setStyle(canvas, '--builder-grid-row-pitch', `${rowHeight + gap}px`)
    const height = `${innerHeight}px`
    if (canvas.style.minHeight !== height) canvas.style.minHeight = height
    if (canvas.style.height !== height) canvas.style.height = height
    if (Math.abs(this.canvasScale - scale) > 0.0001) this.canvasScale = scale
  }

  private readonly changeCanvasZoom = (delta: number): void => {
    this.canvasZoom = Math.min(2, Math.max(0.25, this.canvasScale + delta))
    this.syncCanvasViewport(this.builder ? this.selectedPage(this.builder) : undefined)
  }

  private readonly fitCanvasToViewport = (): void => {
    this.canvasZoom = null
    this.syncCanvasViewport(this.builder ? this.selectedPage(this.builder) : undefined)
  }

  private destroyCanvasViewportObserver(): void {
    this.canvasResizeObserver?.disconnect()
    this.canvasResizeObserver = null
    this.canvasViewportElement = null
  }

  private scheduleGridCommit(): void {
    if (this.gridCommitQueued) return
    if (!this.gridStack || this.isMobileViewport() || !this.builder?.capabilities.canEdit) {
      this.resumeGridPreviewAfterSave()
      return
    }
    if (this.commandPending) return
    this.gridCommitQueued = true
    queueMicrotask(() => {
      this.gridCommitQueued = false
      this.commitGridPlacements()
    })
  }

  private commitGridPlacements(): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (this.commandPending) return
    if (!builder || !page || !this.gridStack || this.isMobileViewport() || !builder.capabilities.canEdit) {
      this.resumeGridPreviewAfterSave()
      return
    }
    const nodes = new Map(this.gridStack.getGridItems().map((item) => [item.gridstackNode?.id || item.getAttribute('gs-id') || '', item.gridstackNode]))
    const components = this.pageEditableComponents(page)
    const placements: GridPlacement[] = components.map((component) => {
      const node = nodes.get(component.id)
      return {
        componentId: component.id,
        placement: {
          column: Math.max(1, Math.round((node?.x ?? component.placement.col - 1) + 1)),
          row: Math.max(1, Math.round((node?.y ?? component.placement.row - 1) + 1)),
          columnSpan: Math.max(1, Math.round(node?.w ?? component.placement.colSpan)),
          rowSpan: Math.max(1, Math.round(node?.h ?? component.placement.rowSpan)),
        },
      }
    })
    if (placements.every((placement, index) => this.placementEqual(placement, components[index].placement))) {
      this.resumeGridPreviewAfterSave()
      return
    }
    if (!this.gridInteractionMessage) this.gridInteractionMessage = 'Layout updated.'
    // A manually placed or resized tile is authoritative. Page-wide packing
    // here would move unrelated tiles and resize their renderers after release.
    this.emitCommand('set_placements', { pageId: page.id, placements, compact: false })
  }

  private readonly arrangeVisuals = (): void => {
    // Fix repairs missing fields using the currently authored layout. In a
    // standalone builder, Arrange remains an explicit request to repack.
    if (this.builderFilterController.pending || this.builderFilterCommandInFlight) {
      this.chatProjectionKey = ''
      this.requestUpdate()
      return
    }
    const page = this.builder ? this.selectedPage(this.builder) : undefined
    if (this.embeddedInChat && page?.visuals.length && this.builder?.capabilities.canEdit && !this.commandPending) {
      this.pendingFixVisuals = { pageID: page.id, visualIDs: new Set(page.visuals.filter(visual => this.visualNeedsRepair(visual)).map(visual => visual.id)) }
      this.fixVisualsMessage = ''
      if (this.pendingFixVisuals.visualIDs.size === 0) {
        this.finishFixVisuals()
        this.chatProjectionKey = ''
        this.requestUpdate()
        return
      }
    }
    const preservedIDs = new Set(this.embeddedInChat ? page?.visuals.map(visual => visual.id) : [])
    this.applyBalancedLayout(true, true, preservedIDs)
    // A layout that is already balanced still acknowledges the parent click.
    this.chatProjectionKey = ''
    this.requestUpdate()
  }

  private finishFixVisuals(error?: string): void {
    const pending = this.pendingFixVisuals
    if (!pending) return
    this.pendingFixVisuals = null
    if (error) { this.fixVisualsMessage = error; return }
    const page = this.builder?.pages.find(page => page.id === pending.pageID)
    const remaining = page?.visuals.filter(visual => this.visualNeedsRepair(visual)) ?? []
    const completed = page?.visuals.filter(visual => pending.visualIDs.has(visual.id) && !this.visualNeedsRepair(visual)).length ?? 0
    const summary = completed > 0 ? `Completed ${completed} visual${completed === 1 ? '' : 's'}. ` : ''
    this.fixVisualsMessage = remaining.length > 0
      ? `${summary}${remaining.length} visual${remaining.length === 1 ? ' could not be completed automatically' : 's could not be completed automatically'}. Select ${remaining.length === 1 ? 'it' : 'them'} in the Visuals panel.`
      : completed > 0 ? `${summary}Your layout is unchanged.` : 'Visuals are ready. Your layout is unchanged.'
  }

  private isFixedVisualReady(visual: DashboardBuilderVisualSignal): boolean {
    const preview = this.builderVisuals[this.visualSignalID(visual)]
    return Boolean(this.builder?.preview.active && !visual.previewError && preview && ['ready', 'no_data', 'partial'].includes(preview.status.kind))
  }

  private visualNeedsRepair(visual: DashboardBuilderVisualSignal): boolean {
    return Boolean(visual.previewError?.trim()) || this.visualRequirementMessages(visual).length > 0
  }

  private applyBalancedLayout(recordHistory: boolean, fillMissingFields = false, preservedIDs: ReadonlySet<string> = new Set()): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || this.commandPending || page.visuals.length === 0) return
    const obstacles = this.pagePlacedComponents(page).filter(component => !page.visuals.some(visual => visual.id === component.id))
    const placements = arrangeDashboardVisuals([...page.visuals].sort((a, b) => a.placement.row - b.placement.row || a.placement.col - b.placement.col).map(visual => ({ id: visual.id, type: this.visualTypeForRender(visual), placement: preservedIDs.has(visual.id) ? visual.placement : undefined })), obstacles.map(component => component.placement), page.grid)
    if (!fillMissingFields && placements.every(p => this.placementEqual(p, page.visuals.find(visual => visual.id === p.componentId)!.placement))) {
      if (preservedIDs.size === 0) this.fitCanvasToViewport()
      return
    }
    if (preservedIDs.size === 0) this.canvasZoom = null
    this.gridInteractionMessage = fillMissingFields && preservedIDs.size > 0 ? 'Completing visuals while keeping your layout.' : 'Arranging dashboard visuals.'
    this.emitCommand('set_placements', { pageId: page.id, placements, ...(fillMissingFields ? { fillMissingFields: true } : {}) }, recordHistory)
  }

  private placementEqual(left: GridPlacement, right: DashboardBuilderVisualSignal['placement']): boolean {
    return left.placement.column === right.col && left.placement.row === right.row && left.placement.columnSpan === right.colSpan && left.placement.rowSpan === right.rowSpan
  }

  private pagePlacedComponents(page: DashboardBuilderPageSignal): Array<DashboardBuilderVisualSignal | DashboardBuilderFilterComponentSignal | DashboardBuilderHeaderSignal | DashboardBuilderPlaceholderSignal> {
    return [...page.visuals, ...(page.filterComponents ?? []), ...(page.headers ?? []), ...(page.placeholders ?? [])]
  }

  private pageEditableComponents(page: DashboardBuilderPageSignal): Array<DashboardBuilderVisualSignal | DashboardBuilderFilterComponentSignal | DashboardBuilderHeaderSignal> {
    return [...page.visuals, ...(page.filterComponents ?? []), ...(page.headers ?? [])]
  }

  get builder(): DashboardBuilderSignal | null {
    if (!this.updatingBuilder) return this.signal<DashboardBuilderSignal | null>('builder', null)
    if (this.builderUpdateSnapshot === undefined) {
      this.builderUpdateSnapshot = this.signal<DashboardBuilderSignal | null>('builder', null)
    }
    return this.builderUpdateSnapshot
  }

  get status(): DashboardStatus {
    return this.signal<DashboardStatus>('status', emptyDashboardStatus())
  }

  get builderVisuals(): Record<string, VisualizationEnvelope> {
    if (this.updatingBuilder && this.builderVisualUpdateSnapshot) return this.builderVisualUpdateSnapshot
    const builder = this.builder
    const runtime = this.signal<RouteRuntimeSignal>('runtime', { kind: 'dashboard_builder' })
    const previews = this.visualizationDecoder.decode(
      this.signal<Record<string, DashboardVisualizationSignal>>('builderVisuals', {}) ?? {},
      runtime.servingStateId ?? '',
      builder ? this.selectedPage(builder)?.id ?? '' : '',
      this.builderFilterState.revision,
    )
    if (this.updatingBuilder) this.builderVisualUpdateSnapshot = previews
    return previews
  }

  private get builderFilterContract(): DashboardFilterContract {
    const value = this.signal<DashboardFilterContract>('builderFilterContract', { applicationMode: 'immediate', definitions: {}, bindings: {} })
    return { applicationMode: value.applicationMode || 'immediate', definitions: value.definitions ?? {}, bindings: value.bindings ?? {} }
  }

  private get builderFilterState(): DashboardFilterState {
    const value = this.signal<DashboardFilterState>('builderFilterState', { revision: 0, appliedControls: {}, draftControls: {}, dirtyBindings: [], defaultsRevision: '' })
    return { ...value, appliedControls: value.appliedControls ?? {}, draftControls: value.draftControls ?? {}, dirtyBindings: value.dirtyBindings ?? [] }
  }

  private get rawBuilderFilterOptionPages(): Record<string, DashboardFilterOptionPage> {
    return this.signal<Record<string, DashboardFilterOptionPage> | null>('builderFilterOptionPages', {}) ?? {}
  }

  private get builderFilterOptionPages(): Record<string, DashboardFilterOptionPage> {
    const runtime = this.signal<RouteRuntimeSignal>('runtime', { kind: 'dashboard_builder' })
    const servingStateID = runtime.servingStateId ?? ''
    const state = this.builderFilterState
    const builder = this.builder
    const pageID = builder ? this.selectedPage(builder)?.id ?? '' : ''
    this.resetBuilderFilterOptionCache(servingStateID)

    const isCurrent = (key: string, page: DashboardFilterOptionPage): boolean => {
      if (page.bindingKey !== key || page.servingStateID !== servingStateID || page.filterRevision !== state.revision) return false
      const binding = this.builderFilterContract.bindings[key]
      if (!binding) return false
      const generation = this.filterOptionGenerations.get(key)
      const requestContext = this.filterOptionRequestContexts.get(key)?.get(page.requestGeneration)
      const currentContext = this.builderFilterOptionContext(binding, pageID)
      // Bootstrap may contain an option page before the first leaf has had a
      // chance to request it. Keep the same fail-closed revision/generation
      // checks as the dashboard surface for that initial response, while all
      // subsequent pages must match the exact request that is current.
      const initialPage = generation === undefined
        && page.requestGeneration > 0
        && page.streamGeneration === this.status.generation
      return (generation !== undefined && page.requestGeneration === generation && requestContext === currentContext)
        || (initialPage && requestContext === undefined)
    }

    for (const [key, page] of Object.entries(this.rawBuilderFilterOptionPages)) {
      if (!isCurrent(key, page)) continue
      this.retainedFilterOptionPages.set(key, page)
      const inFlight = this.filterOptionInFlight.get(key)
      if (inFlight && page.requestGeneration >= inFlight.generation) this.filterOptionInFlight.delete(key)
    }
    // Do not continue to expose a page after its revision, generation, or
    // dependency context has changed. The leaf will retain selected values
    // while the replacement request is in flight.
    for (const [key, page] of this.retainedFilterOptionPages) {
      if (!isCurrent(key, page)) this.retainedFilterOptionPages.delete(key)
    }
    return Object.fromEntries(this.retainedFilterOptionPages)
  }

  private resetBuilderFilterOptionCache(servingStateID: string): void {
    if (this.retainedFilterOptionServingStateID === servingStateID) return
    this.retainedFilterOptionServingStateID = servingStateID
    this.retainedFilterOptionPages.clear()
    this.filterOptionGenerations.clear()
    this.filterOptionRequestContexts.clear()
    this.filterOptionInFlight.clear()
  }

  private restoreCollapsedPanes(): void {
    if (typeof window === 'undefined') return
    try {
      const stored = window.localStorage.getItem(builderPaneStorageKey + (this.embeddedInChat ? '-chat' : ''))
      if (stored === null) return
      const value = JSON.parse(stored)
      // Old chat preferences came from the automatic one-panel layout. Reset
      // those once; version 3 stores only the user's independent choices.
      const legacy = !this.embeddedInChat && Array.isArray(value)
      const version = this.embeddedInChat ? 3 : 2
      const panes = legacy ? value : value?.version === version && Array.isArray(value.collapsed) ? value.collapsed : null
      if (!panes) return
      const collapsed = new Set(panes.filter((pane: unknown): pane is BuilderPane => pane === 'filters' || pane === 'visuals' || pane === 'data' || pane === 'agent'))
      this.collapsedPanes = {
        filters: collapsed.has('filters'),
        visuals: collapsed.has('visuals'),
        data: collapsed.has('data'),
        agent: legacy ? true : collapsed.has('agent'),
      }
      if (this.embeddedInChat) { this.collapsedPanes.agent = true; this.collapsedPanes.visuals = false }
    } catch {
      this.collapsedPanes = { ...defaultCollapsedPanes }
    }
  }

  private persistCollapsedPanes(): void {
    if (typeof window === 'undefined') return
    try {
      const collapsed = (Object.keys(this.collapsedPanes) as BuilderPane[]).filter((pane) => this.collapsedPanes[pane])
      window.localStorage.setItem(builderPaneStorageKey + (this.embeddedInChat ? '-chat' : ''), JSON.stringify({ version: this.embeddedInChat ? 3 : 2, collapsed }))
    } catch {
      // Storage can be unavailable in hardened browser contexts. The current
      // session still keeps the pane state through this reactive property.
    }
  }

  private togglePane = (pane: BuilderPane): void => {
    const opening = this.collapsedPanes[pane]
    if (opening && pane === 'data') this.savedVisualsOpen = false
    // Each section changes independently and retains its DOM and draft.
    this.collapsedPanes = { ...this.collapsedPanes, [pane]: !opening }
    this.persistCollapsedPanes()
  }

  private toggleTools = (): void => {
    // Hiding the dock is distinct from collapsing its individual sections.
    // Keep headers available even when every section is collapsed.
    this.toolsHidden = !this.toolsHidden
  }

  private closeAgentPane = (): void => {
    if (this.collapsedPanes.agent) return
    this.collapsedPanes = { ...this.collapsedPanes, agent: true }
    this.persistCollapsedPanes()
  }

  private rightDockStyle(): string {
    const size = (pane: BuilderPane, open: string) => pane === 'agent' && this.embeddedInChat ? '0px' : this.collapsedPanes[pane] ? '2.75rem' : open
    const rowSize = (pane: BuilderPane) => pane === 'agent' && this.embeddedInChat ? '0px' : this.collapsedPanes[pane] ? '2.5rem' : 'minmax(0, 1fr)'
    return [
      `--dock-filters-width:${size('filters', '11.5rem')}`,
      `--dock-visuals-width:${size('visuals', '13.5rem')}`,
      `--dock-data-width:${size('data', '11.5rem')}`,
      `--dock-agent-width:${size('agent', '19rem')}`,
      `--dock-filters-flex:${size('filters', 'minmax(0, 1fr)')}`,
      // The medium-width layout places the authoring panes below the canvas.
      // Keep the visual inspector wide enough for its field wells and action
      // controls instead of dividing the dock into equally narrow columns.
      `--dock-visuals-flex:${size('visuals', 'minmax(15rem, 1.35fr)')}`,
      `--dock-data-flex:${size('data', 'minmax(0, 1fr)')}`,
      `--dock-agent-flex:${size('agent', 'minmax(0, 1fr)')}`,
      `--dock-filters-row:${rowSize('filters')}`,
      `--dock-visuals-row:${rowSize('visuals')}`,
      `--dock-data-row:${rowSize('data')}`,
      `--dock-agent-row:${rowSize('agent')}`,
    ].join(';')
  }

  private renderPaneToggle(pane: BuilderPane, label: string, controls: string) {
    const collapsed = this.collapsedPanes[pane]
    const action = collapsed ? 'Expand' : 'Collapse'
    return html`
      <button
        type="button"
        class="pane-collapse"
        data-pane-toggle=${pane}
        aria-label=${`${action} ${label}`}
        aria-controls=${controls}
        aria-expanded=${!collapsed}
        title=${`${action} ${label}`}
        @click=${() => this.togglePane(pane)}
      >${lucideIcon(collapsed ? PanelRightOpen : PanelRightClose, { size: 16, strokeWidth: 2 })}</button>
    `
  }

  private get builderFilterValidation(): DashboardFilterValidationResult {
    return this.signal<DashboardFilterValidationResult>('builderFilterValidation', { accepted: true, message: '', currentRevision: this.builderFilterState.revision, clientMutationID: '' })
  }

  render() {
    const builder = this.builder
    if (!builder) {
      const status = this.status
      if (status.error) {
        return html`<section class="state" role="alert" aria-live="assertive">
          <div><strong>Dashboard builder could not load</strong><span>${status.error}</span></div>
        </section>`
      }
      return html`<section class="state" aria-live="polite"><div><strong>Loading dashboard builder…</strong><span>Preparing the draft dashboard.</span></div></section>`
    }
    const page = this.selectedPage(builder)
    const visual = page ? this.selectedVisual(page, builder) : undefined
    const header = page ? this.selectedHeader(page) : undefined
    return html`
      <section class=${`builder${this.embeddedInChat ? ' chat-preview' : ''}`} aria-label="Dashboard builder">
        ${this.renderTerminalFailure()}
        <div class="builder-header">${this.renderToolbar(builder)}</div>
        <div class=${`body${this.toolsHidden ? ' tools-hidden' : ''}`}>
          ${this.renderCanvas(builder, page)}
          ${this.renderPageBar(builder, page)}
          <div id="builder-tools" class="right-dock" ?hidden=${this.toolsHidden} style=${this.rightDockStyle()}>
            ${this.renderFiltersPane(builder)}
            ${this.renderInspector(builder, page, visual, header)}
            ${this.renderDataPane(builder, visual)}
            ${this.renderAgentPane()}
          </div>
        </div>
      </section>
      <iframe class="visual-import-frame" name=${this.importFrameName} title="Visual import" hidden @load=${this.handleVisualImportLoad}></iframe>
      <lv-visual-modal></lv-visual-modal>
    `
  }

  private renderToolbar(builder: DashboardBuilderSignal) {
    const saveState = builder.save.state
    const blockingDiagnostics = builder.diagnostics.filter((item) => item.severity === 'error')
    const previewValidation = this.previewValidationMessage(builder)
    const publishing = this.commandPending && this.activeCommandAction === 'publish'
    const publishDisabled = this.commandPending || !builder.hasUnpublishedChanges || blockingDiagnostics.length > 0 || Boolean(previewValidation)
    const publishLabel = publishing ? 'Publishing…' : builder.hasUnpublishedChanges ? 'Publish' : 'Published'
    const publishTitle = blockingDiagnostics.length > 0
      ? `Fix ${blockingDiagnostics.length} validation ${blockingDiagnostics.length === 1 ? 'error' : 'errors'} before publishing`
      : previewValidation
        ? previewValidation
      : !builder.hasUnpublishedChanges ? 'This revision is already published' : 'Publish this dashboard revision'
    const canDelete = this.canDeleteDashboard(builder)
    const hasMoreActions = builder.capabilities.canShare || builder.capabilities.canExport || (builder.capabilities.canArchive && !canDelete) || canDelete || Boolean(this.forkHref)
    const appearanceColor = dashboardAppearanceColor(builder.appearance.color)
    return html`
      <header class="toolbar">
        ${this.embeddedInChat ? nothing : this.backHref ? html`<a class="back" href=${this.backHref} aria-label=${this.backHref.startsWith('/chats/') ? 'Back to chat' : 'Back to dashboards'}>Back</a>` : html`<span class="back" aria-label="Back to dashboards">Back</span>`}
        <div class="appearance-control">
          <button
            type="button"
            class=${`appearance-trigger appearance-color-${appearanceColor}`}
            data-builder-action="appearance"
            data-appearance-color=${appearanceColor}
            aria-label="Change dashboard icon and color"
            aria-expanded=${this.appearanceOpen}
            ?disabled=${!builder.capabilities.canEdit || this.commandPending}
            @click=${() => { this.appearanceOpen = !this.appearanceOpen }}
          >${lucideIcon(lucideIconByCanonicalName(builder.appearance.icon), { size: 17, strokeWidth: 1.8 })}</button>
          ${this.appearanceOpen ? html`
            <div class="appearance-popover" @lv-dashboard-appearance-select=${this.updateDashboardAppearance}>
              <lv-dashboard-icon-picker .icon=${builder.appearance.icon} .color=${builder.appearance.color} .label=${builder.title}></lv-dashboard-icon-picker>
            </div>
          ` : nothing}
        </div>
        <div class="title-wrap">
          <h1 class="title">${builder.title}</h1>

          <div class="meta" data-state=${builder.hasUnpublishedChanges || saveState === 'dirty' ? 'dirty' : saveState} aria-label="Dashboard draft status" aria-live="polite" title=${`${builder.origin.label} · Revision ${builder.revision.number} · ${builder.revision.id}`}>
            <span>${this.titleCase(builder.visibility)} ${this.titleCase(builder.lifecycle)} · Revision ${builder.revision.number} · ${builder.preview.loading ? 'Loading page data…' : publishing ? 'Publishing…' : this.commandPending ? 'Saving…' : this.saveLabel(builder)}</span>
          </div>
        </div>
        <div class="toolbar-actions" aria-label="Builder actions">
          ${this.embeddedInChat ? html`<button class="magic-fill" type="button" aria-label="Visual magic" aria-busy=${Boolean(this.pendingFixVisuals)} title="Complete missing chart fields on this page. Your layout stays unchanged." ?disabled=${!builder.capabilities.canEdit || this.commandPending || Boolean(this.pendingFixVisuals) || this.builderFilterController.pending || Boolean(this.builderFilterCommandInFlight) || !this.selectedPage(builder)?.visuals.length} @click=${this.arrangeVisuals}>${lucideIcon(WandSparkles, { size: 16, strokeWidth: 2 })}<span>${this.pendingFixVisuals ? 'Completing…' : 'Visual magic'}</span></button>` : nothing}
          ${this.embeddedInChat ? nothing : html`<button class="arrange-toolbar" type="button" aria-label="Arrange visuals" title="Fit visuals into a balanced grid" ?disabled=${!builder.capabilities.canEdit || this.commandPending || !this.selectedPage(builder)?.visuals.length} @click=${this.arrangeVisuals}>${lucideIcon(Grid2X2, { size: 16, strokeWidth: 2 })}<span class="arrange-label">Arrange visuals</span></button>`}
          <button type="button" class="icon-action" data-builder-action="tools" aria-label=${this.toolsHidden ? 'Show tools' : 'Hide tools'} title=${this.toolsHidden ? 'Show editing panels' : 'Hide editing panels'} aria-expanded=${!this.toolsHidden} aria-controls="builder-tools" @click=${this.toggleTools}>${lucideIcon(this.toolsHidden ? PanelRightOpen : PanelRightClose, { size: 16, strokeWidth: 2 })}<span class="sr-only">${this.toolsHidden ? 'Show tools' : 'Hide tools'}</span></button>
          <details class="dashboard-metadata">
            <summary aria-label="Dashboard settings" title="Dashboard settings">${lucideIcon(Settings2, { size: 16, strokeWidth: 2 })}<span class="sr-only">Dashboard settings</span></summary>
            <div class="dashboard-metadata-form">
              <label class="format-text-field"><span>Dashboard title</span><input type="text" maxlength="128" aria-label="Dashboard title" .value=${builder.title} ?disabled=${!builder.capabilities.canEdit || this.commandPending} @change=${this.updateDashboardTitle} /></label>
              <label class="format-text-field"><span>Dashboard description</span><textarea maxlength="512" aria-label="Dashboard description" ?disabled=${!builder.capabilities.canEdit || this.commandPending} @change=${this.updateDashboardDescription}>${builder.description ?? ''}</textarea></label>
            </div>
          </details>
          <button type="button" class="icon-action" data-builder-action="undo" aria-label="Undo" title="Undo (Ctrl or Cmd + Z)" ?disabled=${!builder.capabilities.canEdit || this.commandPending || this.undoStack.length === 0} @click=${this.undo}>${lucideIcon(Undo2, { size: 16, strokeWidth: 2 })}<span class="sr-only">Undo</span></button>
          <button type="button" class="icon-action" data-builder-action="redo" aria-label="Redo" title="Redo (Ctrl or Cmd + Shift + Z)" ?disabled=${!builder.capabilities.canEdit || this.commandPending || this.redoStack.length === 0} @click=${this.redo}>${lucideIcon(Redo2, { size: 16, strokeWidth: 2 })}<span class="sr-only">Redo</span></button>
          ${this.renderThemeToggle()}
          ${hasMoreActions ? html`
            <details class="more-actions">
              <summary aria-label="More dashboard actions">More</summary>
              <div class="more-menu" aria-label="More dashboard actions">
                ${this.embeddedInChat ? nothing : html`<button class="arrange-mobile" type="button" aria-label="Arrange visuals" ?disabled=${!builder.capabilities.canEdit || this.commandPending || !this.selectedPage(builder)?.visuals.length} @click=${this.arrangeVisuals}>Arrange visuals</button>`}
                ${this.forkHref ? html`<a class="button" href=${this.forkHref}>Make a copy</a>` : nothing}
                ${builder.capabilities.canShare ? html`<button @click=${this.toggleVisibility} aria-label="Toggle dashboard visibility">${builder.visibility === 'organization' ? 'Make private' : 'Share with organization'}</button>` : nothing}
                ${builder.capabilities.canExport
                  ? this.exportYAMLHref ? html`<a class="button" href=${this.exportYAMLHref} download>Export YAML</a>` : html`<button disabled title="YAML export is not available yet">Export YAML</button>`
                  : nothing}
                ${builder.capabilities.canArchive && !canDelete ? html`<button type="button" class="archive-action" data-builder-action="archive" @click=${this.archiveDashboard}>${lucideIcon(Archive, { size: 14, strokeWidth: 2 })}<span>Archive dashboard</span></button>` : nothing}
                ${canDelete ? html`<button type="button" class="delete-action" data-builder-action="delete" @click=${this.deleteDashboard}>${lucideIcon(Trash2, { size: 14, strokeWidth: 2 })}<span>Delete dashboard</span></button>` : nothing}
              </div>
            </details>` : nothing}
          ${builder.capabilities.canPublish ? html`<button type="button" class="primary" data-builder-action="publish" title=${publishTitle} ?disabled=${publishDisabled} @click=${this.publish}>${publishLabel}</button>` : nothing}
        </div>
      </header>
      ${this.embeddedInChat && this.fixVisualsMessage ? html`<p class="magic-fill-result" role="status">${this.fixVisualsMessage}</p>` : nothing}
    `
  }

  private renderTerminalFailure() {
    const failure = this.terminalFailure
    if (!failure) return nothing
    return html`<div class="terminal-failure" role="alert" aria-live="assertive">
      <span>${failure.message}</span>
      <button type="button" @click=${this.reloadAfterFailure}>Reload latest draft</button>
      ${failure.retryable ? html`<button type="button" @click=${this.clearTerminalFailure}>Dismiss</button>` : nothing}
    </div>`
  }

  private renderThemeToggle() {
    const targetTheme: BuilderResolvedTheme = this.resolvedTheme === 'dark' ? 'light' : 'dark'
    const label = `Switch to ${targetTheme} mode`
    return html`
      <button
        type="button"
        class="icon-action"
        data-builder-action="theme"
        data-theme-mode=${this.resolvedTheme}
        aria-label=${label}
        title=${label}
        @click=${this.toggleTheme}
      ><span data-theme-icon=${targetTheme}>${lucideIcon(targetTheme === 'dark' ? Moon : Sun, { size: 16, strokeWidth: 2 })}</span><span class="sr-only">${label}</span></button>
    `
  }

  private readonly handleThemeApplied = (event: Event): void => {
    const resolvedMode = (event as CustomEvent<{ resolvedMode?: string }>).detail?.resolvedMode
    this.resolvedTheme = resolvedMode === 'dark' ? 'dark' : resolvedMode === 'light' ? 'light' : currentResolvedTheme()
  }

  private readonly toggleTheme = (): void => {
    const mode: BuilderResolvedTheme = this.resolvedTheme === 'dark' ? 'light' : 'dark'
    this.resolvedTheme = mode
    document.dispatchEvent(new CustomEvent('leapview-theme-change', { detail: { mode } }))
  }

  private readonly handleVisualSignalPatch = (event: Event): void => {
    const patch = (event as CustomEvent<Record<string, unknown>>).detail
    if (!patch || !Object.hasOwn(patch, 'builderVisuals')) return
    // Consume each completed signal patch before Lit can coalesce responses.
    // Otherwise a late window can hide a newer sort (or another row block)
    // in the shared transport slot before the next render sees it.
    void this.builderVisuals
  }

  private readonly handleDatastarFetch = (event: Event): void => {
    if (!ownsBrowserCommandFetch(this, event)) return
    const detail = (event as CustomEvent<{ type?: string }>).detail
    const transportFailure = browserCommandFailure(event, 'Dashboard filter update')
    // Filter posts share the builder host with authoring commands, so a
    // document-global terminal error cannot identify which request failed.
    // Reset both transient filter paths conservatively; successful responses
    // are reconciled from their canonical signal patches in updated().
    const hasFilterPending = this.builderFilterController.pending
    if (transportFailure && (this.builderFilterCommandInFlight || hasFilterPending || this.filterOptionInFlight.size > 0)) {
      const action = this.builderFilterCommandInFlight || hasFilterPending ? 'Dashboard filter update' : 'Loading dashboard filter values'
      this.builderFilterCommandInFlight = null
      this.invalidateBuilderFilterOptionRequests()
      this.builderFilterController.reconcile(this.builderFilterState)
      this.builderFilterTransportError = browserCommandFailure(event, action)?.message ?? transportFailure.message
      this.requestUpdate()
    }

    if (!this.commandPending) return
    if (detail?.type === 'finished') {
      // Filter and authoring fetches share this host. A filter's completion
      // must not unlock a newer edit before its authoritative revision arrives.
      if (this.hasAttribute('data-on:lv-builder-command') && this.activeCommandAction !== 'publish' && this.activeCommandRevisionKey && this.builder && this.revisionKey(this.builder) === this.activeCommandRevisionKey) return
      this.activeCommandRevisionKey = ''
      this.resumeGridPreviewAfterSave()
      this.selectPendingAddedVisual(this.builder, true)
      this.commandPending = false
      this.activeCommandAction = ''
      this.finishFixVisuals()
      this.pendingHistorySnapshot = null
      this.setGridEditingEnabled(Boolean(this.builder?.capabilities.canEdit))
      this.requestUpdate()
      if (this.refreshQueued) { this.refreshQueued = false; this.refreshBuilderSignals() }
      return
    }
    const commandFailure = browserCommandFailure(event, 'Dashboard builder action')
    if (!commandFailure) return
    this.finishFixVisuals(commandFailure.message)
    const failedAction = this.activeCommandAction
    this.commandPending = false
    this.activeCommandAction = ''
    if (this.pendingHistorySnapshot) {
      this.undoStack = this.pendingHistorySnapshot.undo
      this.redoStack = this.pendingHistorySnapshot.redo
      this.pendingHistorySnapshot = null
    }
    this.visualTypeOverrides = {}
    this.pendingVisualTypeSwitch = null
    this.reversibleVisualTypeSwitch = null
    this.interactionEffectOverrides = {}
    this.interactionOverridesRevision = ''
    this.autoArrangePageID = ''
    this.autoArrangePreservedIDs = new Set()
    this.pendingAddVisual = null
    this.pendingAddPage = null
    this.pendingAddFilter = null
    this.pendingAddFilterComponent = null
    this.pendingAddSlicer = null
    this.addingSlicer = false
    if (this.pendingRemovePage) {
      this.localPageID = this.pendingRemovePage.pageID
      this.localVisualID = this.pendingRemovePage.visualID
      this.pendingRemovePage = null
    }
    if (failedAction === 'set_placements') {
      // A rejected placement leaves GridStack's mutable node coordinates
      // ahead of the canonical builder signal. Recreate it from the authored
      // layout so the next interaction cannot commit stale coordinates.
      this.resetGridStackToCanonicalLayout()
    } else {
      this.setGridEditingEnabled(Boolean(this.builder?.capabilities.canEdit))
    }
    this.resumeGridPreviewAfterSave()
    this.terminalFailure = commandFailure
    this.requestUpdate()
  }

  private invalidateBuilderFilterOptionRequests(): void {
    for (const [key, inFlight] of this.filterOptionInFlight) {
      const generation = this.filterOptionGenerations.get(key)
      if (generation === inFlight.generation) this.filterOptionGenerations.set(key, generation + 1)
    }
    this.filterOptionInFlight.clear()
  }

  private resetGridStackToCanonicalLayout(): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    this.destroyGridStack()
    if (!page) return
    applyCanonicalGridAttributes(this.shadowRoot, this.pagePlacedComponents(page))
  }

  private readonly reloadAfterFailure = (): void => {
    if (typeof window !== 'undefined') window.location.reload()
  }

  private readonly clearTerminalFailure = (): void => {
    this.terminalFailure = null
    this.setGridEditingEnabled(Boolean(this.builder?.capabilities.canEdit))
  }

  private renderPageBar(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined) {
    const pageIndex = page ? builder.pages.findIndex((item) => item.id === page.id) : -1
    return html`
      <footer class="page-bar">
        <span class="sr-only">Pages</span>
        <nav class="page-tabs" aria-label="Dashboard pages" role=${this.pageBaseHref ? nothing : 'tablist'}>
          ${repeat(builder.pages, (item) => item.id, (item) => this.pageBaseHref
            ? html`<a
                class="page-tab"
                aria-current=${item.id === page?.id ? 'page' : nothing}
                href=${this.pageHref(item.id)}
                title=${`${item.title}. Drag or use Alt+Arrow keys to reorder.`}
                .draggable=${builder.capabilities.canEdit}
                data-page-id=${item.id}
                data-page-dragging=${this.draggedPageID === item.id}
                data-page-drop=${this.pageDropTargetID === item.id}
                @click=${(event: MouseEvent) => { if (this.embeddedInChat) { event.preventDefault(); this.selectPage(item.id) } else if (item.id === page?.id) this.openPageSettings(item, event) }}
                @dragstart=${(event: DragEvent) => this.startPageDrag(event, item.id)}
                @dragover=${(event: DragEvent) => this.dragPageOver(event, item.id)}
                @dragleave=${() => this.leavePageDrop(item.id)}
                @drop=${(event: DragEvent) => this.dropPage(event, item.id)}
                @dragend=${this.endPageDrag}
                @keydown=${(event: KeyboardEvent) => this.handlePageTabKeydown(event, item.id)}
              >${item.title}</a>`
            : html`<button
                type="button"
                class="page-tab"
                role="tab"
                aria-selected=${item.id === page?.id}
                tabindex=${item.id === page?.id ? '0' : '-1'}
                title=${`${item.title}. Drag or use Alt+Arrow keys to reorder.`}
                .draggable=${builder.capabilities.canEdit}
                data-page-id=${item.id}
                data-page-dragging=${this.draggedPageID === item.id}
                data-page-drop=${this.pageDropTargetID === item.id}
                @click=${() => this.selectPage(item.id)}
                @dragstart=${(event: DragEvent) => this.startPageDrag(event, item.id)}
                @dragover=${(event: DragEvent) => this.dragPageOver(event, item.id)}
                @dragleave=${() => this.leavePageDrop(item.id)}
                @drop=${(event: DragEvent) => this.dropPage(event, item.id)}
                @dragend=${this.endPageDrag}
                @keydown=${(event: KeyboardEvent) => this.handlePageTabKeydown(event, item.id)}
              >${item.title}</button>`)}
        </nav>
        ${builder.capabilities.canAddPage ? html`<button type="button" class="page-add" @click=${this.addPage} aria-label="Add page" title="Add page">${lucideIcon(Plus, { size: 16, strokeWidth: 2 })}<span class="sr-only">+</span></button>` : nothing}
        <div class="page-bar-tools">
          ${page && builder.capabilities.canEdit ? html`
            <details class="page-actions">
              <summary aria-label=${`Actions for ${page.title}`} title=${`Actions for ${page.title}`}>${lucideIcon(MoreHorizontal, { size: 16, strokeWidth: 2 })}</summary>
              <div class="page-actions-menu" role="menu" aria-label=${`${page.title} page actions`}>
                <button type="button" role="menuitem" data-page-action="settings" @click=${(event: Event) => this.openPageSettings(page, event)}>${lucideIcon(Settings2, { size: 14, strokeWidth: 2 })}<span>Page settings</span></button>
                <button type="button" role="menuitem" ?disabled=${this.commandPending || pageIndex <= 0} @click=${(event: Event) => this.movePageFromMenu(event, page, pageIndex - 1)}>${lucideIcon(ChevronLeft, { size: 14, strokeWidth: 2 })}<span>Move earlier</span></button>
                <button type="button" role="menuitem" ?disabled=${this.commandPending || pageIndex < 0 || pageIndex >= builder.pages.length - 1} @click=${(event: Event) => this.movePageFromMenu(event, page, pageIndex + 1)}>${lucideIcon(ChevronRight, { size: 14, strokeWidth: 2 })}<span>Move later</span></button>
                <button type="button" role="menuitem" ?disabled=${this.commandPending} @click=${(event: Event) => this.duplicatePage(event, page)}>${lucideIcon(Copy, { size: 14, strokeWidth: 2 })}<span>Duplicate page</span></button>
                <button type="button" role="menuitem" class="page-delete" ?disabled=${this.commandPending || builder.pages.length <= 1} @click=${(event: Event) => this.removePage(event, page)}>${lucideIcon(Trash2, { size: 14, strokeWidth: 2 })}<span>Delete page</span></button>
              </div>
            </details>
          ` : nothing}
          <div class="page-zoom" role="group" aria-label="Canvas zoom">
            <button type="button" @click=${() => this.changeCanvasZoom(-0.1)} ?disabled=${this.canvasScale <= 0.25} aria-label="Zoom out" title="Zoom out">${lucideIcon(Minus, { size: 15, strokeWidth: 2 })}</button>
            <button type="button" class="page-zoom-value" @click=${this.fitCanvasToViewport} aria-label="Fit canvas to available space" title="Fit canvas to available space">${Math.round(this.canvasScale * 100)}%</button>
            <button type="button" @click=${() => this.changeCanvasZoom(0.1)} ?disabled=${this.canvasScale >= 2} aria-label="Zoom in" title="Zoom in">${lucideIcon(Plus, { size: 15, strokeWidth: 2 })}</button>
          </div>
        </div>
      </footer>
    `
  }

  private renderFieldBrowser(builder: DashboardBuilderSignal, visual: DashboardBuilderVisualSignal | undefined) {
    const datasets = builder.semanticModel.datasets ?? []
    const catalog = this.filteredCatalog(this.renderCache.semanticCatalog(datasets))
    const visibleCatalog = this.fieldFilter === 'all' ? catalog : catalog.filter((item) => item.group === this.fieldFilter)
    const supported: BuilderCatalogField[] = []
    const recordColumns: BuilderCatalogField[] = []
    const unavailable: BuilderCatalogField[] = []
    for (const item of visibleCatalog) {
      const compatible = this.addingSlicer
        ? this.fieldSupportsFilter(item.field)
        : visual
          ? Boolean(this.fieldUsedIn(item.field, visual) || this.fieldCompatibleWithVisual(item.field, visual))
          : this.fieldDataTypeSupported(item.field)
      if (compatible) supported.push(item)
      else if (item.field.roles?.includes('detail')) recordColumns.push(item)
      else unavailable.push(item)
    }
    const groups: Array<Exclude<BuilderFieldFilter, 'all'>> = ['metric', 'dimension', 'time']
    const collapsed = this.collapsedPanes.data
    return html`
      <div class="field-browser" ?hidden=${this.savedVisualsOpen || collapsed}>
        <div class="field-search-header">
          <div class="pane-header-details" ?hidden=${collapsed}>
            <label>
              <span class="sr-only">Search fields</span>
              <input class="search" type="search" aria-label="Search fields" placeholder="Search fields" title="Search measures and dimensions" .value=${this.fieldQuery} @input=${this.onFieldQuery} />
            </label>
            <div class="field-filter" role="group" aria-label="Filter fields by role">
              ${(['all', ...groups] as BuilderFieldFilter[]).map((filter) => html`
                <button type="button" data-field-filter=${filter} aria-pressed=${this.fieldFilter === filter} @click=${() => { this.fieldFilter = filter }}>
                  ${this.fieldFilterLabel(filter)}
                </button>
              `)}
            </div>
          </div>
        </div>
        <div id="builder-data-content" class="pane-content" ?hidden=${collapsed}>
          <p class="sr-only" role="status" aria-live="polite">${visibleCatalog.length} ${visibleCatalog.length === 1 ? 'field' : 'fields'} shown.</p>
          <div class="field-results">
            ${visibleCatalog.length === 0
              ? html`<div class="empty-fields"><p class="pane-hint">No fields match this search.</p>${this.fieldQuery ? html`<button type="button" @click=${this.clearFieldQuery}>Clear search</button>` : nothing}</div>`
              : html`
                ${this.renderCatalogEntities(supported, datasets, visual)}
                ${this.renderCatalogDisclosure('record-fields', 'Record columns', recordColumns, datasets, visual, false)}
                ${this.renderCatalogDisclosure('unsupported-fields', this.addingSlicer ? 'Unavailable for slicers' : 'Unavailable for this visual', unavailable, datasets, visual, true)}
              `}
          </div>
        </div>
      </div>
    `
  }

  private get savedVisualFrame(): HTMLIFrameElement | null {
    return this.shadowRoot?.querySelector<HTMLIFrameElement>('.saved-visuals-frame') ?? null
  }

  private get visualImportFrame(): HTMLIFrameElement | null {
    return this.shadowRoot?.querySelector<HTMLIFrameElement>('.visual-import-frame') ?? null
  }

  private finishVisualImport(message = ''): void {
    const imported = this.importingSavedVisual
    this.pendingVisualSource = null
    window.clearTimeout(this.importTimer)
    this.importingSavedVisual = false
    this.refreshingBuilder = false
    this.commandPending = false
    this.activeCommandAction = ''
    this.setGridEditingEnabled(Boolean(this.builder?.capabilities.canEdit))
    if (message || imported) this.visualActionMessage = message || 'Visual added to dashboard.'
    if (message) {
      this.terminalFailure = { kind: 'unknown', status: null, message, retryable: true }
      this.pendingAddVisual = null
      if (this.embeddedInChat) window.parent.postMessage({ type: 'lv-builder-operation-error', message } satisfies ChatDashboardMessage, window.location.origin)
    }
    this.importRevision = null
    this.requestUpdate()
    if (this.refreshQueued) {
      this.refreshQueued = false
      this.refreshBuilderSignals()
    }
  }

  private handleVisualImportLoad = (): void => {
    const frame = this.visualImportFrame
    if ((!this.importingSavedVisual && !this.refreshingBuilder) || !frame?.contentDocument || frame.contentWindow?.location.href === 'about:blank') return
    if (!frame.contentDocument.getElementById('chat-dashboard-receipt')) {
      this.finishVisualImport(frame.contentDocument.body?.innerText.trim().slice(0, 500) || 'Could not add this visual. Please try again.')
    }
  }

  private handleVisualImportMessage = async (event: MessageEvent<SavedVisualImportMessage>): Promise<void> => {
    if ((!this.importingSavedVisual && !this.refreshingBuilder) || event.origin !== window.location.origin || event.source !== this.visualImportFrame?.contentWindow || event.data?.type !== 'lv-builder-imported') return
    const envelope = event.data.envelope
    if (envelope.builder?.dashboardId !== this.builder?.dashboardId || envelope.builder.draftId !== this.builder?.draftId) return
    const source = this.pendingVisualSource
    if (source && envelope.builder.pages.some(page => page.visuals.some(visual => visual.id === source.componentID))) {
      this.importedVisualSources.set(source.componentID, source.savedID)
    }
    const runtime = await loadDatastarRuntime()
    // Replace discriminated chart envelopes instead of merging stale specs.
    runtime.mergePatch({ builderVisuals: null })
    runtime.mergePatch({
      builder: envelope.builder, builderVisuals: envelope.builderVisuals,
      agentContext: event.data.agentContext,
      runtime: { servingStateId: envelope.runtime.servingStateId },
      builderFilterContract: envelope.builderFilterContract, builderFilterState: envelope.builderFilterState,
      builderFilterOptionPages: envelope.builderFilterOptionPages, builderFilterValidation: envelope.builderFilterValidation,
      status: envelope.status,
    })
    if (this.importRevision) {
      this.undoStack = [...this.undoStack.slice(-99), this.importRevision]
      this.redoStack = []
    }
    this.finishVisualImport()
  }

  private refreshSavedVisualLibrary = (): void => {
    const frame = this.savedVisualFrame
    if (frame) frame.src = frame.src
  }

  private refreshBuilderSignals(): void {
    if (this.commandPending || this.importingSavedVisual || this.refreshingBuilder) {
      this.refreshQueued = true
      return
    }
    const page = this.builder ? this.selectedPage(this.builder) : undefined
    const frame = this.visualImportFrame
    if (!page || !frame) return
    this.refreshingBuilder = true
    this.commandPending = true
    this.activeCommandAction = 'refresh_builder'
    const href = new URL(window.location.href)
    href.searchParams.set('builderReceipt', '1')
    href.searchParams.set('page', page.id)
    frame.src = href.pathname + href.search
    this.importTimer = window.setTimeout(() => this.finishVisualImport('Refreshing the dashboard took too long. Please try again.'), 45000)
  }

  private handleSavedVisualMessage = (event: MessageEvent<SavedVisualLibraryMessage>): void => {
    if (event.origin !== window.location.origin) return
    const fromParent = this.embeddedInChat && event.source === window.parent
    if (!fromParent && event.source !== this.savedVisualFrame?.contentWindow) return
    if (fromParent && (event.data as { type: string }).type === 'lv-arrange-dashboard-visuals') this.arrangeVisuals()
    if (fromParent && (event.data as {type: string}).type === 'lv-select-dashboard-page') {
      const pageId = (event.data as unknown as {pageId: string}).pageId
      if (this.builder?.pages.some(page => page.id === pageId) && this.selectedPage(this.builder)?.id !== pageId) this.selectPage(pageId)
    }
    if (fromParent && event.data?.type === 'lv-add-saved-visual' && event.data.pageId && event.data.pageId !== (this.builder ? this.selectedPage(this.builder)?.id : undefined)) {
      window.parent.postMessage({type: 'lv-builder-operation-error', message: 'The selected page changed. Select your destination and try again.'} satisfies ChatDashboardMessage, window.location.origin)
      return
    }
    if (fromParent && (event.data as { type: string }).type === 'lv-refresh-builder') this.refreshBuilderSignals()
    if (fromParent && ['lv-add-saved-visual', 'lv-remove-dashboard-visual'].includes(event.data?.type) && (this.commandPending || this.importingSavedVisual)) {
      window.parent.postMessage({ type: 'lv-builder-operation-error', message: 'The dashboard is finishing another update. Please try again.' } satisfies ChatDashboardMessage, window.location.origin)
      return
    }
    if (fromParent && event.data?.type === 'lv-remove-dashboard-visual') {
      this.emitCommand('remove_visual', { pageId: event.data.pageId, visualId: event.data.componentId })
    }
    if (event.data?.type === 'lv-refresh-saved-visuals') this.refreshSavedVisualLibrary()
    if (event.data?.type === 'lv-add-saved-visual') this.addSavedVisual(event.data.id, undefined, event.data.requestId)
  }

  private handleAgentVisualAdd = (event: CustomEvent<{savedId: string}>): void => {
    event.preventDefault()
    event.stopPropagation()
    if ((this.builder ? this.selectedPage(this.builder)?.visuals : [])?.some(visual => (this.importedVisualSources.get(visual.id) ?? savedVisualSourceId(visual.id)) === event.detail.savedId)) return
    this.addSavedVisual(event.detail.savedId)
  }

  private allowSavedVisualDrop = (event: DragEvent): void => {
    if (!event.dataTransfer?.types.includes(savedVisualDragType)) return
    if (!event.composedPath().some(node => node instanceof HTMLElement && node.classList.contains('canvas-pane'))) return
    event.preventDefault()
    event.stopPropagation()
    event.dataTransfer.dropEffect = 'copy'
  }

  private dropSavedVisual = (event: DragEvent): void => {
    if (!event.dataTransfer?.types.includes(savedVisualDragType)) return
    if (!event.composedPath().some(node => node instanceof HTMLElement && node.classList.contains('canvas-pane'))) return
    event.preventDefault()
    event.stopPropagation()
    const page = this.builder ? this.selectedPage(this.builder) : undefined
    const canvas = this.shadowRoot?.querySelector('.canvas')
    if (!page || !canvas) return
    const y = (event.clientY - canvas.getBoundingClientRect().top) / this.canvasScale
    const row = Math.max(1, Math.floor(y / ((page.grid.rowHeight || 48) + (page.grid.gap || 0))) + 1)
    this.addSavedVisual(event.dataTransfer.getData(savedVisualDragType), row)
  }

  private addSavedVisual(id: string, row?: number, requestId?: string): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || this.commandPending || this.importingSavedVisual || !id) return
    const importID = requestId || uuidv7()
    this.pendingVisualSource = { componentID: savedVisualComponentId(id, importID), savedID: id }
    this.importingSavedVisual = true
    this.importRevision = this.currentRevisionReference()
    this.pendingAddVisual = { revision: this.revisionKey(builder), visualIDs: new Set(page.visuals.map(visual => visual.id)), pageID: page.id, autoArrange: row === undefined }
    this.commandPending = true
    this.activeCommandAction = 'import_saved_visual'
    this.setGridEditingEnabled(false)
    this.terminalFailure = null
    this.importTimer = window.setTimeout(() => this.finishVisualImport('Adding this visual took too long. Please try again.'), 45000)
    this.requestUpdate()
    submitVisualForm(`/dashboards/${encodeURIComponent(builder.dashboardId)}/draft/saved-visual`, this.visualImportFrame, {
      idempotencyKey: importID, builderReceipt: '1',
      savedVisualId: id, pageId: page.id, revisionId: builder.revision.id,
      row: String(row ?? 1), embed: this.embeddedInChat ? 'chat' : '',
    })
  }

  private renderDataPane(builder: DashboardBuilderSignal, visual: DashboardBuilderVisualSignal | undefined) {
    const collapsed = this.collapsedPanes.data
    return html`
      <aside class="pane data-pane" data-collapsed=${collapsed} aria-labelledby="builder-data-heading">
        <div class="pane-header field-browser-header">
          <div class="pane-heading-row">
            <div class="pane-title-group">
              <span class="pane-title-icon">${lucideIcon(Database, { size: 16, strokeWidth: 2 })}</span>
              <h2 id="builder-data-heading" class="pane-title">Data</h2>
            </div>
            ${this.renderPaneToggle('data', 'Data pane', 'builder-data-tabs')}
          </div>
        </div>
        <div id="builder-data-tabs" class="saved-library-tabs" ?hidden=${collapsed}>
          <button type="button" aria-pressed=${!this.savedVisualsOpen} @click=${() => { this.savedVisualsOpen = false }}>Fields</button>
          <button type="button" aria-pressed=${this.savedVisualsOpen} @click=${() => { this.savedVisualLibraryLoaded = true; this.savedVisualsOpen = true }}>Saved visuals</button>
        </div>
        ${this.renderFieldBrowser(builder, visual)}
        ${this.savedVisualLibraryLoaded ? html`
          <iframe id="builder-saved-visuals" class="saved-visuals-frame" title="Saved visuals" ?hidden=${collapsed || !this.savedVisualsOpen} src=${`/visuals/saved?model=${encodeURIComponent(builder.semanticModel.id)}`}></iframe>
        ` : nothing}
      </aside>
    `
  }

  private renderAgentPane() {
    if (this.embeddedInChat) return nothing
    const collapsed = this.collapsedPanes.agent
    return html`
      <aside class="pane agent-pane" data-collapsed=${collapsed} aria-labelledby="builder-agent-heading">
        <div class="pane-header">
          <div class="pane-heading-row">
            <div class="pane-title-group">
              <span class="pane-title-icon">${agentIcon()}</span>
              <h2 id="builder-agent-heading" class="pane-title">Agent</h2>
            </div>
            ${this.renderPaneToggle('agent', 'Agent pane', 'builder-agent-content')}
          </div>
        </div>
        <div id="builder-agent-content" class="pane-content agent-pane-content" ?hidden=${collapsed}>
          <lv-chat-drawer .dashboardSavedVisualIds=${(this.builder ? this.selectedPage(this.builder)?.visuals : [])?.map(visual => this.importedVisualSources.get(visual.id) ?? savedVisualSourceId(visual.id)).filter((id): id is string => Boolean(id)) ?? []} .open=${!collapsed} embedded @lv-chat-drawer-close=${this.closeAgentPane}></lv-chat-drawer>
        </div>
      </aside>
    `
  }

  private renderFiltersPane(builder: DashboardBuilderSignal) {
    const filter = this.selectedBuilderFilter(builder)
    const filters = builder.filters ?? []
    const page = this.selectedPage(builder)
    const visual = page ? this.selectedVisual(page, builder) : undefined
    const grouped = this.groupFiltersByScope(filters, page, visual)
    const dimensions = this.renderCache.semanticCatalog(builder.semanticModel.datasets ?? []).filter((item) => this.fieldSupportsFilter(item.field) && (!this.addFilterQuery.trim() || item.field.label.toLocaleLowerCase().includes(this.addFilterQuery.trim().toLocaleLowerCase())))
    const filterError = this.builderFilterErrorMessage()
    const collapsed = this.collapsedPanes.filters
    return html`
      <aside class="pane filters-pane" data-collapsed=${collapsed} aria-labelledby="builder-filters-heading">
        <div class="pane-header">
          <div class="pane-heading-row">
            <div class="pane-title-group">
              <span class="pane-title-icon">${lucideIcon(ListFilter, { size: 16, strokeWidth: 2 })}</span>
              <h2 id="builder-filters-heading" class="pane-title">Filters</h2>
              <span class="filter-count">${filters.length}</span>
            </div>
            ${this.renderPaneToggle('filters', 'Filters pane', 'builder-filters-content')}
          </div>
          <div class="pane-header-details" ?hidden=${collapsed}>
            ${filterError ? html`<p class="filter-validation" role="alert" aria-live="assertive">${filterError}</p>` : nothing}
          </div>
        </div>
        <div id="builder-filters-content" class="filter-pane-body pane-content" ?hidden=${collapsed}>
          ${this.draggedFieldID ? html`<div class="filter-drop-zone" data-field-dragging="true" @dragover=${this.allowFieldDrop} @drop=${this.dropFieldOnFilters}>Drop to add filter</div>` : nothing}
          <button class="filter-add-trigger" type="button" aria-label="Add filter" aria-haspopup="menu" aria-controls="builder-filter-field-options" aria-expanded=${this.addFilterMenuOpen} ?disabled=${!builder.capabilities.canEdit || this.commandPending} @click=${this.toggleAddFilterMenu}>+ Add filter</button>
          <div class="filter-add-menu" ?hidden=${!this.addFilterMenuOpen} @keydown=${this.handleAddFilterMenuKey}>
            <input class="filter-add-search" type="search" aria-label="Search filter fields" placeholder="Search fields" .value=${this.addFilterQuery} @input=${(event: Event) => { this.addFilterQuery = (event.target as HTMLInputElement).value }} />
            <div id="builder-filter-field-options" class="filter-add-options" role="menu" aria-label="Choose filter field">
            ${dimensions.map((item) => html`<button type="button" role="menuitem" class="filter-add-option" data-field-id=${item.field.id}
              ?disabled=${filters.some((candidate) => candidate.dimension === item.field.id) || !this.filterHasCompatibleVisual(item.field)}
              title=${filters.some(candidate => candidate.dimension === item.field.id) ? 'Already added' : this.filterHasCompatibleVisual(item.field) ? item.field.label : 'This field does not apply to any dashboard visual'}
              @click=${() => this.chooseFilterField(item.field)}>${item.field.label}</button>`)}
            </div>
            ${dimensions.length === 0 ? html`<p class="filter-add-empty" role="status">No matching fields</p>` : nothing}
          </div>
          ${this.renderBuilderFilterResetControls(page)}
          ${this.renderBuilderFilterApplicationActions()}
          ${filters.length === 0 ? html`<p class="filter-pane-empty">No filters yet</p>` : nothing}
          ${grouped.visual.length > 0 ? this.renderFilterScopeGroup('This visual', grouped.visual, filter) : nothing}
          ${grouped.page.length > 0 ? this.renderFilterScopeGroup('This page', grouped.page, filter) : nothing}
          ${grouped.report.length > 0 ? this.renderFilterScopeGroup('All pages', grouped.report, filter) : nothing}
          ${grouped.custom.length > 0 ? this.renderFilterScopeGroup('Custom', grouped.custom, filter) : nothing}
        </div>
      </aside>
    `
  }

  private renderBuilderFilterResetControls(page: DashboardBuilderPageSignal | undefined) {
    const pageBindingKeys = this.builderFilterResetBindingKeys('page', page?.id)
    const dashboardBindingKeys = this.builderFilterResetBindingKeys('dashboard')
    if (pageBindingKeys.length === 0 && dashboardBindingKeys.length === 0) return nothing
    const pending = this.builderFilterController.pending || this.status.loading
    return html`
      <div class="filter-reset-actions" role="group" aria-label="Reset dashboard filters">
        ${pageBindingKeys.length > 0 ? html`<button
          type="button"
          class="filter-reset-button"
          data-reset-scope="page"
          title="Reset filters on this page"
          ?disabled=${pageBindingKeys.length === 0 || pending}
          @click=${() => this.resetBuilderFilters('page', pageBindingKeys)}
        >Reset page</button>` : nothing}
        <button
          type="button"
          class="filter-reset-button"
          data-reset-scope="dashboard"
          title="Reset filters on all pages"
          ?disabled=${dashboardBindingKeys.length === 0 || pending}
          @click=${() => this.resetBuilderFilters('dashboard', dashboardBindingKeys)}
        >Reset all</button>
      </div>
    `
  }

  private renderBuilderFilterApplicationActions() {
    if (this.builderFilterContract.applicationMode !== 'deferred') return nothing
    const projected = this.builderFilterController.projected.revision > 0
      ? this.builderFilterController.projected
      : this.builderFilterState
    const dirtyCount = projected.dirtyBindings.length
    if (dirtyCount === 0 && !this.builderFilterController.pending) return nothing
    const pending = this.builderFilterController.pending || this.status.loading
    return html`
      <div class="filter-reset-actions filter-application-actions" role="group" aria-label="Apply dashboard filters">
        <button
          type="button"
          class="filter-reset-button"
          data-filter-cancel
          ?disabled=${dirtyCount === 0 || pending}
          @click=${this.handleBuilderFilterCancel}
        >Cancel</button>
        <button
          type="button"
          class="filter-reset-button filter-apply-button"
          data-filter-apply
          ?disabled=${dirtyCount === 0 || pending}
          @click=${this.handleBuilderFilterApply}
        >Apply${dirtyCount > 0 ? ` (${dirtyCount})` : nothing}</button>
      </div>
    `
  }

  private renderFilterEditor(builder: DashboardBuilderSignal, filter: DashboardBuilderFilterSignal) {
    const editable = builder.capabilities.canEdit && !this.commandPending
    const canRequire = canRequireFilter(filter, this.builderFilterContract)
    const page = this.selectedPage(builder)
    const placedComponent = page?.filterComponents?.find((component) => component.filterId === filter.id)
    const visual = page ? this.selectedVisual(page, builder) : undefined
    const scope = this.filterScope(filter, page, visual)
    return html`
      <section class="filter-editor" aria-label=${`Configure ${filter.label} filter`}>
        <details class="filter-settings">
          <summary>${filter.label} settings</summary>
          <div class="filter-settings-body">
        <div class="filter-scope-options" role="radiogroup" aria-label="Filter scope">
          <label class="filter-scope-option" title="Apply on all pages"><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'report'} ?disabled=${!editable} @change=${() => this.setFilterScope(filter, 'report')} /><span>All pages</span></label>
          <label class="filter-scope-option" title=${page ? `Apply on ${page.title}` : 'Select a page first'}><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'page'} ?disabled=${!editable || !page} @change=${() => page && this.setFilterScope(filter, 'page', page)} /><span>This page</span></label>
          <label class="filter-scope-option" title=${visual ? `Apply only to ${visual.title}` : 'Select a visual first'}><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'visual'} ?disabled=${!editable || !visual || !page} @change=${() => page && visual && this.setFilterScope(filter, 'page', page, [visual.id])} /><span>Visual</span></label>
        </div>
        ${scope === 'custom' ? html`<p class="filter-scope-empty">Uses a custom set of pages or visuals.</p>` : nothing}

            <label>Label
              <input type="text" maxlength="128" .value=${filter.label} ?disabled=${!editable} @change=${(event: Event) => {
                const input = event.currentTarget as HTMLInputElement
                input.value = input.value.trim() || filter.label
                this.updateFilter(filter, { label: input.value })
              }} />
            </label>
            <label>Control
              <select .value=${filter.controlType} ?disabled=${!editable} @change=${(event: Event) => this.updateFilter(filter, { controlType: (event.currentTarget as HTMLSelectElement).value as BuilderFilterControl })}>
                ${this.filterControlChoices(filter).map(([value, label]) => html`<option value=${value}>${label}</option>`)}
              </select>
            </label>
            <label>URL parameter
              <input type="text" maxlength="64" placeholder="Optional" .value=${filter.urlParameter ?? ''} ?disabled=${!editable} @change=${(event: Event) => {
                const input = event.currentTarget as HTMLInputElement
                input.value = input.value.trim()
                this.updateFilter(filter, { urlParameter: input.value })
              }} />
            </label>
            <label class="filter-toggle"><span>Readers can edit</span><input type="checkbox" .checked=${filter.readerEditable} ?disabled=${!editable} @change=${(event: Event) => this.updateFilter(filter, { readerEditable: (event.currentTarget as HTMLInputElement).checked })} /></label>
            <label class="filter-toggle" title=${canRequire ? '' : 'Required is available for filters with a default value.'}><span>Required</span><input type="checkbox" .checked=${filter.required} ?disabled=${!editable || !canRequire} @change=${(event: Event) => this.updateFilter(filter, { required: (event.currentTarget as HTMLInputElement).checked })} /></label>
        <div class="filter-editor-actions">
          ${page ? html`
            <button type="button" class="filter-placement-action" ?disabled=${!editable} @click=${() => placedComponent ? this.removeFilterComponent(page, placedComponent) : this.addFilterComponent(page, filter)}>
              ${placedComponent ? 'Remove from canvas' : 'Add to canvas'}
            </button>
          ` : html`<span></span>`}
          <button type="button" class="filter-remove" ?disabled=${!editable} @click=${() => this.removeFilter(filter)}>Delete</button>
        </div>
          </div>
        </details>
      </section>
    `
  }

  private renderFilterScopeGroup(title: string, filters: DashboardBuilderFilterSignal[], selected: DashboardBuilderFilterSignal | undefined) {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const visual = page && builder ? this.selectedVisual(page, builder) : undefined
    return html`
      <section class="filter-scope-group" aria-label=${title}>
        <div class="filter-scope-heading"><span>${title}</span><span>${filters.length}</span></div>
        <div class="filter-list">
          ${repeat(filters, (item) => item.id, (item) => html`<div class="filter-item">${this.renderFilterPanePreview(item, selected?.id === item.id, page, visual)}${builder && selected?.id === item.id ? this.renderFilterEditor(builder, item) : nothing}</div>`)}
        </div>
      </section>
    `
  }

  private renderFilterPanePreview(filter: DashboardBuilderFilterSignal, selected: boolean, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined) {
    const binding = this.compiledBindingForFilter(filter, page, visual)
    const definition = binding ? this.builderFilterContract.definitions[binding.filter] : undefined
    if (!binding || !definition) {
      return html`
        <button type="button" class="filter-card" aria-pressed=${selected} @click=${() => this.selectFilterDefinition(filter.id)}>
          <span class="filter-card-title">${filter.label}</span>
          <span class="filter-card-meta">${this.filterControlLabel(filter.controlType)}</span>
        </button>
      `
    }
    const projectedState = this.builderFilterController.projected.revision > 0 ? this.builderFilterController.projected : this.builderFilterState
    const expression = projectedState.draftControls[binding.key] ?? projectedState.appliedControls[binding.key]?.expression ?? binding.default
    return html`
      <div
        class="filter-card-preview"
        data-selected=${selected}
        role="group"
        aria-label=${`${filter.label} filter preview`}
        @click=${() => this.selectFilterDefinition(filter.id)}
      >
        <lv-filter-pane-card
          .presentation=${definition.predicates.some(predicate => predicate.kind === 'set') ? { style: 'dropdown', search: true, selectAll: false, showCounts: false, showSummary: false, compact: true } : undefined}
          .definition=${definition}
          .binding=${binding}
          .expression=${expression}
          .options=${this.builderFilterOptionPages[binding.key]}
          .optionContext=${this.builderFilterOptionContext(binding, page?.id ?? '')}
          .optionRequestReady=${this.builderFilterOptionsReady}
          .pending=${this.builderFilterController.pendingFor(binding.key)}
          .stale=${false}
          .active=${expression.kind !== 'unfiltered'}
          .dirty=${projectedState.dirtyBindings.includes(binding.key)}
          @lv-filter-clear=${this.handleBuilderFilterClear}
          @lv-filter-reset-binding=${this.handleBuilderFilterResetBinding}
        ></lv-filter-pane-card>
      </div>
    `
  }

  private renderCatalogEntities(fields: BuilderCatalogField[], datasets: DashboardBuilderDatasetSignal[], visual: DashboardBuilderVisualSignal | undefined) {
    return builderCatalogEntities(fields, datasets, this.builder?.semanticModel.title).map((entity) => html`
      <details class="field-entity" data-dataset-id=${entity.id} open>
        <summary>
          <span class="field-entity-title">${entity.title}</span>
          <span class="field-entity-count">${entity.fields.length}</span>
        </summary>
        ${this.renderCatalogRoleLists(entity.fields, visual, true)}
      </details>
    `)
  }

  private renderCatalogDisclosure(className: string, title: string, fields: BuilderCatalogField[], datasets: DashboardBuilderDatasetSignal[], visual: DashboardBuilderVisualSignal | undefined, showCompatibilityContext: boolean) {
    if (fields.length === 0) return nothing
    return html`
      <details class="catalog-disclosure ${className}" ?open=${Boolean(this.fieldQuery)}>
        <summary><span class="catalog-disclosure-title">${title}</span><span class="catalog-disclosure-count">${fields.length}</span></summary>
        <div class="catalog-disclosure-body">
          ${builderCatalogEntities(fields, datasets, this.builder?.semanticModel.title).map((entity) => html`
            <section class="catalog-entity" data-dataset-id=${entity.id} aria-label=${entity.title}>
              <h3 class="catalog-entity-title">${entity.title}</h3>
              ${this.renderCatalogRoleLists(entity.fields, visual, false, showCompatibilityContext)}
            </section>
          `)}
        </div>
      </details>
    `
  }

  private renderCatalogRoleLists(fields: BuilderCatalogField[], visual: DashboardBuilderVisualSignal | undefined, compatible: boolean, showCompatibilityContext = true) {
    const groups: Array<Exclude<BuilderFieldFilter, 'all'>> = ['metric', 'dimension', 'time']
    return groups.map((group) => {
      const groupedFields = fields.filter((item) => item.group === group)
      if (groupedFields.length === 0) return nothing
      return html`
        <div class="field-list" data-field-group=${group} aria-label=${this.fieldGroupLabel(group)}>
          ${repeat(groupedFields, (item) => this.catalogFieldKey(item), (item) => this.renderCatalogField(item, visual, compatible, false, showCompatibilityContext))}
        </div>
      `
    })
  }

  private renderCatalogField(item: BuilderCatalogField, visual: DashboardBuilderVisualSignal | undefined, compatible: boolean, showDatasetContext = true, showCompatibilityContext = true) {
    const field = item.field
    const visualType = visual ? this.visualTypeForRender(visual) : ''
    const datasetContext = item.datasets[0]?.title ?? ''
    const usedIn = visual ? this.fieldUsedIn(field, visual) : ''
    const editable = Boolean(this.builder?.capabilities.canEdit && compatible && (this.addingSlicer || visual || this.builder?.capabilities.canAddVisual))
    const roleLabel = this.fieldGroupLabel(item.group, true)
    const dataType = field.dataType.toLowerCase() === 'unknown' ? '' : field.dataType
    const targetRole = visual ? this.roleForField(field, visual) : undefined
    const roleFull = Boolean(visual && targetRole && this.fieldDataTypeSupported(field) && this.fieldSupportsRole(field, targetRole) && !this.roleHasCapacity(visual, targetRole))
    const action = !visual
      ? this.addingSlicer
        ? `Click or drag to use ${field.label} in the slicer.`
        : `Click or drag to create a ${this.visualLabel(this.recommendedVisualForField(field))} visual.`
      : !compatible
        ? roleFull
          ? `${this.fieldWellLabel(visual!, targetRole!)} is full. Remove its current field before adding another.`
          : `Not compatible with the selected ${visualType} visual.`
        : usedIn
          ? `Used in ${usedIn}. Drag to a compatible field well.`
          : `Click to add to ${this.fieldWellLabel(visual, this.roleForField(field, visual))}, or drag to a field well.`
    const accessibleName = [field.label, roleLabel, dataType, datasetContext, action].filter(Boolean).join('. ')
    const compatibilityContext = roleFull ? `${this.fieldWellLabel(visual!, targetRole!)} is full` : `Not compatible with ${this.addingSlicer ? 'slicers' : visualType || 'this visual'}`
    const context = compatible
      ? (showDatasetContext ? datasetContext : '')
      : [showDatasetContext ? datasetContext : '', showCompatibilityContext ? compatibilityContext : ''].filter(Boolean).join(' · ')

    if (!compatible) {
      return html`
        <div class="field field-unsupported" role="note" aria-label=${accessibleName}>
          <span class="field-role-icon" aria-hidden="true">${this.renderFieldRoleIcon(item.group)}</span>
          <span class="field-copy"><span class="field-label">${field.label}</span>${context ? html`<span class="field-context">${context}</span>` : nothing}</span>
          ${showCompatibilityContext ? html`<span class="field-used">Unsupported</span>` : nothing}
        </div>
      `
    }

    return html`
      <button class="field" type="button" data-used=${usedIn ? 'true' : 'false'} data-dragging=${this.draggedFieldID === field.id ? 'true' : 'false'} draggable=${editable ? 'true' : 'false'} ?disabled=${!editable} title=${field.description || action} aria-label=${accessibleName} @click=${() => this.addField(field)} @dragstart=${(event: DragEvent) => this.dragField(event, field)} @dragend=${this.clearDraggedField}>
        <span class="field-role-icon" aria-hidden="true">${this.renderFieldRoleIcon(item.group)}</span>
        <span class="field-copy"><span class="field-label">${field.label}</span>${context ? html`<span class="field-context">${context}</span>` : nothing}</span>
        ${usedIn ? html`<span class="field-used">${usedIn}</span>` : nothing}
      </button>
    `
  }

  private renderCanvas(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined) {
    if (!page) {
      return html`<section class="canvas-pane" aria-label="Dashboard canvas"><div class="state"><div><strong>No pages yet</strong><span>Create a page to start designing this dashboard.</span>${builder.capabilities.canAddPage ? html`<div><button @click=${this.addPage} aria-label="Add page">Add page</button></div>` : nothing}</div></div></section>`
    }
    const width = Math.max(12, page.grid.columns || 12)
    const pageFormatting = this.editingPage
      && !this.effectiveVisualID(builder, page)
      && !this.selectedFilterComponentID
    const previews = this.builderVisuals
    // GridStack reorders tiles outside Lit's repeat markers. Replace their
    // parent on membership changes so Lit never reconciles displaced ranges.
    const canvasKey = JSON.stringify([page.id, this.pagePlacedComponents(page).map(component => component.id)])
    return html`
      <section class="canvas-pane" aria-label="Dashboard canvas">
        <div class="canvas-scroll">
          <p id="dashboard-builder-grid-help" class="sr-only">Select a canvas component and drag any edge or corner handle to resize it. Use Alt plus an arrow key to move it one grid cell. Use Alt plus Shift plus an arrow key to resize it.</p>
          <div class="canvas-fit">
            ${keyed(canvasKey, html`<div class="canvas grid-stack" data-field-dragging=${this.draggedFieldID ? 'true' : 'false'} data-grid-guides=${this.draggedFieldID || pageFormatting ? 'true' : 'false'} aria-describedby="dashboard-builder-grid-help" style=${`grid-template-columns: repeat(${width}, 1fr);`} @click=${this.deselectVisualFromCanvas} @dragover=${this.allowFieldDrop} @drop=${this.dropField}>
              ${this.draggedFieldID ? html`<div class="canvas-field-drop-hint" role="status">Drop on the canvas to create a ${this.visualLabel(this.recommendedVisualForDraggedField(builder), builder)} visual</div>` : nothing}
              ${page.visuals.length === 0 && (page.filterComponents?.length ?? 0) === 0 && (page.headers?.length ?? 0) === 0 && (page.placeholders?.length ?? 0) === 0
                ? html`<div class="visual-empty"><div><strong>This page is empty</strong><span>Choose a visual or place a report-filter slicer to begin.</span></div></div>`
                : html`${repeat(page.visuals, (visual) => visual.id, (visual) => this.renderVisual(visual, page, previews))}${repeat(page.filterComponents ?? [], (component) => component.id, (component) => this.renderFilterComponent(component, page))}${repeat(page.headers ?? [], (header) => header.id, (header) => this.renderHeader(header))}${repeat(page.placeholders ?? [], (placeholder) => placeholder.id, (placeholder) => this.renderPlaceholder(placeholder))}`}
            </div>`)}
          </div>
          <div class="sr-only" aria-live="polite">${this.gridInteractionMessage}</div>
        </div>
      </section>
    `
  }

  private renderVisual(visual: DashboardBuilderVisualSignal, page: DashboardBuilderPageSignal, previews: Record<string, VisualizationEnvelope>) {
    const selected = visual.id === this.effectiveVisualID(this.builder, page)
    const visualType = this.visualTypeForRender(visual)
    const previewCandidate = previews[this.visualSignalID(visual)]
    const visualTypeSwitchPending = isBuilderVisualTypeSwitchPending(this.commandPending, this.activeCommandAction, this.pendingVisualTypeSwitch, page.id, visual, visualType)
    const mobileOrder = this.renderCache.mobileComponentOrder(visual.id, page)
    const draggedField = this.draggedFieldFromBuilder(this.builder)
    const fieldDrop = draggedField ? (this.fieldCompatibleWithVisual(draggedField, visual) ? 'compatible' : 'incompatible') : ''
    const requirementMessages = visualTypeSwitchPending ? [] : this.visualRequirementMessages(visual)
    const suggestedMeasure = visualType === 'gauge' && !visual.slots.some((slot) => this.slotRole(slot) === 'metric')
      && this.builder?.capabilities.canEdit && !this.commandPending
      ? this.defaultGaugeMeasure(this.builder)
      : undefined
    const previewIssue = visualTypeSwitchPending ? '' : this.visualPreviewErrorMessage(visual)
    const previewUnavailable = visualTypeSwitchPending || requirementMessages.length > 0 || Boolean(previewIssue) || Boolean(this.builder?.preview.error && !this.builder.preview.active)
    const preview = previewUnavailable ? undefined : previewCandidate
    const previewHasHeader = preview ? this.visualPreviewHasHeader(preview) : false
    const previewLoading = visualTypeSwitchPending || Boolean(this.builder?.preview.loading)
    const fallbackMessage = this.builder?.preview.error ? 'Preview unavailable. Try again after the draft is valid.' : 'Add fields to preview.'
    return html`
      <div class="visual grid-stack-item ${preview ? 'has-preview' : ''}" data-visual-type=${visualType} data-selected=${selected} data-field-drop=${fieldDrop || nothing} gs-id=${visual.id} gs-x=${Math.max(0, visual.placement.col - 1)} gs-y=${Math.max(0, visual.placement.row - 1)} gs-w=${Math.max(1, visual.placement.colSpan)} gs-h=${Math.max(1, visual.placement.rowSpan)} role="group" tabindex="0" aria-label=${selected ? `${visual.title}, selected dashboard visual` : `${visual.title}, dashboard visual`} aria-describedby="dashboard-builder-grid-help" style=${styleMap({ '--mobile-order': mobileOrder })} @click=${(event: MouseEvent) => { event.stopPropagation(); this.selectVisualFromPointer(visual.id) }} @keydown=${(event: KeyboardEvent) => this.selectVisualOnKey(event, visual.id)} @dragover=${this.allowFieldDrop} @drop=${(event: DragEvent) => this.dropFieldOnVisual(event, visual.id)}>
        <div class="grid-stack-item-content">
          ${preview
            ? keyed(preview.dataState.kind === 'windowed' ? `${visual.id}:${preview.specRevision}:${this.builderFilterState.revision}` : visual.id, html`<span class="visual-preview"><lv-visualization-host .resizeSuspended=${this.previewResizeSuspended} ?authoring=${previewHasHeader} .envelope=${preview}>${previewHasHeader ? html`<span slot="authoring-drag-handle" class="visual-drag-header component-drag-handle" title="Drag to move ${visual.title}" @pointerdown=${() => this.selectVisualFromPointer(visual.id)}>${visual.title}</span>` : nothing}</lv-visualization-host>${previewHasHeader ? nothing : this.renderComponentDragGrip(visual.title, () => this.selectVisualFromPointer(visual.id))}</span>`)
            : html`<span class="visual-drag-header component-drag-handle" title="Drag to move ${visual.title}" @pointerdown=${() => this.selectVisualFromPointer(visual.id)}>${visual.title}</span><span class="visual-preview-empty" role="status"><strong>${previewLoading ? `Loading ${this.visualLabel(visualType).toLowerCase()}…` : suggestedMeasure ? 'Gauge needs a measure' : `${this.visualLabel(visualType)} preview unavailable`}</strong>${previewLoading ? nothing : requirementMessages.length > 0 ? requirementMessages.map((message) => html`<span>${message}</span>`) : html`<span>${previewIssue || fallbackMessage}</span>`}${suggestedMeasure && !previewLoading ? html`<button type="button" @click=${(event: MouseEvent) => { event.stopPropagation(); this.emitCommand('assign_field', { pageId: page.id, visualId: visual.id, fieldId: suggestedMeasure.id, role: 'metric' }) }}>Use ${suggestedMeasure.label} measure</button>` : nothing}</span><span class="visual-type">${visualType} · ${visual.slots.length} field slots</span>`}
        </div>
      </div>
    `
  }

  private renderFilterComponent(component: DashboardBuilderFilterComponentSignal, page: DashboardBuilderPageSignal) {
    const selected = component.id === this.selectedFilterComponentID
    const mobileOrder = this.renderCache.mobileComponentOrder(component.id, page)
    const bindings = Object.values(this.builderFilterContract.bindings)
    const binding = bindings.find((candidate) => candidate.filter === component.filterId && candidate.scope === 'report')
      ?? bindings.find((candidate) => candidate.filter === component.filterId && candidate.scope === 'page' && candidate.pageID === page.id)
    const definition = binding ? this.builderFilterContract.definitions[binding.filter] : undefined
    const projectedState = this.builderFilterController.projected.revision > 0 ? this.builderFilterController.projected : this.builderFilterState
    const expression = binding
      ? projectedState.draftControls[binding.key] ?? projectedState.appliedControls[binding.key]?.expression ?? binding.default
      : undefined
    const validationMessage = this.builderFilterErrorMessage()
    return html`
      <div class="filter-component grid-stack-item" data-selected=${selected} gs-id=${component.id} gs-x=${Math.max(0, component.placement.col - 1)} gs-y=${Math.max(0, component.placement.row - 1)} gs-w=${Math.max(1, component.placement.colSpan)} gs-h=${Math.max(1, component.placement.rowSpan)} role="group" tabindex="0" aria-label=${selected ? `${component.label}, selected dashboard slicer` : `${component.label}, dashboard slicer`} aria-describedby="dashboard-builder-grid-help" style=${styleMap({ '--mobile-order': mobileOrder })} @click=${(event: MouseEvent) => { event.stopPropagation(); this.selectFilterComponent(component) }} @keydown=${(event: KeyboardEvent) => this.selectFilterComponentOnKey(event, component)}>
        <div class="grid-stack-item-content">
          ${binding ? this.renderComponentDragGrip(component.label, () => this.selectFilterComponent(component)) : html`<span class="filter-drag-header component-drag-handle" title="Drag to move ${component.label}" @pointerdown=${() => this.selectFilterComponent(component)}>${component.label}</span>`}
          ${binding && definition && expression ? html`<lv-slicer
            .definition=${definition}
            .binding=${binding}
            .expression=${expression}
            .options=${this.builderFilterOptionPages[binding.key]}
            .optionContext=${this.builderFilterOptionContext(binding, page.id)}
            .optionRequestReady=${this.builderFilterOptionsReady}
            .pending=${this.builderFilterController.pendingFor(binding.key)}
            .stale=${false}
          ></lv-slicer>` : this.renderFilterControlPreview(component)}
          ${validationMessage ? html`<span class="filter-runtime-note" role="alert">${validationMessage}</span>` : nothing}
        </div>
      </div>
    `
  }

  private renderFilterControlPreview(component: DashboardBuilderFilterComponentSignal) {
    if (component.controlType === 'numericRange' || component.controlType === 'dateRange') {
      return html`<div class="filter-control-preview" aria-label=${`${this.filterControlLabel(component.controlType)} preview`}><div class="filter-preview-range"><span>From</span><span>To</span></div></div>`
    }
    const preview = component.controlType === 'relativePeriod' ? 'Last 30 days' : component.controlType === 'text' ? 'Search values' : 'All'
    return html`<div class="filter-control-preview" aria-label=${`${this.filterControlLabel(component.controlType)} preview`}><div class="filter-preview-input"><span>${preview}</span><span aria-hidden="true">${lucideIcon(component.controlType === 'text' ? Search : ChevronDown, { size: 13, strokeWidth: 2 })}</span></div></div>`
  }

  private renderHeader(header: DashboardBuilderHeaderSignal) {
    const selected = header.id === this.selectedHeaderID
    return html`
      <div class="header-component grid-stack-item" data-selected=${selected} gs-id=${header.id} gs-x=${Math.max(0, header.placement.col - 1)} gs-y=${Math.max(0, header.placement.row - 1)} gs-w=${Math.max(1, header.placement.colSpan)} gs-h=${Math.max(1, header.placement.rowSpan)} role="group" tabindex="0" aria-label=${selected ? `${header.title}, selected dashboard header` : `${header.title}, dashboard header`} aria-describedby="dashboard-builder-grid-help" @click=${(event: MouseEvent) => { event.stopPropagation(); this.selectHeader(header) }} @keydown=${(event: KeyboardEvent) => this.selectHeaderOnKey(event, header)}>
        <div class="grid-stack-item-content">
          <span class="component-drag-grip component-drag-handle" role="button" aria-label=${`Drag to move ${header.title}`} title=${`Drag to move ${header.title}`} @pointerdown=${() => this.selectHeader(header)}>${lucideIcon(GripHorizontal, { size: 16, strokeWidth: 2 })}</span>
          <div class="header-copy"><strong>${header.title}</strong>${header.description ? html`<span>${header.description}</span>` : nothing}</div>
        </div>
      </div>
    `
  }

  private renderPlaceholder(placeholder: DashboardBuilderPlaceholderSignal) {
    return html`
      <div class="builder-placeholder grid-stack-item" data-locked=${placeholder.locked} gs-id=${placeholder.id} gs-x=${Math.max(0, placeholder.placement.col - 1)} gs-y=${Math.max(0, placeholder.placement.row - 1)} gs-w=${Math.max(1, placeholder.placement.colSpan)} gs-h=${Math.max(1, placeholder.placement.rowSpan)} role="note" aria-label=${`${placeholder.title}, locked ${placeholder.kind} placeholder`}>
        <div class="grid-stack-item-content"><strong>${placeholder.title}</strong><span>${placeholder.message}</span><span>Locked until the ${placeholder.kind} definition is restored.</span></div>
      </div>
    `
  }

  private renderComponentDragGrip(label: string, select: () => void) {
    return html`<span class="component-drag-grip component-drag-handle" role="button" aria-label=${`Drag to move ${label}`} title=${`Drag to move ${label}`} @pointerdown=${select}>${lucideIcon(GripHorizontal, { size: 16, strokeWidth: 2 })}</span>`
  }

  private visualPreviewHasHeader(preview: VisualizationEnvelope): boolean {
    return preview.spec.titleVisible !== false && !['kpi', 'table', 'matrix', 'pivot'].includes(preview.spec.kind)
  }

  private builderFilterErrorMessage(): string {
    if (this.builderFilterTransportError) return this.builderFilterTransportError
    const validation = this.builderFilterValidation
    if (!validation.accepted) return validation.message
    const previewError = this.builder?.preview.error?.trim() ?? ''
    if (previewError.includes('compile dashboard filters')) {
      return previewError.includes('narrow targets explicitly')
        ? 'Choose a narrower filter scope for compatible visuals.'
        : 'Review this filter’s field and scope.'
    }
    return ''
  }

  private previewValidationMessage(builder: DashboardBuilderSignal): string {
    const error = builder.preview.error?.trim() ?? ''
    if (builder.preview.active || !error.includes('strictly compile dashboard draft:')) return ''
    return error.includes('compile dashboard filters')
      ? 'Fix the filter scope before publishing'
      : 'Fix draft validation before publishing'
  }

  private renderInspector(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined, header?: DashboardBuilderHeaderSignal) {
    const collapsed = this.collapsedPanes.visuals
    const slicer = page?.filterComponents?.find((component) => component.id === this.selectedFilterComponentID)
    const slicerFilter = slicer ? (builder.filters ?? []).find((filter) => filter.id === slicer.filterId) : undefined
    const slicerActive = Boolean(this.addingSlicer || slicer)
    const formattingPage = Boolean(page && !visual && !header && !slicerActive && this.editingPage)
    const metadataHeader = Boolean(page && header && !slicerActive)
    return html`
      <aside class="pane properties visual-builder" data-collapsed=${collapsed} aria-label=${formattingPage ? 'Page properties' : metadataHeader ? 'Header properties' : 'Visual builder'}>
        <div class="pane-header">
          <div class="inspector-heading">
            <div class="inspector-title">
              <span class="pane-title-icon">${lucideIcon(slicerActive ? ListFilter : ChartColumn, { size: 16, strokeWidth: 2 })}</span>
              <h2 class="pane-title">${collapsed ? 'Visuals' : this.addingSlicer ? 'Add a slicer' : slicer ? slicer.label : visual ? visual.title : header ? header.title : formattingPage ? page?.title : page ? 'Add a visual' : 'Visual builder'}</h2>
              ${visual && !collapsed ? html`<span class="visual-type-badge">${this.titleCase(this.visualTypeForRender(visual))}</span>` : nothing}
              ${slicer && !collapsed ? html`<span class="visual-type-badge">Slicer</span>` : nothing}
              ${formattingPage && !collapsed ? html`<span class="visual-type-badge">Page</span>` : nothing}
              ${metadataHeader && !collapsed ? html`<span class="visual-type-badge">Header</span>` : nothing}
            </div>
            ${this.embeddedInChat ? nothing : this.renderPaneToggle('visuals', 'Visuals pane', 'builder-visuals-content')}
          </div>
          <p class="sr-only" role="status" aria-live="polite">${this.visualActionMessage}</p>
        </div>
        <div id="builder-visuals-content" class="pane-content" ?hidden=${collapsed}>
          ${formattingPage
            ? html`<section class="inspector-panel" aria-label="Page settings">${this.renderPageProperties(page)}</section>`
            : metadataHeader
              ? html`<section class="inspector-panel" aria-label="Header settings">${this.renderHeaderProperties(page, header)}</section>`
            : html`<section class="inspector-panel" aria-label=${slicerActive ? 'Build slicer' : visual ? 'Visual configuration' : 'Add visual'}>
                ${this.renderVisualPicker(builder, page, visual)}
                ${slicerActive
                  ? this.renderSlicerFieldWell(slicerFilter)
                  : visual
                    ? html`${this.renderFieldWells(visual, page?.id ?? '')}${this.renderVisualQueryControls(visual)}${this.renderVisualFormatControls(visual)}${this.renderInteractionEditor(builder, page, visual)}`
                    : page ? html`<div class="inline-page-properties">${this.renderPageProperties(page)}</div>` : nothing}
              </section>`}
          ${this.renderInspectorDetails(builder)}
        </div>
      </aside>
    `
  }

  private renderVisualPicker(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined) {
    const currentType = visual ? this.visualTypeForRender(visual) : undefined
    const slicerSelected = Boolean(this.addingSlicer || this.selectedFilterComponentID)
    const pickerHelpID = 'builder-visual-type-help'
    const catalog = this.visualCatalogGroups(builder.visualCatalog ?? []).flatMap(([, entries]) => entries)
    const selectedEntry = visual ? this.visualCatalogEntry(currentType ?? '', builder) : undefined
    return html`
      <section class="property-group" aria-label=${visual ? 'Edit visual type' : slicerSelected ? 'Edit slicer' : 'Add visual'}>
        <div class="property-heading">
          <span class="property-label">${visual || this.addingSlicer || this.selectedFilterComponentID ? 'Visual type' : 'Add a visual'}</span>
          ${selectedEntry ? html`<a class="visual-reference-link" href=${selectedEntry.referenceHref}>Reference</a>` : nothing}
        </div>
        <p id=${pickerHelpID} class="sr-only">${visual ? `Choose a type to change ${visual.title}.` : 'Choose a type to add it immediately.'}</p>
        <details class="visual-type-disclosure" open>
          <summary>${selectedEntry ? `${selectedEntry.label} · Change type` : slicerSelected ? 'Slicer · Change type' : 'Choose a visual'}</summary>
          <div class="visual-picker-catalog" role="group" aria-label=${visual ? `Change ${visual.title} type` : 'Visual types'}>
            <div class="visual-picker">
              ${catalog.map((entry) => html`
                <button type="button" class="visual-picker-button" data-visual-picker-type=${entry.type} data-visual-type=${entry.type} data-visual-group=${entry.group} aria-label=${visual ? `Change to ${entry.label} visual` : `Add ${entry.label} visual`} aria-describedby=${pickerHelpID} title=${entry.label} aria-pressed=${Boolean(visual && currentType === entry.type)} ?disabled=${this.commandPending || (visual ? !builder.capabilities.canEdit : !page || !builder.capabilities.canAddVisual)} @click=${() => this.selectVisualType(entry.type, visual)}>
                  ${renderVisualTypeIcon(entry.type)}
                  <span class="sr-only">${entry.label}</span>
                </button>
              `)}
              <button type="button" class="visual-picker-button" data-visual-picker-type="slicer" data-visual-type="slicer" data-visual-group="Filters" aria-label="Add slicer" aria-describedby=${pickerHelpID} title="Slicer" aria-pressed=${slicerSelected} ?disabled=${this.commandPending || !page || !builder.capabilities.canEdit} @click=${this.startAddingSlicer}>
                ${renderVisualTypeIcon('slicer')}
                <span class="sr-only">Slicer</span>
              </button>
            </div>
          </div>
        </details>
      </section>
    `
  }

  private renderSlicerFieldWell(filter?: DashboardBuilderFilterSignal) {
    const draggedField = this.draggedFieldID ? this.builder?.semanticModel.datasets.flatMap((dataset) => dataset.fields).find((field) => field.id === this.draggedFieldID) : undefined
    const fieldDrop = draggedField ? this.fieldSupportsFilter(draggedField) ? 'compatible' : 'incompatible' : ''
    const assignedField = filter ? this.builder?.semanticModel.datasets.flatMap((dataset) => dataset.fields).find((field) => field.id === filter.dimension) : undefined
    return html`
      <section class="property-group slicer-field-well" aria-label="Slicer field">
        <div class="property-heading"><span class="property-label">Field</span></div>
        ${filter
          ? html`<div class="field-well-target" aria-label=${`Slicer field ${assignedField?.label ?? filter.dimension}`}><span class="field-pill"><span class="field-token-label">${assignedField?.label ?? filter.dimension}</span></span></div>`
          : html`<div class="field-well-target" data-field-drop=${fieldDrop || nothing} tabindex="0" aria-label="Drop a dimension field for the slicer" @dragover=${this.allowFieldDrop} @drop=${this.dropFieldOnSlicer}><span class="field-placeholder">Drop a dimension here</span></div>`}
      </section>
    `
  }

  private visualCatalogGroups(catalog: DashboardBuilderVisualTypeSignal[]): Array<[string, DashboardBuilderVisualTypeSignal[]]> {
    const groups = new Map<string, DashboardBuilderVisualTypeSignal[]>()
    for (const entry of catalog) groups.set(entry.group, [...(groups.get(entry.group) ?? []), entry])
    return [...groups.entries()]
  }

  private visualCatalogEntry(type: string, builder = this.builder): DashboardBuilderVisualTypeSignal | undefined {
    return this.renderCache.visualCatalogEntry(type, builder)
  }

  private visualLabel(type: string, builder = this.builder): string {
    return this.visualCatalogEntry(type, builder)?.label ?? this.titleCase(type)
  }

  private renderFieldWells(visual: DashboardBuilderVisualSignal, pageID: string) {
    const entry = this.visualCatalogEntry(this.visualTypeForRender(visual))
    const roles = this.hasCompiledPreview(visual)
      ? [...new Set(visual.slots.map(slot => this.slotRole(slot)))]
      : (entry?.roles ?? ['dimension', 'metric']).filter((role): role is BuilderFieldRole => role === 'dimension' || role === 'metric' || role === 'detail')
    const visualTypeSwitchPending = isBuilderVisualTypeSwitchPending(this.commandPending, this.activeCommandAction, this.pendingVisualTypeSwitch, pageID, visual, this.visualTypeForRender(visual))
    const requirements = visualTypeSwitchPending ? [] : this.visualRequirementMessages(visual)
    const previewIssue = visualTypeSwitchPending ? '' : this.visualPreviewErrorMessage(visual)
    const ready = !visualTypeSwitchPending && requirements.length === 0 && !previewIssue
    const heatmapNeedsFields = this.visualTypeForRender(visual) === 'heatmap' && requirements.length > 0
    return html`
      <section class="property-group" aria-label="Field wells">
        <div class="property-heading"><span class="property-label">Fields</span></div>
        ${ready ? nothing : html`<div class="visual-requirements" role="status"><span>${visualTypeSwitchPending ? `Updating ${this.visualLabel(this.visualTypeForRender(visual)).toLowerCase()} preview…` : requirements.length > 0 ? this.visualRequirementSummary(requirements) : previewIssue}</span></div>`}
        ${heatmapNeedsFields ? html`<div class="visual-field-help"><span>Click two dimensions in Data: first sets X, second sets Y. Then select Measures and click one for color. You can also drag fields into the wells.</span><button type="button" @click=${this.focusDataPane}>Browse data fields</button></div>` : nothing}
        <div class="field-wells">${roles.map((role) => this.renderFieldWell(visual, role))}</div>
      </section>
    `
  }

  private readonly focusDataPane = async (): Promise<void> => {
    this.fieldQuery = ''
    this.fieldFilter = 'dimension'
    if (this.collapsedPanes.data) {
      this.collapsedPanes = { ...this.collapsedPanes, data: false }
      this.persistCollapsedPanes()
    }
    await this.updateComplete
    const search = this.shadowRoot?.querySelector<HTMLInputElement>('.data-pane input[aria-label="Search fields"]')
    search?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    search?.focus()
  }

  private renderVisualQueryControls(visual: DashboardBuilderVisualSignal) {
    const editable = Boolean(this.builder?.capabilities.canEdit && !this.commandPending)
    const slots = (visual.slots ?? []).filter((slot) => Boolean(slot.fieldId))
    const sort = visual.queryOptions?.sort ?? []
    const resultFields = slots.map((slot) => ({
      field: slot.alias?.trim() || slot.fieldId || '',
      label: this.fieldLabel(slot.fieldId ?? '', slot.label),
    })).filter((field) => field.field)
    return html`
      <details class="visual-query-controls" aria-label="Query controls">
        <summary>Query</summary>
        <div class="builder-disclosure-content query-control-list">
          ${slots.map((slot) => html`
            <label class="format-text-field">
              <span>${this.fieldLabel(slot.fieldId ?? '', slot.label)} alias</span>
              <input type="text" maxlength="128" data-query-control="alias" data-field-id=${slot.fieldId ?? ''} aria-label=${`${this.fieldLabel(slot.fieldId ?? '', slot.label)} alias`} .value=${slot.alias ?? ''} ?disabled=${!editable} @change=${(event: Event) => this.updateVisualQueryAlias(visual, slot, event)} />
            </label>
            ${this.slotRole(slot) === 'dimension' && this.fieldIsTemporal(slot.fieldId ?? '') ? html`
              <label class="format-text-field">
                <span>${this.fieldLabel(slot.fieldId ?? '', slot.label)} grain</span>
                <select data-query-control="grain" data-field-id=${slot.fieldId ?? ''} aria-label=${`${this.fieldLabel(slot.fieldId ?? '', slot.label)} grain`} ?disabled=${!editable} @change=${(event: Event) => this.updateVisualQueryGrain(visual, slot, event)}>
                  <option value="" ?selected=${!slot.grain}>Default</option>
                  ${(['second', 'minute', 'hour', 'day', 'week', 'month', 'quarter', 'year'] as const).map((grain) => html`<option value=${grain} ?selected=${slot.grain === grain}>${this.titleCase(grain)}</option>`)}
                </select>
              </label>
            ` : nothing}
          `)}
          ${visual.queryOptions?.supportsSort && resultFields.length > 0 ? html`
            <label class="format-text-field">
              <span>Sort field</span>
              <select data-query-control="sort-field" aria-label="Sort field" ?disabled=${!editable} @change=${(event: Event) => this.updateVisualQuerySortField(visual, event)}>
                <option value="">No sort</option>
                ${resultFields.map((field) => html`<option value=${field.field} ?selected=${sort[0]?.field === field.field}>${field.label}</option>`)}
              </select>
            </label>
            <label class="format-text-field">
              <span>Sort direction</span>
              <select data-query-control="sort-direction" aria-label="Sort direction" ?disabled=${!editable || sort.length === 0} @change=${(event: Event) => this.updateVisualQuerySortDirection(visual, event)}>
                <option value="asc" ?selected=${sort[0]?.direction !== 'desc'}>Ascending</option>
                <option value="desc" ?selected=${sort[0]?.direction === 'desc'}>Descending</option>
              </select>
            </label>
          ` : nothing}
          ${visual.queryOptions?.supportsLimit ? html`
            <label class="format-text-field">
              <span>Limit / Top N</span>
              <input type="number" min="1" max="1000" step="1" data-query-control="limit" aria-label="Limit or Top N" .value=${visual.queryOptions?.limit == null ? '' : String(visual.queryOptions.limit)} ?disabled=${!editable} @change=${(event: Event) => this.updateVisualQueryLimit(visual, event)} />
            </label>
          ` : nothing}
        </div>
      </details>
    `
  }

  private updateVisualQueryAlias(visual: DashboardBuilderVisualSignal, slot: DashboardBuilderVisualSlotSignal, event: Event): void {
    const page = this.builder ? this.selectedPage(this.builder) : undefined
    const fieldID = slot.fieldId ?? ''
    if (!this.builder?.capabilities.canEdit || !page || !fieldID || this.commandPending) return
    this.visualActionMessage = `Saving ${this.fieldLabel(fieldID, slot.label)} alias.`
    this.emitCommand('set_visual_query_options', { pageId: page.id, visualId: visual.id, fieldId: fieldID, role: this.slotRole(slot), alias: (event.currentTarget as HTMLInputElement).value })
  }

  private updateVisualQueryGrain(visual: DashboardBuilderVisualSignal, slot: DashboardBuilderVisualSlotSignal, event: Event): void {
    const page = this.builder ? this.selectedPage(this.builder) : undefined
    const fieldID = slot.fieldId ?? ''
    if (!this.builder?.capabilities.canEdit || !page || !fieldID || this.commandPending) return
    const grain = (event.currentTarget as HTMLSelectElement).value
    this.visualActionMessage = `Saving ${this.fieldLabel(fieldID, slot.label)} time grain.`
    this.emitCommand('set_visual_query_options', { pageId: page.id, visualId: visual.id, fieldId: fieldID, role: 'dimension', ...(grain ? { grain } : { clearGrain: true }) })
  }

  private updateVisualQuerySortField(visual: DashboardBuilderVisualSignal, event: Event): void {
    const page = this.builder ? this.selectedPage(this.builder) : undefined
    if (!this.builder?.capabilities.canEdit || !page || this.commandPending) return
    const field = (event.currentTarget as HTMLSelectElement).value
    const direction = visual.queryOptions?.sort[0]?.direction ?? 'asc'
    this.visualActionMessage = field ? 'Saving visual sort.' : 'Clearing visual sort.'
    this.emitCommand('set_visual_query_options', { pageId: page.id, visualId: visual.id, sort: field ? [{ field, direction }] : [] })
  }

  private updateVisualQuerySortDirection(visual: DashboardBuilderVisualSignal, event: Event): void {
    const page = this.builder ? this.selectedPage(this.builder) : undefined
    const current = visual.queryOptions?.sort[0]
    if (!this.builder?.capabilities.canEdit || !page || !current || this.commandPending) return
    const direction = (event.currentTarget as HTMLSelectElement).value as 'asc' | 'desc'
    this.visualActionMessage = 'Saving visual sort direction.'
    this.emitCommand('set_visual_query_options', { pageId: page.id, visualId: visual.id, sort: [{ field: current.field, direction }] })
  }

  private updateVisualQueryLimit(visual: DashboardBuilderVisualSignal, event: Event): void {
    const page = this.builder ? this.selectedPage(this.builder) : undefined
    if (!this.builder?.capabilities.canEdit || !page || this.commandPending) return
    const raw = (event.currentTarget as HTMLInputElement).value.trim()
    if (!raw) {
      this.visualActionMessage = 'Clearing visual limit.'
      this.emitCommand('set_visual_query_options', { pageId: page.id, visualId: visual.id, clearLimit: true })
      return
    }
    const limit = Math.max(1, Math.min(1000, Number.parseInt(raw, 10)))
    if (!Number.isFinite(limit)) return
    this.visualActionMessage = 'Saving visual limit.'
    this.emitCommand('set_visual_query_options', { pageId: page.id, visualId: visual.id, limit })
  }

  private renderInteractionEditor(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal) {
    if (!page) return nothing
    const source = visual as DashboardBuilderVisualWithInteraction
    const interaction = source.interaction
    const sourceDefinitionID = this.visualSignalID(visual)
    const targets = page.visuals.filter((candidate, index, values) => {
      const definitionID = this.visualSignalID(candidate)
      return definitionID !== sourceDefinitionID && values.findIndex((item) => this.visualSignalID(item) === definitionID) === index
    })
    const helpID = `interaction-help-${visual.id}`
    const editable = Boolean(builder.capabilities.canEdit && interaction?.editable && interaction.mappings.length > 0 && !this.commandPending)
    return html`
      <details class="builder-disclosure interaction-editor" aria-label="Visual interactions">
        <summary><span>Interactions</span>${targets.length > 0 ? html`<span class="disclosure-count">${targets.length}</span>` : nothing}</summary>
        <div class="builder-disclosure-content">
        <p id=${helpID} class="sr-only">Choose what happens to other visuals when users select data in this visual.</p>
        ${!interaction
          ? html`<p class="pane-hint">Interaction settings are unavailable until this visual has a valid preview.</p>`
          : !interaction.editable
            ? html`<p class="pane-hint" role="status">${interaction.message || 'This interaction is configured in dashboard code and cannot be edited here.'}</p>`
            : targets.length === 0
              ? html`<p class="pane-hint">Add another visual to configure an interaction.</p>`
              : html`
                <fieldset class="interaction-targets" aria-describedby=${helpID}>
                  <legend class="sr-only">Target visuals</legend>
                  ${targets.map((target) => this.renderInteractionTarget(page, visual, target, interaction, editable))}
                </fieldset>
              `}
        </div>
      </details>
    `
  }

  private renderInteractionTarget(page: DashboardBuilderPageSignal, source: DashboardBuilderVisualSignal, target: DashboardBuilderVisualSignal, interaction: DashboardBuilderInteractionSignal, editable: boolean) {
    const effect = this.interactionEffect(source, target, interaction)
    const effects: BuilderInteractionEffect[] = ['filter', 'highlight', 'none']
    const description: Record<BuilderInteractionEffect, string> = {
      filter: `Filter ${target.title} to the selected data`,
      highlight: `Highlight the selected data in ${target.title} while retaining its comparison`,
      none: `Leave ${target.title} unchanged`,
    }
    return html`
      <div class="interaction-target" data-interaction-target=${this.visualSignalID(target)}>
        <span class="interaction-target-title" title=${target.title}>${target.title}</span>
        <div class="interaction-effects" role="radiogroup" aria-label=${`Effect on ${target.title}`}>
          ${effects.map((candidate) => {
            const supported = this.interactionEffectSupported(candidate, target, interaction) || candidate === effect
            return html`
              <label class="interaction-effect" data-effect=${candidate} data-selected=${candidate === effect} title=${description[candidate]}>
                <input
                  type="radio"
                  name=${`interaction-${source.id}-${target.id}`}
                  value=${candidate}
                  .checked=${candidate === effect}
                  ?disabled=${!editable || !supported}
                  aria-label=${`${this.titleCase(candidate)} ${target.title}`}
                  @change=${() => this.setInteractionTarget(page, source, target, candidate)}
                />
                <span>${this.titleCase(candidate)}</span>
              </label>
            `
          })}
        </div>
      </div>
    `
  }

  private interactionEffect(source: DashboardBuilderVisualSignal, target: DashboardBuilderVisualSignal, interaction: DashboardBuilderInteractionSignal): BuilderInteractionEffect {
    const override = this.interactionEffectOverrides[this.interactionOverrideKey(source, target)]
    if (override) return override
    const targetID = this.visualSignalID(target)
    if (interaction.targets.includes(targetID)) return 'filter'
    if (interaction.highlightTargets.includes(targetID)) return 'highlight'
    return 'none'
  }

  private interactionEffectSupported(effect: BuilderInteractionEffect, target: DashboardBuilderVisualSignal, interaction: DashboardBuilderInteractionSignal): boolean {
    if (effect !== 'highlight') return true
    const fields = new Set(target.slots.map((slot) => slot.fieldId).filter((field): field is string => Boolean(field)))
    return interaction.mappings.every((mapping) => fields.has(mapping.value))
  }

  private interactionOverrideKey(source: DashboardBuilderVisualSignal, target: DashboardBuilderVisualSignal): string {
    return `${this.visualSignalID(source)}\u0000${this.visualSignalID(target)}`
  }

  private setInteractionTarget(page: DashboardBuilderPageSignal, source: DashboardBuilderVisualSignal, target: DashboardBuilderVisualSignal, effect: BuilderInteractionEffect): void {
    const builder = this.builder
    const interaction = (source as DashboardBuilderVisualWithInteraction).interaction
    if (!builder?.capabilities.canEdit || !interaction?.editable || interaction.mappings.length === 0 || this.commandPending) return
    const key = this.interactionOverrideKey(source, target)
    this.interactionEffectOverrides = { ...this.interactionEffectOverrides, [key]: effect }
    this.interactionOverridesRevision = this.revisionKey(builder)
    this.visualActionMessage = `${this.titleCase(effect)} interaction for ${target.title} is saving.`
    this.emitCommand('set_interaction_target', {
      pageId: page.id,
      visualId: source.id,
      targetVisualId: target.id,
      effect,
    })
  }

  private renderFieldWell(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole) {
    const slots = visual.slots.filter((slot) => this.slotRole(slot) === role)
    const label = this.fieldWellLabel(visual, role)
    const heatmapAxis = this.visualTypeForRender(visual) === 'heatmap' && role === 'dimension' && slots.length < 2
    const draggedField = this.draggedFieldFromBuilder(this.builder)
    const fieldDrop = draggedField ? (this.fieldCompatibleWithRole(draggedField, role) && this.roleHasCapacity(visual, role) ? 'compatible' : 'incompatible') : ''
    return html`
      <section class="field-well">
        <div class="field-well-label"><span>${label}</span>${slots.length > 0 ? html`<span>${slots.length}</span>` : nothing}</div>
        <div class="field-well-target" data-drop-well=${role} data-field-drop=${fieldDrop || nothing} tabindex="0" aria-label=${`Drop ${role} field in ${label}`} @dragover=${this.allowFieldDrop} @drop=${(event: DragEvent) => this.dropFieldOnRole(event, role)}>
          ${slots.map((slot, index) => this.renderFieldToken(visual, role, slot, index, slots.length))}
          ${slots.length === 0 || heatmapAxis
            ? html`<span class="empty-well">${heatmapAxis ? `Drop ${slots.length === 0 ? 'X' : 'Y'} dimension` : `Drop ${role === 'metric' ? 'a measure' : role === 'detail' ? 'a column' : 'a dimension'}`}</span>`
            : nothing}
        </div>
      </section>
    `
  }

  private renderFieldToken(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole, slot: DashboardBuilderVisualSlotSignal, index: number, count: number) {
    const fieldID = slot.fieldId ?? ''
    const label = this.fieldLabel(fieldID, slot.label)
    const editable = Boolean(this.builder?.capabilities.canEdit && fieldID && !this.commandPending)
    const alternateRole = this.alternateFieldRole(visual, role)
    return html`
      <span class="field-token field-pill" data-field-id=${fieldID} data-field-role=${role}>
        <span class="field-token-label" title=${label}>${label}</span>
        <span class="field-token-actions" aria-label=${`${label} field actions`}>
          <button type="button" class="field-token-action" data-field-action="remove" aria-label=${`Remove ${label} field`} title="Remove field" ?disabled=${!editable} @click=${() => this.removeField(visual, role, fieldID, label)}>${lucideIcon(X, { size: 12, strokeWidth: 2 })}</button>
          <button type="button" class="field-token-action" data-field-action="move-up" aria-label=${`Move ${label} field up`} title="Move field up" ?disabled=${!editable || index === 0} @click=${() => this.moveField(visual, role, fieldID, 'up', label)}>${lucideIcon(ArrowUp, { size: 12, strokeWidth: 2 })}</button>
          <button type="button" class="field-token-action" data-field-action="move-down" aria-label=${`Move ${label} field down`} title="Move field down" ?disabled=${!editable || index === count - 1} @click=${() => this.moveField(visual, role, fieldID, 'down', label)}>${lucideIcon(ArrowDown, { size: 12, strokeWidth: 2 })}</button>
          <button type="button" class="field-token-action" data-field-action="move-role" aria-label=${alternateRole ? `Move ${label} field to ${this.fieldWellLabel(visual, alternateRole)}` : `Move ${label} field to another role`} title=${alternateRole ? `Move to ${this.fieldWellLabel(visual, alternateRole)}` : 'No compatible role for this field'} ?disabled=${!editable || !alternateRole} @click=${() => alternateRole && this.moveFieldRole(visual, role, alternateRole, fieldID, label)}>${lucideIcon(ArrowLeftRight, { size: 12, strokeWidth: 2 })}</button>
        </span>
      </span>
    `
  }

  private renderVisualFormatControls(visual: DashboardBuilderVisualSignal) {
    const editable = Boolean(this.builder?.capabilities.canEdit && !this.commandPending)
    const sections = new Map<string, DashboardBuilderFormatOptionSignal[]>()
    const formatOptions = visual.formatOptions ?? []
    for (const option of formatOptions) sections.set(option.section, [...(sections.get(option.section) ?? []), option])
    const reference = this.visualCatalogEntry(this.visualTypeForRender(visual))
    return html`
      <details class="visual-format-disclosure builder-disclosure" aria-label="Formatting controls">
        <summary>Format</summary>
        <section class="format-controls visual-format-controls builder-disclosure-content" aria-label="Visual formatting">
          <div class="format-section">
            <h3>Title</h3>
            <label class="format-text-field">
              <span>Title text</span>
              <input type="text" maxlength="128" data-format-control="title-text" aria-label="Title text" .value=${visual.title} ?disabled=${!editable} @change=${(event: Event) => this.updateVisualTitle(visual, event)} />
            </label>
            ${this.renderFormatToggle(visual, 'title-visible', 'Show title', visual.titleVisible !== false, editable, 'titleVisible')}
          </div>
          ${[...sections.entries()].map(([section, options]) => html`
            <div class="format-section" data-format-section=${section}>
              <h3>${section}</h3>
              ${options.map((option) => this.renderFormatOption(visual, option, editable))}
            </div>
          `)}
          ${formatOptions.length === 0 ? html`<p class="pane-hint">This presentation has no additional formatting controls. Configure advanced options in dashboard code.</p>` : nothing}
          ${reference ? html`<a class="visual-reference-link" href=${reference.referenceHref}>View every ${reference.label} option in the visual reference</a>` : nothing}
        </section>
      </details>
    `
  }

  private renderFormatOption(visual: DashboardBuilderVisualSignal, option: DashboardBuilderFormatOptionSignal, editable: boolean) {
    if (option.control === 'toggle') {
      return html`
        <label class="format-toggle">
          <span>${option.label}</span>
          <input type="checkbox" data-format-control=${option.key} aria-label=${option.label} .checked=${option.value === 'true'} ?disabled=${!editable} @change=${(event: Event) => this.updateVisualFormatOption(visual, option.key, String((event.currentTarget as HTMLInputElement).checked))} />
        </label>
      `
    }
    if (option.control === 'select') {
      return html`
        <label class="format-text-field">
          <span>${option.label}</span>
          <select data-format-control=${option.key} aria-label=${option.label} ?disabled=${!editable} @change=${(event: Event) => this.updateVisualFormatOption(visual, option.key, (event.currentTarget as HTMLSelectElement).value)}>
            ${option.choices.map((choice) => html`<option value=${choice.value} ?selected=${choice.value === option.value}>${choice.label}</option>`)}
          </select>
        </label>
      `
    }
    const inputType = option.control === 'number' ? 'number' : 'text'
    return html`
      <label class="format-text-field">
        <span>${option.label}</span>
        <input type=${inputType} maxlength=${inputType === 'text' ? '256' : nothing} step=${inputType === 'number' ? 'any' : nothing} data-format-control=${option.key} aria-label=${option.label} placeholder=${option.placeholder ?? ''} .value=${option.value} ?disabled=${!editable} @change=${(event: Event) => this.updateVisualFormatOption(visual, option.key, (event.currentTarget as HTMLInputElement).value)} />
      </label>
    `
  }

  private renderFormatToggle(visual: DashboardBuilderVisualSignal, control: string, label: string, checked: boolean, enabled: boolean, field: keyof Omit<BuilderVisualFormatPatch, 'title'>) {
    return html`
      <label class="format-toggle">
        <span>${label}</span>
        <input type="checkbox" data-format-control=${control} aria-label=${label} .checked=${checked} ?disabled=${!enabled} @change=${(event: Event) => this.updateVisualFormat(visual, { [field]: (event.currentTarget as HTMLInputElement).checked })} />
      </label>
    `
  }

  private fieldLabel(fieldID: string, fallback: string): string {
    const builder = this.builder
    const field = builder?.semanticModel.datasets.flatMap((dataset) => dataset.fields).find((candidate) => candidate.id === fieldID)
    if (field?.label.trim()) return field.label.trim()
    const source = fallback.trim() || fieldID.split('.').at(-1) || 'Field'
    const label = source.replace(/[_-]+/g, ' ').replace(/\s+/g, ' ').trim()
    return label ? label.charAt(0).toLocaleUpperCase() + label.slice(1) : 'Field'
  }

  private fieldIsTemporal(fieldID: string): boolean {
    const field = this.builder?.semanticModel.datasets.flatMap((dataset) => dataset.fields).find((candidate) => candidate.id === fieldID)
    return Boolean(field && builderFieldCatalogGroup(field) === 'time')
  }

  private alternateFieldRole(_visual: DashboardBuilderVisualSignal, _role: BuilderFieldRole): BuilderFieldRole | undefined {
    // The V1 chart wells are semantic: dimensions cannot be reclassified as
    // measures, and record details cannot be moved into aggregate axes. Keep
    // the affordance discoverable but disabled until a visual exposes two
    // compatible roles (for example category and series dimensions).
    return undefined
  }

  private removeField(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole, fieldID: string, label: string): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || !fieldID || this.commandPending) return
    this.gridInteractionMessage = `Removing ${label} from ${this.fieldWellLabel(visual, role)}.`
    this.emitCommand('remove_field', { pageId: page.id, visualId: visual.id, fieldId: fieldID, role })
  }

  private moveField(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole, fieldID: string, direction: 'up' | 'down', label: string): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || !fieldID || this.commandPending) return
    this.gridInteractionMessage = `Moving ${label} ${direction} in ${this.fieldWellLabel(visual, role)}.`
    this.emitCommand('move_field', { pageId: page.id, visualId: visual.id, fieldId: fieldID, role, direction })
  }

  private moveFieldRole(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole, targetRole: BuilderFieldRole, fieldID: string, label: string): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || !fieldID || role === targetRole || this.commandPending) return
    this.gridInteractionMessage = `Moving ${label} to ${this.fieldWellLabel(visual, targetRole)}.`
    this.emitCommand('move_field', { pageId: page.id, visualId: visual.id, fieldId: fieldID, role, targetRole })
  }

  private updateVisualTitle(visual: DashboardBuilderVisualSignal, event: Event): void {
    const input = event.currentTarget as HTMLInputElement
    const title = input.value.trim()
    if (!title) {
      input.value = visual.title
      this.visualActionMessage = 'Visual title cannot be empty.'
      return
    }
    if (title === visual.title) return
    this.updateVisualFormat(visual, { title })
  }

  private updateVisualFormat(visual: DashboardBuilderVisualSignal, patch: BuilderVisualFormatPatch): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || this.commandPending) return
    this.visualActionMessage = `Saving formatting for ${visual.title}.`
    this.emitCommand('update_visual_format', { pageId: page.id, visualId: visual.id, ...patch })
  }

  private updateVisualFormatOption(visual: DashboardBuilderVisualSignal, formatKey: string, formatValue: string): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || this.commandPending) return
    this.visualActionMessage = `Saving ${this.visualLabel(this.visualTypeForRender(visual), builder)} formatting.`
    this.emitCommand('update_visual_format', { pageId: page.id, visualId: visual.id, formatKey, formatValue })
  }

  private renderPageProperties(page: DashboardBuilderPageSignal | undefined) {
    if (!page) return html`<span class="pane-hint">Select a page to edit its properties.</span>`
    const editable = Boolean(this.builder?.capabilities.canEdit && !this.commandPending)
    const minimumColumns = Math.max(1, ...[...page.visuals, ...(page.filterComponents ?? [])].map((component) => component.placement.col + component.placement.colSpan - 1))
    return html`
      <section class="format-controls" aria-label="Page formatting">
        <div class="format-section">
          <h3>Page</h3>
          <label class="format-text-field">
            <span>Page name</span>
            <input type="text" maxlength="128" data-page-control="title" aria-label="Page name" .value=${page.title} ?disabled=${!editable} @change=${(event: Event) => this.updatePageTitle(page, event)} />
          </label>
          <label class="format-text-field">
            <span>Page description</span>
            <textarea maxlength="512" aria-label="Page description" ?disabled=${!editable} @change=${(event: Event) => this.updatePageDescription(page, event)}>${page.description ?? ''}</textarea>
          </label>
          ${page.canvas.width > 0 && page.canvas.height > 0 ? html`<p class="page-format-summary"><span>Current canvas</span><span>${page.canvas.width} × ${page.canvas.height}</span></p>` : nothing}
        </div>
        <div class="format-section">
          <h3>Grid</h3>
          <div class="page-layout-grid">
            <label class="format-text-field"><span>Columns</span><input type="number" min=${minimumColumns} step="1" data-page-control="columns" aria-label="Grid columns" .value=${String(page.grid.columns)} ?disabled=${!editable} @change=${(event: Event) => this.updatePageLayout(page, 'columns', event, minimumColumns)} /></label>
            <label class="format-text-field"><span>Row height</span><input type="number" min="1" step="1" data-page-control="rowHeight" aria-label="Grid row height" .value=${String(page.grid.rowHeight)} ?disabled=${!editable} @change=${(event: Event) => this.updatePageLayout(page, 'rowHeight', event, 1)} /></label>
            <label class="format-text-field"><span>Gap</span><input type="number" min="0" step="1" data-page-control="gap" aria-label="Grid gap" .value=${String(page.grid.gap)} ?disabled=${!editable} @change=${(event: Event) => this.updatePageLayout(page, 'gap', event, 0)} /></label>
            <label class="format-text-field"><span>Padding</span><input type="number" min="0" step="1" data-page-control="padding" aria-label="Grid padding" .value=${String(page.grid.padding)} ?disabled=${!editable} @change=${(event: Event) => this.updatePageLayout(page, 'padding', event, 0)} /></label>
          </div>
          <p class="pane-hint">The grid is stored on this page in dashboard code. Columns cannot clip an existing visual.</p>
        </div>
      </section>
    `
  }

  private renderHeaderProperties(page: DashboardBuilderPageSignal | undefined, header: DashboardBuilderHeaderSignal | undefined) {
    if (!page || !header) return html`<span class="pane-hint">Select a header to edit its properties.</span>`
    const editable = Boolean(this.builder?.capabilities.canEdit && !this.commandPending)
    return html`
      <section class="format-controls" aria-label="Header formatting">
        <div class="format-section">
          <h3>Header</h3>
          <label class="format-text-field">
            <span>Header title</span>
            <input type="text" maxlength="128" data-header-control="title" aria-label="Header title" .value=${header.title} ?disabled=${!editable} @change=${(event: Event) => this.updateHeaderTitle(page, header, event)} />
          </label>
          <label class="format-text-field">
            <span>Header description</span>
            <textarea maxlength="512" data-header-control="description" aria-label="Header description" ?disabled=${!editable} @change=${(event: Event) => this.updateHeaderDescription(page, header, event)}>${header.description ?? ''}</textarea>
          </label>
        </div>
      </section>
    `
  }

  private renderDiagnostics(diagnostics: DashboardBuilderDiagnosticSignal[]) {
    if (diagnostics.length === 0) return nothing
    return html`<section class="property-group" aria-label="Validation diagnostics"><span class="property-label">Validation</span><div class="diagnostics">${diagnostics.map((item) => html`<div class="diagnostic ${item.severity}" role=${item.severity === 'error' ? 'alert' : 'status'}><strong>${item.code}</strong> ${item.message}</div>`)}</div></section>`
  }

  private renderInspectorDetails(builder: DashboardBuilderSignal) {
    const diagnostics = builder.diagnostics ?? []
    const evidence = this.sourceEvidenceLabel(builder)
    if (diagnostics.length === 0 && !evidence) return nothing
    const errorCount = diagnostics.filter((item) => item.severity === 'error').length
    const validationLabel = errorCount > 0
      ? `Fix ${errorCount} validation ${errorCount === 1 ? 'error' : 'errors'}`
      : `Validation (${diagnostics.length})`
    return html`
      <div class="properties-body">
        ${diagnostics.length > 0 ? html`
          <details class="secondary-details validation-details" ?open=${errorCount > 0}>
            <summary>${validationLabel}</summary>
            <div class="secondary-details-content">${this.renderDiagnostics(diagnostics)}</div>
          </details>
        ` : nothing}
        ${evidence ? html`
          <details class="secondary-details technical-details">
            <summary>Technical details</summary>
            <div class="secondary-details-content">
              <section class="property-group" aria-label="Dashboard source">
                <span class="property-label">Source</span>
                <div class="evidence"><span>${evidence}</span></div>
              </section>
            </div>
          </details>
        ` : nothing}
      </div>
    `
  }

  private sourceEvidenceLabel(builder: DashboardBuilderSignal): string {
    const evidence = builder.sourceEvidence
    if (!evidence?.projectId || !evidence.dashboardId || !evidence.generationId) return ''
    return `${evidence.projectId}/${evidence.dashboardId} · ${evidence.generationId}${evidence.path ? ` · ${evidence.path}` : ''}`
  }

  private get renderCache(): BuilderRenderCache {
    return this.builderRenderCache ?? new BuilderRenderCache()
  }

  private filteredCatalog(catalog: BuilderCatalogField[]): BuilderCatalogField[] {
    const query = this.fieldQuery.trim().toLowerCase()
    if (!query) return catalog
    return catalog.filter((item) => [
      item.field.label,
      item.field.id,
      item.field.dataType,
      item.field.description ?? '',
      this.fieldGroupLabel(item.group),
      ...item.datasets.flatMap((dataset) => [dataset.id, dataset.title]),
    ].join(' ').toLowerCase().includes(query))
  }

  private fieldFilterLabel(filter: BuilderFieldFilter): string {
    if (filter === 'all') return 'All'
    return this.fieldGroupLabel(filter)
  }

  private fieldGroupLabel(group: Exclude<BuilderFieldFilter, 'all'>, singular = false): string {
    if (group === 'metric') return singular ? 'Measure' : 'Measures'
    if (group === 'time') return 'Time'
    return singular ? 'Dimension' : 'Dimensions'
  }

  private catalogFieldKey(item: BuilderCatalogField): string {
    return `${item.field.kind}:${[...(item.field.roles ?? [])].sort().join(',')}:${item.field.id}:${item.field.datasetId ?? ''}`
  }

  private fieldUsedIn(field: DashboardBuilderFieldSignal, visual: DashboardBuilderVisualSignal): string {
    const labels = visual.slots
      .filter((slot) => this.fieldMatchesSlot(field, slot, visual))
      .map((slot) => this.fieldWellLabel(visual, this.slotRole(slot)))
    return Array.from(new Set(labels)).join(', ')
  }

  private renderFieldRoleIcon(group: Exclude<BuilderFieldFilter, 'all'>) {
    if (group === 'metric') {
      return html`<svg viewBox="0 0 24 24"><rect x="4" y="3" width="16" height="18" rx="2"></rect><path d="M8 7h8M8 12h2M14 12h2M8 16h2M14 16h2"></path></svg>`
    }
    if (group === 'time') {
      return html`<svg viewBox="0 0 24 24"><rect x="3" y="5" width="18" height="16" rx="2"></rect><path d="M16 3v4M8 3v4M3 10h18M8 14h.01M12 14h.01M16 14h.01M8 18h.01M12 18h.01"></path></svg>`
    }
    return html`<svg viewBox="0 0 24 24"><path d="M20 13 13 20l-9-9V4h7l9 9Z"></path><path d="M8.5 8.5h.01"></path></svg>`
  }

  private fieldCompatibleWithVisual(field: DashboardBuilderFieldSignal, visual: DashboardBuilderVisualSignal): boolean {
    if (!this.fieldDataTypeSupported(field)) return false
    const role = this.roleForField(field, visual)
    const entry = this.visualCatalogEntry(this.visualTypeForRender(visual))
    return Boolean(entry?.roles.includes(role) && this.fieldCompatibleWithRole(field, role) && this.fieldMatchesVisualDataset(field, visual, role) && this.roleHasCapacity(visual, role))
  }

  private fieldMatchesVisualDataset(field: DashboardBuilderFieldSignal, visual: DashboardBuilderVisualSignal, role: BuilderFieldRole): boolean {
    if (role !== 'detail' || !visual.datasetId) return true
    return !field.datasetId || field.datasetId === visual.datasetId
  }

  private fieldMatchesSlot(field: DashboardBuilderFieldSignal, slot: DashboardBuilderVisualSlotSignal, visual: DashboardBuilderVisualSignal): boolean {
    if (this.slotRole(slot) !== 'detail') return slot.fieldId === field.id
    if (!field.roles?.includes('detail')) return false
    if (visual.datasetId && field.datasetId && visual.datasetId !== field.datasetId) return false
    const fieldID = field.id.includes('.') ? field.id.slice(field.id.indexOf('.') + 1) : field.id
    return slot.fieldId === field.id || slot.fieldId === fieldID
  }

  private fieldDataTypeSupported(field: DashboardBuilderFieldSignal): boolean {
    const dataType = field.dataType.trim().toLowerCase()
    return !['opaque', 'binary', 'blob', 'object', 'struct', 'list', 'map'].some((unsupported) => dataType.includes(unsupported))
  }

  private fieldCompatibleWithRole(field: DashboardBuilderFieldSignal, role: BuilderFieldRole): boolean {
    if (!this.fieldDataTypeSupported(field)) return false
    if (field.roles?.length) return field.roles.includes(role)
    if (role === 'detail') return field.kind === 'dimension'
    return field.kind === role
  }

  private fieldSupportsRole(field: DashboardBuilderFieldSignal, role: BuilderFieldRole): boolean {
    if (field.roles?.length) return field.roles.includes(role)
    if (role === 'detail') return field.kind === 'dimension'
    return field.kind === role
  }

  private roleHasCapacity(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole): boolean {
    const limit = this.visualCatalogEntry(this.visualTypeForRender(visual))?.roleLimits?.find((candidate) => candidate.role === role)
    if (!limit || limit.maximum <= 0) return true
    return visual.slots.filter((slot) => this.slotRole(slot) === role).length < limit.maximum
  }

  private visualRequirementMessages(visual: DashboardBuilderVisualSignal): string[] {
    if (this.hasCompiledPreview(visual)) return []
    const entry = this.visualCatalogEntry(this.visualTypeForRender(visual))
    if (!entry) return []
    const messages: string[] = []
    for (const requirement of entry.roleLimits ?? []) {
      const count = visual.slots.filter((slot) => this.slotRole(slot) === requirement.role).length
      if (count < requirement.minimum) {
        const missing = requirement.minimum - count
        messages.push(this.visualTypeForRender(visual) === 'map' && requirement.role === 'dimension' ? 'Choose numeric latitude and longitude fields to preview this map.' : `Add ${missing} ${this.requirementRoleLabel(visual, requirement.role, missing)} to preview.`)
      }
      if (requirement.maximum > 0 && count > requirement.maximum) {
        const extra = count - requirement.maximum
        messages.push(`Remove ${extra} ${this.requirementRoleLabel(visual, requirement.role, extra)} to preview.`)
      }
    }
    return messages
  }

  private visualRequirementSummary(messages: string[]): string {
    const additions = messages.map((message) => message.match(/^Add (.+) to preview\.$/)?.[1])
    if (additions.every((item): item is string => Boolean(item))) return `Needs ${additions.join(' · ')}.`
    return messages.join(' ')
  }

  private hasCompiledPreview(visual: DashboardBuilderVisualSignal): boolean {
    return hasCompiledBuilderPreview(visual, this.visualTypeForRender(visual), Boolean(this.builder?.preview.active), this.builderVisuals[this.visualSignalID(visual)])
  }

  private visualPreviewErrorMessage(visual: DashboardBuilderVisualSignal): string {
    const message = visual.previewError?.trim() ?? ''
    if (!message) return ''
    const marker = `visual "${this.visualSignalID(visual)}"`
    const markerIndex = message.indexOf(marker)
    const detail = (markerIndex >= 0 ? message.slice(markerIndex + marker.length) : message)
      .replace(/^\s*:?\s*(query|presentation|references|result aliases|interactions|geographic delivery|calculations|IR|definition):\s*/i, '')
      .trim()
    if (!detail) return 'Preview unavailable for this field combination.'
    if (this.visualTypeForRender(visual) === 'map' && /(?:latitude|longitude) field must be numeric/i.test(detail)) {
      return 'Choose numeric latitude and longitude fields to preview this map.'
    }
    const bounded = detail.length > 180 ? `${detail.slice(0, 177)}…` : detail
    return bounded.charAt(0).toUpperCase() + bounded.slice(1)
  }

  private requirementRoleLabel(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole, count: number): string {
    let label = role === 'metric' ? 'measure' : role === 'detail' ? 'column' : 'dimension'
    if (this.visualTypeForRender(visual) === 'map' && role === 'detail') label = count === 1 ? 'coordinate column' : 'coordinate columns'
    else if (count !== 1) label += 's'
    return label
  }

  private roleForField(field: DashboardBuilderFieldSignal, visual: DashboardBuilderVisualSignal): BuilderFieldRole {
    const type = this.visualTypeForRender(visual)
    if (type === 'table') return 'detail'
    if (type === 'kpi') return 'metric'
    return field.kind
  }

  private slotRole(slot: DashboardBuilderVisualSlotSignal): BuilderFieldRole {
    if (slot.kind === 'detail') return 'detail'
    if (slot.kind === 'metric' || slot.kind === 'value') return 'metric'
    return 'dimension'
  }

  private fieldWellLabel(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole): string {
    if (role === 'detail') return 'Columns'
    const type = this.visualTypeForRender(visual)
    if (type === 'map') return role === 'dimension' ? 'Dimensions' : 'Measures'
    if (type === 'kpi') return 'Value'
    if (type === 'matrix' || type === 'pivot') return role === 'dimension' ? 'Rows / Columns' : 'Values'
    if (type === 'heatmap') return role === 'dimension' ? 'Dimensions (X then Y)' : 'Color value'
    if (['pie', 'donut', 'funnel', 'treemap', 'sunburst'].includes(type)) return role === 'dimension' ? 'Category' : 'Values'
    const horizontal = type === 'bar'
    if (role === 'dimension') return horizontal ? 'Y-axis' : 'X-axis'
    return horizontal ? 'X-axis' : 'Y-axis'
  }

  private selectedPage(builder: DashboardBuilderSignal): DashboardBuilderPageSignal | undefined {
    const id = this.localPageID || builder.selectedPageId
    return builder.pages.find((page) => page.id === id) ?? builder.pages[0]
  }

  private selectedVisual(page: DashboardBuilderPageSignal, builder: DashboardBuilderSignal): DashboardBuilderVisualSignal | undefined {
    const id = this.effectiveVisualID(builder, page)
    return page.visuals.find((visual) => visual.id === id)
  }

  private selectedHeader(page: DashboardBuilderPageSignal): DashboardBuilderHeaderSignal | undefined {
    return page.headers?.find((header) => header.id === this.selectedHeaderID)
  }

  private effectiveVisualID(builder: DashboardBuilderSignal | null, page: DashboardBuilderPageSignal): string {
    if (this.localVisualID !== null) {
      if (this.localVisualID && page.visuals.some((visual) => visual.id === this.localVisualID)) return this.localVisualID
      return ''
    }
    if (builder?.selectedVisualId && page.visuals.some((visual) => visual.id === builder.selectedVisualId)) return builder.selectedVisualId
    return page.visuals[0]?.id ?? ''
  }

  private toggleVisibility = (): void => {
    const builder = this.builder
    if (!builder?.capabilities.canShare) return
    this.emitCommand('set_visibility', { visibility: builder.visibility === 'organization' ? 'private' : 'organization' })
  }

  private updateDashboardAppearance = (event: CustomEvent<{ icon?: string; color?: string }>): void => {
    const builder = this.builder
    if (!builder?.capabilities.canEdit || this.commandPending) return
    const reset = event.detail.icon === 'default' || event.detail.color === 'default'
    const icon = reset ? 'layout-dashboard' : event.detail.icon ?? builder.appearance.icon
    const color = reset ? 'purple' : event.detail.color ?? builder.appearance.color
    if (icon === builder.appearance.icon && color === builder.appearance.color) return
    this.visualActionMessage = 'Saving dashboard icon and color.'
    this.emitCommand('update_appearance', { icon, color })
  }

  private updateDashboardTitle = (event: Event): void => {
    const builder = this.builder
    const title = (event.currentTarget as HTMLInputElement).value.trim()
    if (!builder || !title) {
      if (builder) (event.currentTarget as HTMLInputElement).value = builder.title
      this.visualActionMessage = 'Dashboard title cannot be empty.'
      return
    }
    if (title === builder.title || !builder.capabilities.canEdit || this.commandPending) return
    this.visualActionMessage = 'Saving dashboard title.'
    this.emitCommand('update_dashboard_metadata', { title, description: builder.description ?? '' })
  }

  private updateDashboardDescription = (event: Event): void => {
    const builder = this.builder
    const description = (event.currentTarget as HTMLTextAreaElement).value.trim()
    if (!builder || description === (builder.description ?? '') || !builder.capabilities.canEdit || this.commandPending) return
    this.visualActionMessage = 'Saving dashboard description.'
    this.emitCommand('update_dashboard_metadata', { title: builder.title, description })
  }

  private publish = (): void => {
    const builder = this.builder
    if (!builder?.capabilities.canPublish || !builder.hasUnpublishedChanges || builder.diagnostics.some((item) => item.severity === 'error') || this.previewValidationMessage(builder) || this.commandPending) return
    this.emitCommand('publish')
  }

  private archiveDashboard = (): void => {
    if (!this.builder?.capabilities.canArchive || this.commandPending) return
    this.emitCommand('archive', {}, false)
  }

  private canDeleteDashboard(builder: DashboardBuilderSignal): boolean {
    return builder.capabilities.canArchive && builder.lifecycle === 'draft' && builder.visibility === 'private'
  }

  private deleteDashboard = (): void => {
    const builder = this.builder
    if (!builder || !this.canDeleteDashboard(builder) || this.commandPending) return
    if (!window.confirm(`Delete ${builder.title}? This cannot be undone.`)) return
    this.emitCommand('delete', {}, false)
  }

  private addPage = (): void => {
    const builder = this.builder
    if (!builder?.capabilities.canAddPage) return
    this.pendingAddPage = {
      revision: this.revisionKey(builder),
      pageIDs: new Set(builder.pages.map((page) => page.id)),
    }
    this.emitCommand('add_page', { pageId: '', title: '' })
  }

  private updatePageTitle(page: DashboardBuilderPageSignal, event: Event): void {
    const input = event.currentTarget as HTMLInputElement
    const title = input.value.trim()
    if (!title) {
      input.value = page.title
      this.visualActionMessage = 'Page name cannot be empty.'
      return
    }
    if (title === page.title || !this.builder?.capabilities.canEdit || this.commandPending) return
    this.visualActionMessage = `Renaming ${page.title}.`
    this.emitCommand('rename_page', { pageId: page.id, title })
  }

  private updatePageDescription(page: DashboardBuilderPageSignal, event: Event): void {
    const description = (event.currentTarget as HTMLTextAreaElement).value.trim()
    if (description === (page.description ?? '') || !this.builder?.capabilities.canEdit || this.commandPending) return
    this.visualActionMessage = `Saving ${page.title} description.`
    this.emitCommand('update_page_metadata', { pageId: page.id, title: page.title, description })
  }

  private updateHeaderTitle(page: DashboardBuilderPageSignal, header: DashboardBuilderHeaderSignal, event: Event): void {
    const title = (event.currentTarget as HTMLInputElement).value.trim()
    if (!title) {
      (event.currentTarget as HTMLInputElement).value = header.title
      this.visualActionMessage = 'Header title cannot be empty.'
      return
    }
    if (title === header.title || !this.builder?.capabilities.canEdit || this.commandPending) return
    this.visualActionMessage = `Saving ${header.title}.`
    this.emitCommand('update_header_metadata', { pageId: page.id, headerId: header.id, title, description: header.description ?? '' })
  }

  private updateHeaderDescription(page: DashboardBuilderPageSignal, header: DashboardBuilderHeaderSignal, event: Event): void {
    const description = (event.currentTarget as HTMLTextAreaElement).value.trim()
    if (description === (header.description ?? '') || !this.builder?.capabilities.canEdit || this.commandPending) return
    this.visualActionMessage = `Saving ${header.title} description.`
    this.emitCommand('update_header_metadata', { pageId: page.id, headerId: header.id, title: header.title, description })
  }

  private updatePageLayout(page: DashboardBuilderPageSignal, key: keyof DashboardBuilderPageSignal['grid'], event: Event, minimum: number): void {
    const input = event.currentTarget as HTMLInputElement
    const parsed = Number(input.value)
    if (!Number.isInteger(parsed) || parsed < minimum) {
      input.value = String(page.grid[key])
      this.visualActionMessage = `${input.getAttribute('aria-label') ?? 'Grid value'} must be ${minimum} or greater.`
      return
    }
    if (parsed === page.grid[key] || !this.builder?.capabilities.canEdit || this.commandPending) return
    const grid = { ...page.grid, [key]: parsed }
    this.visualActionMessage = `Saving ${page.title} grid settings.`
    this.emitCommand('update_page_layout', {
      pageId: page.id,
      columns: grid.columns,
      rowHeight: grid.rowHeight,
      gap: grid.gap,
      padding: grid.padding,
    })
  }

  private duplicatePage(event: Event, page: DashboardBuilderPageSignal): void {
    this.closePageActions(event)
    const builder = this.builder
    if (!builder?.capabilities.canEdit || this.commandPending) return
    this.pendingAddPage = { revision: this.revisionKey(builder), pageIDs: new Set(builder.pages.map((item) => item.id)) }
    this.visualActionMessage = `Duplicating ${page.title}.`
    this.emitCommand('duplicate_page', { pageId: page.id, newPageId: '', title: `${page.title} copy` })
  }

  private removePage(event: Event, page: DashboardBuilderPageSignal): void {
    this.closePageActions(event)
    const builder = this.builder
    if (!builder?.capabilities.canEdit || this.commandPending || builder.pages.length <= 1) return
    const index = builder.pages.findIndex((item) => item.id === page.id)
    const fallback = builder.pages[index + 1] ?? builder.pages[index - 1] ?? builder.pages[0]
    this.pendingRemovePage = {
      revision: this.revisionKey(builder),
      pageID: page.id,
      visualID: this.localVisualID,
    }
    if (fallback?.id && fallback.id !== page.id) {
      this.localPageID = fallback.id
      this.localVisualID = ''
      this.selectedFilterID = ''
      this.selectedFilterComponentID = ''
    }
    this.visualActionMessage = `Deleting ${page.title}. Use Undo to restore it.`
    this.emitCommand('remove_page', { pageId: page.id })
  }

  private movePageFromMenu(event: Event, page: DashboardBuilderPageSignal, index: number): void {
    this.closePageActions(event)
    this.movePage(page.id, index)
  }

  private movePage(pageID: string, index: number): void {
    const builder = this.builder
    const current = builder?.pages.findIndex((page) => page.id === pageID) ?? -1
    if (!builder?.capabilities.canEdit || this.commandPending || current < 0 || index < 0 || index >= builder.pages.length || current === index) return
    this.visualActionMessage = `Moving ${builder.pages[current].title}.`
    this.emitCommand('move_page', { pageId: pageID, index })
  }

  private closePageActions(event: Event): void {
    const details = (event.currentTarget as HTMLElement | null)?.closest('details') as HTMLDetailsElement | null
    if (details) details.open = false
  }

  private startPageDrag(event: DragEvent, pageID: string): void {
    if (!this.builder?.capabilities.canEdit || this.commandPending) {
      event.preventDefault()
      return
    }
    this.draggedPageID = pageID
    this.pageDropTargetID = ''
    event.dataTransfer?.setData('text/x-leapview-page', pageID)
    if (event.dataTransfer) event.dataTransfer.effectAllowed = 'move'
  }

  private dragPageOver(event: DragEvent, pageID: string): void {
    if (!this.draggedPageID || this.draggedPageID === pageID || this.commandPending) return
    event.preventDefault()
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
    this.pageDropTargetID = pageID
  }

  private leavePageDrop(pageID: string): void {
    if (this.pageDropTargetID === pageID) this.pageDropTargetID = ''
  }

  private dropPage(event: DragEvent, targetPageID: string): void {
    event.preventDefault()
    const builder = this.builder
    const sourcePageID = this.draggedPageID || event.dataTransfer?.getData('text/x-leapview-page') || ''
    const sourceIndex = builder?.pages.findIndex((page) => page.id === sourcePageID) ?? -1
    const targetIndex = builder?.pages.findIndex((page) => page.id === targetPageID) ?? -1
    if (!builder || sourceIndex < 0 || targetIndex < 0 || sourceIndex === targetIndex) {
      this.endPageDrag()
      return
    }
    const target = event.currentTarget as HTMLElement
    const after = event.clientX > target.getBoundingClientRect().left + target.getBoundingClientRect().width / 2
    let finalIndex = targetIndex + (after ? 1 : 0)
    if (sourceIndex < finalIndex) finalIndex--
    this.endPageDrag()
    this.movePage(sourcePageID, finalIndex)
  }

  private endPageDrag = (): void => {
    this.draggedPageID = ''
    this.pageDropTargetID = ''
  }

  private handlePageTabKeydown(event: KeyboardEvent, pageID: string): void {
    const builder = this.builder
    const index = builder?.pages.findIndex((page) => page.id === pageID) ?? -1
    if (!builder || index < 0) return
    if (event.altKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight')) {
      event.preventDefault()
      this.movePage(pageID, index + (event.key === 'ArrowLeft' ? -1 : 1))
      return
    }
    if (this.pageBaseHref || !['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return
    event.preventDefault()
    const nextIndex = event.key === 'Home' ? 0 : event.key === 'End' ? builder.pages.length - 1 : index + (event.key === 'ArrowLeft' ? -1 : 1)
    const next = builder.pages[Math.max(0, Math.min(builder.pages.length - 1, nextIndex))]
    if (!next || next.id === pageID) return
    this.selectPage(next.id)
    void this.updateComplete.then(() => this.renderRoot.querySelector<HTMLElement>(`.page-tab[data-page-id="${CSS.escape(next.id)}"]`)?.focus())
  }

  private addVisual(type: BuilderVisualType = this.visualType): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canAddVisual || !page || this.commandPending) return
    const measure = type === 'gauge' ? this.defaultGaugeMeasure(builder) : undefined
    this.pendingAddVisual = {
      revision: this.revisionKey(builder),
      visualIDs: new Set(page.visuals.map((visual) => visual.id)),
      pageID: page.id,
    }
    this.visualType = type
    this.visualActionMessage = `Adding a ${this.visualLabel(type, builder)} visual${measure ? ` for ${measure.label}` : ''}.`
    this.emitCommand('add_visual', {
      pageId: page.id, visualId: '', componentId: '', type, title: measure?.label ?? this.visualLabel(type, builder),
      ...(measure ? { fieldId: measure.id, role: 'metric' } : {}),
    })
  }

  private defaultGaugeMeasure(builder: DashboardBuilderSignal): DashboardBuilderFieldSignal | undefined {
    return this.renderCache.semanticCatalog(builder.semanticModel.datasets ?? []).find((item) =>
      item.group === 'metric' && this.fieldCompatibleWithRole(item.field, 'metric'))?.field
  }

  private copySelectedVisual(): boolean {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const visual = builder && page ? this.selectedVisual(page, builder) : undefined
    if (!builder?.capabilities.canEdit || !page || !visual || this.commandPending) return false
    this.copiedVisual = { pageId: page.id, visualId: visual.id }
    this.visualActionMessage = `Copied ${visual.title}.`
    return true
  }

  private pasteCopiedVisual(): boolean {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const copied = this.copiedVisual
    const visual = copied && page?.id === copied.pageId ? page.visuals.find((item) => item.id === copied.visualId) : undefined
    if (!builder?.capabilities.canEdit || !page || !visual || !copied || this.commandPending) return false
    this.pendingAddVisual = { revision: this.revisionKey(builder), visualIDs: new Set(page.visuals.map((item) => item.id)), pageID: page.id }
    this.visualActionMessage = `Pasting ${visual.title}.`
    this.emitCommand('duplicate_visual', { pageId: page.id, visualId: visual.id })
    return true
  }

  private deleteSelectedVisual(): boolean {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const visual = builder && page ? this.selectedVisual(page, builder) : undefined
    if (!builder?.capabilities.canEdit || !page || !visual || this.commandPending) return false
    this.visualActionMessage = `Deleting ${visual.title}.`
    this.emitCommand('remove_visual', { pageId: page.id, visualId: visual.id })
    return true
  }

  private deleteSelectedFilterComponent(): boolean {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const component = page?.filterComponents?.find((item) => item.id === this.selectedFilterComponentID)
    if (!builder?.capabilities.canEdit || !page || !component || this.commandPending) return false
    this.removeFilterComponent(page, component)
    return true
  }

  private selectVisualType(type: BuilderVisualType, visual: DashboardBuilderVisualSignal | undefined): void {
    this.addingSlicer = false
    this.visualType = type
    if (!visual) {
      this.selectedFilterID = ''
      this.selectedFilterComponentID = ''
      this.editingPage = false
      this.addVisual(type)
      return
    }
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || this.commandPending) return
    const currentType = this.visualTypeForRender(visual)
    const currentRevision = this.currentRevisionReference()
    const switchBack = this.reversibleVisualTypeSwitch
    const undoTarget = this.undoStack.at(-1)
    if (currentType !== type && currentRevision && switchBack?.toRevision
      && switchBack.pageID === page.id && switchBack.visualID === visual.id
      && switchBack.fromType === type && switchBack.toType === currentType
      && this.sameRevisionReference(switchBack.toRevision, currentRevision)
      && undoTarget && this.sameRevisionReference(switchBack.fromRevision, undoTarget)) {
      this.visualTypeOverrides = { ...this.visualTypeOverrides, [visual.id]: type }
      this.pendingVisualTypeSwitch = null
      this.reversibleVisualTypeSwitch = null
      this.undo()
      return
    }
    const acceptedRoles = new Set(this.visualCatalogEntry(type, builder)?.roles ?? [])
    const legacyQueryMismatch = visual.slots.some((slot) => !acceptedRoles.has(this.slotRole(slot)))
      || (visual.previewError ?? '').toLowerCase().includes('incompatible with')
    if (currentType === type && !legacyQueryMismatch) {
      this.visualActionMessage = `${visual.title} is already a ${this.visualLabel(type, builder)} visual.`
      return
    }
    this.visualActionMessage = currentType === type
      ? `Repairing ${visual.title} as a ${this.visualLabel(type, builder)} visual.`
      : `Changing ${visual.title} to a ${this.visualLabel(type, builder)} visual.`
    if (currentType !== type) {
      this.visualTypeOverrides = { ...this.visualTypeOverrides, [visual.id]: type }
      if (currentRevision && !visual.previewError) {
        this.pendingVisualTypeSwitch = { pageID: page.id, visualID: visual.id, fromType: currentType, toType: type, fromRevision: currentRevision }
      }
      this.reversibleVisualTypeSwitch = null
    }
    this.emitCommand('set_visual_type', { pageId: page.id, visualId: visual.id, type })
  }

  private readonly startAddingSlicer = (): void => {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || this.commandPending) return
    const selectedVisualID = this.effectiveVisualID(builder, page)
    this.addingSlicer = true
    this.localVisualID = ''
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.editingPage = false
    this.fieldFilter = 'all'
    this.visualActionMessage = 'Choose a dimension for the slicer.'
    if (selectedVisualID) this.emit('lv-builder-visual-select', { ...this.commandDetail(), visualId: '' })
  }

  private visualTypeForRender(visual: DashboardBuilderVisualSignal): string {
    return this.visualTypeOverrides[visual.id] ?? visual.type.toLowerCase()
  }

  private reconcileVisualTypeOverrides(builder: DashboardBuilderSignal | null): void {
    if (!builder || Object.keys(this.visualTypeOverrides).length === 0) return
    const visuals = new Map(builder.pages.flatMap((page) => page.visuals).map((visual) => [visual.id, visual]))
    const next = Object.fromEntries(Object.entries(this.visualTypeOverrides).filter(([visualID, type]) => {
      const visual = visuals.get(visualID)
      return visual !== undefined && visual.type.toLowerCase() !== type
    })) as Record<string, BuilderVisualType>
    if (Object.keys(next).length !== Object.keys(this.visualTypeOverrides).length) this.visualTypeOverrides = next
  }

  private reconcileVisualTypeSwitch(builder: DashboardBuilderSignal | null): void {
    const pending = this.pendingVisualTypeSwitch
    if (!builder || !pending) return
    const current = this.currentRevisionReference()
    if (!current || this.sameRevisionReference(current, pending.fromRevision)) return
    const page = builder.pages.find((candidate) => candidate.id === pending.pageID)
    const visual = page?.visuals.find((candidate) => candidate.id === pending.visualID)
    if (!visual || visual.type.toLowerCase() !== pending.toType) {
      this.pendingVisualTypeSwitch = null
      return
    }
    this.reversibleVisualTypeSwitch = { ...pending, toRevision: current }
    this.pendingVisualTypeSwitch = null
  }

  private sameRevisionReference(left: BuilderRevisionReference, right: BuilderRevisionReference): boolean {
    return left.id === right.id && left.number === right.number && left.contentHash === right.contentHash
  }

  private reconcileInteractionEffectOverrides(builder: DashboardBuilderSignal | null): void {
    if (!builder || Object.keys(this.interactionEffectOverrides).length === 0) return
    if (this.interactionOverridesRevision && this.interactionOverridesRevision === this.revisionKey(builder)) return
    this.interactionEffectOverrides = {}
    this.interactionOverridesRevision = ''
  }

  private addField(field: DashboardBuilderFieldSignal): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const visual = page && builder ? this.selectedVisual(page, builder) : undefined
    if (!builder?.capabilities.canEdit || !page) return
    if (this.addingSlicer) {
      this.createSlicerFromField(field)
      return
    }
    if (!visual) {
      this.createVisualFromField(field)
      return
    }
    const usedIn = this.fieldUsedIn(field, visual)
    if (usedIn) {
      this.gridInteractionMessage = `${field.label} is already used in ${usedIn}.`
      return
    }
    if (!this.fieldCompatibleWithVisual(field, visual)) return
    const role = this.roleForField(field, visual)
    this.gridInteractionMessage = `Adding ${field.label} to ${this.fieldWellLabel(visual, role)}.`
    this.emitCommand('assign_field', { pageId: page.id, visualId: visual.id, fieldId: field.id, role })
  }

  private dropField = (event: DragEvent): void => {
    event.preventDefault()
    const builder = this.builder
    if (!builder?.capabilities.canEdit) return
    const field = this.draggedField(event, builder)
    this.clearDraggedField()
    if (!field) return
    if (this.addingSlicer) this.createSlicerFromField(field)
    else this.createVisualFromField(field)
  }

  private readonly dropFieldOnSlicer = (event: DragEvent): void => {
    event.preventDefault()
    event.stopPropagation()
    const builder = this.builder
    if (!builder?.capabilities.canEdit) return
    const field = this.draggedField(event, builder)
    this.clearDraggedField()
    if (field) this.createSlicerFromField(field)
  }

  private dropFieldOnRole(event: DragEvent, role: BuilderFieldRole): void {
    event.preventDefault()
    event.stopPropagation()
    const builder = this.builder
    if (!builder?.capabilities.canEdit) return
    const page = this.selectedPage(builder)
    const visual = page ? this.selectedVisual(page, builder) : undefined
    const field = this.draggedField(event, builder)
    this.clearDraggedField()
    if (!page || !visual || !field || !this.fieldCompatibleWithRole(field, role) || !this.roleHasCapacity(visual, role)) return
    this.emitCommand('assign_field', { pageId: page.id, visualId: visual.id, fieldId: field.id, role })
  }

  private dropFieldOnVisual(event: DragEvent, visualID: string): void {
    event.preventDefault()
    event.stopPropagation()
    const builder = this.builder
    if (!builder?.capabilities.canEdit) return
    const page = this.selectedPage(builder)
    const visual = page?.visuals.find((item) => item.id === visualID)
    const field = this.draggedField(event, builder)
    this.clearDraggedField()
    if (!page || !visual || !field || !this.fieldCompatibleWithVisual(field, visual)) return
    this.addingSlicer = false
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.editingPage = false
    this.localVisualID = visual.id
    this.emitCommand('assign_field', { pageId: page.id, visualId: visual.id, fieldId: field.id, role: this.roleForField(field, visual) })
  }

  private readonly allowFieldDrop = (event: DragEvent): void => {
    event.preventDefault()
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'copy'
  }

  private draggedField(event: DragEvent, builder: DashboardBuilderSignal): DashboardBuilderFieldSignal | undefined {
    const fieldID = event.dataTransfer?.getData('text/leapview-field') || event.dataTransfer?.getData('text/plain')
    if (!fieldID) return undefined
    return (builder.semanticModel.datasets ?? []).flatMap((dataset) => dataset.fields).find((item) => item.id === fieldID)
  }

  private dragField(event: DragEvent, field: DashboardBuilderFieldSignal): void {
    if (!this.builder?.capabilities.canEdit) return
    this.draggedFieldID = field.id
    if (event.dataTransfer) event.dataTransfer.effectAllowed = 'copy'
    event.dataTransfer?.setData('text/leapview-field', field.id)
    event.dataTransfer?.setData('text/plain', field.id)
  }

  private clearDraggedField = (): void => {
    this.draggedFieldID = ''
  }

  private get builderFilterOptionsReady(): boolean {
    const runtime = this.signal<RouteRuntimeSignal>('runtime', { kind: 'dashboard_builder' })
    return Boolean(runtime.servingStateId) && this.builderFilterState.revision > 0
  }

  private builderFilterOptionContext(binding: DashboardCompiledFilterBinding, pageID: string): string {
    const bindings = Object.values(this.builderFilterContract.bindings)
    const dependencies = binding.optionDependencies.flatMap((reference) => {
      const dependency = bindings.find((candidate) => candidate.scope === reference.scope
        && candidate.id === reference.id
        && (candidate.scope === 'report' || candidate.pageID === pageID))
      if (!dependency) return []
      const applied = this.builderFilterState.appliedControls[dependency.key]
      if (!applied) return []
      const expression = applied.resolvedExpression?.kind ? applied.resolvedExpression : applied.expression
      return expression.kind === 'unfiltered' ? [] : [[dependency.key, expression] as const]
    }).sort(([left], [right]) => left.localeCompare(right))
    return JSON.stringify({ pageID, revision: this.builderFilterState.revision, dependencies })
  }

  private builderFilterOptionRequestSignature(context: string, detail: FilterOptionsNeededDetail): string {
    return `${context}\u0000${detail.search}\u0000${detail.cursor ?? ''}`
  }

  private builderFilterResetBindingKeys(scope: 'page' | 'dashboard', pageID?: string): string[] {
    return Object.values(this.builderFilterContract.bindings)
      .filter((binding) => binding.readerEditable && (
        scope === 'dashboard'
        || (binding.scope === 'page' && binding.pageID === pageID)
      ))
      .map((binding) => binding.key)
      .sort()
  }

  private resetBuilderFilters(scope: 'page' | 'dashboard', bindingKeys: string[]): void {
    if (bindingKeys.length === 0 || this.builderFilterController.pending || this.status.loading) return
    this.builderFilterController.reset(scope, bindingKeys)
    this.requestUpdate()
  }

  private readonly handleBuilderFilterApply = (event: Event): void => {
    event.stopPropagation()
    if (this.builderFilterContract.applicationMode !== 'deferred' || this.builderFilterController.pending || this.status.loading) return
    const projected = this.builderFilterController.projected.revision > 0
      ? this.builderFilterController.projected
      : this.builderFilterState
    if (projected.dirtyBindings.length === 0) return
    this.builderFilterController.apply()
    this.requestUpdate()
  }

  private readonly handleBuilderFilterCancel = (event: Event): void => {
    event.stopPropagation()
    if (this.builderFilterContract.applicationMode !== 'deferred' || this.builderFilterController.pending || this.status.loading) return
    const projected = this.builderFilterController.projected.revision > 0
      ? this.builderFilterController.projected
      : this.builderFilterState
    if (projected.dirtyBindings.length === 0) return
    this.builderFilterController.cancel()
    this.requestUpdate()
  }

  private handleBuilderFilterMutation = (event: CustomEvent<FilterMutationDetail>): void => {
    const detail = event.detail
    if (!detail?.bindingKey || !detail.expression) return
    const binding = this.builderFilterContract.bindings[detail.bindingKey]
    if (!binding?.readerEditable) return
    event.stopPropagation()
    if (detail.expression.kind === 'unfiltered') this.builderFilterController.clear(detail.bindingKey)
    else this.builderFilterController.mutate(detail.bindingKey, detail.expression)
    this.requestUpdate()
  }

  private handleBuilderFilterClear = (event: CustomEvent<{ bindingKey: string }>): void => {
    event.stopPropagation()
    const binding = this.builderFilterContract.bindings[event.detail?.bindingKey]
    if (!binding?.readerEditable) return
    this.builderFilterController.clear(binding.key)
    this.requestUpdate()
  }

  private handleBuilderFilterResetBinding = (event: CustomEvent<{ bindingKey: string }>): void => {
    event.stopPropagation()
    const binding = this.builderFilterContract.bindings[event.detail?.bindingKey]
    if (!binding?.readerEditable) return
    this.builderFilterController.resetBinding(binding.key)
    this.requestUpdate()
  }

  private handleBuilderFilterOptionsNeeded = (event: CustomEvent<FilterOptionsNeededDetail>): void => {
    const detail = event.detail
    if (!detail?.bindingKey || !this.builderFilterContract.bindings[detail.bindingKey]) return
    event.stopPropagation()
    const runtime = this.signal<RouteRuntimeSignal>('runtime', { kind: 'dashboard_builder' })
    this.resetBuilderFilterOptionCache(runtime.servingStateId ?? '')
    const binding = this.builderFilterContract.bindings[detail.bindingKey]
    const builder = this.builder
    const pageID = builder ? this.selectedPage(builder)?.id ?? '' : ''
    const context = this.builderFilterOptionContext(binding, pageID)
    const requestSignature = this.builderFilterOptionRequestSignature(context, detail)
    const inFlight = this.filterOptionInFlight.get(detail.bindingKey)
    if (inFlight?.context === requestSignature && Date.now() - inFlight.startedAt < 250) return
    const generation = (this.filterOptionGenerations.get(detail.bindingKey) ?? 0) + 1
    this.filterOptionGenerations.set(detail.bindingKey, generation)
    this.filterOptionInFlight.set(detail.bindingKey, { context: requestSignature, generation, startedAt: Date.now() })
    const contexts = this.filterOptionRequestContexts.get(detail.bindingKey) ?? new Map<number, string>()
    contexts.set(generation, context)
    for (const existingGeneration of contexts.keys()) {
      if (existingGeneration < generation - 4) contexts.delete(existingGeneration)
    }
    this.filterOptionRequestContexts.set(detail.bindingKey, contexts)
    this.builderFilterTransportError = ''
    this.dispatchEvent(new CustomEvent('lv-builder-filter-options-request', {
      bubbles: true,
      composed: true,
      detail: {
        ...detail,
        servingStateID: runtime.servingStateId ?? '',
        filterRevision: this.builderFilterState.revision,
        requestGeneration: generation,
      },
    }))
  }

  private reconcileBuilderFilterController(): void {
    const contract = this.builderFilterContract
    const state = this.builderFilterState
    this.builderFilterController.setApplicationMode(contract.applicationMode)
    this.builderFilterController.setDefaults(Object.fromEntries(Object.values(contract.bindings).map((binding) => [binding.key, binding.default])))
    const fingerprint = JSON.stringify(state)
    if (fingerprint !== this.builderFilterStateFingerprint) {
      this.builderFilterStateFingerprint = fingerprint
      this.builderFilterController.reconcile(state)
      this.builderFilterCommandInFlight = null
      this.requestUpdate()
    }
    const validation = this.builderFilterValidation
    if (!validation.accepted && validation.clientMutationID && validation.clientMutationID !== this.builderFilterValidationMutationID) {
      this.builderFilterValidationMutationID = validation.clientMutationID
      if (this.builderFilterController.reject(validation.clientMutationID, state)) {
        this.builderFilterCommandInFlight = null
        this.requestUpdate()
      }
    }
  }

  private selectedBuilderFilter(builder: DashboardBuilderSignal): DashboardBuilderFilterSignal | undefined {
    return (builder.filters ?? []).find((filter) => filter.id === this.selectedFilterID)
  }

  private compiledBindingForFilter(filter: DashboardBuilderFilterSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined): DashboardCompiledFilterBinding | undefined {
    const candidates = Object.values(this.builderFilterContract.bindings).filter((binding) => binding.filter === filter.id)
    const scope = this.filterScope(filter, page, visual)
    if (scope === 'report') return candidates.find((binding) => binding.scope === 'report')
    if (page) {
      const pageBinding = candidates.find((binding) => binding.scope === 'page' && binding.pageID === page.id)
      if (pageBinding) return pageBinding
    }
    return candidates.find((binding) => binding.scope === 'report') ?? candidates[0]
  }

  private filterScope(filter: DashboardBuilderFilterSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined): BuilderFilterScope {
    const pageBindings = (filter.bindings ?? []).filter((binding) => binding.scope === 'page')
    if (pageBindings.length > 0) {
      const binding = page && pageBindings.find((candidate) => candidate.pageId === page.id)
      if (!binding) return 'custom'
      const targets = [...new Set(binding.targets ?? [])].sort()
      if (targets.length === 0) return 'page'
      if (visual && targets.length === 1 && targets[0] === visual.id) return 'visual'
      return 'custom'
    }
    const targets = [...new Set(filter.targets ?? [])].sort()
    if (targets.length === 0) return 'report'
    return 'custom'
  }

  private groupFiltersByScope(filters: DashboardBuilderFilterSignal[], page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined): Record<BuilderFilterScope, DashboardBuilderFilterSignal[]> {
    const grouped: Record<BuilderFilterScope, DashboardBuilderFilterSignal[]> = { report: [], page: [], visual: [], custom: [] }
    for (const filter of filters) grouped[this.filterScope(filter, page, visual)].push(filter)
    return grouped
  }

  private selectFilterDefinition(filterID: string): void {
    this.addingSlicer = false
    this.selectedFilterID = filterID
    this.selectedFilterComponentID = ''
  }

  private readonly toggleAddFilterMenu = async (): Promise<void> => {
    this.addFilterMenuOpen = !this.addFilterMenuOpen
    this.addFilterQuery = ''
    await this.updateComplete
    if (this.addFilterMenuOpen) this.renderRoot.querySelector<HTMLInputElement>('.filter-add-search')?.focus({ preventScroll: true })
  }

  private chooseFilterField(field: DashboardBuilderFieldSignal): void {
    this.addFilterMenuOpen = false
    this.addFilterForField(field)
    this.renderRoot.querySelector<HTMLElement>('.filter-add-trigger')?.focus({ preventScroll: true })
  }

  private readonly handleAddFilterMenuKey = (event: KeyboardEvent): void => {
    const menu = event.currentTarget as HTMLElement
    const buttons = [...menu.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')]
    const index = buttons.indexOf(event.target as HTMLButtonElement)
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      this.addFilterMenuOpen = false
      this.renderRoot.querySelector<HTMLElement>('.filter-add-trigger')?.focus({ preventScroll: true })
    } else if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) && buttons.length > 0) {
      if (event.target instanceof HTMLInputElement && ['Home', 'End'].includes(event.key)) return
      event.preventDefault()
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : index < 0 ? (event.key === 'ArrowUp' ? buttons.length - 1 : 0) : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length
      buttons[next].focus()
    }
  }

  private readonly dropFieldOnFilters = (event: DragEvent): void => {
    event.preventDefault()
    event.stopPropagation()
    const builder = this.builder
    if (!builder?.capabilities.canEdit) return
    const field = this.draggedField(event, builder)
    this.clearDraggedField()
    if (field) this.addFilterForField(field)
  }

  private addFilterForField(field: DashboardBuilderFieldSignal): boolean {
    const builder = this.builder
    if (!builder?.capabilities.canEdit || this.commandPending || !this.fieldSupportsFilter(field)) return false
    const existing = (builder.filters ?? []).find((filter) => filter.dimension === field.id)
    if (existing) {
      this.selectFilterDefinition(existing.id)
      this.visualActionMessage = `${field.label} is already a report filter.`
      return false
    }
    if (!this.filterHasCompatibleVisual(field)) {
      this.builderFilterTransportError = `${field.label} does not apply to any dashboard visual. Choose a field from a chart’s dataset.`
      this.requestUpdate()
      return false
    }
    this.builderFilterTransportError = ''
    const controlType = this.recommendedFilterControl(field)
    this.pendingAddFilter = { revision: this.revisionKey(builder), filterIDs: new Set((builder.filters ?? []).map((filter) => filter.id)) }
    this.visualActionMessage = `Adding ${field.label} as a report filter.`
    this.emitCommand('add_filter', { fieldId: field.id, title: field.label, dataset: this.datasetForField(builder, field.id), controlType })
    return true
  }

  private createSlicerFromField(field: DashboardBuilderFieldSignal): boolean {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || this.commandPending || !this.fieldSupportsFilter(field)) return false
    const existing = (builder.filters ?? []).find((filter) => filter.dimension === field.id)
    if (existing) {
      const placed = page.filterComponents?.find((component) => component.filterId === existing.id)
      if (placed) {
        this.addingSlicer = false
        this.selectFilterComponent(placed)
        this.visualActionMessage = `${field.label} is already on this page.`
        return false
      }
      this.addFilterComponent(page, existing)
      return true
    }
    if (!this.filterHasCompatibleVisual(field)) {
      this.builderFilterTransportError = `${field.label} does not apply to any dashboard visual.`
      this.requestUpdate()
      return false
    }
    this.builderFilterTransportError = ''
    this.pendingAddSlicer = {
      revision: this.revisionKey(builder),
      filterIDs: new Set((builder.filters ?? []).map((filter) => filter.id)),
      componentIDs: new Set((page.filterComponents ?? []).map((component) => component.id)),
      pageID: page.id,
    }
    this.visualActionMessage = `Adding ${field.label} as a slicer on ${page.title}.`
    this.emitCommand('add_slicer', {
      pageId: page.id,
      filterId: '',
      componentId: '',
      fieldId: field.id,
      title: field.label,
      dataset: this.datasetForField(builder, field.id),
      controlType: this.recommendedFilterControl(field),
    })
    return true
  }

  private filterHasCompatibleVisual(field: DashboardBuilderFieldSignal): boolean {
    // Governed query resolution includes computed measures and multiple
    // datasets; use its projection instead of guessing from visible slots.
    return field.canFilter !== false
  }

  private fieldSupportsFilter(field: DashboardBuilderFieldSignal): boolean {
    return field.kind === 'dimension' && Boolean(field.roles?.includes('dimension'))
  }

  private updateFilter(filter: DashboardBuilderFilterSignal, patch: Partial<Pick<DashboardBuilderFilterSignal, 'label' | 'controlType' | 'required' | 'readerEditable' | 'urlParameter'>>): void {
    const builder = this.builder
    if (!builder?.capabilities.canEdit || this.commandPending) return
    const next = { ...filter, ...patch }
    if (Object.entries(patch).every(([key, value]) => (filter[key as keyof DashboardBuilderFilterSignal] ?? '') === value)) return
    this.visualActionMessage = `Saving ${next.label} filter settings.`
    this.emitCommand('update_filter', {
      filterId: next.id,
      title: next.label,
      description: next.description ?? '',
      dataset: this.datasetForField(builder, next.dimension),
      controlType: next.controlType,
      required: next.required,
      readerEditable: next.readerEditable,
      urlParameter: next.urlParameter ?? '',
    })
  }

  private setFilterScope(filter: DashboardBuilderFilterSignal, scope: 'report' | 'page', page?: DashboardBuilderPageSignal, targets: string[] = []): void {
    if (!this.builder?.capabilities.canEdit || this.commandPending) return
    this.visualActionMessage = scope === 'page'
      ? `Moving ${filter.label} to ${page?.title ?? 'this page'}.`
      : targets.length === 1
        ? `Applying ${filter.label} to the selected visual.`
        : `Moving ${filter.label} to all pages.`
    this.emitCommand('set_filter_scope', {
      filterId: filter.id,
      scope,
      pageId: scope === 'page' && page ? page.id : '',
      ...(targets.length > 0 ? { targets } : {}),
    })
  }

  private removeFilter(filter: DashboardBuilderFilterSignal): void {
    if (!this.builder?.capabilities.canEdit || this.commandPending) return
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.visualActionMessage = `Removing ${filter.label} filter.`
    this.emitCommand('remove_filter', { filterId: filter.id })
  }

  private addFilterComponent(page: DashboardBuilderPageSignal, filter: DashboardBuilderFilterSignal): void {
    const builder = this.builder
    if (!builder?.capabilities.canEdit || this.commandPending) return
    this.pendingAddFilterComponent = {
      revision: this.revisionKey(builder),
      componentIDs: new Set((page.filterComponents ?? []).map((component) => component.id)),
      pageID: page.id,
    }
    this.visualActionMessage = `Placing ${filter.label} as a slicer on ${page.title}.`
    this.emitCommand('add_filter_component', { pageId: page.id, filterId: filter.id, componentId: '' })
  }

  private removeFilterComponent(page: DashboardBuilderPageSignal, component: DashboardBuilderFilterComponentSignal): void {
    if (!this.builder?.capabilities.canEdit || this.commandPending) return
    this.selectedFilterComponentID = ''
    this.visualActionMessage = `Removing ${component.label} slicer from ${page.title}.`
    this.emitCommand('remove_filter_component', { pageId: page.id, componentId: component.id })
  }

  private datasetForField(builder: DashboardBuilderSignal, fieldID: string): string {
    return builder.semanticModel.datasets.find((dataset) => dataset.fields.some((field) => field.id === fieldID))?.id ?? builder.semanticModel.datasets[0]?.id ?? ''
  }

  private recommendedFilterControl(field: DashboardBuilderFieldSignal): BuilderFilterControl {
    const dataType = field.dataType.toLowerCase()
    if (dataType.includes('date') || dataType.includes('time')) return 'relativePeriod'
    if (dataType.includes('number') || dataType.includes('integer') || dataType.includes('decimal') || dataType.includes('float')) return 'numericRange'
    return 'multiSelect'
  }

  private filterControlChoices(filter: DashboardBuilderFilterSignal): Array<[BuilderFilterControl, string]> {
    const field = this.builder?.semanticModel.datasets.flatMap((dataset) => dataset.fields).find((candidate) => candidate.id === filter.dimension)
    return filterControlChoices(field?.dataType ?? '', filter.controlType)
  }

  private filterControlLabel(control: BuilderFilterControl): string {
    return filterControlLabel(control)
  }

  private draggedFieldFromBuilder(builder: DashboardBuilderSignal | null): DashboardBuilderFieldSignal | undefined {
    if (!builder || !this.draggedFieldID) return undefined
    return (builder.semanticModel.datasets ?? []).flatMap((dataset) => dataset.fields).find((field) => field.id === this.draggedFieldID)
  }

  private recommendedVisualForField(field: DashboardBuilderFieldSignal): BuilderVisualType {
    return field.kind === 'metric' ? 'kpi' : field.roles?.length && !field.roles.includes('detail') ? 'bar' : 'table'
  }

  private recommendedVisualForDraggedField(builder: DashboardBuilderSignal): BuilderVisualType {
    const field = this.draggedFieldFromBuilder(builder)
    return field ? this.recommendedVisualForField(field) : 'table'
  }

  private createVisualFromField(field: DashboardBuilderFieldSignal): boolean {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canAddVisual || !page || this.commandPending || !this.fieldDataTypeSupported(field)) return false
    const type = this.recommendedVisualForField(field)
    const role: BuilderFieldRole = field.kind === 'metric' ? 'metric' : type === 'table' ? 'detail' : 'dimension'
    this.pendingAddVisual = { revision: this.revisionKey(builder), visualIDs: new Set(page.visuals.map((visual) => visual.id)), pageID: page.id }
    this.visualType = type
    this.gridInteractionMessage = `Creating a ${this.visualLabel(type, builder)} visual for ${field.label}.`
    this.emitCommand('add_visual', { pageId: page.id, visualId: '', componentId: '', type, title: field.label, fieldId: field.id, role })
    return true
  }

  private selectPage(pageID: string): void {
    const builder = this.builder
    const currentPage = builder ? this.selectedPage(builder) : undefined
    if (currentPage?.id === pageID) {
      this.openPageSettings(currentPage)
      return
    }
    this.localPageID = pageID
    this.localVisualID = ''
    this.selectedHeaderID = ''
    this.addingSlicer = false
    this.editingPage = false
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.emit('lv-builder-page-select', { ...this.commandDetail(), pageId: pageID })
    // Embedded tabs retain the document, so load the newly selected page's
    // governed preview instead of keeping envelopes from the previous page.
    if (this.embeddedInChat) this.refreshBuilderSignals()
  }

  private openPageSettings(page: DashboardBuilderPageSignal, event?: Event): void {
    event?.preventDefault()
    event?.stopPropagation()
    const details = event?.currentTarget instanceof HTMLElement ? event.currentTarget.closest('details') : null
    if (details instanceof HTMLDetailsElement) details.open = false
    const builder = this.builder
    const selectedVisual = builder ? this.effectiveVisualID(builder, page) : ''
    this.localPageID = page.id
    this.localVisualID = ''
    this.selectedHeaderID = ''
    this.addingSlicer = false
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.editingPage = true
    if (this.collapsedPanes.visuals) {
      this.togglePane('visuals')
    }
    this.visualActionMessage = `Editing settings for ${page.title}.`
    if (selectedVisual) this.emit('lv-builder-visual-select', { ...this.commandDetail(), visualId: '' })
  }

  private pageHref(pageID: string): string {
    const separator = this.pageBaseHref.includes('?') ? '&' : '?'
    return `${this.pageBaseHref}${separator}page=${encodeURIComponent(pageID)}`
  }

  private selectVisual(visualID: string): void {
    this.addingSlicer = false
    this.editingPage = false
    this.localVisualID = visualID
    this.selectedHeaderID = ''
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const visual = page?.visuals.find((item) => item.id === visualID)
    const type = visual ? this.visualTypeForRender(visual) : ''
    if (this.visualCatalogEntry(type, builder)) this.visualType = type
    this.emit('lv-builder-visual-select', { ...this.commandDetail(), visualId: visualID })
  }

  private selectVisualFromPointer(visualID: string): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (page && this.effectiveVisualID(builder, page) === visualID) return
    this.selectVisual(visualID)
  }

  private readonly deselectVisualFromCanvas = (event: MouseEvent): void => {
    const target = event.target
    if (target instanceof Element && target.closest('.visual, .filter-component, .header-component, .builder-placeholder')) return
    this.clearCanvasSelection()
  }

  private clearCanvasSelection(): boolean {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!page || (!this.effectiveVisualID(builder, page) && !this.selectedHeaderID && !this.selectedFilterComponentID && !this.selectedFilterID && !this.addingSlicer)) return false
    this.localVisualID = ''
    this.addingSlicer = false
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.selectedHeaderID = ''
    this.editingPage = false
    this.visualActionMessage = 'Visual selection cleared.'
    this.emit('lv-builder-visual-select', { ...this.commandDetail(), visualId: '' })
    return true
  }

  private selectVisualOnKey(event: KeyboardEvent, visualID: string): void {
    // The tile remains a focusable authoring container while its chart body is
    // fully interactive. Do not steal keyboard events from visual controls.
    const target = event.target as HTMLElement | null
    if (target?.closest('button, input, select, textarea, a, [contenteditable="true"]')) return

    if (event.altKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight' || event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
      event.preventDefault()
      this.adjustVisualWithKeyboard(visualID, event.key, event.shiftKey)
      return
    }
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    this.selectVisual(visualID)
  }

  private adjustVisualWithKeyboard(visualID: string, key: string, resize: boolean): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const visual = page?.visuals.find((item) => item.id === visualID)
    if (!builder?.capabilities.canEdit || !page || !visual || this.commandPending || this.isMobileViewport()) return
    this.adjustGridComponentWithKeyboard(visualID, visual.placement, visual.title, key, resize, page)
  }

  private selectFilterComponent(component: DashboardBuilderFilterComponentSignal): void {
    this.addingSlicer = false
    this.editingPage = false
    this.selectedFilterComponentID = component.id
    this.selectedFilterID = component.filterId
    this.localVisualID = ''
    this.selectedHeaderID = ''
  }

  private selectHeader(header: DashboardBuilderHeaderSignal): void {
    this.addingSlicer = false
    this.editingPage = false
    this.selectedHeaderID = header.id
    this.selectedFilterComponentID = ''
    this.selectedFilterID = ''
    this.localVisualID = ''
  }

  private selectHeaderOnKey(event: KeyboardEvent, header: DashboardBuilderHeaderSignal): void {
    if (event.altKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight' || event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
      event.preventDefault()
      const page = this.builder ? this.selectedPage(this.builder) : undefined
      if (page) this.adjustGridComponentWithKeyboard(header.id, header.placement, header.title, event.key, event.shiftKey, page)
      return
    }
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    this.selectHeader(header)
  }

  private selectFilterComponentOnKey(event: KeyboardEvent, component: DashboardBuilderFilterComponentSignal): void {
    if (event.altKey && (event.key === 'ArrowLeft' || event.key === 'ArrowRight' || event.key === 'ArrowUp' || event.key === 'ArrowDown')) {
      event.preventDefault()
      const builder = this.builder
      const page = builder ? this.selectedPage(builder) : undefined
      if (page) this.adjustGridComponentWithKeyboard(component.id, component.placement, component.label, event.key, event.shiftKey, page)
      return
    }
    if (event.key !== 'Enter' && event.key !== ' ') return
    event.preventDefault()
    this.selectFilterComponent(component)
  }

  private adjustGridComponentWithKeyboard(componentID: string, placement: DashboardBuilderVisualSignal['placement'], label: string, key: string, resize: boolean, page: DashboardBuilderPageSignal): void {
    const builder = this.builder
    if (!builder?.capabilities.canEdit || this.commandPending || this.isMobileViewport()) return
    const columns = Math.max(1, page.grid.columns || 12)
    const node = this.gridStack?.getGridItems().find((item) => (item.gridstackNode?.id || item.getAttribute('gs-id')) === componentID)?.gridstackNode
    const current = {
      col: Math.max(1, Math.round((node?.x ?? placement.col - 1) + 1)),
      row: Math.max(1, Math.round((node?.y ?? placement.row - 1) + 1)),
      colSpan: Math.max(1, Math.round(node?.w ?? placement.colSpan)),
      rowSpan: Math.max(1, Math.round(node?.h ?? placement.rowSpan)),
    }
    const next = { ...current }
    if (resize) {
      if (key === 'ArrowLeft') next.colSpan = Math.max(1, current.colSpan - 1)
      if (key === 'ArrowRight') next.colSpan = Math.min(columns - current.col + 1, current.colSpan + 1)
      if (key === 'ArrowUp') next.rowSpan = Math.max(1, current.rowSpan - 1)
      if (key === 'ArrowDown') next.rowSpan = current.rowSpan + 1
    } else {
      if (key === 'ArrowLeft') next.col = Math.max(1, current.col - 1)
      if (key === 'ArrowRight') next.col = Math.min(columns - current.colSpan + 1, current.col + 1)
      if (key === 'ArrowUp') next.row = Math.max(1, current.row - 1)
      if (key === 'ArrowDown') next.row = current.row + 1
    }
    if (next.col === current.col && next.row === current.row && next.colSpan === current.colSpan && next.rowSpan === current.rowSpan) return

    const element = this.gridStack?.getGridItems().find((item) => (item.gridstackNode?.id || item.getAttribute('gs-id')) === componentID)
    if (this.gridStack && element) {
      this.gridStack.update(element, { x: next.col - 1, y: next.row - 1, w: next.colSpan, h: next.rowSpan })
    }
    const direction = key.replace('Arrow', '').toLowerCase()
    this.gridInteractionMessage = resize ? `${label} resized ${direction}.` : `${label} moved ${direction}.`
    // GridStack emits change synchronously for update(), and the bounded
    // microtask commits one atomic set_placements payload after the event.
    this.scheduleGridCommit()
  }

  private visualSignalID(visual: DashboardBuilderVisualSignal): string {
    return (visual as DashboardBuilderVisualWithPreview).visualId || visual.id
  }

  private commandDetail(): Record<string, string> {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    return {
      dashboardId: builder?.dashboardId ?? '',
      semanticModelId: builder?.semanticModel?.id ?? '',
      draftId: builder?.draftId ?? '',
      revisionId: builder?.revision.id ?? '',
      revisionNumber: String(builder?.revision.number ?? 0),
      revisionContentHash: builder?.revision.contentHash ?? '',
      pageId: page?.id ?? '',
      visualId: builder && page ? this.effectiveVisualID(builder, page) : '',
    }
  }

  private emit(name: string, detail: Record<string, unknown>): void {
    this.dispatchEvent(new CustomEvent(name, { bubbles: true, composed: true, detail }))
  }

  private emitCommand(action: string, detail: Record<string, unknown> = {}, recordHistory = true): void {
    if (this.commandPending) return
    if (action !== 'set_visual_type') {
      this.pendingVisualTypeSwitch = null
      this.reversibleVisualTypeSwitch = null
    }
    if (recordHistory && action !== 'publish' && action !== 'set_visibility') {
      const current = this.currentRevisionReference()
      if (current) {
        this.pendingHistorySnapshot = { undo: [...this.undoStack], redo: [...this.redoStack] }
        this.undoStack = [...this.undoStack.slice(-99), current]
        this.redoStack = []
      }
    }
    this.commandPending = true
    this.activeCommandAction = action
    this.activeCommandRevisionKey = this.builder ? this.revisionKey(this.builder) : ''
    this.setGridEditingEnabled(false)
    this.terminalFailure = null
    this.requestUpdate()
    this.emit('lv-builder-command', { ...this.commandDetail(), action, ...detail })
  }

  private currentRevisionReference(): BuilderRevisionReference | null {
    const revision = this.builder?.revision
    if (!revision?.id || !revision.number || !revision.contentHash) return null
    return { id: revision.id, number: revision.number, contentHash: revision.contentHash }
  }

  private undo = (): void => {
    const current = this.currentRevisionReference()
    const target = this.undoStack.at(-1)
    if (!current || !target || !this.builder?.capabilities.canEdit || this.commandPending) return
    this.pendingHistorySnapshot = { undo: [...this.undoStack], redo: [...this.redoStack] }
    this.undoStack = this.undoStack.slice(0, -1)
    this.redoStack = [...this.redoStack.slice(-99), current]
    this.visualActionMessage = 'Undoing the last change.'
    this.emitCommand('restore_revision', {
      targetRevisionId: target.id,
      targetRevisionNumber: String(target.number),
      targetRevisionContentHash: target.contentHash,
    }, false)
  }

  private redo = (): void => {
    const current = this.currentRevisionReference()
    const target = this.redoStack.at(-1)
    if (!current || !target || !this.builder?.capabilities.canEdit || this.commandPending) return
    this.pendingHistorySnapshot = { undo: [...this.undoStack], redo: [...this.redoStack] }
    this.redoStack = this.redoStack.slice(0, -1)
    this.undoStack = [...this.undoStack.slice(-99), current]
    this.visualActionMessage = 'Redoing the last change.'
    this.emitCommand('restore_revision', {
      targetRevisionId: target.id,
      targetRevisionNumber: String(target.number),
      targetRevisionContentHash: target.contentHash,
    }, false)
  }

  private readonly handleToolbarPointerDown = (event: PointerEvent): void => {
    this.closeToolbarPopovers(event.composedPath())
  }

  private closeToolbarPopovers(inside: EventTarget[] = [], restoreFocus = false): boolean {
    let closed = false
    let trigger: HTMLElement | null = null
    for (const details of this.renderRoot.querySelectorAll<HTMLDetailsElement>('.dashboard-metadata[open], .more-actions[open]')) {
      if (inside.includes(details)) continue
      details.open = false
      trigger = details.querySelector('summary')
      closed = true
    }
    const appearance = this.renderRoot.querySelector('.appearance-control')
    if (this.appearanceOpen && (!appearance || !inside.includes(appearance))) {
      this.appearanceOpen = false
      trigger = this.renderRoot.querySelector('.appearance-trigger')
      closed = true
    }
    if (restoreFocus) trigger?.focus()
    return closed
  }

  private readonly handleBuilderKeydown = (event: KeyboardEvent): void => {
    if (!event.defaultPrevented && event.key === 'Escape' && this.closeToolbarPopovers([], true)) {
      event.preventDefault()
      return
    }
    if (event.defaultPrevented || event.altKey || this.keyboardEventUsesEditableTarget(event)) return
    const modifier = event.metaKey || event.ctrlKey
    const key = event.key.toLowerCase()
    let handled = false
    if (!modifier && !event.shiftKey && event.key === 'Escape') {
      handled = this.clearCanvasSelection()
    } else if (modifier && key === 'z' && event.shiftKey) {
      if (this.redoStack.length > 0) { this.redo(); handled = true }
    } else if (modifier && key === 'z') {
      if (this.undoStack.length > 0) { this.undo(); handled = true }
    } else if (modifier && key === 'y') {
      if (this.redoStack.length > 0) { this.redo(); handled = true }
    } else if (modifier && key === 'c') {
      handled = this.copySelectedVisual()
    } else if (modifier && key === 'v') {
      handled = this.pasteCopiedVisual()
    } else if (!modifier && !event.shiftKey && (event.key === 'Delete' || event.key === 'Backspace')) {
      handled = this.deleteSelectedFilterComponent() || this.deleteSelectedVisual()
    }
    if (handled) event.preventDefault()
  }

  private keyboardEventUsesEditableTarget(event: KeyboardEvent): boolean {
    return event.composedPath().some((target) => target instanceof HTMLElement && (target.matches('input, textarea, select, button, a[href]') || target.isContentEditable))
  }

  private selectPendingAddedPage(builder: DashboardBuilderSignal | null): void {
    const pending = this.pendingAddPage
    if (!pending || !builder) return
    if (this.status.error) {
      this.pendingAddPage = null
      return
    }
    if (pending.revision === this.revisionKey(builder)) return
    const addedPage = builder.pages.find((page) => !pending.pageIDs.has(page.id))
    this.pendingAddPage = null
    if (!addedPage) return
    this.localPageID = addedPage.id
    this.localVisualID = ''
  }

  private reconcilePendingRemovedPage(builder: DashboardBuilderSignal | null): void {
    const pending = this.pendingRemovePage
    if (!pending || !builder || pending.revision === this.revisionKey(builder)) return
    this.pendingRemovePage = null
  }

  private selectPendingAddedVisual(builder: DashboardBuilderSignal | null, settle = false): void {
    const pending = this.pendingAddVisual
    if (!pending || !builder) return
    if (this.status.error) {
      this.pendingAddVisual = null
      return
    }
    if (pending.revision === this.revisionKey(builder)) return
    const page = builder.pages.find((item) => item.id === pending.pageID)
    const addedVisual = page?.visuals.find((visual) => !pending.visualIDs.has(visual.id))
    if (!page || !addedVisual) {
      if (settle) this.pendingAddVisual = null
      return
    }
    this.pendingAddVisual = null
    if (this.embeddedInChat && pending.autoArrange !== false) {
      this.autoArrangePageID = page.id
      this.autoArrangePreservedIDs = new Set(pending.visualIDs)
    }
    this.localPageID = page.id
    this.localVisualID = addedVisual.id
    this.addingSlicer = false
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.editingPage = false
    if (this.visualCatalogEntry(addedVisual.type, builder)) this.visualType = addedVisual.type
  }

  private selectPendingAddedFilter(builder: DashboardBuilderSignal | null): void {
    const pending = this.pendingAddFilter
    if (!pending || !builder) return
    if (this.status.error) {
      this.pendingAddFilter = null
      return
    }
    if (pending.revision === this.revisionKey(builder)) return
    const addedFilter = (builder.filters ?? []).find((filter) => !pending.filterIDs.has(filter.id))
    this.pendingAddFilter = null
    if (addedFilter) this.selectedFilterID = addedFilter.id
  }

  private selectPendingAddedFilterComponent(builder: DashboardBuilderSignal | null): void {
    const pending = this.pendingAddFilterComponent
    if (!pending || !builder) return
    if (this.status.error) {
      this.pendingAddFilterComponent = null
      return
    }
    if (pending.revision === this.revisionKey(builder)) return
    const page = builder.pages.find((item) => item.id === pending.pageID)
    const addedComponent = page?.filterComponents?.find((component) => !pending.componentIDs.has(component.id))
    this.pendingAddFilterComponent = null
    if (!page || !addedComponent) return
    this.localPageID = page.id
    this.localVisualID = ''
    this.addingSlicer = false
    this.editingPage = false
    this.selectedFilterID = addedComponent.filterId
    this.selectedFilterComponentID = addedComponent.id
  }

  private selectPendingAddedSlicer(builder: DashboardBuilderSignal | null): void {
    const pending = this.pendingAddSlicer
    if (!pending || !builder) return
    if (this.status.error) {
      this.pendingAddSlicer = null
      this.addingSlicer = false
      return
    }
    if (pending.revision === this.revisionKey(builder)) return
    const page = builder.pages.find((item) => item.id === pending.pageID)
    const addedFilter = (builder.filters ?? []).find((filter) => !pending.filterIDs.has(filter.id))
    const addedComponent = page?.filterComponents?.find((component) => !pending.componentIDs.has(component.id))
    if (!page || !addedFilter || !addedComponent || addedComponent.filterId !== addedFilter.id) return
    this.pendingAddSlicer = null
    this.addingSlicer = false
    this.localPageID = page.id
    this.localVisualID = ''
    this.editingPage = false
    this.selectedFilterID = addedFilter.id
    this.selectedFilterComponentID = addedComponent.id
  }

  private revisionKey(builder: DashboardBuilderSignal): string {
    return `${builder.revision.id}:${builder.revision.number}:${builder.revision.contentHash}`
  }

  private onFieldQuery = (event: Event): void => {
    this.fieldQuery = (event.currentTarget as HTMLInputElement).value
  }

  private clearFieldQuery = (): void => {
    this.fieldQuery = ''
    void this.updateComplete.then(() => this.renderRoot.querySelector<HTMLInputElement>('.search')?.focus())
  }

  private saveLabel(builder: DashboardBuilderSignal): string {
    if (builder.save.state === 'saving') return 'Saving…'
    if (builder.save.state === 'error') return builder.save.message || 'Save failed'
    // The server's dirty state compares the saved draft with its published
    // revision; it does not mean an autosave is still running.
    if (builder.save.state === 'dirty') return 'Saved · Unpublished'
    if (builder.hasUnpublishedChanges) return 'Saved · Unpublished'
    return builder.save.message || 'Saved'
  }

  private titleCase(value: string): string {
    return value.length === 0 ? value : `${value[0].toUpperCase()}${value.slice(1)}`
  }
}

function dashboardAppearanceColor(value: string): string {
  return ['gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'].includes(value) ? value : 'purple'
}

function currentResolvedTheme(): BuilderResolvedTheme {
  if (typeof document === 'undefined') return 'light'
  const colorMode = document.documentElement.dataset.colorMode
  if (colorMode === 'dark' || colorMode === 'light') return colorMode
  return typeof window !== 'undefined' && window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

if (!customElements.get('lv-dashboard-builder')) customElements.define('lv-dashboard-builder', LeapViewDashboardBuilder)

declare global {
  interface HTMLElementTagNameMap {
    'lv-dashboard-builder': LeapViewDashboardBuilder
  }
}
