import { LitElement, css, html } from 'lit'
import { property, query, state } from 'lit/decorators.js'
import type { VisualizationEnvelope } from '../../../generated/visualization'
import validateGeneratedEnvelope from '../../../generated/visualization/validate'
import '../../shared/loading-spinner'
import { visualActionStyles } from '../visual-action-styles'
import { visualMenuIcon } from '../visual-menu-icons'
import type { VisualActionDetail } from '../visual-modal'
import { defaultRendererContext, normalizeRendererLocale, VisualizationController, validateEnvelopeBoundary, type RendererContext } from './host-controller'
import { visualizationRegistry } from './registry'
import { adapterObservation } from './telemetry'
import { resolveVisualizationMetadata } from './metadata'

export class VisualizationHost extends LitElement {
  @property({ attribute: false }) envelope?: VisualizationEnvelope
  @property({ attribute: false }) openVisualFocus?: (source: HTMLElement, detail: VisualActionDetail) => void
  @property({ type: Boolean, reflect: true }) authoring = false
  @query('.renderer') private rendererContainer?: HTMLDivElement
  @state() private error = ''
  @state() private applying = false
  @state() private presented = false
  private controller?: VisualizationController
  private resizeObserver?: ResizeObserver
  private applyGeneration = 0
  private connectionGeneration = 0
  private presentedRendererID = ''
  private contextListenersConnected = false
  private reducedMotionMedia?: MediaQueryList

