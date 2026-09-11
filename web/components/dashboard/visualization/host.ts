import { LitElement, html } from 'lit'
import { property, query, state } from 'lit/decorators.js'
import type { VisualizationEnvelope } from '../../../generated/visualization'
import validateGeneratedEnvelope from '../../../generated/visualization/validate'
import '../../shared/loading-spinner'
import { visualActionStyles } from '../visual-action-styles'
import { visualMenuIcon } from '../visual-menu-icons'
import type { VisualActionDetail } from '../visual-modal'
import { defaultRendererContext, normalizeRendererLocale, primerCategoricalPalette, VisualizationController, validateEnvelopeBoundary, type RendererContext } from './host-controller'
import { visualizationRegistry } from './registry'
import { adapterObservation } from './telemetry'
import { accessibleDataStatus, accessibleStatus, accessibleVisualizationData, displayValue, supportsHostDataActions, visualizationChangeAnnouncement } from './accessibility'
import { clearInteractionCommand } from './interaction-command'
import { resolveVisualizationMetadata } from './metadata'
import { visualizationHostStyles } from './host-styles'

export { accessibleDataStatus, accessibleStatus, accessibleVisualizationData, supportsHostDataActions, type AccessibleVisualizationData, type AccessibleVisualizationColumn } from './accessibility'

/** Start mounting within 600 CSS pixels above or below the viewport. */
export const visualizationNearViewportRootMargin = '600px 0px'

export class VisualizationHost extends LitElement {
  private envelopeValue?: VisualizationEnvelope
  @property({ attribute: false })
  get envelope(): VisualizationEnvelope | undefined { return this.envelopeValue }
  set envelope(value: VisualizationEnvelope | undefined) {
    const previous = this.envelopeValue
    // Keep the shell and actions on the same valid revision as the renderer,
    // both before and after a deferred host mounts. Spec revisions are opaque
    // identities (and may legitimately revert); data revisions order one spec.
    // Eager hosts retain their existing validation/error boundary.
    if (this.deferMount && !this.authoring) {
      if (value && !isAcceptedEnvelope(value)) return
      if (value && previous && previous.specRevision === value.specRevision && value.dataRevision < previous.dataRevision) return
    }
    if (Object.is(previous, value)) return
    this.envelopeValue = value
    this.requestUpdate('envelope', previous)
  }
  @property({ attribute: false }) openVisualFocus?: (source: HTMLElement, detail: VisualActionDetail) => void
  @property({ type: Boolean, attribute: 'defer-mount', reflect: true }) deferMount = false
  @property({ type: Boolean, reflect: true }) authoring = false
  @query('.renderer') private rendererContainer?: HTMLDivElement
  @state() private error = ''
  @state() private applying = false
  @state() private presented = false
  @state() private announcement = ''
  private controller?: VisualizationController
  private resizeObserver?: ResizeObserver
  private applyGeneration = 0
  private connectionGeneration = 0
  private presentedRendererID = ''
  private contextListenersConnected = false
  private reducedMotionMedia?: MediaQueryList
  private mountObserver?: IntersectionObserver
  private mountRequested = false
  private pendingApply?: Promise<void>
  private applyQueued = false
  private mountEpoch = 0

  static styles = [visualActionStyles, visualizationHostStyles]

  protected firstUpdated(): void {
    this.setupMountLifecycle()
  }

  connectedCallback(): void {
    super.connectedCallback()
    const generation = ++this.connectionGeneration
    if (!this.hasUpdated || this.controller || this.mountObserver) return
    queueMicrotask(() => {
      if (generation === this.connectionGeneration && this.isConnected) {
        this.setupMountLifecycle()
      }
    })
  }

