import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Maximize2, Minus, Plus, type IconNode } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'

type VisualElement = HTMLElement & {
  dataset: DOMStringMap
}

type ZoomMode = 'fit-width' | 'fit-page' | 'actual-size' | 'custom'
type PresentationMode = ZoomMode | 'responsive'

type ZoomCommand = {
  mode?: ZoomMode
  scale?: number
}

type ZoomAnchor = {
  x: number
  y: number
}

class ReportCanvas extends LitElement {
  @property({ type: Number }) width = 1366
  @property({ type: Number }) height = 768
  @state() private scale = 1
  @state() private zoomMode: ZoomMode = storedZoomMode()
  @state() private presentationMode: PresentationMode = 'fit-width'
  @state() private contentHeight = this.height
  private customScale = storedCustomScale()
  private zoomAnchor?: ZoomAnchor

  private resizeObserver?: ResizeObserver

  static styles = css`
    :host {
      display: block;
      width: 100%;
      height: 100%;
      max-width: 100%;
      min-width: 0;
      min-height: 0;
      box-sizing: border-box;
    }

    .surface {
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      background: var(--lv-report-canvas-bg);
    }

    .viewport {
      position: relative;
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: auto;
      padding: 0;
    }

    .sizer {
      display: grid;
      width: max(100%, calc(var(--report-canvas-width) * var(--report-canvas-scale) * 1px));
      height: max(100%, calc(var(--report-canvas-height) * var(--report-canvas-scale) * 1px));
      min-width: 100%;
      min-height: 100%;
      align-items: start;
      justify-items: center;
    }

    .frame-wrap {
      position: relative;
      width: calc(var(--report-canvas-width) * var(--report-canvas-scale) * 1px);
      height: calc(var(--report-canvas-height) * var(--report-canvas-scale) * 1px);
      flex: 0 0 auto;
    }

    .frame {
      position: absolute;
      inset: 0 auto auto 0;
      box-sizing: border-box;
      width: calc(var(--report-canvas-width) * 1px);
      height: calc(var(--report-canvas-height) * 1px);
      transform: scale(var(--report-canvas-scale));
      transform-origin: top left;
      background: var(--lv-report-page-bg);
    }

    ::slotted([data-canvas-visual]) {
      position: absolute;
      display: block;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
      box-sizing: border-box;
    }

    ::slotted([data-canvas-filter-visual]) {
      overflow: visible;
      z-index: 5;
    }

    @media (max-width: 767px) {
      :host,
      .surface,
      .viewport,
      .sizer,
      .frame-wrap,
      .frame {
        width: 100%;
        height: auto;
        min-height: 0;
      }

      .viewport {
        overflow: visible;
      }

      .sizer {
        display: block;
      }

      .frame-wrap {
        position: relative;
      }

      .frame {
        position: relative;
        inset: auto;
        display: grid;
        gap: var(--base-size-12);
        transform: none;
        background: transparent;
      }

      ::slotted([data-canvas-visual]) {
        position: relative !important;
        inset: auto !important;
        width: 100% !important;
        height: auto !important;
        min-height: 132px;
        overflow: visible;
      }

      ::slotted([data-component-kind='header']) {
        min-height: 96px;
      }

      ::slotted([data-component-kind='slicer']) {
        min-height: 88px;
      }

      ::slotted([data-slicer-style='dropdown']) {
        min-height: 90px;
      }

      ::slotted([data-slicer-style='input']) {
        min-height: 112px;
      }

      ::slotted([data-slicer-style='numeric_range']),
      ::slotted([data-slicer-style='date_range']) {
        min-height: 172px;
      }

      ::slotted([data-slicer-style='relative_period']) {
        min-height: 184px;
      }

      ::slotted([data-component-kind='visual'][data-visual-type='kpi']) {
        height: auto !important;
        min-height: 144px;
        overflow: hidden;
      }

      ::slotted([data-component-kind='visual']:not([data-visual-type='kpi'])) {
        height: 520px !important;
        min-height: 320px;
        overflow: hidden;
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('lv-report-zoom-command', this.onZoomCommand as EventListener)
    this.resizeObserver = new ResizeObserver(() => this.updateScale())
    this.updateComplete.then(() => {
      this.resizeObserver?.observe(this)
      this.positionVisuals()
      this.updateScale()
      this.emitZoomState()
    })
  }

  disconnectedCallback(): void {
    document.removeEventListener('lv-report-zoom-command', this.onZoomCommand as EventListener)
    this.resizeObserver?.disconnect()
    super.disconnectedCallback()
  }

  updated(): void {
    this.positionVisuals()
    this.updateScale()
  }

  private updateScale(): void {
    const hostRect = this.getBoundingClientRect()
    const availableWidth = Math.max(0, hostRect.width)
    const availableHeight = Math.max(0, hostRect.height)
    if (!availableWidth || !availableHeight || !this.width || !this.contentHeight) return
    const responsive = availableWidth < 768
    const widthScale = availableWidth / this.width
    const heightScale = availableHeight / this.contentHeight
    let nextMode: PresentationMode = this.zoomMode
    let nextScale = 1

    if (responsive) {
      nextMode = 'responsive'
    } else if (this.zoomMode === 'fit-width') {
      nextScale = widthScale
    } else if (this.zoomMode === 'fit-page') {
      const fitPageScale = Math.min(widthScale, heightScale)
      if (fitPageScale < 0.75) {
        nextMode = 'fit-width'
        nextScale = widthScale
      } else {
        nextScale = fitPageScale
      }
    } else if (this.zoomMode === 'custom') {
      nextScale = this.customScale
    }
    nextScale = clampScale(nextScale)
    const modeChanged = nextMode !== this.presentationMode
    const scaleChanged = Math.abs(nextScale - this.scale) > 0.001
    if (modeChanged) this.presentationMode = nextMode
    if (scaleChanged) {
      const anchor = this.zoomAnchor
      this.scale = nextScale
      this.emitZoomState()
      if (anchor) {
        this.updateComplete.then(() => this.restoreZoomAnchor(anchor))
      }
    } else if (!modeChanged) {
      this.zoomAnchor = undefined
    }
    if (modeChanged && !scaleChanged) this.emitZoomState()
  }

  private positionVisuals(): void {
    const slot = this.shadowRoot?.querySelector('slot:not([name])') as HTMLSlotElement | null
    const assigned = slot?.assignedElements({ flatten: true }) ?? []
    let nextContentHeight = this.height
    for (const element of assigned) {
      if (!(element instanceof HTMLElement)) continue
      this.positionVisual(element as VisualElement)
      const y = parseCanvasNumber(element.dataset.y, 0)
      const height = parseCanvasNumber(element.dataset.h, 180)
      nextContentHeight = Math.max(nextContentHeight, y + height + 16)
    }
    if (nextContentHeight !== this.contentHeight) {
      this.contentHeight = nextContentHeight
    }
  }

  private positionVisual(element: VisualElement): void {
    const x = parseCanvasNumber(element.dataset.x, 0)
    const y = parseCanvasNumber(element.dataset.y, 0)
    const width = parseCanvasNumber(element.dataset.w, 280)
    const height = parseCanvasNumber(element.dataset.h, 180)
    element.style.left = `${x}px`
    element.style.top = `${y}px`
    element.style.width = `${width}px`
    element.style.height = `${height}px`
  }

  private setZoomMode(mode: ZoomMode): void {
    this.zoomMode = mode
    try {
      localStorage.setItem(zoomStorageKey(), mode)
    } catch {
      // Ignore storage failures; the active component state still updates.
    }
    this.updateComplete.then(() => this.updateScale())
    this.updateComplete.then(() => this.emitZoomState())
  }

  private onZoomCommand = (event: CustomEvent<ZoomCommand>): void => {
    const detail = event.detail ?? {}
    this.zoomAnchor = this.captureZoomAnchor()
    if (detail.scale !== undefined) {
      this.customScale = clampScale(detail.scale)
      try {
        localStorage.setItem(zoomScaleStorageKey(), String(this.customScale))
      } catch {
        // Ignore storage failures; the active component state still updates.
      }
    }
    this.setZoomMode(detail.mode ?? (detail.scale !== undefined ? 'custom' : this.zoomMode))
  }

  private captureZoomAnchor(): ZoomAnchor {
    const viewport = this.viewportElement()
    const frame = this.frameWrapElement()
    if (!viewport || !frame || frame.offsetWidth === 0 || frame.offsetHeight === 0) {
      return { x: 0.5, y: 0.5 }
    }
    const centerX = viewport.scrollLeft + viewport.clientWidth / 2 - frame.offsetLeft
    const centerY = viewport.scrollTop + viewport.clientHeight / 2 - frame.offsetTop
    return {
      x: clampRatio(centerX / frame.offsetWidth),
      y: clampRatio(centerY / frame.offsetHeight),
    }
  }

  private restoreZoomAnchor(anchor: ZoomAnchor): void {
    const viewport = this.viewportElement()
    const frame = this.frameWrapElement()
    if (!viewport || !frame) {
      this.zoomAnchor = undefined
      return
    }
    const left = frame.offsetLeft + frame.offsetWidth * anchor.x - viewport.clientWidth / 2
    const top = frame.offsetTop + frame.offsetHeight * anchor.y - viewport.clientHeight / 2
    viewport.scrollLeft = clampScroll(left, viewport.scrollWidth - viewport.clientWidth)
    viewport.scrollTop = clampScroll(top, viewport.scrollHeight - viewport.clientHeight)
    this.zoomAnchor = undefined
  }

  private viewportElement(): HTMLDivElement | null {
    return this.shadowRoot?.querySelector('.viewport') ?? null
  }

  private frameWrapElement(): HTMLDivElement | null {
    return this.shadowRoot?.querySelector('.frame-wrap') ?? null
  }

  private emitZoomState(): void {
    this.dispatchEvent(new CustomEvent('lv-report-zoom-state', {
      detail: { mode: this.presentationMode, scale: this.scale },
      bubbles: true,
      composed: true,
    }))
  }

  render() {
    const style = [
      `--report-canvas-width:${this.width}`,
      `--report-canvas-height:${this.contentHeight}`,
      `--report-canvas-scale:${this.scale}`,
    ].join(';')

    return html`
      <div
        class="surface"
        style=${style}
        data-presentation-mode=${this.presentationMode}
        data-scale=${String(this.scale)}
      >
        <div class="viewport">
          <div class="sizer">
            <div class="frame-wrap">
              <div class="frame">
                <slot @slotchange=${this.positionVisuals}></slot>
              </div>
            </div>
          </div>
        </div>
      </div>
    `
  }
}

class ReportZoom extends LitElement {
  @state() private mode: PresentationMode = storedZoomMode()
  @state() private scale = storedCustomScale()

  static styles = css`
    :host {
      display: inline-block;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .zoom {
      display: inline-grid;
      grid-template-columns: auto auto auto auto minmax(86px, 176px) auto auto;
      align-items: center;
      min-height: 32px;
    }

    button {
      display: grid;
      width: 28px;
      height: 28px;
      place-items: center;
      border: 0;
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
      font: inherit;
    }

    button:hover,
    button:focus-visible {
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-default);
      outline: 0;
    }

