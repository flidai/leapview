import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import {
  autoMobileLayoutQuery,
  clampFittedScale,
  clampScale,
  layoutStorageKey,
  resolvedLayoutMode,
  storedCustomScale,
  storedLayoutMode,
  storedZoomMode,
  zoomScaleStorageKey,
  zoomStorageKey,
  type LayoutMode,
  type PresentationMode,
  type ResolvedLayout,
  type ZoomCommand,
  type ZoomMode,
} from './report-view-state'

type VisualElement = HTMLElement & {
  dataset: DOMStringMap
}

type ZoomAnchor = {
  x: number
  y: number
}

// Canonical documents own grid placement, not a pixel viewport. Keep a stable
// renderer-owned desktop canvas so resizing changes scroll/zoom, not chart geometry.
const defaultDesktopCanvasWidth = 1366

class ReportCanvas extends LitElement {
  @property({ type: Number }) width = defaultDesktopCanvasWidth
  @property({ type: Number }) height = 768
  @property({ type: Number }) columns = 0
  @property({ type: Number }) rowHeight = 0
  @property({ type: Number }) gap = 0
  @property({ type: Number }) padding = 0
  @state() private scale = 1
  @state() private layoutMode: LayoutMode = storedLayoutMode()
  @state() private resolvedLayout: ResolvedLayout = resolvedLayoutMode(this.layoutMode)
  @state() private zoomMode: ZoomMode = storedZoomMode()
  @state() private presentationMode: PresentationMode = 'fit-width'
  @state() private contentHeight = this.height
  @state() private contentWidth = this.width
  private customScale = storedCustomScale()
  private zoomAnchor?: ZoomAnchor

  private resizeObserver?: ResizeObserver
  private autoLayoutMediaQuery?: MediaQueryList

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

    :host([data-layout='desktop']) .viewport {
      scrollbar-gutter: stable;
    }

    @media (hover: hover) and (pointer: fine) {
      :host([data-layout='desktop']) .viewport {
        scrollbar-color: var(--lv-scrollbar-thumb) var(--lv-report-canvas-bg);
        scrollbar-width: thin;
      }

      :host([data-layout='desktop']) .viewport::-webkit-scrollbar {
        width: var(--base-size-8);
        height: var(--base-size-8);
      }

      :host([data-layout='desktop']) .viewport::-webkit-scrollbar-track,
      :host([data-layout='desktop']) .viewport::-webkit-scrollbar-corner {
        background: var(--lv-report-canvas-bg);
      }

      :host([data-layout='desktop']) .viewport::-webkit-scrollbar-thumb {
        min-width: var(--base-size-32);
        min-height: var(--base-size-32);
        border: var(--borderWidth-default) solid transparent;
        border-radius: var(--lv-radius-full);
        background: var(--lv-scrollbar-thumb);
        background-clip: padding-box;
      }

      :host([data-layout='desktop']) .viewport::-webkit-scrollbar-thumb:hover,
      :host([data-layout='desktop']) .viewport::-webkit-scrollbar-thumb:active {
        background: var(--lv-scrollbar-thumb-hover);
        background-clip: padding-box;
      }
    }

