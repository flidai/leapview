import type { TableVisualizationFormattingRule, VisualizationConditionalFormat, VisualizationEnvelope, VisualizationField } from '../../../../generated/visualization'
import type { TableBlock, TableColumn, TableFormattingRule, TableSignal, TableSort } from '../../table/types'
import type { RendererAdapter, RendererHandle } from '../host-controller'
import { resolveVisualizationMetadata } from '../metadata'
import { projectVisualizationHighlights } from '../highlight'

type ReportTableElement = HTMLElement & { tableId: string; table: TableSignal }

export const adapter: RendererAdapter = {
  async mount(container, envelope) {
    const { ReportTable } = await import('../../table/report-table')
    await customElements.whenDefined('lv-report-table')
    const table = new ReportTable() as ReportTableElement
    container.replaceChildren(table)
    const handle = new TanStackHandle(container, table)
    handle.update(envelope)
    return handle
  },
}

class TanStackHandle implements RendererHandle {
  private envelope?: VisualizationEnvelope
  constructor(private readonly container: HTMLElement, private readonly table: ReportTableElement) {
    this.table.addEventListener('lv-visual-window-change', this.handleWindowChange)
  }
  update(envelope: VisualizationEnvelope): void {
    this.envelope = envelope
    this.table.tableId = envelope.visualID
    this.table.table = tableSignal(envelope)
  }
  resize(): void {}
  async snapshot(): Promise<Blob> { return new Blob([JSON.stringify(this.table.table)], { type: 'application/json' }) }
  dispose(): void {
    this.table.removeEventListener('lv-visual-window-change', this.handleWindowChange)
    this.container.replaceChildren()
  }
  private readonly handleWindowChange = (event: Event) => {
    const envelope = this.envelope
    if (!envelope || envelope.dataState.kind !== 'windowed') return
    const detail = (event as CustomEvent<{ visual: string; block: string; start: number; count: number; requestSeq: number; resetVersion: number; sort: TableSort }>).detail
    event.stopPropagation()
    this.table.dispatchEvent(new CustomEvent('lv-visualization-window-request', {
      bubbles: true, composed: true,
      detail: {
        visualID: envelope.visualID, specRevision: envelope.specRevision, dataRevision: envelope.dataRevision,
        requestSeq: detail.requestSeq, resetVersion: detail.resetVersion, start: detail.start, limit: detail.count,
        sort: [{ field: { dataset: envelope.dataState.schema.id, field: detail.sort.key }, direction: detail.sort.direction === 'desc' ? 'descending' : 'ascending' }],
        blockID: detail.block,
      },
    }))
  }
}

