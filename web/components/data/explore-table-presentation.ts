import type { ExplorationSpec, ExplorationTableColumn } from '../../generated/exploration'
import type { DataExploreResultSignal } from '../../generated/signals'
import type { WindowedTableColumn, WindowedTablePayload } from '../shared/windowed-table'
import { explorationResultKeyForSort } from './data-explorer-spec'

/** Project display metadata only; the governed window blocks stay untouched. */
export function exploreTablePresentation(
  spec: ExplorationSpec,
  source: DataExploreResultSignal['columns'],
  columnWidths: Record<string, number> = {},
): Pick<WindowedTablePayload, 'columns' | 'rowHeight' | 'showHeader' | 'striped' | 'fillWidth'> {
  const keys = source.map(column => column.key)
  const resultKey = (field: string) => keys.includes(field) ? field : explorationResultKeyForSort(spec, field, keys)
  const visualColumns = spec.visualization?.kind === 'table' ? spec.visualization.columns : []
  const requested: ExplorationTableColumn[] = spec.table?.columns?.length ? spec.table.columns : visualColumns
  const selected = requested.map(authored => ({ authored, column: source.find(column => column.key === resultKey(authored.field)) }))
  // A stale preference must not hide the current governed result.
  const valid = selected.length > 0 && selected.every(item => item.column)
  const columns = (valid ? selected : source.map(column => ({ column, authored: undefined }))).map(({ column, authored }): WindowedTableColumn => ({
    key: column!.key,
    label: authored?.label?.trim() || column!.label || column!.key,
    type: column!.type,
    align: /int|decimal|double|float|number|numeric|real|sum|count|avg|min|max/i.test(column!.type ?? '') ? 'right' : 'left',
    sortable: true,
    width: authored?.width,
    format: authored?.format ?? visualColumns.find(ref => resultKey(ref.field) === column!.key)?.format,
  }))
  return {
    columns,
    rowHeight: spec.table?.rowHeight ?? (spec.table?.density === 'compact' ? 28 : spec.table?.density === 'comfortable' ? 36 : 32),
    showHeader: spec.table?.showHeader ?? true,
    striped: spec.table?.striped ?? false,
    fillWidth: !columns.some(column => column.width || columnWidths[column.key]),
  }
}
