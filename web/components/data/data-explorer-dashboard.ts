import { css, html, nothing, type TemplateResult } from 'lit'
import { ChevronDown, LayoutDashboard } from 'lucide'
import { lucideIcon } from '../shared/lucide-icons'
import type { ExplorationSpec } from '../../generated/exploration'
import '../shared/command'

export interface DashboardAppendTargetPage {
  id: string
  title: string
}

export interface DashboardAppendTarget {
  id: string
  title: string
  semanticModel: string
  draftId?: string
  revisionToken?: string
  pages?: DashboardAppendTargetPage[]
}

export interface DashboardAppendPickerOptions {
  enabled: boolean
  targets: DashboardAppendTarget[]
  selectedDashboardID: string
  selectedPageID: string
  placementChoice: 'half' | 'full'
  loading: boolean
  saving: boolean
  pendingTitle?: string
  status: string
  successDashboardURL: string
  onToggle(open: boolean): void
  onDashboardChange(id: string): void
  onPageChange(id: string): void
  onPlacementChange(value: 'half' | 'full'): void
  onAppend(): void
}

type PlacementChoice = 'half' | 'full'

type PendingAppend = {
  endpoint: string
  body: string
  idempotencyKey: string
  target: DashboardAppendTarget & { draftId: string; revisionToken: string }
  modelID: string
  uncertain: boolean
}

/** Owns dashboard picker requests and invalidates results when its source model changes. */
export class DashboardAppendController {
  targets: DashboardAppendTarget[] = []
  selectedDashboardID = ''
  selectedPageID = ''
  placementChoice: PlacementChoice = 'half'
  loading = false
  saving = false
  status = ''
  successDashboardURL = ''
  targetsLoaded = false
  private modelID = ''
  private requestVersion = 0
  private pendingAppend?: PendingAppend

  constructor(private readonly host: HTMLElement, private readonly refresh: () => void) {}

  private attribute(name: string): string {
    return this.host.getAttribute(name)?.trim() ?? ''
  }

  isAvailable(
    enabled: boolean,
    configured: boolean,
    expectedRequest: number,
    status: { state?: string; loading?: boolean; stale?: boolean; requestSeq?: number } | undefined,
    result: { requestSeq?: number; error?: unknown } | undefined,
    validSpec: boolean,
  ): boolean {
    return enabled && configured && (Boolean(this.pendingAppend) || (status?.state === 'success' && !status.loading && !status.stale &&
      status.requestSeq === expectedRequest && result?.requestSeq === expectedRequest && !result.error && validSpec
    ))
  }

  render(enabled: boolean, spec: ExplorationSpec): TemplateResult | typeof nothing {
    return renderDashboardAppendPicker({
      enabled,
      targets: this.targets,
      selectedDashboardID: this.selectedDashboardID,
      selectedPageID: this.selectedPageID,
      placementChoice: this.placementChoice,
      loading: this.loading,
      saving: this.saving,
      pendingTitle: this.pendingAppend ? this.pendingAppend.target.title || this.pendingAppend.target.id : undefined,
      status: this.status,
      successDashboardURL: this.successDashboardURL,
      onToggle: (open) => { if (open && !this.targetsLoaded && !this.pendingAppend) void this.loadTargets(spec.modelId?.trim() ?? '') },
      onDashboardChange: (id) => void this.selectTarget(id, spec.modelId?.trim() ?? ''),
      onPageChange: (id) => { this.selectedPageID = id; this.refresh() },
      onPlacementChange: (value) => { this.placementChoice = value; this.refresh() },
      onAppend: () => void this.append(spec),
    })
  }

  syncModel(modelID: string): void {
    if (modelID === this.modelID) return
    this.modelID = modelID
    this.requestVersion += 1
    this.targets = []
    this.selectedDashboardID = ''
    this.selectedPageID = ''
    this.targetsLoaded = false
    this.loading = false
    this.status = ''
    this.successDashboardURL = ''
    this.refresh()
  }