export function tableSignal(envelope: VisualizationEnvelope): TableSignal {
  const spec = envelope.spec
  if (spec.kind !== 'table' && spec.kind !== 'matrix' && spec.kind !== 'pivot') throw new Error(`TanStack cannot render ${spec.kind}`)
	if (envelope.dataState.kind === 'spatial_tiled') throw new Error('TanStack cannot render spatial map data')
  const schema = envelope.dataState.kind === 'windowed' ? envelope.dataState.schema : spec.datasets[0]
  const fields = new Map((schema?.fields ?? []).map((field) => [field.id, field]))
  const fieldRefs = spec.kind === 'table' ? spec.columns.map((column) => column.field) : (schema?.fields ?? []).map((field) => ({ dataset: schema?.id ?? 'primary', field: field.id }))
  const columns: TableColumn[] = fieldRefs.map((ref) => {
    const field = fields.get(ref.field)
    const authored = spec.kind === 'table' ? spec.columns.find((column) => column.field.field === ref.field) : undefined
    const metricKey = field?.grid?.metric ?? ref.field
    const metricFormatting = spec.kind === 'matrix' || spec.kind === 'pivot' ? spec.metricFormatting[metricKey] : undefined
    const gridFormatting = field?.grid?.formatting
    const conditionalFormatting = conditionalFormatsForField(spec.conditionalFormatting ?? [], ref.dataset, ref.field, metricKey)
    const grid = authored ?? field?.grid
	return tableColumn(field, authored?.label, authored?.width, authored?.formatting ?? (gridFormatting?.length ? gridFormatting : metricFormatting), grid ? { ...grid, metric: metricKey } : { metric: metricKey }, conditionalFormatting)
  })
  const state = envelope.dataState
  const sort = state.kind === 'windowed' ? tableSort(state.sort[0], columns[0]?.key) : tableSort(spec.kind === 'table' ? spec.defaultSort?.[0] : undefined, columns[0]?.key)
  const blocks = state.kind === 'windowed' ? tableBlocks(envelope, state, schema?.fields ?? []) : inlineBlocks(envelope, state, schema?.fields ?? [], sort)
  const availableRows = state.kind === 'windowed' ? state.availableRows : state.datasets[0]?.rows.length ?? 0
  const cardinalityCount = state.kind === 'windowed' ? state.cardinality.count ?? availableRows : availableRows
  const interaction = tableInteraction(spec)
  return {
    id: envelope.visualID, version: 2, type: spec.kind, title: resolveVisualizationMetadata(envelope).title,
    style: { density: spec.presentation.rowHeight <= 30 ? 'compact' : spec.presentation.rowHeight >= 42 ? 'spacious' : 'comfortable', zebra: spec.presentation.striped, grid: 'rows' },
    interaction, selection: tableSelection(envelope, interaction), columns,
    highlight: tableHighlight(envelope, schema?.id ?? 'primary'),
    cardinality: { kind: state.kind === 'windowed' ? state.cardinality.kind : 'exact', value: cardinalityCount },
    availableRows, isCapped: state.kind === 'windowed' && availableRows >= state.rowCap,
    rowCap: state.kind === 'windowed' ? state.rowCap : spec.dataBudget.maxRows,
    chunkSize: state.kind === 'windowed' ? state.chunkSize : Math.max(1, availableRows),
    rowHeight: spec.presentation.rowHeight, resetVersion: state.kind === 'windowed' ? state.resetVersion : 0,
    sort, blocks, loadingBlock: envelope.status.kind === 'loading' ? 'all' : '', error: envelope.status.kind === 'error' ? envelope.status.message ?? 'Visualization error' : '',
  }
}

function tableInteraction(spec: Extract<VisualizationEnvelope['spec'], { kind: 'table' | 'matrix' | 'pivot' }>): TableSignal['interaction'] {
  const interaction = spec.interactions.find((candidate) => candidate.kind === 'select')
  if (!interaction) return undefined
  return {
    kind: interaction.id,
    toggle: interaction.mode === 'multiple',
    targets: [...interaction.targets],
    mappings: interaction.mappings.map((mapping) => ({
      field: mapping.targetFieldID,
      ...(mapping.targetDatasetID ? { dataset: mapping.targetDatasetID } : {}),
      ...(mapping.grain ? { grain: mapping.grain } : {}),
      value: mapping.source.field,
      ...(mapping.label ? { label: mapping.label.field } : {}),
    })),
  }
}

function tableSelection(envelope: VisualizationEnvelope, interaction: TableSignal['interaction']): TableSignal['selection'] {
  const mappings = interaction?.mappings ?? []
  if (mappings.length === 0) return []
  return envelope.selection.flatMap((entry) => {
    const selected = mappings.flatMap((mapping) => {
      const value = entry.datum.identity[mapping.value]
      if (value !== null && typeof value !== 'string' && typeof value !== 'number' && typeof value !== 'boolean') return []
      return [{ field: mapping.field, dataset: mapping.dataset, grain: mapping.grain, value, label: entry.label }]
    })
    return selected.length === mappings.length ? [{ mappings: selected, label: entry.label }] : []
  })
}

function tableColumn(
  field: VisualizationField | undefined,
  label?: string,
  width?: number,
  formatting: TableVisualizationFormattingRule[] = [],
	metadata?: { group?: string; metric?: string; columnValue?: string },
  conditionalFormatting: VisualizationConditionalFormat[] = [],
): TableColumn {
  const key = field?.id ?? ''
  return {
    key, label: label ?? field?.label ?? key, width,
    align: field?.role === 'metric' ? 'right' : 'left', role: field?.role === 'metric' ? 'metric' : 'row_header',
	group: metadata?.group, metric: metadata?.metric, columnValue: metadata?.columnValue,
    format: tableFormat(field), visualizationFormat: field?.format, formatting: formatting.map(tableFormattingRule),
    conditionalFormatting,
  }
}

