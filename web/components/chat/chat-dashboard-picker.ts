import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { LayoutDashboard, Plus, X } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'

import { uuidv7 } from '../shared/command'
import { addChatVisualToDashboard, listChatDashboards, type ChatDashboardDestination, type ChatDashboardResult, type ChatDashboardChoice } from './chat-dashboard-api'

export class ChatDashboardPicker extends LitElement {
  @property() visualTitle = ''
  @property() conversationId = ''
  @property() artifactId = ''
  @property() preferredPageId = ''
  private loadController?: AbortController
  private attempt?: { choice: string; key: string }
  @state() private destinations: ChatDashboardDestination[] = []
  @state() private loading = true
  @state() private saving = false
  @state() private canCreate = false
  @state() private error = ''
  @property() preferredDashboardId = ''
  @state() private result?: ChatDashboardResult
  @state() private selectedId = ''
  @state() private pageId = ''
  @state() private newTitle = ''
  @state() private query = ''

  static styles = css`
    :host { display: contents; }
    * { box-sizing: border-box; }
    dialog { width: 32rem; max-width: calc(100vw - 2rem); max-height: 85svh; padding: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); color: var(--lv-fg-default); font: var(--lv-type-body); }
    dialog::backdrop { background: var(--lv-modal-backdrop); }
    header { display: flex; align-items: start; gap: var(--base-size-16); padding: var(--base-size-20); border-bottom: var(--lv-border-muted); }
    header > div { flex: 1; min-width: 0; }
    h2 { margin: 0; font: var(--lv-type-section-title); }
    p { margin: var(--base-size-8) 0 0; }
    .description, .hint { color: var(--lv-fg-muted); font: var(--lv-type-secondary); }
    .body { padding: var(--base-size-20); overflow: auto; max-height: 52svh; }
    .destinations { display: grid; gap: var(--base-size-8); margin-top: var(--base-size-12); }
    .destination { display: flex; align-items: center; gap: var(--base-size-12); min-height: 3rem; padding: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); cursor: pointer; }
    .destination:has(input:checked) { border-color: var(--lv-line-accent); background: var(--lv-bg-control-hover); }
    .destination span { overflow-wrap: anywhere; }
    .destination svg { flex-shrink: 0; }
    .field { display: grid; gap: var(--base-size-8); margin-top: var(--base-size-16); }
    input:not([type=radio]), select { width: 100%; min-height: var(--lv-control-large); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); padding: var(--base-size-8) var(--base-size-12); background: var(--lv-bg-control); color: var(--lv-fg-default); font: inherit; }
    input[type=radio] { margin: 0; accent-color: var(--lv-line-accent); }
    footer { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: var(--base-size-8); padding: var(--base-size-16) var(--base-size-20); border-top: var(--lv-border-muted); }
    button, .button { display: inline-flex; align-items: center; justify-content: center; min-height: var(--lv-control-medium); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); padding: var(--base-size-8) var(--base-size-12); color: var(--lv-fg-default); background: var(--lv-bg-control); font: var(--lv-type-secondary); text-decoration: none; cursor: pointer; }
    button:hover:not(:disabled), .button:hover { background: var(--lv-bg-control-hover); }
    button:disabled { opacity: .6; cursor: default; }
    button:focus-visible, a:focus-visible, input:focus-visible, select:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-line-accent); outline-offset: 2px; }
    .close { padding: 0; min-width: var(--lv-control-medium); }
    .error { color: var(--lv-fg-danger); }
  `

  protected firstUpdated(): void {
    this.newTitle = this.visualTitle
    this.renderRoot.querySelector<HTMLDialogElement>('dialog')?.showModal()
    void this.loadDashboards()
  }

  disconnectedCallback(): void {
    this.loadController?.abort()
    super.disconnectedCallback()
  }

  private async loadDashboards(): Promise<void> {
    this.loadController?.abort()
    const controller = this.loadController = new AbortController()
    this.loading = true
    this.error = ''
    try {
      const options = await listChatDashboards(this.conversationId, this.artifactId, controller.signal)
      if (!this.isConnected || controller.signal.aborted) return
      this.destinations = options.dashboards
      this.canCreate = options.canCreate
    } catch {
      if (!controller.signal.aborted) this.error = 'Could not load editable dashboards. Please try again.'
    } finally {
      if (!controller.signal.aborted) this.loading = false
    }
  }

  protected updated(changed: Map<string, unknown>): void {
    if ((changed.has('destinations') || changed.has('canCreate')) && !this.selectedId) {
      const preferred = this.destinations.find(item => item.id === this.preferredDashboardId)
      this.selectDestination(preferred?.id || this.destinations[0]?.id || (this.canCreate ? 'new' : ''))
    }
  }

