import { LitElement, html, nothing } from 'lit'
import { property } from 'lit/decorators.js'
import type { PipelineCommandSignal, PipelineListItemSignal, PipelineRunMonitorSignal, PipelineWaitingIntentSignal, RecordTableSignal } from '../../generated/signals'
import type { EntityListItem } from '../shared/entity-list'
import { capitalize, firstRunListValue, formatRunDetailDate, formatRunListDate, pipelineRunActionIcon, pipelineStatusLabel, runDetailValue, runStatusIcon, runStatusTone } from './pipelines-page-format'
import { displayRunDuration, hasLiveRunDuration } from './live-run-duration'
import './live-run-duration-element'
import '../shared/entity-list'

const runStatuses = ['queued', 'running', 'succeeded', 'failed', 'cancelled', 'superseded', 'skipped'] as const

/** Shared run history for the global monitor and a single pipeline. */
class PipelineRunsList extends LitElement {
  @property({ attribute: false }) monitor: PipelineRunMonitorSignal | null = null
  @property({ attribute: false }) table: RecordTableSignal = { columns: [], rows: [], empty: 'No runs match these filters.' }
  @property({ attribute: false }) pipelines: PipelineListItemSignal[] = []
  @property({ attribute: false }) waitingIntents: PipelineWaitingIntentSignal[] = []
  @property({ type: String }) baseHref = '/pipelines/runs'
  @property({ type: String }) fixedPipelineId = ''
  @property({ attribute: false }) pendingCommand: PipelineCommandSignal | null = null
  @property({ type: Boolean, reflect: true }) live = true

  createRenderRoot(): HTMLElement { return this }

  render() {
    const monitor = this.monitor ?? { query: '', range: '24h', pipeline: this.fixedPipelineId, status: '', trigger: '', page: 1, pageSize: 25, total: 0 }
    const fixed = Boolean(this.fixedPipelineId)
    const start = monitor.total > (monitor.page - 1) * monitor.pageSize ? (monitor.page - 1) * monitor.pageSize + 1 : 0
    const end = Math.min(monitor.total, monitor.page * monitor.pageSize)
    const requests = this.requestItems(this.table, monitor, fixed)
    const items = this.activityItems(requests, this.runItems(this.table, fixed))
    return html`
      <style>${pipelineRunsListStyles}</style>
      <form class=${fixed ? 'run-filters' : 'run-toolbar'} method="get" action=${this.baseHref} aria-label="Run history filters">
        <input type="search" name="q" placeholder=${fixed ? 'Search run or request ID' : 'Search pipeline or ID'} aria-label="Search pipeline runs" .value=${monitor.query}>
        <select name="range" aria-label="Filter runs by time range" .value=${monitor.range} @change=${this.submitFilters}>
          <option value="24h" ?selected=${monitor.range === '24h'}>Last 24 hours</option><option value="7d" ?selected=${monitor.range === '7d'}>Last 7 days</option><option value="30d" ?selected=${monitor.range === '30d'}>Last 30 days</option><option value="all" ?selected=${monitor.range === 'all'}>All time</option>
        </select>
        ${fixed ? nothing : html`<select name="pipeline" aria-label="Filter runs by pipeline" .value=${monitor.pipeline} @change=${this.submitFilters}>
          <option value="" ?selected=${monitor.pipeline === ''}>All pipelines</option>
          ${this.pipelines.map((pipeline) => html`<option value=${pipeline.pipelineId} ?selected=${pipeline.pipelineId === monitor.pipeline}>${pipeline.title}</option>`)}
        </select>`}
        <select name="status" aria-label="Filter runs by status" .value=${monitor.status} @change=${this.submitFilters}>
          <option value="" ?selected=${monitor.status === ''}>All statuses</option>
          ${runStatuses.map((status) => html`<option value=${status} ?selected=${status === monitor.status}>${pipelineStatusLabel(status)}</option>`)}
        </select>
        <select name="trigger" aria-label="Filter runs by trigger" .value=${monitor.trigger} @change=${this.submitFilters}>
          <option value="" ?selected=${monitor.trigger === ''}>All triggers</option>
          ${['manual', 'schedule'].map((trigger) => html`<option value=${trigger} ?selected=${trigger === monitor.trigger}>${capitalize(trigger)}</option>`)}
        </select>
        <button type="submit">Search</button>${this.hasFilters(monitor, fixed) ? html`<a href=${this.baseHref}>Clear filters</a>` : nothing}
      </form>
      <div class=${`run-table ${fixed ? 'is-fixed' : 'is-global'}`}>
        <lv-entity-list
          compact
          title-emphasis="normal"
          row-action=${fixed ? nothing : 'detail'}
          min-width=${fixed ? '950px' : '1050px'}
          list-label="Pipeline runs and requests"
          empty-text="No runs match these filters."
          .showToolbar=${false}
          .items=${items}
          .columns=${fixed ? [
            { id: 'time', label: 'Start time', width: '180px' },
            { id: 'status', label: 'Status', width: '130px', render: 'status' },
            { id: 'duration', label: 'Duration', width: '100px' },
            { id: 'trigger', label: 'Trigger', width: '100px' },
            { id: 'name', label: 'Run / request ID', width: '200px' },
            { id: 'actions', label: '', width: '90px', sortable: false, render: 'actions' },
          ] : [
            { id: 'time', label: 'Start time', width: '180px' },
            { id: 'pipeline', label: 'Pipeline', width: '180px' },
            { id: 'status', label: 'Status', width: '120px', render: 'status' },
            { id: 'duration', label: 'Duration', width: '90px' },
            { id: 'trigger', label: 'Trigger', width: '100px' },
            { id: 'name', label: 'Run / request ID', width: '180px' },
            { id: 'actions', label: '', width: '90px', sortable: false, render: 'actions' },
          ]}
          @lv-entity-list-row-action=${this.handleRequestAction}
        ></lv-entity-list>
      </div>
      ${this.monitor || fixed ? html`<nav class="run-pagination" aria-label="Run pages">
        <span>${requests.length ? `${requests.length} ${requests.length === 1 ? 'request' : 'requests'} · ` : nothing}Showing ${start}–${end} of ${monitor.total} runs</span>
        <div class="run-pagination-actions">
          ${monitor.page > 1 ? html`<a href=${this.pageHref(monitor, monitor.page - 1, fixed)}>Previous</a>` : nothing}
          ${end < monitor.total ? html`<a href=${this.pageHref(monitor, monitor.page + 1, fixed)}>Next</a>` : nothing}
        </div>
      </nav>` : nothing}
    `
  }

