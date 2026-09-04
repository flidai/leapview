import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ArrowLeft, ChevronDown, Copy, EllipsisVertical, PencilLine, SlidersHorizontal, Star } from 'lucide'
import type {
  AgentContextSignal,
  AgentReferenceSignal,
  DashboardComponentSignal,
  DashboardCompiledFilterBinding,
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
import { lucideIconByCanonicalName } from '../shared/lucide-catalog'
import { lucideIcon } from '../shared/lucide-icons'
import { breadcrumbStyles, renderBreadcrumb } from '../shared/breadcrumb'
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
import {
  DashboardAgentStateController,
  DashboardNavigationController,
  DashboardOptimisticInteractionController,
} from './dashboard-page-controller'

const emptyStatus: DashboardStatus = {
  loading: false,
  error: '',
  generation: 0,
  lastUpdated: '',
  refreshId: '',
  setupRequired: false,
  progressPercent: 100,
}

const dashboardFavoritesStorageKey = 'leapview.dashboard-catalog.favorites.v1'

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

class LeapViewDashboardPage extends DatastarLit(LitElement) {
  @property({ type: String, reflect: true }) presentation: 'app' | 'public' | 'embed' = 'app'
  @property({ type: Boolean, reflect: true, attribute: 'read-only' }) readOnly = false
  @property({ attribute: 'authoring-action-label' }) authoringActionLabel = ''
  @property({ attribute: 'authoring-action-href' }) authoringActionHref = ''
  @state() private unsupportedKinds = new Set<string>()
  @state() private optimisticSelections: CanonicalInteractionSelection[] | null = null
  @state() private optimisticSpatialSelections: VisualizationSpatialSelectionState[] | null = null
  @state() private agentDrawerOpen = false
  @state() private agentReferences: AgentReferenceSignal[] = []
  @state() private reportLayout: 'desktop' | 'mobile' = 'desktop'
  @state() private filterDockOpen = false
  @state() private dashboardFavorite = false
  @state() private dashboardOptionsOpen = false
  private favoriteDashboardID = ''
  private agentStateInitialized = false
  private agentRestoreDispatched = false
  private restoredAgentConversationID = ''
  private persistedAgentOpen = false
  private persistedAgentConversationID = ''
  private optimisticExpectedGeneration = 0
  private renderSnapshot?: DashboardRenderSnapshot
  private filterStateFingerprint = ''
  private filterValidationMutationID = ''
  private readonly navigationController = new DashboardNavigationController()
  private readonly agentStateController = new DashboardAgentStateController()
  private readonly optimisticController = new DashboardOptimisticInteractionController((snapshot) => {
    this.optimisticSelections = snapshot.selections
    this.optimisticSpatialSelections = snapshot.spatialSelections
    this.optimisticExpectedGeneration = snapshot.expectedGeneration
    this.requestUpdate()
  }, () => this.status.generation)
  private readonly filterOptionGenerations = new Map<string, number>()
  private readonly filterOptionRequestContexts = new Map<string, Map<number, string>>()
  private readonly filterOptionInFlight = new Map<string, { context: string, generation: number, startedAt: number }>()
  private readonly retainedFilterOptionPages = new Map<string, DashboardFilterOptionPage>()
  private retainedFilterOptionServingStateID = ''
  private readonly filterController = new DashboardFilterController((command) => {
    this.dispatchEvent(new CustomEvent('lv-filter-command', {
      bubbles: true, composed: true, detail: command,
    }))
    this.requestUpdate()
  })
  private readonly visualizationDecoder = new DashboardVisualizationSignalDecoder()

  static styles = [breadcrumbStyles, css`
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
      height: 100svh;
      min-height: 100svh;
      grid-template-columns: auto minmax(0, 1fr) 0px;
      grid-template-rows: auto minmax(0, 1fr) auto;
      overflow: hidden;
      background: var(--lv-bg-app);
      transition: grid-template-columns var(--lv-duration-fast) var(--motion-easing-move);
    }

    .route > .rail-footer {
      grid-column: 1;
      grid-row: 3;
    }

    .route > .header {
      grid-column: 2 / -1;
      grid-row: 1;
    }

    .route > lv-sub-sidebar {
      grid-column: 1;
      grid-row: 1 / 3;
      --lv-sub-sidebar-header-height: calc(var(--control-medium-size) + (2 * var(--lv-space-control, var(--base-size-8))) + var(--borderWidth-thin));
      --lv-sub-sidebar-header-padding-block: var(--lv-space-control, var(--base-size-8));
      --lv-sub-sidebar-nav-padding-block-start: 0px;
    }

    .route > .main {
      grid-column: 2;
      grid-row: 2;
    }

    .route > lv-chat-drawer {
      grid-column: 3;
      grid-row: 2 / 4;
    }

    .route > lv-report-footer {
      grid-column: 2;
      grid-row: 3;
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

    :host([presentation='embed']) .rail-footer,
    :host([presentation='embed']) .header,
    :host([presentation='embed']) lv-report-footer {
      display: none;
    }

    .route.agent-open {
      grid-template-columns: auto minmax(0, 1fr) var(--lv-dashboard-agent-width);
    }

    .main {
      display: grid;
      min-width: 0;
      height: 100%;
      min-height: 0;
      grid-template-rows: minmax(0, 1fr);
      overflow: hidden;
      background: var(--lv-bg-app);
    }

    .main[data-report-layout='mobile'] {
      height: 100%;
      min-height: 0;
      overflow: hidden;
    }

    .main[data-report-layout='mobile'] .body {
      overflow-x: hidden;
      overflow-y: auto;
      overscroll-behavior: contain;
    }

    .main[data-report-layout='mobile'] .canvas-wrap {
      overflow: visible;
    }

    .main[data-report-layout='mobile'] lv-filter-dock:not([data-open]) {
      position: sticky;
      top: 0;
      align-self: start;
      height: 100%;
      max-height: 100%;
    }

    .header {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding: var(--lv-space-control, var(--base-size-8)) var(--base-size-16);
    }

    .dashboard-heading {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-2);
    }

    .dashboard-heading .breadcrumb {
      min-width: 0;
    }

    .dashboard-heading .dashboard-favorite,
    .dashboard-heading .dashboard-options {
      flex: 0 0 auto;
    }

    .rail-footer {
      box-sizing: border-box;
      min-width: 0;
      contain: inline-size;
      overflow: hidden;
      border-right: var(--lv-border-muted);
      background: var(--lv-sidebar-bg);
    }

    .rail-footer {
      display: grid;
      min-height: var(--control-medium-size);
      align-items: center;
      justify-items: start;
      border-top: var(--lv-border-muted);
      padding: 0 var(--base-size-16);
    }

    .route:has(> lv-sub-sidebar[data-collapsed]) .rail-footer {
      justify-items: center;
      padding-inline: 0;
    }

    .route:has(> lv-sub-sidebar[data-collapsed]) .rail-back-link {
      display: grid;
      width: var(--control-medium-size);
      gap: 0;
      padding: 0;
    }

    .route:has(> lv-sub-sidebar[data-collapsed]) .rail-back-label {
      display: none;
    }

    .breadcrumb-root {
      flex: 0 0 auto;
    }

    .breadcrumb {
      overflow: hidden;
    }

    .breadcrumb-dashboard,
    .breadcrumb-current {
      flex: 0 1 auto;
    }

    .dashboard-appearance-glyph {
      color: var(--display-purple-fgColor);
    }

    .dashboard-appearance-glyph.appearance-color-gray { color: var(--display-gray-fgColor); }
    .dashboard-appearance-glyph.appearance-color-blue { color: var(--display-blue-fgColor); }
    .dashboard-appearance-glyph.appearance-color-green { color: var(--display-green-fgColor); }
    .dashboard-appearance-glyph.appearance-color-yellow { color: var(--display-yellow-fgColor); }
    .dashboard-appearance-glyph.appearance-color-orange { color: var(--display-orange-fgColor); }
    .dashboard-appearance-glyph.appearance-color-red { color: var(--display-red-fgColor); }
    .dashboard-appearance-glyph.appearance-color-purple { color: var(--display-purple-fgColor); }
    .dashboard-appearance-glyph.appearance-color-pink { color: var(--display-pink-fgColor); }
    .dashboard-appearance-glyph.appearance-color-coral { color: var(--display-coral-fgColor); }

    .breadcrumb-separator {
      flex: 0 0 auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
    }

    .dashboard-back-link {
      display: grid;
      width: var(--control-medium-size);
      height: var(--control-medium-size);
      flex: 0 0 auto;
      place-items: center;
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      text-decoration: none;
    }

    .dashboard-back-link:hover {
      color: var(--lv-fg-default);
    }

    .dashboard-back-link:focus-visible {
      color: var(--lv-fg-default);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .dashboard-back-link svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .rail-back-link {
      display: inline-flex;
      width: auto;
      min-width: 0;
      max-width: 100%;
      align-items: center;
      gap: var(--base-size-8);
      padding-right: var(--base-size-8);
    }

    .rail-back-label {
      display: block;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
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

    .actions {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: flex-end;
      gap: var(--base-size-8);
    }

    .icon-button.dashboard-favorite,
    .icon-button.dashboard-options-trigger {
      border-color: transparent;
      color: var(--lv-fg-muted);
    }

    .dashboard-favorite[aria-pressed='true'] {
      color: var(--display-yellow-fgColor, var(--lv-fg-default));
    }

    .dashboard-favorite[aria-pressed='true'] svg {
      fill: currentColor;
    }

    .dashboard-options {
      position: relative;
    }

    .dashboard-options-menu {
      position: absolute;
      z-index: var(--zIndex-popover, 300);
      top: calc(100% + var(--base-size-6));
      right: 0;
      display: grid;
      width: max-content;
      min-width: 12rem;
      gap: var(--base-size-2);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-6);
      box-shadow: var(--shadow-floating-small);
    }

    .dashboard-options-menu a {
      display: flex;
      min-height: var(--control-medium-size);
      align-items: center;
      gap: var(--base-size-8);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8);
      text-decoration: none;
      white-space: nowrap;
      font: var(--lv-type-body-compact);
    }

    .dashboard-options-menu a:hover,
    .dashboard-options-menu a:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .dashboard-options-menu a:focus-visible {
      outline: var(--focus-outline);
      outline-offset: calc(-1 * var(--focus-outline-offset));
    }

    .dashboard-options-menu svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
      color: var(--lv-fg-muted);
    }

    .mobile-page-menu,
    .mobile-page-label,
    .icon-button.mobile-filter-toggle {
      display: none;
    }

    .mobile-page-label {
      max-width: 9rem;
      overflow: hidden;
      color: var(--lv-fg-muted);
      padding-inline: var(--base-size-4);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
    }

    .mobile-page-menu {
      position: relative;
      flex: 0 1 auto;
      min-width: 0;
    }

    .mobile-page-menu summary {
      display: flex;
      width: auto;
      max-width: 9rem;
      height: var(--control-medium-size);
      box-sizing: border-box;
      align-items: center;
      gap: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0 var(--base-size-8);
      list-style: none;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
    }

    .mobile-page-menu summary::-webkit-details-marker {
      display: none;
    }

    .mobile-page-menu summary:hover,
    .mobile-page-menu summary:focus-visible,
    .mobile-page-menu[open] summary {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .mobile-page-menu summary:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .mobile-page-current {
      overflow: hidden;
      min-width: 0;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .mobile-page-menu summary svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
      flex: 0 0 auto;
    }

    .mobile-page-popover {
      position: absolute;
      z-index: var(--zIndex-popover, 300);
      top: calc(100% + var(--base-size-6));
      right: 0;
      display: grid;
      width: min(18rem, calc(100vw - var(--base-size-24)));
      max-height: min(60svh, 24rem);
      gap: var(--base-size-2);
      overflow-y: auto;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-6);
      box-shadow: var(--shadow-floating-small);
    }

    .mobile-page-option {
      display: flex;
      min-height: var(--control-large-size);
      align-items: center;
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      padding: 0 var(--lv-space-control);
      text-decoration: none;
      font: var(--lv-type-body);
    }

    .mobile-page-option:hover,
    .mobile-page-option:focus-visible,
    .mobile-page-option[aria-current='page'] {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .mobile-page-option[aria-current='page'] {
      color: var(--lv-fg-link);
      font-weight: var(--base-text-weight-semibold);
    }

    .mobile-filter-toggle {
      position: relative;
      width: auto;
      min-width: var(--control-medium-size);
      grid-auto-flow: column;
      gap: var(--base-size-4);
      padding-inline: var(--base-size-8);
    }

    .mobile-filter-toggle svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .mobile-filter-count {
      display: grid;
      min-width: 18px;
      min-height: 18px;
      place-items: center;
      border-radius: var(--lv-radius-full);
      background: var(--lv-line-accent);
      color: var(--lv-fg-on-emphasis);
      font: var(--lv-type-caption);
      line-height: 1;
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

    .icon-button:hover {
      background: var(--lv-bg-control-hover);
    }

    .icon-button:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
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
      padding: 0;
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

      .route,
      .route.agent-open {
        height: 100svh;
        min-height: 0;
        grid-template-rows: auto minmax(0, 1fr) auto;
        overflow: hidden;
      }

      .route > .rail-footer {
        display: none;
      }

      .route > .header {
        grid-column: 1;
        grid-row: 1;
      }

      .route > lv-report-footer {
        grid-column: 1;
        grid-row: 3;
      }

      :host(:not([presentation='embed'])) .route > lv-sub-sidebar {
        display: none;
      }

      :host(:not([presentation='embed'])) .header {
        position: relative;
        z-index: var(--zIndex-sticky, 50);
        min-height: var(--control-large-size);
        padding: var(--base-size-8) var(--base-size-12);
      }

      :host(:not([presentation='embed'])) .dashboard-back-link {
        width: var(--control-medium-size);
        height: var(--control-medium-size);
      }

      :host(:not([presentation='embed'])) .actions {
        gap: var(--base-size-4);
      }

      :host(:not([presentation='embed'])) .mobile-page-menu {
        display: block;
      }

      :host(:not([presentation='embed'])) .mobile-page-label {
        display: block;
      }

      :host(:not([presentation='embed'])) .mobile-filter-toggle {
        display: inline-grid;
      }

      :host(:not([presentation='embed'])) .mobile-filter-toggle,
      :host(:not([presentation='embed'])) .agent-toggle,
      :host(:not([presentation='embed'])) .dashboard-favorite,
      :host(:not([presentation='embed'])) .dashboard-options-trigger {
        width: var(--control-medium-size);
        padding-inline: 0;
      }

      :host(:not([presentation='embed'])) .mobile-filter-label,
      :host(:not([presentation='embed'])) .agent-toggle span {
        display: none;
      }

      :host(:not([presentation='embed'])) .mobile-filter-count {
        position: absolute;
        top: calc(-1 * var(--base-size-4));
        right: calc(-1 * var(--base-size-4));
      }

      :host(:not([presentation='embed'])) lv-filter-dock:not([data-open]) {
        position: absolute;
        width: 0;
        height: 0;
        min-height: 0;
        overflow: hidden;
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

      .route > .main {
        grid-column: 1;
        height: 100%;
        min-height: 0;
        grid-row: 2;
        overflow: hidden;
      }

      .main[data-report-layout='mobile'] {
        height: 100%;
      }

      :host([slot='page'][presentation='app']) {
        height: 100%;
        min-height: 0;
      }

      :host([slot='page'][presentation='app']) .route,
      :host([slot='page'][presentation='app']) .route.agent-open {
        height: 100%;
      }

      .canvas-wrap {
        grid-row: 3;
        padding: var(--base-size-12);
        overflow: hidden;
      }

      :host(:not([presentation='embed'])) .canvas-wrap {
        grid-row: 1;
      }

      lv-chat-drawer:not([open]) {
        display: none;
      }

      .route > lv-chat-drawer {
        grid-column: 1;
        grid-row: 2 / 4;
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
  `]

  connectedCallback(): void {
    if (this.presentation === 'app' && !this.agentStateInitialized) {
      const stored = this.agentStateController.initialize(true)
      this.agentDrawerOpen = stored.open
      this.restoredAgentConversationID = stored.conversationId
      this.persistedAgentOpen = stored.open
      this.persistedAgentConversationID = stored.conversationId
      this.agentStateInitialized = true
    }
    super.connectedCallback()
    document.addEventListener('pointerdown', this.handleMobilePageMenuPointerDown, true)
    document.addEventListener('keydown', this.handleMobilePageMenuKeyDown, true)
    document.addEventListener('pointerdown', this.handleDashboardOptionsPointerDown, true)
    document.addEventListener('keydown', this.handleDashboardOptionsKeyDown, true)
    window.addEventListener('storage', this.handleDashboardFavoriteStorage)
    this.addEventListener('lv-interaction-select', this.handleOptimisticInteraction as EventListener, { capture: true })
    this.addEventListener('lv-interaction-spatial-select', this.handleOptimisticSpatialInteraction as EventListener, { capture: true })
    this.addEventListener('lv-filter-mutate', this.handleFilterMutation as EventListener, { capture: true })
    this.addEventListener('lv-filter-options-needed', this.handleFilterOptionsNeeded as EventListener, { capture: true })
    this.loadRenderedComponents()
  }

  disconnectedCallback(): void {
    document.removeEventListener('pointerdown', this.handleMobilePageMenuPointerDown, true)
    document.removeEventListener('keydown', this.handleMobilePageMenuKeyDown, true)
    document.removeEventListener('pointerdown', this.handleDashboardOptionsPointerDown, true)
    document.removeEventListener('keydown', this.handleDashboardOptionsKeyDown, true)
    window.removeEventListener('storage', this.handleDashboardFavoriteStorage)
    this.removeEventListener('lv-interaction-select', this.handleOptimisticInteraction as EventListener, { capture: true })
    this.removeEventListener('lv-interaction-spatial-select', this.handleOptimisticSpatialInteraction as EventListener, { capture: true })
    this.removeEventListener('lv-filter-mutate', this.handleFilterMutation as EventListener, { capture: true })
    this.removeEventListener('lv-filter-options-needed', this.handleFilterOptionsNeeded as EventListener, { capture: true })
    this.optimisticController.dispose()
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
        this.agentStateController.syncConversation(activeConversationID)
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
    if (this.favoriteDashboardID !== page.dashboardId) {
      this.favoriteDashboardID = page.dashboardId
      this.dashboardFavorite = dashboardIsFavorite(readDashboardFavorites(), page.dashboardId)
    }
    checkSignalContract('dashboard page', page, {
      dashboardId: 'required',
      pageId: 'required',
      components: 'required',
    })
    this.loadRenderedComponents()
    this.reconcileFilterController()
    const completedNavigation = this.navigationController.complete(page.pageId)
    if (completedNavigation) {
      const path = new URL(completedNavigation.href, window.location.href).pathname
      window.DatastarURLSync?.push(this.signal<Record<string, string | string[]>>('urlParams', {}), path)
      return
    }
    if (
      this.navigationController.request
      && this.canonicalFilterState.dirtyBindings.length === 0
      && !this.filterController.pending
      && !this.navigationController.navigationRequested
    ) {
      this.dispatchPageNavigation()
      return
    }
    this.optimisticController.reconcile(this.status.generation)
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
    const servingStateID = runtime.servingStateId ?? ''
    if (this.retainedFilterOptionServingStateID !== servingStateID) {
      this.retainedFilterOptionServingStateID = servingStateID
      this.retainedFilterOptionPages.clear()
      this.filterOptionRequestContexts.clear()
      this.filterOptionInFlight.clear()
    }
    for (const [key, page] of Object.entries(this.filterOptionPages)) {
      if (page.bindingKey !== key || page.servingStateID !== servingStateID) continue
      const binding = this.filterContract.bindings[key]
      if (!binding) continue
      const requestContext = this.filterOptionRequestContexts.get(key)?.get(page.requestGeneration)
      const currentContext = this.filterOptionContext(binding)
      const currentLegacyPage = requestContext === undefined
        && page.filterRevision === this.canonicalFilterState.revision
        && page.streamGeneration === this.status.generation
      if (requestContext === currentContext || currentLegacyPage) {
        this.retainedFilterOptionPages.set(key, page)
        const inFlight = this.filterOptionInFlight.get(key)
        if (inFlight && page.requestGeneration >= inFlight.generation) {
          this.filterOptionInFlight.delete(key)
        }
      }
    }
    return Object.fromEntries([...this.retainedFilterOptionPages].filter(([key, page]) =>
      page.servingStateID === servingStateID && Boolean(this.filterContract.bindings[key])))
  }

  private get filterOptionsReady(): boolean {
    const runtime = this.signal<RouteRuntimeSignal>('runtime', { kind: 'dashboard' })
    return Boolean(runtime.servingStateId) && this.canonicalFilterState.revision > 0
  }

  private filterOptionContext(binding: DashboardCompiledFilterBinding): string {
    const pageID = this.page?.pageId ?? ''
    const bindings = Object.values(this.filterContract.bindings)
    const dependencies = binding.optionDependencies.flatMap((reference) => {
      const dependency = bindings.find((candidate) =>
        candidate.scope === reference.scope
        && candidate.id === reference.id
        && (candidate.scope === 'report' || candidate.pageID === pageID))
      if (!dependency) return []
      const applied = this.canonicalFilterState.appliedControls[dependency.key]
      if (!applied) return []
      const expression = applied.resolvedExpression?.kind
        ? applied.resolvedExpression
        : applied.expression
      if (expression.kind === 'unfiltered') return []
      return [[dependency.key, expression] as const]
    }).sort(([left], [right]) => left.localeCompare(right))
    return JSON.stringify({ pageID, dependencies })
  }

  private get filterOptionContexts(): Record<string, string> {
    return Object.fromEntries(Object.values(this.filterContract.bindings).map((binding) => [
      binding.key,
      this.filterOptionContext(binding),
    ]))
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

  private handleReportZoomState = (event: CustomEvent<{ layout?: unknown }>): void => {
    const layout = event.detail?.layout
    if ((layout === 'desktop' || layout === 'mobile') && layout !== this.reportLayout) {
      this.reportLayout = layout
    }
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
    const activeFilterCount = this.activeFilterCount(snapshot)
    return html`
			<div class=${`route${agentEnabled && this.agentDrawerOpen ? ' agent-open' : ''}`}>
          <footer class="rail-footer">
            ${this.presentation === 'app' ? html`
              <a
                class="dashboard-back-link rail-back-link"
                href="/"
                aria-label="Back to dashboards"
                title="All dashboards"
              >${lucideIcon(ArrowLeft)}<span class="rail-back-label">Back</span></a>
            ` : nothing}
          </footer>
          <header class="header">
						<div class="dashboard-heading">
						${renderBreadcrumb([
						  { label: 'Dashboards', href: '/', className: 'breadcrumb-root' },
						  {
							label: page.dashboardTitle,
							current: true,
							className: 'breadcrumb-dashboard',
							prefix: html`<span
							  class=${`breadcrumb-glyph dashboard-appearance-glyph appearance-color-${appearanceColor(page.appearanceColor)}`}
							  data-icon=${page.appearanceIcon}
							  data-color=${appearanceColor(page.appearanceColor)}
							  aria-hidden="true"
							>${lucideIcon(lucideIconByCanonicalName(page.appearanceIcon), { size: 16, strokeWidth: 1.75 })}</span>`,
						  },
						], 'Breadcrumb')}
						${this.presentation === 'app' ? this.renderDashboardHeaderActions(page) : nothing}
						</div>
						<div class="actions">
							${this.renderMobilePageMenu(page)}
							<button
								type="button"
								class="icon-button mobile-filter-toggle"
								aria-label=${activeFilterCount > 0 ? `Filters, ${activeFilterCount} active` : 'Filters'}
								aria-expanded=${String(this.filterDockOpen)}
								aria-haspopup="dialog"
								title="Filters"
								@click=${this.openFiltersFromHeader}
							>
								${lucideIcon(SlidersHorizontal)}
								<span class="mobile-filter-label">Filters</span>
								${activeFilterCount > 0 ? html`<span class="mobile-filter-count" aria-hidden="true">${activeFilterCount}</span>` : nothing}
							</button>
							${agentEnabled ? html`
							<button
								type="button"
								class="icon-button agent-toggle"
								aria-label="Toggle dashboard agent"
								aria-expanded=${String(this.agentDrawerOpen)}
								title="Ask"
								@click=${() => { this.setAgentDrawerOpen(!this.agentDrawerOpen) }}
							>${agentIcon()}<span>Ask</span></button>
							` : nothing}
						</div>
          </header>
        <lv-sub-sidebar .config=${this.pageSidebar(page)} @click=${this.handlePageNavigation}></lv-sub-sidebar>
        <section
          class="main"
          data-report-layout=${this.reportLayout}
          aria-label="LeapView report canvas"
          @lv-report-zoom-state=${this.handleReportZoomState}
        >
          <div
            class="body"
            role=${this.reportLayout === 'mobile' ? 'region' : nothing}
            aria-label=${this.reportLayout === 'mobile' ? 'Scrollable report content' : nothing}
            tabindex=${this.reportLayout === 'mobile' ? '0' : nothing}
          >
            ${this.renderRefreshProgress(refreshProgress)}
            ${this.renderFilterValidation()}
            ${this.renderFilterDock()}
            <div class="canvas-wrap">
              <lv-report-canvas
                .width=${page.canvas.width}
                .height=${page.canvas.height}
                .columns=${page.grid.columns}
                .rowHeight=${page.grid.rowHeight}
                .gap=${page.grid.gap}
                .padding=${page.grid.padding}
              >
                ${page.components.map((component) => this.renderCanvasComponent(component))}
              </lv-report-canvas>
            </div>
          </div>
        </section>
        <lv-report-footer .status=${snapshot.status}></lv-report-footer>
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
      widthStorageKey: 'leapview-report-sidebar-width',
      activeId: page.pageId,
      items: page.pages.map((item: DashboardPageNavSignal) => ({
        id: item.id,
        title: item.title,
        href: item.href,
        active: item.active,
      })) ?? [],
    }
  }

  private renderMobilePageMenu(page: DashboardPageSignal) {
    const activePage = page.pages.find(item => item.active || item.id === page.pageId) ?? page.pages[0]
    if (!activePage) return nothing
    if (page.pages.length === 1) {
      return html`<span class="mobile-page-label" aria-label=${`Current page: ${activePage.title}`}>${activePage.title}</span>`
    }
    return html`
      <details class="mobile-page-menu" @click=${this.handleMobilePageNavigation}>
        <summary aria-label=${`Pages, current ${activePage.title}`}>
          <span class="mobile-page-current">${activePage.title}</span>
          ${lucideIcon(ChevronDown)}
        </summary>
        <nav class="mobile-page-popover" aria-label="Report pages">
          ${page.pages.map(item => html`
            <a
              class="mobile-page-option"
              href=${item.href}
              aria-current=${item.active || item.id === page.pageId ? 'page' : nothing}
            >${item.title}</a>
          `)}
        </nav>
      </details>
    `
  }

  private renderDashboardHeaderActions(page: DashboardPageSignal) {
    const favoriteLabel = this.dashboardFavorite
      ? `Remove ${page.dashboardTitle} from favorites`
      : `Add ${page.dashboardTitle} to favorites`
    return html`
      <button
        type="button"
        class="icon-button dashboard-favorite"
        aria-label=${favoriteLabel}
        aria-pressed=${String(this.dashboardFavorite)}
        title=${favoriteLabel}
        @click=${this.toggleDashboardFavorite}
      >${lucideIcon(Star)}</button>
      ${this.authoringActionLabel && this.authoringActionHref ? html`
        <div class="dashboard-options">
          <button
            type="button"
            class="icon-button dashboard-options-trigger"
            aria-label="Dashboard options"
            aria-haspopup="menu"
            aria-controls="dashboard-options-menu"
            aria-expanded=${String(this.dashboardOptionsOpen)}
            title="Dashboard options"
            @click=${() => { this.dashboardOptionsOpen = !this.dashboardOptionsOpen }}
          >${lucideIcon(EllipsisVertical)}</button>
          ${this.dashboardOptionsOpen ? html`
            <div id="dashboard-options-menu" class="dashboard-options-menu" role="menu" aria-label="Dashboard options">
              <a role="menuitem" href=${this.authoringActionHref} @click=${() => { this.dashboardOptionsOpen = false }}>
                ${this.authoringActionLabel.toLowerCase().includes('copy') ? lucideIcon(Copy) : lucideIcon(PencilLine)}
                <span>${this.authoringActionLabel}</span>
              </a>
            </div>
          ` : nothing}
        </div>
      ` : nothing}
    `
  }

  private toggleDashboardFavorite = (): void => {
    const dashboardID = this.page?.dashboardId.trim() ?? ''
    if (!dashboardID) return
    const stored = readDashboardFavorites()
    const wasFavorite = dashboardIsFavorite(stored, dashboardID)
    const favorites = stored.filter(id => id !== dashboardID && !id.endsWith(`:${dashboardID}`))
    if (!wasFavorite) favorites.push(dashboardID)
    this.dashboardFavorite = !wasFavorite
    writeDashboardFavorites(favorites)
  }

  private handleDashboardFavoriteStorage = (event: StorageEvent): void => {
    if (event.key !== dashboardFavoritesStorageKey || !this.favoriteDashboardID) return
    this.dashboardFavorite = dashboardIsFavorite(readDashboardFavorites(), this.favoriteDashboardID)
  }

  private handleDashboardOptionsPointerDown = (event: PointerEvent): void => {
    if (!this.dashboardOptionsOpen) return
    const options = this.renderRoot.querySelector<HTMLElement>('.dashboard-options')
    if (options && event.composedPath().includes(options)) return
    this.dashboardOptionsOpen = false
  }

  private handleDashboardOptionsKeyDown = (event: KeyboardEvent): void => {
    if (event.key !== 'Escape' || !this.dashboardOptionsOpen) return
    event.preventDefault()
    this.dashboardOptionsOpen = false
    void this.updateComplete.then(() => {
      this.renderRoot.querySelector<HTMLElement>('.dashboard-options-trigger')?.focus()
    })
  }

  private activeFilterCount(snapshot: DashboardRenderSnapshot): number {
    return Object.values(snapshot.filterContract.bindings)
      .filter(binding => binding.paneVisible && (binding.scope === 'report' || binding.pageID === snapshot.page.pageId))
      .filter(binding => {
        const expression = snapshot.filterState.draftControls[binding.key]
          ?? snapshot.filterState.appliedControls[binding.key]?.expression
          ?? binding.default
        return expression.kind !== 'unfiltered'
      }).length
  }

  private handleMobilePageNavigation = (event: MouseEvent): void => {
    const menu = event.currentTarget as HTMLDetailsElement
    const anchor = event.composedPath().find((node): node is HTMLAnchorElement => node instanceof HTMLAnchorElement)
    if (!anchor) return
    this.handlePageNavigation(event)
    menu.open = false
  }

  private handleMobilePageMenuPointerDown = (event: PointerEvent): void => {
    const menu = this.renderRoot.querySelector<HTMLDetailsElement>('.mobile-page-menu')
    if (!menu?.open || event.composedPath().includes(menu)) return
    menu.open = false
  }

  private handleMobilePageMenuKeyDown = (event: KeyboardEvent): void => {
    if (event.key !== 'Escape') return
    const menu = this.renderRoot.querySelector<HTMLDetailsElement>('.mobile-page-menu')
    if (!menu?.open) return
    event.preventDefault()
    menu.open = false
    menu.querySelector<HTMLElement>('summary')?.focus()
  }

  private openFiltersFromHeader = (event: MouseEvent): void => {
    const trigger = event.currentTarget as HTMLButtonElement
    const dock = this.renderRoot.querySelector('lv-filter-dock') as (HTMLElement & { openPanel(returnFocus?: HTMLElement): Promise<void> }) | null
    void dock?.openPanel(trigger)
  }

  private handleFilterDockState = (event: CustomEvent<{ open?: boolean }>): void => {
    this.filterDockOpen = event.detail?.open === true
  }

  private handlePageNavigation = (event: MouseEvent): void => {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    const anchor = event.composedPath().find((node): node is HTMLAnchorElement => node instanceof HTMLAnchorElement)
    if (!anchor?.href) return
    const target = this.page?.pages.find((item) => new URL(item.href, window.location.href).href === anchor.href)
    if (!target) return
    // Exact draft previews deliberately have no mutation bridge. Let their
    // revision-pinned page links perform normal document navigation instead
    // of dispatching an authoring command that nothing can handle.
    if (this.readOnly) return
    if (target.active) {
      event.preventDefault()
      return
    }
    event.preventDefault()
    event.stopPropagation()
    const decision = this.navigationController.begin({ href: anchor.href, pageId: target.id }, {
      active: target.active,
      deferred: this.filterContract.applicationMode === 'deferred',
      dirty: this.canonicalFilterState.dirtyBindings.length > 0,
      confirm: (message) => window.confirm(message),
    })
    if (decision === 'apply') {
        this.filterController.apply()
        this.requestUpdate()
        return
    }
    if (decision === 'discard') {
        this.filterController.cancel()
        this.requestUpdate()
        return
    }
    if (decision === 'cancel') {
      return
    }
    this.dispatchPageNavigation()
  }

  private dispatchPageNavigation(): void {
    const request = this.navigationController.markRequested()
    if (!request) return
    this.dispatchEvent(new CustomEvent('lv-page-navigate', {
      bubbles: true,
      composed: true,
      detail: {
        pageID: request.pageId,
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
		const referenced = askReference ? this.agentReferences.some((reference) => reference.reference.kind === askReference.reference.kind
			&& reference.reference.id === askReference.reference.id) : false
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
        data-col=${component.placement?.col ?? 0}
        data-row=${component.placement?.row ?? 0}
        data-col-span=${component.placement?.colSpan ?? 0}
        data-row-span=${component.placement?.rowSpan ?? 0}
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
      .optionContext=${this.filterOptionContext(binding)}
      .optionRequestReady=${this.filterOptionsReady}
      .pending=${this.filterController.pendingFor(binding.key)}
      .stale=${false}
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
        .externalTrigger=${this.presentation !== 'embed'}
        .loading=${(this.renderSnapshot?.status ?? this.status).loading}
        .pending=${this.filterController.pending}
        .contract=${this.renderSnapshot?.filterContract ?? this.filterContract}
        .filterState=${this.renderSnapshot?.filterState ?? this.canonicalFilterState}
        .optionPages=${this.renderSnapshot?.filterOptionPages ?? this.filterOptionPages}
        .optionContexts=${this.filterOptionContexts}
        .optionRequestReady=${this.filterOptionsReady}
        .pendingBindingKeys=${this.filterController.pendingBindingKeys}
        .pageId=${(this.renderSnapshot?.page ?? this.page)?.pageId ?? ''}
        @lv-filter-clear=${this.handleFilterClear}
        @lv-filter-reset-binding=${this.handleFilterResetBinding}
        @lv-filter-reset-scope=${this.handleFilterResetScope}
        @lv-filter-apply=${this.handleFilterApply}
        @lv-filter-cancel=${this.handleFilterCancel}
        @lv-filter-dock-state=${this.handleFilterDockState}
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
		const runtime = this.signal<RouteRuntimeSignal>('runtime', { kind: 'dashboard' })
		const projectId = runtime.projectId ?? ''
		const href = `/dashboards/${encodeURIComponent(page.dashboardId)}/pages/${encodeURIComponent(page.pageId)}`
		return {
				reference: { kind: 'visual', id: `${page.dashboardId}.${component.visual}` },
				name: component.title || visual.spec.title || component.visual,
				visualType: visualizationType(visual),
				hierarchy: [projectId, this.agentContext?.dashboardTitle ?? page.dashboardTitle, page.pageTitle].filter(Boolean),
			href,
			locations: [{ dashboardId: page.dashboardId, dashboardName: this.agentContext?.dashboardTitle, pageId: page.pageId, pageName: page.pageTitle, href }],
			context: ['current_page', 'current_dashboard', 'current_project'],
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
    this.agentStateController.newConversation()
    this.persistAgentState()
  }

  private setAgentDrawerOpen(open: boolean): void {
    this.agentDrawerOpen = open
    this.persistAgentState()
  }

  private persistAgentState(): void {
    this.agentStateController.setOpen(this.agentDrawerOpen)
    this.persistedAgentOpen = this.agentDrawerOpen
    this.persistedAgentConversationID = this.restoredAgentConversationID
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
    if (this.readOnly) return
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
    this.optimisticController.setSelections(applyOptimisticInteraction(current, {
      ...command,
      toggle: configured?.toggle !== false,
    }), this.status.generation)
  }

  private handleFilterMutation = (event: CustomEvent<FilterMutationDetail>): void => {
    if (this.readOnly) {
      event.stopPropagation()
      return
    }
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
    if (this.readOnly) return
    const binding = this.filterContract.bindings[event.detail?.bindingKey]
    if (!binding?.readerEditable) return
    this.filterController.clear(binding.key)
    this.requestUpdate()
  }

  private handleFilterResetBinding = (event: CustomEvent<{ bindingKey: string }>): void => {
    event.stopPropagation()
    if (this.readOnly) return
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
    if (this.readOnly) return
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
    if (this.readOnly) return
    if (this.filterContract.applicationMode !== 'deferred') return
    this.filterController.apply()
    this.requestUpdate()
  }

  private handleFilterCancel = (event: Event): void => {
    event.stopPropagation()
    if (this.readOnly) return
    if (this.filterContract.applicationMode !== 'deferred') return
    this.filterController.cancel()
    this.requestUpdate()
  }

  private handleFilterOptionsNeeded = (event: CustomEvent<FilterOptionsNeededDetail>): void => {
    if (this.readOnly) {
      event.stopPropagation()
      return
    }
    const detail = event.detail
    if (!detail?.bindingKey) return
    event.stopPropagation()
    const binding = this.filterContract.bindings[detail.bindingKey]
    if (!binding) return
    const context = this.filterOptionContext(binding)
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
    if (this.readOnly) return
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
    this.optimisticController.setSpatialSelections(current, this.status.generation)
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
        ...(mapping.targetDatasetID ? { dataset: mapping.targetDatasetID } : {}),
        ...(mapping.grain ? { grain: mapping.grain } : {}),
        value: mapping.source.field,
        ...(mapping.label ? { label: mapping.label.field } : {}),
      })),
    }
  }

  private clearOptimisticState(): void {
    this.optimisticController.clear(this.status.generation)
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

function appearanceColor(value: string): string {
  return ['gray', 'blue', 'green', 'yellow', 'orange', 'red', 'purple', 'pink', 'coral'].includes(value) ? value : 'purple'
}

function readDashboardFavorites(): string[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(dashboardFavoritesStorageKey) ?? '[]')
    return Array.isArray(value)
      ? value.filter((entry): entry is string => typeof entry === 'string' && Boolean(entry.trim()))
      : []
  } catch {
    return []
  }
}

function writeDashboardFavorites(favorites: string[]): void {
  try {
    localStorage.setItem(dashboardFavoritesStorageKey, JSON.stringify(favorites))
  } catch {
    // Favorites remain usable for the current view when storage is unavailable.
  }
}

function dashboardIsFavorite(favorites: string[], dashboardID: string): boolean {
  return favorites.some(id => id === dashboardID || id.endsWith(`:${dashboardID}`))
}

if (!customElements.get('lv-dashboard-page')) customElements.define('lv-dashboard-page', LeapViewDashboardPage)
if (!customElements.get('lv-dashboard-visual-frame')) customElements.define('lv-dashboard-visual-frame', DashboardVisualFrame)
