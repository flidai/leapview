import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import type { PipelineCommandSignal, PipelineCommandStatusSignal, PipelineDetailRunSignal, PipelineListItemSignal, PipelinePageSignal, PipelineWaitingIntentSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure, ownsBrowserCommandFetch, type BrowserCommandFailure } from '../shared/command-failure'
import { checkSignalContract } from '../shared/signal-contract'
import { showToast } from '../shared/toast'
import { pageHeaderStyles, renderPageHeader } from '../shared/page-header'
import type { EntityListItem } from '../shared/entity-list'
import { commandLoadingLabel, formatDateTime, formatExactDateTime, runDetailValue, runStatusIcon, runStatusTone } from './pipelines-page-format'
import { showPipelineCommandToast } from './pipeline-command-toast'
import '../shared/entity-list'
import './pipeline-runs-list'

type RecentSlot = { kind: 'run', run: PipelineDetailRunSignal } | { kind: 'request', intent: PipelineWaitingIntentSignal }

class LeapViewPipelinesPage extends DatastarLit(LitElement) {
  @state() private terminalFailure: BrowserCommandFailure | null = null
  @state() private streamState: 'live' | 'reconnecting' | 'lost' = 'live'
  private pendingToastAction = ''
  private commandMessageAtDispatch = ''
  private commandErrorAtDispatch = ''
  private commandSawLoading = false

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

    .recent-runs { display: inline-flex; box-sizing: border-box; width: 100%; align-items: center; justify-content: flex-end; gap: var(--base-size-8); white-space: nowrap; }
    .recent-run { display: inline-flex; width: 20px; height: 24px; align-items: center; justify-content: center; border-radius: var(--lv-radius-small); color: var(--lv-fg-muted); text-decoration: none; }
    .recent-run:hover, .recent-run:focus-visible { background: var(--lv-bg-control-hover); outline-offset: 2px; }
    .recent-run[data-tone='success'] { color: var(--lv-fg-success); }
    .recent-run[data-tone='danger'] { color: var(--lv-fg-danger); }
    .recent-run[data-tone='attention'] { color: var(--lv-fg-warning); }
    .recent-run[data-tone='accent'] { color: var(--lv-fg-accent); }
    .page[data-stream-state='live'] .recent-run[data-status='running'] svg, .page[data-stream-state='live'] .recent-run[data-status='prepared'] svg { animation: recent-run-spin 1s linear infinite; }
    @keyframes recent-run-spin { to { transform: rotate(360deg); } }
    @media (prefers-reduced-motion: reduce) { .page[data-stream-state='live'] .recent-run[data-status='running'] svg, .page[data-stream-state='live'] .recent-run[data-status='prepared'] svg { animation: none; } }
    .recent-run-empty { color: var(--lv-fg-muted); font: var(--lv-type-body); }

