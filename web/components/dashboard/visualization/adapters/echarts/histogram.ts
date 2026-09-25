import type { VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { categoryIdentity } from './category-colors'
import { displayUnitForField, escapeHTML, field, formatDisplayField, formatField, inlineDataset } from './common'
import type { CartesianSpec } from './series-intent'

export function histogramRangeFormatter(
  envelope: VisualizationEnvelope,
  spec: CartesianSpec,
  context: RendererContext,
): (value: unknown) => string {
  const dataset = inlineDataset(envelope, spec.x.dataset)
  const bucketIndex = dataset?.columns.indexOf(spec.x.field) ?? -1
  const startRef: VisualizationFieldRef = { dataset: spec.x.dataset, field: 'start' }
  const endRef: VisualizationFieldRef = { dataset: spec.x.dataset, field: 'end' }
  const startIndex = dataset?.columns.indexOf(startRef.field) ?? -1
  const endIndex = dataset?.columns.indexOf(endRef.field) ?? -1
  if (!dataset || bucketIndex < 0 || startIndex < 0 || endIndex < 0) {
    return (value) => formatField(envelope, spec.x, value, context)
  }

  const displayUnit = displayUnitForField(envelope, endRef, 'x', [startRef, endRef])
  const rowsByBucket = new Map<string, readonly unknown[]>()
  for (const row of dataset.rows) {
    const identity = categoryIdentity(row[bucketIndex])
    if (!rowsByBucket.has(identity)) rowsByBucket.set(identity, row)
  }
  return (bucket) => {
    const row = rowsByBucket.get(categoryIdentity(bucket))
    if (!row) return formatField(envelope, spec.x, bucket, context)
    const startValue = row[startIndex]
    const endValue = row[endIndex]
    const start = formatDisplayField(envelope, startRef, startValue, context, displayUnit)
    if (startValue !== null && startValue !== undefined && Object.is(startValue, endValue)) return start
    return `${start}–${formatDisplayField(envelope, endRef, endValue, context, displayUnit)}`
  }
}

export function histogramTooltipFormatter(envelope: VisualizationEnvelope, spec: CartesianSpec, context: RendererContext): (raw: unknown) => string {
  const dataset = inlineDataset(envelope, spec.x.dataset)
  const bucketIndex = dataset?.columns.indexOf(spec.x.field) ?? -1
  const valueRef = spec.y.find((candidate) => candidate.field === 'count') ?? spec.y[0]
  const valueIndex = valueRef && dataset ? dataset.columns.indexOf(valueRef.field) : -1
  const definition = valueRef ? field(envelope, valueRef) : undefined
  const valueLabel = definition?.label && definition.label.toLowerCase() !== valueRef?.field.toLowerCase()
    ? definition.label
    : 'Count'
  const formatRange = histogramRangeFormatter(envelope, spec, context)
  return (raw: unknown): string => {
    for (const item of (Array.isArray(raw) ? raw : [raw])) {
      const row = (item as { value?: unknown })?.value
      if (!Array.isArray(row)) continue
      const range = formatRange(bucketIndex >= 0 ? row[bucketIndex] : undefined)
      const value = valueIndex >= 0 && valueRef
        ? formatDisplayField(envelope, valueRef, row[valueIndex], context)
        : '—'
      return `Range: ${escapeHTML(range)}<br>${escapeHTML(valueLabel)}: ${escapeHTML(value)}`
    }
    return ''
  }
}