  static styles = [visualActionStyles, css`
    :host, .surface { display: block; width: 100%; height: 100%; min-width: 0; min-height: 0; }
    :host { color: var(--lv-fg-default); background: var(--lv-chart-surface); font-family: var(--fontStack-system); }
    .surface { position: relative; display: grid; grid-template-rows: auto minmax(0, 1fr); background: var(--lv-chart-surface); }
    .surface.headerless { grid-template-rows: minmax(0, 1fr); }
    .renderer-stage { position: relative; min-width: 0; min-height: 0; overflow: hidden; background: var(--lv-chart-surface); }
    .renderer { display: block; width: 100%; height: 100%; min-width: 0; min-height: 0; overflow: hidden; }
    .lv-kpi-card {
      position: relative;
      display: grid;
      align-content: center;
      box-sizing: border-box;
      width: 100%;
      height: 100%;
      min-height: 0;
      gap: var(--base-size-4);
      padding: var(--base-size-12) var(--base-size-16);
      overflow: hidden;
      background: var(--lv-chart-surface);
      container-type: inline-size;
    }
    .lv-visualization-label {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      text-transform: uppercase;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .lv-visualization-kpi {
      display: block;
      overflow: hidden;
      color: var(--lv-fg-default);
      font-size: clamp(var(--text-title-size-small), 10cqi, var(--text-display-size));
      font-weight: var(--base-text-weight-semibold);
      line-height: 1.1;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .lv-visualization-note {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .lv-kpi-comparison {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--base-size-4) var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
    }
    .lv-kpi-delta {
      font-weight: var(--base-text-weight-semibold);
    }
    .lv-kpi-delta[data-status='favorable'] { color: var(--lv-fg-success); }
    .lv-kpi-delta[data-status='unfavorable'] { color: var(--lv-fg-danger); }
    .lv-kpi-delta[data-status='unavailable'] { color: var(--lv-fg-muted); }
    .lv-kpi-goal, .lv-kpi-status {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }
    .lv-kpi-status[data-tone='success'] { color: var(--lv-fg-success); }
    .lv-kpi-status[data-tone='warning'] { color: var(--lv-fg-warning); }
    .lv-kpi-status[data-tone='danger'] { color: var(--lv-fg-danger); }
    .lv-kpi-progress {
      position: relative;
      isolation: isolate;
      box-sizing: border-box;
      width: 100%;
      height: var(--base-size-8);
      overflow: hidden;
      border: var(--lv-border-width) solid var(--lv-line-default);
      background: var(--lv-bg-panel-muted);
    }
    .lv-kpi-progress-progress { border-radius: var(--lv-radius-full); }
    .lv-kpi-progress-bullet {
      height: var(--base-size-12);
    }
    .lv-kpi-bullet-range {
      position: absolute;
      z-index: 0;
      inset-block: 0;
      background: var(--lv-bg-panel-muted);
    }
    .lv-kpi-bullet-range[data-tone='ink'] { background: var(--lv-data-1-muted); }
    .lv-kpi-bullet-range[data-tone='success'] {
      background: color-mix(in srgb, var(--lv-fg-success) 28%, var(--lv-chart-surface));
    }
    .lv-kpi-bullet-range[data-tone='warning'] {
      background: color-mix(in srgb, var(--lv-fg-warning) 28%, var(--lv-chart-surface));
    }
    .lv-kpi-bullet-range[data-tone='danger'] {
      background: color-mix(in srgb, var(--lv-fg-danger) 28%, var(--lv-chart-surface));
    }
    .lv-kpi-bullet-target {
      position: absolute;
      z-index: 2;
      inset-block: calc(-1 * var(--lv-border-width));
      width: var(--lv-border-width-focus);
      transform: translateX(-50%);
      background: var(--lv-fg-default);
    }
    .lv-kpi-progress-fill {
      position: relative;
      z-index: 1;
      display: block;
      height: 100%;
      background: var(--lv-data-1);
    }
    .lv-kpi-progress-bullet .lv-kpi-progress-fill {
      inset-block-start: 25%;
      height: 50%;
    }
    .lv-kpi-sparkline {
      width: 100%;
      height: var(--base-size-24);
      overflow: visible;
    }
    .lv-kpi-sparkline path {
      fill: none;
      stroke: var(--lv-data-1);
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: var(--lv-border-width-focus);
      vector-effect: non-scaling-stroke;
    }
    .lv-kpi-card[data-mode='bullet'], .lv-kpi-card[data-mode='progress'] {
      align-content: center;
      gap: var(--base-size-4);
    }
    .lv-kpi-card[data-layout='stacked'] {
      gap: var(--base-size-4);
      padding: var(--base-size-8) var(--base-size-12);
    }
    .lv-kpi-card[data-layout='stacked'] .lv-visualization-kpi {
      font-size: clamp(var(--text-title-size-small), 10cqi, var(--text-title-size-medium));
      line-height: var(--base-text-lineHeight-tight);
    }
    .lv-kpi-card[data-layout='stacked'] .lv-kpi-comparison {
      display: grid;
      gap: var(--base-size-2);
    }
    .lv-kpi-card[data-layout-fit='too-small'] {
      outline: var(--lv-border-width-focus) solid var(--lv-fg-danger);
      outline-offset: calc(-1 * var(--lv-border-width-focus));
    }
    .initial-loading {
      position: absolute;
      inset: 0;
      z-index: var(--zIndex-sticky);
      display: grid;
      align-content: center;
      justify-items: center;
      gap: var(--base-size-8);
      background: var(--lv-chart-surface);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }
    .toolbar {
      position: relative;
      z-index: var(--zIndex-sticky);
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      min-height: calc(var(--control-small-size) + var(--base-size-6));
      border-bottom: var(--lv-border-default);
      background: var(--lv-chart-surface);
      padding: var(--base-size-6) var(--base-size-8) var(--base-size-4) var(--control-small-paddingInline-normal);
      box-sizing: border-box;
    }
    .toolbar-title { flex: 1 1 auto; min-width: 0; }
    .headerless-actions {
      position: absolute;
      inset: var(--base-size-6) var(--base-size-8) auto auto;
      z-index: var(--zIndex-sticky);
    }
    h2 {
      min-width: 0;
      margin: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      letter-spacing: 0;
    }
    .toolbar-subtitle {
      margin: var(--base-size-2) 0 0;
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .error { position: absolute; inset: 0; display: grid; place-items: center; color: var(--lv-fg-danger); padding: 1rem; text-align: center; background: var(--lv-bg-panel); }
    .fallback { position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; border: 0; }
  `]

  protected firstUpdated(): void {
    this.connectContextListeners()
    this.ensureController()
  }

  connectedCallback(): void {
    super.connectedCallback()
    const generation = ++this.connectionGeneration
    if (!this.hasUpdated || this.controller) return
    queueMicrotask(() => {
      if (generation === this.connectionGeneration && this.isConnected) {
        this.connectContextListeners()
        this.ensureController()
      }
    })
  }

