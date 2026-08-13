import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import { defaultRendererContext, type RendererContext } from '../../host-controller'
import { formatValue } from '../../format'
import { geographicDataset } from './data'

export type RenderedFeatureLocator = Readonly<{ layer?: { id?: string }; properties?: Record<string, unknown> | null }>

export function mapTooltipEntries(envelope: VisualizationEnvelope, features: readonly RenderedFeatureLocator[], context: RendererContext = defaultRendererContext): Array<{ label: string; value: string }> {
	if (envelope.spec.kind !== 'geographic') return []
	if (envelope.dataState.kind === 'spatial_tiled') {
		for (const feature of features) {
			const layerID = feature.layer?.id?.replace(/^lv-/, '')
			const layer = envelope.spec.layers.find((candidate) => candidate.id === layerID)
			if (!layer || !feature.properties) continue
			const fields = layer.tooltip.length ? layer.tooltip : 'label' in layer && layer.label ? [layer.label] : []
			const entries = fields.flatMap((reference) => {
				const schema = envelope.spec.datasets.find((candidate) => candidate.id === reference.dataset)
				const field = schema?.fields.find((candidate) => candidate.id === reference.field)
				const raw = feature.properties?.[reference.field]
				if (!field || raw === undefined) return []
				let value: string
				try { value = field.format ? formatValue(context.locale, field.format, raw) : raw == null ? '—' : String(raw) } catch { value = raw == null ? '—' : String(raw) }
				return [{ label: field.label, value }]
			})
			if (feature.properties.__lv_aggregate === true) {
				const coordinateCount = feature.properties.__lv_coordinate_count
				if (typeof coordinateCount === 'number' && Number.isFinite(coordinateCount)) entries.unshift({ label: 'Contained locations', value: String(coordinateCount) })
				entries.unshift({ label: 'Precision', value: 'Aggregated area' })
			}
			if (entries.length) return entries
		}
		return []
	}
  for (const feature of features) {
    const datasetID = feature.properties?.__lv_dataset, rowIndex = feature.properties?.__lv_row_index, layerID = feature.properties?.__lv_layer_id
    if (typeof datasetID !== 'string' || typeof rowIndex !== 'number' || !Number.isInteger(rowIndex) || rowIndex < 0 || typeof layerID !== 'string') continue
    const dataset = geographicDataset(envelope, datasetID)
    const layer = envelope.spec.layers.find((candidate) => candidate.id === layerID)
    const row = dataset?.rows[rowIndex]
    if (!dataset || !layer || !row) continue
    const fields = layer.tooltip.length ? layer.tooltip : layer.label ? [layer.label] : []
    return fields.flatMap((reference) => {
      if (reference.dataset !== datasetID) return []
      const column = dataset.columns.indexOf(reference.field)
      if (column < 0 || column >= row.length) return []
      const schema = envelope.spec.datasets.find((candidate) => candidate.id === datasetID)
      const field = schema?.fields.find((candidate) => candidate.id === reference.field)
      const raw = row[column]
      let value: string
      try { value = field?.format ? formatValue(context.locale, field.format, raw) : raw == null ? '—' : String(raw) } catch { value = raw == null ? '—' : String(raw) }
      return [{ label: field?.label ?? reference.field, value }]
    })
  }
  return []
}

