import { LitElement, html, nothing } from 'lit'
import { property } from 'lit/decorators.js'
import type { PipelineListItemSignal, PipelineRunMonitorSignal, RecordTableSignal } from '../../generated/signals'
import type { EntityListItem } from '../shared/entity-list'
import { capitalize, firstRunListValue, formatRunDetailDate, formatRunListDate, pipelineRunActionIcon, pipelineStatusLabel, runDetailValue, shortFailureReason } from './pipelines-page-format'
import '../shared/entity-list'

const runStatuses = ['queued', 'running', 'prepared', 'succeeded', 'failed', 'cancelled', 'superseded', 'skipped'] as const

/** Shared run history for the global monitor and a single pipeline. */
class PipelineRunsList extends LitElement {
  @property({ attribute: false }) monitor: PipelineRunMonitorSignal | null = null
  @property({ attribute: false }) table: RecordTableSignal = { columns: [], rows: [], empty: 'No runs match these filters.' }
  @property({ attribute: false }) pipelines: PipelineListItemSignal[] = []
  @property({ type: String }) baseHref = '/pipelines/runs'
  @property({ type: String }) fixedPipelineId = ''
  @property({ attribute: false }) pendingCommand: { pipelineId: string, runId: string } | null = null

  createRenderRoot(): HTMLElement { return this }

  render() {
    const monitor = this.monitor ?? { query: '', range: '24h', pipeline: this.fixedPipelineId, status: '', trigger: '', page: 1, pageSize: 25, total: 0 }
    const fixed = Boolean(this.fixedPipelineId)
    const start = monitor.total > (monitor.page - 1) * monitor.pageSize ? (monitor.page - 1) * monitor.pageSize + 1 : 0
    const end = Math.min(monitor.total, monitor.page * monitor.pageSize)
    return html`
      <style>${pipelineRunsListStyles}</style>
      <form class=${fixed ? 'run-filters' : 'run-toolbar'} method="get" action=${this.baseHref} aria-label="Run history filters">
        <input type="search" name="q" placeholder=${fixed ? 'Search run ID' : 'Search pipeline or run ID'} aria-label="Search pipeline runs" .value=${monitor.query}>
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
      <div class="run-table">
        <lv-entity-list
          compact
          title-emphasis="normal"
          row-action=${fixed ? nothing : 'detail'}
          min-width=${fixed ? '950px' : '1050px'}
          list-label="Pipeline run history"
          empty-text="No runs match these filters."
          .showToolbar=${false}
          .items=${this.runItems(this.table, fixed)}
          .columns=${fixed ? [
            { id: 'status', label: 'Status', width: '130px', render: 'status' },
            { id: 'name', label: 'Run ID', width: '200px' },
            { id: 'started', label: 'Started', width: '170px' },
            { id: 'duration', label: 'Duration', width: '100px' },
            { id: 'trigger', label: 'Trigger', width: '100px' },
          ] : [
            { id: 'status', label: 'Status', width: '120px', render: 'status' },
            { id: 'name', label: 'Run ID', width: '180px' },
            { id: 'started', label: 'Started', width: '155px' },
            { id: 'pipeline', label: 'Pipeline', width: '180px' },
            { id: 'duration', label: 'Duration', width: '90px' },
            { id: 'trigger', label: 'Trigger', width: '100px' },
            { id: 'actions', label: '', width: '90px', sortable: false, render: 'actions' },
          ]}
        ></lv-entity-list>
      </div>
      ${this.monitor || fixed ? html`<nav class="run-pagination" aria-label="Run pages">
        <span>Showing ${start}–${end} of ${monitor.total} runs</span>
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

  private runItems(table: RecordTableSignal, fixed: boolean): EntityListItem[] {
    return table.rows.map((row) => {
      const status = runDetailValue(row, 'status_value')
      const href = runDetailValue(row, 'run_href')
      const pipelineHref = runDetailValue(row, 'pipeline_href')
      const startedAt = String(row.started_at || '')
      const failure = status === 'failed' && runDetailValue(row, 'error') !== '—' ? shortFailureReason(runDetailValue(row, 'error')) : ''
      const columnHrefs: Record<string, string> | undefined = fixed
        ? href !== '—' ? { started: href } : undefined
        : { started: href === '—' ? '#' : href, pipeline: pipelineHref === '—' ? '#' : pipelineHref }
      return {
        id: String(row.run_id || row.id || ''),
        title: firstRunListValue(row.run, row.run_id, row.id),
        href: fixed && href !== '—' ? href : undefined,
        icon: 'none',
        columnHrefs,
        columnDescriptions: failure ? { [fixed ? 'status' : 'pipeline']: failure } : undefined,
        columns: {
          status: pipelineStatusLabel(status),
          started: formatRunListDate(startedAt),
          ...(!fixed ? { pipeline: runDetailValue(row, 'pipeline') } : {}),
          duration: runDetailValue(row, 'duration'),
          trigger: runDetailValue(row, 'trigger'),
        },
        columnTitles: { started: formatRunDetailDate(startedAt) },
        sortValues: { status, started: startedAt },
        actions: fixed || !Array.isArray(row.actions) ? [] : row.actions
          .filter((action) => (action as Record<string, unknown>).action !== 'detail')
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
  lv-pipeline-runs-list .run-pagination { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-8); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  lv-pipeline-runs-list .run-pagination-actions { display: flex; gap: var(--base-size-8); }
  @media (max-width: 720px) { lv-pipeline-runs-list input { width: 100%; } lv-pipeline-runs-list .run-pagination { align-items: flex-start; flex-direction: column; } }
`

if (!customElements.get('lv-pipeline-runs-list')) customElements.define('lv-pipeline-runs-list', PipelineRunsList)
