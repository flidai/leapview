import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { Archive, ArchiveRestore, Search, Trash2, X } from 'lucide'
import { DatastarLit } from '../shared/datastar-lit'
import { lucideIcon } from '../shared/lucide-icons'
import { browserCommandFailure, ownsBrowserCommandFetch } from '../shared/command-failure'
import type { ChatManagementSignal } from '../../generated/signals'

export type ChatAction = { action: string; conversationId: string; title?: string }

export const CHAT_UNDO_WINDOW_MS = 10_000
const pendingUndoStorageKey = 'lv-chat-manager.pending-undo'

type PendingUndo = {
  action: ChatAction
  requestId: string
  deadline: number
}

type StoredUndo = PendingUndo & { phase: 'waiting' }

function focusedElement(): HTMLElement | null {
  let element = document.activeElement
  while (element?.shadowRoot?.activeElement) element = element.shadowRoot.activeElement
  return element instanceof HTMLElement ? element : null
}

const emptyManagement: ChatManagementSignal = { action: '', conversationId: '', archivedConversations: [] }

function isUndoableAction(detail: ChatAction | null | undefined): detail is ChatAction {
  return Boolean(detail && (detail.action === 'archive' || detail.action === 'delete') && detail.conversationId)
}

function readStoredUndos(): StoredUndo[] {
  try {
    const raw = window.sessionStorage.getItem(pendingUndoStorageKey)
    if (!raw) return []
    const parsed = JSON.parse(raw) as Partial<StoredUndo> | Partial<StoredUndo>[]
    const values = Array.isArray(parsed) ? parsed : [parsed]
    const stored = values.filter((value): value is StoredUndo => Boolean(
      value && value.phase === 'waiting' && typeof value.requestId === 'string' && value.requestId &&
      typeof value.deadline === 'number' && Number.isFinite(value.deadline) && value.action && isUndoableAction(value.action),
    ))
    if (stored.length !== values.length) {
      window.sessionStorage.removeItem(pendingUndoStorageKey)
      return []
    }
    return stored
  } catch {
    return []
  }
}

function writeStoredUndos(values: StoredUndo[]): void {
  try {
    if (values.length) window.sessionStorage.setItem(pendingUndoStorageKey, JSON.stringify(values))
    else window.sessionStorage.removeItem(pendingUndoStorageKey)
  } catch {
    // The in-memory timer still provides Undo when storage is unavailable.
  }
}

function clearStoredUndo(requestId?: string): void {
  try {
    if (!requestId) {
      window.sessionStorage.removeItem(pendingUndoStorageKey)
      return
    }
    writeStoredUndos(readStoredUndos().filter(pending => pending.requestId !== requestId))
  } catch {
    // Storage can be unavailable in privacy-restricted browsing contexts.
  }
}

export class ChatManager extends DatastarLit(LitElement) {
  @state() private opened = false
  @state() private confirmation: ChatAction | null = null
  @state() private pending = ''
  @state() private query = ''
  @state() private feedback = ''
  @state() private failure = ''
  @state() private undoPendings: PendingUndo[] = []
  private pendingAction: ChatAction | null = null
  private archivesOpen = false
  private lastFocus: HTMLElement | null = null
  private undoTimers = new Map<string, number>()

