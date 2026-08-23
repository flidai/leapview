import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import { CheckCircle2, Circle, Clock3, XCircle } from 'lucide'
import type { PipelineCommandSignal, PipelineCommandStatusSignal, PipelineListItemSignal, PipelinePageSignal, RecordTableSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure, type BrowserCommandFailure } from '../shared/command-failure'
import { lucideIcon } from '../shared/lucide-icons'
import { checkSignalContract } from '../shared/signal-contract'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import type { EntityListItem, EntityListRowAction } from '../shared/entity-list'
import '../shared/drawer'
import '../shared/entity-list'

const pipelineRunStatuses = ['queued', 'running', 'prepared', 'succeeded', 'failed', 'cancelled', 'superseded'] as const

class LeapViewPipelinesPage extends DatastarLit(LitElement) {
  @state() private runQuery = ''
  @state() private runStatus = 'all'
  @state() private runTrigger = 'all'
  @state() private selectedRunID = ''
  @state() private terminalFailure: BrowserCommandFailure | null = null

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

    .metrics {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
    }

    .metric {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
      padding: var(--base-size-16);
    }

    .metric + .metric {
      border-left: var(--lv-border-muted);
    }

    .metric-label,
    .metric-detail {
      overflow: hidden;
      color: var(--lv-fg-muted);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-caption);
    }

    .metric-value {
      color: var(--lv-fg-default);
      font: var(--lv-type-section-title);
      font-variant-numeric: tabular-nums;
    }

    .metric.tone-danger .metric-value { color: var(--lv-fg-danger); }
    .metric.tone-accent .metric-value { color: var(--lv-fg-accent, var(--lv-fg-default)); }

    .tabs {
      display: flex;
      gap: var(--base-size-16);
      border-bottom: var(--lv-border-muted);
    }

    .tabs a {
      position: relative;
      display: inline-flex;
      min-height: var(--control-medium-size);
      align-items: center;
      color: var(--lv-fg-muted);
      text-decoration: none;
      font: var(--lv-type-body);
    }

    .tabs a:hover,
    .tabs a:focus-visible,
    .tabs a.active {
      color: var(--lv-fg-default);
    }

    .tabs a:focus-visible {
      outline: var(--focus-outline);
      outline-offset: var(--focus-outline-offset);
    }

    .tabs a.active::after {
      position: absolute;
      right: 0;
      bottom: -1px;
      left: 0;
      height: 2px;
      border-radius: 2px 2px 0 0;
      background: var(--lv-fg-accent, var(--lv-fg-default));
      content: '';
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

    .run-table {
      min-width: 0;
    }

    .run-detail-title {
      display: inline-flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-6);
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
    }

    .run-detail-title svg {
      display: block;
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .run-detail-title.is-success svg { color: var(--lv-fg-success); }
    .run-detail-title.is-danger svg { color: var(--lv-fg-danger); }
    .run-detail-title.is-attention svg { color: var(--lv-fg-warning); }
    .run-detail-title.is-muted svg { color: var(--lv-fg-muted); }

    .run-detail-subtitle {
      margin: var(--base-size-4) 0 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
    }

    .run-detail-body {
      display: grid;
      min-width: 0;
      align-content: start;
      gap: var(--base-size-20);
    }

    .run-detail-section {
      display: grid;
      min-width: 0;
      gap: var(--base-size-8);
    }

    .run-detail-section h2 {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .run-detail-facts {
      display: grid;
      gap: var(--base-size-6);
    }

    .run-detail-fact {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(7rem, .44fr) minmax(0, 1fr);
      align-items: start;
      gap: var(--base-size-12);
      font: var(--lv-type-body-compact);
    }

    .run-detail-fact > span:first-child {
      color: var(--lv-fg-muted);
    }

    .run-detail-fact code,
    .run-detail-fact strong,
    .run-detail-fact a {
      min-width: 0;
      overflow-wrap: anywhere;
    }

    .run-detail-fact a {
      color: var(--lv-fg-link);
      text-decoration: none;
    }

    .run-detail-fact a:hover,
    .run-detail-fact a:focus-visible {
      text-decoration: underline;
    }

    .run-detail-error {
      max-height: 16rem;
      min-width: 0;
      overflow: auto;
      margin: 0;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-danger);
      padding: var(--base-size-12);
      font-family: var(--fontStack-monospace);
      white-space: pre-wrap;
      overflow-wrap: anywhere;
    }

    @media (max-width: 880px) {
      .metrics { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .metric:nth-child(3) { border-left: 0; border-top: var(--lv-border-muted); }
      .metric:nth-child(4) { border-top: var(--lv-border-muted); }
    }

    @media (max-width: 720px) {
      .page { padding: var(--base-size-12); }
      .run-toolbar input { width: 100%; }
    }
  `]

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleDatastarFetch)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleDatastarFetch)
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
    const selectedRun = this.selectedRunID
      ? page.runsTable.rows.find((row) => String(row.run_id || row.id || '') === this.selectedRunID)
      : null
    return html`
      <section class="page" aria-label="LeapView pipelines">
        ${renderPageHeader(page.title, page.description, page.environment ? `Environment · ${page.environment}` : '')}
        ${this.renderCommandFeedback()}
        <div class="metrics" aria-label="Pipeline health summary">
          ${page.metrics.map((metric) => html`
            <div class=${`metric tone-${metric.tone || 'muted'}`}>
              <span class="metric-label">${metric.label}</span>
              <strong class="metric-value">${metric.value}</strong>
              ${metric.detail ? html`<span class="metric-detail">${metric.detail}</span>` : nothing}
            </div>
          `)}
        </div>
        <nav class="tabs" aria-label="Pipeline monitor views">
          <a class=${page.activeTab === 'pipelines' ? 'active' : ''} href="/pipelines?view=pipelines" aria-current=${page.activeTab === 'pipelines' ? 'page' : nothing}>Pipelines</a>
          <a class=${page.activeTab === 'runs' ? 'active' : ''} href="/pipelines?view=runs" aria-current=${page.activeTab === 'runs' ? 'page' : nothing}>Run history</a>
        </nav>
        ${page.activeTab === 'runs' ? this.renderRuns(page) : this.renderPipelines(page)}
      </section>
      ${selectedRun ? this.renderRunDetail(selectedRun) : nothing}
    `
  }

  private renderPipelines(page: PipelinePageSignal) {
    return html`
      <lv-entity-list
        .items=${page.pipelines.map((pipeline) => ({
          id: pipeline.id,
          title: pipeline.title,
          description: pipeline.description || `${pipeline.semanticModel} · ${pipeline.schedule}`,
          href: pipeline.href,
          icon: 'workflow',
          columns: {
            semanticModel: pipeline.semanticModel,
            schedule: pipeline.schedule,
            nextRun: formatDateTime(pipeline.nextRun),
            status: capitalize(pipeline.status),
          },
          sortValues: {
            nextRun: pipeline.nextRun || '',
            status: pipeline.status,
          },
          columnTitles: {
            nextRun: formatExactDateTime(pipeline.nextRun),
          },
          actions: pipeline.canRun ? [{
            label: pipeline.running ? 'Pipeline is running' : 'Run now',
            action: 'run',
            icon: 'play',
            disabled: pipeline.running || this.commandPendingFor(pipeline.pipelineId),
          }] : [],
        }))}
        .columns=${[
          { id: 'name', label: 'Pipeline', width: '26%' },
          { id: 'semanticModel', label: 'Semantic model', width: '15%' },
          { id: 'schedule', label: 'Schedule', width: '19%' },
          { id: 'nextRun', label: 'Next run', width: '13%' },
          { id: 'status', label: 'Status', width: '11%' },
          { id: 'actions', label: '', width: '5%', sortable: false, render: 'actions' },
        ]}
        search-placeholder="Search pipelines"
        empty-text="No refresh pipelines are available."
        client-filter
        @lv-entity-list-row-action=${this.handlePipelineAction}
      ></lv-entity-list>
    `
  }

  private renderRuns(page: PipelinePageSignal) {
    return html`
      <div class="runs">
        <div class="run-toolbar" aria-label="Run history filters">
          <input type="search" placeholder="Search pipeline or run ID" aria-label="Search pipeline runs" .value=${this.runQuery} @input=${(event: Event) => { this.runQuery = (event.currentTarget as HTMLInputElement).value }}>
          <select aria-label="Filter runs by status" .value=${this.runStatus} @change=${(event: Event) => { this.runStatus = (event.currentTarget as HTMLSelectElement).value }}>
            <option value="all">All statuses</option>
            ${pipelineRunStatuses.map((status) => html`<option value=${status}>${capitalize(status)}</option>`)}
          </select>
          <select aria-label="Filter runs by trigger" .value=${this.runTrigger} @change=${(event: Event) => { this.runTrigger = (event.currentTarget as HTMLSelectElement).value }}>
            <option value="all">All triggers</option>
            ${['manual', 'schedule', 'retry'].map((trigger) => html`<option value=${trigger}>${capitalize(trigger)}</option>`)}
          </select>
        </div>
        <div class="run-table">
          <lv-entity-list
            compact
            title-emphasis="normal"
            row-action="detail"
            min-width="1050px"
            list-label="Pipeline run history"
            empty-text="No pipeline runs have been recorded yet."
            .showToolbar=${false}
            .items=${this.filteredRunItems(page.runsTable)}
            .columns=${[
              { id: 'status', label: 'Status', width: '120px', render: 'status' },
              { id: 'name', label: 'Pipeline', width: '200px' },
              { id: 'started', label: 'Started', width: '170px' },
              { id: 'duration', label: 'Duration', width: '90px' },
              { id: 'trigger', label: 'Trigger', width: '100px' },
              { id: 'triggeredBy', label: 'Triggered by', width: '130px' },
              { id: 'actions', label: '', width: '90px', sortable: false, render: 'actions' },
            ]}
            @lv-entity-list-row-action=${this.handleRunAction}
          ></lv-entity-list>
        </div>
      </div>
    `
  }

  private filteredRunItems(table: RecordTableSignal): EntityListItem[] {
    const query = this.runQuery.trim().toLowerCase()
    return table.rows
      .filter((row) => {
        if (query && !String(row.pipeline_search || '').includes(query)) return false
        if (this.runStatus !== 'all' && row.status_value !== this.runStatus) return false
        if (this.runTrigger !== 'all' && row.trigger_value !== this.runTrigger) return false
        return true
      })
      .map((row) => ({
        id: String(row.run_id || row.id || ''),
        title: runDetailValue(row, 'pipeline'),
        description: firstRunListValue(row.run_id, row.run, row.id),
        href: runDetailValue(row, 'pipeline_href') === '—' ? '#' : runDetailValue(row, 'pipeline_href'),
        icon: 'none',
        columns: {
          status: capitalize(runDetailValue(row, 'status_value')),
          started: runDetailValue(row, 'started'),
          duration: runDetailValue(row, 'duration'),
          trigger: runDetailValue(row, 'trigger'),
          triggeredBy: runDetailValue(row, 'triggered_by'),
        },
        columnTitles: {
          started: formatRunDetailDate(row.started_at),
        },
        sortValues: {
          status: runDetailValue(row, 'status_value'),
          started: String(row.started_at || ''),
        },
        actions: Array.isArray(row.actions)
          ? row.actions.map((action) => {
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

  private handlePipelineAction = (event: CustomEvent<{ action: string, item: { id: string } }>): void => {
    if (event.detail.action !== 'run') return
    const pipeline = this.page?.pipelines.find((candidate) => candidate.id === event.detail.item.id)
    if (!pipeline || !pipeline.canRun || pipeline.running) return
    this.emitCommand('run', pipeline, '')
  }

  private handleRunAction = (event: CustomEvent<{ action: string, item: EntityListItem }>): void => {
    const action = event.detail.action
    const row = this.page?.runsTable.rows.find((candidate) => String(candidate.run_id || candidate.id || '') === event.detail.item.id)
    if (!row) return
    if (action === 'detail') {
      this.selectedRunID = String(row.run_id || row.id || '')
      return
    }
    if (action !== 'retry' && action !== 'cancel') return
    const pipeline = this.page?.pipelines.find((candidate) => candidate.pipelineId === String(row.pipeline_id))
    if (!pipeline) return
    this.emitCommand(action, pipeline, String(row.run_id || ''))
  }

  private closeRunDetail = (): void => {
    this.selectedRunID = ''
  }

  private renderRunDetail(row: Record<string, unknown>) {
    const status = runDetailValue(row, 'status_value')
    const pipeline = runDetailValue(row, 'pipeline')
    const project = runDetailValue(row, 'project')
    const error = runDetailValue(row, 'error')
    const pipelineHref = runDetailValue(row, 'pipeline_href')
    return html`
      <lv-drawer
        open
        size="wide"
        label="Pipeline run detail"
        .modal=${false}
        @lv-drawer-close=${this.closeRunDetail}
      >
        <div slot="title" class=${`run-detail-title is-${runStatusTone(status)}`}>
          ${runStatusIcon(status)}
          <span>${capitalize(status)}</span>
        </div>
        <p slot="subtitle" class="run-detail-subtitle">${pipeline} · ${project}</p>
        <div class="run-detail-body">
          <section class="run-detail-section" aria-label="Run identity">
            <h2>Run identity</h2>
            <div class="run-detail-facts">
              ${runDetailFact('Run ID', runDetailValue(row, 'run_id'), true)}
              ${pipelineHref !== '—'
                ? html`<div class="run-detail-fact"><span>Pipeline</span><a href=${pipelineHref}>${pipeline}</a></div>`
                : runDetailFact('Pipeline', pipeline)}
              ${runDetailFact('Project', project)}
              ${runDetailFact('Environment', runDetailValue(row, 'environment'))}
              ${runDetailFact('Semantic model', runDetailValue(row, 'semantic_model'), true)}
            </div>
          </section>
          <section class="run-detail-section" aria-label="Lifecycle">
            <h2>Lifecycle</h2>
            <div class="run-detail-facts">
              ${runDetailFact('Status', capitalize(status))}
              ${runDetailFact('Trigger', runDetailValue(row, 'trigger'))}
              ${runDetailFact('Triggered by', runDetailValue(row, 'principal_display_name'))}
              ${runDetailFact('Principal ID', runDetailValue(row, 'principal_id'), true)}
              ${runDetailFact('Created', formatRunDetailDate(row.created_at))}
              ${runDetailFact('Started', formatRunDetailDate(row.started_at))}
              ${runDetailFact('Finished', formatRunDetailDate(row.finished_at))}
              ${runDetailFact('Duration', runDetailValue(row, 'duration'))}
            </div>
          </section>
          <section class="run-detail-section" aria-label="Execution">
            <h2>Execution</h2>
            <div class="run-detail-facts">
              ${runDetailFact('Retry of', runDetailValue(row, 'retry_of'), true)}
              ${runDetailFact('Parent run', runDetailValue(row, 'parent_run_id'), true)}
              ${runDetailFact('Serving state', runDetailValue(row, 'serving_state_id'), true)}
              ${runDetailFact('Target generation', runDetailValue(row, 'target_generation'), true)}
            </div>
          </section>
          ${error !== '—' ? html`
            <section class="run-detail-section" aria-label="Run error">
              <h2>Error</h2>
              <pre class="run-detail-error"><code>${error}</code></pre>
            </section>
          ` : nothing}
        </div>
      </lv-drawer>
    `
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
    const failure = browserCommandFailure(event, 'Pipeline action')
    if (!failure) return
    this.terminalFailure = failure
  }

  private readonly reloadAfterFailure = (): void => {
    if (typeof window !== 'undefined') window.location.reload()
  }
}

function commandLoadingLabel(action: string): string {
  if (action === 'cancel') return 'Cancelling pipeline run…'
  if (action === 'retry') return 'Queuing pipeline retry…'
  return 'Queuing pipeline run…'
}

customElements.define('lv-pipelines-page', LeapViewPipelinesPage)

function capitalize(value: string): string {
  return value ? value.charAt(0).toUpperCase() + value.slice(1) : '—'
}

function formatDateTime(value: string | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const elapsed = date.getTime() - Date.now()
  const minutes = Math.round(Math.abs(elapsed) / 60_000)
  if (minutes < 1) return elapsed >= 0 ? 'Now' : 'Just now'
  if (minutes < 60) return elapsed >= 0 ? `In ${minutes} min` : `${minutes} min ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return elapsed >= 0 ? `In ${hours} hr` : `${hours} hr ago`
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium' }).format(date)
}

function formatExactDateTime(value: string | undefined): string {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date)
}

