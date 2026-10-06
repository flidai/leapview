import { LitElement, css, html, type PropertyValues } from 'lit'
import { property } from 'lit/decorators.js'
import type { DataExploreCommand, DataExploreResultSignal } from '../../generated/signals'
import { emptyDataExploreCommand, explorationResultKeyForSort, explorationSortFieldForResult, explorationSpecFor } from './data-explorer-spec'
import '../shared/windowed-table'
import { exploreTablePresentation } from './explore-table-presentation'
import type { WindowedTablePayload, WindowedTableRequest } from '../shared/windowed-table'

const emptyResult: DataExploreResultSignal = {
  columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [],
}

class DataExploreTable extends LitElement {
  @property({ attribute: false }) command: DataExploreCommand = emptyDataExploreCommand
  @property({ attribute: false }) result: DataExploreResultSignal = emptyResult
  @property({ attribute: false }) visibleColumns: string[] = []

  private pendingWindows: WindowedTableRequest[] = []
  private activeWindow?: WindowedTableRequest

  protected updated(changed: PropertyValues): void {
    if (changed.has('command') && this.activeWindow && (this.command.action === 'stop' || this.command.resetVersion !== this.activeWindow.resetVersion)) {
      this.activeWindow = undefined
      this.pendingWindows = []
    }
    if (!changed.has('result') || !this.activeWindow) return
    const response = this.result.window
    const acknowledged = response && Object.values(response.blocks).some(block =>
      block.requestSeq === this.activeWindow?.requestSeq && block.resetVersion === this.activeWindow?.resetVersion)
    if (acknowledged || this.result.error) {
      this.activeWindow = undefined
      if (this.result.error) this.pendingWindows = []
      const next = this.pendingWindows.shift()
      if (next) this.dispatchWindow(next)
    } else if (response && response.resetVersion !== this.activeWindow.resetVersion) {
      this.activeWindow = undefined
      this.pendingWindows = []
    }
  }

  static styles = css`
    :host {
      display: grid;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    lv-windowed-table {
      min-width: 0;
      min-height: 0;
      --lv-windowed-table-surface: var(--lv-bg-app);
    }
  `

  render() {
    return html`<lv-windowed-table
      compact
      .table=${this.tablePayload()}
      @lv-windowed-table-request=${this.forwardWindow}
      @lv-windowed-table-column-widths=${this.forwardColumnWidths}
    ></lv-windowed-table>`
  }

  private tablePayload(): WindowedTablePayload {
    const command = this.command ?? emptyDataExploreCommand
    const spec = explorationSpecFor(command)
    const result = this.result ?? emptyResult
    const sort = spec.sort?.[0]
    const resultSortKey = sort
      ? explorationResultKeyForSort(spec, sort.field, (result.columns ?? []).map((column) => column.key)) ?? sort.field
      : ''
    const rows = result.rows ?? []
    return {
      tableKey: `${spec.modelId ?? ''}:${spec.datasetId ?? ''}:explore`,
      title: 'Exploration results',
      ...exploreTablePresentation(spec, result.columns ?? [], command.columnWidths),
      totalRows: result.window?.totalRows ?? rows.length,
      availableRows: result.window?.availableRows ?? rows.length,
      chunkSize: result.window?.chunkSize ?? 100,
      resetVersion: result.window?.resetVersion ?? command.resetVersion ?? 0,
      sort: { key: resultSortKey, column: resultSortKey, direction: sort?.direction ?? '' },
      blocks: result.window?.blocks ?? {
        a: {
          start: 0,
          requestSeq: result.requestSeq ?? command.requestSeq ?? 0,
          resetVersion: command.resetVersion ?? 0,
          sort: { key: resultSortKey, column: resultSortKey, direction: sort?.direction ?? '' },
          rows,
        },
      },
      error: result.error,
      visibleColumns: this.visibleColumns,
      columnWidths: command.columnWidths ?? {},
      totalLabel: result.window?.totalRowLabel ?? (result.truncated ? `${rows.length}+ rows` : `${rows.length} rows`),
    }
  }

  private forwardWindow = (event: CustomEvent<WindowedTableRequest>): void => {
    event.stopPropagation()
    const request = event.detail
    if (request.block === 'all') this.pendingWindows = []
    if (this.activeWindow) {
      this.pendingWindows.push(request)
      return
    }
    this.dispatchWindow(request)
  }

  private dispatchWindow(request: WindowedTableRequest): void {
    this.activeWindow = request
    const resultKey = request.sort.key ?? request.sort.column ?? ''
    const direction = request.sort.direction
    const spec = explorationSpecFor(this.command)
    const field = explorationSortFieldForResult(spec, resultKey)
    const current = spec.sort?.[0]
    const sortChanged = field && (direction === 'asc' || direction === 'desc')
      && (current?.field !== field || current.direction !== direction)
    this.dispatchEvent(new CustomEvent('lv-data-explore-table-window', {
      bubbles: true,
      composed: true,
      detail: {
        window: { block: request.block, start: request.start, count: request.count, requestSeq: request.requestSeq, resetVersion: request.resetVersion },
        ...(sortChanged ? { spec: { ...spec, sort: [{ field, direction }] } } : {}),
      } satisfies Partial<DataExploreCommand>,
    }))
  }

  private forwardColumnWidths = (event: CustomEvent<{ columnWidths?: Record<string, number> }>): void => {
    event.stopPropagation()
    this.dispatchEvent(new CustomEvent('lv-data-explore-table-command', {
      bubbles: true,
      composed: true,
      detail: { columnWidths: event.detail?.columnWidths ?? {} },
    }))
  }
}

if (!customElements.get('lv-data-explore-table')) customElements.define('lv-data-explore-table', DataExploreTable)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explore-table': DataExploreTable
  }
}