  static styles = css`
    :host { display: contents; color: var(--lv-fg-default); font: var(--lv-type-body); }
    dialog { width: min(580px, calc(100vw - 32px)); max-height: min(680px, calc(100svh - 48px)); box-sizing: border-box; padding: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); color: inherit; box-shadow: var(--lv-shadow-floating-lg); }
    dialog::backdrop { background: var(--lv-modal-backdrop); }
    header { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 20px 24px 16px; }
    h2 { margin: 0; font: var(--lv-type-section-title); }
    p { margin: 0; line-height: 1.6; }
    .body { padding: 0 24px 24px; display: grid; gap: 16px; }
    button { display: inline-flex; align-items: center; justify-content: center; gap: 8px; padding: 8px 13px; font: inherit; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: inherit; cursor: pointer; }
    button:hover { background: var(--lv-bg-panel-muted); }
    button:focus-visible, input:focus-visible { outline: 2px solid var(--lv-fg-accent); outline-offset: 2px; }
    button:disabled { opacity: .5; cursor: wait; }
    .icon { padding: 7px; border: 0; flex-shrink: 0; }
    .danger { color: var(--lv-fg-danger); }
    .confirm-delete { background: var(--lv-fg-danger); color: var(--lv-fg-on-emphasis); border-color: transparent; }
    .confirm-delete:hover { filter: brightness(.92); }
    .actions { display: flex; justify-content: flex-end; gap: 8px; margin-top: 4px; }
    .muted { color: var(--lv-fg-muted); }
    .search { display: flex; align-items: center; gap: 8px; padding: 8px 10px; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); color: var(--lv-fg-muted); }
    input { min-width: 0; flex: 1; width: 100%; border: 0; background: transparent; color: var(--lv-fg-default); font: inherit; outline: none; }
    .list { display: grid; overflow: auto; max-height: 390px; }
    .chat { display: flex; align-items: center; gap: 8px; padding: 12px 0; border-bottom: var(--lv-border-muted); }
    .chat:last-child { border-bottom: 0; }
    .chat-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .empty { padding: 26px 0; text-align: center; color: var(--lv-fg-muted); }
    .error { color: var(--lv-fg-danger); line-height: 1.5; }
    .toast-stack { position: fixed; z-index: 100; top: max(16px, env(safe-area-inset-top)); left: 50%; transform: translateX(-50%); display: grid; justify-items: center; gap: 8px; width: max-content; max-width: calc(100vw - 32px); }
    .toast { display: flex; align-items: center; gap: 10px; box-sizing: border-box; width: max-content; max-width: 100%; padding: 8px 12px; border: 0; border-radius: var(--lv-radius-large); background: var(--lv-bg-panel-muted); box-shadow: none; font: var(--lv-type-caption); }
    .toast-label { font-weight: var(--base-text-weight-semibold); }
    .toast svg { flex-shrink: 0; }
    .toast button { border: 0; padding: 5px 10px; font: inherit; }
    .toast .undo { background: var(--lv-fg-default); color: var(--lv-bg-panel); }
    @media (max-width: 500px) { header { padding: 16px; } .body { padding: 0 16px 16px; } }
  `

