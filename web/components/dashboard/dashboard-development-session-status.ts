import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import {
  developmentSessionEventsPath,
  DevelopmentSessionViewController,
  type DevelopmentSessionDiagnostic,
  type DevelopmentSessionRecord,
} from './dashboard-page-session'

class LeapViewDashboardDevelopmentSessionStatus extends LitElement {
  @property({ attribute: 'serving-state-id' }) servingStateID = ''
  @property({ type: Boolean, reflect: true }) private visible = false
  @state() private diagnostics: DevelopmentSessionDiagnostic[] = []
  private events?: EventSource
  private readonly controller = new DevelopmentSessionViewController()

  static styles = css`
    :host {
      display: none;
    }

    :host([visible]) {
      display: grid;
      gap: var(--base-size-4);
      margin: var(--base-size-8) var(--base-size-12) 0;
      border: 1px solid var(--display-yellow-borderColor, var(--lv-border-muted));
      border-radius: var(--lv-radius-default);
      background: var(--display-yellow-bgColor, var(--lv-bg-panel));
      color: var(--lv-fg-default);
      padding: var(--base-size-8) var(--base-size-12);
      font: var(--lv-type-body-compact);
    }

    ul {
      display: grid;
      gap: var(--base-size-2);
      margin: 0;
      padding-inline-start: var(--base-size-20);
    }

    code {
      margin-inline-end: var(--base-size-4);
      color: var(--lv-fg-muted);
    }
  `

  connectedCallback(): void {
    super.connectedCallback()
    this.setAttribute('role', 'status')
    this.setAttribute('aria-live', 'polite')
    this.connect()
  }

  disconnectedCallback(): void {
    this.events?.close()
    this.events = undefined
    super.disconnectedCallback()
  }

  render() {
    if (!this.visible) return nothing
    return html`
      <strong>Preview out of date</strong>
      ${this.diagnostics.length > 0
        ? html`<span>Repair the attempted edit; the previous working candidate remains visible.</span>
          <ul>
            ${this.diagnostics.map((diagnostic) => html`
              <li>
                ${diagnostic.path ? html`<code>${diagnostic.path}${diagnostic.line ? `:${diagnostic.line}` : ''}${diagnostic.column ? `:${diagnostic.column}` : ''}</code>` : nothing}
                ${diagnostic.message || diagnostic.code || 'Development synchronization failed'}
              </li>
            `)}
          </ul>`
        : html`<span>An edit is being synchronized; this view remains pinned to the last valid candidate.</span>`}
    `
  }

  private connect(): void {
    const pathname = typeof window === 'undefined' ? '' : window.location.pathname
    const eventsPath = developmentSessionEventsPath(pathname)
    if (!eventsPath || typeof EventSource === 'undefined') return
    this.events = new EventSource(eventsPath, { withCredentials: true })
    this.events.addEventListener('development-session', this.handleEvent)
  }

  private handleEvent = (event: Event): void => {
    const data = (event as MessageEvent<string>).data
    if (!data) return
    let record: DevelopmentSessionRecord
    try {
      record = JSON.parse(data) as DevelopmentSessionRecord
    } catch {
      return
    }
    const currentCandidateID = this.servingStateID.startsWith('candidate:')
      ? this.servingStateID.split(':')[1]?.trim() ?? ''
      : ''
    const transition = this.controller.consume(record, currentCandidateID)
    this.diagnostics = transition.diagnostics
    this.visible = transition.outOfDate || transition.diagnostics.length > 0
    if (transition.shouldReload) window.location.reload()
  }
}

if (typeof customElements !== 'undefined' && !customElements.get('lv-dashboard-development-session-status')) {
  customElements.define('lv-dashboard-development-session-status', LeapViewDashboardDevelopmentSessionStatus)
}