function conditionalFormatsForField(
  formats: VisualizationConditionalFormat[],
  dataset: string,
  field: string,
	metric: string,
): VisualizationConditionalFormat[] {
  return formats.flatMap((format) => {
		if (format.field.dataset !== dataset || (format.field.field !== field && format.field.field !== metric)) return []
    if (format.field.field === field) return [format]
    return [{ ...format, field: { dataset, field } }]
  })
}

function tableFormattingRule(rule: TableVisualizationFormattingRule): TableFormattingRule {
  switch (rule.kind) {
    case 'badge': return { kind: rule.kind, values: { ...rule.values } }
    case 'text_color': return { kind: rule.kind, color: rule.color, values: rule.values ? { ...rule.values } : undefined, min: rule.minimum, max: rule.maximum }
    case 'background_scale': return { kind: rule.kind, min: rule.minimum, max: rule.maximum, lowColor: rule.lowColor, highColor: rule.highColor }
    case 'data_bar': return { kind: rule.kind, min: rule.minimum, max: rule.maximum, color: rule.color, background: rule.background }
  }
}

function tableFormat(field?: VisualizationField): TableColumn['format'] {
  if (!field) return 'text'
  if (field.format?.kind === 'currency') return 'currency'
  if (field.dataType === 'integer') return 'integer'
  if (field.dataType === 'decimal' || field.dataType === 'float') return 'decimal'
  return 'text'
}

function tableSort(sort: { field: { field: string }; direction: string } | undefined, fallback = ''): TableSort {
  return { key: sort?.field.field ?? fallback, direction: sort?.direction === 'descending' ? 'desc' : 'asc' }
}

function rowObject(fields: VisualizationField[], row: unknown[]): Record<string, unknown> {
  return Object.fromEntries(fields.map((field, index) => [field.id, row[index]]))
}

function block(start: number, rows: unknown[][], fields: VisualizationField[], requestSeq: number, resetVersion: number, sort: TableSort, highlighted: ReadonlySet<number> = new Set()): TableBlock {
  return {
    start,
    rows: rows.map((row, index) => ({ ...rowObject(fields, row), __lv_highlighted: highlighted.has(index) })),
    requestSeq,
    resetVersion,
    sort,
  }
}

function tableBlocks(envelope: VisualizationEnvelope, state: Extract<VisualizationEnvelope['dataState'], { kind: 'windowed' }>, fields: VisualizationField[]): TableSignal['blocks'] {
  const fallbackSort = tableSort(state.sort[0], fields[0]?.id)
  const at = (id: 'a' | 'b' | 'c', start: number): TableBlock => {
    const value = state.blocks[id]
    if (!value) return block(start, [], fields, 0, state.resetVersion, fallbackSort)
    const projection = projectVisualizationHighlights(envelope, state.schema.id, fields.map((field) => field.id), value.rows)
    return block(value.start, value.rows, fields, value.requestSeq, value.resetVersion, tableSort(value.sort[0], fallbackSort.key), projection.matchedRows)
  }
  return { a: at('a', 0), b: at('b', state.chunkSize), c: at('c', state.chunkSize * 2) }
}

function inlineBlocks(envelope: VisualizationEnvelope, state: Extract<VisualizationEnvelope['dataState'], { kind: 'inline' }>, fields: VisualizationField[], sort: TableSort): TableSignal['blocks'] {
  const rows = state.datasets[0]?.rows ?? []
  const datasetID = state.datasets[0]?.id ?? 'primary'
  const projection = projectVisualizationHighlights(envelope, datasetID, fields.map((field) => field.id), rows)
  return { a: block(0, rows, fields, 0, 0, sort, projection.matchedRows), b: block(rows.length, [], fields, 0, 0, sort), c: block(rows.length, [], fields, 0, 0, sort) }
}

function tableHighlight(envelope: VisualizationEnvelope, datasetID: string): TableSignal['highlight'] {
  const state = envelope.dataState
	if (state.kind === 'spatial_tiled') return { active: false, announcement: '' }
  const rows = state.kind === 'inline'
    ? state.datasets.find((dataset) => dataset.id === datasetID)?.rows ?? []
    : Object.values(state.blocks).flatMap((block) => block.rows)
  const columns = state.kind === 'inline'
    ? state.datasets.find((dataset) => dataset.id === datasetID)?.columns ?? []
    : state.schema.fields.map((field) => field.id)
  const projection = projectVisualizationHighlights(envelope, datasetID, columns, rows)
  return { active: projection.active, announcement: projection.announcement }
}
