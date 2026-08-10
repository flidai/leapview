import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { X } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import { mountVisualFocus, restoreVisualFocus, visualSourceFromEvent, type VisualFocusMount } from './visual-modal-focus'
import '../shared/record-table'

type VisualActionName = 'focus' | 'show-data' | 'copy-data' | 'export-csv' | 'clear-selection'

type VisualColumn = {
  key: string
  label: string
  align?: 'left' | 'right'
}

type VisualRow = Record<string, unknown>

export type VisualActionDetail = {
  action: VisualActionName
  visualType: 'chart' | 'map' | 'table' | 'visualization'
  visualId: string
  title: string
  columns: VisualColumn[]
  rows: VisualRow[]
  selection: string[]
  chart?: Record<string, unknown>
  table?: Record<string, unknown>
}

type ModalMode = 'focus' | 'show-data'

export class VisualModal extends LitElement {
  @state() private mode: ModalMode | '' = ''
  @state() private detail: VisualActionDetail | null = null
  @state() private notice = ''
  private focusMount: VisualFocusMount<HTMLElement> | null = null
  private focusSource: HTMLElement | null = null
  private restoreFocusTo: HTMLElement | null = null
  private actionEventTarget: Node | null = null

  static styles = css`
    :host {
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .backdrop {
      position: fixed;
      inset: 0;
      z-index: var(--zIndex-modal);
      display: grid;
      place-items: center;
      background: var(--lv-modal-backdrop);
      padding: var(--base-size-28);
    }

    .dialog {
      display: grid;
      width: min(1120px, 100%);
      max-height: min(760px, calc(100vh - 56px));
      min-height: 420px;
      grid-template-rows: auto minmax(0, 1fr);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-panel);
      background: var(--lv-bg-overlay);
      box-shadow: var(--shadow-floating-large);
      overflow: hidden;
    }

    .focus-dialog {
      position: relative;
      width: min(1420px, 100%);
      height: min(920px, calc(100vh - 56px));
      max-height: calc(100vh - 56px);
      min-height: min(520px, calc(100vh - 56px));
      grid-template-rows: minmax(0, 1fr);
      background: var(--lv-chart-surface);
    }

    header {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--lv-space-lg);
      border-bottom: var(--lv-border-default);
      padding: var(--lv-space-md) var(--lv-space-lg);
    }

    .title {
      min-width: 0;
    }

    .eyebrow {
      margin: 0 0 var(--borderRadius-small);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      line-height: 1;
      text-transform: uppercase;
    }

    h2 {
      margin: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-body-large);
      font-weight: var(--base-text-weight-semibold);
      line-height: var(--base-text-lineHeight-tight);
    }

    .actions {
      display: flex;
      flex: 0 0 auto;
      align-items: center;
      gap: var(--lv-space-sm);
    }

    button {
      min-height: var(--lv-control-small);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0 var(--lv-space-md);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    button:hover,
    button:focus-visible {
      background: var(--control-bgColor-hover);
      outline: 0;
    }

    .close {
      width: calc(var(--lv-control-small) + var(--base-size-2));
      padding: 0;
    }

    .focus-close {
      position: absolute;
      top: var(--lv-space-md);
      right: var(--lv-space-md);
      z-index: var(--zIndex-popover, 300);
      display: grid;
      place-items: center;
      background: var(--lv-bg-overlay);
      box-shadow: var(--shadow-floating-small);
    }

    .close svg {
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .body {
      min-height: 0;
      overflow: hidden;
      background: var(--lv-chart-surface);
    }

    .focus-slot {
      display: block;
      min-height: 0;
      width: 100%;
      height: 100%;
      overflow: hidden;
      background: var(--lv-chart-surface);
    }

    .focus-slot > * {
      display: block;
      width: 100%;
      height: 100%;
      min-height: 0;
    }

    ::slotted([slot='focus-visual']) {
      display: block;
      width: 100%;
      height: 100%;
      min-height: 0;
    }

    .focus-chart,
    .focus-table {
      height: 100%;
      min-height: 0;
    }

    .data-shell {
      display: grid;
      height: 100%;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
    }

    .data-summary {
      border-bottom: var(--lv-border-default);
      color: var(--lv-fg-muted);
      padding: var(--lv-space-md) var(--lv-space-lg);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-medium);
    }

    .data-scroll {
      min-height: 0;
      overflow: auto;
    }

    .empty {
      display: grid;
      height: 100%;
      place-items: center;
      color: var(--lv-fg-muted);
      font-weight: var(--base-text-weight-semibold);
    }

    .notice {
      position: fixed;
      right: calc(var(--base-size-16) + var(--base-size-2));
      bottom: calc(var(--base-size-48) + var(--base-size-2));
      z-index: var(--zIndex-popover);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-overlay);
      box-shadow: var(--shadow-floating-small);
      color: var(--lv-fg-default);
      padding: var(--lv-space-md) var(--lv-space-lg);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    this.actionEventTarget = this.getRootNode()
    this.actionEventTarget.addEventListener('lv-visual-action', this.handleVisualAction as EventListener, { capture: true })
    window.addEventListener('keydown', this.handleKeydown)
  }

  disconnectedCallback(): void {
    this.actionEventTarget?.removeEventListener('lv-visual-action', this.handleVisualAction as EventListener, { capture: true })
    this.actionEventTarget = null
    window.removeEventListener('keydown', this.handleKeydown)
    this.restoreFocusedVisual(false)
    super.disconnectedCallback()
  }

  render() {
    return html`
      ${this.detail && this.mode ? this.renderDialog(this.detail, this.mode) : nothing}
      ${this.notice ? html`<div class="notice" role="status">${this.notice}</div>` : nothing}
    `
  }

  private renderDialog(detail: VisualActionDetail, mode: ModalMode) {
    if (mode === 'focus') return this.renderFocusDialog(detail)
    return html`
      <div class="backdrop" @click=${this.closeFromBackdrop}>
        <section class="dialog" role="dialog" aria-modal="true" aria-label=${detail.title}>
          <header>
            <div class="title">
              <p class="eyebrow">Show data · ${detail.visualType}</p>
              <h2>${detail.title}</h2>
            </div>
            <div class="actions">
              <button type="button" @click=${() => this.copy(detail)}>Copy</button>
              <button type="button" @click=${() => this.exportCSV(detail)}>Export CSV</button>
              <button class="close" type="button" aria-label="Close visual modal" @click=${this.close}>${lucideIcon(X)}</button>
            </div>
          </header>
          <div class="body">
            ${this.renderData(detail)}
          </div>
        </section>
      </div>
    `
  }

  private renderFocusDialog(detail: VisualActionDetail) {
    return html`
      <div class="backdrop" @click=${this.closeFromBackdrop}>
        <section class="dialog focus-dialog" role="dialog" aria-modal="true" aria-label=${detail.title}>
          <button class="close focus-close" type="button" aria-label="Close visual modal" @click=${this.close}>${lucideIcon(X)}</button>
          <div class="focus-slot"><slot name="focus-visual"></slot></div>
        </section>
      </div>
    `
  }

  private renderData(detail: VisualActionDetail) {
    const columns = detail.columns ?? []
    const rows = detail.rows ?? []
    if (columns.length === 0 || rows.length === 0) return html`<div class="empty">No visual data</div>`
    return html`
      <div class="data-shell">
        <div class="data-summary">${rows.length.toLocaleString()} row${rows.length === 1 ? '' : 's'} from current visual data</div>
        <div class="data-scroll">
          <lv-record-table
            .table=${{
              columns: columns.map((column) => ({
                id: column.key,
                header: column.label,
                align: column.align,
              })),
              rows,
              empty: 'No visual data',
              minWidth: `${Math.max(columns.length * 160, 520)}px`,
            }}
          ></lv-record-table>
        </div>
      </div>
    `
  }

  private handleVisualAction = (event: CustomEvent<VisualActionDetail>): void => {
    const detail = event.detail
    if (!detail || detail.action === 'clear-selection') return
    if (detail.action === 'copy-data') {
      void this.copy(detail)
      return
    }
    if (detail.action === 'export-csv') {
      this.exportCSV(detail)
      return
    }
    if (detail.action === 'focus') {
      this.openFocus(detail, event)
      return
    }
    if (detail.action === 'show-data') {
      const focusToRestore = this.deepActiveElement()
      this.restoreFocusedVisual(false)
      this.restoreFocusTo = focusToRestore
      this.detail = detail
      this.mode = 'show-data'
      void this.updateComplete.then(() => this.focusInitialControl())
    }
  }

  private handleKeydown = (event: KeyboardEvent): void => {
    if (event.key === 'Escape') {
      this.close()
      return
    }
    if (event.key === 'Tab' && this.mode) this.trapFocus(event)
  }

  private closeFromBackdrop = (event: Event): void => {
    if (event.target === event.currentTarget) this.close()
  }

  private close = (): void => {
    this.restoreFocusedVisual(true)
    this.mode = ''
    this.detail = null
  }

  private openFocus(detail: VisualActionDetail, event: Event): void {
    const source = visualSourceFromEvent(event)
    if (!source) return
    this.openVisualFocus(source, detail)
  }

  openVisualFocus(source: HTMLElement, detail: VisualActionDetail): void {
    if (detail.action !== 'focus') return
    if (this.mode === 'focus' && this.focusMount?.element === source) return

    const focusToRestore = this.deepActiveElement()
    this.restoreFocusedVisual(false)
    this.restoreFocusTo = focusToRestore
    this.detail = detail
    this.mode = 'focus'
    this.focusSource = source
    void this.updateComplete.then(() => {
      this.mountFocusedVisual(source)
      this.focusInitialControl()
    })
  }

  private mountFocusedVisual(source: HTMLElement): void {
    if (this.mode !== 'focus' || this.focusSource !== source || this.focusMount) return
    this.focusMount = mountVisualFocus(source, this, { slot: 'focus-visual' })
  }

  private restoreFocusedVisual(restoreFocus: boolean): void {
    const focusToRestore = this.restoreFocusTo
    if (this.focusMount) restoreVisualFocus(this.focusMount)
    this.focusMount = null
    this.focusSource = null
    this.restoreFocusTo = null
    if (restoreFocus && focusToRestore?.isConnected) {
      queueMicrotask(() => focusToRestore.focus({ preventScroll: true }))
    }
  }

  private focusInitialControl(): void {
    this.renderRoot.querySelector<HTMLButtonElement>('.focus-close, .dialog .close')?.focus({ preventScroll: true })
  }

  private trapFocus(event: KeyboardEvent): void {
    const focusable = this.focusableElements()
    if (focusable.length === 0) {
      event.preventDefault()
      return
    }

    const active = this.deepActiveElement()
    const first = focusable[0]
    const last = focusable[focusable.length - 1]
    const activeInsideModal = Boolean(active && focusable.includes(active))
    if (event.shiftKey && (!activeInsideModal || active === first)) {
      event.preventDefault()
      last.focus({ preventScroll: true })
      return
    }
    if (!event.shiftKey && (!activeInsideModal || active === last)) {
      event.preventDefault()
      first.focus({ preventScroll: true })
    }
  }

  private focusableElements(): HTMLElement[] {
    return [
      ...this.deepFocusableElements(this.renderRoot),
      ...(this.focusMount ? this.deepFocusableElements(this.focusMount.element) : []),
    ]
  }

  private deepFocusableElements(root: ParentNode): HTMLElement[] {
    const selector = [
      'button:not([disabled])',
      'a[href]',
      'input:not([disabled])',
      'select:not([disabled])',
      'textarea:not([disabled])',
      '[tabindex]:not([tabindex="-1"])',
    ].join(',')
    const elements: HTMLElement[] = []
    root.querySelectorAll<HTMLElement>(selector).forEach((element) => {
      elements.push(element)
      if (element.shadowRoot) elements.push(...this.deepFocusableElements(element.shadowRoot))
    })
    root.querySelectorAll<HTMLElement>('*').forEach((element) => {
      if (element.shadowRoot) elements.push(...this.deepFocusableElements(element.shadowRoot))
    })
    return [...new Set(elements)]
  }

  private deepActiveElement(): HTMLElement | null {
    let active = document.activeElement
    while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement
    return active instanceof HTMLElement ? active : null
  }

  private async copy(detail: VisualActionDetail): Promise<void> {
    const text = toDelimited(detail, '\t')
    try {
      await navigator.clipboard.writeText(text)
      this.flash('Copied visual data')
    } catch {
      this.fallbackCopy(text)
      this.flash('Copied visual data')
    }
  }

  private fallbackCopy(text: string): void {
    const area = document.createElement('textarea')
    area.value = text
    area.setAttribute('readonly', '')
    area.style.position = 'fixed'
    area.style.opacity = '0'
    document.body.append(area)
    area.select()
    document.execCommand('copy')
    area.remove()
  }

  private exportCSV(detail: VisualActionDetail): void {
    const blob = new Blob([toDelimited(detail, ',')], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${slug(detail.title || detail.visualId || 'visual')}.csv`
    document.body.append(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
    this.flash('Downloaded CSV')
  }

  private flash(message: string): void {
    this.notice = message
    window.setTimeout(() => {
      if (this.notice === message) this.notice = ''
    }, 1800)
  }
}

function toDelimited(detail: VisualActionDetail, delimiter: ',' | '\t'): string {
  const columns = detail.columns ?? []
  const rows = detail.rows ?? []
  return [
    columns.map((column) => escapeCell(column.label, delimiter)).join(delimiter),
    ...rows.map((row) => columns.map((column) => escapeCell(row[column.key], delimiter)).join(delimiter)),
  ].join('\n')
}

function escapeCell(value: unknown, delimiter: ',' | '\t'): string {
  const text = stringValue(value)
  if (delimiter === '\t') return text.replace(/\t/g, ' ').replace(/\r?\n/g, ' ')
  if (!/[",\r\n]/.test(text)) return text
  return `"${text.replace(/"/g, '""')}"`
}

function stringValue(value: unknown): string {
  if (value === null || value === undefined) return ''
  return String(value)
}

function slug(value: string): string {
  const normalized = value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
  return normalized || 'visual'
}

customElements.define('lv-visual-modal', VisualModal)

declare global { interface HTMLElementTagNameMap { 'lv-visual-modal': VisualModal } }