  private setupMountLifecycle(): void {
    if (!this.rendererContainer) return
    if (this.controller || this.mountObserver) return
    if (this.mountRequested || !this.deferMount || this.authoring) {
      this.requestMount()
      return
    }
    if (typeof IntersectionObserver !== 'function') {
      this.requestMount()
      return
    }
    let observer: IntersectionObserver | undefined
    try {
      observer = new IntersectionObserver((entries) => {
        if (!this.isConnected || this.mountObserver !== observer || this.mountRequested) return
        if (entries.some((entry) => entry.isIntersecting)) this.requestMount()
      }, {
        rootMargin: visualizationNearViewportRootMargin,
        scrollMargin: visualizationNearViewportRootMargin,
      })
      // rootMargin does not expand clipping by nested scroll containers.
      // Browsers without IntersectionObserver.scrollMargin cannot preserve the
      // dashboard prefetch contract, so keep their existing eager behavior.
      const supportsScrollMargin = typeof (observer as unknown as { scrollMargin?: unknown }).scrollMargin === 'string'
      if (this.closest('lv-report-canvas') && !supportsScrollMargin) {
        observer.disconnect()
        this.requestMount()
        return
      }
      this.mountObserver = observer
      observer.observe(this.rendererContainer)
    } catch {
      if (this.mountObserver === observer) this.mountObserver = undefined
      try { observer?.disconnect() } catch { /* best-effort cleanup */ }
      this.requestMount()
    }
  }

  private ensureController(): void {
    if (this.controller || !this.rendererContainer) return
    this.controller = new VisualizationController(
      visualizationRegistry,
      this.rendererContainer,
      (value): value is VisualizationEnvelope => validateGeneratedEnvelope(value) && validateEnvelopeBoundary(value),
      (detail) => this.dispatchEvent(new CustomEvent('lv-visualization-observation', { bubbles: true, composed: true, detail })),
    )
    this.connectContextListeners()
    if (typeof ResizeObserver !== 'function') return
    try {
      this.resizeObserver = new ResizeObserver(([entry]) => {
        if (!entry) return
        this.controller?.resize(entry.contentRect.width, entry.contentRect.height, window.devicePixelRatio || 1)
      })
      this.resizeObserver.observe(this.rendererContainer)
    } catch {
      try { this.resizeObserver?.disconnect() } catch { /* best-effort cleanup */ }
      this.resizeObserver = undefined
    }
  }

