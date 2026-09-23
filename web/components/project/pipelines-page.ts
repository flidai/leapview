import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import type { PipelineCommandSignal, PipelineCommandStatusSignal, PipelineListItemSignal, PipelinePageSignal, PipelineRunMonitorSignal, RecordTableSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure, ownsBrowserCommandFetch, type BrowserCommandFailure } from '../shared/command-failure'
import { checkSignalContract } from '../shared/signal-contract'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import type { EntityListItem } from '../shared/entity-list'
import { capitalize, commandLoadingLabel, firstRunListValue, formatDateTime, formatExactDateTime, formatRunDetailDate, formatRunListDate, pipelineRunActionIcon, pipelineStatusLabel, runDetailValue, runMonitorPageHref, shortFailureReason } from './pipelines-page-format'
import '../shared/entity-list'

const pipelineRunStatuses = ['queued', 'running', 'prepared', 'succeeded', 'failed', 'cancelled', 'superseded', 'skipped'] as const

class LeapViewPipelinesPage extends DatastarLit(LitElement) {
  @state() private terminalFailure: BrowserCommandFailure | null = null
  @state() private streamState: 'live' | 'reconnecting' | 'lost' = 'live'

  static styles = [pageHeaderStyles, css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .page {
      box-sizing: border-box;
      display: grid;
      width: min(100%, var(--lv-page-content-max-width));
      min-width: 0;
      min-height: 100svh;
      align-content: start;
      gap: var(--base-size-16);
      margin-inline: auto;
      padding: var(--base-size-24);
    }

    .pipeline-tabs {
      display: flex;
      gap: var(--base-size-20);
      border-bottom: var(--lv-border-muted);
    }

    .pipeline-tabs a {
      display: inline-flex;
      min-height: var(--control-medium-size);
      align-items: center;
      border-bottom: 2px solid transparent;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
      text-decoration: none;
    }

    .pipeline-tabs a:hover,
    .pipeline-tabs a:focus-visible { color: var(--lv-fg-default); }

    .pipeline-tabs a[aria-current='page'] {
      border-bottom-color: var(--lv-fg-accent, var(--lv-fg-default));
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
    }

    .runs {
      display: grid;
      min-width: 0;
      gap: var(--base-size-16);
    }

    .command-feedback {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      padding: var(--base-size-8) var(--base-size-12);
      font: var(--lv-type-body);
    }

    .command-feedback.is-error {
      border-color: var(--lv-border-danger, var(--lv-border-muted));
      color: var(--lv-fg-danger);
    }

    .command-feedback-actions { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-8); margin-top: var(--base-size-8); }
    .command-feedback-actions button { border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding: var(--base-size-4) var(--base-size-8); cursor: pointer; font: var(--lv-type-caption); }

    .run-toolbar {
      display: flex;
      min-width: 0;
      flex-wrap: wrap;
      gap: var(--base-size-8);
    }

    .run-toolbar input,
    .run-toolbar select {
      box-sizing: border-box;
      height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      padding: 0 var(--base-size-8);
      font: var(--lv-type-body);
    }

    .run-toolbar input {
      width: min(100%, 19rem);
      min-width: 12rem;
      padding-inline: var(--base-size-12);
    }

    .run-toolbar input:focus-visible,
    .run-toolbar select:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .run-toolbar button, .run-toolbar a, .run-pagination a {
      display: inline-flex;
      align-items: center;
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      padding: 0 var(--base-size-12);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      text-decoration: none;
      cursor: pointer;
    }

    .run-pagination { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-8); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .run-pagination-actions { display: flex; gap: var(--base-size-8); }
    .run-table {
      min-width: 0;
    }

