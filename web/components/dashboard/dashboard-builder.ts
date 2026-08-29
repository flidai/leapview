import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { GridStack, type GridItemHTMLElement, type GridStackNode } from 'gridstack'
import { ChartColumn, Database, ListFilter, PanelRightClose, PanelRightOpen, Redo2, Undo2 } from 'lucide'
import { repeat } from 'lit/directives/repeat.js'
import type {
  DashboardBuilderDiagnosticSignal,
  DashboardBuilderFieldSignal,
  DashboardBuilderFilterComponentSignal,
  DashboardBuilderFilterSignal,
  DashboardBuilderFormatOptionSignal,
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
import { lucideIcon } from '../shared/lucide-icons'
import { checkSignalContract } from '../shared/signal-contract'
import { browserCommandFailure, ownsBrowserCommandFetch, type BrowserCommandFailure } from '../shared/command-failure'
import './visualization/host'
import { DashboardVisualizationSignalDecoder } from './visualization/signal-envelope'
import { renderVisualTypeIcon } from './visual-type-icon'
import './filters/filter-control'
import { DashboardFilterController } from './filters/filter-controller'
import type { FilterMutationDetail, FilterOptionsNeededDetail } from './filters/filter-control'

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

const builderPaneStorageKey = 'leapview-dashboard-builder-collapsed-panes'
const defaultCollapsedPanes: Record<BuilderPane, boolean> = { filters: false, visuals: false, data: false }

type BuilderCatalogField = {
  field: DashboardBuilderFieldSignal
  datasets: Array<{ id: string; title: string }>
  group: Exclude<BuilderFieldFilter, 'all'>
}

type DashboardBuilderVisualWithPreview = DashboardBuilderVisualSignal & { visualId?: string }

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
  @state() private gridInteractionMessage = ''
  @state() private visualActionMessage = ''
  @state() private draggedFieldID = ''
  @state() private undoStack: BuilderRevisionReference[] = []
  @state() private redoStack: BuilderRevisionReference[] = []
  @state() private visualTypeOverrides: Record<string, BuilderVisualType> = {}
  @state() private terminalFailure: BrowserCommandFailure | null = null
  @state() private collapsedPanes: Record<BuilderPane, boolean> = { ...defaultCollapsedPanes }
  private commandPending = false
  private pendingHistorySnapshot: BuilderHistorySnapshot | null = null
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

  // Add-page uses server-generated identifiers. Keep the page set that was
  // visible when the intent was sent so the authoritative response can select
  // the page created by that intent, even when the response's selectedPageId
  // still reflects the page that was active before the mutation.
  private pendingAddPage: { revision: string; pageIDs: Set<string> } | null = null
  private pendingAddVisual: { revision: string; visualIDs: Set<string>; pageID: string } | null = null
  private pendingAddFilter: { revision: string; filterIDs: Set<string> } | null = null
  private pendingAddFilterComponent: { revision: string; componentIDs: Set<string>; pageID: string } | null = null

  override connectedCallback(): void {
    super.connectedCallback()
    this.restoreCollapsedPanes()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
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
    this.removeEventListener('lv-filter-mutate', this.handleBuilderFilterMutation as EventListener, { capture: true })
    this.removeEventListener('lv-filter-options-needed', this.handleBuilderFilterOptionsNeeded as EventListener, { capture: true })
    if (typeof window !== 'undefined') window.removeEventListener('keydown', this.handleBuilderKeydown)
    this.viewportMediaQuery?.removeEventListener('change', this.handleViewportChange)
    this.viewportMediaQuery = null
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
      gap: var(--base-size-12);
      padding: var(--base-size-12);
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
      letter-spacing: 0.04em;
      text-transform: uppercase;
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
      gap: var(--base-size-6);
      padding-bottom: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
    }

    .filter-scope-group:last-of-type {
      border-bottom: 0;
    }

    .filter-scope-empty {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .filter-scope-options {
      display: grid;
      gap: var(--base-size-6);
      padding: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
    }

    .filter-scope-option {
      display: grid;
      grid-template-columns: auto 1fr;
      gap: var(--base-size-8);
      align-items: start;
      color: var(--lv-fg-default) !important;
    }

    .filter-scope-option span {
      display: grid;
      gap: var(--base-size-2);
      font: var(--lv-type-body-compact);
    }

    .filter-scope-option small {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .filter-card {
      display: grid;
      width: 100%;
      gap: var(--base-size-2);
      padding: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel);
      text-align: left;
    }

    .filter-card[aria-pressed='true'] {
      border-color: var(--lv-fg-accent);
      box-shadow: inset 3px 0 0 var(--lv-fg-accent);
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
      padding-top: var(--base-size-8);
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

    .filter-remove {
      color: var(--lv-fg-danger, var(--lv-fg-default));
    }

    .filter-placement-action {
      color: var(--lv-data-2);
    }

    .filter-placement-note {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
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
      margin-right: var(--base-size-8);
      padding: 0;
      border-color: transparent;
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

    .field-section + .field-section,
    .unsupported-fields {
      margin-top: var(--base-size-16);
    }

    .field-section-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      margin: 0 0 var(--base-size-4);
    }

    .field-section-title {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      letter-spacing: 0.04em;
      text-transform: uppercase;
    }

    .field-section-count {
      font-weight: var(--base-text-weight-normal);
      letter-spacing: normal;
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

    .unsupported-fields {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-8);
    }

    .unsupported-fields summary {
      min-height: var(--control-small-size);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      cursor: pointer;
    }

    .unsupported-fields .field-list {
      margin-top: var(--base-size-4);
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
      background: var(--lv-bg-app);
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

    .canvas-scroll {
      position: relative;
      overflow: auto;
      min-width: 0;
      padding: 0;
    }

    .canvas {
      position: relative;
      width: max(100%, 38rem);
      min-width: 38rem;
      min-height: 30rem;
      border: 0;
      border-radius: 0;
      background-color: var(--lv-bg-panel);
      background-image: linear-gradient(to right, color-mix(in srgb, var(--lv-line-muted) 55%, transparent) 1px, transparent 1px), linear-gradient(to bottom, color-mix(in srgb, var(--lv-line-muted) 55%, transparent) 1px, transparent 1px);
      background-size: 8.333% 2.5rem;
      box-shadow: none;
    }

    .canvas[data-field-dragging='true'] {
      outline: 2px dashed var(--lv-data-2);
      outline-offset: -4px;
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
      gap: var(--base-size-12);
      padding: var(--base-size-12);
    }

    .field-wells {
      display: grid;
      gap: var(--base-size-12);
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

    .preview-error {
      margin: 0;
      padding: var(--base-size-8) var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      color: var(--lv-fg-danger, var(--lv-fg-muted));
      background: var(--lv-bg-danger-muted, var(--lv-bg-panel));
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
      display: grid;
      min-height: 0;
      place-items: center;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .filter-component > .grid-stack-item-content {
      grid-template-rows: auto minmax(0, 1fr) auto;
      gap: var(--base-size-8);
      background: color-mix(in srgb, var(--lv-bg-panel) 92%, var(--lv-data-2-muted));
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
      .canvas {
        display: grid;
        width: 100%;
        min-width: 0;
        min-height: 0;
        aspect-ratio: auto !important;
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

      .toolbar-actions {
        width: 100%;
        overflow-x: auto;
      }
    }
  `

  updated(): void {
    const builder = this.builder
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
    this.selectPendingAddedPage(builder)
    this.selectPendingAddedVisual(builder)
    this.selectPendingAddedFilter(builder)
    this.selectPendingAddedFilterComponent(builder)
    this.reconcileBuilderFilterController()
    this.syncGridStack(builder, builder ? this.selectedPage(builder) : undefined)
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
        cellHeight: Math.max(1, page.grid.rowHeight || 48),
        margin: Math.max(0, Math.round((page.grid.gap || 16) / 2)),
        animate: false,
        float: false,
        disableDrag: !builder?.capabilities.canEdit || this.commandPending,
        disableResize: !builder?.capabilities.canEdit || this.commandPending,
        draggable: { handle: '.component-drag-handle' },
        resizable: { handles: 'se', autoHide: true },
      }, canvas as GridItemHTMLElement)
      if (this.gridStack) {
        this.gridStack.on('dragstop', (_event: Event, element: GridItemHTMLElement) => this.onGridInteractionStop(element))
        this.gridStack.on('resizestop', (_event: Event, element: GridItemHTMLElement) => this.onGridInteractionStop(element))
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
    this.scheduleGridCommit()
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
    const hasMoreActions = builder.capabilities.canShare || builder.capabilities.canExport || Boolean(this.forkHref)
    return html`
      <header class="toolbar">
        ${this.backHref ? html`<a class="back" href=${this.backHref} aria-label="Back to dashboard">Back</a>` : html`<span class="back" aria-label="Back to dashboard">Back</span>`}
        <div class="title-wrap">
          <h1 class="title">${builder.title}</h1>
          <div class="meta" data-state=${builder.hasUnpublishedChanges || saveState === 'dirty' ? 'dirty' : saveState} aria-label="Dashboard draft status" aria-live="polite" title=${`${builder.origin.label} · Revision ${builder.revision.number} · ${builder.revision.id}`}>
            <span>${this.titleCase(builder.visibility)} ${this.titleCase(builder.lifecycle)} · Revision ${builder.revision.number} · ${this.saveLabel(builder)}</span>
          </div>
        </div>
        <div class="toolbar-actions" aria-label="Builder actions">
          <button type="button" class="icon-action" data-builder-action="undo" aria-label="Undo" title="Undo (Ctrl/⌘ Z)" ?disabled=${!builder.capabilities.canEdit || this.commandPending || this.undoStack.length === 0} @click=${this.undo}>${lucideIcon(Undo2, { size: 16, strokeWidth: 2 })}<span class="sr-only">Undo</span></button>
          <button type="button" class="icon-action" data-builder-action="redo" aria-label="Redo" title="Redo (Ctrl/⌘ Shift Z)" ?disabled=${!builder.capabilities.canEdit || this.commandPending || this.redoStack.length === 0} @click=${this.redo}>${lucideIcon(Redo2, { size: 16, strokeWidth: 2 })}<span class="sr-only">Redo</span></button>
          ${(builder.preview.href || this.previewHref) && builder.capabilities.canPreview
            ? html`<a class="button" href=${builder.preview.href || this.previewHref}>Preview</a>`
            : builder.capabilities.canPreview ? html`<button disabled title="Preview is not available yet">Preview</button>` : nothing}
          ${hasMoreActions ? html`
            <details class="more-actions">
              <summary aria-label="More dashboard actions">More</summary>
              <div class="more-menu" aria-label="More dashboard actions">
                ${this.forkHref ? html`<a class="button" href=${this.forkHref}>Fork as draft</a>` : nothing}
                ${builder.capabilities.canShare ? html`<button @click=${this.toggleVisibility} aria-label="Toggle dashboard visibility">${builder.visibility === 'organization' ? 'Make private' : 'Share with organization'}</button>` : nothing}
                ${builder.capabilities.canExport
                  ? this.exportYAMLHref ? html`<a class="button" href=${this.exportYAMLHref} download>Export YAML</a>` : html`<button disabled title="YAML export is not available yet">Export YAML</button>`
                  : nothing}
              </div>
            </details>` : nothing}
          ${builder.capabilities.canPublish ? html`<button class="primary" @click=${this.publish}>Publish</button>` : nothing}
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
      this.pendingHistorySnapshot = null
      this.setGridEditingEnabled(Boolean(this.builder?.capabilities.canEdit))
      this.requestUpdate()
      return
    }
    const commandFailure = browserCommandFailure(event, 'Dashboard builder action')
    if (!commandFailure) return
    this.commandPending = false
    if (this.pendingHistorySnapshot) {
      this.undoStack = this.pendingHistorySnapshot.undo
      this.redoStack = this.pendingHistorySnapshot.redo
      this.pendingHistorySnapshot = null
    }
    this.visualTypeOverrides = {}
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
    return html`
      <footer class="page-bar">
        <span class="sr-only">Pages</span>
        <nav class="page-tabs" aria-label="Dashboard pages" role=${this.pageBaseHref ? nothing : 'tablist'}>
          ${repeat(builder.pages, (item) => item.id, (item) => this.pageBaseHref
            ? html`<a class="page-tab" aria-current=${item.id === page?.id ? 'page' : nothing} href=${this.pageHref(item.id)} title=${item.title}>${item.title}</a>`
            : html`<button class="page-tab" role="tab" aria-selected=${item.id === page?.id} @click=${() => this.selectPage(item.id)} title=${item.title}>${item.title}</button>`)}
        </nav>
        ${builder.capabilities.canAddPage ? html`<button @click=${this.addPage} aria-label="Add page" title="Add page">+</button>` : nothing}
      </footer>
    `
  }

  private renderFieldBrowser(builder: DashboardBuilderSignal, visual: DashboardBuilderVisualSignal | undefined) {
    const catalog = this.filteredCatalog(this.semanticCatalog(builder.semanticModel.datasets ?? []))
    const visibleCatalog = this.fieldFilter === 'all' ? catalog : catalog.filter((item) => item.group === this.fieldFilter)
    const supported = visibleCatalog.filter((item) => visual ? this.fieldCompatibleWithVisual(item.field, visual) : this.fieldDataTypeSupported(item.field))
    const unsupported = visibleCatalog.filter((item) => !supported.includes(item))
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
            <p class="pane-hint">${builder.semanticModel.title} semantic model</p>
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
                ${groups.map((group) => this.renderCatalogGroup(group, supported.filter((item) => item.group === group), visual))}
                ${unsupported.length > 0 ? html`
                  <details class="unsupported-fields" ?open=${Boolean(this.fieldQuery)}>
                    <summary>Other fields not supported by this visual (${unsupported.length})</summary>
                    <div class="field-list">
                      ${repeat(unsupported, (item) => this.catalogFieldKey(item), (item) => this.renderCatalogField(item, visual, false))}
                    </div>
                  </details>
                ` : nothing}
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
    const dimensions = this.semanticCatalog(builder.semanticModel.datasets ?? []).filter((item) => item.field.kind === 'dimension')
    const filterError = this.builderFilterErrorMessage()
    const collapsed = this.collapsedPanes.filters
    return html`
      <aside class="pane filters-pane" data-collapsed=${collapsed} aria-labelledby="builder-filters-heading">
        <div class="pane-header">
          <div class="pane-heading-row">
            <div class="pane-title-group">
              <span class="pane-title-icon">${lucideIcon(ListFilter, { size: 16, strokeWidth: 2 })}</span>
              <h2 id="builder-filters-heading" class="pane-title">Filters</h2>
            </div>
            ${this.renderPaneToggle('filters', 'Filters pane', 'builder-filters-content')}
          </div>
          <div class="pane-header-details" ?hidden=${collapsed}>
            <p class="pane-hint">Report filters in dashboard code. Applied to every page unless targets narrow them.</p>
            ${filterError ? html`<p class="filter-validation" role="alert" aria-live="assertive">${filterError}</p>` : nothing}
          </div>
        </div>
        <div id="builder-filters-content" class="filter-pane-body pane-content" ?hidden=${collapsed}>
          <div class="filter-drop-zone" data-field-dragging=${this.draggedFieldID ? 'true' : 'false'} @dragover=${this.allowFieldDrop} @drop=${this.dropFieldOnFilters}>
            Drop a governed dimension to create a report filter
          </div>
          <label>
            <span class="sr-only">Add report filter</span>
            <select class="filter-add-select" aria-label="Add report filter" ?disabled=${!builder.capabilities.canEdit || this.commandPending} @change=${this.addFilterFromSelect}>
              <option value="">+ Add a data field</option>
              ${dimensions.map((item) => html`<option value=${item.field.id} ?disabled=${filters.some((candidate) => candidate.dimension === item.field.id)}>${item.field.label}</option>`)}
            </select>
          </label>
          ${this.renderFilterScopeGroup('Filters on this visual', grouped.visual, filter, visual ? visual.title : 'Select a visual to target it')}
          ${this.renderFilterScopeGroup('Filters for page visuals', grouped.page, filter, page?.title ?? 'No page selected')}
          ${this.renderFilterScopeGroup('Filters on all pages', grouped.report, filter, 'Every compatible visual')}
          ${grouped.custom.length > 0 ? this.renderFilterScopeGroup('Custom targets', grouped.custom, filter, 'Target set authored in dashboard code') : nothing}
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
          <label class="filter-scope-option"><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'report'} ?disabled=${!editable} @change=${() => this.setFilterScope(filter, 'report')} /><span>All pages<small>One report binding applies to every compatible visual.</small></span></label>
          <label class="filter-scope-option"><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'page'} ?disabled=${!editable || !page} @change=${() => page && this.setFilterScope(filter, 'page', page)} /><span>This page<small>${page ? `Create a page-scoped binding for ${page.title}.` : 'Select a page first.'}</small></span></label>
          <label class="filter-scope-option"><input type="radio" name=${`filter-scope-${filter.id}`} .checked=${scope === 'visual'} ?disabled=${!editable || !visual || !page} @change=${() => page && visual && this.setFilterScope(filter, 'page', page, [visual.id])} /><span>This visual<small>${visual ? `Only ${visual.title}.` : 'Select a visual first.'}</small></span></label>
          ${scope === 'custom' ? html`<p class="filter-scope-empty">Custom targets are preserved from dashboard code until you choose another scope.</p>` : nothing}
        </div>
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
        <p class="filter-placement-note">${placedComponent ? `Slicer placed on ${page?.title}. Its position is stored in dashboard code.` : `Not shown as a slicer on ${page?.title ?? 'this page'}.`}</p>
        ${page ? html`
          <button type="button" class="filter-placement-action" ?disabled=${!editable} @click=${() => placedComponent ? this.removeFilterComponent(page, placedComponent) : this.addFilterComponent(page, filter)}>
            ${placedComponent ? 'Remove slicer from this page' : 'Add slicer to this page'}
          </button>
        ` : nothing}
        <button type="button" class="filter-remove" ?disabled=${!editable} @click=${() => this.removeFilter(filter)}>Remove filter</button>
      </section>
    `
  }

  private renderFilterScopeGroup(title: string, filters: DashboardBuilderFilterSignal[], selected: DashboardBuilderFilterSignal | undefined, empty: string) {
    return html`
      <section class="filter-scope-group" aria-label=${title}>
        <div class="filter-scope-heading"><span>${title}</span><span>${filters.length}</span></div>
        <div class="filter-list">
          ${filters.length === 0
            ? html`<p class="filter-scope-empty">${empty}</p>`
            : repeat(filters, (item) => item.id, (item) => html`
                <button type="button" class="filter-card" aria-pressed=${selected?.id === item.id} @click=${() => this.selectFilterDefinition(item.id)}>
                  <span class="filter-card-title">${item.label}</span>
                  <span class="filter-card-meta">${this.filterControlLabel(item.controlType)} · ${this.fieldLabel(item.dimension, item.dimension)}</span>
                </button>
              `)}
        </div>
      </section>
    `
  }

  private renderCatalogGroup(group: Exclude<BuilderFieldFilter, 'all'>, fields: BuilderCatalogField[], visual: DashboardBuilderVisualSignal | undefined) {
    if (fields.length === 0) return nothing
    const headingID = `builder-data-${group}-heading`
    return html`
      <section class="field-section" data-field-group=${group} aria-labelledby=${headingID}>
        <div class="field-section-header">
          <h3 id=${headingID} class="field-section-title">${this.fieldGroupLabel(group)}</h3>
          <span class="field-section-count">${fields.length}</span>
        </div>
        <div class="field-list">
          ${repeat(fields, (item) => this.catalogFieldKey(item), (item) => this.renderCatalogField(item, visual, true))}
        </div>
      </section>
    `
  }

  private renderCatalogField(item: BuilderCatalogField, visual: DashboardBuilderVisualSignal | undefined, compatible: boolean) {
    const field = item.field
    const visualType = visual ? this.visualTypeForRender(visual) : ''
    const datasetContext = item.datasets[0]?.title ?? ''
    const usedIn = visual ? this.fieldUsedIn(field, visual) : ''
    const editable = Boolean(this.builder?.capabilities.canEdit && compatible && (visual || this.builder?.capabilities.canAddVisual))
    const roleLabel = this.fieldGroupLabel(item.group, true)
    const dataType = field.dataType.toLowerCase() === 'unknown' ? '' : field.dataType
    const action = !visual
      ? `Click or drag to create a ${this.visualLabel(this.recommendedVisualForField(field))} visual.`
      : !compatible
        ? `Not compatible with the selected ${visualType} visual.`
        : usedIn
          ? `Used in ${usedIn}. Drag to a compatible field well.`
          : `Click to add to ${this.fieldWellLabel(visual, this.roleForField(field, visual))}, or drag to a field well.`
    const accessibleName = [field.label, roleLabel, dataType, datasetContext, action].filter(Boolean).join('. ')
    const context = compatible ? datasetContext : `${datasetContext} · Not compatible with ${visualType || 'this visual'}`

    if (!compatible) {
      return html`
        <div class="field field-unsupported" role="note" aria-label=${accessibleName}>
          <span class="field-role-icon" aria-hidden="true">${this.renderFieldRoleIcon(item.group)}</span>
          <span class="field-copy"><span class="field-label">${field.label}</span><span class="field-context">${context}</span></span>
          <span class="field-used">Unsupported</span>
        </div>
      `
    }

    return html`
      <button class="field" type="button" data-used=${usedIn ? 'true' : 'false'} data-dragging=${this.draggedFieldID === field.id ? 'true' : 'false'} draggable=${editable ? 'true' : 'false'} ?disabled=${!editable} title=${field.description || action} aria-label=${accessibleName} @click=${() => this.addField(field)} @dragstart=${(event: DragEvent) => this.dragField(event, field)} @dragend=${this.clearDraggedField}>
        <span class="field-role-icon" aria-hidden="true">${this.renderFieldRoleIcon(item.group)}</span>
        <span class="field-copy"><span class="field-label">${field.label}</span><span class="field-context">${datasetContext}</span></span>
        ${usedIn ? html`<span class="field-used">✓ ${usedIn}</span>` : nothing}
      </button>
    `
  }

  private renderCanvas(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined) {
    if (!page) {
      return html`<main class="canvas-pane" aria-label="Dashboard canvas"><div class="state"><div><strong>No pages yet</strong><span>Create a page to start designing this dashboard.</span>${builder.capabilities.canAddPage ? html`<div><button @click=${this.addPage} aria-label="Add page">Add page</button></div>` : nothing}</div></div></main>`
    }
    const width = Math.max(12, page.grid.columns || 12)
    return html`
      <main class="canvas-pane" aria-label="Dashboard canvas">
        <div class="canvas-scroll">
          ${builder.preview.error ? html`<p class="preview-error" role="alert">${builder.preview.error}</p>` : nothing}
          <p id="dashboard-builder-grid-help" class="sr-only">Focus a canvas component. Use Alt plus an arrow key to move it one grid cell. Use Alt plus Shift plus an arrow key to resize it.</p>
          <div class="canvas grid-stack" data-field-dragging=${this.draggedFieldID ? 'true' : 'false'} aria-describedby="dashboard-builder-grid-help" style=${`aspect-ratio: ${page.canvas.width || 16} / ${page.canvas.height || 9}; grid-template-columns: repeat(${width}, 1fr);`} @click=${this.deselectVisualFromCanvas} @dragover=${this.allowFieldDrop} @drop=${this.dropField}>
            ${this.draggedFieldID ? html`<div class="canvas-field-drop-hint" role="status">Drop on the canvas to create a ${this.visualLabel(this.recommendedVisualForDraggedField(builder), builder)} visual</div>` : nothing}
            ${page.visuals.length === 0 && (page.filterComponents?.length ?? 0) === 0
              ? html`<div class="visual-empty"><div><strong>This page is empty</strong><span>Choose a visual or place a report-filter slicer to begin.</span></div></div>`
              : html`${repeat(page.visuals, (visual) => visual.id, (visual) => this.renderVisual(visual, page))}${repeat(page.filterComponents ?? [], (component) => component.id, (component) => this.renderFilterComponent(component, page))}`}
          </div>
          <div class="sr-only" aria-live="polite">${this.gridInteractionMessage}</div>
        </div>
      </main>
    `
  }

  private renderVisual(visual: DashboardBuilderVisualSignal, page: DashboardBuilderPageSignal) {
    const selected = visual.id === this.effectiveVisualID(this.builder, page)
    const visualType = this.visualTypeForRender(visual)
    const preview = this.builderVisuals[this.visualSignalID(visual)]
    const mobileOrder = this.mobileVisualOrder(visual, page)
    const columns = Math.max(1, page.grid.columns || 12)
    const left = `${Math.max(0, visual.placement.col - 1) * (100 / columns)}%`
    const top = `${Math.max(0, visual.placement.row - 1) * (page.grid.rowHeight || 40)}px`
    const width = `${Math.max(1, visual.placement.colSpan) * (100 / columns)}%`
    const height = `${Math.max(1, visual.placement.rowSpan) * (page.grid.rowHeight || 40)}px`
    const draggedField = this.draggedFieldFromBuilder(this.builder)
    const fieldDrop = draggedField ? (this.fieldCompatibleWithVisual(draggedField, visual) ? 'compatible' : 'incompatible') : ''
    return html`
      <div class="visual grid-stack-item ${preview ? 'has-preview' : ''}" data-visual-type=${visualType} data-selected=${selected} data-field-drop=${fieldDrop || nothing} gs-id=${visual.id} gs-x=${Math.max(0, visual.placement.col - 1)} gs-y=${Math.max(0, visual.placement.row - 1)} gs-w=${Math.max(1, visual.placement.colSpan)} gs-h=${Math.max(1, visual.placement.rowSpan)} role="group" tabindex="0" aria-label=${selected ? `${visual.title}, selected dashboard visual` : `${visual.title}, dashboard visual`} aria-describedby="dashboard-builder-grid-help" style=${`left:${left};top:${top};width:${width};height:${height};--mobile-order:${mobileOrder}`} @click=${(event: MouseEvent) => { event.stopPropagation(); this.selectVisualFromPointer(visual.id) }} @keydown=${(event: KeyboardEvent) => this.selectVisualOnKey(event, visual.id)} @dragover=${this.allowFieldDrop} @drop=${(event: DragEvent) => this.dropFieldOnVisual(event, visual.id)}>
        <div class="grid-stack-item-content">
          ${preview
            ? html`<span class="visual-preview"><lv-visualization-host authoring .envelope=${preview}><span slot="authoring-drag-handle" class="visual-drag-header component-drag-handle" title="Drag to move ${visual.title}" @pointerdown=${() => this.selectVisualFromPointer(visual.id)}>${visual.title}</span></lv-visualization-host></span>`
            : html`<span class="visual-drag-header component-drag-handle" title="Drag to move ${visual.title}" @pointerdown=${() => this.selectVisualFromPointer(visual.id)}>${visual.title}</span><span class="visual-preview-empty">${this.builder?.preview.error ? 'Preview unavailable' : 'Add fields to preview'}</span><span class="visual-type">${visualType} · ${visual.slots.length} field slots</span>`}
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
          <span class="filter-drag-header component-drag-handle" title="Drag to move ${component.label}" @pointerdown=${() => this.selectFilterComponent(component)}>${component.label}</span>
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
          <span class="filter-runtime-note">${validationMessage || (binding ? 'Interactive draft preview' : `${this.filterControlLabel(component.controlType)} · preparing preview`)}</span>
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

  private builderFilterErrorMessage(): string {
    if (this.builderFilterTransportError) return this.builderFilterTransportError
    const validation = this.builderFilterValidation
    return validation.accepted ? '' : validation.message
  }

  private renderInspector(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined) {
    const collapsed = this.collapsedPanes.visuals
    return html`
      <aside class="pane properties visual-builder" data-collapsed=${collapsed} aria-label="Visual builder">
        <div class="pane-header">
          <div class="inspector-heading">
            <div class="inspector-title">
              <span class="pane-title-icon">${lucideIcon(ChartColumn, { size: 16, strokeWidth: 2 })}</span>
              <h2 class="pane-title">${collapsed ? 'Visuals' : visual ? visual.title : page ? 'Add a visual' : 'Visual builder'}</h2>
              ${visual && !collapsed ? html`<span class="visual-type-badge">${this.titleCase(this.visualTypeForRender(visual))}</span>` : nothing}
            </div>
            ${this.renderPaneToggle('visuals', 'Visuals pane', 'builder-visuals-content')}
          </div>
          <div class="pane-header-details" ?hidden=${collapsed}>
            <p class="pane-hint">${visual ? 'Build this visual with governed fields.' : page ? 'Choose a visual type to add it to this page.' : 'Add a page to start building.'}</p>
          </div>
          <p class="sr-only" role="status" aria-live="polite">${this.visualActionMessage}</p>
        </div>
        <div id="builder-visuals-content" class="pane-content" ?hidden=${collapsed}>
          <div class="inspector-tabs" role="tablist" aria-label="Visual configuration">
            <button class="inspector-tab" role="tab" data-inspector-tab="build" aria-selected=${this.inspectorTab === 'build'} @click=${() => { this.inspectorTab = 'build' }}>Build</button>
            <button class="inspector-tab" role="tab" data-inspector-tab="format" aria-selected=${this.inspectorTab === 'format'} @click=${() => { this.inspectorTab = 'format' }}>Format</button>
          </div>
          ${this.inspectorTab === 'build'
            ? html`<div class="inspector-panel" role="tabpanel" aria-label="Build visual">
                ${this.renderVisualPicker(builder, page, visual)}
                ${visual ? this.renderFieldWells(visual) : html`<div class="format-placeholder">Select a visual to see its field wells. New visuals are placed on the current page and selected after the saved revision arrives.</div>`}
              </div>`
            : html`<div class="inspector-panel" role="tabpanel" aria-label="Format visual">
                ${visual ? this.renderVisualFormatControls(visual) : this.renderPageProperties(page)}
              </div>`}
          <div class="properties-body">
            <details class="secondary-details">
              <summary>Diagnostics &amp; source evidence</summary>
              <div class="secondary-details-content">${this.renderDiagnostics(builder.diagnostics)}${this.renderEvidence(builder)}</div>
            </details>
          </div>
        </div>
      </aside>
    `
  }

  private renderVisualPicker(builder: DashboardBuilderSignal, page: DashboardBuilderPageSignal | undefined, visual: DashboardBuilderVisualSignal | undefined) {
    const currentType = visual ? this.visualTypeForRender(visual) : undefined
    const pickerHelpID = 'builder-visual-type-help'
    const catalog = this.visualCatalogGroups(builder.visualCatalog ?? []).flatMap(([, entries]) => entries)
    const selectedEntry = visual ? this.visualCatalogEntry(currentType ?? '', builder) : undefined
    return html`
      <section class="property-group" aria-label=${visual ? 'Edit visual type' : 'Add visual'}>
        <span class="property-label">${visual ? 'Visual type' : 'Add a visual'}</span>
        <p id=${pickerHelpID} class="pane-hint">${visual ? `Choose a type to change ${visual.title}.` : 'Choose a type to add it immediately.'}</p>
        <div class="visual-picker-catalog" role="group" aria-label=${visual ? `Change ${visual.title} type` : 'Visual types'}>
          <div class="visual-picker">
            ${catalog.map((entry) => html`
              <button type="button" class="visual-picker-button" data-visual-picker-type=${entry.type} data-visual-type=${entry.type} data-visual-group=${entry.group} aria-label=${visual ? `Change to ${entry.label} visual` : `Add ${entry.label} visual`} aria-describedby=${pickerHelpID} title=${entry.label} aria-pressed=${Boolean(visual && currentType === entry.type)} ?disabled=${this.commandPending || (visual ? !builder.capabilities.canEdit : !page || !builder.capabilities.canAddVisual)} @click=${() => this.selectVisualType(entry.type, visual)}>
                ${renderVisualTypeIcon(entry.type)}
                <span class="sr-only">${entry.label}</span>
              </button>
            `)}
          </div>
        </div>
        ${selectedEntry ? html`<a class="visual-reference-link" href=${selectedEntry.referenceHref}>Open ${selectedEntry.label} visual reference</a>` : nothing}
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
    return html`
      <section class="property-group" aria-label="Field wells">
        <span class="property-label">Fields</span>
        <p class="pane-hint">Drag fields into a role, or click a compatible field below.</p>
        <div class="field-wells">${roles.map((role) => this.renderFieldWell(visual, role))}</div>
      </section>
    `
  }

  private renderFieldWell(visual: DashboardBuilderVisualSignal, role: BuilderFieldRole) {
    const slots = visual.slots.filter((slot) => this.slotRole(slot) === role)
    const label = this.fieldWellLabel(visual, role)
    const draggedField = this.draggedFieldFromBuilder(this.builder)
    const fieldDrop = draggedField ? (this.fieldCompatibleWithRole(draggedField, role) ? 'compatible' : 'incompatible') : ''
    return html`
      <section class="field-well">
        <div class="field-well-label"><span>${label}</span><span>${slots.length}</span></div>
        <div class="field-well-target" data-drop-well=${role} data-field-drop=${fieldDrop || nothing} tabindex="0" aria-label=${`Drop ${role} field in ${label}`} @dragover=${this.allowFieldDrop} @drop=${(event: DragEvent) => this.dropFieldOnRole(event, role)}>
          ${slots.length === 0
            ? html`<span class="empty-well">Drop a ${role} field here</span>`
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
    return html`
      <section class="property-group"><span class="property-label">Page</span><span class="property-value">${page.title}</span></section>
      <section class="property-group"><span class="property-label">Canvas</span><span class="property-value">${page.canvas.width} × ${page.canvas.height}</span></section>
      <section class="property-group"><span class="property-label">Grid</span><span class="property-value">${page.grid.columns} columns · ${page.grid.rowHeight}px rows</span></section>
    `
  }

  private renderDiagnostics(diagnostics: DashboardBuilderDiagnosticSignal[]) {
    if (diagnostics.length === 0) return nothing
    return html`<section class="property-group" aria-label="Validation diagnostics"><span class="property-label">Validation</span><div class="diagnostics">${diagnostics.map((item) => html`<div class="diagnostic ${item.severity}" role=${item.severity === 'error' ? 'alert' : 'status'}><strong>${item.code}</strong> ${item.message}</div>`)}</div></section>`
  }

  private renderEvidence(builder: DashboardBuilderSignal) {
    const evidence = builder.sourceEvidence
    if (!evidence) return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>Not available</span></div></section>`
    if (evidence.kind === 'project' && evidence.projectId && evidence.dashboardId && evidence.generationId) {
      return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>project · ${evidence.projectId}/${evidence.dashboardId} · ${evidence.generationId}${evidence.path ? ` · ${evidence.path}` : ''}</span></div></section>`
    }
    return html`<section class="property-group" aria-label="Source evidence"><span class="property-label">Source evidence</span><div class="evidence"><span>Unavailable</span></div></section>`
  }

  private semanticCatalog(datasets: DashboardBuilderDatasetSignal[]): BuilderCatalogField[] {
    const catalog = new Map<string, BuilderCatalogField>()
    for (const dataset of datasets) {
      const datasetFields = new Map<string, DashboardBuilderFieldSignal>()
      for (const field of dataset.fields) {
        const labelKey = `${field.kind}:${field.label.trim().toLocaleLowerCase()}`
        const existing = datasetFields.get(labelKey)
        if (!existing || this.fieldCatalogScore(field) > this.fieldCatalogScore(existing)) datasetFields.set(labelKey, field)
      }
      for (const field of datasetFields.values()) {
        const key = `${field.kind}:${field.id}`
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
    return `${item.field.kind}:${item.field.id}`
  }

  private fieldUsedIn(field: DashboardBuilderFieldSignal, visual: DashboardBuilderVisualSignal): string {
    const labels = visual.slots
      .filter((slot) => slot.fieldId === field.id)
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
    const type = this.visualTypeForRender(visual)
    if (type === 'table') return field.kind === 'dimension'
    if (type === 'kpi') return field.kind === 'metric'
    return field.kind === 'dimension' || field.kind === 'metric'
  }

  private fieldDataTypeSupported(field: DashboardBuilderFieldSignal): boolean {
    const dataType = field.dataType.trim().toLowerCase()
    return !['opaque', 'binary', 'blob', 'object', 'struct', 'list', 'map'].some((unsupported) => dataType.includes(unsupported))
  }

  private fieldCompatibleWithRole(field: DashboardBuilderFieldSignal, role: BuilderFieldRole): boolean {
    if (!this.fieldDataTypeSupported(field)) return false
    if (role === 'detail') return field.kind === 'dimension'
    return field.kind === role
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
    if (this.visualTypeForRender(visual) === 'kpi') return 'Value'
    const horizontal = this.visualTypeForRender(visual) === 'bar'
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
  private publish = (): void => this.emitCommand('publish')

  private addPage = (): void => {
    const builder = this.builder
    if (!builder?.capabilities.canAddPage) return
    this.pendingAddPage = {
      revision: this.revisionKey(builder),
      pageIDs: new Set(builder.pages.map((page) => page.id)),
    }
    this.emitCommand('add_page', { pageId: '', title: '' })
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
    this.visualType = type
    if (!visual) {
      this.addVisual(type)
      return
    }
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!builder?.capabilities.canEdit || !page || this.commandPending) return
    if (this.visualTypeForRender(visual) === type) {
      this.visualActionMessage = `${visual.title} is already a ${this.visualLabel(type, builder)} visual.`
      return
    }
    this.visualActionMessage = `Changing ${visual.title} to a ${this.visualLabel(type, builder)} visual.`
    this.visualTypeOverrides = { ...this.visualTypeOverrides, [visual.id]: type }
    this.emitCommand('set_visual_type', { pageId: page.id, visualId: visual.id, type })
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

  private addField(field: DashboardBuilderFieldSignal): void {
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    const visual = page && builder ? this.selectedVisual(page, builder) : undefined
    if (!builder?.capabilities.canEdit || !page) return
    if (!visual) {
      this.createVisualFromField(field)
      return
    }
    if (!this.fieldCompatibleWithVisual(field, visual)) return
    const usedIn = this.fieldUsedIn(field, visual)
    if (usedIn) {
      this.gridInteractionMessage = `${field.label} is already used in ${usedIn}.`
      return
    }
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
    this.createVisualFromField(field)
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
    if (!page || !visual || !field || !this.fieldCompatibleWithRole(field, role)) return
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
    if (!builder?.capabilities.canEdit || this.commandPending || field.kind !== 'dimension') return false
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
    this.localPageID = pageID
    this.localVisualID = ''
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.emit('lv-builder-page-select', { ...this.commandDetail(), pageId: pageID })
  }

  private pageHref(pageID: string): string {
    const separator = this.pageBaseHref.includes('?') ? '&' : '?'
    return `${this.pageBaseHref}${separator}page=${encodeURIComponent(pageID)}`
  }

  private selectVisual(visualID: string): void {
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
    const builder = this.builder
    const page = builder ? this.selectedPage(builder) : undefined
    if (!page || (!this.effectiveVisualID(builder, page) && !this.selectedFilterComponentID && !this.selectedFilterID)) return
    this.localVisualID = ''
    this.selectedFilterID = ''
    this.selectedFilterComponentID = ''
    this.inspectorTab = 'build'
    this.visualActionMessage = 'Visual selection cleared.'
    this.emit('lv-builder-visual-select', { ...this.commandDetail(), visualId: '' })
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
    if (recordHistory && action !== 'publish' && action !== 'set_visibility') {
      const current = this.currentRevisionReference()
      if (current) {
        this.pendingHistorySnapshot = { undo: [...this.undoStack], redo: [...this.redoStack] }
        this.undoStack = [...this.undoStack.slice(-99), current]
        this.redoStack = []
      }
    }
    this.commandPending = true
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
    if (modifier && key === 'z' && event.shiftKey) {
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
    this.selectedFilterID = addedComponent.filterId
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
    if (builder.save.state === 'dirty') return 'Unsaved changes'
    if (builder.hasUnpublishedChanges) return 'Unpublished draft'
    return builder.save.message || 'Saved'
  }

  private titleCase(value: string): string {
    return value.length === 0 ? value : `${value[0].toUpperCase()}${value.slice(1)}`
  }
}

if (!customElements.get('lv-dashboard-builder')) customElements.define('lv-dashboard-builder', LeapViewDashboardBuilder)

declare global {
  interface HTMLElementTagNameMap {
    'lv-dashboard-builder': LeapViewDashboardBuilder
  }
}
