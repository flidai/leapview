import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ArrowUpRight, MessageSquareText, Save, Trash2, X } from 'lucide'
import type { ChatDashboardDraftSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { lucideIcon } from '../shared/lucide-icons'
import { uuidv7 } from '../shared/command'
import { normalizeChatVisualDisplayPayload } from './chat-visual-panel'
import { saveChatDashboardDraft, type ChatDashboardSaveResult } from './chat-dashboard-api'
import '../dashboard/visualization/host'

type SaveAttempt = {
  identity: string
  key: string
  revision: string
  title: string
}

type SavedDashboard = {
  revision: string
  result: ChatDashboardSaveResult
}

export class ChatDashboardDraft extends LitElement {
  @property({ attribute: false }) draft?: ChatDashboardDraftSignal
  @property({ attribute: false }) visuals: Record<string, VisualizationEnvelope> = {}
  @property({ attribute: 'conversation-id' }) conversationId = ''
  @property({ type: Boolean, reflect: true }) modal = false
  @property({ attribute: false }) selectedVisualId = ''
  @property({ type: Boolean }) busy = false
  @property({ attribute: 'source-artifact-id' }) sourceArtifactId = ''

  @state() private saveDialogOpen = false
  @state() private saveTitle = ''
  @state() private saving = false
  @state() private saveError = ''
  @state() private saved?: SavedDashboard
  private saveAttempt?: SaveAttempt
  private saveTrigger?: HTMLElement
  private saveDialogElement?: HTMLDialogElement
  private saveRequestSequence = 0
  private displayedRevision = ''
  private normalizedVisuals = new WeakMap<VisualizationEnvelope, VisualizationEnvelope>()

  static styles = css`
    :host {
      display: block;
      box-sizing: border-box;
      min-width: 0;
      min-height: 0;
      height: 100%;
      container-type: inline-size;
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel);
      font-family: var(--fontStack-system);
    }

    *, *::before, *::after { box-sizing: inherit; }

    aside {
      display: grid;
      height: 100%;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr);
    }

    .toolbar { min-width: 0; }

    .header {
      display: flex;
      flex-wrap: wrap;
      min-height: 4.5rem;
      align-items: center;
      gap: var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      padding: var(--base-size-12) var(--base-size-16);
    }

    .heading { min-width: 0; flex: 1; }
    .eyebrow { display: block; margin-bottom: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    h2 { overflow: hidden; margin: 0; color: var(--lv-fg-default); font: var(--lv-type-section-title); text-overflow: ellipsis; white-space: nowrap; }
    .count { display: block; margin-top: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .actions { display: flex; flex: 0 0 auto; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: var(--base-size-8); }

    button, a.button {
      display: inline-flex;
      min-height: var(--lv-control-medium);
      align-items: center;
      justify-content: center;
      gap: var(--base-size-8);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: 0 var(--base-size-12);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-secondary);
      text-decoration: none;
      white-space: nowrap;
    }

    button:hover:not(:disabled), a.button:hover { background: var(--lv-bg-control-hover); }
    button:focus-visible, a:focus-visible, input:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-line-accent); outline-offset: 2px; }
    button:disabled { cursor: default; opacity: .6; }
    .primary { border-color: var(--lv-line-accent); background: var(--lv-line-accent); color: var(--lv-accent-fg); }
    .primary:hover:not(:disabled) { filter: brightness(.94); }
    .close { width: var(--lv-control-medium); padding: 0; }

    .saved-link { max-width: 100%; color: var(--lv-fg-accent); font: var(--lv-type-secondary); white-space: nowrap; }
    .saved-note { margin: var(--base-size-8) var(--base-size-16) 0; color: var(--lv-fg-muted); font: var(--lv-type-caption); }

    .canvas {
      min-width: 0;
      min-height: 0;
      overflow: auto;
      padding: var(--base-size-16);
      background: var(--lv-bg-app);
    }

    .cards {
      display: grid;
      grid-template-columns: minmax(0, 1fr);
      align-items: start;
      gap: var(--base-size-16);
    }

    .card {
      min-width: 0;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-panel);
      box-shadow: var(--shadow-resting-small);
    }

    .card[data-selected="true"] { border-color: var(--lv-line-accent); box-shadow: 0 0 0 1px var(--lv-line-accent); }
    .card-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--base-size-8); border-bottom: var(--lv-border-muted); padding: var(--base-size-8) var(--base-size-12); }
    .card-actions button { min-height: 1.9rem; padding-inline: var(--base-size-8); }
    .remove { color: var(--lv-fg-danger); }
    .visual { height: clamp(22rem, 42vh, 30rem); min-height: 22rem; padding: var(--base-size-8); }
    .visual.scroll-labels { overflow-x: auto; }
    .visual.scroll-labels lv-visualization-host { min-width: 40rem; }
    lv-visualization-host { display: block; width: 100%; height: 100%; min-width: 0; min-height: 0; }
    .unavailable { display: grid; min-height: 18rem; place-items: center; padding: var(--base-size-16); color: var(--lv-fg-muted); text-align: center; font: var(--lv-type-secondary); }
    .empty { display: grid; min-height: 15rem; place-content: center; justify-items: center; gap: var(--base-size-8); padding: var(--base-size-24); color: var(--lv-fg-muted); text-align: center; }
    .empty strong { color: var(--lv-fg-default); font: var(--lv-type-section-title); }
    .empty p { max-width: 28rem; margin: 0; font: var(--lv-type-secondary); }

    dialog { width: min(32rem, calc(100vw - 2rem)); max-height: 85svh; padding: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); color: var(--lv-fg-default); font: var(--lv-type-body); }
    dialog::backdrop { background: var(--lv-modal-backdrop); }
    dialog header { display: flex; align-items: start; gap: var(--base-size-16); border-bottom: var(--lv-border-muted); padding: var(--base-size-16) var(--base-size-20); }
    dialog header > div { min-width: 0; flex: 1; }
    dialog h3 { margin: 0; font: var(--lv-type-section-title); }
    dialog p { margin: var(--base-size-8) 0 0; color: var(--lv-fg-muted); font: var(--lv-type-secondary); }
    .dialog-body { display: grid; gap: var(--base-size-12); padding: var(--base-size-20); }
    .field { display: grid; gap: var(--base-size-8); font: var(--lv-type-secondary); }
    input { width: 100%; min-height: 2.5rem; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-control); padding: 0 var(--base-size-12); color: var(--lv-fg-default); font: inherit; }
    .error { margin: 0; color: var(--lv-fg-danger); }
    .success { display: grid; gap: var(--base-size-8); }
    dialog footer { display: flex; justify-content: flex-end; gap: var(--base-size-8); border-top: var(--lv-border-muted); padding: var(--base-size-12) var(--base-size-16); }

    @media (max-width: 768px) {
      :host([modal]) { position: fixed; z-index: 30; inset: 0; height: 100svh; }
      .header { padding: var(--base-size-8) var(--base-size-12); }
      .header > .close { display: inline-flex; }
      .actions { gap: var(--base-size-4); }
      .actions button { padding-inline: var(--base-size-8); }
      .canvas { padding: var(--base-size-12); }
      .visual { height: min(54svh, 30rem); min-height: 18rem; }
    }

    @container (min-width: 1000px) {
      .cards { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    }

    @container (max-width: 42rem) {
      .header { align-items: start; }
      .heading { flex-basis: calc(100% - 3rem); }
      .actions { width: 100%; justify-content: flex-end; }
      .saved-link { white-space: normal; overflow-wrap: anywhere; }
    }


  `

  protected willUpdate(changed: Map<string, unknown>): void {
    const revision = this.draft?.revision?.trim() ?? ''
    if (revision !== this.displayedRevision) {
      this.displayedRevision = revision
      if (!this.draft?.visuals.some((visual) => visual.id === this.selectedVisualId)) {
        this.selectedVisualId = this.draft?.visuals[0]?.id ?? ''
      }
      this.saveError = ''
    }
    if (changed.has('conversationId')) {
      this.saveRequestSequence++
      this.saved = undefined
      this.saveAttempt = undefined
      this.saveTrigger = undefined
      this.saveDialogOpen = false
      this.saveTitle = ''
      this.saveError = ''
      this.saving = false
    }
  }

  protected updated(changed: Map<string, unknown>): void {
    const dialog = this.shadowRoot?.querySelector<HTMLDialogElement>('dialog') ?? this.saveDialogElement
    if (this.saveDialogOpen && dialog && !dialog.open) {
      this.saveDialogElement = dialog
      dialog.showModal()
      this.shadowRoot?.querySelector<HTMLInputElement>('dialog input')?.focus()
    } else if (!this.saveDialogOpen && this.saveDialogElement) {
      if (dialog?.open) dialog.close()
      const returnFocus = this.saveTrigger?.isConnected && !(this.saveTrigger as HTMLButtonElement).disabled
        ? this.saveTrigger
        : this.shadowRoot?.querySelector<HTMLAnchorElement>('.header .saved-link')
      returnFocus?.focus()
      this.saveTrigger = undefined
      this.saveDialogElement = undefined
    }
    if (changed.has('modal') && this.modal) {
      this.focusClose()
    }
  }

  render() {
    const draft = this.draft
    const draftVisuals = draft?.visuals ?? []
    const savedForCurrentRevision = this.saved?.revision === draft?.revision
    const savedFromEarlierRevision = Boolean(this.saved && this.saved.revision !== draft?.revision)
    return html`
      <aside
        role=${this.modal ? 'dialog' : 'region'}
        aria-modal=${this.modal ? 'true' : nothing}
        aria-labelledby="dashboard-draft-title"
        aria-label=${this.modal ? nothing : 'Dashboard draft'}
        @keydown=${this.onKeyDown}
      >
        <div class="toolbar">
          <header class="header">
            <button class="close" type="button" aria-label="Back to chat" @click=${this.closeModal}>${lucideIcon(X, { size: 16 })}</button>
            <div class="heading">
              <span class="eyebrow">Live dashboard draft</span>
              <h2 id="dashboard-draft-title">${this.sourceArtifactId ? 'Dashboard preview' : draft?.title || 'Dashboard draft'}</h2>
              <span class="count">${draftVisuals.length} ${draftVisuals.length === 1 ? 'visual' : 'visuals'} · Draft</span>
            </div>
            <div class="actions">
              ${this.saved ? html`<a class="saved-link" href=${this.saved.result.href}>Open saved dashboard ${lucideIcon(ArrowUpRight, { size: 14 })}</a>` : nothing}
              <button class="primary" type="button" ?disabled=${this.busy || this.saving || savedForCurrentRevision || draftVisuals.length === 0} @click=${this.openSaveDialog}>
                ${this.saving ? 'Saving…' : savedForCurrentRevision ? 'Saved' : savedFromEarlierRevision ? 'Save as new dashboard' : html`${lucideIcon(Save, { size: 15 })} Save dashboard`}
              </button>
            </div>
          </header>
          ${savedFromEarlierRevision ? html`<p class="saved-note" role="status">The saved dashboard reflects an earlier draft. Save these changes as a new dashboard.</p>` : nothing}
        </div>
        <div class="canvas">
          ${draftVisuals.length === 0 ? html`
            <div class="empty" role="status">
              <strong>Your dashboard is taking shape</strong>
              <p>Ask LeapView to add charts. Each completed dashboard update appears here.</p>
            </div>
          ` : html`
            <div class="cards" aria-label="Draft dashboard visuals">
              ${draftVisuals.map((visual) => this.renderVisual(visual))}
            </div>
          `}
        </div>
      </aside>
      ${this.saveDialogOpen ? this.renderSaveDialog(savedFromEarlierRevision) : nothing}
    `
  }

  private renderVisual(visual: ChatDashboardDraftSignal['visuals'][number]) {
    const payload = this.visuals[visual.artifactId]
    const displayPayload = payload ? this.normalizedVisualPayload(payload) : undefined
    const title = visual.title?.trim() || payload?.spec.title?.trim() || 'Untitled visual'
    const selected = this.selectedVisualId === visual.id
    const scrollLabels = payload?.spec.kind === 'cartesian'
      && (payload.spec.mark === 'line' || payload.spec.mark === 'area')
      && payload.spec.presentation.labelPolicy.density === 'always'
    return html`
      <article class="card" data-draft-visual-id=${visual.id} data-selected=${String(selected)}>
        <div class="card-actions" role="group" aria-label=${`Actions for ${title}`}>
          <button type="button" aria-pressed=${String(selected)} aria-label=${`Ask about ${title}`} title=${`Ask about ${title}`} @click=${() => this.requestEdit(visual, title)}>
            ${lucideIcon(MessageSquareText, { size: 15 })}<span>Ask about visual</span>
          </button>
          <button class="remove" type="button" aria-label=${`Ask to remove ${title}`} title=${`Ask to remove ${title}`} @click=${() => this.requestRemove(visual, title)}>
            ${lucideIcon(Trash2, { size: 15 })}<span>Remove</span>
          </button>
        </div>
        ${payload ? html`
          <div class=${scrollLabels ? 'visual scroll-labels' : 'visual'} @click=${() => { this.selectedVisualId = visual.id }}>
            <lv-visualization-host .envelope=${displayPayload} defer-mount></lv-visualization-host>
          </div>
        ` : html`<div class="unavailable" role="status">Chart data is loading for ${title}…</div>`}
      </article>
    `
  }

  private normalizedVisualPayload(payload: VisualizationEnvelope): VisualizationEnvelope {
    const cached = this.normalizedVisuals.get(payload)
    if (cached) return cached
    const normalized = normalizeChatVisualDisplayPayload(payload) ?? payload
    this.normalizedVisuals.set(payload, normalized)
    return normalized
  }

  private renderSaveDialog(copy: boolean) {
    const saved = this.saved
    const result = saved && saved.revision === this.draft?.revision ? saved.result : undefined
    return html`
      <dialog aria-labelledby="save-dashboard-title" aria-describedby="save-dashboard-description" @cancel=${this.cancelSave} @click=${this.backdrop}>
        <header>
          <div>
            <h3 id="save-dashboard-title">${result ? 'Dashboard saved' : copy ? 'Save a new dashboard copy' : 'Save dashboard'}</h3>
            <p id="save-dashboard-description">${result ? 'Your live chat draft is still available here.' : copy ? 'Save the latest chat draft as a new dashboard. The earlier saved dashboard remains unchanged.' : 'Save this live chat draft as a new dashboard.'}</p>
          </div>
          <button class="close" type="button" aria-label="Close save dialog" ?disabled=${this.saving} @click=${this.closeSaveDialog}>${lucideIcon(X, { size: 16 })}</button>
        </header>
        ${result ? html`
          <div class="dialog-body success" role="status">
            <strong>${result.title}</strong>
            <a class="saved-link" href=${result.href}>Open dashboard ${lucideIcon(ArrowUpRight, { size: 14 })}</a>
          </div>
          <footer><button type="button" @click=${this.closeSaveDialog}>Done</button></footer>
        ` : html`
          <form @submit=${this.submitSave}>
            <div class="dialog-body" aria-busy=${String(this.saving || this.busy)}>
              <label class="field">Dashboard name
                <input name="title" maxlength="255" required autocomplete="off" .value=${this.saveTitle} ?disabled=${this.saving || this.busy} @input=${this.updateSaveTitle}>
              </label>
              ${this.busy ? html`<p role="status">Wait for the chat update to finish before saving.</p>` : nothing}
              ${this.saveError ? html`<p class="error" role="alert">${this.saveError}</p>` : nothing}
            </div>
            <footer>
              <button type="button" ?disabled=${this.saving} @click=${this.closeSaveDialog}>Cancel</button>
              <button class="primary" type="submit" ?disabled=${this.busy || this.saving || !this.saveTitle.trim()}>${this.saving ? 'Saving…' : 'Save dashboard'}</button>
            </footer>
          </form>
        `}
      </dialog>
    `
  }

  private openSaveDialog = (event: Event): void => {
    if (!this.draft || this.draft.visuals.length === 0 || this.busy || this.saving || this.saved?.revision === this.draft.revision) return
    if (this.sourceArtifactId) {
      this.dispatchEvent(new CustomEvent('lv-chat-dashboard-save-visual', { bubbles: true, composed: true }))
      return
    }
    this.saveTrigger = event.currentTarget instanceof HTMLElement ? event.currentTarget : undefined
    this.saveTitle = this.draft.title.trim()
    this.saveError = ''
    this.saveDialogOpen = true
  }

  private updateSaveTitle = (event: Event): void => {
    this.saveTitle = (event.currentTarget as HTMLInputElement).value
  }

  private async submitSave(event: Event): Promise<void> {
    event.preventDefault()
    const revision = this.draft?.revision?.trim() ?? ''
    const title = this.saveTitle.trim()
    const conversationId = this.conversationId.trim()
    if (this.busy || this.saving || !revision || !title || !conversationId) return
    const identity = JSON.stringify({ revision, title })
    if (this.saveAttempt?.identity !== identity) {
      this.saveAttempt = { identity, key: uuidv7(), revision, title }
    }
    const attempt = this.saveAttempt
    const requestSequence = ++this.saveRequestSequence
    this.saving = true
    this.saveError = ''
    try {
      const result = await saveChatDashboardDraft(conversationId, attempt.revision, attempt.title, attempt.key)
      if (!this.isConnected || requestSequence !== this.saveRequestSequence || this.conversationId.trim() !== conversationId) return
      this.saved = { revision: attempt.revision, result }
    } catch (error) {
      if (!this.isConnected || requestSequence !== this.saveRequestSequence || this.conversationId.trim() !== conversationId) return
      this.saveError = error instanceof Error ? error.message : 'Could not save this dashboard. Please try again.'
    } finally {
      if (this.isConnected && requestSequence === this.saveRequestSequence) this.saving = false
    }
  }

  private requestEdit(visual: ChatDashboardDraftSignal['visuals'][number], title: string): void {
    this.selectedVisualId = visual.id
    this.dispatchEditRequest('edit', visual, title)
  }

  private requestRemove(visual: ChatDashboardDraftSignal['visuals'][number], title: string): void {
    this.selectedVisualId = visual.id
    this.dispatchEditRequest('remove', visual, title)
  }

  private dispatchEditRequest(action: 'edit' | 'remove', visual: ChatDashboardDraftSignal['visuals'][number], title: string): void {
    const safeTitle = title.replace(/[\\"\n\r]/g, ' ').replace(/\s+/g, ' ').trim()
    const stableIdentity = this.sourceArtifactId ? `preview artifact id: ${this.sourceArtifactId}` : `draft visual id: ${visual.id}`
    const prompt = action === 'remove'
      ? `Remove the dashboard visual "${safeTitle}" (${stableIdentity}).`
      : `Update the dashboard visual "${safeTitle}" (${stableIdentity}): `
    this.dispatchEvent(new CustomEvent('lv-chat-dashboard-edit', {
      bubbles: true,
      composed: true,
      detail: { action, visualId: visual.id, title, prompt },
    }))
  }

  private closeModal = (): void => {
    this.dispatchEvent(new CustomEvent('lv-chat-dashboard-close', { bubbles: true, composed: true }))
  }

  focusSave(): void {
    this.renderRoot.querySelector<HTMLButtonElement>('.header .primary')?.focus()
  }

  focusClose(): void {
    this.renderRoot.querySelector<HTMLButtonElement>('.header .close')?.focus()
  }

  private onKeyDown = (event: KeyboardEvent): void => {
    if (!this.modal) return
    if (this.shadowRoot?.querySelector<HTMLDialogElement>('dialog[open]')) return
    if (event.key === 'Escape') {
      event.preventDefault()
      event.stopPropagation()
      this.closeModal()
      return
    }
    if (event.key !== 'Tab') return
    const focusable = Array.from(this.renderRoot.querySelectorAll<HTMLElement>('button:not(:disabled), a[href], input:not(:disabled), [tabindex]:not([tabindex="-1"])'))
    const first = focusable[0]
    const last = focusable.at(-1)
    const active = this.shadowRoot?.activeElement
    if (!first || !last) event.preventDefault()
    else if (event.shiftKey && active === first) {
      event.preventDefault()
      last.focus()
    } else if (!event.shiftKey && active === last) {
      event.preventDefault()
      first.focus()
    }
  }

  private closeSaveDialog = (): void => {
    if (this.saving) return
    this.saveDialogOpen = false
  }

  private cancelSave = (event: Event): void => {
    event.preventDefault()
    this.closeSaveDialog()
  }

  private backdrop = (event: MouseEvent): void => {
    if (event.target === event.currentTarget) this.closeSaveDialog()
  }
}

if (!customElements.get('lv-chat-dashboard-draft')) customElements.define('lv-chat-dashboard-draft', ChatDashboardDraft)