    @media (max-width: 720px) {
      .page { padding: var(--base-size-12); }
      .entity-list.has-mobile-cards .entity-list-table-row > td[data-column='trigger']:has(.pipeline-trigger-empty) { display: none; }
      .entity-list.has-mobile-cards .entity-list-table-row > td[data-column='recentRuns'] { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-8); }
      .recent-runs { width: auto; }
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
    return this.signal<PipelineCommandSignal>('pipelineCommand', { action: '', assetId: '', intentId: '', pipelineId: '', runId: '' })
  }

  get commandStatus(): PipelineCommandStatusSignal {
    return this.signal<PipelineCommandStatusSignal>('pipelineCommandStatus', { loading: false, error: '', message: '' })
  }

  updated(): void {
    checkSignalContract('pipelines page', this.page, { kind: 'required', pipelines: 'required', runsTable: 'required', waitingIntents: 'required' })
    if (this.pendingToastAction) {
      const status = this.commandStatus
      if (this.terminalFailure || (status.error && !status.loading && (this.commandSawLoading || status.error !== this.commandErrorAtDispatch))) {
        if (this.page?.activeTab === 'pipelines') {
          showToast({ message: this.terminalFailure?.message || status.error, tone: 'error',
            action: { label: 'Reload latest pipeline state', onClick: this.reloadAfterFailure } })
        }
        this.pendingToastAction = ''
      } else if (status.loading) this.commandSawLoading = true
      else if (status.message && (this.commandSawLoading || status.message !== this.commandMessageAtDispatch)) {
        showPipelineCommandToast(this.pendingToastAction)
        this.pendingToastAction = ''
      }
    }
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    return html`
      <section class="page" data-stream-state=${this.streamState} aria-label="LeapView pipelines">
        ${renderPageHeader(page.title)}
        ${this.renderTabs(page.activeTab)}
        ${page.activeTab === 'runs' && this.streamState !== 'live' ? html`<p class="command-feedback is-error" role="alert">${this.streamState === 'reconnecting' ? 'Reconnecting to run updates…' : 'Run updates disconnected. Reload to see the latest state.'}</p>` : nothing}
        ${page.activeTab === 'runs' ? this.renderCommandFeedback() : nothing}
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
        min-width="900px"
        .items=${this.pipelineItems(page.pipelines, page.waitingIntents ?? [])}
        .columns=${[
          { id: 'name', label: 'Pipeline', width: '220px' },
          { id: 'semanticModel', label: 'Refreshes', width: '180px' },
          { id: 'trigger', label: 'Trigger', width: '130px' },
          { id: 'recentRuns', label: 'Recent runs', width: '155px', sortable: false },
          { id: 'lastPublished', label: 'Last published', width: '160px' },
          { id: 'actions', label: 'Run now', width: '55px', sortable: false, render: 'actions' },
        ]}
        @lv-entity-list-row-action=${this.handlePipelineAction}
      ></lv-entity-list>
    `
  }

  private pipelineItems(pipelines: PipelineListItemSignal[], waitingIntents: PipelineWaitingIntentSignal[]): EntityListItem[] {
    return pipelines.map((pipeline) => {
      const published = pipeline.lastPublishedAt
        ? formatDateTime(pipeline.lastPublishedAt)
        : pipeline.publicationStatus === 'unavailable' ? 'Unavailable' : pipeline.status === 'never run' || pipeline.status === 'not refreshed' ? 'Never published' : '—'
      const schedule = pipeline.schedule.trim()
      const scheduled = schedule !== '' && schedule.toLowerCase() !== 'manual only' && schedule.toLowerCase() !== 'manual'
      const triggerTitle = scheduled ? [schedule, pipeline.nextRun ? `Next ${formatExactDateTime(pipeline.nextRun)}` : ''].filter(Boolean).join(' · ') : ''
      return {
        id: pipeline.pipelineId,
        title: pipeline.title,
        href: pipeline.href,
        icon: 'pipeline',
        columns: { semanticModel: pipeline.semanticModel, trigger: scheduled ? 'Scheduled' : '', recentRuns: (pipeline.recentRuns ?? []).map((run) => run.status).join(' '), lastPublished: published },
        columnContent: {
          recentRuns: this.renderRecentRuns(pipeline, waitingIntents),
          ...(!scheduled ? { trigger: html`<span class="pipeline-trigger-empty" aria-hidden="true"></span>` } : {}),
        },
        columnTitles: { trigger: triggerTitle, lastPublished: formatExactDateTime(pipeline.lastPublishedAt) },
        sortValues: { lastPublished: pipeline.lastPublishedAt ? Date.parse(pipeline.lastPublishedAt) || 0 : 0 },
        actions: pipeline.canRun ? [{ label: `Run ${pipeline.title} now`, action: 'run', icon: 'play', disabled: this.commandPendingFor(pipeline.pipelineId) || Boolean(this.terminalFailure) }] : [],
      }
    })
  }

  private renderRecentRuns(pipeline: PipelineListItemSignal, waitingIntents: PipelineWaitingIntentSignal[]) {
    const runs = (pipeline.recentRuns ?? []).slice(0, 5)
    const visibleRunIDs = new Set(runs.map((run) => run.id))
    const requests = waitingIntents
      .filter((intent) => intent.pipelineId === pipeline.pipelineId && ['waiting', 'claimed', 'attached'].includes(intent.status.toLowerCase()) && (!intent.runId || !visibleRunIDs.has(intent.runId)))
      .sort((left, right) => Date.parse(left.createdAt) - Date.parse(right.createdAt))
    const recent: RecentSlot[] = [...runs.reverse().map((run): RecentSlot => ({ kind: 'run', run })), ...requests.map((intent): RecentSlot => ({ kind: 'request', intent }))].slice(-5)
    const slots: (RecentSlot | undefined)[] = [...Array<undefined>(5 - recent.length).fill(undefined), ...recent]
    return html`<span class="recent-runs" role="group" aria-label=${`Recent runs and queued requests for ${pipeline.title}, oldest to newest`}>
      ${slots.map((slot) => {
        if (!slot) return html`<span class="recent-run recent-run-empty" aria-hidden="true">-</span>`
        if (slot.kind === 'request') {
          const label = `Queued request ${slot.intent.intentId} for ${pipeline.title}`
          return html`<a class="recent-run" data-status="queued" data-tone="accent" href=${`/pipelines/${encodeURIComponent(pipeline.pipelineId)}/runs`} title=${label} aria-label=${label} @click=${(event: Event) => event.stopPropagation()}>${runStatusIcon('queued')}</a>`
        }
        const run = slot.run
        const when = formatExactDateTime(run.startedAt)
        const label = `${run.status === 'prepared' ? 'Running' : `${run.status.charAt(0).toUpperCase()}${run.status.slice(1)}`} run ${run.id}${when ? `, started ${when}` : ''}`
        return html`<a class="recent-run" data-status=${run.status} data-tone=${run.status === 'queued' ? 'accent' : runStatusTone(run.status)} href=${run.href} title=${label} aria-label=${label} @click=${(event: Event) => event.stopPropagation()}>${runStatusIcon(run.status)}</a>`
      })}
    </span>`
  }

  private handlePipelineAction = (event: CustomEvent<{ action: string, item: EntityListItem }>): void => {
    if (event.detail.action !== 'run') return
    const pipeline = this.page?.pipelines.find((candidate) => candidate.pipelineId === event.detail.item.id)
    if (pipeline) this.runPipeline(pipeline)
  }

  private renderRuns(page: PipelinePageSignal) {
    return html`<lv-pipeline-runs-list
      .monitor=${page.runMonitor ?? null}
      .table=${page.runsTable}
      .pipelines=${page.pipelines}
      .waitingIntents=${page.waitingIntents ?? []}
      .pendingCommand=${this.commandStatus.loading && !this.terminalFailure ? this.command : null}
      .live=${this.streamState === 'live'}
      @lv-pipeline-command=${this.captureChildCommand}
      @lv-entity-list-row-action=${this.handleRunAction}
    ></lv-pipeline-runs-list>`
  }

  private captureChildCommand = (event: CustomEvent<PipelineCommandSignal>): void => {
    if (event.detail.action !== 'cancel-intent') return
    this.terminalFailure = null
    this.pendingToastAction = event.detail.action
    this.commandMessageAtDispatch = this.commandStatus.message
    this.commandErrorAtDispatch = this.commandStatus.error
    this.commandSawLoading = false
  }

  private renderCommandFeedback() {
    const status = this.commandStatus
    const failure = this.terminalFailure
    if (!failure && !status.loading && !status.error) return nothing
    const message = failure?.message || (status.loading ? commandLoadingLabel(this.command.action) : status.error)
    return html`
      <div class=${`command-feedback ${failure || status.error ? 'is-error' : ''}`} role=${failure || status.error ? 'alert' : 'status'} aria-live=${failure || status.error ? 'assertive' : 'polite'}>
        <div>${message}</div>
        ${failure ? html`
          <div class="command-feedback-actions">
            <span>Check Recent runs before retrying.</span>
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
    if (!pipeline.canRun || this.commandPendingFor(pipeline.pipelineId)) return
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
    this.dispatchPipelineCommand(action, pipeline.pipelineId, '', runId)
  }

  private dispatchPipelineCommand(action: 'run' | 'retry' | 'cancel' | 'cancel-intent', pipelineId: string, intentId: string, runId: string): void {
    this.terminalFailure = null
    this.pendingToastAction = action
    this.commandMessageAtDispatch = this.commandStatus.message
    this.commandErrorAtDispatch = this.commandStatus.error
    this.commandSawLoading = false
    this.dispatchEvent(new CustomEvent('lv-pipeline-command', {
      bubbles: true,
      composed: true,
      detail: { action, pipelineId, assetId: pipelineId, intentId, runId },
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
    // A transport error can arrive after the server queued the run (for
    // example, if durable acknowledgement fails). Never claim the mutation
    // was rolled back or invite an immediate duplicate retry.
    this.terminalFailure = failure.kind === 'unavailable' || failure.kind === 'network' || failure.kind === 'unknown'
      ? { ...failure, message: 'Could not confirm whether the pipeline action was accepted. Check Recent runs before retrying.' }
      : failure
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