  protected updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('envelope') && this.mountRequested) this.scheduleApply()
    if ((changed.has('deferMount') || changed.has('authoring')) && !this.mountRequested) {
      if (!this.deferMount || this.authoring) this.requestMount()
      else if (!this.mountObserver) this.setupMountLifecycle()
    }
  }

  disconnectedCallback(): void {
    const generation = ++this.connectionGeneration
    super.disconnectedCallback()
    // A synchronous DOM move fires disconnected/connected callbacks even though
    // the visual remains live. Defer teardown so transient moves retain renderer
    // state; a host that stays detached is still disposed in the same microtask.
    queueMicrotask(() => {
      if (generation !== this.connectionGeneration || this.isConnected) return
      this.mountEpoch++
      try { this.mountObserver?.disconnect() } catch { /* best-effort cleanup */ }
      this.mountObserver = undefined
      try { this.resizeObserver?.disconnect() } catch { /* best-effort cleanup */ }
      this.resizeObserver = undefined
      this.disconnectContextListeners()
      this.applyGeneration++
      this.pendingApply = undefined
      this.applyQueued = false
      this.controller?.dispose()
      this.controller = undefined
      this.mountRequested = false
      this.presented = false
      this.presentedRendererID = ''
      this.applying = false
    })
  }

  async ensureMounted(): Promise<void> {
    if (!this.isConnected) throw new Error('visualization is detached')
    const epoch = this.mountEpoch
    this.mountRequested = true
    try { this.mountObserver?.disconnect() } catch { /* best-effort cleanup */ }
    this.mountObserver = undefined
    await this.updateComplete
    if (!this.isConnected || this.mountEpoch !== epoch) throw new Error('visualization mount superseded')
    this.ensureController()
    this.scheduleApply()
    await this.waitForApply()
    if (!this.isConnected || this.mountEpoch !== epoch) throw new Error('visualization mount superseded')
    if (!this.envelope) throw new Error('visualization has no envelope')
    if (this.error) throw new Error(this.error)
  }

  async snapshot(): Promise<Blob> {
    await this.ensureMounted()
    await this.waitForApply()
    return this.controller?.snapshot() ?? Promise.reject(new Error('visualization is not mounted'))
  }

  protected render() {
    const statusError = this.envelope?.status.kind === 'error' ? this.envelope.status.message ?? 'Visualization error' : ''
    const error = this.error || statusError
    const header = this.sharedHeader()
    const metadata = this.envelope ? resolveVisualizationMetadata(this.envelope) : undefined
    const titleVisible = this.envelope?.spec.titleVisible !== false
    const showHeader = Boolean((header && titleVisible) || this.authoring)
    const tableActions = this.hasTableActions()
    const showInitialLoading = !this.presented && !error
    const loadingLabel = `Loading ${header ?? 'visualization'}…`
    return html`<div class=${showHeader ? 'surface' : 'surface headerless'}>
      ${showHeader ? html`
        <header class="toolbar">
          <div class="toolbar-title">
            ${this.authoring
              ? html`<slot name="authoring-drag-handle"><h2 data-visualization-title>${metadata?.title}</h2></slot>`
              : html`<h2 data-visualization-title>${metadata?.title}</h2>`}
            ${metadata?.subtitle ? html`<p class="toolbar-subtitle" data-visualization-subtitle>${metadata.subtitle}</p>` : null}
          </div>
          <div class="visual-actions">
            <slot name="agent-action"></slot>
            ${header ? html`<button class="icon-action" type="button" data-visualization-expand data-visualization-id=${this.envelope?.visualID ?? ''} aria-label=${`Expand ${header}`} title=${`Expand ${header}`} @click=${this.expand}>${visualMenuIcon('focus')}</button>` : null}
            ${this.visualActions()}
          </div>
        </header>
      ` : html`<div class="headerless-actions" ?data-table-actions=${tableActions}><div class="visual-actions"><slot name="agent-action"></slot>${header ? html`<button class="icon-action" type="button" data-visualization-expand data-visualization-id=${this.envelope?.visualID ?? ''} aria-label=${`Expand ${header}`} title=${`Expand ${header}`} @click=${this.expand}>${visualMenuIcon('focus')}</button>` : null}${tableActions ? null : this.visualActions()}</div></div>`}
      <div class="renderer-stage" aria-busy=${String(this.applying)}>
        <div class="renderer" role="group" aria-label=${metadata?.title ?? 'Visualization'} aria-describedby="visualization-fallback" aria-busy=${String(this.applying)} aria-hidden=${String(!this.presented)} ?inert=${!this.presented} @lv-map-observation=${this.forwardAdapterObservation}></div>
        ${showInitialLoading ? html`<div class="initial-loading" data-visualization-loading role="status" aria-live="polite">
          <lv-loading-spinner size="medium" aria-hidden="true"></lv-loading-spinner>
          <span>${loadingLabel}</span>
        </div>` : null}
      </div>
      <div id="visualization-fallback" class="fallback">${this.accessibleFallback()}</div>
      ${this.announcement ? html`<div class="announcement" role="status" aria-live="polite">${this.announcement}</div>` : null}
      ${error ? html`<div class="error" role="alert">${error}</div>` : null}
    </div>`
  }

  private hasTableActions(): boolean {
    const kind = this.envelope?.spec.kind
    return kind === 'table' || kind === 'matrix' || kind === 'pivot'
  }

  private requestMount(): void {
    this.mountRequested = true
    try { this.mountObserver?.disconnect() } catch { /* best-effort cleanup */ }
    this.mountObserver = undefined
    // Eager callers retain the original firstUpdated timing. The explicit
    // ensureMounted path below still waits for Lit to settle before observing
    // the result, while event callbacks can start the renderer immediately.
    if (this.rendererContainer) {
      this.ensureController()
      this.scheduleApply()
      return
    }
    void this.ensureMounted().catch(() => {})
  }

  private scheduleApply(): void {
    if (!this.mountRequested || !this.envelope || !this.controller) return
    if (this.pendingApply) {
      this.applyQueued = true
      return
    }
    const pending = this.applyEnvelope()
    this.pendingApply = pending
    void pending.then(
      () => {
        if (this.pendingApply !== pending) return
        this.pendingApply = undefined
        if (this.applyQueued) {
          this.applyQueued = false
          this.scheduleApply()
        }
      },
      () => {
        if (this.pendingApply !== pending) return
        this.pendingApply = undefined
        if (this.applyQueued) {
          this.applyQueued = false
          this.scheduleApply()
        }
      },
    )
  }

  private async waitForApply(): Promise<void> {
    while (this.pendingApply) {
      const pending = this.pendingApply
      await pending
    }
  }

  private async applyEnvelope(): Promise<void> {
    const envelope = this.envelope
    if (!envelope || !this.controller) return
    const previous = this.controller.envelope
    if (this.presentedRendererID !== envelope.rendererID) {
      this.presentedRendererID = envelope.rendererID
      this.presented = false
    }
    const generation = ++this.applyGeneration
    this.applying = true
    try {
      await this.controller.apply(envelope, this.rendererContext())
      if (generation === this.applyGeneration && envelope === this.envelope) {
        this.error = ''
        this.presented = true
        this.announcement = visualizationChangeAnnouncement(previous, envelope)
      }
    } catch (error) {
      if (generation === this.applyGeneration && envelope === this.envelope) this.error = error instanceof Error ? error.message : String(error)
    } finally {
      if (generation === this.applyGeneration && envelope === this.envelope) this.applying = false
    }
  }

  private sharedHeader(): 'chart' | 'map' | 'visualization' | undefined {
    const kind = this.envelope?.spec.kind
    if (!kind || kind === 'kpi' || kind === 'table' || kind === 'matrix' || kind === 'pivot') return undefined
    if (kind === 'geographic') return 'map'
    return 'chart'
  }

  private expand = (): void => {
    const envelope = this.envelope
    const visualType = this.sharedHeader()
    if (!envelope || !visualType) return
    const detail: VisualActionDetail = {
      action: 'focus',
      visualType,
      visualId: envelope.visualID,
      title: resolveVisualizationMetadata(envelope).title,
      columns: [],
      rows: [],
      selection: envelope.selection.map((entry) => entry.label ?? Object.values(entry.datum.identity).join(' · ')),
    }
    this.requestMount()
    this.openFocus(detail)
  }

  private openFocus(detail: VisualActionDetail): void {
    if (this.openVisualFocus) {
      this.openVisualFocus(this, detail)
      return
    }
    this.dispatchEvent(new CustomEvent('lv-visual-action', {
      bubbles: true,
      composed: true,
      detail,
    }))
  }

  private visualActions() {
    const envelope = this.envelope
    if (!envelope || !supportsHostDataActions(envelope)) return null
    return html`<details class="visual-options">
      <summary aria-label="Visual options" aria-haspopup="menu" title="Visual options">${visualMenuIcon('show-data')}</summary>
      <div class="menu" role="menu">
        <button type="button" role="menuitem" @click=${() => this.runAction('show-data')}>${visualMenuIcon('show-data')}<span>Show data</span></button>
        <button type="button" role="menuitem" @click=${() => this.runAction('copy-data')}>${visualMenuIcon('copy-data')}<span>Copy data</span></button>
        <button type="button" role="menuitem" @click=${() => this.runAction('export-csv')}>${visualMenuIcon('export-csv')}<span>Export CSV</span></button>
        ${envelope.selection.length > 0 && clearInteractionCommand(envelope) ? html`<button type="button" role="menuitem" @click=${() => this.runAction('clear-selection')}>${visualMenuIcon('clear-selection')}<span>Clear selection</span></button>` : null}
      </div>
    </details>`
  }

  private runAction(action: Extract<VisualActionDetail['action'], 'show-data' | 'copy-data' | 'export-csv' | 'clear-selection'>): void {
    const envelope = this.envelope
    if (!envelope || !supportsHostDataActions(envelope)) return
    this.renderRoot.querySelector<HTMLDetailsElement>('.visual-options')?.removeAttribute('open')
    const data = accessibleVisualizationData(envelope, this.rendererContext())
    const metadata = resolveVisualizationMetadata(envelope)
    if (action === 'clear-selection') {
      const command = clearInteractionCommand(envelope)
      if (command) this.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: command }))
    }
    this.dispatchEvent(new CustomEvent<VisualActionDetail>('lv-visual-action', {
      bubbles: true,
      composed: true,
      detail: {
        action,
        visualType: envelope.spec.kind === 'geographic' ? 'map' : this.sharedHeader() === 'chart' ? 'chart' : 'visualization',
        visualId: envelope.visualID,
        title: metadata.title,
        columns: [...data.columns],
        rows: [...data.rows],
        selection: envelope.selection.map((entry) => entry.label ?? Object.values(entry.datum.identity).map(displayValue).join(' · ')),
        totalRows: data.totalRows,
        truncated: data.truncated,
        dataStatus: accessibleDataStatus(envelope, data),
      },
    }))
  }

  private forwardAdapterObservation = (event: CustomEvent<unknown>): void => {
    const detail = adapterObservation(event.detail)
    if (!detail) return
    event.stopPropagation()
    this.dispatchEvent(new CustomEvent('lv-visualization-observation', { bubbles: true, composed: true, detail }))
  }

  private connectContextListeners(): void {
    if (this.contextListenersConnected) return
    this.contextListenersConnected = true
    document.addEventListener('leapview-theme-applied', this.handleRendererContextChange)
    this.reducedMotionMedia = window.matchMedia?.('(prefers-reduced-motion: reduce)')
    this.reducedMotionMedia?.addEventListener?.('change', this.handleRendererContextChange)
  }

  private disconnectContextListeners(): void {
    if (!this.contextListenersConnected) return
    this.contextListenersConnected = false
    document.removeEventListener('leapview-theme-applied', this.handleRendererContextChange)
    this.reducedMotionMedia?.removeEventListener?.('change', this.handleRendererContextChange)
    this.reducedMotionMedia = undefined
  }

  private readonly handleRendererContextChange = (): void => { this.scheduleApply() }

  private rendererContext(): RendererContext {
    const target = this.rendererContainer
    if (!target) return defaultRendererContext
    const styles = getComputedStyle(target)
    const color = (name: string, fallback: string): string => styles.getPropertyValue(name).trim() || fallback
    const colorScheme = document.documentElement.style.colorScheme.trim()
    const theme = colorScheme === 'dark' || (colorScheme !== 'light' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) ? 'dark' : 'light'
    return {
      locale: normalizeRendererLocale(document.documentElement.lang || 'en'),
      theme,
      reducedMotion: this.reducedMotionMedia?.matches ?? true,
      devicePixelRatio: window.devicePixelRatio || 1,
      fontFamily: styles.fontFamily || defaultRendererContext.fontFamily,
      colors: {
        foreground: color('--lv-fg-default', defaultRendererContext.colors.foreground),
        muted: color('--lv-chart-axis', defaultRendererContext.colors.muted),
        grid: color('--lv-chart-grid', defaultRendererContext.colors.grid),
        surface: color('--lv-chart-surface', defaultRendererContext.colors.surface),
        accent: color('--lv-fg-accent', defaultRendererContext.colors.accent),
        success: color('--lv-fg-success', defaultRendererContext.colors.success),
        attention: color('--lv-fg-warning', defaultRendererContext.colors.attention),
        danger: color('--lv-fg-danger', defaultRendererContext.colors.danger),
        data: primerCategoricalPalette.map(({ token }, index) => color(token, defaultRendererContext.colors.data[index]!)),
      },
    }
  }

  private accessibleFallback() {
    const envelope = this.envelope
    if (!envelope) return 'Visualization is loading.'
    const data = accessibleVisualizationData(envelope, this.rendererContext(), 6)
    const metadata = resolveVisualizationMetadata(envelope)
    const summary = metadata.summary ?? metadata.description
    const status = accessibleStatus(envelope)
    const dataSummary = accessibleDataStatus(envelope, data)
    return `${metadata.title}.${metadata.subtitle ? ` ${metadata.subtitle}.` : ''} ${summary}. ${status}. ${dataSummary}`
  }
}

if (!customElements.get('lv-visualization-host')) customElements.define('lv-visualization-host', VisualizationHost)

declare global { interface HTMLElementTagNameMap { 'lv-visualization-host': VisualizationHost } }

function isAcceptedEnvelope(value: unknown): value is VisualizationEnvelope {
  return validateGeneratedEnvelope(value) && validateEnvelopeBoundary(value)
}
