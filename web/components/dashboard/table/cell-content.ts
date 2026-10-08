import { LitElement, css, html, nothing } from 'lit'
import { Image as ImageIcon, ExternalLink } from 'lucide'
import { lucideIcon } from '../../shared/lucide-icons'
import type { TableCellContent, TableRow } from './types'

/** Accept user-authored web URLs without allowing executable URL schemes. */
export function safeCellURL(value: unknown, origin = globalThis.location?.origin ?? 'http://localhost'): string | undefined {
  if (typeof value !== 'string' || !value || /[\u0000-\u0020\u007f-\u009f\\]/.test(value)) return undefined
  const relative = value.startsWith('/') && !value.startsWith('//')
  if (!relative && !/^https?:\/\//i.test(value)) return undefined
  try {
    const url = new URL(value, origin)
    if (url.protocol !== 'http:' && url.protocol !== 'https:') return undefined
    if (relative && url.origin !== new URL(origin).origin) return undefined
    // Keep the normalized origin attached: a path such as /a/..//host/x
    // becomes //host/x, which must never be reparsed as a new authority.
    return url.href
  } catch { return undefined }
}

export function safeImageURL(value: unknown, origin = globalThis.location?.origin ?? 'http://localhost'): string | undefined {
  const safe = safeCellURL(value, origin)
  if (!safe) return undefined
  try {
    const url = new URL(safe, origin)
    return url.protocol === 'https:' || url.origin === new URL(origin).origin ? safe : undefined
  } catch { return undefined }
}

function dimension(value: number | undefined, fallback: number, maximum: number): number {
  return Number.isFinite(value) && Number(value) > 0 ? Math.min(Math.max(1, Number(value)), maximum) : Math.min(fallback, maximum)
}

function fieldLabel(row: TableRow, field: string | undefined, fallback: string): string {
  const value = field ? row[field] : undefined
  return typeof value === 'string' && value.trim() ? value : typeof value === 'number' ? String(value) : fallback
}

/** Owns media lifecycle for a single virtualized cell; no shared preview/fetch. */
export class TableCellContentElement extends LitElement {
  static properties = {
    value: { attribute: false },
    row: { attribute: false },
    content: { attribute: false },
    columnLabel: { attribute: false },
    rowHeight: { attribute: false },
    identity: { attribute: false },
    failed: { state: true },
    previewOpen: { state: true },
  }

  declare value: unknown
  declare row: TableRow
  declare content: TableCellContent
  declare columnLabel: string
  declare rowHeight: number
  declare identity: string
  declare private failed: boolean
  declare private previewOpen: boolean
  private controller?: AbortController
  private observer?: ResizeObserver
  private positionFrame = 0
  private closeTimer = 0
  private pinned = false
  private pointerWasOpen?: boolean
  private previewHovered = false
  private focusSuppressed = false
  private previewGeneration = 0

  constructor() {
    super()
    this.value = undefined
    this.row = {}
    this.content = { kind: 'image', display: 'inline' }
    this.columnLabel = 'Image'
    this.rowHeight = 34
    this.identity = ''
    this.failed = false
    this.previewOpen = false
  }

  static styles = css`
    :host { display: flex; align-items: center; width: 100%; height: 100%; min-width: 0; overflow: hidden; color: inherit; font: inherit; }
    .inline-image { display: block; object-fit: contain; margin: 2px var(--lv-table-cell-padding-inline, 8px); flex: 0 1 auto; max-width: calc(100% - 2 * var(--lv-table-cell-padding-inline, 8px)); }
    .placeholder, .preview-trigger, .cell-link { display: flex; align-items: center; gap: 6px; min-width: 0; padding: 0 var(--lv-table-cell-padding-inline, 8px); box-sizing: border-box; height: 100%; font: inherit; }
    .placeholder { color: var(--lv-fg-muted); font-size: 11px; }
    .placeholder svg, .preview-trigger svg, .cell-link svg { flex: 0 0 14px; width: 14px; height: 14px; }
    .preview-trigger { width: 100%; border: 0; background: transparent; color: var(--lv-fg-link); cursor: pointer; text-align: inherit; }
    .label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .cell-link { max-width: 100%; color: var(--lv-fg-link); text-decoration: underline; text-underline-offset: 2px; }
    .preview-trigger:hover, .cell-link:hover { background: var(--lv-bg-panel-muted); }
    .preview-trigger:focus-visible, .cell-link:focus-visible { outline: 2px solid var(--lv-line-accent); outline-offset: -2px; }
    .preview { position: fixed; inset: auto; margin: 0; box-sizing: border-box; padding: 10px; border: 1px solid var(--lv-line-default); border-radius: 8px; background: var(--lv-chart-surface, white); color: var(--lv-fg-default); box-shadow: 0 8px 24px rgb(0 0 0 / 18%); overflow: auto; }
    .preview:popover-open { display: grid; gap: 8px; }
    .preview img { display: block; width: 100%; object-fit: contain; max-height: 100%; }
    .preview-label { font: inherit; font-size: 12px; overflow-wrap: anywhere; }
  `

  willUpdate(changes: Map<PropertyKey, unknown>): void {
    if (changes.has('value') || changes.has('content') || changes.has('identity')) {
      this.closePreview()
      this.failed = false
      this.focusSuppressed = false
    }
  }

  disconnectedCallback(): void {
    this.closePreview()
    super.disconnectedCallback()
  }

  private get trigger(): HTMLButtonElement | null { return this.renderRoot.querySelector('.preview-trigger') }
  private get preview(): HTMLElement | null { return this.renderRoot.querySelector('.preview') }

  private clearCloseTimer(): void {
    if (this.closeTimer) window.clearTimeout(this.closeTimer)
    this.closeTimer = 0
  }

  private scheduleClose(): void {
    this.clearCloseTimer()
    this.closeTimer = window.setTimeout(() => {
      this.closeTimer = 0
      if (!this.pinned && !this.previewHovered && this.shadowRoot?.activeElement !== this.trigger) this.closePreview()
    }, 140)
  }

  closePreview(): void {
    this.previewGeneration++
    this.clearCloseTimer()
    this.controller?.abort()
    this.controller = undefined
    this.observer?.disconnect()
    this.observer = undefined
    if (this.positionFrame) cancelAnimationFrame(this.positionFrame)
    this.positionFrame = 0
    const preview = this.preview
    if (preview?.matches(':popover-open')) preview.hidePopover()
    this.previewOpen = false
    this.pinned = false
    this.previewHovered = false
    this.pointerWasOpen = undefined
  }

  private positionPreview = (): void => {
    const trigger = this.trigger, preview = this.preview
    if (!trigger?.isConnected || !preview?.isConnected) { this.closePreview(); return }
    const view = this.ownerDocument.defaultView ?? window
    const bounds = trigger.getBoundingClientRect()
    const padding = 8, gap = 6
    const config = this.content.kind === 'image' ? this.content : undefined
    const width = Math.min(dimension(config?.width, 280, 512) + 20, Math.max(1, view.innerWidth - padding * 2))
    const below = Math.max(0, view.innerHeight - bounds.bottom - gap - padding)
    const above = Math.max(0, bounds.top - gap - padding)
    const requestedHeight = dimension(config?.height, 220, 512) + 52
    const openAbove = below < Math.min(requestedHeight, 120) && above > below
    preview.style.width = `${width}px`
    preview.style.maxHeight = `${Math.max(1, openAbove ? above : below)}px`
    preview.style.left = `${Math.max(padding, Math.min(bounds.left, view.innerWidth - width - padding))}px`
    const height = preview.getBoundingClientRect().height
    preview.style.top = `${openAbove ? Math.max(padding, bounds.top - height - gap) : bounds.bottom + gap}px`
  }

  private schedulePosition = (): void => {
    if (this.positionFrame) return
    this.positionFrame = requestAnimationFrame(() => { this.positionFrame = 0; this.positionPreview() })
  }

  private async openPreview(): Promise<void> {
    if (this.failed || this.content.kind !== 'image' || !safeImageURL(this.value, this.ownerDocument.location?.origin)) return
    this.clearCloseTimer()
    if (this.previewOpen) return
    const generation = ++this.previewGeneration
    this.previewOpen = true
    await this.updateComplete
    if (generation !== this.previewGeneration || !this.previewOpen || !this.isConnected) return
    const preview = this.preview
    if (!preview || !this.trigger || typeof preview.showPopover !== 'function') { this.closePreview(); return }
    this.positionPreview()
    preview.showPopover()
    this.positionPreview()
    const controller = new AbortController()
    this.controller = controller
    this.observer = new ResizeObserver(this.schedulePosition)
    this.observer.observe(preview)
    this.observer.observe(this.trigger)
    const view = this.ownerDocument.defaultView ?? window
    view.addEventListener('resize', this.schedulePosition, { signal: controller.signal })
    this.ownerDocument.addEventListener('scroll', () => this.closePreview(), { capture: true, signal: controller.signal })
    // Scroll events do not cross shadow boundaries. Listen to each enclosing
    // shadow root as well as the document while this preview is open.
    let root = this.getRootNode()
    while (root instanceof ShadowRoot) {
      root.addEventListener('scroll', () => this.closePreview(), { capture: true, signal: controller.signal })
      root = root.host.getRootNode()
    }
    this.ownerDocument.addEventListener('keydown', (event: KeyboardEvent) => {
      if (event.key === 'Escape') { this.focusSuppressed = true; this.closePreview() }
    }, { capture: true, signal: controller.signal })
  }

  private imageFailed = (): void => {
    this.failed = true
    this.closePreview()
  }

  private handleToggle = (): void => {
    if (!this.preview?.matches(':popover-open')) this.closePreview()
  }

  private handleClick = (event: MouseEvent): void => {
    event.stopPropagation()
    const wasOpen = this.pointerWasOpen ?? this.previewOpen
    this.pointerWasOpen = undefined
    if (wasOpen) { this.focusSuppressed = true; this.closePreview() }
    else { this.pinned = true; this.focusSuppressed = false; void this.openPreview() }
  }

  render() {
    const href = this.content.kind === 'image'
      ? safeImageURL(this.value, this.ownerDocument.location?.origin) : safeCellURL(this.value, this.ownerDocument.location?.origin)
    if (this.content.kind === 'link') {
      const label = fieldLabel(this.row, this.content.labelField, typeof this.value === 'string' ? this.value : 'Link')
      const newTab = this.content.newTab !== false
      return href ? html`<a class="cell-link" href=${href} target=${newTab ? '_blank' : nothing}
        rel=${newTab ? 'noopener noreferrer' : nothing}
        aria-label=${newTab ? `${label} (opens in a new tab)` : label}
        @click=${(event: MouseEvent) => event.stopPropagation()}
      ><span class="label">${label}</span>${newTab ? lucideIcon(ExternalLink, { size: 14 }) : nothing}</a>`
        : html`<span class="placeholder" aria-label=${`${this.columnLabel}: link unavailable`}>Link unavailable</span>`
    }
    const alt = fieldLabel(this.row, this.content.altField, this.columnLabel)
    if (!href || this.failed) return html`<span class="placeholder" role="img" aria-label=${`${alt}: image unavailable`}>${lucideIcon(ImageIcon, { size: 14 })}<span class="label">Image unavailable</span></span>`
    if (this.content.display === 'inline') {
      const height = dimension(this.content.height, Math.max(1, this.rowHeight - 8), Math.max(1, this.rowHeight - 4))
      const width = dimension(this.content.width, 48, 512)
      return html`<img class="inline-image" src=${href} alt=${alt} width=${width} height=${height}
        loading="lazy" decoding="async" referrerpolicy="no-referrer" @error=${this.imageFailed}
        style=${`width:${width}px;height:${height}px`} />`
    }
    return html`
      <button class="preview-trigger" type="button" aria-label=${`View image: ${alt}`} aria-expanded=${String(this.previewOpen)}
        aria-controls="image-preview" aria-describedby=${this.previewOpen ? 'image-preview' : nothing}
        aria-description="Image preview. Hover, focus, or activate to open. Press Escape to close."
        @pointerdown=${() => { this.pointerWasOpen = this.previewOpen }}
        @pointercancel=${() => { this.pointerWasOpen = undefined }}
        @keydown=${() => { this.pointerWasOpen = undefined }}
        @pointerenter=${(event: PointerEvent) => { this.clearCloseTimer(); if (event.pointerType !== 'touch') void this.openPreview() }}
        @pointerleave=${() => this.scheduleClose()}
        @focus=${() => { if (!this.focusSuppressed) void this.openPreview() }}
        @blur=${() => { this.focusSuppressed = false; this.pinned = false; this.scheduleClose() }}
        @click=${this.handleClick}
      >${lucideIcon(ImageIcon, { size: 14 })}<span class="label">View image</span></button>
      <div id="image-preview" class="preview" popover="auto" role="tooltip" aria-label=${alt}
        @toggle=${this.handleToggle}
        @click=${(event: MouseEvent) => event.stopPropagation()}
        @pointerenter=${() => { this.previewHovered = true; this.clearCloseTimer() }}
        @pointerleave=${() => { this.previewHovered = false; this.scheduleClose() }}
      >${this.previewOpen ? html`<img src=${href} alt=${alt} loading="lazy" decoding="async" referrerpolicy="no-referrer"
          @load=${this.schedulePosition} @error=${this.imageFailed}
          style=${`height:${dimension(this.content.height, 220, 512)}px`} />
        <span class="preview-label">${alt}</span>` : nothing}</div>
    `
  }
}

if (!customElements.get('lv-table-cell-content')) customElements.define('lv-table-cell-content', TableCellContentElement)
