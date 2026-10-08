import { LitElement, css, html, nothing, render } from 'lit'
import { state } from 'lit/decorators.js'
import { X } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import { mountVisualFocus, restoreVisualFocus, visualSourceFromEvent, type VisualFocusMount } from './visual-modal-focus'
import { visualDataActionNotice, visualDataSummary, visualDataToDelimited } from './visual-modal-actions'
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
  totalRows?: number
  truncated?: boolean
  dataStatus?: string
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
  private focusClose: HTMLButtonElement | null = null
  private restoreFocusTo: HTMLElement | null = null
  private actionEventTarget: Node | null = null
  private noticeTimer: number | undefined

  static styles = css`
    :host {
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .dialog {
      box-sizing: border-box;
      position: fixed;
      inset: 0;
      margin: auto;
      padding: 0;
      width: min(1120px, calc(100% - 56px));
      max-height: min(760px, calc(100dvh - 56px));
      min-height: min(420px, calc(100dvh - 56px));
      grid-template-rows: auto minmax(0, 1fr);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-panel);
      background: var(--lv-bg-overlay);
      color: inherit;
      box-shadow: var(--shadow-floating-large);
      overflow: hidden;
    }

    .dialog[open] { display: grid; }
    .dialog::backdrop { background: var(--lv-modal-backdrop); }

    .data-dialog.is-single,
    .data-dialog.is-compact {
      width: min(30rem, calc(100% - 56px));
    }

    .data-dialog.is-medium {
      width: min(54rem, calc(100% - 56px));
    }

    .data-dialog.is-compact lv-record-table .record-table {
      margin-inline: 0;
      table-layout: auto;
    }

    .focus-dialog {
      width: min(1420px, calc(100% - 56px));
      height: min(920px, calc(100dvh - 56px));
      max-height: calc(100dvh - 56px);
      min-height: min(520px, calc(100dvh - 56px));
      grid-template-rows: minmax(0, 1fr);
      background: var(--lv-chart-surface);
    }

    .focus-dialog.focus-table-dialog {
      height: auto;
      min-height: 0;
    }

    :host([tabular-focus]) .focus-slot,
    :host([tabular-focus]) ::slotted([slot='focus-visual']) {
      height: auto;
      --lv-visual-height: auto;
      --lv-table-max-body-height: max(80px, calc(100dvh - 180px));
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
      line-height: 1;
      text-transform: uppercase;
    }

    h2 {
      margin: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
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
      font-weight: var(--base-text-weight-medium);
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    this.actionEventTarget = this.getRootNode()
    this.actionEventTarget.addEventListener('lv-visual-action', this.handleVisualAction as EventListener, { capture: true })
  }

  disconnectedCallback(): void {
    this.actionEventTarget?.removeEventListener('lv-visual-action', this.handleVisualAction as EventListener, { capture: true })
    this.actionEventTarget = null
    window.clearTimeout(this.noticeTimer)
    this.noticeTimer = undefined
    this.renderRoot.querySelector<HTMLDialogElement>('dialog')?.close()
    this.restoreFocusedVisual(false)
    super.disconnectedCallback()
  }

  render() {
    return html`
      ${this.detail && this.mode ? this.renderDialog(this.detail, this.mode) : nothing}
      ${!this.mode ? this.renderNotice() : nothing}
    `
  }

  private renderDialog(detail: VisualActionDetail, mode: ModalMode) {
    if (mode === 'focus') return this.renderFocusDialog(detail)
    return html`
      <dialog class=${`dialog data-dialog ${this.dataDialogSize(detail.columns.length)}`} role="dialog" aria-modal="true" aria-label=${detail.title} @cancel=${this.cancel} @click=${this.closeFromBackdrop}>
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
        ${this.renderNotice()}
      </dialog>
    `
  }

  private renderFocusDialog(detail: VisualActionDetail) {
    const isTable = detail.visualType === 'table'
    const availableRows = detail.table?.availableRows
    const rowHeight = detail.table?.rowHeight
    const rows = typeof availableRows === 'number' && Number.isFinite(availableRows) ? Math.max(0, availableRows) : detail.rows.length
    const height = Math.min(920, Math.max(360, 150 + rows * (typeof rowHeight === 'number' && rowHeight > 0 ? rowHeight : 34)))
    return html`
      <dialog class=${`dialog focus-dialog${isTable ? ' focus-table-dialog' : ''}`} style=${isTable ? `height:min(${height}px, calc(100dvh - 56px))` : ''} role="dialog" aria-modal="true" aria-label=${detail.title} @cancel=${this.cancel} @click=${this.closeFromBackdrop}>
          <div class="focus-slot"><slot name="focus-visual"></slot></div>
        ${this.renderNotice()}
      </dialog>
    `
  }

  private renderNotice() {
    return this.notice ? html`<div class="notice" role="status">${this.notice}</div>` : nothing
  }

  private renderData(detail: VisualActionDetail) {
    const columns = detail.columns ?? []
    const rows = detail.rows ?? []
    const compactColumns = columns.length === 2
    if (columns.length === 0 || rows.length === 0) return html`
      <div class="data-shell">
        <div class="data-summary" role="status">${visualDataSummary(detail)}</div>
        <div class="empty">No visual data</div>
      </div>
    `
    return html`
      <div class="data-shell">
        <div class="data-summary" role="status">${visualDataSummary(detail)}</div>
        <div class="data-scroll">
          <lv-record-table
            variant="data"
            .table=${{
              columns: columns.map((column) => ({
                id: column.key,
                header: column.label,
                align: column.align,
              })),
              rows,
              empty: 'No visual data',
              width: compactColumns ? '100%' : '',
              minWidth: compactColumns ? '100%' : columns.length > 4 ? `${columns.length * 160}px` : '0',
              density: 'tight',
            }}
          ></lv-record-table>
        </div>
      </div>
    `
  }

  private dataDialogSize(columnCount: number): string {
    if (columnCount <= 1) return 'is-single'
    if (columnCount <= 2) return 'is-compact'
    if (columnCount <= 4) return 'is-medium'
    return 'is-wide'
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
      const focusToRestore = this.restoreFocusTo ?? this.deepActiveElement()
      this.restoreFocusedVisual(false)
      this.restoreFocusTo = focusToRestore
      this.detail = detail
      this.mode = 'show-data'
      void this.updateComplete.then(() => {
        if (!this.isConnected || this.mode !== 'show-data') return
        this.showDialog()
        this.focusInitialControl()
      })
    }
  }

  private cancel = (event: Event): void => {
    event.preventDefault()
    this.close()
  }

  private closeFromBackdrop = (event: MouseEvent): void => {
    const dialog = event.currentTarget as HTMLDialogElement
    if (event.target !== dialog) return
    const bounds = dialog.getBoundingClientRect()
    if (event.clientX < bounds.left || event.clientX > bounds.right || event.clientY < bounds.top || event.clientY > bounds.bottom) this.close()
  }

  private showDialog(): void {
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('dialog')
    if (dialog && !dialog.open) dialog.showModal()
  }

  private close = (): void => {
    this.renderRoot.querySelector<HTMLDialogElement>('dialog')?.close()
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
    this.toggleAttribute('tabular-focus', detail.visualType === 'table')
    this.detail = detail
    this.mode = 'focus'
    this.focusSource = source
    void this.updateComplete.then(async () => {
      if (!this.isConnected || this.mode !== 'focus' || this.focusSource !== source) return
      this.mountFocusedVisual(source)
      if (source instanceof LitElement) await source.updateComplete
      if (!this.isConnected || this.mode !== 'focus' || this.focusSource !== source) return
      this.showDialog()
      this.focusInitialControl()
      // Table mounting replaces the temporary action slot. Restore initial
      // focus after that move, unless the user has already begun interacting.
      const dialog = this.renderRoot.querySelector<HTMLDialogElement>('dialog')!
      const listeners = new AbortController()
      let interacted = false
      const markInteraction = () => { interacted = true }
      dialog.addEventListener('pointerdown', markInteraction, { capture: true, signal: listeners.signal })
      dialog.addEventListener('keydown', markInteraction, { capture: true, signal: listeners.signal })
      try {
        await (source as HTMLElement & { ensureMounted?: () => Promise<void> }).ensureMounted?.()
      } catch {
        // The host renders its own error; its close action remains available.
      } finally {
        listeners.abort()
      }
      if (!interacted && this.isConnected && this.mode === 'focus' && this.focusSource === source && this.renderRoot.querySelector('dialog') === dialog) this.focusInitialControl()
    })
  }

  private mountFocusedVisual(source: HTMLElement): void {
    if (this.mode !== 'focus' || this.focusSource !== source || this.focusMount) return
    this.focusMount = mountVisualFocus(source, this, { slot: 'focus-visual' })
    if (!this.focusMount) return
    const close = document.createElement('button')
    close.type = 'button'
    close.slot = 'focus-action'
    close.className = 'focus-close'
    close.setAttribute('aria-label', 'Close visual modal')
    close.title = 'Close visual modal'
    close.addEventListener('click', this.close)
    render(lucideIcon(X, { size: 16 }), close)
    source.append(close)
    this.focusClose = close
  }

  private restoreFocusedVisual(restoreFocus: boolean): void {
    const focusToRestore = this.restoreFocusTo
    this.focusClose?.remove()
    this.focusClose = null
    if (this.focusMount) restoreVisualFocus(this.focusMount)
    this.focusMount = null
    this.removeAttribute('tabular-focus')
    this.focusSource = null
    this.restoreFocusTo = null
    if (restoreFocus && focusToRestore?.isConnected) {
      queueMicrotask(() => focusToRestore.focus({ preventScroll: true }))
    }
  }

  private focusInitialControl(): void {
    const close = this.focusClose ?? this.renderRoot.querySelector<HTMLButtonElement>('.dialog .close')
    close?.focus({ preventScroll: true })
  }

  private deepActiveElement(): HTMLElement | null {
    let active = document.activeElement
    while (active?.shadowRoot?.activeElement) active = active.shadowRoot.activeElement
    return active instanceof HTMLElement ? active : null
  }

  private async copy(detail: VisualActionDetail): Promise<void> {
    const text = visualDataToDelimited(detail, '\t')
    try {
      await navigator.clipboard.writeText(text)
      this.flash(visualDataActionNotice(detail, 'copy-data'))
    } catch {
      this.fallbackCopy(text)
      this.flash(visualDataActionNotice(detail, 'copy-data'))
    }
  }

  private fallbackCopy(text: string): void {
    const area = document.createElement('textarea')
    area.value = text
    area.setAttribute('readonly', '')
    area.style.position = 'fixed'
    area.style.opacity = '0'
    const target = this.renderRoot.querySelector('dialog[open]') ?? document.body
    target.append(area)
    area.select()
    document.execCommand('copy')
    area.remove()
  }

  private exportCSV(detail: VisualActionDetail): void {
    const blob = new Blob([visualDataToDelimited(detail, ',')], { type: 'text/csv;charset=utf-8' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${slug(detail.title || detail.visualId || 'visual')}.csv`
    document.body.append(link)
    link.click()
    link.remove()
    URL.revokeObjectURL(url)
    this.flash(visualDataActionNotice(detail, 'export-csv'))
  }

  private flash(message: string): void {
    window.clearTimeout(this.noticeTimer)
    this.notice = message
    this.noticeTimer = window.setTimeout(() => {
      this.notice = ''
      this.noticeTimer = undefined
    }, 1800)
  }
}

function slug(value: string): string {
  const normalized = value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
  return normalized || 'visual'
}

customElements.define('lv-visual-modal', VisualModal)

declare global { interface HTMLElementTagNameMap { 'lv-visual-modal': VisualModal } }
