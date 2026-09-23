import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import type { AssetLineageGraphSignal, PipelineRunDetailPageSignal, PipelineRunModelSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { breadcrumbStyles, renderAssetBreadcrumbGlyph, renderBreadcrumb, type BreadcrumbItem } from '../shared/breadcrumb'
import { checkSignalContract } from '../shared/signal-contract'

class LeapViewPipelineRunPage extends DatastarLit(LitElement) {
  @state() private selectedModelID = ''
  @state() private fullRunGraph = false
  @state() private streamState: 'live' | 'reconnecting' | 'lost' = 'live'
  @state() private mobileExecutionView: 'models' | 'graph' = 'models'
  @state() private mobileViewport = false
  private currentRunKey = ''
  private mobileMediaQuery?: MediaQueryList

  static styles = [breadcrumbStyles, css`
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
      gap: var(--base-size-12);
      margin-inline: auto;
      padding: var(--base-size-24);
    }
    .sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0, 0, 0, 0); white-space: nowrap; }

    .run-heading { display: grid; gap: var(--base-size-8); }
    .run-live-status { display: inline-flex; width: fit-content; align-items: center; gap: var(--base-size-6); font: var(--lv-type-caption); }
    .run-live-status::before { content: ''; width: var(--base-size-8); height: var(--base-size-8); flex: none; border-radius: 50%; background: currentColor; }
    .run-live-status.is-reconnecting { color: var(--lv-fg-warning); }
    .run-live-status.is-lost { color: var(--lv-fg-danger); }
    .run-status-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); overflow: hidden; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); }
    .run-status { display: grid; min-width: 0; gap: var(--base-size-4); padding: var(--base-size-12) var(--base-size-16); }
    .run-status + .run-status { border-left: var(--lv-border-muted); }
    .run-status-label { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .run-status-value { overflow-wrap: anywhere; color: var(--lv-fg-default); font: var(--lv-type-section-title); }
    .run-status-detail { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .tone-success { color: var(--lv-fg-success); }
    .tone-danger { color: var(--lv-fg-danger); }
    .tone-warning { color: var(--lv-fg-warning); }

    .run-error, .graph-message, .empty-state {
      margin: 0;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-muted);
      padding: var(--base-size-12) var(--base-size-16);
      font: var(--lv-type-body-compact);
      overflow-wrap: anywhere;
    }
    .run-error { border-color: var(--lv-border-danger, var(--lv-border-muted)); color: var(--lv-fg-danger); white-space: pre-wrap; }
    .run-error button { width: fit-content; margin-top: var(--base-size-8); border: 0; background: transparent; color: var(--lv-fg-accent); padding: 0; font: var(--lv-type-body-compact); text-decoration: underline; cursor: pointer; }

    .tabs { display: flex; overflow-x: auto; gap: var(--base-size-4); border-bottom: var(--lv-border-muted); }
    .tabs a { display: inline-flex; min-height: 2.5rem; align-items: center; padding: 0 var(--base-size-12); border-bottom: 2px solid transparent; color: var(--lv-fg-muted); font: var(--lv-type-body); text-decoration: none; white-space: nowrap; }
    .tabs a[aria-current="page"] { border-bottom-color: var(--lv-fg-accent, var(--lv-fg-default)); color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }
    .tabs a:focus-visible, .model-list button:focus-visible, .model-list a:focus-visible, .mobile-execution-switch button:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }

    .panel { display: grid; min-width: 0; gap: var(--base-size-12); }
    .panel h2 { margin: 0; font: var(--lv-type-section-title); }
    .panel h3 { margin: 0; font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .lifecycle-panel { display: grid; gap: var(--base-size-8); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-12) var(--base-size-16); }
    .lifecycle-summary { display: flex; min-width: 0; flex-wrap: wrap; align-items: baseline; justify-content: space-between; gap: var(--base-size-4) var(--base-size-12); cursor: pointer; list-style-position: inside; }
    .lifecycle-summary strong { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .lifecycle-summary span { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .lifecycle-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--base-size-8); }
    .lifecycle-phase { display: grid; min-width: 0; align-content: start; gap: var(--base-size-4); border-left: 2px solid var(--lv-border-muted); padding: var(--base-size-4) var(--base-size-12); }
    .lifecycle-phase > span { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .lifecycle-phase > strong { overflow-wrap: anywhere; color: var(--lv-fg-default); font: var(--lv-type-body-compact); }
    .lifecycle-phase > small { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .execution-layout { display: grid; min-width: 0; grid-template-columns: minmax(0, 1.55fr) minmax(16rem, .75fr); gap: var(--base-size-16); }
    .mobile-execution-switch { display: none; }
    .graph-panel, .models-panel, .detail-panel { display: grid; min-width: 0; align-content: start; gap: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-16); }
    .graph-panel lv-asset-lineage-graph { display: block; height: clamp(30rem, 55vh, 36rem); min-height: 30rem; }
    .model-list { display: grid; gap: var(--base-size-6); margin: 0; padding: 0; list-style: none; }
    .model-list > li > button, .model-list > li > a { display: grid; width: 100%; min-width: 0; grid-template-columns: minmax(0, 1fr) auto; align-items: start; gap: var(--base-size-8); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding: var(--base-size-8) var(--base-size-12); text-align: left; cursor: pointer; text-decoration: none; }
    .model-list button[aria-pressed="true"] { border-color: var(--lv-fg-accent, var(--lv-border-muted)); background: var(--lv-bg-panel-muted); }
    .model-list > li > a:hover { border-color: var(--lv-fg-accent, var(--lv-border-muted)); background: var(--lv-bg-panel-muted); }
    .model-name { min-width: 0; overflow-wrap: anywhere; font: var(--lv-type-body-compact); }
    .model-outcome { color: var(--lv-fg-muted); font: var(--lv-type-caption); white-space: nowrap; }
    .model-timing { grid-column: 1 / -1; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .model-diagnostic { display: grid; gap: var(--base-size-6); margin: 0 var(--base-size-8) var(--base-size-8); border-top: var(--lv-border-muted); padding-top: var(--base-size-12); }
    .model-diagnostic p { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-body-compact); overflow-wrap: anywhere; }
    .model-diagnostic .diagnostic-error { color: var(--lv-fg-danger); white-space: pre-wrap; }
    .model-open { width: fit-content; color: var(--lv-fg-accent, var(--lv-fg-default)); font: var(--lv-type-body-compact); text-decoration: underline; text-underline-offset: 2px; }
    .attempt-details { font: var(--lv-type-body-compact); }
    .attempt-details summary { width: fit-content; color: var(--lv-fg-accent); cursor: pointer; }
    .attempt-details ol, .attempt-list { display: grid; gap: var(--base-size-8); margin: var(--base-size-8) 0 0; padding-left: var(--base-size-20); font: var(--lv-type-body-compact); }
    .attempt-details li, .attempt-list li { padding-left: var(--base-size-4); }
    .attempt-details li span, .attempt-list li span { margin-left: var(--base-size-8); color: var(--lv-fg-muted); }
    .attempt-details li p, .attempt-list li p { margin: var(--base-size-4) 0 0; color: var(--lv-fg-danger); overflow-wrap: anywhere; }

    .event-list { display: grid; gap: var(--base-size-8); margin: 0; padding: 0; list-style: none; }
    .event { display: grid; min-width: 0; grid-template-columns: minmax(10rem, .4fr) minmax(0, 1fr); align-items: start; gap: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-12); }
    .event-type { min-width: 0; overflow-wrap: anywhere; font: var(--lv-type-body); }
    .event-id, .event time { color: var(--lv-fg-muted); font: var(--lv-type-caption); font-variant-numeric: tabular-nums; }
    .event time { text-align: left; }
    .event-technical { margin-top: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .event-technical summary { width: fit-content; cursor: pointer; }

    .facts { display: grid; min-width: 0; gap: var(--base-size-8); }
    .fact { display: grid; min-width: 0; grid-template-columns: minmax(8rem, .5fr) minmax(0, 1fr); gap: var(--base-size-12); font: var(--lv-type-body-compact); }
    .fact > span:first-child { color: var(--lv-fg-muted); }
    .fact strong, .fact code { min-width: 0; overflow-wrap: anywhere; color: var(--lv-fg-default); }
    .fact code, .digest-list code { font-family: var(--fontStack-monospace); }
    .digest-list { display: grid; gap: var(--base-size-8); margin: 0; padding: 0; list-style: none; }
    .digest-list li { display: grid; min-width: 0; grid-template-columns: minmax(7rem, .35fr) minmax(0, 1fr) auto; gap: var(--base-size-12); font: var(--lv-type-body-compact); }
    .digest-list span { color: var(--lv-fg-muted); }
    .digest-list button { border: 0; background: transparent; color: var(--lv-fg-accent); padding: 0; font: var(--lv-type-caption); text-decoration: underline; cursor: pointer; }
    .technical-details > summary, .recorded-scope > summary { cursor: pointer; font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .technical-details .digest-list, .recorded-scope .model-list { margin-top: var(--base-size-12); }

    @media (max-width: 800px) {
      .page { padding: var(--base-size-12); }
      .execution-layout { grid-template-columns: minmax(0, 1fr); }
      .execution-layout[data-view="models"] .graph-panel, .execution-layout[data-view="graph"] .models-panel { display: none; }
      .mobile-execution-switch { display: flex; gap: var(--base-size-4); border-bottom: var(--lv-border-muted); }
      .mobile-execution-switch button { min-height: 2.5rem; border: 0; border-bottom: 2px solid transparent; background: transparent; color: var(--lv-fg-muted); padding-inline: var(--base-size-12); font: var(--lv-type-body); cursor: pointer; }
      .mobile-execution-switch button[aria-pressed="true"] { border-bottom-color: var(--lv-fg-accent); color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }
      .run-status-grid { grid-template-columns: minmax(0, 1fr); }
      .run-status + .run-status { border-top: var(--lv-border-muted); border-left: 0; }
      .lifecycle-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .lifecycle-panel { padding: var(--base-size-8) var(--base-size-12); }
      .graph-panel lv-asset-lineage-graph { height: clamp(30rem, 58svh, 36rem); min-height: 30rem; }
    }
    @media (max-width: 520px) {
      .event { grid-template-columns: minmax(0, 1fr); }
      .event time { text-align: left; }
      .fact, .digest-list li { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-2); }
      .lifecycle-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    }
  `]

  get page(): PipelineRunDetailPageSignal | null {
    return this.signal<PipelineRunDetailPageSignal | null>('page', null)
  }

  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('datastar-fetch', this.handleStreamFetch)
    document.addEventListener('datastar-signal-patch', this.handleStreamPatch)
    this.mobileMediaQuery = window.matchMedia('(max-width: 800px)')
    this.mobileViewport = this.mobileMediaQuery.matches
    this.mobileMediaQuery.addEventListener('change', this.handleViewportChange)
  }

  override disconnectedCallback(): void {
    document.removeEventListener('datastar-fetch', this.handleStreamFetch)
    document.removeEventListener('datastar-signal-patch', this.handleStreamPatch)
    this.mobileMediaQuery?.removeEventListener('change', this.handleViewportChange)
    super.disconnectedCallback()
  }

  updated(): void {
    const page = this.page
    const key = `${page?.pipelineId ?? ''}:${page?.runId ?? ''}`
    if (key !== this.currentRunKey) {
      this.currentRunKey = key
      this.selectedModelID = ''
      this.fullRunGraph = false
      this.mobileExecutionView = 'models'
    }
    checkSignalContract('pipeline run detail', page, {
      kind: 'required', activeTab: 'required', execution: 'required', events: 'required', details: 'required',
    })
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    const selectedModel = this.selectedModel(page)
    const tabs: Array<{ id: 'execution' | 'events' | 'details', label: string }> = [
      { id: 'execution', label: 'Execution' }, { id: 'events', label: 'Events' }, { id: 'details', label: 'Details' },
    ]
    const breadcrumbs: BreadcrumbItem[] = [
      { label: 'Pipelines', href: '/pipelines' },
      { label: page.pipelineTitle, href: page.pipelineHref, prefix: renderAssetBreadcrumbGlyph('refresh_pipeline') },
      { label: 'Runs', href: `/pipelines/${encodeURIComponent(page.pipelineId)}/runs` },
      { label: `# ${shortRunID(page.runId)}`, current: true },
    ]
    return html`<section class="page" aria-label="Pipeline run investigation">
      <div class="run-heading">
        ${renderBreadcrumb(breadcrumbs)}
        ${this.streamState !== 'live' ? html`<span class=${`run-live-status is-${this.streamState}`} role="status" aria-live="polite">${this.streamState === 'reconnecting' ? 'Reconnecting' : 'Connection lost'}</span>` : nothing}
      </div>
      <div class="run-status-grid" aria-label="Run outcome">
        <div class="run-status"><strong class=${`run-status-value ${statusTone(page.status)}`}>${page.statusLabel}</strong><span class="run-status-detail">${[page.execution.duration, humanize(page.details.trigger), page.execution.startedAt ? formatRunDate(page.execution.startedAt) : 'Not started'].filter(Boolean).join(' · ')}</span></div>
        <div class="run-status"><span class="run-status-label">Publication</span><strong class=${`run-status-value ${publicationTone(page.execution.publicationOutcome)}`}>${publicationLabel(page.execution.publicationOutcome)}</strong>${this.renderPublicationEvidence(page)}</div>
      </div>
      ${page.runError ? html`<div class="run-error" role="alert"><strong>Run error</strong><div>${page.runError}</div>${page.execution.models.some((model) => model.status === 'failed') ? html`<button type="button" @click=${() => this.showFailedModel(page)}>View failed model</button>` : nothing}</div>` : nothing}
      <nav class="tabs" aria-label="Run investigation sections">
        ${tabs.map((tab) => html`<a href=${this.tabHref(page, tab.id)} aria-current=${page.activeTab === tab.id ? 'page' : nothing}>${tab.label}</a>`)}
      </nav>
      ${page.activeTab === 'events' ? this.renderEvents(page) : page.activeTab === 'details' ? this.renderDetails(page) : this.renderExecution(page, selectedModel)}
    </section>`
  }

  private renderPublicationEvidence(page: PipelineRunDetailPageSignal) {
    const publication = page.execution.publication
    if (!publication) return html`<span class="run-status-detail">${page.execution.publicationOutcome === 'not_published' ? 'No new data published.' : 'No confirmed publication yet.'}</span>`
    return html`<span class="run-status-detail">Snapshot ${publication.snapshotId} · ${formatRunDate(publication.publishedAt)}</span>`
  }

  private renderExecution(page: PipelineRunDetailPageSignal, selectedModel: PipelineRunModelSignal | undefined) {
    return html`<section class="panel" aria-labelledby="execution-title">
      <h2 id="execution-title" class="sr-only">Execution</h2>
      <details class="lifecycle-panel" aria-label="Lifecycle evidence" ?open=${!this.mobileViewport}>
        <summary class="lifecycle-summary"><strong>Progress</strong></summary>
        <div class="lifecycle-grid">
          <div class="lifecycle-phase"><span>Queue</span><strong>${queuePhase(page)}</strong></div>
          <div class="lifecycle-phase"><span>Models</span><strong>${modelsProgress(page)}</strong></div>
          <div class="lifecycle-phase"><span>Validation</span><strong>${validationOutcomeLabel(page.execution.validationOutcome)}</strong></div>
          <div class="lifecycle-phase"><span>Publish</span><strong>${publicationProgress(page)}</strong></div>
        </div>
      </details>
      <div class="mobile-execution-switch" role="group" aria-label="Execution view">
        <button type="button" aria-pressed=${this.mobileExecutionView === 'models' ? 'true' : 'false'} @click=${() => { this.mobileExecutionView = 'models' }}>Models</button>
        <button type="button" aria-pressed=${this.mobileExecutionView === 'graph' ? 'true' : 'false'} @click=${() => { this.mobileExecutionView = 'graph' }}>Graph</button>
      </div>
      <div class="execution-layout" data-view=${this.mobileExecutionView}>
        <section class="graph-panel" aria-label="Historical pipeline graph">
          <h3>Dependencies</h3>
          ${page.execution.graph
            ? html`<lv-asset-lineage-graph .graph=${this.selectGraphNode(page, selectedModel?.modelId)} .scope=${this.fullRunGraph ? 'full' : 'focused'} scope-mode="run" .dialogTitle=${`${page.pipelineTitle} · ${this.fullRunGraph ? 'Full run graph' : 'Focused path'} · ${shortRunID(page.runId)}`} @lv-lineage-select=${this.handleGraphSelection} @lv-lineage-scope-change=${this.handleGraphScopeChange}></lv-asset-lineage-graph>`
            : html`<p class="graph-message" role="status">${page.execution.graphUnavailableReason || 'The graph for this run is unavailable.'}</p>`}
        </section>
        <section class="models-panel" id="run-models" aria-label="Model diagnostics">
          <h3>Models</h3>
          ${page.execution.modelsUnavailable ? html`<p class="graph-message" role="status">Some model outcomes are unavailable.</p>` : nothing}
          ${page.execution.models.length ? html`<ul class="model-list">
            ${page.execution.models.map((model) => html`<li><button type="button" aria-pressed=${model.modelId === selectedModel?.modelId ? 'true' : 'false'} aria-expanded=${model.modelId === selectedModel?.modelId ? 'true' : 'false'} @click=${() => { this.selectedModelID = model.modelId }}>
              <span class="model-name">${this.modelLabel(page, model)}</span><span class=${`model-outcome ${model.status ? statusTone(model.status) : ''}`}>${modelOutcomeLabel(model, page.execution.modelsUnavailable === true)}</span>
              ${model.duration ? html`<small class="model-timing">Duration ${model.duration}</small>` : nothing}
            </button>${model.modelId === selectedModel?.modelId ? this.renderModelDiagnostic(page, model) : nothing}</li>`)}
          </ul>` : html`<p class="empty-state">No model scope was recorded for this run.</p>`}
        </section>
      </div>
    </section>`
  }

  private renderModelDiagnostic(page: PipelineRunDetailPageSignal, model: PipelineRunModelSignal) {
    return html`<div class="model-diagnostic" aria-live="polite">
      ${model.error && model.error !== page.runError ? html`<p class="diagnostic-error">${model.error}</p>` : model.status === 'failed' && !model.error && !page.runError ? html`<p>No error details were recorded.</p>` : !model.status ? html`<p>${page.execution.modelsUnavailable ? 'This model’s outcome is unavailable.' : 'No model outcome was recorded.'}</p>` : nothing}
      ${model.attemptsUnavailable ? html`<p>Attempt history unavailable.</p>` : nothing}
      ${model.attemptsTruncated ? html`<p>Showing the first ${model.attempts.length} attempts.</p>` : nothing}
      ${model.attempts?.length ? html`<details class="attempt-details" ?open=${model.status === 'failed'}><summary>Attempts (${model.attempts.length})</summary><ol>${model.attempts.map((attempt) => html`<li><strong>Attempt ${attempt.number} · ${humanize(attempt.status)}</strong>${attempt.duration ? html`<span>${attempt.duration}</span>` : nothing}${attempt.error && attempt.error !== model.error && attempt.error !== page.runError ? html`<p class="diagnostic-error">${attempt.error}</p>` : nothing}</li>`)}</ol></details>` : nothing}
      ${this.modelHref(page, model) ? html`<a class="model-open" href=${this.modelHref(page, model)}>Open model details</a>` : nothing}
    </div>`
  }

  private renderEvents(page: PipelineRunDetailPageSignal) {
    return html`<section class="panel" aria-label="Run events">
      ${page.eventsUnavailable ? html`<p class="graph-message" role="status">Run events could not be loaded.</p>` : nothing}
      ${page.eventsTruncated ? html`<p class="graph-message" role="status">Showing the first ${page.events.length} events. More events are available.</p>` : nothing}
      ${page.eventsUnavailable ? nothing : page.events.length ? html`<ol class="event-list">
        ${page.events.map((event) => html`<li class="event"><time datetime=${event.createdAt}>${formatRunDate(event.createdAt)}</time><div><div class="event-type">${eventLabel(event.type)}</div><details class="event-technical"><summary>Technical details</summary><code class="event-id">${event.type} · Event ${event.id}</code></details></div></li>`)}
      </ol>` : html`<p class="empty-state">No run events are available.</p>`}
    </section>`
  }

  private renderDetails(page: PipelineRunDetailPageSignal) {
    const details = page.details
    return html`<section class="panel" aria-label="Run details">
      <section class="detail-panel" aria-label="Invocation">
        <h3>Invocation</h3>
        <div class="facts">
          ${fact('Trigger', humanize(details.trigger))}
          ${details.triggeredBy ? fact('Initiated by', details.triggeredBy) : nothing}
          ${details.matchingScheduleIds.length ? fact('Matching schedule', details.matchingScheduleIds.join(', '), true) : nothing}
          ${fact('Created', formatRunDate(page.execution.createdAt))}
          ${fact('Started', page.execution.startedAt ? formatRunDate(page.execution.startedAt) : 'Not started')}
          ${fact('Finished', page.execution.finishedAt ? formatRunDate(page.execution.finishedAt) : isPipelineRunActive(page.status) ? 'In progress' : 'Not recorded')}
        </div>
      </section>
      ${page.execution.attempts?.length || page.execution.attemptsUnavailable ? html`<section class="detail-panel" aria-label="Attempts"><h3>Attempts</h3>${page.execution.attemptsUnavailable ? html`<p class="graph-message" role="status">Attempt history is unavailable.</p>` : nothing}${page.execution.attemptsTruncated ? html`<p class="graph-message">Showing the first ${page.execution.attempts.length} attempts.</p>` : nothing}${page.execution.attempts?.length ? html`<ol class="attempt-list">${page.execution.attempts.map((attempt) => html`<li><strong>Attempt ${attempt.number} · ${humanize(attempt.status)}</strong>${attempt.duration ? html`<span>${attempt.duration}</span>` : nothing}${attempt.error ? html`<p class="diagnostic-error">${attempt.error}</p>` : nothing}</li>`)}</ol>` : nothing}</section>` : nothing}
      <section class="detail-panel" aria-label="Configuration">
        <h3>Configuration</h3>
        <div class="facts">
          ${fact('Serving generation', details.servingStateId, true)}
          ${fact('Historical definition', details.historicalPipelineVersionAvailable ? details.historicalPipelineName || 'Available' : 'Unavailable')}
          ${details.planDigest ? fact('Plan fingerprint', details.planDigest.length > 16 ? `${details.planDigest.slice(0, 12)}…` : details.planDigest, true) : nothing}
        </div>
      </section>
      <details class="detail-panel technical-details"><summary>Technical identifiers</summary>
        <ul class="digest-list">
          ${factRow('Pipeline ID', page.pipelineId)}
          ${factRow('Run ID', page.runId)}
          ${details.principalId ? factRow('Principal ID', details.principalId) : nothing}
          ${factRow('Semantic model ID', details.semanticModelId)}
          ${details.pipelinePlanId ? factRow('Plan ID', details.pipelinePlanId) : nothing}
          ${factRow('Plan digest', details.planDigest)}
          ${details.artifactDigest ? factRow('Artifact digest', details.artifactDigest) : nothing}
          ${details.selectionDigest ? factRow('Selection digest', details.selectionDigest) : nothing}
          ${details.executionDigest ? factRow('Execution digest', details.executionDigest) : nothing}
          ${details.provenanceDigest ? factRow('Provenance digest', details.provenanceDigest) : nothing}
          ${details.governanceDigest ? factRow('Governance digest', details.governanceDigest) : nothing}
          ${details.evidenceDigest ? factRow('Evidence digest', details.evidenceDigest) : nothing}
        </ul>
        <details class="recorded-scope"><summary>Recorded model scope · ${details.materializationScope.length}</summary>
          ${details.materializationScope.length ? html`<ul class="model-list">${details.materializationScope.map((model) => html`<li><a href=${this.modelExecutionHref(page, model)}><code>${model}</code><span class="model-outcome">Inspect execution</span></a></li>`)}</ul>` : html`<p class="empty-state">No materialization scope was recorded.</p>`}
        </details>
      </details>
    </section>`
  }

  private showFailedModel(page: PipelineRunDetailPageSignal): void {
    const failed = page.execution.models.find((model) => model.status === 'failed')
    if (!failed) return
    this.selectedModelID = failed.modelId
    this.mobileExecutionView = 'models'
    this.updateComplete.then(() => this.renderRoot.querySelector('.models-panel')?.scrollIntoView({ block: 'start' }))
  }

  private selectedModel(page: PipelineRunDetailPageSignal): PipelineRunModelSignal | undefined {
    const models = page.execution.models
    const requestedID = this.selectedModelID || new URL(window.location.href).searchParams.get('model') || ''
    const selected = models.find((model) => model.modelId === requestedID)
    if (selected) return selected
    return models.find((model) => model.status === 'failed') || models[0]
  }

  private modelExecutionHref(page: PipelineRunDetailPageSignal, modelID: string): string {
    return `/pipelines/${encodeURIComponent(page.pipelineId)}/runs/${encodeURIComponent(page.runId)}?section=execution&model=${encodeURIComponent(modelID)}`
  }

  private modelHref(page: PipelineRunDetailPageSignal, model: PipelineRunModelSignal | undefined): string | undefined {
    if (!model) return undefined
    return page.execution.graph?.nodes.find((node) => graphNodeMatchesModel(node, model.modelId))?.href
  }

  private modelLabel(page: PipelineRunDetailPageSignal, model: PipelineRunModelSignal): string {
    return page.execution.graph?.nodes.find((node) => graphNodeMatchesModel(node, model.modelId))?.label || humanize(model.modelId.replace(/^model:/, ''))
  }

  private selectGraphNode(page: PipelineRunDetailPageSignal, modelID: string | undefined): AssetLineageGraphSignal {
    const graph = page.execution.graph!
    return {
      ...graph,
      nodes: graph.nodes.map((node) => {
        const model = page.execution.models.find((candidate) => graphNodeMatchesModel(node, candidate.modelId))
        const pipeline = node.id === page.pipelineId
        return {
          ...node,
          ...(modelID ? { selected: graphNodeMatchesModel(node, modelID) } : {}),
          ...(model?.status ? { runStatus: model.status, runStatusLabel: model.statusLabel || humanize(model.status), runAnimate: model.status === 'running' && this.streamState === 'live' } : {}),
          ...(pipeline ? { runStatus: page.status, runStatusLabel: page.statusLabel } : {}),
        }
      }),
    }
  }

  private readonly handleGraphSelection = (event: Event): void => {
    const id = (event as CustomEvent<{ id?: string }>).detail?.id
    const model = this.page?.execution.models.find((candidate) => id &&
      this.page?.execution.graph?.nodes.some((node) => node.id === id && graphNodeMatchesModel(node, candidate.modelId)))
    if (!model) return
    this.selectedModelID = model.modelId
    if (window.matchMedia('(max-width: 800px)').matches) this.mobileExecutionView = 'models'
  }

  private readonly handleGraphScopeChange = (event: Event): void => {
    this.fullRunGraph = (event as CustomEvent<{ scope?: string }>).detail?.scope === 'full'
  }

  private readonly handleStreamFetch = (event: Event): void => {
    const detail = (event as CustomEvent<{ type?: string, el?: Element }>).detail
    if (!detail || detail.el !== document.querySelector('main[data-init]')) return
    if (detail.type === 'retrying') this.streamState = 'reconnecting'
    else if (detail.type === 'retries-failed' || detail.type === 'finished' || detail.type === 'error') this.streamState = 'lost'
  }

  private readonly handleStreamPatch = (event: Event): void => {
    const patch = (event as CustomEvent<Record<string, unknown>>).detail
    const runtime = patch?.runtime
    const newStream = runtime && typeof runtime === 'object' && Object.hasOwn(runtime, 'streamInstanceId')
    if (this.page && patch && (Object.hasOwn(patch, 'page') || newStream)) this.streamState = 'live'
  }

  private readonly handleViewportChange = (event: MediaQueryListEvent): void => {
    this.mobileViewport = event.matches
  }

  private tabHref(page: PipelineRunDetailPageSignal, section: string): string {
    return `/pipelines/${encodeURIComponent(page.pipelineId)}/runs/${encodeURIComponent(page.runId)}?section=${section}`
  }
}

