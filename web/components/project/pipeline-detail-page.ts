import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import type { AssetLineageGraphSignal, PipelineCommandStatusSignal, PipelineDetailPageSignal, PipelineRunMonitorSignal, RecordTableSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { pageHeaderStyles } from '../shared/page-header'
import { breadcrumbStyles, renderAssetBreadcrumbGlyph, renderBreadcrumb } from '../shared/breadcrumb'
import type { EntityListItem } from '../shared/entity-list'
import { capitalize, firstRunListValue, formatRunDetailDate, formatRunListDate, runDetailValue, shortFailureReason } from './pipelines-page-format'
import '../shared/entity-list'

const pipelineRunStatuses = ['queued', 'running', 'prepared', 'succeeded', 'failed', 'cancelled', 'superseded', 'skipped'] as const

class PipelineDetailPage extends DatastarLit(LitElement) {
  @state() private fullDependencyGraph = false
  @state() private copiedDefinition = false

  static styles = [pageHeaderStyles, breadcrumbStyles, css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      background: var(--lv-bg-app);
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    .page { box-sizing: border-box; display: grid; width: min(100%, var(--lv-page-content-max-width)); min-width: 0; min-height: 100svh; align-content: start; gap: var(--base-size-16); margin-inline: auto; padding: var(--base-size-24); }
    .pipeline-context { display: flex; justify-content: space-between; align-items: center; gap: var(--base-size-16); }
    .run-action { min-height: var(--control-medium-size); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding-inline: var(--base-size-12); font: var(--lv-type-body); cursor: pointer; white-space: nowrap; }
    .run-action:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .run-action:disabled { opacity: .6; cursor: progress; }
    .command-feedback { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .command-feedback.is-error { color: var(--lv-fg-danger); }
    .tabs { display: flex; gap: var(--base-size-4); border-bottom: var(--lv-border-muted); overflow-x: auto; }
    .tabs a { display: inline-flex; align-items: center; min-height: var(--control-medium-size); padding-inline: var(--base-size-12); border-bottom: 2px solid transparent; color: var(--lv-fg-muted); font: var(--lv-type-body); text-decoration: none; white-space: nowrap; }
    .tabs a[aria-current="page"] { border-bottom-color: var(--lv-fg-accent); color: var(--lv-fg-default); }
    .tabs a:focus-visible, a:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .panel { display: grid; min-width: 0; gap: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-16); }
    .panel h2, .panel p { margin: 0; }
    .panel h2 { font: var(--lv-type-section-title); }
    .panel p, .muted { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .overview-summary { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-12) var(--base-size-16); }
    .summary-item { display: grid; min-width: 0; align-content: center; gap: var(--base-size-4); }
    .summary-item + .summary-item { border-left: var(--lv-border-muted); padding-left: var(--base-size-16); }
    .summary-item:nth-child(odd) { border-left: 0; padding-left: 0; }
    .summary-item:nth-child(n+3) { border-top: var(--lv-border-muted); padding-top: var(--base-size-8); }
    .summary-label { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .summary-value { min-width: 0; overflow-wrap: anywhere; font: var(--lv-type-body); }
    .summary-value a { color: var(--lv-fg-accent); }
    .panel-heading { display: flex; min-width: 0; align-items: baseline; justify-content: space-between; gap: var(--base-size-12); }
    .panel-heading p { margin: 0; text-align: right; }
    .fact-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--base-size-12); margin: 0; }
    .fact { min-width: 0; display: grid; gap: var(--base-size-4); }
    .fact dt { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .fact dd { min-width: 0; margin: 0; overflow-wrap: anywhere; font: var(--lv-type-body); }
    .code { overflow: auto; max-height: 36rem; margin: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); font: var(--lv-type-body-compact); font-family: var(--fontStack-monospace); white-space: pre; }
    .status { display: inline-flex; align-items: center; width: fit-content; border: var(--lv-border-muted); border-radius: 999px; padding: var(--base-size-4) var(--base-size-8); font: var(--lv-type-caption); text-transform: capitalize; }
    .status.is-failed, .status.is-error { color: var(--lv-fg-danger); }
    .status.is-succeeded { color: var(--lv-fg-success, var(--lv-fg-default)); }
    .consumer a, .fact a { color: var(--lv-fg-accent); overflow-wrap: anywhere; }
    .consumer-list { display: grid; gap: var(--base-size-8); }
    .consumer { display: flex; align-items: baseline; justify-content: space-between; gap: var(--base-size-8); }
    .dependency-list { display: grid; gap: var(--base-size-4); margin: 0; padding-left: var(--base-size-24); font: var(--lv-type-body); }
    .dependency-list a { color: var(--lv-fg-accent); }
    .dependency-kind { margin-left: var(--base-size-8); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .lineage-graph { display: block; height: clamp(20rem, 47vh, 32rem); min-width: 0; }
    .empty { border: var(--lv-border-muted); border-radius: var(--lv-radius-default); color: var(--lv-fg-muted); padding: var(--base-size-16); font: var(--lv-type-body); }
    .secondary { display: grid; gap: var(--base-size-12); }
    .secondary details > summary, .technical-identity > summary, .dependency-resources > summary { cursor: pointer; font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .secondary details > summary:focus-visible, .technical-identity > summary:focus-visible, .dependency-resources > summary:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .details-content { display: grid; gap: var(--base-size-12); margin-top: var(--base-size-12); }
    .run-filters { display: grid; grid-template-columns: minmax(12rem, 1.5fr) repeat(3, minmax(9rem, 1fr)) auto auto; align-items: center; gap: var(--base-size-8); }
    .run-filters input, .run-filters select, .run-filters button { box-sizing: border-box; min-width: 0; min-height: var(--control-medium-size); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding-inline: var(--base-size-8); font: var(--lv-type-body); }
    .run-filters button { cursor: pointer; }
    .run-filters a { color: var(--lv-fg-accent); white-space: nowrap; }
    .run-table { min-width: 0; }
    .run-pagination { display: flex; justify-content: space-between; align-items: center; gap: var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .run-pagination-actions { display: flex; gap: var(--base-size-12); }
    .run-pagination-actions a { color: var(--lv-fg-accent); }
    .technical-identity { border-top: var(--lv-border-muted); padding-top: var(--base-size-12); }
    .definition-heading { display: flex; min-width: 0; align-items: center; justify-content: space-between; gap: var(--base-size-12); }
    .definition-heading code { min-width: 0; overflow-wrap: anywhere; font: var(--lv-type-body); font-family: var(--fontStack-monospace); }
    .definition-heading button { flex: none; min-height: var(--control-medium-size); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); color: var(--lv-fg-default); padding-inline: var(--base-size-12); font: var(--lv-type-body); cursor: pointer; }

    @media (max-width: 780px) {
      .page { padding: var(--base-size-12); }
      .fact-grid { grid-template-columns: minmax(0, 1fr); }
      .overview-summary { grid-template-columns: minmax(0, 1fr); }
      .summary-item + .summary-item { border-top: var(--lv-border-muted); border-left: 0; padding-top: var(--base-size-8); padding-left: 0; }
      .panel-heading { align-items: start; flex-direction: column; gap: var(--base-size-4); }
      .panel-heading p { text-align: left; }
      .run-filters { grid-template-columns: repeat(2, minmax(0, 1fr)); }
      .run-filters input[type='search'] { grid-column: 1 / -1; }
      .run-filters a { justify-self: end; }
      .run-pagination { align-items: flex-start; flex-direction: column; }
    }
  `]

  get page(): PipelineDetailPageSignal | null {
    return this.signal<PipelineDetailPageSignal | null>('page', null)
  }

  get commandStatus(): PipelineCommandStatusSignal {
    return this.signal<PipelineCommandStatusSignal>('pipelineCommandStatus', { loading: false, error: '', message: '' })
  }

  updated(): void {
    checkSignalContract('pipeline detail', this.page, { kind: 'required', graph: 'required', asset: 'required', tabs: 'required' })
  }

  render() {
    const page = this.page
    if (!page) return html`<slot></slot>`
    return html`
      <section class="page" aria-label="Pipeline detail">
        ${renderBreadcrumb([{ label: 'Pipelines', href: '/pipelines' }, { label: page.title, current: true, prefix: renderAssetBreadcrumbGlyph('refresh_pipeline') }])}
        <div class="page-header pipeline-context">
          <div class="page-title-block">
            <h1 class="page-title">${page.title}</h1>
            ${page.asset.description ? html`<p class="page-detail">${page.asset.description}</p>` : nothing}
          </div>
          ${page.canRun ? html`<button class="run-action" type="button" ?disabled=${this.commandStatus.loading} @click=${this.runNow}>${this.commandStatus.loading ? 'Queuing…' : 'Run now'}</button>` : nothing}
        </div>
        ${this.commandStatus.error ? html`<p class="command-feedback is-error" role="alert">${this.commandStatus.error}</p>` : this.commandStatus.message ? html`<p class="command-feedback" role="status">${this.commandStatus.message}</p>` : nothing}
        <nav class="tabs" aria-label="Pipeline sections">
          ${page.tabs.map((tab) => html`<a href=${tab.href} aria-current=${tab.active ? 'page' : nothing}>${tab.label}</a>`)}
        </nav>
        ${page.activeTab === 'runs' ? this.renderRuns(page) : page.activeTab === 'definition' ? this.renderDefinition(page) : this.renderOverview(page)}
      </section>
    `
  }

  private runNow = (): void => {
    const page = this.page
    if (!page?.canRun || this.commandStatus.loading) return
    this.dispatchEvent(new CustomEvent('lv-pipeline-command', {
      bubbles: true,
      composed: true,
      detail: { action: 'run', pipelineId: page.asset.id, assetId: page.asset.id, runId: '' },
    }))
  }

  private renderOverview(page: PipelineDetailPageSignal) {
    return html`
      <section class="overview-summary" aria-label="Pipeline summary">
        <div class="summary-item">
          <span class="summary-label">Refreshes</span>
          <strong class="summary-value">${page.semanticModel ? html`<a href=${page.semanticModel.href}>${page.semanticModel.label}</a>` : 'Semantic model unavailable'}</strong>
        </div>
        <div class="summary-item">
          <span class="summary-label">Schedule</span>
          <strong class="summary-value">${page.schedules.length ? page.schedules.map((schedule) => schedule.cron).join(' · ') : 'Manual'}</strong>
          ${page.schedules.length ? html`<span class="muted">${page.timezone || 'UTC'} · ${page.nextRunAt ? `Next ${formatDateTime(page.nextRunAt, page.timezone)}` : 'Next run unavailable'}</span>` : nothing}
        </div>
        <div class="summary-item">
          <span class="summary-label">Latest run</span>
          <strong class="summary-value">${page.latestRun ? html`<a href=${page.latestRun.href}><span class=${`status is-${page.latestRun.status}`}>${pipelineStatusLabel(page.latestRun.status)}</span> · ${page.latestRun.duration || 'Duration unavailable'} · ${formatDateTime(page.latestRun.startedAt, page.timezone)}</a>` : 'Never run'}</strong>
        </div>
        <div class="summary-item">
          <span class="summary-label">Last published</span>
          <strong class="summary-value">${page.publicationStatus === 'confirmed' && page.publication
            ? page.publication.runId
              ? html`<a href=${`/pipelines/${encodeURIComponent(page.asset.id)}/runs/${encodeURIComponent(page.publication.runId)}`}>${formatDateTime(page.publication.publishedAt, page.timezone)}</a>`
              : formatDateTime(page.publication.publishedAt, page.timezone)
            : page.publicationStatus === 'unavailable' ? 'Publication provenance unavailable' : 'No confirmed publication'}</strong>
        </div>
      </section>
      <section class="panel" aria-labelledby="pipeline-dependencies-title">
        <div class="panel-heading">
          <h2 id="pipeline-dependencies-title">${this.fullDependencyGraph ? 'Full dependency graph' : 'Direct dependencies'}</h2>
        </div>
        ${page.graph.nodes.length
          ? html`<lv-asset-lineage-graph class="lineage-graph" .graph=${page.graph as AssetLineageGraphSignal} .scope=${this.fullDependencyGraph ? 'full' : 'focused'} scope-mode="dependencies" .dialogTitle=${`${page.title} · ${this.fullDependencyGraph ? 'Full dependency graph' : 'Direct dependencies'}`} @lv-lineage-scope-change=${this.handleGraphScopeChange}></lv-asset-lineage-graph>`
          : html`<p>No compiled source or model dependencies are available.</p>`}
        ${page.graph.nodes.length
          ? html`<details class="dependency-resources">
              <summary>Dependency list</summary>
              <ul class="dependency-list" aria-label="Dependency navigation">
                ${page.graph.nodes.map((node) => html`<li>
                  ${node.href ? html`<a href=${node.href}>${node.label}</a>` : html`<span>${node.label}</span>`}
                  ${node.selected ? html`<span class="muted">selected semantic model</span>` : nothing}
                  <span class="dependency-kind">${node.kind}</span>
                </li>`)}
              </ul>
            </details>`
          : nothing}
      </section>
      <details class="panel secondary">
        <summary>Schedule details</summary>
        <div class="details-content">
          <section aria-labelledby="pipeline-schedule-title">
            <h2 id="pipeline-schedule-title">Schedule</h2>
            ${page.schedules.length
              ? html`<dl class="fact-grid">
                  <div class="fact"><dt>Cron</dt><dd>${page.schedules.map((schedule) => html`<code>${schedule.cron}</code>`)}</dd></div>
                  <div class="fact"><dt>Timezone</dt><dd>${page.timezone || '—'}</dd></div>
                  <div class="fact"><dt>Next run</dt><dd>${formatDateTime(page.nextRunAt, page.timezone)}</dd></div>
                  <div class="fact"><dt>Overlap</dt><dd>${page.concurrencyPolicy || '—'}<p>${page.concurrencyDescription}</p></dd></div>
                  <div class="fact"><dt>Starting deadline</dt><dd>${page.startingDeadlineSeconds} seconds</dd></div>
                </dl>`
              : html`<p>Manual only. No schedule or scheduled-run overlap rule applies.</p>`}
          </section>
        </div>
      </details>
      <details class="panel secondary">
        <summary>Downstream dashboards · ${page.dashboardConsumers.length}</summary>
        <div class="details-content">
          <p>${page.dashboardConsumersNote}</p>
          ${page.dashboardConsumers.length
            ? html`<ul class="consumer-list">${page.dashboardConsumers.map((consumer) => html`<li class="consumer"><a href=${consumer.href}>${consumer.label}</a><span class="muted">${consumer.type}</span></li>`)}</ul>`
            : html`<p>No downstream dashboards are known for this semantic model.</p>`}
        </div>
      </details>
    `
  }

  private renderRuns(page: PipelineDetailPageSignal) {
    const monitor = page.runMonitor ?? { query: '', range: '24h', pipeline: page.asset.id, status: '', trigger: '', page: 1, pageSize: 25, total: 0 }
    const table: RecordTableSignal = page.runsTable ?? { columns: [], rows: [], empty: 'No runs match these filters.' }
    const start = monitor.total > (monitor.page - 1) * monitor.pageSize ? (monitor.page - 1) * monitor.pageSize + 1 : 0
    const end = Math.min(monitor.total, monitor.page * monitor.pageSize)
    const baseHref = page.tabs.find((tab) => tab.id === 'runs')?.href || `/pipelines/${encodeURIComponent(page.asset.id)}/runs`
    return html`<section class="panel" aria-label="Pipeline runs">
      <form class="run-filters" action=${baseHref} method="get" aria-label="Run history filters">
        <input type="search" name="q" placeholder="Search run ID" aria-label="Search pipeline runs" .value=${monitor.query}>
        <select name="range" aria-label="Filter runs by time range" .value=${monitor.range} @change=${this.submitRunFilters}>
          <option value="24h" ?selected=${monitor.range === '24h'}>Last 24 hours</option><option value="7d" ?selected=${monitor.range === '7d'}>Last 7 days</option><option value="30d" ?selected=${monitor.range === '30d'}>Last 30 days</option><option value="all" ?selected=${monitor.range === 'all'}>All time</option>
        </select>
        <select name="status" aria-label="Filter runs by status" .value=${monitor.status} @change=${this.submitRunFilters}>
          <option value="" ?selected=${monitor.status === ''}>All statuses</option>${pipelineRunStatuses.map((status) => html`<option value=${status} ?selected=${status === monitor.status}>${pipelineStatusLabel(status)}</option>`)}
        </select>
        <select name="trigger" aria-label="Filter runs by trigger" .value=${monitor.trigger} @change=${this.submitRunFilters}>
          <option value="" ?selected=${monitor.trigger === ''}>All triggers</option>${['manual', 'schedule'].map((trigger) => html`<option value=${trigger} ?selected=${trigger === monitor.trigger}>${capitalize(trigger)}</option>`)}
        </select>
        <button type="submit">Search</button>${monitor.query || monitor.status || monitor.trigger || monitor.range !== '24h' ? html`<a href=${baseHref}>Clear filters</a>` : nothing}
      </form>
      <div class="run-table">
        <lv-entity-list
          compact
          title-emphasis="normal"
          min-width="950px"
          list-label="Pipeline run history"
          empty-text="No runs match these filters."
          .showToolbar=${false}
          .items=${this.runItems(table)}
          .columns=${[
            { id: 'status', label: 'Status', width: '130px', render: 'status' },
            { id: 'name', label: 'Run ID', width: '200px' },
            { id: 'started', label: 'Started', width: '170px' },
            { id: 'duration', label: 'Duration', width: '100px' },
            { id: 'trigger', label: 'Trigger', width: '100px' },
          ]}
        ></lv-entity-list>
      </div>
      <nav class="run-pagination" aria-label="Run pages">
        <span>Showing ${start}–${end} of ${monitor.total} runs</span>
        <div class="run-pagination-actions">
          ${monitor.page > 1 ? html`<a href=${pipelineRunsPageHref(baseHref, monitor, monitor.page - 1)}>Previous</a>` : nothing}
          ${end < monitor.total ? html`<a href=${pipelineRunsPageHref(baseHref, monitor, monitor.page + 1)}>Next</a>` : nothing}
        </div>
      </nav>
    </section>`
  }

  private submitRunFilters = (event: Event): void => { (event.currentTarget as HTMLSelectElement).form?.requestSubmit() }

  private handleGraphScopeChange = (event: Event): void => {
    this.fullDependencyGraph = (event as CustomEvent<{ scope?: string }>).detail?.scope === 'full'
  }

  private runItems(table: RecordTableSignal): EntityListItem[] {
    return table.rows.map((row) => {
      const status = runDetailValue(row, 'status_value')
      const href = runDetailValue(row, 'run_href')
      const startedAt = String(row.started_at || '')
      return {
        id: String(row.run_id || row.id || ''),
        title: firstRunListValue(row.run, row.run_id, row.id),
        description: undefined,
        href: href === '—' ? undefined : href,
        columnHrefs: href === '—' ? undefined : { started: href },
        icon: 'none',
        columnDescriptions: status === 'failed' && runDetailValue(row, 'error') !== '—' ? { status: shortFailureReason(runDetailValue(row, 'error')) } : undefined,
        columns: {
          status: pipelineStatusLabel(status),
          started: formatRunListDate(startedAt),
          duration: runDetailValue(row, 'duration'),
          trigger: runDetailValue(row, 'trigger'),
        },
        columnTitles: { started: formatRunDetailDate(startedAt) },
        sortValues: { status, started: startedAt },
      }
    })
  }

  private renderDefinition(page: PipelineDetailPageSignal) {
    return html`<section class="panel" aria-label="Pipeline definition">
      <div class="definition-heading"><code>${page.asset.sourceFile || 'Pipeline definition'}</code><button type="button" ?disabled=${!page.definitionYaml} @click=${this.copyDefinition}>${this.copiedDefinition ? 'Copied' : 'Copy'}</button></div>
      ${page.definitionYaml
        ? html`<pre class="code"><code>${page.definitionYaml}</code></pre>`
        : html`<p class="empty">Authored YAML is unavailable for this serving generation.</p>`}
      <details class="technical-identity">
        <summary>Technical details</summary>
        <dl class="fact-grid details-content">
          <div class="fact"><dt>Resource ID</dt><dd><code>${page.asset.id}</code></dd></div>
          <div class="fact"><dt>Content hash</dt><dd><code>${page.asset.contentHash || '—'}</code></dd></div>
        </dl>
      </details>
    </section>`
  }

  private copyDefinition = async (): Promise<void> => {
    if (!this.page?.definitionYaml) return
    try {
      await navigator.clipboard.writeText(this.page.definitionYaml)
      this.copiedDefinition = true
    } catch {
      this.copiedDefinition = false
    }
  }
}

if (!customElements.get('lv-pipeline-detail-page')) customElements.define('lv-pipeline-detail-page', PipelineDetailPage)

function formatDateTime(value: string | undefined, timezone: string): string {
  if (!value) return '—'
  const date = new Date(value)
  if (!Number.isFinite(date.getTime())) return value
  const displayTimezone = timezone || 'UTC'
  try {
    const formatted = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short', timeZone: displayTimezone }).format(date)
    return `${formatted} · ${displayTimezone}`
  } catch {
    const formatted = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' }).format(date)
    return `${formatted} · UTC`
  }
}

function pipelineStatusLabel(status: string): string {
  if (status === 'prepared') return 'Finalizing'
  if (!status) return 'Unknown'
  return status.charAt(0).toUpperCase() + status.slice(1)
}

function pipelineRunsPageHref(baseHref: string, monitor: PipelineRunMonitorSignal, page: number): string {
  const params = new URLSearchParams({ q: monitor.query, range: monitor.range, status: monitor.status, trigger: monitor.trigger, page: String(page) })
  return `${baseHref}?${params.toString()}`
}
