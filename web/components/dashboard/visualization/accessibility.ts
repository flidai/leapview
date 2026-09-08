import type { VisualizationEnvelope } from '../../../generated/visualization'
import { formatValue } from './format'
import { defaultRendererContext, type RendererContext } from './host-controller'
import { resolveVisualizationMetadata } from './metadata'

export const accessibleRowLimit = 100

export type AccessibleVisualizationColumn = Readonly<{ key: string; label: string; align?: 'left' | 'right' }>
export type AccessibleVisualizationData = Readonly<{
  columns: readonly AccessibleVisualizationColumn[]
  rows: readonly Record<string, string>[]
  totalRows: number
  truncated: boolean
}>

/** Host data actions are truthful only when the renderer's inline frame is available. */
export function supportsHostDataActions(envelope: VisualizationEnvelope): boolean {
  return envelope.dataState.kind === 'inline'
    && envelope.spec.kind !== 'table'
    && envelope.spec.kind !== 'matrix'
    && envelope.spec.kind !== 'pivot'
}

export function accessibleVisualizationData(
  envelope: VisualizationEnvelope,
  context: RendererContext = defaultRendererContext,
  limit = accessibleRowLimit,
): AccessibleVisualizationData {
  if (limit < 1 || !Number.isFinite(limit)) return { columns: [], rows: [], totalRows: 0, truncated: false }
  if (envelope.dataState.kind !== 'inline') return { columns: [], rows: [], totalRows: 0, truncated: false }
  const datasets = envelope.dataState.datasets
  const dataset = datasets.find((candidate) => envelope.spec.datasets.some((schema) => schema.id === candidate.id)) ?? datasets[0]
  const schema = dataset ? envelope.spec.datasets.find((candidate) => candidate.id === dataset.id) : undefined
  if (!dataset || !schema) return { columns: [], rows: [], totalRows: 0, truncated: false }
  const fields = schema.fields.filter((field) => dataset.columns.includes(field.id))
  const columns = fields.map((field) => ({
    key: field.id,
    label: field.label || field.id,
    ...(field.role === 'metric' ? { align: 'right' as const } : {}),
  }))
  const rows = dataset.rows.slice(0, Math.floor(limit)).map((row) => Object.fromEntries(fields.map((field) => {
    const value = row[dataset.columns.indexOf(field.id)]
    return [field.id, safeFormatField(envelope, { dataset: dataset.id, field: field.id }, value, context)]
  })))
  return {
    columns,
    rows,
    totalRows: dataset.rows.length,
    truncated: dataset.rows.length > rows.length || dataset.completeness === 'truncated' || dataset.completeness === 'partial',
  }
}

function safeFormatField(envelope: VisualizationEnvelope, ref: { dataset: string; field: string }, value: unknown, context: RendererContext): string {
  try {
    const field = envelope.spec.datasets.find((dataset) => dataset.id === ref.dataset)?.fields.find((candidate) => candidate.id === ref.field)
    if (field?.format) return formatValue(context.locale, field.format, value)
    return displayValue(value)
  }
  catch { return displayValue(value) }
}

export function displayValue(value: unknown): string {
  return value === null || value === undefined ? '—' : String(value)
}

export function accessibleStatus(envelope: VisualizationEnvelope): string {
  const status = envelope.status.message?.trim()
  if (status) return status
  switch (envelope.status.kind) {
    case 'ready': return 'Ready'
    case 'no_data': return 'No data'
    case 'partial': return 'Partial data'
    case 'loading': return 'Loading'
    case 'idle': return 'Waiting for data'
    case 'error': return 'Visualization error'
  }
}

export function accessibleDataStatus(envelope: VisualizationEnvelope, data: AccessibleVisualizationData): string {
  if (envelope.dataState.kind !== 'inline') return 'Data is available through the visual renderer.'
  const completeness = envelope.dataState.datasets.some((dataset) => dataset.completeness === 'partial')
    ? 'partial'
    : envelope.dataState.datasets.some((dataset) => dataset.completeness === 'truncated')
      ? 'truncated'
      : envelope.dataState.datasets[0]?.completeness
  const source = completeness === 'partial'
    ? 'Source data is partial.'
    : completeness === 'truncated'
      ? 'Source data is truncated.'
      : ''
  if (data.totalRows === 0 || envelope.dataState.datasets.every((dataset) => dataset.rows.length === 0)) {
    return [source, 'No data rows are available.'].filter(Boolean).join(' ')
  }
  const label = `${data.totalRows.toLocaleString()} row${data.totalRows === 1 ? '' : 's'} available.`
  const preview = data.rows.length < data.totalRows
    ? ` Showing the first ${data.rows.length.toLocaleString()} rows in the accessible preview.`
    : ''
  return `${label}${preview}${source ? ` ${source}` : ''}`
}

export function visualizationChangeAnnouncement(previous: VisualizationEnvelope | undefined, next: VisualizationEnvelope): string {
  if (next.spec.accessibility.announceChanges !== true || !previous) return ''
  const dataChanged = previous.dataRevision !== next.dataRevision
  const statusChanged = previous.status.kind !== next.status.kind || previous.status.message !== next.status.message
  const selectionChanged = JSON.stringify(previous.selection) !== JSON.stringify(next.selection)
  const highlightChanged = JSON.stringify(previous.highlights) !== JSON.stringify(next.highlights)
  if (!dataChanged && !statusChanged && !selectionChanged && !highlightChanged) return ''
  const metadata = resolveVisualizationMetadata(next)
  const reasons = [
    dataChanged ? accessibleDataStatus(next, accessibleVisualizationData(next, defaultRendererContext, 6)) : '',
    statusChanged ? accessibleStatus(next) : '',
    selectionChanged ? next.selection.length > 0
      ? `${next.selection.length.toLocaleString()} selection${next.selection.length === 1 ? '' : 's'} active.`
      : 'Selection cleared.' : '',
    highlightChanged ? next.highlights.length > 0
      ? `${next.highlights.length.toLocaleString()} highlight${next.highlights.length === 1 ? '' : 's'} active.`
      : 'Highlights cleared.' : '',
  ].filter(Boolean)
  return `${metadata.title} updated. ${reasons.join(' ')}`
}