export function mapAccessibleData(envelope: VisualizationEnvelope, limit = 100, context: RendererContext = defaultRendererContext): {
  columns: Array<{ id: string; label: string }>
  rows: string[][]
  totalRows: number
} {
  if (envelope.spec.kind !== 'geographic' || limit < 1) return { columns: [], rows: [], totalRows: 0 }
  const schema = envelope.spec.datasets[0]
  if (!schema) return { columns: [], rows: [], totalRows: 0 }
  const dataset = geographicDataset(envelope, schema.id)
  if (!dataset) return { columns: [], rows: [], totalRows: 0 }
  const fieldIDs: string[] = []
  const add = (reference?: { dataset: string; field: string }) => {
    if (reference?.dataset === schema.id && !fieldIDs.includes(reference.field)) fieldIDs.push(reference.field)
  }
  for (const layer of envelope.spec.layers) {
    for (const reference of layer.tooltip) add(reference)
    if (layer.tooltip.length > 0) continue
    add(layer.label)
    if (layer.kind === 'choropleth') { add(layer.join); add(layer.value); add(layer.category) }
    if (layer.kind === 'point') { add(layer.latitude); add(layer.longitude); add(layer.value); add(layer.category) }
    if (layer.kind === 'heat' || layer.kind === 'density') { add(layer.latitude); add(layer.longitude); add(layer.value) }
    if (layer.kind === 'path') { add(layer.path); add(layer.order); add(layer.latitude); add(layer.longitude); add(layer.value); add(layer.category) }
  }
  if (fieldIDs.length === 0) for (const field of schema.fields.slice(0, 3)) add({ dataset: schema.id, field: field.id })
  const columns = fieldIDs.flatMap((id) => {
    const field = schema.fields.find((candidate) => candidate.id === id)
    return field ? [{ id, label: field.label }] : []
  })
  const indexes = columns.map((column) => dataset.columns.indexOf(column.id))
  const fields = columns.map((column) => schema.fields.find((field) => field.id === column.id))
  const rows = dataset.rows.slice(0, Math.min(limit, dataset.rows.length)).map((row) => indexes.map((index, columnIndex) => {
    const raw = index >= 0 ? row[index] : null
    const field = fields[columnIndex]
    try { return field?.format ? formatValue(context.locale, field.format, raw) : raw == null ? '—' : String(raw) } catch { return raw == null ? '—' : String(raw) }
  }))
  return { columns, rows, totalRows: dataset.rows.length }
}

export function mapAccessibleRenderedFeatures(
  envelope: VisualizationEnvelope,
  features: readonly RenderedFeatureLocator[],
  limit = 100,
  context: RendererContext = defaultRendererContext,
): { columns: Array<{ id: string; label: string }>; rows: string[][]; totalRows: number; visibleRows: number; aggregateRows: number; rawRows: number } {
  if (envelope.spec.kind !== 'geographic' || envelope.dataState.kind !== 'spatial_tiled' || limit < 1) return { columns: [], rows: [], totalRows: 0, visibleRows: 0, aggregateRows: 0, rawRows: 0 }
  const schema = envelope.dataState.schema
  const fieldIDs: string[] = []
  const add = (reference?: { dataset: string; field: string }) => {
    if (reference?.dataset === schema.id && !fieldIDs.includes(reference.field)) fieldIDs.push(reference.field)
  }
  for (const layer of envelope.spec.layers) {
    for (const reference of layer.tooltip) add(reference)
    if (layer.tooltip.length === 0) add(layer.label)
    if (layer.kind === 'point' || layer.kind === 'heat' || layer.kind === 'density') {
      add(layer.latitude); add(layer.longitude); add(layer.value)
    }
  }
  if (fieldIDs.length === 0) for (const field of schema.fields.slice(0, 3)) fieldIDs.push(field.id)
  const fields = fieldIDs.flatMap((id) => {
    const field = schema.fields.find((candidate) => candidate.id === id)
    return field ? [field] : []
  })
	const columns = [{ id: '__lv_precision', label: 'Precision' }, { id: '__lv_coordinate_count', label: 'Contained locations' }, ...fields.map((field) => ({ id: field.id, label: field.label }))]
  const seen = new Set<string>()
  const rows: string[][] = []
  let aggregateRows = 0
  let rawRows = 0
  for (const feature of features) {
    if (!feature.properties) continue
    const identity = String(feature.properties.__lv_id ?? JSON.stringify(feature.properties))
    if (seen.has(identity)) continue
    seen.add(identity)
    const aggregate = feature.properties.__lv_aggregate === true
    if (aggregate) aggregateRows++
    else rawRows++
    if (rows.length >= limit) continue
    const values = fields.map((field) => {
      const raw = feature.properties?.[field.id]
      try { return field.format ? formatValue(context.locale, field.format, raw) : raw == null ? '—' : String(raw) } catch { return raw == null ? '—' : String(raw) }
    })
		const coordinateCount = aggregate && typeof feature.properties.__lv_coordinate_count === 'number' ? String(feature.properties.__lv_coordinate_count) : '1'
		rows.push([aggregate ? 'Aggregated area' : 'Raw point', coordinateCount, ...values])
  }
  return { columns, rows, totalRows: envelope.dataState.cardinality.count ?? 0, visibleRows: seen.size, aggregateRows, rawRows }
}
