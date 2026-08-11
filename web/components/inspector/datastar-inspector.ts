/**
 * Datastar Inspector - Dev-only debugging tool
 *
 * A development-only view of effective Datastar signal state and history.
 */
import { LitElement, css, html, nothing, type TemplateResult } from 'lit'
import { customElement, state } from 'lit/decorators.js'
import { ChevronDown, ChevronRight, X } from 'lucide'

import { lucideIcon } from '../shared/lucide-icons'
import type {
  InspectorPosition,
  InspectorState,
  PageStreamSignalChange,
  PageStreamSignalLeaf,
  PageStreamSignalsResponse,
  SignalObject,
} from './types.js'
import {
  countSignals,
  filterObject,
  findChangedPaths,
  parseFilterPattern,
} from './utils.js'

const FLASH_DURATION = 400
const STORAGE_KEY = 'ds-inspector'
const SIGNAL_POLL_INTERVAL = 500
const SIGNAL_HISTORY_LIMIT = 500
const DRAG_THRESHOLD = 4
const VIEWPORT_MARGIN = 8

type DragTarget = 'toggle' | 'panel'

interface InspectorDrag {
  target: DragTarget
  pointerId: number
  startX: number
  startY: number
  originX: number
  originY: number
  width: number
  height: number
  moved: boolean
  captureElement: HTMLElement
}

@customElement('datastar-inspector')
export class DatastarInspector extends LitElement {
  @state() private expanded = false
  @state() private filter = ''
  @state() private signals: SignalObject = {}
  @state() private signalCount = 0
  @state() private changedPaths: Set<string> = new Set()
  @state() private expandedPaths: Set<string> = new Set()
  @state() private hasUnseenChanges = false
  @state() private signalLeaves: PageStreamSignalLeaf[] = []
  @state() private signalHistory: PageStreamSignalChange[] = []
  @state() private signalStreamID = ''
  @state() private selectedSignalPath = ''
  @state() private signalError = ''
  @state() private togglePosition?: InspectorPosition
  @state() private panelPosition?: InspectorPosition

  private observer: MutationObserver | null = null
  private signalsElementId = `ds-inspector-signals-${Math.random().toString(36).slice(2, 9)}`
  private signalsElement: HTMLElement | null = null
  private previousSignals: SignalObject = {}
  private flashTimeout: number | null = null
  private parseFrame: number | null = null
  private signalTimer: number | null = null
  private signalAfter = 0
  private signalLoading = false
  private signalAbort: AbortController | null = null
  private drag?: InspectorDrag
  private suppressToggleClick = false
  private suppressToggleClickTimer: number | null = null

