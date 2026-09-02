import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { GridStack, type GridItemHTMLElement, type GridStackNode } from 'gridstack'
import { Archive, ChartColumn, ChevronLeft, ChevronRight, Copy, Database, GripHorizontal, ListFilter, Moon, MoreHorizontal, PanelRightClose, PanelRightOpen, Plus, Redo2, Settings2, Sun, Trash2, Undo2 } from 'lucide'
import { repeat } from 'lit/directives/repeat.js'
import type {
  DashboardBuilderDiagnosticSignal,
  DashboardBuilderFieldSignal,
  DashboardBuilderFilterComponentSignal,
  DashboardBuilderFilterSignal,
  DashboardBuilderFormatOptionSignal,
  DashboardBuilderInteractionSignal,
  DashboardBuilderPageSignal,
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
import { lucideIconByCanonicalName } from '../shared/lucide-catalog'
import { lucideIcon } from '../shared/lucide-icons'
import { checkSignalContract } from '../shared/signal-contract'
import { browserCommandFailure, ownsBrowserCommandFetch, type BrowserCommandFailure } from '../shared/command-failure'
import './visualization/host'
import { DashboardVisualizationSignalDecoder } from './visualization/signal-envelope'
import { renderVisualTypeIcon } from './visual-type-icon'
import './filters/filter-control'
import { DashboardFilterController } from './filters/filter-controller'
import type { FilterMutationDetail, FilterOptionsNeededDetail } from './filters/filter-control'
import '../app/dashboard-icon-picker'

const emptyStatus: DashboardStatus = {
  loading: false,
  error: '',
  generation: 0,
  lastUpdated: '',
  refreshId: '',
  setupRequired: false,
  progressPercent: 100,
}

type BuilderVisualType = string
type BuilderInspectorTab = 'build' | 'format'
type BuilderFieldRole = 'dimension' | 'metric' | 'detail'
type BuilderFieldFilter = 'all' | 'metric' | 'dimension' | 'time'
type BuilderFilterControl = DashboardBuilderFilterSignal['controlType']
type BuilderFilterScope = 'report' | 'page' | 'visual' | 'custom'
type BuilderPane = 'filters' | 'visuals' | 'data'
type BuilderInteractionEffect = 'filter' | 'highlight' | 'none'
type BuilderResolvedTheme = 'light' | 'dark'

const builderPaneStorageKey = 'leapview-dashboard-builder-collapsed-panes'
const defaultCollapsedPanes: Record<BuilderPane, boolean> = { filters: false, visuals: false, data: false }
const builderCanvasDesktopWidth = 1366
const builderCanvasMinimumHeight = 768
const builderCanvasRunwayRows = 3

type BuilderCatalogField = {
  field: DashboardBuilderFieldSignal
  datasets: Array<{ id: string; title: string }>
  group: Exclude<BuilderFieldFilter, 'all'>
}

type BuilderCatalogEntity = {
  id: string
  title: string
  fields: BuilderCatalogField[]
}

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
  @property({ attribute: 'back-href' }) backHref = ''
  @property({ attribute: 'fork-href' }) forkHref = ''
  @property({ attribute: 'page-base-href' }) pageBaseHref = ''
  @property({ attribute: 'preview-href' }) previewHref = ''
  @property({ attribute: 'export-yaml-href' }) exportYAMLHref = ''

  @state() private fieldQuery = ''
  @state() private localPageID = ''
  // null follows the server's initial selection; an empty string records an
  // explicit canvas deselection without falling back to the first visual.
  @state() private localVisualID: string | null = null
  @state() private visualType: BuilderVisualType = 'bar'
  @state() private inspectorTab: BuilderInspectorTab = 'build'
  @state() private fieldFilter: BuilderFieldFilter = 'all'
  @state() private selectedFilterID = ''
  @state() private selectedFilterComponentID = ''
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
  @state() private appearanceOpen = false
  @state() private resolvedTheme: BuilderResolvedTheme = currentResolvedTheme()
  private commandPending = false
  private activeCommandAction = ''
  private interactionOverridesRevision = ''
  private pendingHistorySnapshot: BuilderHistorySnapshot | null = null
  private pendingVisualTypeSwitch: BuilderVisualTypeSwitch | null = null
  private reversibleVisualTypeSwitch: BuilderVisualTypeSwitch | null = null
  private copiedVisual: BuilderClipboard | null = null
  private readonly visualizationDecoder = new DashboardVisualizationSignalDecoder()
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
  private viewportMediaQuery: MediaQueryList | null = null
  private canvasResizeObserver: ResizeObserver | null = null
  private canvasViewportElement: HTMLElement | null = null

  // Add-page uses server-generated identifiers. Keep the page set that was
  // visible when the intent was sent so the authoritative response can select
  // the page created by that intent, even when the response's selectedPageId
  // still reflects the page that was active before the mutation.
  private pendingAddPage: { revision: string; pageIDs: Set<string> } | null = null
  private pendingRemovePage: { revision: string; pageID: string; visualID: string | null } | null = null
  private pendingAddVisual: { revision: string; visualIDs: Set<string>; pageID: string } | null = null
  private pendingAddFilter: { revision: string; filterIDs: Set<string> } | null = null
  private pendingAddFilterComponent: { revision: string; componentIDs: Set<string>; pageID: string } | null = null
  private pendingAddSlicer: { revision: string; filterIDs: Set<string>; componentIDs: Set<string>; pageID: string } | null = null

  override connectedCallback(): void {
    super.connectedCallback()
    this.restoreCollapsedPanes()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
    document.addEventListener('leapview-theme-applied', this.handleThemeApplied)
    this.addEventListener('lv-filter-mutate', this.handleBuilderFilterMutation as EventListener, { capture: true })
    this.addEventListener('lv-filter-options-needed', this.handleBuilderFilterOptionsNeeded as EventListener, { capture: true })
    if (typeof window !== 'undefined') {
      window.addEventListener('keydown', this.handleBuilderKeydown)
      this.viewportMediaQuery = window.matchMedia('(max-width: 640px)')
      this.viewportMediaQuery.addEventListener('change', this.handleViewportChange)
    }
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    document.removeEventListener('leapview-theme-applied', this.handleThemeApplied)
    this.removeEventListener('lv-filter-mutate', this.handleBuilderFilterMutation as EventListener, { capture: true })
    this.removeEventListener('lv-filter-options-needed', this.handleBuilderFilterOptionsNeeded as EventListener, { capture: true })
    if (typeof window !== 'undefined') window.removeEventListener('keydown', this.handleBuilderKeydown)
    this.viewportMediaQuery?.removeEventListener('change', this.handleViewportChange)
    this.viewportMediaQuery = null
    this.destroyCanvasViewportObserver()
    this.destroyGridStack()
    super.disconnectedCallback()
  }

  static styles = css`
    :host {
      display: block;
      min-height: 100svh;
      color: var(--lv-fg-default);
      background: var(--lv-bg-app);
      font-family: var(--fontStack-system);
    }

    .sr-only {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }

    .builder {
      display: grid;
      height: 100svh;
      min-height: 100svh;
      grid-template-rows: auto minmax(0, 1fr);
    }

    .toolbar {
      display: flex;
      align-items: center;
      gap: var(--base-size-12);
      min-height: var(--control-medium-size);
      padding: var(--base-size-8) var(--base-size-16);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .terminal-failure {
      display: flex;
      align-items: center;
      flex-wrap: wrap;
      gap: var(--base-size-8);
      padding: var(--base-size-8) var(--base-size-16);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-danger-muted, var(--lv-bg-panel-muted));
      color: var(--lv-fg-danger, var(--lv-fg-default));
      font: var(--lv-type-body-compact);
    }

    .terminal-failure span {
      min-width: 0;
      flex: 1 1 18rem;
    }

    .back {
      color: inherit;
      font: var(--lv-type-body-compact);
      text-decoration: none;
      white-space: nowrap;
    }

    .back:focus-visible,
    button:focus-visible,
    input:focus-visible,
    [role='button']:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .title-wrap {
      min-width: 0;
      margin-right: auto;
    }

    .appearance-control {
      position: relative;
      flex: 0 0 auto;
    }

    .appearance-trigger {
      display: grid;
      width: var(--control-medium-size);
      min-height: var(--control-medium-size);
      place-items: center;
      padding: 0;
      border-color: var(--display-purple-borderColor-muted, var(--lv-border-muted));
      background: var(--display-purple-bgColor-muted, var(--lv-bg-panel-muted));
      color: var(--display-purple-fgColor, var(--lv-fg-default));
    }

    .appearance-trigger.appearance-color-gray { border-color: var(--display-gray-borderColor-muted, var(--lv-border-muted)); background: var(--display-gray-bgColor-muted); color: var(--display-gray-fgColor); }
    .appearance-trigger.appearance-color-blue { border-color: var(--display-blue-borderColor-muted, var(--lv-border-muted)); background: var(--display-blue-bgColor-muted); color: var(--display-blue-fgColor); }
    .appearance-trigger.appearance-color-green { border-color: var(--display-green-borderColor-muted, var(--lv-border-muted)); background: var(--display-green-bgColor-muted); color: var(--display-green-fgColor); }
    .appearance-trigger.appearance-color-yellow { border-color: var(--display-yellow-borderColor-muted, var(--lv-border-muted)); background: var(--display-yellow-bgColor-muted); color: var(--display-yellow-fgColor); }
    .appearance-trigger.appearance-color-orange { border-color: var(--display-orange-borderColor-muted, var(--lv-border-muted)); background: var(--display-orange-bgColor-muted); color: var(--display-orange-fgColor); }
    .appearance-trigger.appearance-color-red { border-color: var(--display-red-borderColor-muted, var(--lv-border-muted)); background: var(--display-red-bgColor-muted); color: var(--display-red-fgColor); }
    .appearance-trigger.appearance-color-purple { border-color: var(--display-purple-borderColor-muted, var(--lv-border-muted)); background: var(--display-purple-bgColor-muted); color: var(--display-purple-fgColor); }
    .appearance-trigger.appearance-color-pink { border-color: var(--display-pink-borderColor-muted, var(--lv-border-muted)); background: var(--display-pink-bgColor-muted); color: var(--display-pink-fgColor); }
    .appearance-trigger.appearance-color-coral { border-color: var(--display-coral-borderColor-muted, var(--lv-border-muted)); background: var(--display-coral-bgColor-muted); color: var(--display-coral-fgColor); }

    .appearance-popover {
      position: absolute;
      z-index: 5;
      top: calc(100% + var(--base-size-8));
      left: 0;
      width: min(22.5rem, calc(100vw - var(--base-size-16)));
    }

    .title {
      margin: 0;
      overflow: hidden;
      font: var(--lv-type-section-title);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .meta {
      display: flex;
      align-items: center;
      gap: var(--base-size-6);
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .meta::before {
      width: var(--base-size-6);
      height: var(--base-size-6);
      border-radius: var(--lv-radius-full);
      background: var(--lv-fg-muted);
      content: '';
    }

    .meta[data-state='dirty']::before,
    .meta[data-state='saving']::before,
    .meta[data-state='error']::before {
      background: var(--lv-fg-warning);
    }

    .meta[data-state='saved']::before {
      background: var(--lv-fg-success);
    }

    .toolbar-actions {
      display: flex;
      align-items: center;
      gap: 0.4rem;
    }

    @media (max-width: 640px) {
      .appearance-popover {
        position: fixed;
        top: calc(var(--control-medium-size) + var(--base-size-16));
        right: var(--base-size-8);
        left: var(--base-size-8);
        width: auto;
      }
    }

    .icon-action,
    .pane-collapse {
      width: var(--lv-button-height-xs, var(--control-xsmall-size));
      min-height: var(--lv-button-height-xs, var(--control-xsmall-size));
      flex: 0 0 auto;
      padding: 0;
      border-color: var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest));
      color: var(--lv-button-invisible-icon-rest, var(--lv-fg-muted));
      background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest));
    }

    .icon-action:hover,
    .pane-collapse:hover {
      border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover));
      color: var(--lv-fg-default);
      background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover));
    }

    button,
    .button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--lv-button-padding-inline, var(--base-size-12));
      color: var(--lv-button-fg-rest);
      background: var(--lv-button-bg-rest);
      font: var(--lv-type-body-compact);
      text-decoration: none;
      cursor: pointer;
    }

    button:hover,
    .button:hover {
      background: var(--lv-button-bg-hover);
    }

    button.primary {
      border-color: var(--lv-button-accent-border-rest);
      color: var(--lv-button-accent-fg-rest);
      background: var(--lv-button-accent-bg-rest);
    }

    button.primary:hover {
      background: var(--lv-button-accent-bg-hover);
    }

    .more-actions {
      position: relative;
    }

    .more-actions summary {
      display: inline-flex;
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      align-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--lv-button-padding-inline, var(--base-size-12));
      color: var(--lv-button-fg-rest);
      background: var(--lv-button-bg-rest);
      font: var(--lv-type-body-compact);
      cursor: pointer;
      list-style: none;
    }

    .more-actions summary::-webkit-details-marker {
      display: none;
    }

    .more-actions summary:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .more-actions summary:hover {
      background: var(--lv-button-bg-hover);
    }

    .more-menu {
      position: absolute;
      z-index: 2;
      top: calc(100% + var(--base-size-6));
      right: 0;
      display: grid;
      min-width: 10rem;
      gap: var(--base-size-2);
      padding: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
    }

    .more-menu button,
    .more-menu .button {
      justify-content: flex-start;
      width: 100%;
      border-color: transparent;
      background: transparent;
      text-align: left;
    }

    .more-menu button:hover,
    .more-menu .button:hover {
      background: var(--lv-bg-panel-muted);
    }

    .more-menu .archive-action {
      color: var(--lv-fg-danger);
    }

    button:disabled {
      cursor: not-allowed;
      opacity: 0.55;
    }

    .body {
      display: grid;
      min-height: 0;
      grid-template-columns: minmax(0, 1fr) max-content;
      grid-template-rows: minmax(0, 1fr) auto;
    }

    .right-dock {
      display: grid;
      grid-column: 2;
      grid-row: 1 / span 2;
      min-width: 0;
      min-height: 0;
      grid-template-columns: var(--dock-filters-width) var(--dock-visuals-width) var(--dock-data-width);
      background: var(--lv-bg-panel);
    }

    .pane {
      min-width: 0;
      min-height: 0;
      overflow: auto;
      background: var(--lv-bg-panel);
    }

    .properties {
      border-left: var(--lv-border-muted);
    }

    .filters-pane {
      border-left: var(--lv-border-muted);
    }

    .data-pane {
      border-left: var(--lv-border-muted);
    }

    .filter-pane-body {
      display: grid;
      align-content: start;
      gap: var(--base-size-8);
      padding: var(--base-size-8) var(--base-size-12) var(--base-size-16);
    }

    .filter-validation {
      margin: var(--base-size-6) 0 0;
      color: var(--lv-fg-danger, var(--lv-fg-default));
      font: var(--lv-type-caption);
    }

    .filter-scope-heading {
      display: flex;
      align-items: center;
      justify-content: space-between;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
    }

    .filter-drop-zone {
      display: grid;
      min-height: 3.75rem;
      place-items: center;
      padding: var(--base-size-8);
      border: 1px dashed var(--lv-fg-muted);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .filter-drop-zone[data-field-dragging='true'] {
      border-color: var(--lv-fg-accent);
      color: var(--lv-fg-default);
      background: var(--lv-bg-accent-muted, var(--lv-bg-panel-muted));
    }

    .filter-add-select,
    .filter-editor select,
    .filter-editor input[type='text'] {
      width: 100%;
      min-height: var(--control-medium-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-default);
      background: var(--lv-bg-input, var(--lv-bg-panel));
      font: var(--lv-type-body-compact);
    }

    .filter-list {
      display: grid;
      gap: var(--base-size-6);
    }

    .filter-scope-group {
      display: grid;
      gap: var(--base-size-4);
    }

    .filter-scope-empty {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .filter-scope-options {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: var(--base-size-4);
    }

    .filter-scope-option {
      position: relative;
      display: flex !important;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: center;
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: 0 var(--base-size-6);
      color: var(--lv-fg-default) !important;
      background: var(--lv-bg-panel);
      cursor: pointer;
    }

    .filter-scope-option:has(input:checked) {
      border-color: var(--lv-line-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
      font-weight: var(--base-text-weight-semibold);
    }

    .filter-scope-option:has(input:disabled) {
      cursor: not-allowed;
      opacity: 0.55;
    }

    .filter-scope-option input {
      position: absolute;
      width: 1px;
      height: 1px;
      opacity: 0;
    }

    .filter-scope-option:has(input:focus-visible) {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .filter-scope-option span {
      font: var(--lv-type-caption);
      text-align: center;
    }

    .filter-card {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      width: 100%;
      min-height: var(--control-medium-size);
      align-items: center;
      gap: var(--base-size-8);
      padding: var(--base-size-6) var(--base-size-8);
      border: 1px solid transparent;
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      color: var(--lv-fg-default);
      background: transparent;
      text-align: left;
    }

    .filter-card[aria-pressed='true'] {
      border-color: var(--lv-line-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
    }

    .filter-card-preview {
      min-width: 0;
      border-radius: var(--lv-radius-default);
      outline: var(--lv-border-width) solid transparent;
      outline-offset: var(--base-size-2);
    }

    .filter-card-preview[data-selected='true'] {
      outline-color: var(--lv-line-accent);
    }

    .filter-card-preview lv-filter-pane-card {
      display: block;
      width: 100%;
    }

    .filter-card-title {
      overflow: hidden;
      font: var(--lv-type-body-compact);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .filter-card-meta {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .filter-editor {
      display: grid;
      gap: var(--base-size-8);
      margin-top: var(--base-size-4);
      padding-top: var(--base-size-12);
      border-top: var(--lv-border-muted);
    }

    .filter-editor label {
      display: grid;
      gap: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .filter-editor .filter-toggle {
      display: flex;
      align-items: center;
      justify-content: space-between;
    }

    .filter-settings {
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      background: var(--lv-bg-panel);
    }

    .filter-settings summary {
      padding: var(--base-size-8);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      cursor: pointer;
    }

    .filter-settings-body {
      display: grid;
      gap: var(--base-size-8);
      padding: 0 var(--base-size-8) var(--base-size-8);
    }

    .filter-editor-actions {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-6);
    }

    .filter-editor-actions button {
      min-height: var(--control-small-size);
      padding-inline: var(--base-size-8);
    }

    .filter-remove {
      color: var(--lv-fg-danger, var(--lv-fg-default));
    }

    .filter-placement-action {
      color: var(--lv-data-2);
    }

    .filter-pane-empty {
      margin: var(--base-size-8) 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .filter-count {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-normal);
    }

    .page-bar {
      display: flex;
      grid-column: 1;
      grid-row: 2;
      min-width: 0;
      min-height: var(--control-large-size);
      align-items: center;
      gap: var(--base-size-4);
      border-top: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .page-bar > button {
      flex: 0 0 auto;
      width: var(--control-large-size);
      min-height: var(--control-large-size);
      padding: 0;
      border-color: transparent;
    }

    .page-add {
      margin-right: var(--base-size-8);
    }

    .page-actions {
      position: relative;
      flex: 0 0 auto;
    }

    .page-actions > summary {
      display: grid;
      width: var(--control-medium-size);
      min-height: var(--control-medium-size);
      place-items: center;
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      color: var(--lv-fg-muted);
      cursor: pointer;
      list-style: none;
    }

    .page-actions > summary::-webkit-details-marker {
      display: none;
    }

    .page-actions > summary:hover {
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel-muted);
    }

    .page-actions-menu {
      position: absolute;
      z-index: 4;
      right: 0;
      bottom: calc(100% + var(--base-size-4));
      display: grid;
      min-width: 10rem;
      gap: var(--base-size-2);
      padding: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
    }

    .page-actions-menu button {
      display: flex;
      width: 100%;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: flex-start;
      gap: var(--base-size-8);
      border-color: transparent;
      background: transparent;
      text-align: left;
    }

    .page-actions-menu button:hover:not(:disabled) {
      background: var(--lv-bg-panel-muted);
    }

    .page-actions-menu .page-delete {
      color: var(--lv-fg-danger, var(--lv-fg-default));
    }

    .pane-header {
      position: sticky;
      top: 0;
      z-index: 1;
      padding: 0.9rem 0.85rem 0.6rem;
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .pane-heading-row {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
    }

    .pane-title-group {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .pane-title-icon {
      display: inline-flex;
      flex: 0 0 auto;
      color: var(--lv-fg-muted);
    }

    .pane-content[hidden],
    .pane-header-details[hidden] {
      display: none !important;
    }

    .inspector-heading {
      display: flex;
      align-items: center;
      justify-content: space-between;
      flex-wrap: wrap;
      gap: var(--base-size-8);
    }

    .inspector-title {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .inspector-heading .pane-title {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .visual-type-badge {
      flex: 0 0 auto;
      border-radius: var(--lv-radius-full);
      padding: var(--base-size-2) var(--base-size-6);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
    }

    .pane-title {
      margin: 0;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .pane-hint {
      margin: 0.25rem 0 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      line-height: 1.4;
    }

    .search {
      width: 100%;
      box-sizing: border-box;
      min-height: var(--control-small-size);
      margin-top: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: 0 var(--control-small-paddingInline-normal, var(--base-size-8));
      color: var(--lv-fg-default);
      background: var(--lv-bg-input, var(--lv-bg-control));
      font: var(--lv-type-body-compact);
    }

    .field-filter {
      display: flex;
      align-items: center;
      gap: var(--base-size-4);
      margin-top: var(--base-size-8);
      overflow-x: auto;
      scrollbar-width: thin;
    }

    .field-filter button {
      min-height: var(--control-small-size);
      flex: 0 0 auto;
      border: 1px solid transparent;
      border-radius: var(--lv-radius-full);
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-muted);
      background: transparent;
      font: var(--lv-type-caption);
      cursor: pointer;
    }

    .field-filter button:hover {
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-default);
    }

    .field-filter button[aria-pressed='true'] {
      border-color: var(--lv-line-default);
      color: var(--lv-fg-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
      font-weight: var(--base-text-weight-semibold);
    }

    .field-results {
      padding: var(--base-size-8) var(--base-size-12) var(--base-size-16);
    }

    .field-entity + .field-entity,
    .catalog-disclosure {
      margin-top: var(--base-size-8);
    }

    .field-entity {
      border-bottom: var(--lv-border-muted);
      padding-bottom: var(--base-size-8);
    }

    .field-entity summary,
    .catalog-disclosure > summary {
      display: flex;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      cursor: pointer;
    }

    .field-entity-title,
    .catalog-disclosure-title {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-entity-count,
    .catalog-disclosure-count {
      flex: 0 0 auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .field-entity > .field-list,
    .catalog-entity > .field-list {
      margin-top: var(--base-size-4);
    }

    .field-list {
      display: grid;
      gap: var(--base-size-2);
    }

    .field {
      display: grid;
      grid-template-columns: 1.25rem minmax(0, 1fr) auto;
      align-items: start;
      column-gap: var(--base-size-8);
      width: 100%;
      box-sizing: border-box;
      border: 1px solid transparent;
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      min-height: 2.65rem;
      padding: var(--base-size-6) var(--base-size-8);
      color: inherit;
      background: transparent;
      text-align: left;
      cursor: grab;
    }

    .field:hover {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-panel-muted);
    }

    .field:disabled {
      cursor: not-allowed;
      opacity: 0.65;
    }

    .field[data-used='true'] {
      background: var(--lv-bg-success-muted, var(--lv-bg-panel-muted));
    }

    .field[data-dragging='true'] {
      border-color: var(--lv-data-2);
      background: var(--lv-data-2-muted);
    }

    .field-role-icon {
      display: inline-flex;
      width: 1.1rem;
      height: 1.1rem;
      align-items: center;
      justify-content: center;
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
    }

    .field-role-icon svg {
      width: 1rem;
      height: 1rem;
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 1.75;
    }

    .field-copy {
      min-width: 0;
    }

    .field-label {
      display: block;
      overflow: hidden;
      font: var(--lv-type-body-compact);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-context {
      display: block;
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-used {
      align-self: center;
      border-radius: var(--lv-radius-full);
      padding: var(--base-size-2) var(--base-size-6);
      color: var(--lv-fg-success, var(--lv-fg-default));
      background: var(--lv-bg-panel);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .catalog-disclosure {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-8);
    }

    .catalog-disclosure > summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .catalog-disclosure-body {
      margin-top: var(--base-size-4);
    }

    .catalog-entity + .catalog-entity {
      margin-top: var(--base-size-8);
    }

    .catalog-entity-title {
      margin: 0 0 var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
    }

    .field-browser {
      min-height: 0;
    }

    .field-browser-header {
      position: sticky;
      z-index: 1;
      top: 0;
      padding: var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    @media (min-width: 1201px), (min-width: 641px) and (max-width: 960px) {
      .pane[data-collapsed='true'] {
        overflow: hidden;
      }

      .pane[data-collapsed='true'] .pane-header,
      .pane[data-collapsed='true'] .field-browser-header {
        position: static;
        height: 100%;
        box-sizing: border-box;
        padding: var(--base-size-8) var(--base-size-4);
        border-bottom: 0;
      }

      .pane[data-collapsed='true'] .pane-heading-row,
      .pane[data-collapsed='true'] .inspector-heading {
        height: 100%;
        flex-direction: column-reverse;
        justify-content: flex-end;
        flex-wrap: nowrap;
      }

      .pane[data-collapsed='true'] .pane-title-group,
      .pane[data-collapsed='true'] .inspector-title {
        flex-direction: column;
      }

      .pane[data-collapsed='true'] .pane-title {
        writing-mode: vertical-rl;
        transform: rotate(180deg);
        overflow: visible;
        text-overflow: clip;
      }
    }

    .canvas-pane {
      display: grid;
      grid-column: 1;
      grid-row: 1;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr);
      background: var(--lv-report-canvas-bg, var(--lv-bg-app));
    }

    .page-tabs {
      display: flex;
      flex: 1 1 auto;
      min-width: 0;
      gap: var(--base-size-4);
      overflow-x: auto;
      overscroll-behavior-x: contain;
      padding: var(--base-size-4) var(--base-size-8);
      scrollbar-width: thin;
    }

    .page-tab {
      display: flex;
      flex: 0 0 auto;
      min-width: 4.5rem;
      max-width: 14rem;
      min-height: var(--control-medium-size);
      align-items: center;
      justify-content: center;
      overflow: hidden;
      border: 1px solid transparent;
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--base-size-8);
      color: inherit;
      background: transparent;
      font: var(--lv-type-body-compact);
      text-align: left;
      text-decoration: none;
      text-overflow: ellipsis;
      white-space: nowrap;
      cursor: pointer;
    }

    .page-tab:hover {
      background: var(--lv-bg-panel-muted);
    }

    .page-tab:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: -2px;
    }

    .page-tab[aria-selected='true'] {
      border-color: var(--lv-data-3);
      color: var(--lv-fg-default);
      background: var(--lv-data-3-muted);
      font-weight: var(--base-text-weight-semibold);
    }

    .page-tab[data-page-dragging='true'] {
      opacity: 0.55;
    }

    .page-tab[data-page-drop='true'] {
      box-shadow: inset 3px 0 0 var(--lv-line-emphasis);
    }

    .canvas-scroll {
      position: relative;
      overflow: auto;
      min-width: 0;
      padding: 0;
      background: var(--lv-report-canvas-bg, var(--lv-bg-app));
    }

    .canvas-fit {
      position: relative;
      width: var(--builder-canvas-fitted-width, 100%);
      height: var(--builder-canvas-fitted-height, 0);
      margin-inline: auto;
    }

    .canvas {
      position: absolute;
      inset: 0 auto auto 0;
      box-sizing: border-box;
      width: var(--builder-canvas-width, ${builderCanvasDesktopWidth}px);
      min-width: var(--builder-canvas-width, ${builderCanvasDesktopWidth}px);
      min-height: var(--builder-canvas-min-height, ${builderCanvasMinimumHeight}px);
      border: 0;
      border-radius: 0;
      background-color: var(--lv-report-page-bg, var(--lv-bg-panel));
      background-image: none;
      background-size: calc(100% / var(--builder-grid-columns)) var(--builder-grid-row-pitch);
      box-shadow: none;
      transform: scale(var(--builder-canvas-scale, 1));
      transform-origin: top left;
    }

    .canvas[data-field-dragging='true'] {
      outline: 2px dashed var(--lv-data-2);
      outline-offset: -4px;
    }

    .canvas[data-grid-guides='true'],
    .canvas:has(.ui-draggable-dragging, .ui-resizable-resizing) {
      background-image: linear-gradient(to right, color-mix(in srgb, var(--lv-line-muted) 55%, transparent) 1px, transparent 1px), linear-gradient(to bottom, color-mix(in srgb, var(--lv-line-muted) 55%, transparent) 1px, transparent 1px);
    }

    .canvas-field-drop-hint {
      position: sticky;
      z-index: 5;
      top: var(--base-size-12);
      display: none;
      width: fit-content;
      max-width: calc(100% - 2rem);
      align-items: center;
      margin: var(--base-size-12) auto 0;
      border: 1px solid var(--lv-data-2);
      border-radius: var(--lv-radius-full);
      padding: var(--base-size-6) var(--base-size-12);
      color: var(--lv-fg-default);
      background: var(--lv-data-2-muted);
      box-shadow: var(--lv-shadow-floating-sm);
      font: var(--lv-type-body-compact);
      pointer-events: none;
    }

    .canvas[data-field-dragging='true'] .canvas-field-drop-hint {
      display: flex;
    }

    /* GridStack's package stylesheet cannot cross this component's shadow
     * boundary, so keep the small set of layout rules it needs local. The
     * library supplies the --gs-* values and writes the gs-* attributes. */
    .grid-stack {
      position: relative;
    }

    .grid-stack > .grid-stack-item {
      position: absolute;
      top: 0;
      width: var(--gs-column-width);
      height: var(--gs-cell-height);
      padding: 0;
    }

    .grid-stack > .grid-stack-item > .grid-stack-item-content {
      position: absolute;
      top: var(--gs-item-margin-top);
      right: var(--gs-item-margin-right);
      bottom: var(--gs-item-margin-bottom);
      left: var(--gs-item-margin-left);
      display: grid;
      width: auto;
      height: auto;
      margin: 0;
      overflow: hidden;
    }

    .grid-stack:not(.grid-stack-rtl) > .grid-stack-item {
      left: 0;
    }

    .grid-stack > .grid-stack-item > .grid-stack-item-content {
      top: var(--gs-item-margin-top);
      right: var(--gs-item-margin-right);
      bottom: var(--gs-item-margin-bottom);
      left: var(--gs-item-margin-left);
    }

    .grid-stack-item > .ui-resizable-handle {
      position: absolute;
      z-index: 2;
      display: block;
      width: 12px;
      height: 12px;
      touch-action: none;
      user-select: none;
    }

    .grid-stack-item > .ui-resizable-se {
      right: var(--gs-item-margin-right);
      bottom: var(--gs-item-margin-bottom);
      cursor: se-resize;
    }

    .grid-stack-item > .ui-resizable-se::after {
      position: absolute;
      right: 2px;
      bottom: 2px;
      width: 6px;
      height: 6px;
      border-right: 2px solid var(--lv-fg-muted);
      border-bottom: 2px solid var(--lv-fg-muted);
      content: '';
    }

    .grid-stack-item.ui-resizable-disabled > .ui-resizable-handle {
      display: none;
    }

    .grid-stack-item:not([data-selected='true']) > .ui-resizable-handle {
      display: none !important;
    }

    .visual > .grid-stack-item-content,
    .filter-component > .grid-stack-item-content {
      grid-template-rows: auto minmax(0, 1fr) auto;
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-8);
      color: inherit;
      background: color-mix(in srgb, var(--lv-bg-panel) 96%, transparent);
      text-align: left;
    }

    .visual.has-preview > .grid-stack-item-content {
      grid-template-rows: minmax(0, 1fr);
      padding: 0;
    }

    .visual:hover > .grid-stack-item-content {
      border-color: var(--lv-line-emphasis);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-bg-control-hover);
    }

    .visual[data-selected='true'] > .grid-stack-item-content {
      border-color: var(--lv-data-3);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-data-3-muted);
    }

    .filter-component[data-selected='true'] > .grid-stack-item-content {
      border-color: var(--lv-data-2);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-data-2-muted);
    }

    .component-drag-handle {
      display: flex;
      width: 100%;
      min-width: 0;
      min-height: var(--control-small-size);
      align-items: center;
      overflow: hidden;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
      cursor: grab;
      touch-action: none;
      user-select: none;
    }

    .component-drag-handle:hover {
      color: var(--lv-data-4);
    }

    .component-drag-grip {
      position: absolute;
      z-index: 3;
      top: var(--base-size-4);
      left: 50%;
      display: grid;
      width: var(--control-small-size);
      min-height: 1.25rem;
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-full);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
      opacity: 0;
      pointer-events: none;
      transform: translateX(-50%);
      transition: opacity var(--lv-duration-fast) var(--motion-easing-move);
    }

    .component-drag-grip svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .visual:hover .component-drag-grip,
    .visual[data-selected='true'] .component-drag-grip,
    .filter-component:hover .component-drag-grip,
    .filter-component[data-selected='true'] .component-drag-grip {
      opacity: 1;
      pointer-events: auto;
    }

    .component-drag-handle:active,
    .grid-stack-item.ui-draggable-dragging .component-drag-handle {
      cursor: grabbing;
    }

    .visual-picker-catalog {
      display: block;
      max-height: none;
      overflow: visible;
    }

    .visual-picker {
      display: grid;
      grid-template-columns: repeat(7, minmax(0, 1fr));
      gap: var(--base-size-4);
    }

    .visual-picker-button {
      --visual-picker-color: var(--lv-fg-muted);
      --visual-picker-muted: var(--lv-bg-panel-muted);
      display: grid;
      min-width: 0;
      min-height: 2.25rem;
      place-items: center;
      border-color: transparent;
      padding: var(--base-size-2);
      color: var(--visual-picker-color);
      background: var(--lv-bg-panel-muted);
    }

    .visual-picker-button[data-visual-picker-type='bar'] {
      --visual-picker-color: var(--lv-data-1);
      --visual-picker-muted: var(--lv-data-1-muted);
    }

    .visual-picker-button[data-visual-picker-type='column'] {
      --visual-picker-color: var(--lv-data-2);
      --visual-picker-muted: var(--lv-data-2-muted);
    }

    .visual-picker-button[data-visual-picker-type='line'] {
      --visual-picker-color: var(--lv-data-3);
      --visual-picker-muted: var(--lv-data-3-muted);
    }

    .visual-picker-button[data-visual-picker-type='area'] {
      --visual-picker-color: var(--lv-data-4);
      --visual-picker-muted: var(--lv-data-4-muted);
    }

    .visual-picker-button[data-visual-picker-type='table'] {
      --visual-picker-color: var(--lv-data-5);
      --visual-picker-muted: var(--lv-data-5-muted);
    }

    .visual-picker-button[data-visual-picker-type='kpi'] {
      --visual-picker-color: var(--lv-data-6);
      --visual-picker-muted: var(--lv-data-6-muted);
    }

    .visual-picker-button[data-visual-group='Part to whole'] {
      --visual-picker-color: var(--lv-data-4);
      --visual-picker-muted: var(--lv-data-4-muted);
    }

    .visual-picker-button[data-visual-group='Distribution'] {
      --visual-picker-color: var(--lv-data-3);
      --visual-picker-muted: var(--lv-data-3-muted);
    }

    .visual-picker-button[data-visual-group='Hierarchy & flow'] {
      --visual-picker-color: var(--lv-data-2);
      --visual-picker-muted: var(--lv-data-2-muted);
    }

    .visual-picker-button[data-visual-group='Specialized'] {
      --visual-picker-color: var(--lv-data-6);
      --visual-picker-muted: var(--lv-data-6-muted);
    }

    .visual-picker-button[data-visual-group='Tables'] {
      --visual-picker-color: var(--lv-data-5);
      --visual-picker-muted: var(--lv-data-5-muted);
    }

    .visual-picker-button[data-visual-group='Filters'] {
      --visual-picker-color: var(--lv-data-1);
      --visual-picker-muted: var(--lv-data-1-muted);
    }

    .visual-picker-button:hover {
      border-color: color-mix(in srgb, var(--visual-picker-color) 55%, var(--lv-line-default));
      background: color-mix(in srgb, var(--visual-picker-muted) 72%, var(--lv-bg-panel-muted));
    }

    .visual-picker-button[aria-pressed='true'] {
      border-color: var(--visual-picker-color);
      background: var(--visual-picker-muted);
      box-shadow: inset 0 0 0 var(--lv-border-width) var(--visual-picker-color);
    }

    .visual-picker-button svg {
      width: 1.25rem;
      height: 1.25rem;
      overflow: visible;
    }

    .visual-picker-button .visual-icon-primary,
    .visual-picker-button .visual-icon-secondary,
    .visual-picker-button .visual-icon-tertiary,
    .visual-picker-button .visual-icon-axis {
      fill: currentColor;
    }

    .visual-picker-button .visual-icon-primary {
      opacity: 1;
    }

    .visual-picker-button .visual-icon-secondary {
      opacity: 0.58;
    }

    .visual-picker-button .visual-icon-tertiary {
      opacity: 0.24;
    }

    .visual-picker-button .visual-icon-axis {
      opacity: 0.36;
    }

    .visual-picker-button .visual-icon-stroke,
    .visual-picker-button .visual-icon-band,
    .visual-picker-button .visual-icon-ring,
    .visual-picker-button .visual-icon-sunburst-outer,
    .visual-picker-button .visual-icon-gauge {
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
    }

    .visual-picker-button .visual-icon-stroke {
      stroke-width: 2.25;
    }

    .visual-picker-button .visual-icon-stroke-thin {
      stroke-width: 1.5;
    }

    .visual-picker-button .visual-icon-band {
      stroke-width: 4;
    }

    .visual-picker-button .visual-icon-ring,
    .visual-picker-button .visual-icon-sunburst-outer {
      stroke-width: 4.5;
    }

    .visual-picker-button .visual-icon-gauge {
      stroke-width: 3.5;
    }

    .visual-picker-button .visual-icon-cutout {
      fill: var(--lv-bg-panel-muted);
    }

    .visual-reference-link {
      margin-left: auto;
      color: var(--lv-fg-accent);
      font: var(--lv-type-caption);
      text-decoration: none;
    }

    .visual-reference-link:hover {
      text-decoration: underline;
    }

    .inspector-tabs {
      display: grid;
      grid-template-columns: 1fr 1fr;
      padding: 0 var(--base-size-12);
      border-bottom: var(--lv-border-muted);
    }

    .inspector-tab {
      min-height: var(--control-medium-size);
      border: 0;
      border-bottom: 2px solid transparent;
      border-radius: 0;
      color: var(--lv-fg-muted);
      background: transparent;
    }

    .inspector-tab[aria-selected='true'] {
      border-bottom-color: var(--lv-data-3);
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
    }

    .inspector-panel {
      display: grid;
      gap: var(--base-size-8);
      padding: var(--base-size-12);
    }

    .field-wells {
      display: grid;
      gap: var(--base-size-8);
    }

    .visual-requirements {
      margin: 0;
      padding: var(--base-size-6) var(--base-size-8);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
    }

    .property-heading {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
    }

    .builder-disclosure {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-8);
    }

    .builder-disclosure summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      cursor: pointer;
    }

    .builder-disclosure summary:has(.disclosure-count) {
      display: list-item;
    }

    .disclosure-count {
      float: right;
      min-width: 1.25rem;
      border-radius: var(--lv-radius-full);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font-weight: var(--base-text-weight-normal);
      text-align: center;
    }

    .builder-disclosure-content {
      padding-top: var(--base-size-8);
    }

    .interaction-targets {
      display: grid;
      gap: var(--base-size-8);
      margin: 0;
      padding: 0;
      border: 0;
    }

    .interaction-target {
      display: grid;
      gap: var(--base-size-6);
    }

    .interaction-target-title {
      overflow: hidden;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .interaction-effects {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: var(--base-size-4);
    }

    .interaction-effect {
      display: grid;
      place-items: center;
      min-height: var(--control-small-size);
      box-sizing: border-box;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel);
      font: var(--lv-type-caption);
      cursor: pointer;
    }

    .interaction-effect:has(input:focus-visible) {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 1px;
    }

    .interaction-effect[data-effect='filter'][data-selected='true'] {
      border-color: var(--lv-data-2);
      color: var(--lv-data-2);
      background: var(--lv-data-2-muted);
    }

    .interaction-effect[data-effect='highlight'][data-selected='true'] {
      border-color: var(--lv-data-3);
      color: var(--lv-data-3);
      background: var(--lv-data-3-muted);
    }

    .interaction-effect[data-effect='none'][data-selected='true'] {
      border-color: var(--lv-line-emphasis);
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel-muted);
    }

    .interaction-effect input {
      position: absolute;
      width: 1px;
      height: 1px;
      opacity: 0;
      pointer-events: none;
    }

    .field-well {
      display: grid;
      gap: var(--base-size-6);
    }

    .field-well-label {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
    }

    .field-well-label span:last-child {
      font-weight: var(--base-text-weight-normal);
    }

    .field-well-target {
      display: grid;
      min-height: var(--control-large-size);
      gap: var(--base-size-4);
      align-content: center;
      box-sizing: border-box;
      border: 1px dashed var(--lv-line-default);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-6);
      background: var(--lv-bg-panel-muted);
    }

    .field-well-target:hover,
    .field-well-target:focus-within {
      border-color: var(--lv-data-2);
      background: var(--lv-data-2-muted);
    }

    .field-well-target[data-field-drop='compatible'] {
      border-color: var(--lv-data-2);
      background: var(--lv-data-2-muted);
    }

    .field-well-target[data-field-drop='incompatible'] {
      opacity: 0.55;
    }

    .field-pill,
    .field-token {
      display: flex;
      min-height: var(--control-small-size);
      align-items: center;
      gap: var(--base-size-6);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: 0 var(--base-size-8);
      background: var(--lv-bg-panel);
      font: var(--lv-type-body-compact);
    }

    .field-token-label {
      min-width: 0;
      flex: 1 1 auto;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-token-actions {
      display: flex;
      flex: 0 0 auto;
      align-items: center;
      gap: var(--base-size-2);
    }

    .field-token-action {
      display: grid;
      width: 1.5rem;
      min-width: 1.5rem;
      min-height: 1.5rem;
      place-items: center;
      border-color: transparent;
      padding: 0;
      color: var(--lv-fg-muted);
      background: transparent;
      font: var(--lv-type-caption);
    }

    .field-token-action:hover:not(:disabled) {
      color: var(--lv-fg-default);
      background: var(--lv-bg-control-hover);
    }

    .field-token-action[data-field-action='remove']:hover:not(:disabled) {
      color: var(--lv-fg-danger, var(--lv-fg-default));
    }

    .field-token-action:disabled {
      opacity: 0.35;
    }

    .field-pill-kind {
      margin-left: auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .empty-well {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .format-placeholder {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-12);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
      line-height: 1.45;
    }

    .format-controls {
      display: grid;
      gap: var(--base-size-12);
    }

    .format-section {
      display: grid;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding-bottom: var(--base-size-12);
    }

    .format-section:last-child {
      border-bottom: 0;
      padding-bottom: 0;
    }

    .format-section h3 {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .format-text-field {
      display: grid;
      gap: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .format-text-field input[type='text'],
    .format-text-field input[type='number'],
    .format-text-field select {
      width: 100%;
      min-width: 0;
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-default);
      background: var(--lv-bg-control);
      font: var(--lv-type-body-compact);
    }

    .format-toggle {
      display: flex;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
    }

    .format-toggle input {
      width: 1rem;
      height: 1rem;
      margin: 0;
      accent-color: var(--lv-data-3);
    }

    .format-toggle:has(input:disabled) {
      color: var(--lv-fg-muted);
    }

    .page-layout-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: var(--base-size-8);
    }

    .page-format-summary {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-8);
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .visual,
    .filter-component {
      position: absolute;
      display: block;
      min-width: 4rem;
      min-height: 3rem;
      box-sizing: border-box;
      color: inherit;
      text-align: left;
      cursor: default;
    }

    .visual.has-preview {
      overflow: hidden;
    }

    .visual:focus-visible,
    .filter-component:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .visual[data-field-drop='compatible'] > .grid-stack-item-content {
      outline: 2px dashed var(--lv-data-2);
      outline-offset: -3px;
      background: var(--lv-data-2-muted);
    }

    .visual[data-field-drop='incompatible'] {
      opacity: 0.55;
    }

    .visual-title {
      overflow: hidden;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .visual-type {
      margin-top: 0.2rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .visual-preview {
      position: relative;
      display: block;
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    .visual-preview lv-visualization-host {
      display: block;
      width: 100%;
      height: 100%;
    }

    .visual-preview-empty {
      display: flex;
      min-height: 0;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: var(--base-size-4);
      padding: var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .visual-preview-empty strong {
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .filter-component > .grid-stack-item-content {
      grid-template-rows: minmax(0, 1fr) auto;
      gap: var(--base-size-8);
      background: var(--lv-bg-panel);
    }

    .filter-component:hover > .grid-stack-item-content {
      border-color: var(--lv-line-emphasis);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-bg-control-hover);
    }

    .filter-control-preview {
      display: grid;
      min-height: 0;
      align-content: center;
      gap: var(--base-size-6);
    }

    .filter-preview-input,
    .filter-preview-range > span {
      display: flex;
      min-width: 0;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: space-between;
      padding: 0 var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-input, var(--lv-bg-panel));
      font: var(--lv-type-caption);
    }

    .filter-preview-range {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: var(--base-size-6);
    }

    .filter-runtime-note {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .filter-component lv-slicer {
      display: block;
      min-width: 0;
      min-height: 0;
      overflow: auto;
    }

    .visual-empty {
      display: grid;
      place-items: center;
      min-height: 20rem;
      color: var(--lv-fg-muted);
      text-align: center;
    }

    .visual-empty strong {
      display: block;
      margin-bottom: 0.3rem;
      color: var(--lv-fg-default);
    }

    .properties-body {
      display: grid;
      gap: var(--base-size-12);
      padding: var(--base-size-12);
    }

    .property-group {
      display: grid;
      gap: 0.35rem;
    }

    .property-label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .property-value {
      font: var(--lv-type-body-compact);
    }

    .slot {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.5rem;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: var(--base-size-6);
      font: var(--lv-type-body-compact);
    }

    .slot-kind {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .diagnostics {
      display: grid;
      gap: 0.35rem;
    }

    .diagnostic {
      border-left: 3px solid var(--lv-fg-muted);
      padding: 0.35rem 0.5rem;
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
    }

    .diagnostic.error {
      border-color: var(--lv-fg-danger);
    }

    .diagnostic.warning {
      border-color: var(--lv-fg-warning);
    }

    .diagnostic.info {
      border-color: var(--lv-data-1);
    }

    .evidence {
      display: grid;
      gap: 0.3rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .secondary-details {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-8);
    }

    .secondary-details summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      cursor: pointer;
    }

    .secondary-details-content {
      display: grid;
      gap: var(--base-size-12);
      padding-top: var(--base-size-8);
    }

    .state {
      display: grid;
      place-items: center;
      min-height: 60svh;
      padding: 2rem;
      color: var(--lv-fg-muted);
      text-align: center;
    }

    .state strong {
      display: block;
      margin-bottom: 0.35rem;
      color: var(--lv-fg-default);
    }

    @media (max-width: 960px) {
      .toolbar {
        flex-wrap: wrap;
      }

      .title-wrap {
        min-width: 10rem;
      }

      .body {
        grid-template-columns: minmax(0, 1fr);
        grid-template-rows: minmax(0, 1fr) auto auto;
      }

      .right-dock {
        grid-column: 1;
        grid-row: 3;
        max-height: 19rem;
        grid-template-columns: var(--dock-filters-flex) var(--dock-visuals-flex) var(--dock-data-flex);
        grid-template-rows: minmax(0, 1fr);
        border-top: var(--lv-border-muted);
      }

      .filters-pane,
      .properties,
      .data-pane {
        max-height: none;
        border-top: 0;
      }

      .filters-pane {
        border-left: 0;
      }

      .data-pane {
        border-left: var(--lv-border-muted);
      }
    }

    @media (min-width: 961px) and (max-width: 1200px) {
      .body {
        grid-template-columns: minmax(0, 1fr) minmax(20rem, 23rem);
      }

      .right-dock {
        grid-template-columns: minmax(0, 1fr);
        grid-template-rows: var(--dock-filters-row) var(--dock-visuals-row) var(--dock-data-row);
      }

      .properties,
      .data-pane {
        border-top: var(--lv-border-muted);
      }
    }

    @media (max-width: 640px) {
      :host {
        height: 100%;
        max-height: 100svh;
        overflow-y: auto;
      }

      .builder {
        height: auto;
        min-height: auto;
      }

      .body {
        display: block;
      }

      .right-dock {
        display: block;
        grid-column: 1;
        max-height: none;
        border-top: 0;
      }

      .pane {
        max-height: none;
        border: 0;
        border-bottom: var(--lv-border-muted);
      }

      .properties {
        max-height: none;
      }

      .data-pane {
        border-left: 0;
      }

      .page-tab {
        max-width: 12rem;
      }

      .canvas-pane {
        min-height: 38rem;
      }

      /* The authored grid remains absolute on desktop. On a narrow viewport,
       * switch the canvas to a single-column flow so chart hosts get a real
       * viewport-sized box instead of an off-screen slice of the desktop
       * canvas. The surface stays full-bleed; the canvas scroller remains the
       * only overflow container. */
      .canvas-fit {
        width: 100% !important;
        height: auto !important;
      }

      .canvas {
        position: relative;
        inset: auto;
        display: grid;
        width: 100%;
        min-width: 0;
        min-height: 0;
        aspect-ratio: auto !important;
        transform: none !important;
        grid-template-columns: minmax(0, 1fr) !important;
        grid-auto-rows: auto;
        gap: var(--base-size-12);
      }

      .canvas .visual,
      .canvas .filter-component {
        position: relative;
        top: auto !important;
        right: auto !important;
        bottom: auto !important;
        left: auto !important;
        width: 100% !important;
        height: 16rem !important;
        min-width: 0;
        min-height: 12rem;
        order: var(--mobile-order, 0);
      }

      .canvas .visual[data-visual-type='kpi'] {
        height: 8rem !important;
        min-height: 8rem;
      }

      .canvas .filter-component {
        height: 8rem !important;
        min-height: 8rem;
      }

      .canvas .visual > .grid-stack-item-content,
      .canvas .filter-component > .grid-stack-item-content {
        position: relative;
        inset: auto !important;
        width: 100%;
        height: 100%;
      }

      .toolbar-actions {
        width: 100%;
        overflow-x: auto;
      }
    }
  `

  updated(): void {
    const builder = this.builder
    if (builder?.redirectTo) {
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
    this.syncGridStack(builder, page)
    this.syncCanvasViewport(page)
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
    const componentIDs = page ? [...page.visuals, ...(page.filterComponents ?? [])].map((component) => component.id) : []
    const layoutKey = builder && page
      ? `${this.revisionKey(builder)}:${page.id}:${componentIDs.join(',')}`
      : ''
    if (!canvas || !page || mobile) {
      this.destroyGridStack()
      this.gridIsMobile = mobile
      return
    }
    if (canvas !== this.gridElement || layoutKey !== this.gridLayoutKey || mobile !== this.gridIsMobile) {
      this.destroyGridStack()
      this.gridElement = canvas
      this.gridLayoutKey = layoutKey
      this.gridIsMobile = mobile
      this.gridStack = GridStack.init({
        column: Math.max(1, page.grid.columns || 12),
        // GridStack's cell height is the row pitch. Runtime canvas geometry
        // defines that pitch as the authored row plus its following gap.
        cellHeight: Math.max(1, (page.grid.rowHeight || 48) + (page.grid.gap || 0)),
        margin: Math.max(0, Math.round((page.grid.gap || 16) / 2)),
        animate: false,
        float: true,
        disableDrag: !builder?.capabilities.canEdit || this.commandPending,
        disableResize: !builder?.capabilities.canEdit || this.commandPending,
        draggable: { handle: '.component-drag-handle' },
        resizable: { handles: 'se', autoHide: true },
      }, canvas as GridItemHTMLElement)
      if (this.gridStack) {
        this.gridStack.on('dragstop', (_event: Event, element: GridItemHTMLElement) => this.onGridInteractionStop(element))
        this.gridStack.on('resizestop', (_event: Event, element: GridItemHTMLElement) => this.onGridInteractionStop(element))
        this.gridStack.on('drag', () => this.syncCanvasViewport(page))
        this.gridStack.on('resize', () => this.syncCanvasViewport(page))
        this.gridStack.on('change', (event: Event, nodes: GridStackNode[]) => this.onGridChange(event, nodes))
      }
    }
    this.setGridEditingEnabled(Boolean(builder?.capabilities.canEdit && !this.commandPending))
  }

  private destroyGridStack(): void {
    if (this.gridStack) this.gridStack.destroy(false)
    this.gridStack = null
    this.gridElement = null
    this.gridLayoutKey = ''
    this.gridCommitQueued = false
  }

  private setGridEditingEnabled(enabled: boolean): void {
    if (!this.gridStack) return
    this.gridStack.enableMove(enabled)
    this.gridStack.enableResize(enabled)
  }

  private onGridInteractionStop(_element: GridItemHTMLElement): void {
    this.gridInteractionMessage = 'Layout updated.'
    this.scheduleGridCommit()
  }

  private onGridChange(_event: Event, _nodes: GridStackNode[]): void {
    this.syncCanvasViewport(this.builder ? this.selectedPage(this.builder) : undefined)
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
      canvas?.style.removeProperty('--builder-canvas-width')
      canvas?.style.removeProperty('--builder-canvas-min-height')
      canvas?.style.removeProperty('min-height')
      return
    }
    if (scroll !== this.canvasViewportElement && typeof ResizeObserver !== 'undefined') {
      this.destroyCanvasViewportObserver()
      this.canvasViewportElement = scroll
      this.canvasResizeObserver = new ResizeObserver(() => this.syncCanvasViewport(this.builder ? this.selectedPage(this.builder) : undefined))
      this.canvasResizeObserver.observe(scroll)
    }
    const availableWidth = scroll.clientWidth
    if (availableWidth <= 0) return
    const logicalWidth = page.canvas.width > 0 ? page.canvas.width : builderCanvasDesktopWidth
    const minimumHeight = page.canvas.height > 0 ? page.canvas.height : builderCanvasMinimumHeight
    const scale = Math.min(1, availableWidth / logicalWidth)
    const occupiedRows = this.canvasOccupiedRows(page)
    const workingRows = occupiedRows + builderCanvasRunwayRows
    const rowHeight = Math.max(1, page.grid.rowHeight || 48)
    const gap = Math.max(0, page.grid.gap || 0)
    const padding = Math.max(0, page.grid.padding || 0)
    const contentHeight = workingRows > 0
      ? padding * 2 + workingRows * rowHeight + Math.max(0, workingRows - 1) * gap
      : padding * 2
    const logicalHeight = Math.max(minimumHeight, contentHeight)
    fit.style.setProperty('--builder-canvas-fitted-width', `${logicalWidth * scale}px`)
    fit.style.setProperty('--builder-canvas-fitted-height', `${logicalHeight * scale}px`)
    canvas.style.setProperty('--builder-canvas-scale', String(scale))
    canvas.style.setProperty('--builder-canvas-width', `${logicalWidth}px`)
    canvas.style.setProperty('--builder-canvas-min-height', `${minimumHeight}px`)
    canvas.style.setProperty('--builder-grid-columns', String(Math.max(1, page.grid.columns || 12)))
    canvas.style.setProperty('--builder-grid-row-pitch', `${rowHeight + gap}px`)
    canvas.style.minHeight = `${logicalHeight}px`
  }

  private canvasOccupiedRows(page: DashboardBuilderPageSignal): number {
    if (this.gridStack) {
      return this.gridStack.getGridItems().reduce((maximum, item) => {
        const node = item.gridstackNode
        return Math.max(maximum, (node?.y ?? 0) + (node?.h ?? 1))
      }, 0)
    }
    return [...page.visuals, ...(page.filterComponents ?? [])].reduce((maximum, component) => (
      Math.max(maximum, Math.max(1, component.placement.row) - 1 + Math.max(1, component.placement.rowSpan))
    ), 0)
  }

  private destroyCanvasViewportObserver(): void {
    this.canvasResizeObserver?.disconnect()
    this.canvasResizeObserver = null
    this.canvasViewportElement = null
  }

  private scheduleGridCommit(): void {
    if (this.gridCommitQueued || !this.gridStack || this.isMobileViewport() || this.commandPending || !this.builder?.capabilities.canEdit) return
    this.gridCommitQueued = true
    queueMicrotask(() => {
      this.gridCommitQueued = false
      this.commitGridPlacements()
    })
  }

  private commitGridPlacements(): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder || !page || !this.gridStack || this.isMobileViewport() || this.commandPending || !builder.capabilities.canEdit) return
    const nodes = new Map(this.gridStack.getGridItems().map((item) => [item.gridstackNode?.id || item.getAttribute('gs-id') || '', item.gridstackNode]))
    const components = [...page.visuals, ...(page.filterComponents ?? [])]
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
    if (placements.every((placement, index) => this.placementEqual(placement, components[index].placement))) return
    if (!this.gridInteractionMessage) this.gridInteractionMessage = 'Layout updated.'
    this.emitCommand('set_placements', { pageId: page.id, placements })
  }

  private placementEqual(left: GridPlacement, right: DashboardBuilderVisualSignal['placement']): boolean {
    return left.placement.column === right.col && left.placement.row === right.row && left.placement.columnSpan === right.colSpan && left.placement.rowSpan === right.rowSpan
  }

  get builder(): DashboardBuilderSignal | null {
    return this.signal<DashboardBuilderSignal | null>('builder', null)
  }

  get status(): DashboardStatus {
    return this.signal<DashboardStatus>('status', emptyStatus)
  }

  get builderVisuals(): Record<string, VisualizationEnvelope> {
    return this.visualizationDecoder.decodeAll(
      this.signal<Record<string, DashboardVisualizationSignal>>('builderVisuals', {}),
    )
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
      const value = JSON.parse(window.localStorage.getItem(builderPaneStorageKey) ?? '[]')
      if (!Array.isArray(value)) return
      const collapsed = new Set(value.filter((pane): pane is BuilderPane => pane === 'filters' || pane === 'visuals' || pane === 'data'))
      this.collapsedPanes = {
        filters: collapsed.has('filters'),
        visuals: collapsed.has('visuals'),
        data: collapsed.has('data'),
      }
    } catch {
      this.collapsedPanes = { ...defaultCollapsedPanes }
    }
  }

  private persistCollapsedPanes(): void {
    if (typeof window === 'undefined') return
    try {
      const collapsed = (Object.keys(this.collapsedPanes) as BuilderPane[]).filter((pane) => this.collapsedPanes[pane])
      window.localStorage.setItem(builderPaneStorageKey, JSON.stringify(collapsed))
    } catch {
      // Storage can be unavailable in hardened browser contexts. The current
      // session still keeps the pane state through this reactive property.
    }
  }

  private togglePane = (pane: BuilderPane): void => {
    this.collapsedPanes = { ...this.collapsedPanes, [pane]: !this.collapsedPanes[pane] }
    this.persistCollapsedPanes()
  }

  private rightDockStyle(): string {
    const size = (pane: BuilderPane, open: string) => this.collapsedPanes[pane] ? '2.75rem' : open
    const rowSize = (pane: BuilderPane) => this.collapsedPanes[pane] ? '3.5rem' : 'minmax(0, 1fr)'
    return [
      `--dock-filters-width:${size('filters', '12rem')}`,
      `--dock-visuals-width:${size('visuals', '14rem')}`,
      `--dock-data-width:${size('data', '12rem')}`,
      `--dock-filters-flex:${size('filters', 'minmax(0, 1fr)')}`,
      `--dock-visuals-flex:${size('visuals', 'minmax(0, 1fr)')}`,
      `--dock-data-flex:${size('data', 'minmax(0, 1fr)')}`,
      `--dock-filters-row:${rowSize('filters')}`,
      `--dock-visuals-row:${rowSize('visuals')}`,
      `--dock-data-row:${rowSize('data')}`,
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
    return html`
      <section class="builder" aria-label="Dashboard builder">
        ${this.renderTerminalFailure()}
        ${this.renderToolbar(builder)}
        <div class="body">
          ${this.renderCanvas(builder, page)}
          ${this.renderPageBar(builder, page)}
          <div class="right-dock" style=${this.rightDockStyle()}>
            ${this.renderFiltersPane(builder)}
            ${this.renderInspector(builder, page, visual)}
            ${this.renderDataPane(builder, visual)}
          </div>
        </div>
      </section>
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
    const hasMoreActions = builder.capabilities.canShare || builder.capabilities.canExport || builder.capabilities.canArchive || Boolean(this.forkHref)
    const appearanceColor = dashboardAppearanceColor(builder.appearance.color)
    return html`
      <header class="toolbar">
        ${this.backHref ? html`<a class="back" href=${this.backHref} aria-label="Back to dashboards">Back</a>` : html`<span class="back" aria-label="Back to dashboards">Back</span>`}
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
            <span>${this.titleCase(builder.visibility)} ${this.titleCase(builder.lifecycle)} · Revision ${builder.revision.number} · ${publishing ? 'Publishing…' : this.commandPending ? 'Saving…' : this.saveLabel(builder)}</span>
          </div>
        </div>
        <div class="toolbar-actions" aria-label="Builder actions">
          <button type="button" class="icon-action" data-builder-action="undo" aria-label="Undo" title="Undo (Ctrl/⌘ Z)" ?disabled=${!builder.capabilities.canEdit || this.commandPending || this.undoStack.length === 0} @click=${this.undo}>${lucideIcon(Undo2, { size: 16, strokeWidth: 2 })}<span class="sr-only">Undo</span></button>
          <button type="button" class="icon-action" data-builder-action="redo" aria-label="Redo" title="Redo (Ctrl/⌘ Shift Z)" ?disabled=${!builder.capabilities.canEdit || this.commandPending || this.redoStack.length === 0} @click=${this.redo}>${lucideIcon(Redo2, { size: 16, strokeWidth: 2 })}<span class="sr-only">Redo</span></button>
          ${this.renderThemeToggle()}
          ${hasMoreActions ? html`
            <details class="more-actions">
              <summary aria-label="More dashboard actions">More</summary>
              <div class="more-menu" aria-label="More dashboard actions">
                ${this.forkHref ? html`<a class="button" href=${this.forkHref}>Make a copy</a>` : nothing}
                ${builder.capabilities.canShare ? html`<button @click=${this.toggleVisibility} aria-label="Toggle dashboard visibility">${builder.visibility === 'organization' ? 'Make private' : 'Share with organization'}</button>` : nothing}
                ${builder.capabilities.canExport
                  ? this.exportYAMLHref ? html`<a class="button" href=${this.exportYAMLHref} download>Export YAML</a>` : html`<button disabled title="YAML export is not available yet">Export YAML</button>`
                  : nothing}
                ${builder.capabilities.canArchive ? html`<button type="button" class="archive-action" data-builder-action="archive" @click=${this.archiveDashboard}>${lucideIcon(Archive, { size: 14, strokeWidth: 2 })}<span>Archive dashboard</span></button>` : nothing}
              </div>
            </details>` : nothing}
          ${builder.capabilities.canPublish ? html`<button type="button" class="primary" data-builder-action="publish" title=${publishTitle} ?disabled=${publishDisabled} @click=${this.publish}>${publishLabel}</button>` : nothing}
        </div>
      </header>
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
      this.commandPending = false
      this.activeCommandAction = ''
      this.pendingHistorySnapshot = null
      this.setGridEditingEnabled(Boolean(this.builder?.capabilities.canEdit))
      this.requestUpdate()
      return
    }
    const commandFailure = browserCommandFailure(event, 'Dashboard builder action')
    if (!commandFailure) return
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
    this.setGridEditingEnabled(Boolean(this.builder?.capabilities.canEdit))
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
                @click=${(event: MouseEvent) => { if (item.id === page?.id) this.openPageSettings(item, event) }}
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
        ${builder.capabilities.canAddPage ? html`<button type="button" class="page-add" @click=${this.addPage} aria-label="Add page" title="Add page">${lucideIcon(Plus, { size: 16, strokeWidth: 2 })}<span class="sr-only">+</span></button>` : nothing}
      </footer>
    `
  }

  private renderFieldBrowser(builder: DashboardBuilderSignal, visual: DashboardBuilderVisualSignal | undefined) {
    const datasets = builder.semanticModel.datasets ?? []
    const catalog = this.filteredCatalog(this.semanticCatalog(datasets))
    const visibleCatalog = this.fieldFilter === 'all' ? catalog : catalog.filter((item) => item.group === this.fieldFilter)
    const supported = visibleCatalog.filter((item) => this.addingSlicer
      ? this.fieldSupportsFilter(item.field)
      : visual
        ? Boolean(this.fieldUsedIn(item.field, visual) || this.fieldCompatibleWithVisual(item.field, visual))
        : this.fieldDataTypeSupported(item.field))
    const unsupported = visibleCatalog.filter((item) => !supported.includes(item))
    const recordColumns = unsupported.filter((item) => item.field.roles?.includes('detail'))
    const unavailable = unsupported.filter((item) => !recordColumns.includes(item))
    const groups: Array<Exclude<BuilderFieldFilter, 'all'>> = ['metric', 'dimension', 'time']
    const collapsed = this.collapsedPanes.data
    return html`
      <div class="field-browser">
        <div class="field-browser-header">
          <div class="pane-heading-row">
            <div class="pane-title-group">
              <span class="pane-title-icon">${lucideIcon(Database, { size: 16, strokeWidth: 2 })}</span>
              <h2 id="builder-data-heading" class="pane-title">Data</h2>
            </div>
            ${this.renderPaneToggle('data', 'Data pane', 'builder-data-content')}
          </div>
          <div class="pane-header-details" ?hidden=${collapsed}>
            <label>
              <span class="sr-only">Search fields</span>
              <input class="search" type="search" aria-label="Search fields" placeholder="Search measures and dimensions" .value=${this.fieldQuery} @input=${this.onFieldQuery} />
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

  private renderDataPane(builder: DashboardBuilderSignal, visual: DashboardBuilderVisualSignal | undefined) {
    return html`
      <aside class="pane data-pane" data-collapsed=${this.collapsedPanes.data} aria-labelledby="builder-data-heading">
        ${this.renderFieldBrowser(builder, visual)}
      </aside>
    `
  }

  private renderFiltersPane(builder: DashboardBuilderSignal) {
    const filter = this.selectedBuilderFilter(builder)
    const filters = builder.filters ?? []
    const page = this.selectedPage(builder)
    const visual = page ? this.selectedVisual(page, builder) : undefined
    const grouped = this.groupFiltersByScope(filters, page, visual)
    const dimensions = this.semanticCatalog(builder.semanticModel.datasets ?? []).filter((item) => this.fieldSupportsFilter(item.field))
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
          <label>
            <span class="sr-only">Add filter</span>
            <select class="filter-add-select" aria-label="Add filter" ?disabled=${!builder.capabilities.canEdit || this.commandPending} @change=${this.addFilterFromSelect}>
              <option value="">+ Add filter</option>
              ${dimensions.map((item) => html`<option value=${item.field.id} ?disabled=${filters.some((candidate) => candidate.dimension === item.field.id)}>${item.field.label}</option>`)}
            </select>
          </label>
          ${filters.length === 0 ? html`<p class="filter-pane-empty">No filters yet</p>` : nothing}
          ${grouped.visual.length > 0 ? this.renderFilterScopeGroup('This visual', grouped.visual, filter) : nothing}
          ${grouped.page.length > 0 ? this.renderFilterScopeGroup('This page', grouped.page, filter) : nothing}
          ${grouped.report.length > 0 ? this.renderFilterScopeGroup('All pages', grouped.report, filter) : nothing}
          ${grouped.custom.length > 0 ? this.renderFilterScopeGroup('Custom', grouped.custom, filter) : nothing}
          ${filter ? this.renderFilterEditor(builder, filter) : nothing}
        </div>
      </aside>
    `
  }

  private renderFilterEditor(builder: DashboardBuilderSignal, filter: DashboardBuilderFilterSignal) {
    const editable = builder.capabilities.canEdit && !this.commandPending
    const page = this.selectedPage(builder)
    const placedComponent = page?.filterComponents?.find((component) => component.filterId === filter.id)
    const visual = page ? this.selectedVisual(page, builder) : undefined
    const scope = this.filterScope(filter, page, visual)
    return html`
      <section class="filter-editor" aria-label=${`Configure ${filter.label} filter`}>
        <div class="filter-scope-options" role="radiogroup" aria-label="Filter scope">
          <label class="filter-scope-option" title="Apply on all pages"><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'report'} ?disabled=${!editable} @change=${() => this.setFilterScope(filter, 'report')} /><span>All pages</span></label>
          <label class="filter-scope-option" title=${page ? `Apply on ${page.title}` : 'Select a page first'}><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'page'} ?disabled=${!editable || !page} @change=${() => page && this.setFilterScope(filter, 'page', page)} /><span>This page</span></label>
          <label class="filter-scope-option" title=${visual ? `Apply only to ${visual.title}` : 'Select a visual first'}><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'visual'} ?disabled=${!editable || !visual || !page} @change=${() => page && visual && this.setFilterScope(filter, 'page', page, [visual.id])} /><span>Visual</span></label>
        </div>
        ${scope === 'custom' ? html`<p class="filter-scope-empty">Custom targeting from dashboard code</p>` : nothing}
        <details class="filter-settings">
          <summary>Settings</summary>
          <div class="filter-settings-body">
            <label>Label
              <input type="text" maxlength="128" .value=${filter.label} ?disabled=${!editable} @change=${(event: Event) => this.updateFilter(filter, { label: (event.currentTarget as HTMLInputElement).value.trim() || filter.label })} />
            </label>
            <label>Control
              <select .value=${filter.controlType} ?disabled=${!editable} @change=${(event: Event) => this.updateFilter(filter, { controlType: (event.currentTarget as HTMLSelectElement).value as BuilderFilterControl })}>
                ${this.filterControlChoices(filter).map(([value, label]) => html`<option value=${value}>${label}</option>`)}
              </select>
            </label>
            <label>URL parameter
              <input type="text" maxlength="64" placeholder="Optional" .value=${filter.urlParameter ?? ''} ?disabled=${!editable} @change=${(event: Event) => this.updateFilter(filter, { urlParameter: (event.currentTarget as HTMLInputElement).value.trim() })} />
            </label>
            <label class="filter-toggle"><span>Readers can edit</span><input type="checkbox" .checked=${filter.readerEditable} ?disabled=${!editable} @change=${(event: Event) => this.updateFilter(filter, { readerEditable: (event.currentTarget as HTMLInputElement).checked })} /></label>
            <label class="filter-toggle"><span>Required</span><input type="checkbox" .checked=${filter.required} ?disabled=${!editable} @change=${(event: Event) => this.updateFilter(filter, { required: (event.currentTarget as HTMLInputElement).checked })} /></label>
          </div>
        </details>
        <div class="filter-editor-actions">
          ${page ? html`
            <button type="button" class="filter-placement-action" ?disabled=${!editable} @click=${() => placedComponent ? this.removeFilterComponent(page, placedComponent) : this.addFilterComponent(page, filter)}>
              ${placedComponent ? 'Remove from canvas' : 'Add to canvas'}
            </button>
          ` : html`<span></span>`}
          <button type="button" class="filter-remove" ?disabled=${!editable} @click=${() => this.removeFilter(filter)}>Delete</button>
        </div>
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
          ${repeat(filters, (item) => item.id, (item) => this.renderFilterPanePreview(item, selected?.id === item.id, page, visual))}
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
    return this.catalogEntities(fields, datasets).map((entity) => html`
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
          ${this.catalogEntities(fields, datasets).map((entity) => html`
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
        ${usedIn ? html`<span class="field-used">✓ ${usedIn}</span>` : nothing}
      </button>
    `
  }

  private renderCanvas(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined) {
    if (!page) {
      return html`<section class="canvas-pane" aria-label="Dashboard canvas"><div class="state"><div><strong>No pages yet</strong><span>Create a page to start designing this dashboard.</span>${builder.capabilities.canAddPage ? html`<div><button @click=${this.addPage} aria-label="Add page">Add page</button></div>` : nothing}</div></div></section>`
    }
    const width = Math.max(12, page.grid.columns || 12)
    const pageFormatting = this.inspectorTab === 'format'
      && !this.effectiveVisualID(builder, page)
      && !this.selectedFilterComponentID
    const previews = this.builderVisuals
    return html`
      <section class="canvas-pane" aria-label="Dashboard canvas">
        <div class="canvas-scroll">
          <p id="dashboard-builder-grid-help" class="sr-only">Focus a canvas component. Use Alt plus an arrow key to move it one grid cell. Use Alt plus Shift plus an arrow key to resize it.</p>
          <div class="canvas-fit">
            <div class="canvas grid-stack" data-field-dragging=${this.draggedFieldID ? 'true' : 'false'} data-grid-guides=${this.draggedFieldID || pageFormatting ? 'true' : 'false'} aria-describedby="dashboard-builder-grid-help" style=${`grid-template-columns: repeat(${width}, 1fr);`} @click=${this.deselectVisualFromCanvas} @dragover=${this.allowFieldDrop} @drop=${this.dropField}>
              ${this.draggedFieldID ? html`<div class="canvas-field-drop-hint" role="status">Drop on the canvas to create a ${this.visualLabel(this.recommendedVisualForDraggedField(builder), builder)} visual</div>` : nothing}
              ${page.visuals.length === 0 && (page.filterComponents?.length ?? 0) === 0
                ? html`<div class="visual-empty"><div><strong>This page is empty</strong><span>Choose a visual or place a report-filter slicer to begin.</span></div></div>`
                : html`${repeat(page.visuals, (visual) => visual.id, (visual) => this.renderVisual(visual, page, previews))}${repeat(page.filterComponents ?? [], (component) => component.id, (component) => this.renderFilterComponent(component, page))}`}
            </div>
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
    const mobileOrder = this.mobileVisualOrder(visual, page)
    const columns = Math.max(1, page.grid.columns || 12)
    const left = `${Math.max(0, visual.placement.col - 1) * (100 / columns)}%`
    const top = `${Math.max(0, visual.placement.row - 1) * (page.grid.rowHeight || 40)}px`
    const width = `${Math.max(1, visual.placement.colSpan) * (100 / columns)}%`
    const height = `${Math.max(1, visual.placement.rowSpan) * (page.grid.rowHeight || 40)}px`
    const draggedField = this.draggedFieldFromBuilder(this.builder)
    const fieldDrop = draggedField ? (this.fieldCompatibleWithVisual(draggedField, visual) ? 'compatible' : 'incompatible') : ''
    const requirementMessages = this.visualRequirementMessages(visual)
    const previewIssue = this.visualPreviewErrorMessage(visual)
    const previewUnavailable = requirementMessages.length > 0 || Boolean(previewIssue) || Boolean(this.builder?.preview.error && !this.builder.preview.active)
    const preview = previewUnavailable ? undefined : previewCandidate
    const previewHasHeader = preview ? this.visualPreviewHasHeader(preview) : false
    const fallbackMessage = this.builder?.preview.error ? 'Preview unavailable. Try again after the draft is valid.' : 'Add fields to preview.'
    return html`
      <div class="visual grid-stack-item ${preview ? 'has-preview' : ''}" data-visual-type=${visualType} data-selected=${selected} data-field-drop=${fieldDrop || nothing} gs-id=${visual.id} gs-x=${Math.max(0, visual.placement.col - 1)} gs-y=${Math.max(0, visual.placement.row - 1)} gs-w=${Math.max(1, visual.placement.colSpan)} gs-h=${Math.max(1, visual.placement.rowSpan)} role="group" tabindex="0" aria-label=${selected ? `${visual.title}, selected dashboard visual` : `${visual.title}, dashboard visual`} aria-describedby="dashboard-builder-grid-help" style=${`left:${left};top:${top};width:${width};height:${height};--mobile-order:${mobileOrder}`} @click=${(event: MouseEvent) => { event.stopPropagation(); this.selectVisualFromPointer(visual.id) }} @keydown=${(event: KeyboardEvent) => this.selectVisualOnKey(event, visual.id)} @dragover=${this.allowFieldDrop} @drop=${(event: DragEvent) => this.dropFieldOnVisual(event, visual.id)}>
        <div class="grid-stack-item-content">
          ${preview
            ? html`<span class="visual-preview"><lv-visualization-host ?authoring=${previewHasHeader} .envelope=${preview}>${previewHasHeader ? html`<span slot="authoring-drag-handle" class="visual-drag-header component-drag-handle" title="Drag to move ${visual.title}" @pointerdown=${() => this.selectVisualFromPointer(visual.id)}>${visual.title}</span>` : nothing}</lv-visualization-host>${previewHasHeader ? nothing : this.renderComponentDragGrip(visual.title, () => this.selectVisualFromPointer(visual.id))}</span>`
            : html`<span class="visual-drag-header component-drag-handle" title="Drag to move ${visual.title}" @pointerdown=${() => this.selectVisualFromPointer(visual.id)}>${visual.title}</span><span class="visual-preview-empty" role="status"><strong>${this.visualLabel(visualType)} preview unavailable</strong>${requirementMessages.length > 0 ? requirementMessages.map((message) => html`<span>${message}</span>`) : html`<span>${previewIssue || fallbackMessage}</span>`}</span><span class="visual-type">${visualType} · ${visual.slots.length} field slots</span>`}
        </div>
      </div>
    `
  }

  private renderFilterComponent(component: DashboardBuilderFilterComponentSignal, page: DashboardBuilderPageSignal) {
    const selected = component.id === this.selectedFilterComponentID
    const mobileOrder = this.mobileComponentOrder(component.id, component.placement, page)
    const columns = Math.max(1, page.grid.columns || 12)
    const left = `${Math.max(0, component.placement.col - 1) * (100 / columns)}%`
    const top = `${Math.max(0, component.placement.row - 1) * (page.grid.rowHeight || 40)}px`
    const width = `${Math.max(1, component.placement.colSpan) * (100 / columns)}%`
    const height = `${Math.max(1, component.placement.rowSpan) * (page.grid.rowHeight || 40)}px`
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
      <div class="filter-component grid-stack-item" data-selected=${selected} gs-id=${component.id} gs-x=${Math.max(0, component.placement.col - 1)} gs-y=${Math.max(0, component.placement.row - 1)} gs-w=${Math.max(1, component.placement.colSpan)} gs-h=${Math.max(1, component.placement.rowSpan)} role="group" tabindex="0" aria-label=${selected ? `${component.label}, selected dashboard slicer` : `${component.label}, dashboard slicer`} aria-describedby="dashboard-builder-grid-help" style=${`left:${left};top:${top};width:${width};height:${height};--mobile-order:${mobileOrder}`} @click=${(event: MouseEvent) => { event.stopPropagation(); this.selectFilterComponent(component) }} @keydown=${(event: KeyboardEvent) => this.selectFilterComponentOnKey(event, component)}>
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
    return html`<div class="filter-control-preview" aria-label=${`${this.filterControlLabel(component.controlType)} preview`}><div class="filter-preview-input"><span>${preview}</span><span aria-hidden="true">${component.controlType === 'text' ? '⌕' : '⌄'}</span></div></div>`
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

  private renderInspector(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined) {
    const collapsed = this.collapsedPanes.visuals
    const slicer = page?.filterComponents?.find((component) => component.id === this.selectedFilterComponentID)
    const slicerFilter = slicer ? (builder.filters ?? []).find((filter) => filter.id === slicer.filterId) : undefined
    const slicerActive = Boolean(this.addingSlicer || slicer)
    const formattingPage = Boolean(page && !visual && !slicerActive && this.inspectorTab === 'format')
    return html`
      <aside class="pane properties visual-builder" data-collapsed=${collapsed} aria-label=${formattingPage ? 'Page properties' : 'Visual builder'}>
        <div class="pane-header">
          <div class="inspector-heading">
            <div class="inspector-title">
              <span class="pane-title-icon">${lucideIcon(slicerActive ? ListFilter : ChartColumn, { size: 16, strokeWidth: 2 })}</span>
              <h2 class="pane-title">${collapsed ? 'Visuals' : this.addingSlicer ? 'Add a slicer' : slicer ? slicer.label : visual ? visual.title : formattingPage ? page?.title : page ? 'Add a visual' : 'Visual builder'}</h2>
              ${visual && !collapsed ? html`<span class="visual-type-badge">${this.titleCase(this.visualTypeForRender(visual))}</span>` : nothing}
              ${slicer && !collapsed ? html`<span class="visual-type-badge">Slicer</span>` : nothing}
              ${formattingPage && !collapsed ? html`<span class="visual-type-badge">Page</span>` : nothing}
            </div>
            ${this.renderPaneToggle('visuals', 'Visuals pane', 'builder-visuals-content')}
          </div>
          <p class="sr-only" role="status" aria-live="polite">${this.visualActionMessage}</p>
        </div>
        <div id="builder-visuals-content" class="pane-content" ?hidden=${collapsed}>
          <div class="inspector-tabs" role="tablist" aria-label="Visual configuration">
            <button class="inspector-tab" role="tab" data-inspector-tab="build" aria-selected=${this.inspectorTab === 'build'} @click=${() => { this.inspectorTab = 'build' }}>Build</button>
            <button class="inspector-tab" role="tab" data-inspector-tab="format" aria-selected=${this.inspectorTab === 'format'} ?disabled=${slicerActive} @click=${() => { this.inspectorTab = 'format' }}>Format</button>
          </div>
          ${this.inspectorTab === 'build'
            ? html`<div class="inspector-panel" role="tabpanel" aria-label=${slicerActive ? 'Build slicer' : 'Build visual'}>
                ${this.renderVisualPicker(builder, page, visual)}
                ${slicerActive ? this.renderSlicerFieldWell(slicerFilter) : visual ? html`${this.renderFieldWells(visual)}${this.renderInteractionEditor(builder, page, visual)}` : nothing}
              </div>`
            : html`<div class="inspector-panel" role="tabpanel" aria-label=${visual ? 'Format visual' : 'Format page'}>
                ${visual ? this.renderVisualFormatControls(visual) : this.renderPageProperties(page)}
              </div>`}
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
    return builder?.visualCatalog?.find((entry) => entry.type === type)
  }

  private visualLabel(type: string, builder = this.builder): string {
    return this.visualCatalogEntry(type, builder)?.label ?? this.titleCase(type)
  }

  private renderFieldWells(visual: DashboardBuilderVisualSignal) {
    const entry = this.visualCatalogEntry(this.visualTypeForRender(visual))
    const roles = (entry?.roles ?? ['dimension', 'metric']).filter((role): role is BuilderFieldRole => role === 'dimension' || role === 'metric' || role === 'detail')
    const requirements = this.visualRequirementMessages(visual)
    const previewIssue = this.visualPreviewErrorMessage(visual)
    const ready = requirements.length === 0 && !previewIssue
    return html`
      <section class="property-group" aria-label="Field wells">
        <div class="property-heading"><span class="property-label">Fields</span></div>
        ${ready ? nothing : html`<div class="visual-requirements" role="status"><span>${requirements.length > 0 ? this.visualRequirementSummary(requirements) : previewIssue}</span></div>`}
        <div class="field-wells">${roles.map((role) => this.renderFieldWell(visual, role))}</div>
      </section>
    `
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
    const draggedField = this.draggedFieldFromBuilder(this.builder)
    const fieldDrop = draggedField ? (this.fieldCompatibleWithRole(draggedField, role) && this.roleHasCapacity(visual, role) ? 'compatible' : 'incompatible') : ''
    return html`
      <section class="field-well">
        <div class="field-well-label"><span>${label}</span>${slots.length > 0 ? html`<span>${slots.length}</span>` : nothing}</div>
        <div class="field-well-target" data-drop-well=${role} data-field-drop=${fieldDrop || nothing} tabindex="0" aria-label=${`Drop ${role} field in ${label}`} @dragover=${this.allowFieldDrop} @drop=${(event: DragEvent) => this.dropFieldOnRole(event, role)}>
          ${slots.length === 0
            ? html`<span class="empty-well">Drop ${role === 'metric' ? 'a measure' : role === 'detail' ? 'a column' : 'a dimension'}</span>`
            : slots.map((slot, index) => this.renderFieldToken(visual, role, slot, index, slots.length))}
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
          <button type="button" class="field-token-action" data-field-action="remove" aria-label=${`Remove ${label} field`} title="Remove field" ?disabled=${!editable} @click=${() => this.removeField(visual, role, fieldID, label)}>×</button>
          <button type="button" class="field-token-action" data-field-action="move-up" aria-label=${`Move ${label} field up`} title="Move field up" ?disabled=${!editable || index === 0} @click=${() => this.moveField(visual, role, fieldID, 'up', label)}>↑</button>
          <button type="button" class="field-token-action" data-field-action="move-down" aria-label=${`Move ${label} field down`} title="Move field down" ?disabled=${!editable || index === count - 1} @click=${() => this.moveField(visual, role, fieldID, 'down', label)}>↓</button>
          <button type="button" class="field-token-action" data-field-action="move-role" aria-label=${alternateRole ? `Move ${label} field to ${this.fieldWellLabel(visual, alternateRole)}` : `Move ${label} field to another role`} title=${alternateRole ? `Move to ${this.fieldWellLabel(visual, alternateRole)}` : 'No compatible role for this field'} ?disabled=${!editable || !alternateRole} @click=${() => alternateRole && this.moveFieldRole(visual, role, alternateRole, fieldID, label)}>⇄</button>
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
      <section class="format-controls" aria-label="Visual formatting">
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
        ${formatOptions.length === 0 ? html`<p class="pane-hint">This presentation has no simple Format controls. Configure its governed bindings in Build or dashboards-as-code.</p>` : nothing}
        ${reference ? html`<a class="visual-reference-link" href=${reference.referenceHref}>View every ${reference.label} option in the visual reference</a>` : nothing}
      </section>
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

  private semanticCatalog(datasets: DashboardBuilderDatasetSignal[]): BuilderCatalogField[] {
    const catalog = new Map<string, BuilderCatalogField>()
    for (const dataset of datasets) {
      const datasetFields = new Map<string, DashboardBuilderFieldSignal>()
      for (const field of dataset.fields) {
        const rolesKey = [...(field.roles ?? [])].sort().join(',')
        const labelKey = `${field.kind}:${rolesKey}:${field.label.trim().toLocaleLowerCase()}`
        const existing = datasetFields.get(labelKey)
        if (!existing || this.fieldCatalogScore(field) > this.fieldCatalogScore(existing)) datasetFields.set(labelKey, field)
      }
      for (const field of datasetFields.values()) {
        const rolesKey = [...(field.roles ?? [])].sort().join(',')
        const datasetKey = field.roles?.includes('detail') ? (field.datasetId ?? dataset.id) : ''
        const key = `${field.kind}:${rolesKey}:${field.id}:${datasetKey}`
        const existing = catalog.get(key)
        if (existing) {
          if (!existing.datasets.some((item) => item.id === dataset.id)) existing.datasets.push({ id: dataset.id, title: this.businessGroupTitle(dataset) })
          continue
        }
        catalog.set(key, {
          field,
          datasets: [{ id: dataset.id, title: this.businessGroupTitle(dataset) }],
          group: this.fieldCatalogGroup(field),
        })
      }
    }
    return Array.from(catalog.values()).sort((left, right) => {
      const groupOrder = { metric: 0, dimension: 1, time: 2 }
      const byGroup = groupOrder[left.group] - groupOrder[right.group]
      return byGroup || left.field.label.localeCompare(right.field.label)
    })
  }

  private catalogEntities(fields: BuilderCatalogField[], datasets: DashboardBuilderDatasetSignal[]): BuilderCatalogEntity[] {
    const datasetOrder = new Map(datasets.map((dataset, index) => [dataset.id, index]))
    const entities = new Map<string, BuilderCatalogEntity>()
    for (const item of fields) {
      const dataset = item.datasets.find((candidate) => candidate.id === item.field.datasetId) ?? item.datasets[0]
      const id = dataset?.id ?? 'semantic-model'
      const title = dataset?.title ?? this.builder?.semanticModel.title ?? 'Semantic model'
      const entity = entities.get(id)
      if (entity) entity.fields.push(item)
      else entities.set(id, { id, title, fields: [item] })
    }
    return Array.from(entities.values()).sort((left, right) => {
      const leftOrder = datasetOrder.get(left.id) ?? Number.MAX_SAFE_INTEGER
      const rightOrder = datasetOrder.get(right.id) ?? Number.MAX_SAFE_INTEGER
      return leftOrder - rightOrder || left.title.localeCompare(right.title)
    })
  }

  private fieldCatalogScore(field: DashboardBuilderFieldSignal): number {
    let score = 0
    if (field.dataType.trim().toLowerCase() !== 'unknown') score += 4
    if (!field.id.includes('.')) score += 2
    if (field.description?.trim()) score += 1
    return score
  }

  private businessGroupTitle(dataset: DashboardBuilderDatasetSignal): string {
    const title = dataset.title.trim()
    const source = title.length > 0 && title.length <= 32 && !/[.!?]$/.test(title) ? title : dataset.id
    const normalized = source.replace(/[_-]+/g, ' ').replace(/\s+/g, ' ').trim()
    return normalized ? normalized.charAt(0).toLocaleUpperCase() + normalized.slice(1) : 'Semantic model'
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

  private fieldCatalogGroup(field: DashboardBuilderFieldSignal): Exclude<BuilderFieldFilter, 'all'> {
    if (field.kind === 'metric') return 'metric'
    const dataType = field.dataType.toLowerCase()
    return dataType.includes('date') || dataType.includes('time') || dataType.includes('timestamp') ? 'time' : 'dimension'
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
    const entry = this.visualCatalogEntry(this.visualTypeForRender(visual))
    if (!entry) return []
    const messages: string[] = []
    for (const requirement of entry.roleLimits ?? []) {
      const count = visual.slots.filter((slot) => this.slotRole(slot) === requirement.role).length
      if (count < requirement.minimum) {
        const missing = requirement.minimum - count
        messages.push(`Add ${missing} ${this.requirementRoleLabel(visual, requirement.role, missing)} to preview.`)
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

  private visualPreviewErrorMessage(visual: DashboardBuilderVisualSignal): string {
    const message = visual.previewError?.trim() ?? ''
    if (!message) return ''
    const marker = `visual "${this.visualSignalID(visual)}":`
    const markerIndex = message.indexOf(marker)
    const detail = (markerIndex >= 0 ? message.slice(markerIndex + marker.length) : message)
      .replace(/^\s*(query|presentation|references|result aliases|interactions|geographic delivery|calculations|IR|definition):\s*/i, '')
      .trim()
    if (!detail) return 'Preview unavailable for this field combination.'
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
    if (type === 'kpi') return 'Value'
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

  private publish = (): void => {
    const builder = this.builder
    if (!builder?.capabilities.canPublish || !builder.hasUnpublishedChanges || builder.diagnostics.some((item) => item.severity === 'error') || this.previewValidationMessage(builder) || this.commandPending) return
    this.emitCommand('publish')
  }

  private archiveDashboard = (): void => {
    if (!this.builder?.capabilities.canArchive || this.commandPending) return
    this.emitCommand('archive', {}, false)
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
    this.pendingAddVisual = {
      revision: this.revisionKey(builder),
      visualIDs: new Set(page.visuals.map((visual) => visual.id)),
      pageID: page.id,
    }
    this.visualType = type
    this.visualActionMessage = `Adding a ${this.visualLabel(type, builder)} visual.`
    this.emitCommand('add_visual', { pageId: page.id, visualId: '', componentId: '', type, title: '' })
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
      if (currentRevision) {
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
    this.inspectorTab = 'build'
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
    return JSON.stringify({ pageID, dependencies })
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
    const inFlight = this.filterOptionInFlight.get(detail.bindingKey)
    if (inFlight?.context === context && Date.now() - inFlight.startedAt < 250) return
    const generation = (this.filterOptionGenerations.get(detail.bindingKey) ?? 0) + 1
    this.filterOptionGenerations.set(detail.bindingKey, generation)
    this.filterOptionInFlight.set(detail.bindingKey, { context, generation, startedAt: Date.now() })
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

  private readonly addFilterFromSelect = (event: Event): void => {
    const select = event.currentTarget as HTMLSelectElement
    const fieldID = select.value
    select.value = ''
    if (!fieldID || !this.builder) return
    const field = this.builder.semanticModel.datasets.flatMap((dataset) => dataset.fields).find((candidate) => candidate.id === fieldID)
    if (field) this.addFilterForField(field)
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

  private fieldSupportsFilter(field: DashboardBuilderFieldSignal): boolean {
    return field.kind === 'dimension' && Boolean(field.roles?.includes('dimension'))
  }

  private updateFilter(filter: DashboardBuilderFilterSignal, patch: Partial<Pick<DashboardBuilderFilterSignal, 'label' | 'controlType' | 'required' | 'readerEditable' | 'urlParameter'>>): void {
    const builder = this.builder
    if (!builder?.capabilities.canEdit || this.commandPending) return
    const next = { ...filter, ...patch }
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
      ...(scope === 'page' && page ? { pageId: page.id } : {}),
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
    const dataType = field?.dataType.toLowerCase() ?? ''
    let choices: Array<[BuilderFilterControl, string]>
    if (dataType.includes('date') || dataType.includes('time')) choices = [['relativePeriod', 'Relative period'], ['dateRange', 'Date range'], ['singleSelect', 'Single select']]
    else if (dataType.includes('number') || dataType.includes('integer') || dataType.includes('decimal') || dataType.includes('float')) choices = [['numericRange', 'Numeric range'], ['singleSelect', 'Single select'], ['multiSelect', 'Multi select']]
    else choices = [['multiSelect', 'Multi select'], ['singleSelect', 'Single select'], ['text', 'Text search']]
    if (!choices.some(([value]) => value === filter.controlType)) choices.push([filter.controlType, this.filterControlLabel(filter.controlType)])
    return choices
  }

  private filterControlLabel(control: BuilderFilterControl): string {
    const labels: Record<BuilderFilterControl, string> = {
      singleSelect: 'Single select', multiSelect: 'Multi select', text: 'Text search', numericRange: 'Numeric range', dateRange: 'Date range', relativePeriod: 'Relative period',
    }
    return labels[control]
  }

  private draggedFieldFromBuilder(builder: DashboardBuilderSignal | null): DashboardBuilderFieldSignal | undefined {
    if (!builder || !this.draggedFieldID) return undefined
    return (builder.semanticModel.datasets ?? []).flatMap((dataset) => dataset.fields).find((field) => field.id === this.draggedFieldID)
  }

  private recommendedVisualForField(field: DashboardBuilderFieldSignal): BuilderVisualType {
    return field.kind === 'metric' ? 'kpi' : 'table'
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
    const role: BuilderFieldRole = field.kind === 'metric' ? 'metric' : 'detail'
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
    this.addingSlicer = false
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.emit('lv-builder-page-select', { ...this.commandDetail(), pageId: pageID })
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
    this.addingSlicer = false
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.inspectorTab = 'format'
    if (this.collapsedPanes.visuals) {
      this.collapsedPanes = { ...this.collapsedPanes, visuals: false }
      this.persistCollapsedPanes()
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
    this.localVisualID = visualID
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
    if (target instanceof Element && target.closest('.visual, .filter-component')) return
    this.clearCanvasSelection()
  }

  private clearCanvasSelection(): boolean {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!page || (!this.effectiveVisualID(builder, page) && !this.selectedFilterComponentID && !this.selectedFilterID && !this.addingSlicer)) return false
    this.localVisualID = ''
    this.addingSlicer = false
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.inspectorTab = 'build'
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
    this.inspectorTab = 'build'
    this.selectedFilterComponentID = component.id
    this.selectedFilterID = component.filterId
    this.localVisualID = ''
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

  private mobileVisualOrder(visual: DashboardBuilderVisualSignal, page: DashboardBuilderPageSignal): number {
    return this.mobileComponentOrder(visual.id, visual.placement, page)
  }

  private mobileComponentOrder(componentID: string, _placement: DashboardBuilderVisualSignal['placement'], page: DashboardBuilderPageSignal): number {
    return [...page.visuals, ...(page.filterComponents ?? [])]
      .sort((left, right) => left.placement.row - right.placement.row || left.placement.col - right.placement.col || left.id.localeCompare(right.id))
      .findIndex((item) => item.id === componentID)
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

  private readonly handleBuilderKeydown = (event: KeyboardEvent): void => {
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

  private selectPendingAddedVisual(builder: DashboardBuilderSignal | null): void {
    const pending = this.pendingAddVisual
    if (!pending || !builder) return
    if (this.status.error) {
      this.pendingAddVisual = null
      return
    }
    if (pending.revision === this.revisionKey(builder)) return
    const page = builder.pages.find((item) => item.id === pending.pageID)
    const addedVisual = page?.visuals.find((visual) => !pending.visualIDs.has(visual.id))
    this.pendingAddVisual = null
    if (!page || !addedVisual) return
    this.localPageID = page.id
    this.localVisualID = addedVisual.id
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
    this.inspectorTab = 'build'
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
    this.inspectorTab = 'build'
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
    if (builder.save.state === 'dirty') return 'Saving changes…'
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