  render() {
    const selected = this.destinations.find(item => item.id === this.selectedId)
    const destinations = this.destinations.filter(item => item.title.toLocaleLowerCase().includes(this.query.trim().toLocaleLowerCase()))
    return html`
      <dialog aria-labelledby="dashboard-picker-title" aria-describedby="dashboard-picker-description" @cancel=${this.cancel} @click=${this.backdrop}>
        <header>
          <div><h2 id="dashboard-picker-title">${this.result ? 'Visual added' : 'Add to dashboard'}</h2>
            <p id="dashboard-picker-description" class="description">${this.result ? this.result.title : this.visualTitle}</p></div>
          <button class="close" type="button" aria-label="Close add to dashboard" ?disabled=${this.saving} @click=${this.close}>${lucideIcon(X, { size: 16 })}</button>
        </header>
        ${this.result ? html`
          <div class="body"><p role="status">Your visual has been added. Would you like to add another visual to this dashboard?</p></div>
          <footer>
            <button type="button" @click=${this.close}>Done</button>
            <a class="button" href=${this.result.href}>Open dashboard</a>
            <button type="button" @click=${this.addAnother}>Add another visual</button>
          </footer>
        ` : html`
          <form @submit=${this.submit}>
            <div class="body" aria-busy=${String(this.loading || this.saving)}>
              ${this.loading ? html`<p role="status">Loading dashboards…</p>` : html`
                ${this.destinations.length > 6 ? html`<label class="field">Find a dashboard<input type="search" .value=${this.query} @input=${this.search}></label>` : nothing}
                <div class="destinations" role="group" aria-label="Choose a dashboard">
                  ${this.canCreate ? this.destinationOption('new', 'New dashboard', true) : nothing}
                  ${destinations.map(item => this.destinationOption(item.id, item.title))}
                </div>
                ${this.destinations.length === 0 ? html`<p class="hint">${this.canCreate ? 'Create a dashboard to keep this visual and add more.' : 'No editable dashboards are available for this visual.'}</p>` : nothing}
                ${selected?.createsCopy ? html`<p class="hint">An editable copy of this published dashboard will be created with your visual.</p>` : nothing}
                ${this.selectedId === 'new' ? html`
                  <label class="field">Dashboard name<input name="title" maxlength="255" required .value=${this.newTitle} ?disabled=${this.saving} @input=${(event: Event) => { this.newTitle = (event.target as HTMLInputElement).value }}></label>
                  <p class="hint">Your new dashboard will be private until you publish it.</p>
                ` : selected && selected.pages.length > 1 ? html`
                  <label class="field">Page<select name="page" aria-label="Page" .value=${this.pageId} ?disabled=${this.saving} @change=${(event: Event) => { this.pageId = (event.target as HTMLSelectElement).value }}>${selected.pages.map(page => html`<option value=${page.id}>${page.title}</option>`)}</select></label>
                ` : nothing}
              `}
              ${this.error ? html`<p class="error" role="alert">${this.error}</p>${!this.selectedId ? html`<button type="button" @click=${this.loadDashboards}>Try again</button>` : nothing}` : nothing}
            </div>
            <footer><button type="button" ?disabled=${this.saving} @click=${this.close}>Cancel</button><button type="submit" ?disabled=${this.loading || this.saving || !this.selectedId || (this.selectedId === 'new' && !this.newTitle.trim())}>${this.saving ? 'Adding…' : this.selectedId === 'new' ? 'Create dashboard and add' : selected?.createsCopy ? 'Create copy and add' : 'Add visual'}</button></footer>
          </form>
        `}
      </dialog>
    `
  }

  private search(event: Event): void {
    this.query = (event.target as HTMLInputElement).value
    const selected = this.destinations.find(item => item.id === this.selectedId)
    if (selected && !selected.title.toLocaleLowerCase().includes(this.query.trim().toLocaleLowerCase())) {
      this.selectedId = ''
      this.pageId = ''
    }
  }

  private destinationOption(id: string, title: string, create = false) {
    return html`<label class="destination"><input type="radio" name="dashboard" value=${id} .checked=${this.selectedId === id} ?disabled=${this.saving} @change=${() => this.selectDestination(id)}>${lucideIcon(create ? Plus : LayoutDashboard, { size: 18 })}<span>${title}</span></label>`
  }

  private selectDestination(id: string): void {
    this.selectedId = id
    const pages = this.destinations.find(item => item.id === id)?.pages ?? []
    this.pageId = (id === this.preferredDashboardId && pages.some(page => page.id === this.preferredPageId) ? this.preferredPageId : pages[0]?.id) ?? ''
  }

  private async submit(event: SubmitEvent): Promise<void> {
    event.preventDefault()
    if (this.loading || this.saving || !this.selectedId) return
    const choice: ChatDashboardChoice = this.selectedId === 'new' ? { title: this.newTitle.trim() } : { dashboardId: this.selectedId, pageId: this.pageId }
    if ('title' in choice ? !choice.title : !choice.pageId) return
    const serialized = JSON.stringify(choice)
    if (this.attempt?.choice !== serialized) this.attempt = { choice: serialized, key: uuidv7() }
    this.saving = true
    this.error = ''
    try {
      const result = await addChatVisualToDashboard(this.conversationId, this.artifactId, choice, this.attempt.key)
      if (this.isConnected) this.result = result
    } catch (error) {
      if (this.isConnected) this.error = error instanceof Error ? error.message : 'Could not add this visual. Please try again.'
    } finally { this.saving = false }
  }

  private cancel(event: Event): void { event.preventDefault(); this.close() }
  private backdrop(event: MouseEvent): void { if (event.target === event.currentTarget) this.close() }
  private close(): void {
    if (this.saving) return
    this.renderRoot.querySelector<HTMLDialogElement>('dialog')?.close()
    this.dispatchEvent(new CustomEvent('lv-chat-dashboard-close', { bubbles: true, composed: true }))
  }

  private addAnother(): void {
    this.renderRoot.querySelector<HTMLDialogElement>('dialog')?.close()
    this.dispatchEvent(new CustomEvent('lv-chat-dashboard-add-another', { bubbles: true, composed: true, detail: this.result }))
  }
}

if (!customElements.get('lv-chat-dashboard-picker')) customElements.define('lv-chat-dashboard-picker', ChatDashboardPicker)
