import { LitElement, css, html } from 'lit'
import { property } from 'lit/decorators.js'
import type { DataExplorerCommand, DataPreviewSignal } from '../../generated/signals'
import '../shared/windowed-table'
import type { WindowedTableColumn, WindowedTablePayload, WindowedTableRequest } from '../shared/windowed-table'

const emptyPreview: DataPreviewSignal = {
  columns: [],
  totalRows: 0,
  availableRows: 0,
  chunkSize: 100,
  rowHeight: 32,
  resetVersion: 0,
  blocks: {},
  totalRowLabel: 'Unknown',
  sort: {},
  sql: '',
  error: '',
}

const emptyCommand: DataExplorerCommand = {
  objectKey: '',
  offset: 0,
  limit: 100,
  block: 'all',
  start: 0,
  count: 100,
  requestSeq: 0,
  resetVersion: 0,
  sort: {},
  visibleColumns: [],
  columnWidths: {},
}

class DataPreviewTable extends LitElement {
  @property({ attribute: false }) preview: DataPreviewSignal = emptyPreview
  @property({ attribute: false }) command: DataExplorerCommand = emptyCommand

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

    .failure {
      display: flex;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-12);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-danger);
      font: var(--lv-type-caption);
    }

    .failure span { flex: 1 1 18rem; }
    .failure button {
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-4) var(--base-size-8);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: inherit;
    }
  `

  render() {
    const error = this.preview?.error?.trim() ?? ''
    return html`
      ${error ? html`
        <div class="failure" role="alert">
          <span>${error}</span>
          <button type="button" @click=${this.retryPreview}>Retry</button>
          <button type="button" @click=${this.resetPreview}>Reset view</button>
        </div>
      ` : ''}
      <lv-windowed-table
        compact
        .table=${this.tablePayload(Boolean(error))}
        @lv-windowed-table-request=${this.forwardWindowCommand}
        @lv-windowed-table-columns=${this.forwardColumnCommand}
        @lv-windowed-table-column-widths=${this.forwardColumnWidthsCommand}
      ></lv-windowed-table>
    `
  }

  private tablePayload(suppressError = false): WindowedTablePayload {
    const preview = this.preview ?? emptyPreview
    const command = this.command ?? emptyCommand
    return {
      tableKey: command.objectKey ?? '',
      title: 'Data preview',
      columns: (preview.columns ?? []).map((column): WindowedTableColumn => ({
        key: column.key,
        label: column.label || column.key,
        type: column.type,
        align: isNumericType(column.type) ? 'right' : 'left',
        sortable: true,
      })),
      totalRows: preview.totalRows ?? parseTotalRows(preview.totalRowLabel) ?? 0,
      availableRows: preview.availableRows ?? preview.totalRows ?? 0,
      chunkSize: preview.chunkSize ?? command.count ?? command.limit ?? 100,
      rowHeight: preview.rowHeight ?? 32,
      resetVersion: preview.resetVersion ?? command.resetVersion ?? 0,
      sort: {
        key: preview.sort?.column ?? command.sort?.column ?? '',
        column: preview.sort?.column ?? command.sort?.column ?? '',
        direction: normalizeSortDirection(preview.sort?.direction ?? command.sort?.direction),
      },
      blocks: preview.blocks ?? {},
      loadingBlock: preview.loadingBlock ?? '',
      error: suppressError ? '' : preview.error,
      visibleColumns: command.visibleColumns ?? [],
      columnWidths: command.columnWidths ?? {},
      totalLabel: preview.totalRowLabel,
    }
  }

  private retryPreview = (): void => {
    const current = this.command ?? emptyCommand
    this.emitPreviewCommand({
      ...current,
      requestSeq: (current.requestSeq ?? 0) + 1,
    })
  }

  private resetPreview = (): void => {
    const current = this.command ?? emptyCommand
    this.emitPreviewCommand({
      ...current,
      offset: 0,
      start: 0,
      block: 'all',
      requestSeq: (current.requestSeq ?? 0) + 1,
      resetVersion: (current.resetVersion ?? 0) + 1,
      sort: {},
    })
  }

  private emitPreviewCommand(detail: Partial<DataExplorerCommand>): void {
    this.dispatchEvent(new CustomEvent('lv-data-preview-table-command', {
      bubbles: true,
      composed: true,
      detail,
    }))
  }

  private forwardWindowCommand = (event: CustomEvent<WindowedTableRequest>): void => {
    event.stopPropagation()
    const current = this.command ?? emptyCommand
    const request = event.detail
    this.dispatchEvent(new CustomEvent('lv-data-preview-table-command', {
      bubbles: true,
      composed: true,
      detail: {
        objectKey: current.objectKey,
        offset: request.start,
        limit: request.count,
        block: request.block,
        start: request.start,
        count: request.count,
        requestSeq: request.requestSeq,
        resetVersion: request.resetVersion,
        sort: { column: request.sort.key ?? request.sort.column ?? '', direction: request.sort.direction ?? '' },
        visibleColumns: current.visibleColumns ?? [],
        columnWidths: current.columnWidths ?? {},
      },
    }))
  }

  private forwardColumnCommand = (event: CustomEvent): void => {
    event.stopPropagation()
    const current = this.command ?? emptyCommand
    this.dispatchEvent(new CustomEvent('lv-data-preview-table-command', {
      bubbles: true,
      composed: true,
      detail: {
        objectKey: current.objectKey,
        offset: current.offset ?? current.start ?? 0,
        limit: current.limit ?? current.count ?? 100,
        block: current.block ?? 'all',
        start: current.start ?? current.offset ?? 0,
        count: current.count ?? current.limit ?? 100,
        requestSeq: current.requestSeq ?? 0,
        resetVersion: current.resetVersion ?? 0,
        sort: current.sort ?? {},
        visibleColumns: event.detail?.visibleColumns ?? [],
        columnWidths: current.columnWidths ?? {},
      },
    }))
  }

  private forwardColumnWidthsCommand = (event: CustomEvent): void => {
    event.stopPropagation()
    const current = this.command ?? emptyCommand
    this.dispatchEvent(new CustomEvent('lv-data-preview-table-command', {
      bubbles: true,
      composed: true,
      detail: {
        objectKey: current.objectKey,
        offset: current.offset ?? current.start ?? 0,
        limit: current.limit ?? current.count ?? 100,
        block: current.block ?? 'all',
        start: current.start ?? current.offset ?? 0,
        count: current.count ?? current.limit ?? 100,
        requestSeq: current.requestSeq ?? 0,
        resetVersion: current.resetVersion ?? 0,
        sort: current.sort ?? {},
        visibleColumns: current.visibleColumns ?? [],
        columnWidths: event.detail?.columnWidths ?? {},
      },
    }))
  }
}

function normalizeSortDirection(value: string | undefined): '' | 'asc' | 'desc' {
  return value === 'asc' || value === 'desc' ? value : ''
}

function parseTotalRows(label: string | undefined): number | undefined {
  const normalized = String(label ?? '').replace(/,/g, '').trim()
  if (!/^\d+$/.test(normalized)) return undefined
  const value = Number(normalized)
  return Number.isFinite(value) ? value : undefined
}

function isNumericType(type: string | undefined): boolean {
  return /int|decimal|double|float|number|numeric|real|bigint|smallint/i.test(type ?? '')
}

if (!customElements.get('lv-data-preview-table')) customElements.define('lv-data-preview-table', DataPreviewTable)

declare global {
  interface HTMLElementTagNameMap {
    'lv-data-preview-table': DataPreviewTable
  }
}
