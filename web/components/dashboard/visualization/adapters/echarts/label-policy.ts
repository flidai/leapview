import type { VisualizationEnvelope, VisualizationLabelPolicy } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { resolveConditionalFormat } from '../../conditional-format'
import { inlineDataset, type EChartsTranslation } from './common'

type LabelFormatterParameters = { value?: unknown[]; dataIndex?: number }
type LabelLayoutParameters = {
  dataIndex?: number
  rect?: { width?: number; height?: number }
}

type LabelLayout = EChartsTranslation | ((params: LabelLayoutParameters) => EChartsTranslation)

export function echartsLabelPolicy(
  envelope: VisualizationEnvelope,
  datasetID: string,
  policy: VisualizationLabelPolicy,
  formatter: (params: LabelFormatterParameters) => unknown,
  context: RendererContext,
): { label: EChartsTranslation; labelLayout: LabelLayout } {
  const visible = policy.density !== 'hidden'
  const dense = policy.density === 'dense'
  const label = {
    show: visible,
    color: context.colors.foreground,
    fontFamily: context.fontFamily,
    fontSize: dense ? 10 : 12,
    padding: Math.ceil(policy.minimumSpacing / 2),
    overflow: 'truncate',
    ellipsis: '…',
    formatter: (params: LabelFormatterParameters) =>
      truncateVisualizationLabel(String(formatter(params) ?? ''), policy.maxCharacters, context.locale),
  }
  if (policy.density === 'always') return { label, labelLayout: { hideOverlap: false } }
  if (policy.density === 'hidden') return { label, labelLayout: { hideOverlap: true } }
  return {
    label,
    labelLayout: (params: LabelLayoutParameters) => ({
      hideOverlap: !isPriorityDatum(envelope, datasetID, params.dataIndex, policy),
    }),
  }
}

export function constrainEChartsLabelToDataRect(
  labelLayout: LabelLayout,
  minimumSpacing: number,
): (params: LabelLayoutParameters) => EChartsTranslation {
  return (params) => {
    const base = typeof labelLayout === 'function' ? labelLayout(params) : labelLayout
    const width = finiteInsetSize(params.rect?.width, minimumSpacing)
    const height = finiteInsetSize(params.rect?.height, minimumSpacing)
    return {
      ...base,
      ...(width === undefined ? {} : { width }),
      ...(height === undefined ? {} : { height }),
    }
  }
}

export function truncateVisualizationLabel(value: string, maxCharacters: number, locale: string): string {
  const segments = [...new Intl.Segmenter(locale, { granularity: 'grapheme' }).segment(value)].map((segment) => segment.segment)
  if (segments.length <= maxCharacters) return value
  return `${segments.slice(0, Math.max(1, maxCharacters - 1)).join('')}…`
}

export function isPriorityDatum(
  envelope: VisualizationEnvelope,
  datasetID: string,
  rowIndex: number | undefined,
  policy: VisualizationLabelPolicy,
): boolean {
  if (!Number.isInteger(rowIndex) || rowIndex! < 0) return false
  if (policy.priority.includes('selected') && isSelectedDatum(envelope, datasetID, rowIndex!)) return true
  const wantsAnomaly = policy.priority.includes('anomaly')
  const wantsThreshold = policy.priority.includes('threshold')
  if (!wantsAnomaly && !wantsThreshold) return false
  const dataset = inlineDataset(envelope, datasetID)
  const row = dataset?.rows[rowIndex!]
  if (!dataset || !row) return false
  for (const format of envelope.spec.conditionalFormatting ?? []) {
    if (format.field.dataset !== datasetID) continue
    const result = resolveConditionalFormat(format, dataset.columns, row)
    if (result.outcome !== 'matched') continue
    if (wantsThreshold && format.rule.kind === 'rules') return true
    if (wantsAnomaly && (
      result.style.color === 'warning' || result.style.color === 'danger'
      || result.style.icon === 'warning' || result.style.icon === 'arrow_up' || result.style.icon === 'arrow_down'
    )) return true
  }
  return false
}

function isSelectedDatum(envelope: VisualizationEnvelope, datasetID: string, rowIndex: number): boolean {
  if (envelope.dataState.kind !== 'inline') return false
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
  const schema = envelope.spec.datasets.find((candidate) => candidate.id === datasetID)
  const row = dataset?.rows[rowIndex]
  const identity = schema?.fields.filter((field) => field.role === 'identity') ?? []
  if (!dataset || !row || identity.length === 0) return false
  return envelope.selection.some((selection) =>
    selection.datum.dataset === datasetID
    && selection.datum.dataRevision === envelope.dataRevision
    && identity.every((field) => Object.is(row[dataset.columns.indexOf(field.id)], selection.datum.identity[field.id])),
  )
}

function finiteInsetSize(size: number | undefined, spacing: number): number | undefined {
  if (!Number.isFinite(size)) return undefined
  return Math.max(0, Math.floor(size! - Math.max(0, spacing)))
}