  async loadTargets(modelID: string): Promise<void> {
    const endpoint = this.attribute('data-dashboard-targets-url')
    if (!modelID || !endpoint) return
    const requestVersion = ++this.requestVersion
    this.loading = true
    this.status = ''
    this.refresh()
    try {
      const response = await fetch(`${endpoint}?semanticModel=${encodeURIComponent(modelID)}`, { headers: window.LeapViewCommand.headers() })
      if (!response.ok) throw new Error(response.status === 403 ? 'You cannot use this semantic model.' : 'Dashboard targets are unavailable.')
      const payload = await response.json() as { items?: DashboardAppendTarget[] }
      if (requestVersion !== this.requestVersion || modelID !== this.modelID) return
      this.targets = Array.isArray(payload.items) ? payload.items.filter((item) => item && typeof item.id === 'string' && typeof item.title === 'string') : []
      this.targetsLoaded = true
      if (this.targets.length === 0) this.status = 'No editable authored drafts use this model.'
    } catch (error) {
      if (requestVersion !== this.requestVersion || modelID !== this.modelID) return
      this.status = error instanceof Error ? error.message : 'Dashboard targets are unavailable.'
      this.targetsLoaded = false
    } finally {
      if (requestVersion === this.requestVersion) {
        this.loading = false
        this.refresh()
      }
    }
  }

  async selectTarget(id: string, modelID: string): Promise<void> {
    this.selectedDashboardID = id
    this.selectedPageID = ''
    this.successDashboardURL = ''
    this.status = ''
    const requestVersion = ++this.requestVersion
    if (!id) {
      this.loading = false
      this.refresh()
      return
    }
    const target = this.targets.find((item) => item.id === id)
    const endpoint = this.attribute('data-dashboard-targets-url')
    if (!target || !modelID || !endpoint) {
      this.refresh()
      return
    }
    this.loading = true
    this.refresh()
    try {
      const response = await fetch(`${endpoint}/${encodeURIComponent(id)}?semanticModel=${encodeURIComponent(modelID)}`, { headers: window.LeapViewCommand.headers() })
      if (!response.ok) throw new Error(response.status === 403 ? 'You cannot edit that dashboard.' : 'Dashboard pages are unavailable.')
      const detail = await response.json() as DashboardAppendTarget
      if (requestVersion !== this.requestVersion || modelID !== this.modelID || id !== this.selectedDashboardID) return
      if (detail.id !== id || detail.semanticModel !== modelID || !detail.draftId || !detail.revisionToken || !Array.isArray(detail.pages) || detail.pages.length === 0) {
        throw new Error('Dashboard target is no longer available. Refresh and try again.')
      }
      this.targets = this.targets.map((item) => item.id === id ? detail : item)
      this.selectedPageID = detail.pages[0]?.id ?? ''
    } catch (error) {
      if (requestVersion !== this.requestVersion || modelID !== this.modelID || id !== this.selectedDashboardID) return
      this.targets = this.targets.filter((item) => item.id !== id)
      this.selectedDashboardID = ''
      this.status = error instanceof Error ? error.message : 'Dashboard pages are unavailable.'
    } finally {
      if (requestVersion === this.requestVersion) {
        this.loading = false
        this.refresh()
      }
    }
  }