  static styles = css`
    :host {
      --ds-bg: var(--bgColor-default, #0d1117);
      --ds-panel: var(--overlay-bgColor, var(--bgColor-default, #161b22));
      --ds-panel-muted: var(--bgColor-muted, #21262d);
      --ds-fg: var(--fgColor-default, #f0f6fc);
      --ds-muted: var(--fgColor-muted, #8b949e);
      --ds-border: var(--borderColor-default, #30363d);
      --ds-border-muted: var(--borderColor-muted, #21262d);
      --ds-accent: var(--bgColor-accent-emphasis, #1f6feb);
      --ds-accent-fg: var(--fgColor-onEmphasis, #ffffff);
      --ds-success: var(--fgColor-success, #3fb950);
      --ds-warning: var(--fgColor-attention, #d29922);
      --ds-radius: var(--lv-radius-default, 6px);
      --ds-radius-full: var(--lv-radius-full, 999px);
      --ds-shadow: 0 18px 48px rgb(0 0 0 / 42%), 0 0 0 1px rgb(255 255 255 / 4%);
      color: var(--ds-fg);
      font-family: var(--fontStack-system, Inter, ui-sans-serif, system-ui, sans-serif);
    }

    * {
      box-sizing: border-box;
    }

    button,
    input {
      font: inherit;
    }

    .toggle {
      position: fixed;
      right: 16px;
      bottom: 16px;
      z-index: var(--zIndex-popover);
      display: grid;
      width: 38px;
      height: 38px;
      place-items: center;
      border: 1px solid color-mix(in srgb, var(--ds-accent), white 18%);
      border-radius: 50%;
      background:
        radial-gradient(circle at 35% 24%, rgb(255 255 255 / 22%), transparent 30%),
        linear-gradient(145deg, color-mix(in srgb, var(--ds-accent), white 10%), var(--ds-accent));
      color: var(--ds-accent-fg);
      cursor: pointer;
      font-size: 12px;
      font-weight: var(--base-text-weight-semibold);
      letter-spacing: 0;
      line-height: 1;
      box-shadow: 0 10px 28px rgb(0 0 0 / 38%), 0 0 0 1px rgb(255 255 255 / 8%) inset;
      touch-action: none;
      transition:
        transform var(--motion-transition-hover),
        box-shadow var(--motion-transition-hover),
        filter var(--motion-transition-hover);
      user-select: none;
    }

    .toggle:hover,
    .toggle:focus-visible {
      filter: brightness(1.08);
      transform: translateY(-1px);
      box-shadow: 0 14px 34px rgb(0 0 0 / 44%), 0 0 0 1px rgb(255 255 255 / 14%) inset;
      outline: 0;
    }

    .toggle[data-unseen] {
      animation: ds-pulse var(--base-duration-1000) var(--motion-easing-move) infinite;
    }

    @keyframes ds-pulse {
      0%,
      100% {
        box-shadow: 0 10px 28px rgb(0 0 0 / 38%), 0 0 0 0 color-mix(in srgb, var(--ds-accent), transparent 40%);
      }

      50% {
        box-shadow: 0 10px 28px rgb(0 0 0 / 38%), 0 0 0 7px color-mix(in srgb, var(--ds-accent), transparent 84%);
      }
    }

    .panel {
      position: fixed;
      right: 16px;
      bottom: 16px;
      z-index: var(--zIndex-popover);
      display: flex;
      width: min(760px, calc(100vw - 32px));
      height: min(600px, calc(100vh - 32px));
      overflow: hidden;
      flex-direction: column;
      border: 1px solid var(--ds-border);
      border-radius: 10px;
      background: var(--ds-panel);
      box-shadow: var(--ds-shadow);
    }

    .header {
      display: flex;
      align-items: center;
      gap: 8px;
      border-bottom: 1px solid var(--ds-border-muted);
      background: color-mix(in srgb, var(--ds-panel-muted), var(--ds-panel) 32%);
      padding: 8px 10px;
    }

    .badge {
      display: inline-grid;
      min-width: 26px;
      height: 20px;
      place-items: center;
      border-radius: var(--ds-radius-full);
      background: var(--ds-accent);
      color: var(--ds-accent-fg);
      font-size: 11px;
      font-weight: var(--base-text-weight-semibold);
      line-height: 1;
    }

    .drag-handle {
      border: 0;
      cursor: grab;
      padding: 0;
      touch-action: none;
      user-select: none;
    }

    .drag-handle:active {
      cursor: grabbing;
    }

    .drag-handle:focus-visible {
      outline: 2px solid var(--ds-accent-fg);
      outline-offset: 1px;
    }

    .filter {
      min-width: 0;
      height: 26px;
      flex: 1;
      border: 1px solid var(--ds-border);
      border-radius: var(--ds-radius);
      background: var(--ds-bg);
      color: var(--ds-fg);
      font-size: 12px;
      outline: 0;
      padding: 0 8px;
    }

    .filter::placeholder {
      color: var(--ds-muted);
      opacity: 0.8;
    }

    .filter:focus {
      border-color: var(--ds-accent);
      box-shadow: 0 0 0 2px color-mix(in srgb, var(--ds-accent), transparent 78%);
    }

    .icon-button {
      display: grid;
      width: 26px;
      height: 26px;
      place-items: center;
      border: 1px solid transparent;
      border-radius: var(--ds-radius);
      background: transparent;
      color: var(--ds-muted);
      cursor: pointer;
      font-size: 18px;
      line-height: 1;
      padding: 0;
    }

    .icon-button:hover,
    .icon-button:focus-visible {
      border-color: var(--ds-border);
      background: var(--ds-panel-muted);
      color: var(--ds-fg);
      outline: 0;
    }

    .content {
      min-height: 0;
      flex: 1;
      overflow: auto;
      padding: 10px;
    }

    .empty {
      display: grid;
      height: 100%;
      place-items: center;
      color: var(--ds-muted);
      font-size: 13px;
    }

    .tree {
      font-family: var(--fontStack-monospace, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
      font-size: 12px;
      line-height: 1.55;
    }

    .row,
    .branch {
      min-width: 0;
      border: 0;
      border-radius: 4px;
      color: var(--ds-muted);
      white-space: nowrap;
    }

    .row {
      display: flex;
      align-items: baseline;
      gap: 4px;
      padding: 2px 4px;
    }

    button.row {
      width: 100%;
      background: transparent;
      cursor: pointer;
      font-family: inherit;
      font-size: inherit;
      text-align: left;
    }

    button.row:hover,
    button.row:focus-visible,
    button.row[aria-selected='true'] {
      background: var(--ds-panel-muted);
      color: var(--ds-fg);
      outline: 0;
    }

    button.row[aria-selected='true'] {
      box-shadow: 2px 0 0 var(--ds-accent) inset;
    }

    .branch {
      display: flex;
      align-items: center;
      gap: 4px;
      background: transparent;
      cursor: pointer;
      font-family: inherit;
      font-size: inherit;
      padding: 2px 4px;
      text-align: left;
    }

    .branch:hover,
    .branch:focus-visible {
      background: var(--ds-panel-muted);
      outline: 0;
    }

    .changed {
      background: color-mix(in srgb, var(--ds-warning), transparent 82%);
    }

    .key {
      color: color-mix(in srgb, var(--ds-muted), var(--ds-fg) 18%);
      font-weight: var(--base-text-weight-normal);
    }

    .separator,
    .chevron,
    .null {
      color: var(--ds-muted);
      opacity: 0.8;
    }

    .chevron {
      display: inline-grid;
      width: 16px;
      place-items: center;
      text-align: center;
    }

    .chevron svg {
      width: 14px;
      height: 14px;
    }

    .count {
      border: 1px solid var(--ds-border);
      border-radius: var(--ds-radius-full);
      background: var(--ds-panel-muted);
      color: var(--ds-muted);
      font-size: 10px;
      font-weight: var(--base-text-weight-normal);
      line-height: 16px;
      padding: 0 6px;
    }

    .value {
      min-width: 0;
      max-width: 16rem;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .boolean {
      color: var(--ds-accent);
    }

    .number {
      color: var(--ds-warning);
    }

    .string {
      color: var(--ds-success);
    }

    .signal-error {
      color: var(--ds-warning);
      font-size: 12px;
      padding: 8px 10px 0;
    }

    .signal-content {
      display: flex;
      overflow: hidden;
      flex-direction: column;
      padding: 0;
    }

    .signal-workbench {
      display: grid;
      min-height: 0;
      flex: 1;
      grid-template-columns: minmax(250px, 0.9fr) minmax(300px, 1.1fr);
    }

    .signal-tree-pane,
    .signal-history-pane {
      min-height: 0;
      overflow: auto;
      padding: 10px;
    }

    .signal-tree-pane {
      border-right: 1px solid var(--ds-border-muted);
    }

    .signal-path {
      color: var(--ds-fg);
      font-family: var(--fontStack-monospace, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
      font-size: 12px;
      font-weight: var(--base-text-weight-semibold);
      overflow-wrap: anywhere;
    }

    .signal-section-label {
      color: var(--ds-muted);
      font-size: 10px;
      letter-spacing: 0.04em;
      margin: 14px 0 5px;
      text-transform: uppercase;
    }

    .signal-current {
      border: 1px solid var(--ds-border-muted);
      border-radius: var(--ds-radius);
      background: var(--ds-bg);
      font-family: var(--fontStack-monospace, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
      font-size: 12px;
      margin: 0;
      overflow-wrap: anywhere;
      padding: 8px;
      white-space: pre-wrap;
    }

    .signal-sparkline {
      display: block;
      width: 100%;
      height: 54px;
      border: 1px solid var(--ds-border-muted);
      border-radius: var(--ds-radius);
      background: var(--ds-bg);
    }

    .signal-sparkline polyline {
      fill: none;
      stroke: var(--ds-accent);
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 2;
    }

    .signal-change {
      display: grid;
      grid-template-columns: 68px minmax(0, 1fr);
      align-items: start;
      gap: 8px;
      border-top: 1px solid var(--ds-border-muted);
      font-size: 11px;
      padding: 8px 0;
    }

    .signal-change-time,
    .signal-change-meta {
      color: var(--ds-muted);
    }

    .signal-change-value {
      color: var(--ds-fg);
      font-family: var(--fontStack-monospace, ui-monospace, SFMono-Regular, Menlo, Consolas, monospace);
      overflow-wrap: anywhere;
    }

    .signal-change-removed {
      color: var(--ds-warning);
      font-style: italic;
    }

    @media (max-width: 620px) {
      .signal-workbench {
        grid-template-columns: 1fr;
        grid-template-rows: minmax(180px, 0.9fr) minmax(220px, 1.1fr);
      }

      .signal-tree-pane {
        border-right: 0;
        border-bottom: 1px solid var(--ds-border-muted);
      }
    }
  `

