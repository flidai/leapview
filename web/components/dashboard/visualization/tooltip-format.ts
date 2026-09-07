import type { VisualizationEnvelope, VisualizationField, VisualizationFieldRef, VisualizationFormat, VisualizationTooltipItem } from '../../../generated/visualization'
import { formatValue } from './format'

export type TooltipFormatContext = Readonly<{ locale: string }>

export function formatTooltipValue(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  value: unknown,
  context: TooltipFormatContext,
  override?: VisualizationFormat,
): string {
  if (value === null || value === undefined) return '—'
  const authored = override ?? tooltipField(envelope, ref)?.format
  if (!authored) return String(value)
  return formatValue(context.locale, authored, value)
}

export function formatTooltipEntries(
  envelope: VisualizationEnvelope,
  row: readonly unknown[],
  datasetID: string,
  context: TooltipFormatContext,
  refs: readonly VisualizationFieldRef[],
  items?: readonly VisualizationTooltipItem[],
): Array<{ label: string; value: string }> {
  const dataset = inlineDataset(envelope, datasetID)
  if (!dataset) return []
  const schema = envelope.spec.datasets.find((candidate) => candidate.id === datasetID)
  const itemByField = new Map((items ?? []).map((item) => [`${item.field.dataset}:${item.field.field}`, item]))
  return refs.flatMap((ref) => {
    if (ref.dataset !== datasetID) return []
    const definition = schema?.fields.find((candidate) => candidate.id === ref.field)
    const index = dataset.columns.indexOf(ref.field)
    if (!definition || index < 0 || index >= row.length) return []
    const item = itemByField.get(`${ref.dataset}:${ref.field}`)
    return [{
      label: item?.label ?? definition.label,
      value: formatTooltipValue(envelope, ref, row[index], context, item?.format),
    }]
  })
}

function tooltipField(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): VisualizationField | undefined {
  return envelope.spec.datasets.find((dataset) => dataset.id === ref.dataset)?.fields.find((candidate) => candidate.id === ref.field)
}

function inlineDataset(envelope: VisualizationEnvelope, datasetID: string): { columns: string[]; rows: unknown[][] } | undefined {
  if (envelope.dataState.kind !== 'inline') return undefined
  return envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
}