  async append(spec: ExplorationSpec): Promise<void> {
    if (this.saving) return
    if (!this.pendingAppend && spec.mode !== 'records' && !spec.metrics.length && !spec.pivot?.metrics.length) {
      this.status = 'Add at least one metric before adding an exploration to a dashboard.'
      this.refresh()
      return
    }
    let attempt = this.pendingAppend
    if (!attempt) {
      const target = this.targets.find((item) => item.id === this.selectedDashboardID)
      const operationID = this.attribute('data-dashboard-append-operation-id')
      const endpoint = this.attribute('data-dashboard-append-url')
      if (!target?.draftId || !this.selectedPageID || !target.revisionToken || operationID !== 'executeDashboardAuthoringCommand' || !endpoint || spec.modelId?.trim() !== this.modelID) return
      // Snapshot the complete intent before sending it. A retry must not pick up
      // a newer query, page, placement, or revision under the original key.
      attempt = {
        endpoint, target: { ...target, draftId: target.draftId, revisionToken: target.revisionToken }, modelID: this.modelID, uncertain: false,
        body: JSON.stringify(dashboardAppendRequest(spec, target, this.selectedPageID, this.placementChoice)),
        idempotencyKey: window.LeapViewCommand.headers()['Idempotency-Key']!,
      }
      this.pendingAppend = attempt
    }
    const { target } = attempt
    this.saving = true
    this.status = ''
    this.successDashboardURL = ''
    this.refresh()
    try {
      const response = await fetch(attempt.endpoint, {
        method: 'POST',
        headers: { ...window.LeapViewCommand.headers('executeDashboardAuthoringCommand'), 'Idempotency-Key': attempt.idempotencyKey, 'Content-Type': 'application/json' },
        body: attempt.body,
      })
      if (!response.ok) {
        // Protocol conflicts can mean the original write is still unresolved.
        // Never turn those (or a conflict after a lost response) into a new add.
        const protocolConflict = response.status === 409 && response.headers.get('content-type')?.includes('application/problem+json')
        if (response.status === 409 && !attempt.uncertain && !protocolConflict) {
          this.pendingAppend = undefined
          if (attempt.modelID !== this.modelID) {
            this.status = 'The original dashboard changed; no tile was added.'
            return
          }
          await this.selectTarget(target.id, attempt.modelID)
          const refreshed = this.targets.find((item) => item.id === target.id)
          if (this.selectedDashboardID === target.id && refreshed?.draftId && refreshed.revisionToken) {
            this.status = 'Dashboard changed. The target was refreshed; review the page and retry.'
            this.refresh()
          }
          return
        }
        if (response.status >= 500 || response.status === 408 || response.status === 429 || response.status === 409 || attempt.uncertain) {
          throw new Error('The addition is not confirmed. Retry the previous addition to check its outcome without creating another tile.')
        }
        this.pendingAppend = undefined
        if (response.status === 403) throw new Error('You no longer have access to this dashboard or model.')
        if (response.status === 422) throw new Error('The selected fields or display settings are not supported for dashboard tiles. Review the exploration and try again.')
        throw new Error('Could not add this exploration to the selected dashboard.')
      }
      const result = await response.json() as { dashboardId?: string }
      if (result.dashboardId !== target.id) throw new Error('The dashboard update response was invalid.')
      this.pendingAppend = undefined
      this.status = 'Exploration added as an independent tile.'
      this.successDashboardURL = `/dashboards/${encodeURIComponent(target.id)}/edit?draft=${encodeURIComponent(target.draftId)}`
      this.targetsLoaded = false
      this.selectedDashboardID = ''
      this.selectedPageID = ''
    } catch (error) {
      if (this.pendingAppend === attempt) {
        attempt.uncertain = true
        this.status = 'The addition is not confirmed. Retry the previous addition to check its outcome without creating another tile.'
      } else {
        this.status = error instanceof Error ? error.message : 'Could not add this exploration to the selected dashboard.'
      }
    } finally {
      this.saving = false
      this.refresh()
    }
  }
}