  private submitFilters = (event: Event): void => { (event.currentTarget as HTMLSelectElement).form?.requestSubmit() }

  private hasFilters(monitor: PipelineRunMonitorSignal, fixed: boolean): boolean {
    return Boolean(monitor.query || (!fixed && monitor.pipeline) || monitor.status || monitor.trigger || (monitor.range && monitor.range !== '24h'))
  }

  private pageHref(monitor: PipelineRunMonitorSignal, page: number, fixed: boolean): string {
    const params = new URLSearchParams({ q: monitor.query, range: monitor.range })
    if (!fixed) params.set('pipeline', monitor.pipeline)
    params.set('status', monitor.status)
    params.set('trigger', monitor.trigger)
    params.set('page', String(page))
    return `${this.baseHref}?${params}`
  }

  private activityItems(requests: EntityListItem[], runs: EntityListItem[]): EntityListItem[] {
    const rank = (item: EntityListItem): number => {
      const status = item.sortValues?.status
      if (status === 'queued') return 0
      if (status === 'running' || status === 'prepared') return 1
      return 2
    }
    return [...requests, ...runs].sort((left, right) => {
      const group = rank(left) - rank(right)
      if (group) return group
      if (rank(left) === 0) {
        const position = Number(right.sortValues?.queuePosition ?? 0) - Number(left.sortValues?.queuePosition ?? 0)
        if (position) return position
      }
      return Number(right.sortValues?.activityTime ?? 0) - Number(left.sortValues?.activityTime ?? 0)
    })
  }