  override connectedCallback() {
    super.connectedCallback()
    this.ensureSignalsElement()
    this.loadState()
    window.addEventListener('resize', this.handleViewportResize)
  }

  override disconnectedCallback() {
    super.disconnectedCallback()
    this.observer?.disconnect()
    if (this.parseFrame !== null) {
      cancelAnimationFrame(this.parseFrame)
    }
    if (this.flashTimeout) {
      clearTimeout(this.flashTimeout)
    }
    if (this.signalTimer !== null) {
      clearInterval(this.signalTimer)
    }
    this.signalAbort?.abort()
    if (this.suppressToggleClickTimer !== null) clearTimeout(this.suppressToggleClickTimer)
    this.stopDrag()
    window.removeEventListener('resize', this.handleViewportResize)
  }

  override firstUpdated() {
    this.setupSignalObserver()
    this.startSignalPolling()
    this.handleViewportResize()
  }

  private ensureSignalsElement() {
    let el = this.querySelector<HTMLElement>('[data-json-signals]')
    if (!el) {
      el = document.createElement('pre')
      el.setAttribute('data-json-signals', '')
      this.append(el)
    }
    el.id = this.signalsElementId
    el.hidden = true
    el.style.display = 'none'
  }

  private loadState() {
    try {
      const saved = sessionStorage.getItem(STORAGE_KEY)
      if (saved) {
        const state: InspectorState = JSON.parse(saved)
        this.expanded = state.expanded ?? false
        this.filter = state.filter ?? ''
        this.expandedPaths = new Set(state.expandedPaths ?? [])
        this.togglePosition = validPosition(state.togglePosition)
        this.panelPosition = validPosition(state.panelPosition)
      }
    } catch {
      /* ignore parse errors */
    }
  }