function graphNodeMatchesModel(node: AssetLineageGraphSignal['nodes'][number], modelID: string): boolean {
  if (node.kind !== 'model') return false
  return node.id === modelID || node.id === `model:${modelID}` || node.meta === modelID
}

function fact(label: string, value: string, code = false) {
  return html`<div class="fact"><span>${label}</span>${code ? html`<code>${value}</code>` : html`<strong>${value}</strong>`}</div>`
}

function factRow(label: string, value: string) {
  return html`<li><span>${label}</span><code>${value}</code><button type="button" aria-label=${`Copy ${label}`} @click=${() => navigator.clipboard?.writeText(value)}>Copy</button></li>`
}

function statusTone(status: string): string {
  switch (status) {
    case 'succeeded': return 'tone-success'
    case 'failed': return 'tone-danger'
    case 'prepared': return 'tone-warning'
    default: return ''
  }
}

function modelOutcomeLabel(model: PipelineRunModelSignal, incomplete: boolean): string {
  return model.statusLabel || (incomplete ? 'Unknown' : 'In plan')
}

function shortRunID(id: string): string {
  return id.length > 16 ? id.slice(0, 8) : id
}

function isPipelineRunActive(status: string): boolean {
  return status === 'queued' || status === 'running' || status === 'prepared'
}

