import { LitElement, css, html, nothing } from 'lit'
import { state } from 'lit/decorators.js'
import type { AssetLineageGraphSignal, PipelineCommandStatusSignal, PipelineDetailPageSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { checkSignalContract } from '../shared/signal-contract'
import { breadcrumbStyles, renderAssetBreadcrumbGlyph, renderBreadcrumb } from '../shared/breadcrumb'
import { projectBaseStyles } from './project-page-base.styles'
import { renderAssetPageShell } from './asset-page-shell'
import { renderAssetConfiguration } from './asset-configuration'
import './pipeline-runs-list'

class PipelineDetailPage extends DatastarLit(LitElement) {
  @state() private fullDependencyGraph = false

  static styles = [projectBaseStyles, breadcrumbStyles, css`
    .pipeline-overview { display: grid; min-width: 0; gap: var(--base-size-16); }
    .asset-description { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .run-action:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .run-action:disabled { opacity: .6; cursor: progress; }
    .command-feedback { margin: 0; color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .command-feedback.is-error { color: var(--lv-fg-danger); }
    .definition-body > .command-feedback { padding: var(--base-size-16); }
    .panel { display: grid; min-width: 0; gap: var(--base-size-12); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-16); }
    .panel h2, .panel p { margin: 0; }
    .panel h2 { font: var(--lv-type-section-title); }
    .panel p, .muted { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .muted { overflow: visible; text-overflow: clip; white-space: normal; }
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
    .fact { min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); }
    .fact dt { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .fact dd { min-width: 0; margin: 0; overflow-wrap: anywhere; font: var(--lv-type-body); }
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
    .secondary > .details-content { display: grid; gap: var(--base-size-12); margin-top: var(--base-size-12); padding: 0; }
    .technical-identity { border-top: var(--lv-border-muted); padding-top: var(--base-size-12); }
    .technical-facts { margin-top: var(--base-size-12); }

    @media (max-width: 780px) {
      .fact-grid { grid-template-columns: minmax(0, 1fr); }
      .overview-summary { grid-template-columns: minmax(0, 1fr); }
      .summary-item + .summary-item { border-top: var(--lv-border-muted); border-left: 0; padding-top: var(--base-size-8); padding-left: 0; }
      .panel-heading { align-items: start; flex-direction: column; gap: var(--base-size-4); }
      .panel-heading p { text-align: left; }
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
    return renderAssetPageShell({
      label: 'Pipeline detail',
      breadcrumb: renderBreadcrumb([{ label: 'Pipelines', href: '/pipelines' }, { label: page.title, current: true, prefix: renderAssetBreadcrumbGlyph('refresh_pipeline') }]),
      actions: page.canRun ? html`<button class="action-link run-action" type="button" ?disabled=${this.commandStatus.loading} @click=${this.runNow}>${this.commandStatus.loading ? 'Queuing…' : 'Run now'}</button>` : nothing,
      tabs: page.tabs,
      bodyClass: page.activeTab === 'definition' ? 'section-body definition-body' : 'section-body',
      body: html`${this.commandStatus.error ? html`<p class="command-feedback is-error" role="alert">${this.commandStatus.error}</p>` : this.commandStatus.message ? html`<p class="command-feedback" role="status">${this.commandStatus.message}</p>` : nothing}
        ${page.activeTab === 'runs' ? this.renderRuns(page) : page.activeTab === 'definition' ? this.renderDefinition(page) : html`<div class="pipeline-overview">${page.asset.description ? html`<p class="asset-description">${page.asset.description}</p>` : nothing}${this.renderOverview(page)}</div>`}`,
    })
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
    const baseHref = page.tabs.find((tab) => tab.id === 'runs')?.href || `/pipelines/${encodeURIComponent(page.asset.id)}/runs`
    return html`<section aria-label="Pipeline runs">
      <lv-pipeline-runs-list .monitor=${page.runMonitor ?? null} .table=${page.runsTable ?? { columns: [], rows: [], empty: 'No runs match these filters.' }} .baseHref=${baseHref} .fixedPipelineId=${page.asset.id}></lv-pipeline-runs-list>
    </section>`
  }

  private handleGraphScopeChange = (event: Event): void => {
    this.fullDependencyGraph = (event as CustomEvent<{ scope?: string }>).detail?.scope === 'full'
  }

  private renderDefinition(page: PipelineDetailPageSignal) {
    return html`<section class="details definition" id="definition" aria-label="Asset definition">
      <div class="details-content">
        ${page.definitionYaml
          ? renderAssetConfiguration(page.definitionYaml, 'yaml', 'raw')
          : html`<p class="empty">Authored YAML is unavailable for this serving generation.</p>`}
        <details class="technical-identity">
          <summary>Technical details</summary>
          <dl class="fact-grid technical-facts">
            <div class="fact"><dt>Source file</dt><dd><code>${page.asset.sourceFile || '—'}</code></dd></div>
            <div class="fact"><dt>Resource ID</dt><dd><code>${page.asset.id}</code></dd></div>
            <div class="fact"><dt>Content hash</dt><dd><code>${page.asset.contentHash || '—'}</code></dd></div>
          </dl>
        </details>
      </div>
    </section>`
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