    button[aria-pressed='true'] {
      background: var(--bgColor-accent-muted);
      color: var(--lv-fg-link);
    }

    svg {
      width: 15px;
      height: 15px;
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 2;
    }

    input {
      appearance: none;
      width: 100%;
      min-width: 0;
      height: 16px;
      background: transparent;
      cursor: pointer;
    }

    input::-webkit-slider-runnable-track {
      height: 4px;
      border-radius: var(--lv-radius-full);
      background: var(--lv-line-muted);
    }

    input::-webkit-slider-thumb {
      appearance: none;
      width: 12px;
      height: 12px;
      margin-top: -4px;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-full);
      background: var(--lv-fg-muted);
    }

    input::-moz-range-track {
      height: 4px;
      border-radius: var(--lv-radius-full);
      background: var(--lv-line-muted);
    }

    input::-moz-range-thumb {
      width: 12px;
      height: 12px;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-full);
      background: var(--lv-fg-muted);
    }

    input:focus-visible {
      outline: 0;
    }

    input:focus-visible::-webkit-slider-thumb {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent-muted);
      outline-offset: 2px;
    }

    input:focus-visible::-moz-range-thumb {
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent-muted);
      outline-offset: 2px;
    }

    .slider {
      display: grid;
      min-width: 0;
      margin-inline: 6px;
      padding-inline: 10px;
      border-inline: var(--lv-border-muted);
    }

    .percent {
      min-width: 38px;
      color: var(--lv-fg-muted);
      text-align: center;
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
      white-space: nowrap;
    }

    .mode-label {
      width: auto;
      min-width: 32px;
      padding-inline: 7px;
      font: var(--lv-type-caption);
    }

    @media (max-width: 700px) {
      :host {
        display: none;
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('lv-report-zoom-state', this.onZoomState as EventListener)
  }

  disconnectedCallback(): void {
    document.removeEventListener('lv-report-zoom-state', this.onZoomState as EventListener)
    super.disconnectedCallback()
  }

  private onZoomState = (event: CustomEvent<{ mode: PresentationMode; scale: number }>): void => {
    this.mode = event.detail.mode
    this.scale = event.detail.scale
  }

  private command(detail: ZoomCommand): void {
    this.dispatchEvent(new CustomEvent('lv-report-zoom-command', {
      detail,
      bubbles: true,
      composed: true,
    }))
  }

  private nudge(delta: number): void {
    this.command({ mode: 'custom', scale: clampScale(this.scale + delta) })
  }

  private slide(event: Event): void {
    const input = event.currentTarget as HTMLInputElement
    this.command({ mode: 'custom', scale: clampScale(Number(input.value) / 100) })
  }

  render() {
    const percent = Math.round(this.scale * 100)
    return html`
      <div class="zoom" role="group" aria-label="Report zoom">
        <button class="mode-label" type="button" title="Fit width" aria-label="Fit width" aria-pressed=${String(this.mode === 'fit-width')} @click=${() => this.command({ mode: 'fit-width' })}>
          Width
        </button>
        <button type="button" title="Fit page" aria-label="Fit page" aria-pressed=${String(this.mode === 'fit-page')} @click=${() => this.command({ mode: 'fit-page' })}>
          ${zoomIcon('fit-page')}
        </button>
        <button class="mode-label" type="button" title="Actual size" aria-label="Actual size" aria-pressed=${String(this.mode === 'actual-size')} @click=${() => this.command({ mode: 'actual-size' })}>
          1:1
        </button>
        <button type="button" title="Zoom out" aria-label="Zoom out" @click=${() => this.nudge(-0.1)}>
          ${zoomIcon('minus')}
        </button>
        <div class="slider">
          <input type="range" min="50" max="200" .value=${String(percent)} aria-label="Zoom percent" @input=${this.slide} />
        </div>
        <button type="button" title="Zoom in" aria-label="Zoom in" @click=${() => this.nudge(0.1)}>
          ${zoomIcon('plus')}
        </button>
        <span class="percent">${percent}%</span>
      </div>
    `
  }
}