  private saveState() {
    const state: InspectorState = {
      expanded: this.expanded,
      filter: this.filter,
      expandedPaths: [...this.expandedPaths],
      togglePosition: this.togglePosition,
      panelPosition: this.panelPosition,
    }
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(state))
  }

  private setupSignalObserver() {
    const el = this.querySelector<HTMLElement>(`#${this.signalsElementId}`)
    if (!el) return

    this.observer?.disconnect()
    this.signalsElement = el
    this.parseSignals(el.textContent || '{}', true)

    this.observer = new MutationObserver(() => {
      this.scheduleSignalParse()
    })
    this.observer.observe(el, { childList: true, characterData: true, subtree: true })
  }

  private scheduleSignalParse() {
    if (this.parseFrame !== null) {
      cancelAnimationFrame(this.parseFrame)
    }
    this.parseFrame = requestAnimationFrame(() => {
      this.parseFrame = null
      this.parseSignals(this.signalsElement?.textContent || '{}', false)
    })
  }

  private parseSignals(json: string, isInitial: boolean) {
    try {
      const newSignals = JSON.parse(json) as SignalObject

      if (!isInitial && Object.keys(this.previousSignals).length > 0) {
        const changed = findChangedPaths(this.previousSignals, newSignals)
        if (changed.size > 0) {
          this.changedPaths = changed

          if (!this.expanded) {
            this.hasUnseenChanges = true
          }

          if (this.flashTimeout) {
            clearTimeout(this.flashTimeout)
          }
          this.flashTimeout = window.setTimeout(() => {
            this.changedPaths = new Set()
            this.hasUnseenChanges = false
          }, FLASH_DURATION)
        }
      }

      this.previousSignals = newSignals
      this.signals = newSignals
      this.signalCount = countSignals(this.signals)
    } catch {
      this.signals = {}
      this.signalCount = 0
    }
  }

  private getFilteredSignals(): SignalObject {
    if (!this.filter.trim()) return this.signals

    const regex = parseFilterPattern(this.filter.trim())
    return filterObject(this.signals, regex) as SignalObject
  }

  private toggle() {
    this.expanded = !this.expanded
    this.saveState()
    if (this.expanded) {
      this.hasUnseenChanges = false
    }
    void this.updateComplete.then(this.handleViewportResize)
  }

  private handleToggleClick() {
    if (this.suppressToggleClick) {
      this.suppressToggleClick = false
      if (this.suppressToggleClickTimer !== null) clearTimeout(this.suppressToggleClickTimer)
      this.suppressToggleClickTimer = null
      return
    }
    this.toggle()
  }

  private startDrag(target: DragTarget, event: PointerEvent) {
    if (event.button !== 0) return
    const captureElement = event.currentTarget
    if (!(captureElement instanceof HTMLElement)) return
    const element = target === 'panel' ? captureElement.closest('.panel') : captureElement
    if (!(element instanceof HTMLElement)) return
    captureElement.setPointerCapture(event.pointerId)
    const rect = element.getBoundingClientRect()
    this.drag = {
      target,
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      originX: rect.x,
      originY: rect.y,
      width: rect.width,
      height: rect.height,
      moved: false,
      captureElement,
    }
    window.addEventListener('pointermove', this.handleDragMove, { passive: false })
    window.addEventListener('pointerup', this.handleDragEnd)
    window.addEventListener('pointercancel', this.handleDragEnd)
  }

  private handleDragMove = (event: PointerEvent) => {
    const drag = this.drag
    if (!drag || event.pointerId !== drag.pointerId) return
    const deltaX = event.clientX - drag.startX
    const deltaY = event.clientY - drag.startY
    if (!drag.moved && Math.hypot(deltaX, deltaY) < DRAG_THRESHOLD) return
    drag.moved = true
    event.preventDefault()
    const position = clampPosition({ x: drag.originX + deltaX, y: drag.originY + deltaY }, drag.width, drag.height)
    if (drag.target === 'toggle') {
      this.togglePosition = position
    } else {
      this.panelPosition = position
    }
  }

  private handleDragEnd = (event: PointerEvent) => {
    if (!this.drag || event.pointerId !== this.drag.pointerId) return
    const { target, moved } = this.drag
    if (moved) {
      this.saveState()
      if (target === 'toggle') {
        this.suppressToggleClick = true
        this.suppressToggleClickTimer = window.setTimeout(() => {
          this.suppressToggleClick = false
          this.suppressToggleClickTimer = null
        }, 0)
      }
    }
    this.stopDrag()
  }

  private stopDrag() {
    const drag = this.drag
    if (drag?.captureElement.hasPointerCapture(drag.pointerId)) {
      drag.captureElement.releasePointerCapture(drag.pointerId)
    }
    this.drag = undefined
    window.removeEventListener('pointermove', this.handleDragMove)
    window.removeEventListener('pointerup', this.handleDragEnd)
    window.removeEventListener('pointercancel', this.handleDragEnd)
  }

  private moveWithKeyboard(target: DragTarget, event: KeyboardEvent) {
    const movement: Record<string, InspectorPosition> = {
      ArrowLeft: { x: -10, y: 0 },
      ArrowRight: { x: 10, y: 0 },
      ArrowUp: { x: 0, y: -10 },
      ArrowDown: { x: 0, y: 10 },
    }
    const delta = movement[event.key]
    if (!delta) return
    const element = event.currentTarget instanceof HTMLElement
      ? (target === 'panel' ? event.currentTarget.closest('.panel') : event.currentTarget)
      : null
    if (!(element instanceof HTMLElement)) return
    event.preventDefault()
    const rect = element.getBoundingClientRect()
    const position = clampPosition({ x: rect.x + delta.x, y: rect.y + delta.y }, rect.width, rect.height)
    if (target === 'toggle') {
      this.togglePosition = position
    } else {
      this.panelPosition = position
    }
    this.saveState()
  }

  private handleViewportResize = () => {
    const target: DragTarget = this.expanded ? 'panel' : 'toggle'
    const element = this.shadowRoot?.querySelector<HTMLElement>(target === 'panel' ? '.panel' : '.toggle')
    const current = target === 'panel' ? this.panelPosition : this.togglePosition
    if (!element || !current) return
    const rect = element.getBoundingClientRect()
    const next = clampPosition(current, rect.width, rect.height)
    if (next.x === current.x && next.y === current.y) return
    if (target === 'panel') {
      this.panelPosition = next
    } else {
      this.togglePosition = next
    }
    this.saveState()
  }

  private close() {
    this.expanded = false
    this.saveState()
    void this.updateComplete.then(this.handleViewportResize)
  }

  private handleFilterInput(e: Event) {
    this.filter = (e.target as HTMLInputElement).value
    this.saveState()
  }

  private clearFilter() {
    this.filter = ''
    this.saveState()
  }

  private startSignalPolling() {
    if (!this.signalsURL()) return
    void this.loadSignals()
    this.signalTimer = window.setInterval(() => {
      void this.loadSignals()
    }, SIGNAL_POLL_INTERVAL)
  }

  private signalsURL() {
    return this.getAttribute('signals-url')?.trim() ?? ''
  }

  private preferredSignalStreamID() {
    const runtime = this.signals.runtime
    if (!runtime || Array.isArray(runtime) || typeof runtime !== 'object') return this.signalStreamID
    const values = runtime as Record<string, unknown>
    const parts = ['clientId', 'dashboardId', 'pageId'].map((key) => typeof values[key] === 'string' ? values[key] as string : '')
    if (parts.some((part) => !part)) return this.signalStreamID
    if (typeof values.streamInstanceId === 'string' && values.streamInstanceId) parts.push(values.streamInstanceId)
    return parts.join(':')
  }

  private async loadSignals() {
    const rawURL = this.signalsURL()
    if (!rawURL || this.signalLoading) return
    const requestedPath = this.selectedSignalPath
    this.signalLoading = true
    this.signalAbort?.abort()
    this.signalAbort = new AbortController()
    try {
      const url = new URL(rawURL, window.location.href)
      const streamID = this.preferredSignalStreamID()
      if (streamID) url.searchParams.set('streamId', streamID)
      if (this.selectedSignalPath) url.searchParams.set('path', this.selectedSignalPath)
      if (this.signalAfter > 0) url.searchParams.set('after', String(this.signalAfter))
      url.searchParams.set('limit', '200')
      const response = await fetch(url, {
        cache: 'no-store',
        credentials: 'same-origin',
        signal: this.signalAbort.signal,
      })
      if (!response.ok) throw new Error(`Signal history request failed (${response.status})`)
      const body = await response.json() as PageStreamSignalsResponse
      if (this.signalStreamID && body.streamId && this.signalStreamID !== body.streamId) {
        this.signalAfter = 0
        this.signalHistory = []
      }
      this.signalStreamID = body.streamId || streamID
      this.signals = body.state && typeof body.state === 'object' ? body.state : {}
      this.signalLeaves = Array.isArray(body.leaves) ? body.leaves : []
      this.signalCount = this.signalLeaves.length
      const incoming = Array.isArray(body.history) ? body.history : []
      if (incoming.length > 0) {
        const byID = new Map(this.signalHistory.map((change) => [change.id, change]))
        for (const change of incoming) byID.set(change.id, change)
        this.signalHistory = [...byID.values()].sort((left, right) => right.id - left.id).slice(0, SIGNAL_HISTORY_LIMIT)
      }
      this.signalAfter = Math.max(this.signalAfter, Number(body.nextAfter) || 0)
      this.signalError = ''
    } catch (error) {
      if (!(error instanceof DOMException && error.name === 'AbortError')) {
        this.signalError = error instanceof Error ? error.message : String(error)
      }
    } finally {
      this.signalLoading = false
      if (requestedPath !== this.selectedSignalPath) {
        void this.loadSignals()
      }
    }
  }

  private selectSignal(path: string) {
    if (this.selectedSignalPath === path) return
    this.selectedSignalPath = path
    this.signalHistory = []
    this.signalAfter = 0
    void this.loadSignals()
  }

  private togglePath(path: string) {
    const next = new Set(this.expandedPaths)
    if (next.has(path)) {
      next.delete(path)
    } else {
      next.add(path)
    }
    this.expandedPaths = next
    this.saveState()
  }

  override render() {
    return html`
      ${this.expanded ? this.renderOpenPanel() : this.renderToggle()}
    `
  }

  private renderToggle() {
    return html`
      <button
        class="toggle"
        style=${positionStyle(this.togglePosition)}
        ?data-unseen=${this.hasUnseenChanges}
        @pointerdown=${(event: PointerEvent) => this.startDrag('toggle', event)}
        @keydown=${(event: KeyboardEvent) => this.moveWithKeyboard('toggle', event)}
        @click=${this.handleToggleClick}
        aria-label="Open or move Datastar Inspector"
        title="Drag to move · Click to open"
      >
        DS
      </button>
    `
  }

  private renderOpenPanel() {
    const filteredSignals = this.getFilteredSignals()
    const filteredCount = countSignals(filteredSignals)
    const hasFilter = this.filter.trim().length > 0
    return this.renderPanel(this.renderSignalContent(filteredSignals, hasFilter), filteredCount, this.signalCount)
  }

  private renderPanel(content: unknown, filteredCount: number, totalCount: number) {
    const hasFilter = this.filter.trim().length > 0
    return html`
      <div class="panel" style=${positionStyle(this.panelPosition)}>
        ${this.renderHeader(filteredCount, totalCount, hasFilter)}
        ${content}
      </div>
    `
  }

  private renderHeader(filteredCount: number, totalCount: number, hasFilter: boolean) {
    const placeholder = hasFilter
      ? `${filteredCount}/${totalCount} match...`
      : `Filter ${totalCount} signals...`

    return html`
      <div class="header">
        <button
          type="button"
          class="badge drag-handle"
          aria-label="Move Datastar Inspector"
          title="Drag to move"
          @pointerdown=${(event: PointerEvent) => this.startDrag('panel', event)}
          @keydown=${(event: KeyboardEvent) => this.moveWithKeyboard('panel', event)}
        >DS</button>
        <input
          type="text"
          class="filter"
          placeholder="${placeholder}"
          .value=${this.filter}
          @input=${this.handleFilterInput}
        />
        ${hasFilter
          ? html`<button class="icon-button" @click=${this.clearFilter} aria-label="Clear filter">${lucideIcon(X, { size: 14 })}</button>`
          : nothing}
        <button class="icon-button" @click=${this.close} aria-label="Close">${lucideIcon(X, { size: 14 })}</button>
      </div>
    `
  }

  private renderSignalContent(filteredSignals: SignalObject, hasFilter: boolean) {
    const isEmpty = Object.keys(filteredSignals).length === 0

    return html`
      <div class="content signal-content">
        ${this.signalError ? html`<div class="signal-error" role="alert">${this.signalError}</div>` : nothing}
        <div class="signal-workbench">
          <div class="signal-tree-pane">
            ${isEmpty
              ? html`<div class="empty">
                  ${hasFilter ? 'No signals match filter' : 'No delivered signals found'}
                </div>`
              : this.renderJsonView(filteredSignals)}
          </div>
          <div class="signal-history-pane">
            ${this.renderSignalHistory()}
          </div>
        </div>
      </div>
    `
  }

  private renderSignalHistory() {
    if (!this.selectedSignalPath) {
      return html`<div class="empty">Select a signal to inspect its delivered history</div>`
    }
    const leaf = this.signalLeaves.find((candidate) => candidate.path === this.selectedSignalPath)
    const latest = this.signalHistory[0]
    const displayPath = leaf?.displayPath || latest?.displayPath || this.selectedSignalPath
    const removed = !leaf && latest?.operation === 'removed'
    const current = leaf
      ? JSON.stringify(leaf.value, null, 2)
      : this.signalLoading && this.signalHistory.length === 0
        ? 'Loading history…'
        : removed
          ? 'removed'
          : 'No current value'
    return html`
      <div class="signal-path">${displayPath}</div>
      <div class="signal-section-label">Current value</div>
      <pre class="signal-current ${removed ? 'signal-change-removed' : ''}">${current}</pre>
      ${this.renderSignalSparkline()}
      <div class="signal-section-label">Effective delivered changes · ${this.signalHistory.length}</div>
      ${this.signalHistory.length === 0
        ? html`<div class="empty">No retained changes for this signal</div>`
        : this.signalHistory.map((change) => this.renderSignalChange(change))}
    `
  }

  private renderSignalSparkline() {
    const numeric = [...this.signalHistory].reverse()
      .filter((change) => change.operation === 'set' && typeof change.value === 'number' && Number.isFinite(change.value))
      .map((change) => change.value as number)
    if (numeric.length < 2) return nothing
    const minValue = Math.min(...numeric)
    const maxValue = Math.max(...numeric)
    const span = maxValue - minValue || 1
    const points = numeric.map((value, index) => {
      const x = numeric.length === 1 ? 50 : 4 + (index / (numeric.length - 1)) * 92
      const y = 46 - ((value - minValue) / span) * 38
      return `${x.toFixed(1)},${y.toFixed(1)}`
    }).join(' ')
    return html`
      <div class="signal-section-label">Trend</div>
      <svg class="signal-sparkline" data-signal-sparkline viewBox="0 0 100 54" preserveAspectRatio="none" aria-label="Numeric signal history">
        <polyline points=${points}></polyline>
      </svg>
    `
  }

  private renderSignalChange(change: PageStreamSignalChange) {
    const time = new Date(change.timestamp).toLocaleTimeString([], { hour12: false })
    const value = change.operation === 'removed' ? 'removed' : JSON.stringify(change.value)
    const metadata = [
      `generation ${change.generation ?? '—'}`,
      change.origin || 'unknown origin',
      change.correlationId || 'no correlation',
    ].join(' · ')
    return html`
      <div class="signal-change" data-signal-change>
        <span class="signal-change-time">${time}</span>
        <div>
          <div class="signal-change-value ${change.operation === 'removed' ? 'signal-change-removed' : ''}">${value}</div>
          <div class="signal-change-meta">${metadata}</div>
        </div>
      </div>
    `
  }

  private renderJsonView(signals: SignalObject) {
    return html`
      <div class="tree">
        ${Object.entries(signals).map(([key, value]) => this.renderTreeNode(key, value, `/${this.escapePointerSegment(key)}`, 0, this.filter.trim().length > 0))}
      </div>
    `
  }

  private renderTreeNode(key: string, value: unknown, path: string, depth: number, hasFilter: boolean): TemplateResult {
    const changed = this.isChanged(path)
    const rowClass = changed ? ' changed' : ''
    const rowStyle = this.treeRowStyle(depth)

    if (!this.isBranch(value)) {
      return html`
        <button class="row${rowClass}" data-signal-path=${path} aria-selected=${String(this.selectedSignalPath === path)} style=${rowStyle} @click=${() => this.selectSignal(path)}>
          <span class="key">${key}</span>
          <span class="separator">:</span>
          ${this.renderPrimitive(value)}
        </button>
      `
    }

    const count = countSignals(value)
    const expanded = hasFilter || this.expandedPaths.has(path)
    const marker = Array.isArray(value) ? `[${count}]` : `{${count}}`

    return html`
      <div>
        <button
          type="button"
          class="branch${rowClass}"
          data-signal-branch=${path}
          style=${rowStyle}
          aria-expanded=${String(expanded)}
          aria-label=${expanded ? `Collapse ${key}` : `Expand ${key}`}
          @click=${() => this.togglePath(path)}
        >
          <span class="chevron">${lucideIcon(expanded ? ChevronDown : ChevronRight, { size: 14 })}</span>
          <span class="key">${key}</span>
          <span class="count">${marker}</span>
        </button>
        ${expanded
          ? this.childEntries(value).map(([childKey, childValue]) =>
              this.renderTreeNode(childKey, childValue, this.childPath(path, childKey), depth + 1, hasFilter)
            )
          : nothing}
      </div>
    `
  }

  private treeRowStyle(depth: number) {
    const indent = depth * 0.85
    const guide = depth > 0 ? 'border-left: 1px solid var(--borderColor-muted); padding-left: 0.55rem;' : ''
    return `margin-left: ${indent}rem; width: calc(100% - ${indent}rem); ${guide}`
  }

  private renderPrimitive(value: unknown) {
    if (value === null) {
      return html`<span class="null">null</span>`
    }
    if (typeof value === 'boolean') {
      return html`<span class="value boolean">${String(value)}</span>`
    }
    if (typeof value === 'number') {
      return html`<span class="value number">${value}</span>`
    }
    if (typeof value === 'string') {
      return html`
        <span class="value string">
          "${value}"
        </span>
      `
    }
    if (Array.isArray(value)) {
      return html`<span class="value">${JSON.stringify(value)}</span>`
    }
    return html`<span class="value">${String(value)}</span>`
  }

  private isBranch(value: unknown): value is Record<string, unknown> | unknown[] {
    return typeof value === 'object' && value !== null && !Array.isArray(value)
  }

  private childEntries(value: Record<string, unknown> | unknown[]): Array<[string, unknown]> {
    return Object.entries(value)
  }

  private childPath(path: string, key: string) {
    return `${path}/${this.escapePointerSegment(key)}`
  }

  private escapePointerSegment(value: string) {
    return value.replaceAll('~', '~0').replaceAll('/', '~1')
  }

  private isChanged(path: string) {
    if (this.changedPaths.has(path)) return true
    for (const changedPath of this.changedPaths) {
      if (changedPath.startsWith(`${path}.`) || changedPath.startsWith(`${path}[`)) {
        return true
      }
    }
    return false
  }

}

function validPosition(value: InspectorPosition | undefined): InspectorPosition | undefined {
  if (!value || !Number.isFinite(value.x) || !Number.isFinite(value.y)) return undefined
  return { x: value.x, y: value.y }
}

function clampPosition(position: InspectorPosition, width: number, height: number): InspectorPosition {
  const maxX = Math.max(VIEWPORT_MARGIN, window.innerWidth - width - VIEWPORT_MARGIN)
  const maxY = Math.max(VIEWPORT_MARGIN, window.innerHeight - height - VIEWPORT_MARGIN)
  return {
    x: Math.round(Math.min(maxX, Math.max(VIEWPORT_MARGIN, position.x))),
    y: Math.round(Math.min(maxY, Math.max(VIEWPORT_MARGIN, position.y))),
  }
}

function positionStyle(position: InspectorPosition | undefined): string {
  return position
    ? `left: ${position.x}px; top: ${position.y}px; right: auto; bottom: auto;`
    : ''
}

declare global {
  interface HTMLElementTagNameMap {
    'datastar-inspector': DatastarInspector
  }
}