function runDetailValue(row: Record<string, unknown>, key: string): string {
  const value = row[key]
  if (value == null || value === '' || value === '-') return '—'
  return String(value)
}

function runDetailFact(label: string, value: string, code = false) {
  return html`<div class="run-detail-fact"><span>${label}</span>${code ? html`<code>${value}</code>` : html`<strong>${value}</strong>`}</div>`
}

function formatRunDetailDate(value: unknown): string {
  const normalized = value == null || value === '' || value === '-' ? '' : String(value)
  if (!normalized) return '—'
  const date = new Date(normalized)
  return Number.isNaN(date.getTime()) ? normalized : new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' }).format(date)
}

function runStatusTone(status: string): 'success' | 'danger' | 'attention' | 'muted' {
  if (status === 'succeeded') return 'success'
  if (status === 'failed' || status === 'cancelled') return 'danger'
  if (status === 'queued' || status === 'running' || status === 'prepared') return 'attention'
  return 'muted'
}

function runStatusIcon(status: string) {
  if (status === 'succeeded') return lucideIcon(CheckCircle2, { size: 16, strokeWidth: 2 })
  if (status === 'failed' || status === 'cancelled') return lucideIcon(XCircle, { size: 16, strokeWidth: 2 })
  if (status === 'queued' || status === 'running' || status === 'prepared') return lucideIcon(Clock3, { size: 16, strokeWidth: 2 })
  return lucideIcon(Circle, { size: 16, strokeWidth: 2 })
}

function firstRunListValue(...values: unknown[]): string {
  const value = values.find((candidate) => candidate != null && candidate !== '' && candidate !== '-')
  return value == null ? '—' : String(value)
}

function pipelineRunActionIcon(icon: string): EntityListRowAction['icon'] {
  if (icon === 'details') return 'details'
  if (icon === 'cancel') return 'cancel'
  if (icon === 'refresh') return 'refresh'
  return 'play'
}
