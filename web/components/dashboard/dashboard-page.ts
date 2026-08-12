import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import type {
  AgentContextSignal,
  AgentReferenceSignal,
  DashboardComponentSignal,
  DashboardFilterContract,
  DashboardFilterOptionPage,
  DashboardFilterState,
  DashboardFilterValidationResult,
  DashboardInteractionSelection,
  DashboardPageNavSignal,
  DashboardPageSignal,
  DashboardStatus,
  DashboardVisualizationSignal,
  RouteRuntimeSignal,
} from '../../generated/signals'
import type { VisualizationEnvelope, VisualizationSpatialSelectionCommand, VisualizationSpatialSelectionState } from '../../generated/visualization'
import { DatastarLit } from '../shared/datastar-lit'
import { domainEvents, emitDomainEvent } from '../shared/events'
import { checkSignalContract } from '../shared/signal-contract'
import { agentIcon } from '../chat/agent-icon'
import '../navigation/sub-sidebar'
import '../chat/chat-drawer'
import './filters/filter-dock'
import './filters/filter-control'
import { DashboardFilterController } from './filters/filter-controller'
import type { FilterMutationDetail } from './filters/filter-control'
import type { FilterOptionsNeededDetail } from './filters/filter-control'
import './report-canvas'
import './report-footer'
import './visual-modal'
import type { VisualActionDetail } from './visual-modal'
import './visualization/host'
import { DashboardVisualizationSignalDecoder } from './visualization/signal-envelope'
import {
  applyOptimisticInteraction,
  validateInteractionCommand,
  visualizationHighlightStates,
  visualizationSelectionEntries,
  type CanonicalInteractionSelection,
  type InteractionConfigLike,
  type OptimisticInteractionCommand,
} from './interaction-selection'

const emptyStatus: DashboardStatus = {
  loading: false,
  error: '',
  generation: 0,
  lastUpdated: '',
  refreshId: '',
  setupRequired: false,
  progressPercent: 100,
}

type DashboardRenderSnapshot = {
  page: DashboardPageSignal
  filterContract: DashboardFilterContract
  filterState: DashboardFilterState
  filterOptionPages: Record<string, DashboardFilterOptionPage>
  visuals: Record<string, VisualizationEnvelope>
  status: DashboardStatus
}

type DashboardRefreshProgress = {
  active: boolean
  complete: boolean
  generation: number
  percent: number
}

const dashboardAgentStorageKey = 'leapview-dashboard-agent-state'

type DashboardAgentStoredState = {
  open: boolean
  conversationId: string
}

function readDashboardAgentState(): DashboardAgentStoredState {
  const fallback = { open: false, conversationId: '' }
  try {
    const value = JSON.parse(localStorage.getItem(dashboardAgentStorageKey) ?? '') as Partial<DashboardAgentStoredState>
    return {
      open: value.open === true,
      conversationId: typeof value.conversationId === 'string' ? value.conversationId.trim() : '',
    }
  } catch {
    return fallback
  }
}

function writeDashboardAgentState(state: DashboardAgentStoredState): void {
  try {
    localStorage.setItem(dashboardAgentStorageKey, JSON.stringify(state))
  } catch {
    // Storage can be unavailable in privacy-constrained browser contexts. The
    // drawer remains fully functional for the current page in that case.
  }
}

class LeapViewDashboardPage extends DatastarLit(LitElement) {
  @property({ type: String, reflect: true }) presentation: 'app' | 'public' | 'embed' = 'app'
  @state() private unsupportedKinds = new Set<string>()
  @state() private optimisticSelections: CanonicalInteractionSelection[] | null = null
  @state() private optimisticSpatialSelections: VisualizationSpatialSelectionState[] | null = null
  @state() private agentDrawerOpen = false
  @state() private agentReferences: AgentReferenceSignal[] = []
  private agentStateInitialized = false
  private agentRestoreDispatched = false
  private restoredAgentConversationID = ''
  private persistedAgentOpen = false
  private persistedAgentConversationID = ''
  private optimisticExpectedGeneration = 0
  private optimisticRollbackTimer?: ReturnType<typeof setTimeout>
  private renderSnapshot?: DashboardRenderSnapshot
  private filterStateFingerprint = ''
  private filterValidationMutationID = ''
  private pendingPageNavigation = ''
  private pendingPageID = ''
  private navigationRequested = false
  private readonly filterOptionGenerations = new Map<string, number>()
  private readonly filterController = new DashboardFilterController((command) => {
    this.dispatchEvent(new CustomEvent('lv-filter-command', {
      bubbles: true, composed: true, detail: command,
    }))
    this.requestUpdate()
  })
  private readonly visualizationDecoder = new DashboardVisualizationSignalDecoder()