function parseCanvasNumber(value: string | undefined, fallback: number): number {
  if (!value) return fallback
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

customElements.define('lv-report-canvas', ReportCanvas)
customElements.define('lv-report-zoom', ReportZoom)

function zoomStorageKey(): string {
  return `leapview-report-zoom:${location.pathname}`
}

function zoomScaleStorageKey(): string {
  return `leapview-report-zoom-scale:${location.pathname}`
}

function storedZoomMode(): ZoomMode {
  try {
    const value = localStorage.getItem(zoomStorageKey())
    if (value === 'fit-width' || value === 'fit-page' || value === 'actual-size' || value === 'custom') {
      return value
    }
  } catch {
    // Ignore storage failures.
  }
  return 'fit-width'
}

function storedCustomScale(): number {
  try {
    return clampScale(Number(localStorage.getItem(zoomScaleStorageKey()) || 0.6))
  } catch {
    return 0.6
  }
}

function clampScale(value: number): number {
  if (!Number.isFinite(value)) return 1
  return Math.min(2, Math.max(0.5, value))
}

function clampRatio(value: number): number {
  if (!Number.isFinite(value)) return 0.5
  return Math.min(1, Math.max(0, value))
}

function clampScroll(value: number, max: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(Math.max(0, max), Math.max(0, value))
}

function zoomIcon(name: 'fit-page' | 'minus' | 'plus') {
  const icons: Record<'fit-page' | 'minus' | 'plus', IconNode> = {
    'fit-page': Maximize2,
    minus: Minus,
    plus: Plus,
  }

  return lucideIcon(icons[name])
}