export const dashboardAppendStyles = css`
  .dashboard-append-picker { position: relative; }
  .dashboard-append-picker > summary { list-style: none; cursor: pointer; }
  .dashboard-append-picker > summary::-webkit-details-marker { display: none; }
  .dashboard-append-picker-panel {
    box-sizing: border-box;
    position: static; display: grid; gap: var(--base-size-8); width: 100%; min-width: 0; max-width: none;
    padding: var(--base-size-12); border: var(--lv-border-default); border-radius: var(--lv-radius-default);
    color: var(--lv-fg-default); background: var(--lv-bg-overlay);
    box-shadow: none;
  }
  .dashboard-append-picker-panel label { display: grid; gap: 4px; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .dashboard-append-picker-panel select,
  .dashboard-append-picker-panel button {
    min-width: 0; max-width: 100%;
    min-height: 32px; padding: 4px 8px; border: var(--lv-border-muted); border-radius: var(--lv-radius-default);
    color: var(--lv-fg-default); background: var(--lv-bg-panel); font: var(--lv-type-caption);
  }
  .dashboard-append-picker-panel button { cursor: pointer; }
  .dashboard-append-picker-panel button:disabled { cursor: not-allowed; opacity: .55; }
  .dashboard-append-status { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .dashboard-append-status a { margin-left: 4px; }
`

export function renderDashboardAppendPicker(options: DashboardAppendPickerOptions): TemplateResult | typeof nothing {
  if (!options.enabled) return nothing
  const selected = options.targets.find((target) => target.id === options.selectedDashboardID)
  const pages = selected?.pages ?? []
  return html`
    <details class="dashboard-append-picker" @toggle=${(event: Event) => options.onToggle((event.currentTarget as HTMLDetailsElement).open)}>
      <summary class="text-button">${lucideIcon(LayoutDashboard, { size: 16 })}Add to dashboard<span class="dashboard-append-chevron">${lucideIcon(ChevronDown, { size: 14 })}</span></summary>
      <div class="dashboard-append-picker-panel" role="group" aria-label="Add exploration to dashboard">
        <label>Dashboard
          <select aria-label="Choose dashboard" ?disabled=${options.loading || options.saving || Boolean(options.pendingTitle)} @change=${(event: Event) => options.onDashboardChange((event.target as HTMLSelectElement).value)}>
            <option value="" ?selected=${!options.selectedDashboardID}>${options.loading ? 'Loading dashboards…' : 'Choose a dashboard'}</option>
            ${options.targets.map((target) => html`<option value=${target.id} ?selected=${target.id === options.selectedDashboardID}>${target.title}</option>`)}
          </select>
        </label>
        ${options.pendingTitle ? html`<span class="dashboard-append-status">Confirm the previous addition to ${options.pendingTitle} before adding another tile.</span>
          <button type="button" ?disabled=${options.saving} @click=${options.onAppend}>${options.saving ? 'Confirming…' : 'Retry previous addition'}</button>` : selected ? html`
          <label>Page
            <select aria-label="Choose dashboard page" ?disabled=${options.loading || options.saving} @change=${(event: Event) => options.onPageChange((event.target as HTMLSelectElement).value)}>
              <option value="" ?selected=${!options.selectedPageID}>Choose a page</option>
              ${pages.map((page) => html`<option value=${page.id} ?selected=${page.id === options.selectedPageID}>${page.title}</option>`)}
            </select>
          </label>
          <label>Tile width
            <select aria-label="Choose tile width" ?disabled=${options.saving} @change=${(event: Event) => options.onPlacementChange((event.target as HTMLSelectElement).value as 'half' | 'full')}>
              <option value="half" ?selected=${options.placementChoice === 'half'}>Half width</option><option value="full" ?selected=${options.placementChoice === 'full'}>Full width</option>
            </select>
          </label>
          <button type="button" ?disabled=${options.saving || !options.selectedPageID || !selected.revisionToken} @click=${options.onAppend}>${options.saving ? 'Adding…' : 'Add tile'}</button>
        ` : nothing}
        ${options.status ? html`<span class="dashboard-append-status" role="status">${options.status}${options.successDashboardURL ? html`<a href=${options.successDashboardURL}>Open dashboard</a>` : nothing}</span>` : nothing}
      </div>
    </details>
  `
}

export function dashboardAppendRequest(spec: ExplorationSpec, dashboard: DashboardAppendTarget, pageID: string, placementChoice: 'half' | 'full') {
  return {
    dashboardId: dashboard.id,
    pageId: pageID,
    revisionToken: dashboard.revisionToken,
    placementChoice,
    spec,
  }
}