  private runItems(table: RecordTableSignal, fixed: boolean): EntityListItem[] {
    return table.rows.map((row) => {
      const status = runDetailValue(row, 'status_value')
      const href = runDetailValue(row, 'run_href')
      const pipelineHref = runDetailValue(row, 'pipeline_href')
      const startedAt = String(row.started_at || '')
      const createdAt = String(row.created_at || '')
      const hasStarted = status !== 'queued' && Boolean(startedAt && startedAt !== '-')
      const timeAt = hasStarted ? startedAt : createdAt && createdAt !== '-' ? createdAt : ''
      const timeLabel = hasStarted ? formatRunListDate(startedAt) : ''
      const finishedAt = String(row.finished_at || '')
      const recordedDuration = runDetailValue(row, 'duration')
      const counting = hasLiveRunDuration(status, startedAt, finishedAt)
      const columnHrefs: Record<string, string> = {}
      if (!fixed && pipelineHref !== '—') columnHrefs.pipeline = pipelineHref
      return {
        id: String(row.run_id || row.id || ''),
        title: firstRunListValue(row.run, row.run_id, row.id),
        href: fixed && href !== '—' ? href : undefined,
        icon: 'none',
        columnHrefs,
        columnContent: {
          time: this.timeContent(timeLabel, status, href === '—' ? '' : href),
          ...(counting ? { duration: html`<lv-live-run-duration .status=${status} .startedAt=${startedAt} .finishedAt=${finishedAt} .recordedDuration=${recordedDuration} .live=${this.live}></lv-live-run-duration>` } : {}),
        },
        columns: {
          time: timeLabel,
          status: pipelineStatusLabel(status),
          ...(!fixed ? { pipeline: runDetailValue(row, 'pipeline') } : {}),
          duration: displayRunDuration(status, startedAt, finishedAt, recordedDuration, Date.now()),
          trigger: runDetailValue(row, 'trigger'),
        },
        columnTitles: { time: hasStarted ? formatRunDetailDate(startedAt) : '', ...(counting ? { duration: '' } : {}) },
        sortValues: { status, time: hasStarted ? startedAt : '', activityTime: Date.parse(timeAt) || 0 },
        actions: fixed || !Array.isArray(row.actions) ? [] : row.actions
          .filter((action) => !['detail', 'run'].includes(String((action as Record<string, unknown>).action || '')))
          .map((action) => {
            const value = action as Record<string, unknown>
            return {
              label: String(value.label || ''),
              action: String(value.action || ''),
              icon: pipelineRunActionIcon(String(value.icon || '')),
              disabled: value.action !== 'detail' && this.commandPendingFor(String(row.pipeline_id || ''), String(row.run_id || '')),
            }
          }),
      }
    })
  }

  private requestItems(table: RecordTableSignal, monitor: PipelineRunMonitorSignal, fixed: boolean): EntityListItem[] {
    if (monitor.page !== 1 || (monitor.trigger && monitor.trigger !== 'manual')) return []
    const visibleRunIDs = new Set(table.rows.map((row) => `${String(row.pipeline_id || '')}\u0000${String(row.run_id || '')}`))
    const query = monitor.query.trim().toLocaleLowerCase()
    const rangeDays = monitor.range === '24h' ? 1 : monitor.range === '7d' ? 7 : monitor.range === '30d' ? 30 : 0
    const cutoff = rangeDays ? Date.now() - rangeDays * 24 * 60 * 60 * 1000 : 0
    return this.waitingIntents.filter((intent) => {
      if (this.fixedPipelineId && intent.pipelineId !== this.fixedPipelineId) return false
      if (monitor.pipeline && intent.pipelineId !== monitor.pipeline) return false
      const status = intent.status.toLowerCase()
      if (status === 'attached' && intent.runId && visibleRunIDs.has(`${intent.pipelineId}\u0000${intent.runId}`)) return false
      if (!['waiting', 'claimed', 'attached', 'stale'].includes(status)) return false
      if (monitor.status && !(monitor.status === 'queued' && status !== 'stale')) return false
      if (cutoff && (!intent.createdAt || Date.parse(intent.createdAt) < cutoff)) return false
      if (query) {
        const title = this.pipelines.find((pipeline) => pipeline.pipelineId === intent.pipelineId)?.title ?? ''
        if (![intent.intentId, intent.runId ?? '', intent.pipelineId, title].some((value) => value.toLocaleLowerCase().includes(query))) return false
      }
      return true
    }).map((intent): EntityListItem => {
      const status = intent.status.toLowerCase()
      const stale = status === 'stale'
      const pipeline = this.pipelines.find((candidate) => candidate.pipelineId === intent.pipelineId)
      const reason = stale ? intent.reason || 'Pipeline definition changed while waiting; start a new request' : ''
      const pending = this.pendingCommand?.action === 'cancel-intent' && this.pendingCommand.intentId === intent.intentId
      return {
        id: `request:${intent.intentId}`,
        title: `Request ${intent.intentId.length > 18 ? `${intent.intentId.slice(0, 8)}…${intent.intentId.slice(-6)}` : intent.intentId}`,
        rowActionDisabled: true,
        icon: 'none',
        columns: {
          time: '',
          status: stale ? 'Stale request' : 'Queued',
          ...(!fixed ? { pipeline: pipeline?.title ?? intent.pipelineId } : {}),
          duration: '—',
          trigger: 'Manual',
        },
        columnContent: { time: this.timeContent('', stale ? 'failed' : 'queued') },
        columnDescriptions: stale ? { status: reason } : undefined,
        columnTitles: { time: '' },
        columnHrefs: !fixed && pipeline ? { pipeline: pipeline.href } : undefined,
        sortValues: { status: stale ? 'stale' : 'queued', time: '', queuePosition: intent.queuePosition ?? 0, activityTime: Date.parse(intent.createdAt) || 0 },
        actions: intent.cancelAllowed && !stale ? [{ label: `Cancel queued request ${intent.intentId}`, action: 'cancel-intent', icon: 'cancel', disabled: pending }] : [],
      }
    })
  }