  static styles = css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      background: var(--lv-bg-app);
    }

    .route {
      position: relative;
      display: grid;
      min-height: 100svh;
      grid-template-columns: auto minmax(0, 1fr) 0px;
      background: var(--lv-bg-app);
      transition: grid-template-columns var(--lv-duration-fast) var(--motion-easing-move);
    }

    .publication-attribution {
      position: fixed;
      right: var(--base-size-12);
      bottom: var(--base-size-12);
      z-index: 4;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      padding: var(--base-size-4) var(--base-size-8);
      text-decoration: none;
      box-shadow: var(--shadow-resting-small);
      font: var(--lv-type-caption);
    }

    .publication-attribution:hover,
    .publication-attribution:focus-visible {
      color: var(--lv-fg-default);
      text-decoration: underline;
    }

    :host([presentation='embed']) .header,
    :host([presentation='embed']) lv-report-footer {
      display: none;
    }

    :host([presentation='embed']) .main {
      grid-template-rows: minmax(0, 1fr);
    }

    .route.agent-open {
      grid-template-columns: auto minmax(0, 1fr) var(--lv-dashboard-agent-width);
    }

    .main {
      display: grid;
      min-width: 0;
      height: 100svh;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr) auto;
      overflow: hidden;
      background: var(--lv-bg-app);
    }

    .header {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--lv-space-control) var(--base-size-16);
    }

    .title-block {
      min-width: 0;
    }

    h1,
    h2,
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

    .detail {
      margin-top: var(--base-size-4);
      overflow: hidden;
      color: var(--lv-fg-muted);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body-compact);
    }

    .actions {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: flex-end;
      gap: var(--base-size-8);
    }

    button {
      font: inherit;
    }

    .icon-button {
      display: inline-grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      min-height: var(--control-medium-size);
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0;
    }

    .icon-button:hover,
    .icon-button:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

		.agent-toggle {
			display: inline-flex;
			width: auto;
			align-items: center;
			justify-content: center;
			gap: var(--base-size-6);
			border-color: var(--lv-line-muted);
			background: var(--lv-bg-control, var(--lv-bg-panel-muted));
			padding-inline: var(--base-size-12);
			font: var(--lv-type-body);
			font-weight: var(--base-text-weight-medium);
		}

		.agent-toggle[aria-expanded='true'] {
			width: var(--control-medium-size);
			padding-inline: 0;
			background: var(--lv-bg-control-hover);
		}

		.agent-toggle[aria-expanded='true'] span {
			display: none;
		}

		.agent-toggle svg,
		.ask-visual svg {
			width: var(--base-size-16);
			height: var(--base-size-16);
		}

		.ask-visual {
			display: inline-flex;
			height: var(--lv-button-height-xs, var(--control-xsmall-size));
			min-height: var(--lv-button-height-xs, var(--control-xsmall-size));
			align-items: center;
			gap: var(--base-size-4);
			border: var(--borderWidth-default, var(--lv-border-width)) solid transparent;
			border-radius: var(--lv-radius-tight);
			background: transparent;
			color: var(--lv-button-invisible-icon-rest, var(--lv-icon-muted));
			cursor: pointer;
			opacity: 0;
			pointer-events: none;
			padding: 0 var(--base-size-6);
			font: var(--lv-type-caption);
			font-weight: var(--base-text-weight-medium);
			line-height: 1;
			transition: opacity var(--lv-transition-fast), background-color var(--lv-transition-fast), color var(--lv-transition-fast);
		}

		lv-dashboard-visual-frame:hover .ask-visual,
		lv-dashboard-visual-frame:focus-within .ask-visual,
		.ask-visual:focus-visible,
		lv-dashboard-visual-frame[data-agent-referenced] .ask-visual {
			opacity: 1;
			pointer-events: auto;
		}

		.ask-visual:hover,
		.ask-visual:focus-visible,
		.ask-visual[aria-pressed='true'] {
			border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-default)));
			background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover, var(--lv-bg-panel-muted)));
			color: var(--lv-icon-default, var(--lv-fg-default));
			outline: 0;
		}

		.ask-visual:focus-visible {
			outline: var(--focus-outline, var(--lv-border-default));
			outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
			outline-offset: var(--focus-outline-offset, var(--base-size-2));
		}

    .icon-button[disabled] {
      cursor: not-allowed;
      color: var(--lv-fg-muted);
      opacity: 0.64;
    }

    .body {
      position: relative;
      display: grid;
      min-width: 0;
      min-height: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: stretch;
      overflow: hidden;
    }

    lv-filter-dock {
      grid-column: 2;
      grid-row: 1;
    }

    .dashboard-refresh-progress {
      position: absolute;
      inset: 0 0 auto;
      z-index: var(--zIndex-sticky, 50);
      height: 2px;
      overflow: hidden;
      background: var(--lv-line-muted);
      opacity: 0;
      pointer-events: none;
      transition: opacity var(--motion-transition-stateChange);
      transition-delay: 0s;
    }

    .dashboard-refresh-progress[data-active='true'] {
      opacity: 1;
      transition-delay: 0s;
    }

    .dashboard-refresh-progress[data-active='false'][data-complete='true'] {
      transition-delay: 180ms;
    }

    .dashboard-refresh-progress-value {
      width: 0;
      height: 100%;
      background: var(--lv-line-accent);
      transition: width var(--motion-transition-stateChange);
    }

    .filter-validation {
      position: absolute;
      z-index: var(--zIndex-sticky, 50);
      top: var(--base-size-8);
      left: 50%;
      max-width: min(36rem, calc(100% - var(--base-size-24)));
      border: var(--lv-border-danger);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-danger);
      padding: var(--base-size-8) var(--base-size-12);
      box-shadow: var(--shadow-floating-small);
      font: var(--lv-type-body);
      transform: translateX(-50%);
    }

    .canvas-wrap {
      display: grid;
      grid-column: 1;
      grid-row: 1;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      background: transparent;
      padding: var(--base-size-20) var(--base-size-24);
    }

    .heading-visual {
      display: grid;
      height: 100%;
      min-height: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--base-size-12);
      padding: var(--base-size-8);
    }

    .eyebrow {
      margin-bottom: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
    }

    .heading-visual h2 {
      color: var(--lv-fg-default);
      font: var(--lv-type-title-large);
    }

    .badges {
      display: flex;
      flex-wrap: wrap;
      justify-content: flex-end;
      gap: var(--base-size-8);
    }

    .badge {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      padding: var(--base-size-2) var(--base-size-8);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
    }

    .unsupported {
      display: grid;
      height: 100%;
      place-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      padding: var(--base-size-16);
      text-align: center;
      font: var(--lv-type-body);
    }

    @media (max-width: 640px) {
      .route,
			.route.agent-open,
      .body {
        grid-template-columns: 1fr;
      }

      lv-filter-dock {
        grid-column: 1;
        grid-row: 1;
      }

      .filter-validation {
        position: static;
        grid-row: 2;
        justify-self: center;
        margin: var(--base-size-8);
        transform: none;
      }

      .main {
        height: auto;
        min-height: 0;
        overflow: visible;
      }

      .canvas-wrap {
        grid-row: 3;
        padding: var(--base-size-12);
        overflow: auto;
      }

    }

    @media (prefers-reduced-motion: reduce) {
      .route,
      .dashboard-refresh-progress,
      .dashboard-refresh-progress-value {
        transition: none;
      }

			.ask-visual {
				transition: none;
			}
    }

		@media (hover: none), (pointer: coarse) {
			.ask-visual {
				opacity: 1;
				pointer-events: auto;
			}
		}
  `

  connectedCallback(): void {
    if (this.presentation === 'app' && !this.agentStateInitialized) {
      const stored = readDashboardAgentState()
      this.agentDrawerOpen = stored.open
      this.restoredAgentConversationID = stored.conversationId
      this.persistedAgentOpen = stored.open
      this.persistedAgentConversationID = stored.conversationId
      this.agentStateInitialized = true
    }
    super.connectedCallback()
    this.addEventListener('lv-interaction-select', this.handleOptimisticInteraction as EventListener, { capture: true })
    this.addEventListener('lv-interaction-spatial-select', this.handleOptimisticSpatialInteraction as EventListener, { capture: true })
    this.addEventListener('lv-filter-mutate', this.handleFilterMutation as EventListener, { capture: true })
    this.addEventListener('lv-filter-options-needed', this.handleFilterOptionsNeeded as EventListener, { capture: true })
    this.loadRenderedComponents()
  }

  disconnectedCallback(): void {
    this.removeEventListener('lv-interaction-select', this.handleOptimisticInteraction as EventListener, { capture: true })
    this.removeEventListener('lv-interaction-spatial-select', this.handleOptimisticSpatialInteraction as EventListener, { capture: true })
    this.removeEventListener('lv-filter-mutate', this.handleFilterMutation as EventListener, { capture: true })
    this.removeEventListener('lv-filter-options-needed', this.handleFilterOptionsNeeded as EventListener, { capture: true })
    this.clearOptimisticRollbackTimer()
    super.disconnectedCallback()
  }

  updated(): void {
    const agent = this.presentation === 'app'
      ? this.signal<{ activeConversationId?: string } | null>('agent', null)
      : null
    if (agent !== null) {
      const activeConversationID = agent.activeConversationId?.trim() ?? ''
      if (activeConversationID) {
        this.restoredAgentConversationID = activeConversationID
        this.persistAgentState()
      }
      if (this.restoredAgentConversationID && !this.agentRestoreDispatched) {
        this.agentRestoreDispatched = true
        emitDomainEvent(this, domainEvents.chatRestore, {
          conversationId: this.restoredAgentConversationID,
        })
      }
    }
    const page = this.page
    if (!page) return
    checkSignalContract('dashboard page', page, {
      dashboardId: 'required',
      pageId: 'required',
      components: 'required',
    })
    this.loadRenderedComponents()
    this.reconcileFilterController()
    if (this.pendingPageID && page.pageId === this.pendingPageID) {
      const path = new URL(this.pendingPageNavigation, window.location.href).pathname
      window.DatastarURLSync?.push(this.signal<Record<string, string | string[]>>('urlParams', {}), path)
      this.pendingPageNavigation = ''
      this.pendingPageID = ''
      this.navigationRequested = false
      return
    }
    if (
      this.pendingPageNavigation
      && this.canonicalFilterState.dirtyBindings.length === 0
      && !this.filterController.pending
      && !this.navigationRequested
    ) {
      this.dispatchPageNavigation()
      return
    }
    if (this.optimisticSelections && this.status.generation >= this.optimisticExpectedGeneration) {
      this.clearOptimisticState()
    }
  }

  get page(): DashboardPageSignal | null {
    return this.signal<DashboardPageSignal | null>('page', null)
  }

	private get agentContext(): AgentContextSignal | null {
		return this.signal<AgentContextSignal | null>('agentContext', null)
	}

  private get filterContract(): DashboardFilterContract {
    return this.signal<DashboardFilterContract>('filterContract', {
      applicationMode: 'immediate', definitions: {}, bindings: {},
    })
  }

  private get canonicalFilterState(): DashboardFilterState {
    return this.signal<DashboardFilterState>('filterState', {
      revision: 0, appliedControls: {}, draftControls: {}, dirtyBindings: [], defaultsRevision: '',
    })
  }

  private get filterValidation(): DashboardFilterValidationResult {
    return this.signal<DashboardFilterValidationResult>('filterValidation', {
      accepted: true,
      message: '',
      currentRevision: this.canonicalFilterState.revision,
      clientMutationID: '',
    })
  }

  private get filterOptionPages(): Record<string, DashboardFilterOptionPage> {
    return this.signal<Record<string, DashboardFilterOptionPage>>('filterOptionPages', {})
  }

  private get currentFilterOptionPages(): Record<string, DashboardFilterOptionPage> {
    const runtime = this.signal<RouteRuntimeSignal>('runtime', { kind: 'dashboard' })
    return Object.fromEntries(Object.entries(this.filterOptionPages).filter(([key, page]) =>
      page.bindingKey === key
      && page.servingStateID === (runtime.servingStateId ?? '')
      && page.streamGeneration === this.status.generation
      && page.filterRevision === this.canonicalFilterState.revision
      && page.requestGeneration === (this.filterOptionGenerations.get(key) ?? page.requestGeneration)))
  }

  private get interactionSelections(): DashboardInteractionSelection[] {
    return this.signal<DashboardInteractionSelection[]>('interactionSelections', [])
  }

  private get spatialSelections(): VisualizationSpatialSelectionState[] {
    return this.signal<VisualizationSpatialSelectionState[]>('spatialSelections', [])
  }

  private get visuals(): Record<string, VisualizationEnvelope> {
    return this.visualizationDecoder.decodeAll(
      this.signal<Record<string, DashboardVisualizationSignal>>('visuals', {}),
    )
  }

  private get visualSignals(): Record<string, DashboardVisualizationSignal> {
    return this.signal<Record<string, DashboardVisualizationSignal>>('visuals', {})
  }

  private get status(): DashboardStatus {
    return this.signal<DashboardStatus>('status', emptyStatus)
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    this.filterController.setDefaults(Object.fromEntries(
      Object.values(this.filterContract.bindings).map(binding => [binding.key, binding.default]),
    ))
    const snapshot: DashboardRenderSnapshot = {
      page,
      filterContract: this.filterContract,
      filterState: this.filterController.projected.revision > 0
        ? this.filterController.projected
        : this.canonicalFilterState,
      filterOptionPages: this.currentFilterOptionPages,
      visuals: this.visuals,
      status: this.status,
    }
    this.renderSnapshot = snapshot
    const refreshProgress = this.refreshProgress(snapshot)
    const agentEnabled = this.presentation === 'app'
    return html`
			<div class=${`route${agentEnabled && this.agentDrawerOpen ? ' agent-open' : ''}`}>
        <lv-sub-sidebar .config=${this.pageSidebar(page)} @click=${this.handlePageNavigation}></lv-sub-sidebar>
        <section class="main" aria-label="LeapView report canvas">
          <header class="header">
            <div class="title-block">
              <h1>${page.title}</h1>
              <p class="detail">${page.headerDetail}</p>
            </div>
						${agentEnabled ? html`<div class="actions">
							<button
								type="button"
								class="icon-button agent-toggle"
								aria-label="Toggle dashboard agent"
								aria-expanded=${String(this.agentDrawerOpen)}
								@click=${() => { this.setAgentDrawerOpen(!this.agentDrawerOpen) }}
							>${agentIcon()}<span>Ask</span></button>
						</div>` : nothing}
          </header>
          <div class="body">
            ${this.renderRefreshProgress(refreshProgress)}
            ${this.renderFilterValidation()}
            ${this.renderFilterDock()}
            <div class="canvas-wrap">
              <lv-report-canvas width=${page.canvas.width} height=${page.canvas.height}>
                ${page.components.map((component) => this.renderCanvasComponent(component))}
              </lv-report-canvas>
            </div>
          </div>
          <lv-report-footer .status=${snapshot.status}></lv-report-footer>
        </section>
				${agentEnabled ? html`<lv-chat-drawer
					?open=${this.agentDrawerOpen}
					.suggestions=${this.agentSuggestions(page)}
					@lv-chat-drawer-close=${() => { this.setAgentDrawerOpen(false) }}
					@lv-chat-new=${this.handleAgentNew}
					@lv-agent-references-change=${this.handleAgentReferencesChanged}
				></lv-chat-drawer>` : nothing}
        ${this.presentation !== 'app' ? html`
          <a class="publication-attribution" href="https://leapview.dev" target="_blank" rel="noreferrer">Powered by LeapView</a>
        ` : nothing}
      </div>
      <lv-visual-modal></lv-visual-modal>
    `
  }

  private renderRefreshProgress(progress: DashboardRefreshProgress) {
    const valueText = `${Math.round(progress.percent)}% of dashboard refresh complete`
    return html`
      <div
        class="dashboard-refresh-progress"
        data-dashboard-refresh-progress
        data-active=${String(progress.active)}
        data-complete=${String(progress.complete)}
        data-generation=${progress.generation}
        role="progressbar"
        aria-label="Refreshing dashboard"
        aria-hidden=${String(!progress.active)}
        aria-valuemin="0"
        aria-valuenow=${progress.percent}
        aria-valuemax="100"
        aria-valuetext=${valueText}
      >
        <div
          class="dashboard-refresh-progress-value"
          style=${`width:${progress.percent}%`}
        ></div>
      </div>
    `
  }

  private renderFilterValidation() {
    const validation = this.filterValidation
    if (validation.accepted || !validation.message) return nothing
    return html`<div class="filter-validation" role="alert">${validation.message}</div>`
  }

  private refreshProgress(snapshot: DashboardRenderSnapshot): DashboardRefreshProgress {
    const percent = snapshot.status.progressPercent ?? (snapshot.status.loading ? 0 : 100)
    return {
      active: snapshot.status.loading,
      complete: !snapshot.status.loading && percent === 100,
      generation: snapshot.status.generation,
      percent,
    }
  }

  private pageSidebar(page: DashboardPageSignal) {
    return {
      label: 'Pages',
      railLabel: 'Pages',
      ariaLabel: 'Report pages',
      storageKey: 'leapview-report-sidebar-collapsed',
      activeId: page.pageId,
      items: page.pages.map((item: DashboardPageNavSignal) => ({
        id: item.id,
        title: item.title,
        href: item.href,
        active: item.active,
      })) ?? [],
    }
  }

  private handlePageNavigation = (event: MouseEvent): void => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    const anchor = event.composedPath().find((node): node is HTMLAnchorElement => node instanceof HTMLAnchorElement)
    if (!anchor?.href) return
    const target = this.page?.pages.find((item) => new URL(item.href, window.location.href).href === anchor.href)
    if (!target || target.active) return
    event.preventDefault()
    event.stopPropagation()
    this.pendingPageNavigation = anchor.href
    this.pendingPageID = target.id
    this.navigationRequested = false
    if (this.filterContract.applicationMode === 'deferred' && this.canonicalFilterState.dirtyBindings.length > 0) {
      if (window.confirm('Apply pending filter changes before leaving this page?')) {
        this.filterController.apply()
        this.requestUpdate()
        return
      }
      if (window.confirm('Discard pending filter changes and leave this page?')) {
        this.filterController.cancel()
        this.requestUpdate()
        return
      }
      this.pendingPageNavigation = ''
      this.pendingPageID = ''
      return
    }
    this.dispatchPageNavigation()
  }

  private dispatchPageNavigation(): void {
    if (!this.pendingPageID || this.navigationRequested) return
    this.navigationRequested = true
    this.dispatchEvent(new CustomEvent('lv-page-navigate', {
      bubbles: true,
      composed: true,
      detail: {
        pageID: this.pendingPageID,
        baseFilterRevision: this.canonicalFilterState.revision,
        clientMutationID: crypto.randomUUID(),
      },
    }))
    this.requestUpdate()
  }

  private renderCanvasComponent(component: DashboardComponentSignal) {
    const filterVisual = component.kind === 'slicer'
    const visualType = component.visual ? this.visuals[component.visual]?.spec.kind ?? '' : ''
		const currentPage = this.renderSnapshot?.page ?? this.page
		const askReference = currentPage ? this.agentReference(component, currentPage) : undefined
		const referenced = askReference ? this.agentReferences.some((reference) => reference.reference.workspaceId === askReference.reference.workspaceId
			&& reference.reference.type === askReference.reference.type && reference.reference.id === askReference.reference.id) : false
    return html`
              <lv-dashboard-visual-frame
                data-canvas-visual
                data-component-kind=${component.kind}
                data-visual-type=${visualType}
                data-slicer-style=${component.kind === 'slicer' ? component.presentation?.style ?? '' : nothing}
		data-visual-id=${component.visual || nothing}
        ?data-canvas-filter-visual=${filterVisual}
        data-x=${component.x}
        data-y=${component.y}
        data-w=${component.width}
        data-h=${component.height}
        .transparent=${component.kind === 'header'}
		?data-agent-referenced=${referenced}
		@lv-agent-reference=${this.handleAgentReference}
      >
        ${this.renderComponentContent(component, askReference, referenced)}
      </lv-dashboard-visual-frame>
    `
  }

  private renderComponentContent(component: DashboardComponentSignal, askReference?: AgentReferenceSignal, referenced = false) {
    switch (component.kind) {
      case 'header':
        return this.renderHeadingComponent(component)
      case 'slicer':
        return this.renderSlicer(component)
      case 'visual': {
        const visual = this.visualFor(component)
        if (!visual) return this.missingPayload('visual')
        return html`<lv-visualization-host .envelope=${visual} .openVisualFocus=${this.openVisualFocus}>${this.renderAskAction(askReference, referenced)}</lv-visualization-host>`
      }
      default:
        return html`<div class="unsupported">Unsupported dashboard component: ${component.kind}</div>`
    }
  }

  private renderHeadingComponent(component: DashboardComponentSignal) {
    return html`
      <div class="heading-visual">
        <div>
          <p class="eyebrow">${component.eyebrow || 'LeapView report'}</p>
          <h2>${component.title || 'Dashboard'}</h2>
        </div>
        <div class="badges">
          ${(component.badges ?? []).map((badge) => html`<span class="badge">${badge}</span>`)}
        </div>
      </div>
    `
  }

  private openVisualFocus = (source: HTMLElement, detail: VisualActionDetail): void => {
    this.renderRoot.querySelector('lv-visual-modal')?.openVisualFocus(source, detail)
  }

  private renderSlicer(component: DashboardComponentSignal) {
    const reference = component.binding
    if (!reference) return this.missingPayload('slicer binding')
    const snapshot = this.renderSnapshot
    const binding = Object.values(snapshot?.filterContract.bindings ?? {}).find((candidate) =>
      candidate.scope === reference.scope
      && candidate.id === reference.id
      && (candidate.scope === 'report' || candidate.pageID === snapshot?.page.pageId))
    if (!binding) return this.missingPayload('slicer binding')
    const definition = snapshot?.filterContract.definitions[binding.filter]
    if (!definition) return this.missingPayload('slicer definition')
    const state = snapshot?.filterState
    const expression = state?.draftControls[binding.key]
      ?? state?.appliedControls[binding.key]?.expression
      ?? binding.default
    return html`<lv-slicer
      .definition=${definition}
      .binding=${binding}
      .expression=${expression}
      .options=${snapshot?.filterOptionPages[binding.key]}
      .presentation=${component.presentation}
      .pending=${this.filterController.pending}
      .stale=${(snapshot?.status.loading ?? false)}
    ></lv-slicer>`
  }

	private renderAskAction(reference?: AgentReferenceSignal, referenced = false) {
		if (this.presentation !== 'app' || !reference) return nothing
		return html`
			<button
				slot="agent-action"
				class="ask-visual"
				type="button"
				aria-label=${`Ask about ${reference.name}`}
				aria-pressed=${String(referenced)}
				title=${`Ask about ${reference.name}`}
				@click=${(event: MouseEvent) => this.dispatchAgentReference(event, reference)}
			>
				${agentIcon()}<span>Ask</span>
			</button>
		`
	}

	private dispatchAgentReference(event: MouseEvent, reference: AgentReferenceSignal) {
		event.preventDefault()
		event.stopPropagation()
		;(event.currentTarget as HTMLElement).dispatchEvent(new CustomEvent('lv-agent-reference', {
			bubbles: true,
			composed: true,
			detail: reference,
		}))
	}

  private renderFilterDock() {
    return html`
      <lv-filter-dock
        .loading=${(this.renderSnapshot?.status ?? this.status).loading}
        .pending=${this.filterController.pending}
        .contract=${this.renderSnapshot?.filterContract ?? this.filterContract}
        .filterState=${this.renderSnapshot?.filterState ?? this.canonicalFilterState}
        .optionPages=${this.renderSnapshot?.filterOptionPages ?? this.filterOptionPages}
        .pageId=${(this.renderSnapshot?.page ?? this.page)?.pageId ?? ''}
        @lv-filter-clear=${this.handleFilterClear}
        @lv-filter-reset-binding=${this.handleFilterResetBinding}
        @lv-filter-reset-scope=${this.handleFilterResetScope}
        @lv-filter-apply=${this.handleFilterApply}
        @lv-filter-cancel=${this.handleFilterCancel}
      ></lv-filter-dock>
    `
  }

	private agentSuggestions(page: DashboardPageSignal): AgentReferenceSignal[] {
		return page.components
			.map((component) => this.agentReference(component, page))
			.filter((reference): reference is AgentReferenceSignal => Boolean(reference))
	}

	private agentReference(component: DashboardComponentSignal, page: DashboardPageSignal): AgentReferenceSignal | undefined {
		if (this.presentation !== 'app') return undefined
		if (component.kind !== 'visual' || !component.visual) return undefined
		const visual = this.visuals[component.visual]
		if (!visual) return undefined
		const workspaceId = this.agentContext?.workspaceId ?? ''
		const href = `/workspaces/${encodeURIComponent(workspaceId)}/dashboards/${encodeURIComponent(page.dashboardId)}/pages/${encodeURIComponent(page.pageId)}`
		return {
			reference: { workspaceId, type: 'visual', id: `${page.dashboardId}.${component.visual}` },
			name: component.title || visual.spec.title || component.visual,
			visualType: visualizationType(visual),
			workspace: { id: workspaceId, name: workspaceId },
			hierarchy: [workspaceId, this.agentContext?.dashboardTitle ?? page.dashboardTitle, page.pageTitle].filter(Boolean),
			href,
			locations: [{ dashboardId: page.dashboardId, dashboardName: this.agentContext?.dashboardTitle, pageId: page.pageId, pageName: page.pageTitle, href }],
			context: ['current_page', 'current_dashboard', 'current_workspace'],
		}
	}

	private handleAgentReference = (event: CustomEvent<AgentReferenceSignal>) => {
		const reference = event.detail
		if (!reference) return
		this.setAgentDrawerOpen(true)
		const drawer = this.shadowRoot?.querySelector('lv-chat-drawer') as (HTMLElement & { openWithReference(reference: AgentReferenceSignal): void }) | null
		drawer?.openWithReference(reference)
	}

	private handleAgentReferencesChanged = (event: CustomEvent<{ references: AgentReferenceSignal[] }>) => {
		this.agentReferences = [...(event.detail.references ?? [])]
	}

  private handleAgentNew = () => {
    this.restoredAgentConversationID = ''
    this.agentRestoreDispatched = true
    this.persistAgentState()
  }

  private setAgentDrawerOpen(open: boolean): void {
    this.agentDrawerOpen = open
    this.persistAgentState()
  }

  private persistAgentState(): void {
    if (
      this.persistedAgentOpen === this.agentDrawerOpen
      && this.persistedAgentConversationID === this.restoredAgentConversationID
    ) return
    this.persistedAgentOpen = this.agentDrawerOpen
    this.persistedAgentConversationID = this.restoredAgentConversationID
    writeDashboardAgentState({
      open: this.agentDrawerOpen,
      conversationId: this.restoredAgentConversationID,
    })
  }

  private missingPayload(kind: string) {
    return html`<div class="unsupported">Missing ${kind} payload</div>`
  }

  private visualFor(component: DashboardComponentSignal): VisualizationEnvelope | undefined {
    const visualMap = this.renderSnapshot?.visuals ?? this.visuals
    const visual = component.visual ? visualMap[component.visual] : undefined
    if (!visual) return undefined
    const selections = this.optimisticSelections ?? this.interactionSelections
    const spatialSelections = this.optimisticSpatialSelections ?? this.spatialSelections
    const spatialSelection = [...spatialSelections].reverse().find((selection) => selection.visualID === visual.visualID)
    const highlights = this.optimisticSelections !== null || this.optimisticSpatialSelections !== null
      ? visualizationHighlightStates(visual, visualMap, selections, spatialSelections)
      : visual.highlights
    return {
      ...visual,
      selection: visualizationSelectionEntries(visual, selections),
      highlights,
      ...(spatialSelection ? { spatialSelection } : { spatialSelection: undefined }),
    }
  }

  private handleOptimisticInteraction = (event: CustomEvent<unknown>): void => {
    if (!event.detail || typeof event.detail !== 'object') return
    const candidate = event.detail as Partial<OptimisticInteractionCommand>
    if (typeof candidate.sourceId !== 'string') return
    const source = this.visualSignals[candidate.sourceId]
    if (!source || source.filterRevision !== this.canonicalFilterState.revision || this.status.loading) return
    Object.assign(candidate, {
      specRevision: source.specRevision,
      dataRevision: source.dataRevision,
      servingStateID: source.servingStateID,
      filterRevision: this.canonicalFilterState.revision,
      interactionRevision: this.signal<number>('interactionRevision', 0),
    })
    const command = optimisticCommand(candidate)
    if (!command) return
    const configured = this.interactionConfigFor(command.sourceKind, command.sourceId)
    if (!validateInteractionCommand(command, configured)) return

    const current = this.optimisticSelections ?? this.interactionSelections
    this.optimisticSelections = applyOptimisticInteraction(current, {
      ...command,
      toggle: configured?.toggle !== false,
    })
    this.optimisticExpectedGeneration = Math.max(
      this.status.generation + 1,
      this.optimisticExpectedGeneration + 1,
    )
    this.scheduleOptimisticRollback()
  }

  private handleFilterMutation = (event: CustomEvent<FilterMutationDetail>): void => {
    if (!event.detail?.bindingKey || !event.detail.expression) return
    event.stopPropagation()
    // Clearing is a first-class mutation so textbox and drawer clears share
    // the same idempotent server path and canonical URL tombstone.
    if (event.detail.expression.kind === 'unfiltered') {
      this.filterController.clear(event.detail.bindingKey)
    } else {
      this.filterController.mutate(event.detail.bindingKey, event.detail.expression)
    }
    this.requestUpdate()
  }

  private handleFilterClear = (event: CustomEvent<{ bindingKey: string }>): void => {
    event.stopPropagation()
    const binding = this.filterContract.bindings[event.detail?.bindingKey]
    if (!binding?.readerEditable) return
    this.filterController.clear(binding.key)
    this.requestUpdate()
  }

  private handleFilterResetBinding = (event: CustomEvent<{ bindingKey: string }>): void => {
    event.stopPropagation()
    const binding = this.filterContract.bindings[event.detail?.bindingKey]
    if (!binding?.readerEditable) return
    this.filterController.resetBinding(binding.key)
    this.requestUpdate()
  }

  private handleFilterResetScope = (event: CustomEvent<{
    scope: 'page' | 'dashboard'
    bindingKeys: string[]
  }>): void => {
    event.stopPropagation()
    if (event.detail?.scope !== 'page' && event.detail?.scope !== 'dashboard') return
    const pageID = (this.renderSnapshot?.page ?? this.page)?.pageId
    const allowed = Object.values(this.filterContract.bindings)
      .filter(binding => binding.readerEditable && (
        event.detail.scope === 'dashboard'
        || (binding.scope === 'page' && binding.pageID === pageID)
      ))
      .map(binding => binding.key)
      .sort()
    this.filterController.reset(event.detail.scope, allowed)
    this.requestUpdate()
  }

  private handleFilterApply = (event: Event): void => {
    event.stopPropagation()
    if (this.filterContract.applicationMode !== 'deferred') return
    this.filterController.apply()
    this.requestUpdate()
  }

  private handleFilterCancel = (event: Event): void => {
    event.stopPropagation()
    if (this.filterContract.applicationMode !== 'deferred') return
    this.filterController.cancel()
    this.requestUpdate()
  }

  private handleFilterOptionsNeeded = (event: CustomEvent<FilterOptionsNeededDetail>): void => {
    const detail = event.detail
    if (!detail?.bindingKey) return
    event.stopPropagation()
    const generation = (this.filterOptionGenerations.get(detail.bindingKey) ?? 0) + 1
    this.filterOptionGenerations.set(detail.bindingKey, generation)
    const runtime = this.signal<{ servingStateId?: string }>('runtime', {})
    this.dispatchEvent(new CustomEvent('lv-filter-options-request', {
      bubbles: true, composed: true,
      detail: {
        ...detail,
        servingStateID: runtime.servingStateId ?? '',
        filterRevision: this.canonicalFilterState.revision,
        requestGeneration: generation,
      },
    }))
  }

  private reconcileFilterController(): void {
    const state = this.canonicalFilterState
    const fingerprint = JSON.stringify(state)
    this.filterController.setApplicationMode(this.filterContract.applicationMode)
    if (fingerprint !== this.filterStateFingerprint) {
      this.filterStateFingerprint = fingerprint
      this.filterController.reconcile(state)
      window.DatastarURLSync?.replace(this.signal<Record<string, string | string[]>>('urlParams', {}))
    }
    const validation = this.filterValidation
    if (
      !validation.accepted
      && validation.clientMutationID
      && validation.clientMutationID !== this.filterValidationMutationID
    ) {
      this.filterValidationMutationID = validation.clientMutationID
      if (this.filterController.reject(validation.clientMutationID, state)) {
        this.requestUpdate()
      }
    }
  }

  private handleOptimisticSpatialInteraction = (event: CustomEvent<unknown>): void => {
    if (!event.detail || typeof event.detail !== 'object') return
    const candidate = event.detail as Partial<VisualizationSpatialSelectionCommand>
    if (typeof candidate.visualID !== 'string') return
    const source = this.visualSignals[candidate.visualID]
    if (!source || source.filterRevision !== this.canonicalFilterState.revision || this.status.loading) return
    Object.assign(candidate, {
      specRevision: source.specRevision,
      dataRevision: source.dataRevision,
      servingStateID: source.servingStateID,
      filterRevision: this.canonicalFilterState.revision,
      interactionRevision: this.signal<number>('interactionRevision', 0),
    })
    const command = optimisticSpatialCommand(candidate)
    if (!command) return
    const visual = this.visuals[command.visualID]
    if (!visual || visual.spec.kind !== 'geographic' || visual.specRevision !== command.specRevision || visual.dataRevision !== command.dataRevision) return
    const interaction = visual.spec.spatialInteractions.find((candidate) => candidate.id === command.interactionID)
    if (!interaction || !interaction.gestures.includes(command.gesture)) return
    if (command.action === 'set' && (!command.geometry || command.geometry.kind !== command.gesture)) return

    const current = [...(this.optimisticSpatialSelections ?? this.spatialSelections)]
      .filter((selection) => selection.visualID !== command.visualID || selection.interactionID !== command.interactionID)
    if (command.action === 'set' && command.geometry) current.push({ visualID: command.visualID, interactionID: command.interactionID, geometry: command.geometry })
    this.optimisticSpatialSelections = current
    this.optimisticExpectedGeneration = Math.max(this.status.generation + 1, this.optimisticExpectedGeneration + 1)
    this.scheduleOptimisticRollback()
  }

  private interactionConfigFor(sourceKind: 'visual', sourceId: string): InteractionConfigLike | undefined {
    const interaction = this.visuals[sourceId]?.spec.interactions[0]
    if (!interaction) return undefined
    return {
      kind: interaction.id,
      toggle: interaction.mode === 'multiple',
      targets: interaction.targets,
      mappings: interaction.mappings.map((mapping) => ({
        field: mapping.targetFieldID,
        ...(mapping.targetFactID ? { fact: mapping.targetFactID } : {}),
        ...(mapping.grain ? { grain: mapping.grain } : {}),
        value: mapping.source.field,
        ...(mapping.label ? { label: mapping.label.field } : {}),
      })),
    }
  }

  private scheduleOptimisticRollback(): void {
    this.clearOptimisticRollbackTimer()
    this.optimisticRollbackTimer = setTimeout(() => this.clearOptimisticState(), 10_000)
  }

  private clearOptimisticRollbackTimer(): void {
    if (this.optimisticRollbackTimer !== undefined) clearTimeout(this.optimisticRollbackTimer)
    this.optimisticRollbackTimer = undefined
  }

  private clearOptimisticState(): void {
    this.clearOptimisticRollbackTimer()
    this.optimisticSelections = null
    this.optimisticSpatialSelections = null
    this.optimisticExpectedGeneration = this.status.generation
  }

  private loadRenderedComponents(): void {
    // Slicers and visualization hosts are statically registered by this route.
  }
}

function optimisticSpatialCommand(value: unknown): VisualizationSpatialSelectionCommand | undefined {
  if (!value || typeof value !== 'object') return undefined
  const command = value as Partial<VisualizationSpatialSelectionCommand>
  if (typeof command.visualID !== 'string' || typeof command.specRevision !== 'string' || typeof command.dataRevision !== 'number') return undefined
  if (typeof command.servingStateID !== 'string' || typeof command.filterRevision !== 'number' || typeof command.interactionRevision !== 'number') return undefined
  if (typeof command.interactionID !== 'string' || (command.gesture !== 'box' && command.gesture !== 'lasso' && command.gesture !== 'radius')) return undefined
  if (command.action !== 'set' && command.action !== 'clear') return undefined
  if (command.action === 'set' && (!command.geometry || command.geometry.kind !== command.gesture)) return undefined
  return command as VisualizationSpatialSelectionCommand
}

function optimisticCommand(value: unknown): OptimisticInteractionCommand | undefined {
  if (!value || typeof value !== 'object') return undefined
  const command = value as Partial<OptimisticInteractionCommand>
  if (command.sourceKind !== 'visual') return undefined
  if (typeof command.sourceId !== 'string' || typeof command.interactionKind !== 'string') return undefined
  if (typeof command.specRevision !== 'string' || typeof command.dataRevision !== 'number' || typeof command.servingStateID !== 'string') return undefined
  if (typeof command.filterRevision !== 'number' || typeof command.interactionRevision !== 'number') return undefined
  if (command.action !== 'set' && command.action !== 'replace' && command.action !== 'clear') return undefined
  if (typeof command.toggle !== 'boolean' || !Array.isArray(command.mappings)) return undefined
  return command as OptimisticInteractionCommand
}

function visualizationType(visual: VisualizationEnvelope): string {
  const spec = visual.spec as VisualizationEnvelope['spec'] & { mark?: unknown }
  return typeof spec.mark === 'string' && spec.mark ? spec.mark : spec.kind
}

class DashboardVisualFrame extends LitElement {
  @property({ type: Boolean, reflect: true }) transparent = false

  static styles = css`
    :host {
      display: block;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      box-sizing: border-box;
    }

    .frame {
      position: relative;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-sizing: border-box;
    }

		:host([data-agent-referenced]) .frame {
			box-shadow: inset 0 0 0 2px var(--lv-line-accent);
		}

    :host([transparent]) .frame {
      border-color: transparent;
      background: transparent;
    }

    :host([data-canvas-filter-visual]) {
      overflow: visible;
      z-index: 5;
    }

    :host([data-canvas-filter-visual]) .frame {
      overflow: visible;
    }

    ::slotted(*) {
      display: block;
      width: 100%;
      height: 100%;
    }

  `

  render() {
    return html`
      <article class="frame">
        <slot></slot>
      </article>
    `
  }
}

function tagForComponent(component: DashboardComponentSignal, visuals: Record<string, VisualizationEnvelope>): string {
  switch (component.kind) {
    case 'slicer':
      return 'lv-slicer'
    case 'visual': {
      return component.visual && visuals[component.visual] ? 'lv-visualization-host' : ''
    }
    default:
      return ''
  }
}

function json(value: unknown): string {
  return JSON.stringify(value ?? {})
}

if (!customElements.get('lv-dashboard-page')) customElements.define('lv-dashboard-page', LeapViewDashboardPage)
if (!customElements.get('lv-dashboard-visual-frame')) customElements.define('lv-dashboard-visual-frame', DashboardVisualFrame)
