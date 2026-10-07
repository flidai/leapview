import { html, nothing } from 'lit'
import type { DashboardStatus, ResourceAssetPageSignal } from '../../generated/signals'

export const emptyLineageStatus: DashboardStatus = {
  error: '', generation: 0, lastUpdated: '', loading: false,
  progressPercent: 0, refreshId: '', setupRequired: false,
}

export function renderAssetLineage(lineage: ResourceAssetPageSignal['lineage'], status: DashboardStatus) {
  const hasGraph = Boolean(lineage && ((lineage.graph.nodes?.length ?? 0) > 0 || (lineage.graph.edges?.length ?? 0) > 0))
  const hasTables = Boolean(lineage && ((lineage.usesTable.rows?.length ?? 0) > 0 || (lineage.usedByTable.rows?.length ?? 0) > 0))
  const state = !lineage && status.error ? 'error' : !lineage && status.loading ? 'loading' : !lineage ? 'empty' : hasGraph || hasTables ? 'ready' : 'empty'
  return html`
    <section class="lineage" id="lineage" aria-label="Asset lineage">
      ${lineage && (status.error || status.loading) ? html`<div class="lineage-notice" role=${status.error ? 'alert' : 'status'}>
        ${status.error ? `Lineage could not be refreshed: ${status.error}` : 'Refreshing lineage…'}
      </div>` : nothing}
      ${state === 'ready'
        ? html`<lv-asset-lineage-graph class="lineage-graph" .scope=${'full'} .graph=${lineage!.graph}></lv-asset-lineage-graph>`
        : html`<div class=${`lineage-state lineage-state-${state}`} role=${state === 'error' ? 'alert' : 'status'}>
            ${state === 'loading' ? html`<lv-loading-spinner size="small" aria-hidden="true"></lv-loading-spinner>` : nothing}
            <span>${state === 'loading' ? 'Loading lineage…' : state === 'error' ? status.error : 'No lineage dependencies are available for this asset.'}</span>
          </div>`}
      ${lineage ? html`<div class="lineage-notice">These dependencies describe the asset opened on this page. Select a graph node to trace its path, or open an asset to explore its own lineage.</div><div class="lineage-grids">
        ${renderTable('Uses', lineage.usesTable)}
        ${renderTable('Used by', lineage.usedByTable)}
      </div>` : nothing}
    </section>
  `
}

function renderTable(title: string, table: NonNullable<ResourceAssetPageSignal['lineage']>['usesTable']) {
  return html`<section class="detail-section" aria-label=${title}><h2>${title}</h2><lv-record-table .table=${table}></lv-record-table></section>`
}