  override connectedCallback() {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleFetch)
    void this.updateComplete.then(() => {
      if (this.isConnected) this.restorePendingUndo()
    })
  }

  override disconnectedCallback() {
    document.removeEventListener('datastar-fetch', this.handleFetch)
    this.clearUndoTimer()
    super.disconnectedCallback()
  }

  private handleFetch = (event: Event) => {
    if (!this.pending || !ownsBrowserCommandFetch(this, event)) return
    const failure = browserCommandFailure(event, 'Chat management')
    if (!failure) return
    const action = this.pendingAction
    const requestId = this.pending
    this.pending = ''
    this.pendingAction = null
    if (isUndoableAction(action)) {
      clearStoredUndo(requestId)
      this.emitRemovalPending()
    }
    this.failure = failure.message
    if (action?.action === 'undo') {
      const undoPending = this.undoPendings.find(pending => pending.requestId === requestId)
      if (undoPending) this.scheduleUndo(undoPending)
    }
  }

  private get management(): ChatManagementSignal { return this.signal('chatManagement', emptyManagement) }

  public openArchives() {
    if (this.pending) return
    this.archivesOpen = true
    this.confirmation = null
    this.query = ''
    this.open()
    this.pending = crypto.randomUUID()
    this.failure = ''
    this.dispatchEvent(new CustomEvent('lv-chat-management-load', { bubbles: true, composed: true, detail: { requestId: this.pending } }))
  }

  public requestAction(detail: ChatAction) {
    if (this.pending) return
    this.failure = ''
    this.feedback = ''
    if (detail.action === 'delete' || detail.action.endsWith('_all')) {
      this.confirmation = detail
      this.open()
    } else if (isUndoableAction(detail)) {
      this.lastFocus = focusedElement()
      this.beginUndo(detail)
    } else {
      this.lastFocus = focusedElement()
      this.perform(detail)
    }
  }

  private open() {
    this.lastFocus = focusedElement()
    this.opened = true
  }

  private perform(detail: ChatAction, requestId: string = crypto.randomUUID()) {
    this.pendingAction = detail
    this.pending = requestId
    this.failure = ''

    this.dispatchEvent(new CustomEvent('lv-chat-management', { bubbles: true, composed: true, detail: { ...detail, requestId: this.pending } }))
  }

  private restorePendingUndo() {
    if (this.undoPendings.length) {
      this.emitRemovalPending()
      this.undoPendings.forEach(pending => this.scheduleUndo(pending))
      return
    }
    const stored = readStoredUndos()
    if (this.pending || !stored.length) return
    this.undoPendings = stored
    this.emitRemovalPending()
    stored.forEach(pending => this.scheduleUndo(pending))
  }

  private beginUndo(detail: ChatAction) {
    this.close()
    // The server owns the operation before the notification promises Undo.
    this.perform({ ...detail, action: `${detail.action}_pending` })
  }

  private scheduleUndo(pending: PendingUndo) {
    this.clearUndoTimer(pending.requestId)
    const delay = Math.max(0, pending.deadline - Date.now())
    this.undoTimers.set(pending.requestId, window.setTimeout(() => this.commitUndo(pending.requestId), delay))
    if (delay === 0) this.requestUpdate()
  }

  private clearUndoTimer(requestId?: string) {
    if (requestId) {
      const timer = this.undoTimers.get(requestId)
      if (timer !== undefined) window.clearTimeout(timer)
      this.undoTimers.delete(requestId)
      return
    }
    this.undoTimers.forEach(timer => window.clearTimeout(timer))
    this.undoTimers.clear()
  }

  private commitUndo(requestId: string) {
    const pending = this.undoPendings.find(value => value.requestId === requestId)
    if (!pending) return
    this.clearUndoTimer(requestId)
    this.undoPendings = this.undoPendings.filter(value => value.requestId !== requestId)
    clearStoredUndo(requestId)
    this.emitRemovalPending()
    // Expiry only refreshes the view. Closing this tab cannot cancel the
    // persisted operation, and no second mutation is needed from the browser.
    const current = window.location.pathname.match(/^\/chats\/([^/]+)$/)?.[1]
    if (current && decodeURIComponent(current) === pending.action.conversationId) {
      window.location.assign('/chats/new')
    }
  }

  private undo = (requestId: string) => {
    const pending = this.undoPendings.find(value => value.requestId === requestId)
    if (!pending || this.pending) return
    this.clearUndoTimer(requestId)
    this.perform({ action: 'undo', conversationId: pending.action.conversationId }, pending.requestId)
  }

  private emitRemovalPending() {
    const conversationIds = this.undoPendings.map(pending => pending.action.conversationId)
    this.dispatchEvent(new CustomEvent('lv-chat-removal-pending', { bubbles: true, composed: true, detail: { conversationIds } }))
  }

  protected updated() {
    const dialog = this.renderRoot.querySelector<HTMLDialogElement>('dialog')
    if (this.opened && dialog && !dialog.open) dialog.showModal()
    if (!this.opened && dialog?.open) dialog.close()
    const result = this.management
    if (!this.pending || result.completedRequestId !== this.pending) return
    if (this.pendingAction && !result.error && result.action !== this.pendingAction.action) return
    const completedRequestId = this.pending
    this.pending = ''
    this.failure = result.error || ''
    const action = this.pendingAction
    this.pendingAction = null
    if (isUndoableAction(action)) {
      clearStoredUndo(completedRequestId)
      this.emitRemovalPending()
    }
    if (this.failure) {
      if (action?.action === 'undo') {
        const undoPending = this.undoPendings.find(pending => pending.requestId === completedRequestId)
        if (undoPending) this.scheduleUndo(undoPending)
      }
      return
    }
    if (action?.action === 'archive_pending' || action?.action === 'delete_pending') {
      const deadline = Date.parse(result.undoDeadline || '')
      if (!Number.isFinite(deadline)) {
        this.failure = 'The action was saved, but its Undo deadline could not be loaded.'
        return
      }
      const originalAction = action.action === 'archive_pending' ? 'archive' : 'delete'
      const pendingUndo = { action: { ...action, action: originalAction }, requestId: completedRequestId, deadline }
      this.undoPendings = [...this.undoPendings.filter(pending => pending.requestId !== completedRequestId), pendingUndo]
      writeStoredUndos(this.undoPendings.map(pending => ({ ...pending, phase: 'waiting' })))
      this.emitRemovalPending()
      this.scheduleUndo(pendingUndo)
      this.feedback = ''
      return
    }
    if (action?.action === 'undo') {
      this.undoPendings = this.undoPendings.filter(pending => pending.requestId !== completedRequestId)
      clearStoredUndo(completedRequestId)
      this.emitRemovalPending()
    }
    if (!action) this.emitRemovalPending()
    this.feedback = result.message || ''
    this.confirmation = null
    if (!this.archivesOpen) this.close()
    if (action && ['archive', 'delete', 'archive_all', 'delete_all'].includes(action.action)) {
      const current = window.location.pathname.match(/^\/chats\/([^/]+)$/)?.[1]
      if (current && current !== 'new' && (action.action.endsWith('_all') || decodeURIComponent(current) === action.conversationId)) window.location.assign('/chats/new')
    }
  }

  private close() {
    this.opened = false
    this.confirmation = null
    this.archivesOpen = false
    if (this.lastFocus?.isConnected) this.lastFocus.focus()
    else {
      const shell = this.getRootNode() as ShadowRoot
      shell.querySelector('lv-sidebar')?.shadowRoot?.querySelector<HTMLElement>('a[href="/chats/new"]')?.focus()
    }
  }

  private cancel = (event: Event) => {
    event.preventDefault()
    if (this.pending) return
    if (this.confirmation && this.archivesOpen) this.confirmation = null
    else this.close()
  }

  render() {
    const action = this.confirmation?.action
    const deleting = action === 'delete' || action === 'delete_all'
    const title = this.confirmation ? action === 'delete_all' ? 'Delete all chats?' : action === 'archive_all' ? 'Archive all chats?' : 'Delete chat?' : 'Archived chats'
    const archived = this.management.archivedConversations || []
    const matches = archived.filter(chat => chat.title.toLowerCase().includes(this.query.toLowerCase()))
    return html`
      <dialog aria-labelledby="chat-management-title" @cancel=${this.cancel} @click=${(event: MouseEvent) => { if (event.target === event.currentTarget) this.cancel(event) }}>
        <header><h2 id="chat-management-title">${title}</h2><button class="icon" aria-label="Close" ?disabled=${Boolean(this.pending)} @click=${this.cancel}>${lucideIcon(X, { size: 18 })}</button></header>
        <div class="body">
          ${this.failure ? html`<p class="error" role="alert">${this.failure}</p>${!this.confirmation ? html`<button @click=${() => this.openArchives()}>Retry</button>` : nothing}` : nothing}
          ${this.confirmation ? html`
            <p>${action === 'delete_all' ? 'This permanently deletes all of your chats, including archived chats. This cannot be undone.' : action === 'archive_all' ? 'All of your chats will move out of the sidebar. You can restore them here in Settings.' : html`Delete <strong>${this.confirmation.title || 'this chat'}</strong>? You can undo this from the notification before the chat is permanently deleted.`}</p>
            <div class="actions"><button ?disabled=${Boolean(this.pending)} @click=${this.cancel}>Cancel</button><button class=${deleting ? 'confirm-delete' : ''} ?disabled=${Boolean(this.pending)} @click=${() => this.confirmation && (isUndoableAction(this.confirmation) ? this.beginUndo(this.confirmation) : this.perform(this.confirmation))}>${this.pending ? 'Saving…' : deleting ? 'Delete' : 'Archive all'}</button></div>
          ` : html`
            <p class="muted">Archived chats are hidden from your sidebar. Restore a chat to continue the conversation.</p>
            <label class="search">${lucideIcon(Search, { size: 16 })}<input aria-label="Search archived chats" placeholder="Search archived chats" .value=${this.query} @input=${(event: InputEvent) => { this.query = (event.target as HTMLInputElement).value }}></label>
            ${this.pending ? html`<p class="muted" role="status">Loading…</p>` : html`<div class="list">${matches.length ? matches.map(chat => html`<div class="chat"><span class="chat-name" title=${chat.title}>${chat.title || 'Untitled chat'}</span><button class="icon" aria-label=${`Restore ${chat.title}`} title="Restore chat" @click=${() => this.requestAction({ action: 'restore', conversationId: chat.id, title: chat.title })}>${lucideIcon(ArchiveRestore, { size: 17 })}</button><button class="icon danger" aria-label=${`Delete ${chat.title}`} title="Delete chat" @click=${() => this.requestAction({ action: 'delete', conversationId: chat.id, title: chat.title })}>${lucideIcon(Trash2, { size: 17 })}</button></div>`) : html`<p class="empty">${this.query ? 'No matching archived chats.' : 'No archived chats yet.'}</p>`}</div>`}
          `}
        </div>
      </dialog>
      ${!this.opened && (this.undoPendings.length || this.pending || this.failure || this.feedback) ? html`
        <div class="toast-stack">
          ${this.undoPendings.map(pending => html`<div class="toast" role="status">${lucideIcon(pending.action.action === 'archive' ? Archive : Trash2, { size: 16 })}<span class="toast-label">${pending.action.action === 'archive' ? 'Archived chat' : 'Deleted chat'}</span><button class="undo" data-conversation-id=${pending.action.conversationId} aria-label=${`Undo ${pending.action.action} of ${pending.action.title || 'chat'}`} ?disabled=${Boolean(this.pending)} @click=${() => this.undo(pending.requestId)}>Undo</button></div>`)}
          ${!this.undoPendings.length && this.pending ? html`<div class="toast" role="status">Updating chat…</div>` : nothing}
          ${this.failure || this.feedback ? html`<div class=${`toast ${this.failure ? 'error' : ''}`} role=${this.failure ? 'alert' : 'status'}>${this.failure || this.feedback}<button class="icon" aria-label="Dismiss" @click=${() => { this.failure = ''; this.feedback = '' }}>${lucideIcon(X, { size: 14 })}</button></div>` : nothing}
        </div>
      ` : nothing}
    `
  }
}
if (!customElements.get('lv-chat-manager')) customElements.define('lv-chat-manager', ChatManager)
