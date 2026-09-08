import { LitElement, css, html, nothing, type PropertyValues } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { ExplorationSpec } from '../../generated/exploration'
import type {
  DataExplorerDashboardAppendCommandSignal,
  DataExplorerDashboardSelectTargetCommandSignal,
  DataExplorerDashboardSignal,
} from '../../generated/signals'
import { browserCommandFailure, type BrowserCommandFailure } from '../shared/command-failure'

type DashboardAuthoringAction = 'target' | 'refresh' | 'append'

type DashboardFetchDetail = {
  type?: string
  argsRaw?: { status?: string | number }
  el?: Element
}

const dashboardAuthoringActionAttribute = 'data-lv-dashboard-action'

function dashboardAuthoringAction(event: Event): DashboardAuthoringAction | null {
  const owner = (event as CustomEvent<DashboardFetchDetail>).detail?.el
  const action = owner?.getAttribute(dashboardAuthoringActionAttribute)
  return action === 'target' || action === 'refresh' || action === 'append' ? action : null
}

/**
 * Owns the dashboard target/page/placement interaction so data-explorer keeps
 * query state independent from authoring state. Target metadata is safe to
 * bootstrap; selecting a target asks the server for its opaque CAS/page view.
 */
export class DataExplorerDashboardPicker extends LitElement {
  @property({ attribute: false }) state: DataExplorerDashboardSignal = { enabled: false, targets: [] }
  @property({ attribute: false }) spec: ExplorationSpec = {
    schemaVersion: 1, modelId: '', dimensions: [], metrics: [], filters: [], sort: [], limit: 100,
  }
  @state() private open = false
  @state() private targetID = ''
  @state() private pageID = ''
  @state() private placementChoice: DataExplorerDashboardAppendCommandSignal['placementChoice'] = 'half'
  @state() private appendPending = false
  @state() private targetRequestID = ''
  @state() private refreshPending = false
  @state() private transportFailure: BrowserCommandFailure | null = null
  private targetResponsePending = false
  private refreshResponsePending = false
  private appendResponsePending = false