    .surface[data-presentation-mode='fit-width'] .viewport,
    .surface[data-presentation-mode='fit-page'] .viewport {
      overflow-x: hidden;
      overflow-y: auto;
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

    :host([data-layout='mobile']),
    :host([data-layout='mobile']) .surface,
    :host([data-layout='mobile']) .viewport,
    :host([data-layout='mobile']) .sizer,
    :host([data-layout='mobile']) .frame-wrap,
    :host([data-layout='mobile']) .frame {
      width: 100%;
      height: auto;
      min-height: 0;
    }

    :host([data-layout='mobile']) .viewport {
      overflow: visible;
    }

    :host([data-layout='mobile']) .sizer {
      display: block;
    }

    :host([data-layout='mobile']) .frame-wrap {
      position: relative;
    }

    :host([data-layout='mobile']) .frame {
      position: relative;
      inset: auto;
      display: grid;
      gap: var(--base-size-12);
      transform: none;
      background: transparent;
    }

    :host([data-layout='mobile']) ::slotted([data-canvas-visual]) {
      position: relative !important;
      inset: auto !important;
      width: 100% !important;
      height: auto !important;
      min-height: 132px;
      overflow: visible;
    }

    :host([data-layout='mobile']) ::slotted([data-component-kind='header']) {
      min-height: 96px;
    }

    :host([data-layout='mobile']) ::slotted([data-component-kind='slicer']) {
      min-height: 88px;
    }

    :host([data-layout='mobile']) ::slotted([data-slicer-style='dropdown']) {
      min-height: 90px;
    }

    :host([data-layout='mobile']) ::slotted([data-slicer-style='input']) {
      min-height: 112px;
    }

    :host([data-layout='mobile']) ::slotted([data-slicer-style='numeric_range']),
    :host([data-layout='mobile']) ::slotted([data-slicer-style='date_range']) {
      min-height: 172px;
    }

    :host([data-layout='mobile']) ::slotted([data-slicer-style='relative_period']) {
      min-height: 184px;
    }

    :host([data-layout='mobile']) ::slotted([data-component-kind='visual'][data-visual-type='kpi']) {
      height: auto !important;
      min-height: 144px;
      overflow: hidden;
    }

    :host([data-layout='mobile']) ::slotted([data-component-kind='visual']:not([data-visual-type='kpi'])) {
      height: 520px !important;
      min-height: 320px;
      overflow: hidden;
    }

    @media (max-width: 640px) {
      :host([data-layout='desktop']) {
        width: 100%;
        height: max(360px, calc(100svh - 12rem));
        min-height: 360px;
      }
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('lv-report-zoom-command', this.onZoomCommand as EventListener)
    this.autoLayoutMediaQuery = window.matchMedia(autoMobileLayoutQuery)
    this.autoLayoutMediaQuery.addEventListener('change', this.onAutoLayoutChange)
    this.syncResolvedLayout()
    this.resizeObserver = new ResizeObserver(() => {
      this.positionVisuals()
      this.updateScale()
    })
    this.updateComplete.then(() => {
      this.resizeObserver?.observe(this)
      this.positionVisuals()
      this.updateScale()
      this.emitZoomState()
    })
  }

  disconnectedCallback(): void {
    document.removeEventListener('lv-report-zoom-command', this.onZoomCommand as EventListener)
    this.autoLayoutMediaQuery?.removeEventListener('change', this.onAutoLayoutChange)
    this.autoLayoutMediaQuery = undefined
    this.resizeObserver?.disconnect()
    super.disconnectedCallback()
  }

  updated(): void {
    this.positionVisuals()
    this.updateScale()
  }

  private updateScale(): void {
    const hostRect = this.getBoundingClientRect()
    const viewport = this.viewportElement()
    const availableWidth = Math.max(0, viewport?.clientWidth ?? hostRect.width)
    const availableHeight = Math.max(0, viewport?.clientHeight ?? hostRect.height)
    if (!availableWidth || !availableHeight || !this.contentWidth || !this.contentHeight) return
    const nextLayout = resolvedLayoutMode(this.layoutMode, this.autoLayoutMediaQuery?.matches)
    const widthScale = availableWidth / this.contentWidth
    const heightScale = availableHeight / this.contentHeight
    let nextMode: PresentationMode = this.zoomMode
    let nextScale = 1

    if (nextLayout === 'mobile') {
      nextMode = 'mobile'
    } else if (this.zoomMode === 'fit-width') {
      nextScale = widthScale
    } else if (this.zoomMode === 'fit-page') {
      nextScale = Math.min(widthScale, heightScale)
    } else if (this.zoomMode === 'custom') {
      nextScale = this.customScale
    }
    const fittedMode = nextLayout === 'desktop' && (this.zoomMode === 'fit-width' || this.zoomMode === 'fit-page')
    nextScale = fittedMode ? clampFittedScale(nextScale) : clampScale(nextScale)
    const layoutChanged = nextLayout !== this.resolvedLayout
    const modeChanged = nextMode !== this.presentationMode
    const scaleChanged = Math.abs(nextScale - this.scale) > 0.001
    if (layoutChanged) {
      this.resolvedLayout = nextLayout
      this.setAttribute('data-layout', nextLayout)
    }
    if (modeChanged) this.presentationMode = nextMode
    if (scaleChanged) {
      const anchor = this.zoomAnchor
      this.scale = nextScale
      this.emitZoomState()
      if (anchor) {
        this.updateComplete.then(() => this.restoreZoomAnchor(anchor))
      }
    } else if (!modeChanged && !layoutChanged) {
      this.zoomAnchor = undefined
    }
    if ((layoutChanged || modeChanged) && !scaleChanged) this.emitZoomState()
  }

  private positionVisuals(): void {
    const slot = this.shadowRoot?.querySelector('slot:not([name])') as HTMLSlotElement | null
    const assigned = slot?.assignedElements({ flatten: true }) ?? []
    const responsive = this.responsiveGrid()
    const viewport = this.viewportElement()
    const hostRect = this.getBoundingClientRect()
    const layout = resolvedLayoutMode(this.layoutMode, this.autoLayoutMediaQuery?.matches)
    const nextContentWidth = responsive
      ? layout === 'desktop'
        ? defaultDesktopCanvasWidth
        : Math.max(0, viewport?.clientWidth ?? hostRect.width)
      : this.width
    if (nextContentWidth <= 0) return
    let nextContentHeight = responsive ? this.padding * 2 : this.height
    for (const element of assigned) {
      if (!(element instanceof HTMLElement)) continue
      const geometry = this.positionVisual(element as VisualElement, nextContentWidth, responsive)
      nextContentHeight = Math.max(nextContentHeight, geometry.y + geometry.height + (responsive ? this.padding : 16))
    }
    if (nextContentWidth !== this.contentWidth) {
      this.contentWidth = nextContentWidth
    }
    if (nextContentHeight !== this.contentHeight) {
      this.contentHeight = nextContentHeight
    }
  }

  private positionVisual(element: VisualElement, canvasWidth: number, responsive: boolean): { x: number; y: number; width: number; height: number } {
    let x = parseCanvasNumber(element.dataset.x, 0)
    let y = parseCanvasNumber(element.dataset.y, 0)
    let width = parseCanvasNumber(element.dataset.w, 280)
    let height = parseCanvasNumber(element.dataset.h, 180)
    if (responsive) {
      const col = parseCanvasNumber(element.dataset.col, 0)
      const row = parseCanvasNumber(element.dataset.row, 0)
      const colSpan = parseCanvasNumber(element.dataset.colSpan, 0)
      const rowSpan = parseCanvasNumber(element.dataset.rowSpan, 0)
      const availableWidth = canvasWidth - this.padding * 2 - this.gap * (this.columns - 1)
      const columnWidth = availableWidth / this.columns
      x = this.padding + (col - 1) * (columnWidth + this.gap)
      y = this.padding + (row - 1) * (this.rowHeight + this.gap)
      width = colSpan * columnWidth + (colSpan - 1) * this.gap
      height = rowSpan * this.rowHeight + (rowSpan - 1) * this.gap
    }
    element.style.left = `${x}px`
    element.style.top = `${y}px`
    element.style.width = `${width}px`
    element.style.height = `${height}px`
    return { x, y, width, height }
  }

  private responsiveGrid(): boolean {
    return this.width <= 0
      && this.columns > 0
      && this.rowHeight > 0
      && this.gap >= 0
      && this.padding >= 0
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
    if (detail.layout !== undefined) {
      this.layoutMode = detail.layout
      try {
        localStorage.setItem(layoutStorageKey(), detail.layout)
      } catch {
        // Ignore storage failures; the active component state still updates.
      }
    }
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

  private onAutoLayoutChange = (): void => {
    this.updateScale()
  }

  private syncResolvedLayout(): void {
    const layout = resolvedLayoutMode(this.layoutMode, this.autoLayoutMediaQuery?.matches)
    this.resolvedLayout = layout
    this.setAttribute('data-layout', layout)
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
      detail: {
        layoutMode: this.layoutMode,
        layout: this.resolvedLayout,
        mode: this.presentationMode,
        scale: this.scale,
      },
      bubbles: true,
      composed: true,
    }))
  }

  render() {
    const style = [
      `--report-canvas-width:${this.contentWidth}`,
      `--report-canvas-height:${this.contentHeight}`,
      `--report-canvas-scale:${this.scale}`,
    ].join(';')

    return html`
      <div
        class="surface"
        style=${style}
        data-layout=${this.resolvedLayout}
        data-presentation-mode=${this.presentationMode}
        data-scale=${String(this.scale)}
      >
        <div
          class="viewport"
          role=${this.resolvedLayout === 'desktop' ? 'region' : nothing}
          aria-label=${this.resolvedLayout === 'desktop' ? 'Scrollable report canvas' : nothing}
          tabindex=${this.resolvedLayout === 'desktop' ? '0' : nothing}
        >
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

function parseCanvasNumber(value: string | undefined, fallback: number): number {
  if (!value) return fallback
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

customElements.define('lv-report-canvas', ReportCanvas)

function clampRatio(value: number): number {
  if (!Number.isFinite(value)) return 0.5
  return Math.min(1, Math.max(0, value))
}

function clampScroll(value: number, max: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.min(Math.max(0, max), Math.max(0, value))
}
