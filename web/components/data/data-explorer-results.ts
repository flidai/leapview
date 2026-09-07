import { LitElement, css, html, nothing, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { ExplorationSpec } from '../../generated/exploration'
import type {
  DataExploreCommand,
  DataExploreResultSignal,
  DataExploreStatusSignal,
} from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import type { OptimisticInteractionCommand } from '../dashboard/interaction-selection'
import { emptyDataExploreCommand } from './data-explorer-spec'
import '../dashboard/visualization/host'

export type DataExplorerResultView = 'table' | 'chart' | 'pivot' | 'details'
export type DataExplorerExecutionState = 'idle' | 'pending' | 'running' | 'stopped'

/** The small amount of catalog context that belongs beside every result view. */
export interface DataExplorerSelectedDatasetMetadata {
  id?: string
  title?: string
  grain?: string
  grainEntity?: string
  grainLabel?: string
  freshness?: string
  snapshot?: string | number
}

export interface DataExplorerResultMetadata {
  dataset?: string
  grain?: string
  freshness?: string
  snapshot?: string | number
}

export interface DataExploreInteractionDetail {
  command: OptimisticInteractionCommand
  mode: 'drill' | 'explore_from_here'
}

const emptyResult: DataExploreResultSignal = {
  columns: [],
  rows: [],
  rowsReturned: 0,
  durationMs: 0,
  requestSeq: 0,
  truncated: false,
  warnings: [],
}

const emptyStatus: DataExploreStatusSignal = {
  loading: false,
  requestSeq: 0,
  stale: false,
  state: 'idle',
}

const viewOrder: DataExplorerResultView[] = ['table', 'chart', 'pivot', 'details']
const viewLabels: Record<DataExplorerResultView, string> = {
  table: 'Table',
  chart: 'Chart',
  pivot: 'Pivot',
  details: 'SQL / Details',
}

const chartKinds = new Set<VisualizationEnvelope['spec']['kind']>([
  'cartesian',
  'point',
  'proportional',
  'hierarchy',
  'polar',
  'kpi',
])
let resultSurfaceSequence = 0

/**
 * Result chrome for Data Explorer. The parent may supply compiled visual
 * envelopes as they arrive; this component does not compile or reinterpret
 * them and never renders a table/chart protocol of its own.
 */
export class DataExplorerResults extends LitElement {
  @property({ attribute: false }) command: DataExploreCommand = emptyDataExploreCommand
  @property({ attribute: false }) result: DataExploreResultSignal = emptyResult
  @property({ attribute: false }) status: DataExploreStatusSignal = emptyStatus
  @property({ attribute: false }) visualizations: Record<string, VisualizationEnvelope> = {}
  /** `views` mirrors the DataExploreSignal field; `visualizations` is the explicit component API. */
  @property({ attribute: false }) views: Record<string, VisualizationEnvelope> = {}
  @property() recommendedView: DataExplorerResultView | string = 'table'
  @property({ attribute: false }) selectedDataset?: DataExplorerSelectedDatasetMetadata | string
  @property() grain = ''
  @property() freshness = ''
  @property() snapshot: string | number = ''
  @property({ attribute: false }) metadata: DataExplorerResultMetadata = {}
  @property() executionState: DataExplorerExecutionState = 'idle'
  @property() currentExecutionState?: DataExplorerExecutionState

  @state() private activeView: DataExplorerResultView = 'details'
  @state() private pendingInteraction?: OptimisticInteractionCommand
  private readonly instanceID = `data-explorer-result-${++resultSurfaceSequence}`
  private hasUserSelectedView = false
  private lastCompletedRequest: number | null = null

  static styles = css`
    :host {
      display: grid;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      color: var(--lv-fg-default);
    }

    .surface {
      display: flex;
      flex-direction: column;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      background: var(--lv-bg-app);
    }

    .metadata {
      display: flex;
      flex-wrap: wrap;
      align-items: baseline;
      gap: var(--base-size-4) var(--base-size-12);
      padding: var(--base-size-8) var(--base-size-12);
      border-bottom: var(--lv-border-default);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .metadata strong { color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }
    .metadata [data-state='stale'], .metadata [data-state='error'] { color: var(--lv-fg-danger); }
    .metadata [data-state='running'] { color: var(--lv-fg-warning); }

    .warnings {
      flex-basis: 100%;
      margin: 0;
      padding-inline-start: 1.2rem;
      color: var(--lv-fg-warning);
    }

    .tabs {
      display: flex;
      gap: var(--base-size-4);
      padding: var(--base-size-8) var(--base-size-12) 0;
      border-bottom: var(--lv-border-default);
      background: var(--lv-bg-app);
    }

    .view-availability {
      margin: 0;
      padding: var(--base-size-4) var(--base-size-12);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-app);
      font: var(--lv-type-caption);
    }

    .tab {
      min-height: 2rem;
      padding: 0 var(--base-size-8);
      border: 0;
      border-bottom: 2px solid transparent;
      border-radius: var(--base-size-4) var(--base-size-4) 0 0;
      color: var(--lv-fg-muted);
      background: transparent;
      font: inherit;
      cursor: pointer;
    }

    .tab:hover:not(:disabled) { color: var(--lv-fg-default); background: var(--lv-bg-panel-muted); }
    .tab:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .tab[aria-selected='true'] { border-bottom-color: var(--lv-fg-accent); color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }
    .tab:disabled { color: var(--lv-fg-muted); cursor: not-allowed; opacity: 0.6; }

    .sr-only {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      margin: -1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }

    .interaction-actions {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--base-size-8);
      padding: var(--base-size-8) var(--base-size-12);
      border-bottom: var(--lv-border-default);
      background: var(--lv-bg-panel-muted);
    }

    .interaction-actions p { margin: 0; color: var(--lv-fg-muted); }
    .interaction-actions button { min-height: 2rem; }

    .panel {
      display: grid;
      flex: 1 1 auto;
      min-width: 0;
      min-height: 0;
      overflow: auto;
    }

    lv-visualization-host {
      display: block;
      width: 100%;
      min-width: 0;
      min-height: 18rem;
      height: 100%;
    }

    .unavailable, .details {
      max-width: 72rem;
      padding: var(--base-size-16);
    }

    .unavailable { color: var(--lv-fg-muted); }
    .unavailable strong { color: var(--lv-fg-default); }
    .details { display: grid; align-content: start; gap: var(--base-size-16); }
    .details section { min-width: 0; }
    .details h3 { margin: 0 0 var(--base-size-4); font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .details p { margin: 0; color: var(--lv-fg-muted); }
    pre {
      max-width: 100%;
      max-height: 24rem;
      margin: 0;
      padding: var(--base-size-12);
      overflow: auto;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-code-block);
      white-space: pre-wrap;
      overflow-wrap: anywhere;
    }
  `

  protected willUpdate(changed: PropertyValues<this>): void {
    if (!changed.has('result') && !changed.has('status') && !changed.has('command') && !changed.has('views') && !changed.has('visualizations') && !changed.has('recommendedView')) return
    const requestSeq = this.completedRequestSequence()
    if (!this.requestCompleted() || requestSeq === null) return

    const newCompletedRequest = requestSeq !== this.lastCompletedRequest
    if (newCompletedRequest) {
      this.lastCompletedRequest = requestSeq
      this.pendingInteraction = undefined
    }
    if (!this.hasUserSelectedView || !this.isAvailable(this.activeView)) this.activeView = this.initialView()
  }

  render() {
    const panelID = this.panelID()
    return html`
      <section class="surface" aria-label="Data exploration results">
        ${this.renderMetadata()}
        ${this.renderInteractionActions()}
        <div class="tabs" role="tablist" aria-label="Result view" @keydown=${this.handleTabKeydown}>
          ${viewOrder.map((view) => this.renderTab(view))}
        </div>
        ${this.renderAvailabilitySummary()}
        <section
          id=${panelID}
          class="panel"
          role="tabpanel"
          tabindex="0"
          aria-labelledby=${this.tabID(this.activeView)}
        >
          ${this.renderActiveView()}
        </section>
      </section>
    `
  }

  private renderAvailabilitySummary() {
    const unavailable = viewOrder.filter((view) => view !== 'details' && !this.isAvailable(view)).map((view) => viewLabels[view])
    return unavailable.length ? html`<p class="view-availability" role="status">Unavailable for this result: ${unavailable.join(', ')}.</p>` : nothing
  }

  private renderTab(view: DataExplorerResultView) {
    const available = this.isAvailable(view)
    const unavailableID = `${this.tabID(view)}-unavailable`
    return html`
      <button
        id=${this.tabID(view)}
        class="tab"
        type="button"
        role="tab"
        aria-selected=${String(this.activeView === view)}
        aria-controls=${this.panelID()}
        aria-describedby=${available ? nothing : unavailableID}
        data-view=${view}
        tabindex=${this.activeView === view ? '0' : '-1'}
        aria-disabled=${String(!available)}
        title=${available ? viewLabels[view] : `${viewLabels[view]} view is unavailable`}
        @click=${() => this.selectView(view)}
      >${viewLabels[view]}</button>
      ${available ? nothing : html`<span id=${unavailableID} class="sr-only">${this.unavailableReason(view)}</span>`}
    `
  }

  private renderInteractionActions() {
    if (!this.pendingInteraction) return nothing
    return html`
      <div class="interaction-actions" role="group" aria-label="Selected data action">
        <p>A data point is selected.</p>
        <button type="button" @click=${() => this.chooseInteraction('drill')}>Drill to rows</button>
        <button type="button" @click=${() => this.chooseInteraction('explore_from_here')}>Explore from here</button>
        <button type="button" @click=${this.clearInteraction}>Cancel</button>
      </div>
    `
  }

  private renderMetadata() {
    const metadata = this.resolvedMetadata()
    const state = this.resolvedFreshnessState()
    const result = this.result ?? emptyResult
    const status = this.status ?? emptyStatus
    const duration = Number.isFinite(result.durationMs) ? `${Math.max(0, result.durationMs)} ms` : '—'
    const rows = Number.isFinite(result.rowsReturned) ? Math.max(0, result.rowsReturned) : result.rows?.length ?? 0
    const progress = this.progressText()
    return html`
      <div class="metadata" aria-live="polite" aria-label="Result metadata">
        <span data-result-dataset><strong>Dataset:</strong> ${metadata.dataset || '—'}</span>
        <span data-result-grain><strong>Grain:</strong> ${metadata.grain || '—'}</span>
        <span data-result-duration><strong>Duration:</strong> ${duration}</span>
        <span data-result-freshness data-state=${state.kind}><strong>Freshness:</strong> ${state.label}</span>
        <span data-result-snapshot><strong>Snapshot:</strong> ${metadata.snapshot ?? '—'}</span>
        <span data-result-rows><strong>Rows:</strong> ${rows}${result.truncated ? ' (truncated)' : ''}</span>
        ${status.stale || status.state === 'stale' ? html`<span data-result-stale data-state="stale"><strong>Stale:</strong> Run to refresh</span>` : nothing}
        ${progress ? html`<span data-result-progress data-state="running" role="status">${progress}</span>` : nothing}
        ${result.warnings?.length ? html`
          <ul class="warnings" data-result-warnings aria-label="Result warnings">
            ${result.warnings.map((warning) => html`<li>${warning}</li>`)}
          </ul>
        ` : nothing}
      </div>
    `
  }

  private renderActiveView() {
    if (this.activeView === 'details') return this.renderDetails()
    const envelope = this.envelopeFor(this.activeView)
    if (!envelope) {
      return html`<p class="unavailable" role="status"><strong>${viewLabels[this.activeView]} view unavailable.</strong> ${this.unavailableReason(this.activeView)}</p>`
    }
    return html`<lv-visualization-host .envelope=${envelope}></lv-visualization-host>`
  }

  private renderDetails() {
    const sql = this.result?.sql
    const plan = this.result?.plan
    return html`
      <div class="details" aria-label="SQL and query details">
        <section>
          <h3>SQL</h3>
          ${sql ? html`<pre data-sql>${sql}</pre>` : html`<p data-sql-empty>No diagnostic SQL is available for this result.</p>`}
        </section>
        <section>
          <h3>Query plan</h3>
          ${plan ? html`<pre data-plan>${plan}</pre>` : html`<p data-plan-empty>No diagnostic query plan is available for this result.</p>`}
        </section>
        <p data-query-identity>Model: ${this.command?.spec?.modelId || '—'} · Dataset: ${this.command?.spec?.datasetId || this.resolvedMetadata().dataset || '—'}</p>
      </div>
    `
  }

  private selectView(view: DataExplorerResultView): void {
    if (!this.isAvailable(view)) return
    this.hasUserSelectedView = true
    this.activeView = view
  }

  private chooseInteraction(mode: DataExploreInteractionDetail['mode']): void {
    const command = this.pendingInteraction
    if (!command) return
    this.pendingInteraction = undefined
    this.dispatchEvent(new CustomEvent<DataExploreInteractionDetail>('lv-data-explore-interaction', {
      bubbles: true,
      composed: true,
      detail: { command, mode },
    }))
  }

  private clearInteraction = (): void => { this.pendingInteraction = undefined }

  private handleTabKeydown = (event: KeyboardEvent): void => {
    const target = event.target
    if (!(target instanceof HTMLButtonElement) || target.getAttribute('role') !== 'tab') return
    const current = viewOrder.indexOf(target.dataset.view as DataExplorerResultView)
    if (current < 0) return
    let next = -1
    if (event.key === 'ArrowRight' || event.key === 'ArrowDown') next = this.nextAvailable(current, 1)
    if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') next = this.nextAvailable(current, -1)
    if (event.key === 'Home') next = this.nextAvailable(-1, 1)
    if (event.key === 'End') next = this.nextAvailable(viewOrder.length, -1)
    if (next < 0) return
    event.preventDefault()
    const view = viewOrder[next]
    this.selectView(view)
    queueMicrotask(() => this.renderRoot.querySelector<HTMLButtonElement>(`[data-view="${view}"]`)?.focus())
  }

  private nextAvailable(index: number, direction: 1 | -1): number {
    let candidate = index + direction
    while (candidate >= 0 && candidate < viewOrder.length) {
      if (this.isAvailable(viewOrder[candidate]!)) return candidate
      candidate += direction
    }
    return -1
  }

  private initialView(): DataExplorerResultView {
    const recommended = normalizeView(this.recommendedView)
    if (this.isAvailable(recommended)) return recommended
    return viewOrder.find((view) => this.isAvailable(view)) ?? 'details'
  }

  private requestCompleted(): boolean {
    const result = this.result ?? emptyResult
    const status = this.status ?? emptyStatus
    return status.state === 'success' && !status.loading && !result.error && !status.error
  }

  private completedRequestSequence(): number | null {
    const resultSequence = Number(this.result?.requestSeq)
    const statusSequence = Number(this.status?.requestSeq)
    const sequence = Number.isSafeInteger(resultSequence) && resultSequence > 0 ? resultSequence : statusSequence
    return Number.isSafeInteger(sequence) && sequence >= 0 ? sequence : null
  }

  private resolvedMetadata(): DataExplorerResultMetadata {
    const selected = typeof this.selectedDataset === 'string' ? { title: this.selectedDataset } : this.selectedDataset
    const metadata = this.metadata ?? {}
    const result = this.result ?? emptyResult
    return {
      dataset: metadata.dataset || selected?.title || selected?.id || '',
      grain: metadata.grain || this.grain || selected?.grain || selected?.grainLabel || selected?.grainEntity || '',
      freshness: metadata.freshness || this.freshness || selected?.freshness || result.freshness?.status || '',
      snapshot: metadata.snapshot
        ?? (this.snapshot !== '' ? this.snapshot : selected?.snapshot)
        ?? result.freshness?.snapshotId,
    }
  }

  private resolvedFreshnessState(): { kind: 'fresh' | 'stale' | 'unknown' | 'running' | 'error'; label: string } {
    const result = this.result ?? emptyResult
    const status = this.status ?? emptyStatus
    const execution = this.currentExecutionState ?? this.executionState
    const resultFreshness = result.freshness?.status
    if (result.error || status.error || status.state === 'error') return { kind: 'error', label: 'Error' }
    if (status.stale || status.state === 'stale' || resultFreshness === 'stale') return { kind: 'stale', label: this.displayFreshness('Stale') }
    if (status.loading || execution === 'pending' || execution === 'running') return { kind: 'running', label: this.displayFreshness('Refreshing') }
    if (execution === 'stopped' || status.state === 'cancelled') return { kind: 'stale', label: this.displayFreshness('Stopped') }
    if (resultFreshness === 'fresh') return { kind: 'fresh', label: this.displayFreshness('Fresh') }
    return { kind: 'unknown', label: this.displayFreshness('Unknown') }
  }

  private displayFreshness(fallback: string): string {
    const value = this.resolvedMetadata().freshness?.trim()
    if (!value) return fallback
    if (value === 'fresh') return 'Fresh'
    if (value === 'stale') return 'Stale'
    if (value === 'unknown') return 'Unknown'
    return value
  }

  private progressText(): string {
    const status = this.status ?? emptyStatus
    const execution = this.currentExecutionState ?? this.executionState
    if (!status.loading && execution !== 'pending' && execution !== 'running') return ''
    if (typeof status.progressPercent === 'number' && Number.isFinite(status.progressPercent)) {
      return `Progress: ${Math.round(Math.min(100, Math.max(0, status.progressPercent)))}%`
    }
    return status.message || (execution === 'pending' ? 'Progress: Waiting to run…' : 'Progress: Running…')
  }

  private isAvailable(view: DataExplorerResultView): boolean {
    return view === 'details' || this.envelopeFor(view) !== undefined
  }

  private envelopeFor(view: Exclude<DataExplorerResultView, 'details'>): VisualizationEnvelope | undefined {
    const visualizations = Object.keys(this.visualizations ?? {}).length > 0 ? this.visualizations : this.views ?? {}
    const candidates = [
      visualizations[view],
      visualizations[`${view}Visualization`],
      ...Object.values(visualizations),
    ].filter((candidate): candidate is VisualizationEnvelope => Boolean(candidate))
    return candidates.find((candidate) => envelopeSupportsView(candidate, view))
  }

  private unavailableReason(view: DataExplorerResultView): string {
    if (view === 'details') return ''
    if (view === 'pivot') return 'A compiled pivot visualization was not supplied for this result.'
    if (view === 'chart') return 'A compiled chart visualization was not supplied for this result.'
    return 'A compiled table visualization was not supplied for this result.'
  }

  private tabID(view: DataExplorerResultView): string { return `${this.instanceID}-tab-${view}` }
  private panelID(): string { return `${this.instanceID}-panel` }

  private forwardDrill = (event: Event): void => {
    const detail = (event as CustomEvent<unknown>).detail
    if (!isInteractionCommand(detail)) return
    event.stopPropagation()
    this.pendingInteraction = detail
  }

  connectedCallback(): void {
    super.connectedCallback()
    this.addEventListener('lv-interaction-select', this.forwardDrill as EventListener)
  }

  disconnectedCallback(): void {
    this.removeEventListener('lv-interaction-select', this.forwardDrill as EventListener)
    super.disconnectedCallback()
  }
}

export function normalizeView(value: unknown): DataExplorerResultView {
  if (value === 'chart' || value === 'pivot' || value === 'details' || value === 'sql') return value === 'sql' ? 'details' : value
  // Server projections use stable renderer IDs for specialized chart choices.
  if (value === 'line' || value === 'bar' || value === 'donut' || value === 'kpi') return 'chart'
  return 'table'
}

export function envelopeSupportsView(envelope: VisualizationEnvelope, view: Exclude<DataExplorerResultView, 'details'>): boolean {
  if (!envelope || typeof envelope !== 'object' || !envelope.spec) return false
  if (view === 'table') return envelope.spec.kind === 'table'
  if (view === 'pivot') return envelope.spec.kind === 'pivot'
  return chartKinds.has(envelope.spec.kind)
}

function isInteractionCommand(value: unknown): value is OptimisticInteractionCommand {
  if (!value || typeof value !== 'object') return false
  const command = value as Partial<OptimisticInteractionCommand>
  return command.sourceKind === 'visual'
    && typeof command.sourceId === 'string'
    && typeof command.interactionKind === 'string'
    && (command.action === 'set' || command.action === 'replace' || command.action === 'clear')
    && typeof command.toggle === 'boolean'
    && Array.isArray(command.mappings)
}

if (!customElements.get('lv-data-explorer-results')) customElements.define('lv-data-explorer-results', DataExplorerResults)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explorer-results': DataExplorerResults
  }
}

// Keep this import type available to consumers that currently pass the
// generated ExplorationSpec while signal hydration is still in progress.
export type DataExplorerResultSpec = ExplorationSpec