  static styles = css`
    :host { display: inline-block; }
    .text-button, .close-button, .primary-button { cursor: pointer; }
    .text-button { border: 0; background: transparent; color: var(--lv-fg-muted, #667085); padding: 0.25rem; }
    .text-button:hover, .text-button:focus-visible { color: var(--lv-fg-default, #101828); text-decoration: underline; }
    .add-dashboard-panel { display: grid; gap: 0.5rem; margin-top: 0.5rem; min-width: 18rem; padding: 0.75rem; border: 1px solid var(--lv-border-default, #d0d5dd); border-radius: 0.5rem; background: var(--lv-bg-default, #fff); }
    label { display: grid; gap: 0.2rem; font-size: 0.8rem; }
    select { min-height: 2rem; }
    .panel-header { display: flex; align-items: center; justify-content: space-between; gap: 0.75rem; }
    .panel-header strong { font-size: 0.85rem; }
    .close-button { border: 0; background: transparent; color: var(--lv-fg-muted, #667085); font-size: 1.1rem; }
    .dashboard-add-help, .dashboard-fork-help { margin: 0; color: var(--lv-fg-muted, #667085); font-size: 0.78rem; line-height: 1.35; }
    .dashboard-fork-help { display: grid; gap: 0.25rem; }
    .dashboard-fork-help a { color: var(--lv-fg-link, #175cd3); }
    .status { margin: 0; font-size: 0.8rem; }
    .status.error { color: var(--lv-fg-danger, #b42318); }
    .primary-button { border: 0; border-radius: 0.35rem; padding: 0.45rem 0.7rem; background: var(--lv-bg-accent, #175cd3); color: white; }
  `

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    super.disconnectedCallback()
  }

  render() {
    if (!this.state.enabled) return nothing
    const targets = this.state.targets ?? []
    const selectedTarget = targets.find((target) => target.id === this.targetID) ?? targets[0]
    const targetID = selectedTarget?.id ?? ''
    const pages = selectedTarget?.pages ?? []
    const selectedPage = pages.find((page) => page.id === this.pageID) ?? pages[0]
    const pageID = selectedPage?.id ?? ''
    const forkTargets = this.state.forkTargets ?? []
    return html`
      <button type="button" class="text-button" @click=${() => this.toggle(targets)}>Add to dashboard</button>
      ${this.open ? html`
        <div class="add-dashboard-panel" role="region" aria-label="Add exploration to dashboard">
          <div class="panel-header">
            <strong>Add exploration to dashboard</strong>
            <button type="button" class="close-button" aria-label="Close dashboard picker" @click=${() => this.open = false}>×</button>
          </div>
          <label>Dashboard
            <select .value=${targetID} @change=${(event: Event) => this.selectTarget((event.target as HTMLSelectElement).value)} ?disabled=${targets.length === 0 || this.targetRequestID !== '' || this.refreshPending || this.appendPending}>
              ${targets.length ? targets.map((target) => html`<option value=${target.id} ?selected=${target.id === targetID}>${target.title || target.id}</option>`) : html`<option value="">No authorized authored dashboards</option>`}
            </select>
          </label>
          <label>Page
            <select .value=${pageID} @change=${(event: Event) => this.pageID = (event.target as HTMLSelectElement).value} ?disabled=${pages.length === 0 || this.appendPending || this.targetRequestID !== '' || this.refreshPending}>
              ${pages.length ? pages.map((page) => html`<option value=${page.id} ?selected=${page.id === pageID}>${page.title || page.id}</option>`) : html`<option value="">Select an authorized dashboard</option>`}
            </select>
          </label>
          <label>Placement
            <select .value=${this.placementChoice} ?disabled=${this.appendPending} @change=${(event: Event) => this.placementChoice = (event.target as HTMLSelectElement).value as DataExplorerDashboardAppendCommandSignal['placementChoice']}>
              <option value="half">Half width</option>
              <option value="full">Full width</option>
            </select>
          </label>
          <p class="dashboard-add-help">The server supplies the authorized draft, opaque revision token, and a safe non-overlapping placement. Appends create an independent visual and scoped filters on the selected page.</p>
          ${forkTargets.length ? html`
            <div class="dashboard-fork-help">
              <p>Project/YAML dashboards must first become an independent editable copy.</p>
              ${forkTargets.map((target) => html`
                <a href=${target.forkHref} target="_blank" rel="noopener" data-dashboard-fork=${target.id}>Create editable copy of ${target.title || target.id}</a>
              `)}
            </div>
          ` : nothing}
          <button type="button" class="text-button" ?disabled=${this.appendPending || this.refreshPending || this.targetRequestID !== ''} @click=${() => this.refreshTargets()}>Refresh targets</button>
          ${this.targetRequestID || this.refreshPending ? html`<p class="status" role="status">Loading authorized dashboard targets…</p>` : nothing}
          ${this.transportFailure ? html`<p class="status error" role="alert">${this.transportFailure.message}</p>` : nothing}
          ${this.state.message ? html`<p class=${`status${this.state.state === 'error' ? ' error' : ''}`} role=${this.state.state === 'error' ? 'alert' : 'status'}>${this.state.message}</p>` : nothing}
          <button type="button" class="primary-button" ?disabled=${this.appendPending || this.targetRequestID !== '' || this.refreshPending || !targetID || !selectedTarget?.revisionToken || !pageID} @click=${() => selectedTarget && this.submit(selectedTarget, pageID)}> ${this.appendPending ? 'Adding…' : 'Add visual'} </button>
        </div>
      ` : nothing}
    `
  }

  private toggle(targets: DataExplorerDashboardSignal['targets']): void {
    this.open = !this.open
    if (this.open) {
      const targetID = this.targetID || targets[0]?.id || ''
      if (targetID) this.selectTarget(targetID)
    }
  }

  private selectTarget(targetID: string): void {
    if (!targetID || this.targetRequestID === targetID || this.appendPending || this.refreshPending) return
    this.targetID = targetID
    this.pageID = ''
    this.targetRequestID = targetID
    this.targetResponsePending = false
    this.transportFailure = null
    const command: DataExplorerDashboardSelectTargetCommandSignal = { dashboardId: targetID }
    this.dispatchEvent(new CustomEvent('lv-data-explorer-dashboard-target', {
      bubbles: true, composed: true, detail: command,
    }))
  }

  private submit(target: DataExplorerDashboardSignal['targets'][number], pageID: string): void {
    if (this.appendPending || !target.revisionToken || !pageID) return
    this.appendPending = true
    this.appendResponsePending = false
    this.transportFailure = null
    this.dispatchEvent(new CustomEvent('lv-data-explorer-add-to-dashboard', {
      bubbles: true, composed: true,
      detail: {
        dashboardId: target.id,
        pageId: pageID,
        revisionToken: target.revisionToken ?? '',
        placementChoice: this.placementChoice,
        spec: this.spec,
      },
    }))
  }

  private refreshTargets(): void {
    if (this.appendPending || this.refreshPending || this.targetRequestID !== '') return
    // The canonical spec remains in this component while the fork form is
    // open in a separate tab. The server validates this state again and
    // returns only the refreshed dashboard projection.
    this.targetRequestID = ''
    this.refreshPending = true
    this.refreshResponsePending = false
    this.transportFailure = null
    this.dispatchEvent(new CustomEvent('lv-data-explorer-dashboard-refresh', {
      bubbles: true, composed: true, detail: { modelId: this.spec.modelId },
    }))
  }

  protected override updated(changed: PropertyValues<this>): void {
    let startedTargetRequest = false
    if (changed.has('state') && this.refreshPending) {
      if (this.refreshResponsePending) {
        this.refreshResponsePending = false
        this.refreshPending = false
        const targetID = this.targetID || this.state.targets?.[0]?.id || ''
        if (targetID) {
          this.selectTarget(targetID)
          startedTargetRequest = true
        }
      }
    }
    // Local state changes also trigger updated(). Only a replacement of the
    // server signal can complete a request; otherwise a cached token or the
    // optimistic append click would clear its own pending indicator.
    if (!startedTargetRequest && changed.has('state') && this.targetResponsePending) {
      this.targetResponsePending = false
      this.targetRequestID = ''
    }
    if (changed.has('state') && this.appendResponsePending) {
      this.appendResponsePending = false
      this.appendPending = false
    }
  }

  /**
   * Authoring commands use dedicated document-level action bridges. Keeping
   * their owner separate from the explorer host means a failed append cannot
   * stop or replace a query that happens to be running at the same time.
   */
  private readonly handleDatastarFetch = (event: Event): void => {
    const action = dashboardAuthoringAction(event)
    if (!action) return
    const detail = (event as CustomEvent<DashboardFetchDetail>).detail
    if (detail?.type === 'datastar-patch-signals') {
      if (action === 'target' && this.targetRequestID) this.targetResponsePending = true
      if (action === 'refresh' && this.refreshPending) this.refreshResponsePending = true
      if (action === 'append' && this.appendPending) this.appendResponsePending = true
      return
    }
    const failure = browserCommandFailure(event, action === 'append' ? 'Adding to dashboard' : action === 'refresh' ? 'Refreshing dashboard targets' : 'Loading dashboard pages')
    if (!failure) return
    this.transportFailure = failure
    if (action === 'target') {
      this.targetRequestID = ''
      this.targetResponsePending = false
    } else if (action === 'refresh') {
      this.refreshPending = false
      this.refreshResponsePending = false
    } else {
      this.appendPending = false
      this.appendResponsePending = false
    }
  }
}

if (!customElements.get('lv-data-explorer-dashboard-picker')) customElements.define('lv-data-explorer-dashboard-picker', DataExplorerDashboardPicker)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explorer-dashboard-picker': DataExplorerDashboardPicker
  }
}