function publicationLabel(outcome: PipelineRunDetailPageSignal['execution']['publicationOutcome']): string {
  switch (outcome) {
    case 'published': return 'Published'
    case 'not_published': return 'Not published'
    case 'pending': return 'Pending'
    default: return 'Unverified'
  }
}

function publicationTone(outcome: PipelineRunDetailPageSignal['execution']['publicationOutcome']): string {
  return outcome === 'published' ? 'tone-success' : outcome === 'not_published' ? 'tone-danger' : outcome === 'pending' ? 'tone-warning' : ''
}

function validationOutcomeLabel(outcome: PipelineRunDetailPageSignal['execution']['validationOutcome']): string {
  return outcome === 'passed' ? 'Passed' : outcome === 'failed' ? 'Failed' : 'Not recorded'
}

function formatRunDate(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value || 'Not available'
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' }).format(date) + ' UTC'
}

function humanize(value: string): string {
  if (!value) return 'Not recorded'
  return value.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function eventLabel(value: string): string {
  const action = value.startsWith('refresh.') ? value.slice('refresh.'.length) : value
  return humanize(action.replaceAll('.', ' '))
}

function queuePhase(page: PipelineRunDetailPageSignal): string {
  if (page.execution.startedAt) {
    const created = Date.parse(page.execution.createdAt)
    const started = Date.parse(page.execution.startedAt)
    if (!Number.isFinite(created) || !Number.isFinite(started) || started < created) return 'Wait time unavailable'
    return `Waited ${formatElapsed(started - created)}`
  }
  return page.status === 'queued' ? 'Waiting to start' : 'Start time not recorded'
}

function modelsProgress(page: PipelineRunDetailPageSignal): string {
  if (page.execution.models.some((model) => model.status === 'failed')) return 'Failed'
  if (page.execution.models.some((model) => model.status === 'running')) return 'Running'
  if (page.execution.modelsUnavailable) return 'Evidence incomplete'
  if (page.execution.models.length === 0) return 'Not recorded'
  if (page.execution.models.every((model) => model.status === 'succeeded')) return 'Succeeded'
  return 'Incomplete'
}

function publicationProgress(page: PipelineRunDetailPageSignal): string {
  switch (page.execution.publicationOutcome) {
    case 'published': return 'Completed'
    case 'pending': return 'Pending'
    case 'not_published': return '—'
    default: return 'Unverified'
  }
}

function formatElapsed(milliseconds: number): string {
  const totalSeconds = Math.floor(milliseconds / 1000)
  if (totalSeconds < 60) return `${totalSeconds}s`
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes < 60) return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`
  const hours = Math.floor(minutes / 60)
  const remainingMinutes = minutes % 60
  return remainingMinutes ? `${hours}h ${remainingMinutes}m` : `${hours}h`
}

customElements.define('lv-pipeline-run-page', LeapViewPipelineRunPage)