  private ensureController(): void {
    if (this.controller || !this.rendererContainer) return
    this.controller = new VisualizationController(
      visualizationRegistry,
      this.rendererContainer,
      (value): value is VisualizationEnvelope => validateGeneratedEnvelope(value) && validateEnvelopeBoundary(value),
      (detail) => this.dispatchEvent(new CustomEvent('lv-visualization-observation', { bubbles: true, composed: true, detail })),
    )
    this.resizeObserver = new ResizeObserver(([entry]) => {
      if (!entry) return
      this.controller?.resize(entry.contentRect.width, entry.contentRect.height, window.devicePixelRatio || 1)
    })
    this.resizeObserver.observe(this.rendererContainer)
    void this.applyEnvelope()
  }

  protected updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('envelope')) {
      void this.applyEnvelope()
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
      this.resizeObserver?.disconnect()
      this.resizeObserver = undefined
      this.disconnectContextListeners()
      this.controller?.dispose()
      this.controller = undefined
      this.presented = false
      this.presentedRendererID = ''
    })
  }

  async snapshot(): Promise<Blob> { return this.controller?.snapshot() ?? Promise.reject(new Error('visualization is not mounted')) }

  protected render() {
    const statusError = this.envelope?.status.kind === 'error' ? this.envelope.status.message ?? 'Visualization error' : ''
    const error = this.error || statusError
    const header = this.sharedHeader()
    const metadata = this.envelope ? resolveVisualizationMetadata(this.envelope) : undefined
    const titleVisible = this.envelope?.spec.titleVisible !== false
    const showHeader = Boolean((header && titleVisible) || this.authoring)
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
          </div>
        </header>
      ` : html`<div class="headerless-actions"><slot name="agent-action"></slot>${header ? html`<button class="icon-action" type="button" data-visualization-expand data-visualization-id=${this.envelope?.visualID ?? ''} aria-label=${`Expand ${header}`} title=${`Expand ${header}`} @click=${this.expand}>${visualMenuIcon('focus')}</button>` : null}</div>`}
      <div class="renderer-stage" aria-busy=${String(this.applying)}>
        <div class="renderer" role="group" aria-label=${metadata?.title ?? 'Visualization'} aria-describedby="visualization-fallback" aria-busy=${String(this.applying)} aria-hidden=${String(!this.presented)} ?inert=${!this.presented} @lv-map-observation=${this.forwardAdapterObservation}></div>
        ${showInitialLoading ? html`<div class="initial-loading" data-visualization-loading role="status" aria-live="polite">
          <lv-loading-spinner size="medium" aria-hidden="true"></lv-loading-spinner>
          <span>${loadingLabel}</span>
        </div>` : null}
      </div>
      <div id="visualization-fallback" class="fallback">${this.accessibleFallback()}</div>
      ${error ? html`<div class="error" role="alert">${error}</div>` : null}
    </div>`
  }

  private async applyEnvelope(): Promise<void> {
    if (!this.envelope || !this.controller) return
    if (this.presentedRendererID !== this.envelope.rendererID) {
      this.presentedRendererID = this.envelope.rendererID
      this.presented = false
    }
    const generation = ++this.applyGeneration
    this.applying = true
    try {
      await this.controller.apply(this.envelope, this.rendererContext())
      if (generation === this.applyGeneration) {
        this.error = ''
        this.presented = true
      }
    } catch (error) {
      if (generation === this.applyGeneration) this.error = error instanceof Error ? error.message : String(error)
    } finally {
      if (generation === this.applyGeneration) this.applying = false
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

  private readonly handleRendererContextChange = (): void => { void this.applyEnvelope() }

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
        data: [
          'blue', 'orange', 'green', 'pink', 'brown', 'plum', 'teal', 'yellow', 'red',
          'gray', 'olive', 'pine', 'auburn', 'lemon', 'purple', 'coral', 'lime',
        ].map((name, index) => color(`--data-${name}-color-emphasis`, defaultRendererContext.colors.data[index]!)),
      },
    }
  }

  private accessibleFallback() {
    const envelope = this.envelope
    if (!envelope) return 'Visualization is loading.'
    const status = envelope.status.message ?? envelope.status.kind.replaceAll('_', ' ')
    const metadata = resolveVisualizationMetadata(envelope)
    const summary = metadata.summary ?? metadata.description
    return `${metadata.title}.${metadata.subtitle ? ` ${metadata.subtitle}.` : ''} ${summary}. Status: ${status}.`
  }
}

if (!customElements.get('lv-visualization-host')) customElements.define('lv-visualization-host', VisualizationHost)

declare global { interface HTMLElementTagNameMap { 'lv-visualization-host': VisualizationHost } }