    @media (max-width: 720px) {
      .page { padding: var(--base-size-12); }
      .run-toolbar input { width: 100%; }
    }
  `]

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
    document.addEventListener('datastar-signal-patch', this.handleStreamPatch)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
    document.removeEventListener('datastar-signal-patch', this.handleStreamPatch)
    super.disconnectedCallback()
  }

  get page(): PipelinePageSignal | null {
    return this.signal<PipelinePageSignal | null>('page', null)
  }

  get command(): PipelineCommandSignal {
    return this.signal<PipelineCommandSignal>('pipelineCommand', { action: '', assetId: '', pipelineId: '', runId: '' })
  }

  get commandStatus(): PipelineCommandStatusSignal {
    return this.signal<PipelineCommandStatusSignal>('pipelineCommandStatus', { loading: false, error: '', message: '' })
  }

  updated(): void {
    checkSignalContract('pipelines page', this.page, { kind: 'required', pipelines: 'required', runsTable: 'required' })
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    return html`
      <section class="page" aria-label="LeapView pipelines">
        ${renderPageHeader(page.title)}
        ${this.renderTabs(page.activeTab)}
        ${page.activeTab === 'runs' && this.streamState !== 'live' ? html`<p class="command-feedback is-error" role="alert">${this.streamState === 'reconnecting' ? 'Reconnecting to run updates…' : 'Run updates disconnected. Reload to see the latest state.'}</p>` : nothing}
        ${this.renderCommandFeedback()}
        ${page.activeTab === 'runs' ? this.renderRuns(page) : this.renderPipelines(page)}
      </section>
    `
  }

  private renderTabs(activeTab: string) {
    return html`<nav class="pipeline-tabs" aria-label="Pipeline views">
      <a href="/pipelines" aria-current=${activeTab === 'pipelines' ? 'page' : nothing}>Pipelines</a>
      <a href="/pipelines/runs" aria-current=${activeTab === 'runs' ? 'page' : nothing}>Runs</a>
    </nav>`
  }

  private renderPipelines(page: PipelinePageSignal) {
    return html`
      <lv-entity-list
        mobile-cards
        client-filter
        list-label="Refresh pipelines"
        search-placeholder="Search pipelines"
        empty-text="No refresh pipelines are available."
        min-width="960px"
        .items=${this.pipelineItems(page.pipelines)}
        .columns=${[
          { id: 'name', label: 'Pipeline', width: '240px' },
          { id: 'semanticModel', label: 'Refreshes', width: '180px' },
          { id: 'schedule', label: 'Schedule', width: '200px' },
          { id: 'status', label: 'Latest run', width: '120px', render: 'status' },
          { id: 'lastPublished', label: 'Last published', width: '160px' },
          { id: 'actions', label: 'Run now', width: '60px', sortable: false, render: 'actions' },
        ]}
        @lv-entity-list-row-action=${this.handlePipelineAction}
      ></lv-entity-list>
    `
  }

  private pipelineItems(pipelines: PipelineListItemSignal[]): EntityListItem[] {
    return pipelines.map((pipeline) => {
      const status = pipeline.status === 'never run' || pipeline.status === 'not refreshed' ? 'Never run' : pipelineStatusLabel(pipeline.status)
      const published = pipeline.lastPublishedAt
        ? formatDateTime(pipeline.lastPublishedAt)
        : pipeline.publicationStatus === 'unavailable' ? 'Unavailable' : status === 'Never run' ? 'Never published' : '—'
      const schedule = pipeline.schedule === 'Manual only' || pipeline.schedule === 'manual' ? 'Manual' : pipeline.schedule
      return {
        id: pipeline.pipelineId,
        title: pipeline.title,
        href: pipeline.href,
        icon: 'pipeline',
        columns: { semanticModel: pipeline.semanticModel, schedule, status, lastPublished: published },
        columnTitles: { lastPublished: formatExactDateTime(pipeline.lastPublishedAt) },
        columnDescriptions: pipeline.nextRun ? { schedule: `Next ${formatExactDateTime(pipeline.nextRun)}` } : undefined,
        columnHrefs: pipeline.latestRunHref ? { status: pipeline.latestRunHref } : undefined,
        columnLinkLabels: pipeline.latestRunHref ? { status: `Open latest run for ${pipeline.title} (${status})` } : undefined,
        sortValues: { lastPublished: pipeline.lastPublishedAt ? Date.parse(pipeline.lastPublishedAt) || 0 : 0 },
        actions: pipeline.canRun ? [{ label: `Run ${pipeline.title} now`, action: 'run', icon: 'play', disabled: pipeline.running || this.commandPendingFor(pipeline.pipelineId) }] : [],
      }
    })
  }

  private handlePipelineAction = (event: CustomEvent<{ action: string, item: EntityListItem }>): void => {
    if (event.detail.action !== 'run') return
    const pipeline = this.page?.pipelines.find((candidate) => candidate.pipelineId === event.detail.item.id)
    if (pipeline) this.runPipeline(pipeline)
  }

  private renderRuns(page: PipelinePageSignal) {
    return html`
      <div class="runs">
        <form class="run-toolbar" method="get" action="/pipelines/runs" aria-label="Run history filters">
          <input type="search" name="q" placeholder="Search pipeline or run ID" aria-label="Search pipeline runs" .value=${page.runMonitor?.query || ''}>
          <select name="range" aria-label="Filter runs by time range" .value=${page.runMonitor?.range || '24h'} @change=${this.submitRunFilters}>
            <option value="24h">Last 24 hours</option><option value="7d">Last 7 days</option><option value="30d">Last 30 days</option><option value="all">All time</option>
          </select>
          <select name="pipeline" aria-label="Filter runs by pipeline" .value=${page.runMonitor?.pipeline || ''} @change=${this.submitRunFilters}>
            <option value="">All pipelines</option>
            ${page.pipelines.map((pipeline) => html`<option value=${pipeline.pipelineId} ?selected=${pipeline.pipelineId === page.runMonitor?.pipeline}>${pipeline.title}</option>`)}
          </select>
          <select name="status" aria-label="Filter runs by status" .value=${page.runMonitor?.status || ''} @change=${this.submitRunFilters}>
            <option value="">All statuses</option>
            ${pipelineRunStatuses.map((status) => html`<option value=${status} ?selected=${status === page.runMonitor?.status}>${pipelineStatusLabel(status)}</option>`)}
          </select>
          <select name="trigger" aria-label="Filter runs by trigger" .value=${page.runMonitor?.trigger || ''} @change=${this.submitRunFilters}>
            <option value="">All triggers</option>
            ${['manual', 'schedule'].map((trigger) => html`<option value=${trigger} ?selected=${trigger === page.runMonitor?.trigger}>${capitalize(trigger)}</option>`)}
          </select>
          <button type="submit">Search</button>${this.hasRunFilters(page.runMonitor) ? html`<a href="/pipelines/runs">Clear filters</a>` : nothing}
        </form>
        <div class="run-table">
          <lv-entity-list
            compact
            title-emphasis="normal"
            row-action="detail"
            min-width="1050px"
            list-label="Pipeline run history"
            empty-text="No runs match these filters."
            .showToolbar=${false}
            .items=${this.runItems(page.runsTable)}
            .columns=${[
              { id: 'status', label: 'Status', width: '120px', render: 'status' },
              { id: 'name', label: 'Run ID', width: '180px' },
              { id: 'started', label: 'Started', width: '155px' },
              { id: 'pipeline', label: 'Pipeline', width: '180px' },
              { id: 'duration', label: 'Duration', width: '90px' },
              { id: 'trigger', label: 'Trigger', width: '100px' },
              { id: 'actions', label: '', width: '90px', sortable: false, render: 'actions' },
            ]}
            @lv-entity-list-row-action=${this.handleRunAction}
          ></lv-entity-list>
        </div>
        ${page.runMonitor ? this.renderRunPagination(page.runMonitor) : nothing}
      </div>
    `
  }

  private submitRunFilters = (event: Event): void => { (event.currentTarget as HTMLSelectElement).form?.requestSubmit() }

  private hasRunFilters(monitor?: PipelineRunMonitorSignal): boolean {
    return Boolean(monitor && (monitor.query || monitor.pipeline || monitor.status || monitor.trigger || (monitor.range && monitor.range !== '24h')))
  }

  private renderRunPagination(monitor: PipelineRunMonitorSignal) {
    const start = monitor.total > (monitor.page - 1) * monitor.pageSize ? (monitor.page - 1) * monitor.pageSize + 1 : 0
    const end = Math.min(monitor.total, monitor.page * monitor.pageSize)
    return html`<nav class="run-pagination" aria-label="Run pages">
      <span>Showing ${start}–${end} of ${monitor.total} runs</span>
      <div class="run-pagination-actions">
        ${monitor.page > 1 ? html`<a href=${runMonitorPageHref(monitor, monitor.page - 1)}>Previous</a>` : nothing}
        ${end < monitor.total ? html`<a href=${runMonitorPageHref(monitor, monitor.page + 1)}>Next</a>` : nothing}
      </div>
    </nav>`
  }

  private runItems(table: RecordTableSignal): EntityListItem[] {
    return table.rows
      .map((row) => ({
        id: String(row.run_id || row.id || ''),
        title: firstRunListValue(row.run, row.run_id, row.id),
        description: undefined,
        columnHrefs: {
          started: runDetailValue(row, 'run_href') === '—' ? '#' : runDetailValue(row, 'run_href'),
          pipeline: runDetailValue(row, 'pipeline_href') === '—' ? '#' : runDetailValue(row, 'pipeline_href'),
        },
        columnDescriptions: runDetailValue(row, 'status_value') === 'failed' && runDetailValue(row, 'error') !== '—' ? { pipeline: shortFailureReason(runDetailValue(row, 'error')) } : undefined,
        icon: 'none',
        columns: {
          status: pipelineStatusLabel(runDetailValue(row, 'status_value')),
          started: formatRunListDate(row.started_at),
          pipeline: runDetailValue(row, 'pipeline'),
          duration: runDetailValue(row, 'duration'),
          trigger: runDetailValue(row, 'trigger'),
        },
        columnTitles: {
          started: formatRunDetailDate(row.started_at),
        },
        sortValues: {
          status: runDetailValue(row, 'status_value'),
          started: String(row.started_at || ''),
        },
        actions: Array.isArray(row.actions)
          ? row.actions.filter((action) => (action as Record<string, unknown>).action !== 'detail').map((action) => {
              const value = action as Record<string, unknown>
              return {
                label: String(value.label || ''),
                action: String(value.action || ''),
                icon: pipelineRunActionIcon(String(value.icon || '')),
                disabled: value.action !== 'detail' && this.commandPendingFor(String(row.pipeline_id || ''), String(row.run_id || '')),
              }
            })
          : [],
      }))
  }

  private renderCommandFeedback() {
    const status = this.commandStatus
    const failure = this.terminalFailure
    if (!failure && !status.loading && !status.error && !status.message) return nothing
    const message = failure?.message || (status.loading ? commandLoadingLabel(this.command.action) : status.error || status.message)
    return html`
      <div class=${`command-feedback ${failure || status.error ? 'is-error' : ''}`} role=${failure || status.error ? 'alert' : 'status'} aria-live=${failure || status.error ? 'assertive' : 'polite'}>
        <div>${message}</div>
        ${failure ? html`
          <div class="command-feedback-actions">
            <span>Pipeline state was kept.</span>
            <button type="button" @click=${this.reloadAfterFailure}>Reload latest pipeline state</button>
          </div>
        ` : nothing}
      </div>
    `
  }

  private commandPendingFor(pipelineId: string, runId = ''): boolean {
    const command = this.command
    return this.commandStatus.loading && !this.terminalFailure && command.pipelineId === pipelineId && (!runId || command.runId === runId)
  }

  private runPipeline(pipeline: PipelineListItemSignal): void {
    if (!pipeline.canRun || pipeline.running || this.commandPendingFor(pipeline.pipelineId)) return
    this.emitCommand('run', pipeline, '')
  }

  private handleRunAction = (event: CustomEvent<{ action: string, item: EntityListItem }>): void => {
    const action = event.detail.action
    const row = this.page?.runsTable.rows.find((candidate) => String(candidate.run_id || candidate.id || '') === event.detail.item.id)
    if (!row) return
    if (action === 'detail') {
      const href = runDetailValue(row, 'run_href')
      if (href !== '—' && typeof window !== 'undefined') window.location.assign(href)
      return
    }
    if (action !== 'run' && action !== 'cancel') return
    const pipeline = this.page?.pipelines.find((candidate) => candidate.pipelineId === String(row.pipeline_id))
    if (!pipeline) return
    this.emitCommand(action, pipeline, String(row.run_id || ''))
  }

  private emitCommand(action: 'run' | 'retry' | 'cancel', pipeline: PipelineListItemSignal, runId: string): void {
    this.terminalFailure = null
    this.dispatchEvent(new CustomEvent('lv-pipeline-command', {
      bubbles: true,
      composed: true,
      detail: { action, pipelineId: pipeline.pipelineId, assetId: pipeline.assetId, runId },
    }))
  }

  private readonly handleDatastarFetch = (event: Event): void => {
    const detail = (event as CustomEvent<{ type?: string, el?: Element }>).detail
    if (detail?.el === document.querySelector('main[data-init]')) {
      if (detail.type === 'retrying') this.streamState = 'reconnecting'
      else if (detail.type === 'retries-failed' || detail.type === 'finished' || detail.type === 'error') this.streamState = 'lost'
    }
    if (!this.commandStatus.loading || !ownsBrowserCommandFetch(this, event)) return
    const failure = browserCommandFailure(event, 'Pipeline action')
    if (!failure) return
    this.terminalFailure = failure
  }

  private readonly handleStreamPatch = (event: Event): void => {
    const runtime = (event as CustomEvent<{ runtime?: { streamInstanceId?: string } }>).detail?.runtime
    if (this.page?.activeTab === 'runs' && runtime?.streamInstanceId) this.streamState = 'live'
  }

  private readonly reloadAfterFailure = (): void => {
    if (typeof window !== 'undefined') window.location.reload()
  }
}

customElements.define('lv-pipelines-page', LeapViewPipelinesPage)
