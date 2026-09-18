import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { Archive, ArchiveRestore, MoreHorizontal, Pencil, Pin, Search, Trash2 } from 'lucide'
import type { ChatConversationSummary, ChatManagementSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { lucideIcon } from '../shared/lucide-icons'
import type { ChatAction } from '../chat/chat-manager'

const emptyManagement: ChatManagementSignal = { action: '', conversationId: '', archivedConversations: [] }

class LeapViewArchivedChats extends DatastarLit(LitElement) {
  @state() private query = ''
  @state() private pendingRequestID = ''
  @state() private openMenuID = ''

  static styles = css`
    :host { display: block; color: var(--lv-fg-default); font: var(--lv-type-body); }
    .surface { display: grid; gap: var(--base-size-16); }
    .history-actions { display: grid; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); }
    .history-action { display: grid; min-height: var(--base-size-64); grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-16); padding: var(--base-size-12) var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .history-action:last-child { border-bottom: 0; }
    .history-copy { display: grid; min-width: 0; gap: var(--base-size-2); }
    .history-copy strong { font-weight: var(--base-text-weight-semibold); }
    .history-copy span { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .toolbar { position: relative; display: flex; align-items: center; }
    .toolbar svg { position: absolute; left: 10px; color: var(--lv-fg-muted); pointer-events: none; }
    input { width: 100%; min-height: var(--control-large-size); box-sizing: border-box; border: var(--lv-border-default); border-radius: var(--lv-radius-default); padding: 0 12px 0 36px; color: var(--lv-fg-default); background: var(--lv-bg-panel); font: inherit; }
    input:focus-visible, button:focus-visible, summary:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .list { display: grid; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); overflow: visible; }
    .row { position: relative; display: flex; min-width: 0; align-items: center; gap: var(--base-size-12); min-height: var(--base-size-48); padding: 8px 12px; border-bottom: var(--lv-border-muted); }
    .row:last-child { border-bottom: 0; }
    .title { min-width: 0; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .date { color: var(--lv-fg-muted); font: var(--lv-type-caption); white-space: nowrap; }
    .menu { position: relative; flex: 0 0 auto; }
    .menu > summary { display: inline-flex; width: 30px; height: 30px; align-items: center; justify-content: center; border-radius: var(--lv-radius-default); color: var(--lv-fg-muted); cursor: pointer; list-style: none; }
    .menu > summary::-webkit-details-marker { display: none; }
    .menu > summary:hover { color: var(--lv-fg-default); background: var(--lv-bg-panel-muted); }
    .panel { position: absolute; z-index: 4; top: calc(100% + 4px); right: 0; display: grid; min-width: 186px; padding: 4px; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); }
    button { display: inline-flex; min-height: 32px; align-items: center; gap: 8px; border: 0; border-radius: var(--lv-radius-small); padding: 6px 8px; color: var(--lv-fg-default); background: transparent; cursor: pointer; text-align: left; font: var(--lv-type-body-compact); }
    .panel button { display: grid; width: 100%; grid-template-columns: 20px minmax(0, 1fr); }
    button:hover { background: var(--lv-bg-panel-muted); }
    button.danger:hover { color: var(--lv-fg-danger); }
    .history-action > button { border: var(--lv-border-default); background: var(--lv-button-bg-rest); }
    button.unavailable { cursor: not-allowed; color: var(--lv-fg-muted); opacity: .6; }
    .empty, .loading, .error { padding: var(--base-size-24); color: var(--lv-fg-muted); text-align: center; }
    .error { color: var(--lv-fg-danger); }
    @media (max-width: 40rem) {
      .history-action { grid-template-columns: minmax(0, 1fr); }
      .history-action > button { justify-self: start; }
    }
  `

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('keydown', this.onMenuKeydown)
    document.addEventListener('pointerdown', this.closeMenuOnOutsidePointerdown)
    void this.updateComplete.then(() => this.load())
  }

  override disconnectedCallback(): void {
    document.removeEventListener('keydown', this.onMenuKeydown)
    document.removeEventListener('pointerdown', this.closeMenuOnOutsidePointerdown)
    super.disconnectedCallback()
  }

  private get management(): ChatManagementSignal {
    return this.signal('chatManagement', emptyManagement)
  }

  private load(): void {
    if (this.pendingRequestID) return
    this.pendingRequestID = crypto.randomUUID()
    this.dispatchEvent(new CustomEvent('lv-chat-management-load', {
      bubbles: true,
      composed: true,
      detail: { requestId: this.pendingRequestID },
    }))
  }

  private runAction(event: MouseEvent, action: string, conversation: ChatConversationSummary): void {
    event.stopPropagation()
    const menu = (event.currentTarget as HTMLElement).closest('details')
    if (menu instanceof HTMLDetailsElement) menu.open = false
    this.openMenuID = ''
    const detail: ChatAction = {
      action,
      conversationId: conversation.id,
      title: conversation.title,
      href: `/chats/${encodeURIComponent(conversation.id)}`,
    }
    this.dispatchEvent(new CustomEvent('lv-chat-action', { bubbles: true, composed: true, detail }))
  }

  private runBulkAction(action: 'archive_all' | 'delete_all'): void {
    if (action === 'delete_all' && !window.confirm('Permanently delete all chats, including archived chats?')) return
    this.dispatchEvent(new CustomEvent('lv-chat-action', {
      bubbles: true,
      composed: true,
      detail: { action, conversationId: '' },
    }))
  }

  private onMenuToggle(event: Event, conversationID: string): void {
    const menu = event.currentTarget as HTMLDetailsElement
    if (menu.open) this.openMenuID = conversationID
    else if (this.openMenuID === conversationID) this.openMenuID = ''
  }

  private closeMenuOnOutsidePointerdown = (event: PointerEvent): void => {
    if (!this.openMenuID) return
    const menu = this.renderRoot.querySelector<HTMLDetailsElement>('.menu[open]')
    if (menu && !event.composedPath().includes(menu)) {
      menu.open = false
      this.openMenuID = ''
    }
  }

  private onMenuKeydown = (event: KeyboardEvent): void => {
    const menu = this.renderRoot.querySelector<HTMLDetailsElement>('.menu[open]')
    if (!menu) return
    if (event.key === 'Escape') {
      event.preventDefault()
      menu.open = false
      this.openMenuID = ''
      menu.querySelector<HTMLElement>('summary')?.focus()
      return
    }
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) || !event.composedPath().includes(menu)) return
    const items = Array.from(menu.querySelectorAll<HTMLButtonElement>('[role="menuitem"]:not(:disabled)'))
    if (!items.length) return
    event.preventDefault()
    const current = items.indexOf(event.composedPath().find((target): target is HTMLButtonElement => target instanceof HTMLButtonElement) as HTMLButtonElement)
    const index = event.key === 'Home'
      ? 0
      : event.key === 'End'
        ? items.length - 1
        : event.key === 'ArrowDown'
          ? current < 0 ? 0 : (current + 1) % items.length
          : current < 0 ? items.length - 1 : (current - 1 + items.length) % items.length
    items[index].focus()
  }

  render() {
    const management = this.management
    const query = this.query.trim().toLocaleLowerCase()
    const archived = (management.archivedConversations || []).filter((conversation) => !query || (conversation.title || 'Conversation').toLocaleLowerCase().includes(query))
    const loading = Boolean(this.pendingRequestID && management.completedRequestId !== this.pendingRequestID)
    if (loading && !management.archivedConversations?.length) return html`<section class="surface" aria-label="Archived chats"><p class="loading" role="status">Loading archived chats…</p></section>`
    return html`
      <section class="surface" aria-label="Archived chats">
        <div class="history-actions" aria-label="Chat history actions">
          <div class="history-action">
            <div class="history-copy"><strong>Archive all chats</strong><span>Clear your sidebar while keeping every conversation available here.</span></div>
            <button type="button" @click=${() => this.runBulkAction('archive_all')}>${lucideIcon(Archive, { size: 16 })}<span>Archive all</span></button>
          </div>
          <div class="history-action">
            <div class="history-copy"><strong>Delete all chats</strong><span>Permanently delete active and archived conversations.</span></div>
            <button class="danger" type="button" @click=${() => this.runBulkAction('delete_all')}>${lucideIcon(Trash2, { size: 16 })}<span>Delete all</span></button>
          </div>
        </div>
        <label class="toolbar">${lucideIcon(Search, { size: 16 })}<input type="search" aria-label="Search archived chats" placeholder="Search archived chats" .value=${this.query} @input=${(event: InputEvent) => { this.query = (event.target as HTMLInputElement).value }}></label>
        ${management.error ? html`<p class="error" role="alert">${management.error}</p>` : nothing}
        <div class="list">
          ${archived.length ? archived.map((conversation) => this.renderRow(conversation)) : html`<p class="empty">${query ? 'No matching archived chats.' : 'No archived chats yet.'}</p>`}
        </div>
      </section>
    `
  }

  private renderRow(conversation: ChatConversationSummary) {
    const title = conversation.title || 'Conversation'
    return html`
      <div class="row">
        <span class="title" title=${title}>${title}</span>
        <time class="date" datetime=${conversation.updatedAt}>${shortDate(conversation.updatedAt)}</time>
        <details class="menu" ?open=${this.openMenuID === conversation.id} @toggle=${(event: Event) => this.onMenuToggle(event, conversation.id)}>
          <summary aria-label=${`More actions for ${title}`} title="More actions">${lucideIcon(MoreHorizontal, { size: 17 })}</summary>
          <div class="panel" role="menu" aria-label=${`Actions for ${title}`}>
            <button type="button" role="menuitem" @click=${(event: MouseEvent) => this.runAction(event, 'select', conversation)}><span aria-hidden="true"></span><span>Select</span></button>
            <button class="unavailable" type="button" role="menuitem" disabled title="Archived chats cannot be pinned until restored">${lucideIcon(Pin, { size: 16 })}<span>Pin chat</span></button>
            <button class="unavailable" type="button" role="menuitem" disabled title="Restore the chat before renaming">${lucideIcon(Pencil, { size: 16 })}<span>Rename</span></button>
            <button class="unavailable" type="button" role="menuitem" disabled title="Chat projects are not supported yet"><span aria-hidden="true"></span><span>Add to project</span></button>
            <button type="button" role="menuitem" @click=${(event: MouseEvent) => this.runAction(event, 'restore', conversation)}>${lucideIcon(ArchiveRestore, { size: 16 })}<span>Restore chat</span></button>
            <button class="danger" type="button" role="menuitem" @click=${(event: MouseEvent) => this.runAction(event, 'delete', conversation)}>${lucideIcon(Trash2, { size: 16 })}<span>Delete chat</span></button>
          </div>
        </details>
      </div>
    `
  }
}

function shortDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return ''
  return new Intl.DateTimeFormat(undefined, { month: 'short', day: 'numeric' }).format(date)
}

if (!customElements.get('lv-archived-chats')) customElements.define('lv-archived-chats', LeapViewArchivedChats)
