import { LitElement, css, html } from 'lit'
import { property } from 'lit/decorators.js'
import type { DataExploreCommand, DataExploreResultSignal } from '../../generated/signals'
import '../shared/windowed-table'
import type { WindowedTableColumn, WindowedTablePayload, WindowedTableRequest } from '../shared/windowed-table'

const emptyCommand: DataExploreCommand = {
  dimensions: [], measures: [], filters: [], sort: [], limit: 100, requestSeq: 0, resetVersion: 0,
}

const emptyResult: DataExploreResultSignal = {
  columns: [], rows: [], rowsReturned: 0, durationMs: 0, requestSeq: 0, truncated: false, warnings: [],
}

class DataExploreTable extends LitElement {
  @property({ attribute: false }) command: DataExploreCommand = emptyCommand
  @property({ attribute: false }) result: DataExploreResultSignal = emptyResult
  @property({ attribute: false }) visibleColumns: string[] = []

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
      @lv-windowed-table-request=${this.forwardSort}
      @lv-windowed-table-column-widths=${this.forwardColumnWidths}
    ></lv-windowed-table>`
  }

  private tablePayload(): WindowedTablePayload {
    const command = this.command ?? emptyCommand
    const result = this.result ?? emptyResult
    const sort = command.sort?.[0]
    const rows = result.rows ?? []
    return {
      tableKey: `${command.modelId ?? ''}:${command.datasetId ?? ''}:explore`,
      title: 'Exploration results',
      columns: (result.columns ?? []).map((column): WindowedTableColumn => ({
        key: column.key,
        label: column.label || column.key,
        type: column.type,
        align: isNumericType(column.type) ? 'right' : 'left',
        sortable: true,
      })),
      totalRows: rows.length,
      availableRows: rows.length,
      chunkSize: Math.max(command.limit || 100, 1),
      rowHeight: 32,
      resetVersion: command.resetVersion ?? 0,
      sort: { key: sort?.field ?? '', column: sort?.field ?? '', direction: sort?.direction ?? '' },
      blocks: {
        a: {
          start: 0,
          requestSeq: result.requestSeq ?? command.requestSeq ?? 0,
          resetVersion: command.resetVersion ?? 0,
          sort: { key: sort?.field ?? '', column: sort?.field ?? '', direction: sort?.direction ?? '' },
          rows,
        },
      },
      error: result.error,
      visibleColumns: this.visibleColumns,
      columnWidths: command.columnWidths ?? {},
      totalLabel: result.truncated ? `${rows.length}+ rows` : `${rows.length} rows`,
    }
  }

  private forwardSort = (event: CustomEvent<WindowedTableRequest>): void => {
    event.stopPropagation()
    const request = event.detail
    if (request.start > 0) return
    const field = request.sort.key ?? request.sort.column ?? ''
    const direction = request.sort.direction
    const current = this.command.sort?.[0]
    if (!field || (direction !== 'asc' && direction !== 'desc')) return
    if (current?.field === field && current.direction === direction) return
    this.dispatchEvent(new CustomEvent('lv-data-explore-table-command', {
      bubbles: true,
      composed: true,
      detail: { sort: [{ field, direction }] },
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

function isNumericType(type: string | undefined): boolean {
  return /int|decimal|double|float|number|numeric|real|bigint|smallint|sum|count|avg|min|max/i.test(type ?? '')
}

if (!customElements.get('lv-data-explore-table')) customElements.define('lv-data-explore-table', DataExploreTable)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-explore-table': DataExploreTable
  }
}