  private timeContent(label: string, status: string, href = '') {
    if (!label) return status === 'queued' ? html`<span class="run-time-status is-accent" aria-hidden="true">${runStatusIcon(status)}</span>` : html``
    const tone = status === 'queued' ? 'accent' : runStatusTone(status)
    const active = status === 'running' || status === 'prepared'
    return html`<span class="run-time-value">
      <span class=${`run-time-status is-${tone}${active ? ' is-running' : ''}`} aria-hidden="true">${runStatusIcon(status)}</span>
      ${href ? html`<a class="entity-list-column-link" href=${href} @click=${(event: Event) => event.stopPropagation()}>${label}</a>` : html`<span>${label}</span>`}
    </span>`
  }

  private handleRequestAction = (event: CustomEvent<{ action: string, item: EntityListItem }>): void => {
    if (event.detail.action !== 'cancel-intent') return
    event.stopPropagation()
    const intent = this.waitingIntents.find((candidate) => `request:${candidate.intentId}` === event.detail.item.id)
    if (!intent) return
    this.dispatchEvent(new CustomEvent('lv-pipeline-command', {
      bubbles: true,
      composed: true,
      detail: { action: 'cancel-intent', pipelineId: intent.pipelineId, assetId: intent.pipelineId, intentId: intent.intentId, runId: '' },
    }))
  }

  private commandPendingFor(pipelineId: string, runId: string): boolean {
    const command = this.pendingCommand
    return Boolean(command && command.pipelineId === pipelineId && command.runId === runId)
  }
}

const pipelineRunsListStyles = `
  lv-pipeline-runs-list { display: grid; min-width: 0; gap: var(--base-size-16); }
  lv-pipeline-runs-list .run-toolbar, lv-pipeline-runs-list .run-filters { display: flex; min-width: 0; flex-wrap: wrap; gap: var(--base-size-8); }
  lv-pipeline-runs-list input, lv-pipeline-runs-list select { box-sizing: border-box; height: var(--control-medium-size); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding: 0 var(--base-size-8); font: var(--lv-type-body); }
  lv-pipeline-runs-list input { width: min(100%, 19rem); min-width: 12rem; padding-inline: var(--base-size-12); }
  lv-pipeline-runs-list input:focus-visible, lv-pipeline-runs-list select:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
  lv-pipeline-runs-list .run-toolbar button, lv-pipeline-runs-list .run-toolbar a, lv-pipeline-runs-list .run-filters button, lv-pipeline-runs-list .run-filters a, lv-pipeline-runs-list .run-pagination a { display: inline-flex; align-items: center; min-height: var(--control-medium-size); box-sizing: border-box; padding: 0 var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); font: var(--lv-type-body); text-decoration: none; cursor: pointer; }
  lv-pipeline-runs-list .run-table { min-width: 0; }
  lv-pipeline-runs-list .entity-list-status { font-weight: var(--base-text-weight-normal); }
  lv-pipeline-runs-list .run-time-value { display: inline-flex; max-width: 100%; align-items: center; }
  lv-pipeline-runs-list .run-time-status { display: none; width: var(--base-size-16); height: var(--base-size-16); flex: none; margin-right: var(--base-size-6); }
  lv-pipeline-runs-list .run-time-status svg { display: block; width: var(--base-size-16); height: var(--base-size-16); }
  lv-pipeline-runs-list .run-time-status.is-accent { color: var(--lv-fg-accent); }
  lv-pipeline-runs-list .run-time-status.is-attention { color: var(--lv-fg-warning); }
  lv-pipeline-runs-list .run-time-status.is-success { color: var(--lv-fg-success); }
  lv-pipeline-runs-list .run-time-status.is-danger { color: var(--lv-fg-danger); }
  lv-pipeline-runs-list .run-time-status.is-muted { color: var(--lv-fg-muted); }
  lv-pipeline-runs-list:not([live]) .entity-list-status.is-running svg { animation: none; }
  lv-pipeline-runs-list .run-pagination { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-8); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  lv-pipeline-runs-list .run-pagination-actions { display: flex; gap: var(--base-size-8); }
  @media (max-width: 720px) { lv-pipeline-runs-list input { width: 100%; } lv-pipeline-runs-list .run-pagination { align-items: flex-start; flex-direction: column; } lv-pipeline-runs-list .run-table.is-global .run-time-status { display: inline-flex; } lv-pipeline-runs-list[live] .run-time-status.is-running svg { animation: entity-list-status-spin 1s linear infinite; } }
  @media (prefers-reduced-motion: reduce) { lv-pipeline-runs-list .run-time-status.is-running svg { animation: none !important; } }
`

if (!customElements.get('lv-pipeline-runs-list')) customElements.define('lv-pipeline-runs-list', PipelineRunsList)
