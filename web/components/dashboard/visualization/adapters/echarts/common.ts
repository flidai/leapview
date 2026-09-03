import type { VisualizationCartesianAxis, VisualizationDisplayUnits, VisualizationEnvelope, VisualizationField, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { formatDisplayValue, formatValue, resolveDisplayUnitForFormat, type ResolvedDisplayUnit } from '../../format'
import { resolveVisualizationMetadata } from '../../metadata'

export type EChartsTranslation = Record<string, any>

export function inlineDataset(envelope: VisualizationEnvelope, datasetID = 'primary') {
  if (envelope.dataState.kind !== 'inline') return undefined
  return envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
}

export function field(envelope: VisualizationEnvelope, ref?: VisualizationFieldRef): VisualizationField | undefined {
  if (!ref) return undefined
  return envelope.spec.datasets.find((dataset) => dataset.id === ref.dataset)?.fields.find((candidate) => candidate.id === ref.field)
}

export function fieldLabel(envelope: VisualizationEnvelope, ref?: VisualizationFieldRef): string {
  return field(envelope, ref)?.label ?? ref?.field ?? ''
}

export function formatField(envelope: VisualizationEnvelope, ref: VisualizationFieldRef | undefined, value: unknown, context: RendererContext): string {
  const definition = field(envelope, ref)
  if (definition?.format) return formatValue(context.locale, definition.format, value)
  if (value === null || value === undefined) return '—'
  return String(value)
}

export function displayUnitForField(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef | undefined,
  axisID?: VisualizationCartesianAxis,
  scopeRefs: VisualizationFieldRef[] = ref ? [ref] : [],
  additionalValues: unknown[] = [],
): ResolvedDisplayUnit {
  const definition = field(envelope, ref)
  const values = [...scopeRefs.flatMap((scopeRef) => fieldValues(envelope, scopeRef)), ...additionalValues]
  return resolveDisplayUnitForFormat(displayUnitsPolicy(envelope, axisID), definition?.format, values)
}

export function formatDisplayField(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef | undefined,
  value: unknown,
  context: RendererContext,
  unit = displayUnitForField(envelope, ref),
): string {
  if (value === null || value === undefined) return '—'
  const definition = field(envelope, ref)
  if (definition && definition.dataType !== 'integer' && definition.dataType !== 'decimal' && definition.dataType !== 'float') return formatField(envelope, ref, value, context)
  return formatDisplayValue(context.locale, definition?.format ?? { kind: 'number' }, value, unit)
}

function fieldValues(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): unknown[] {
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  return index < 0 ? [] : (dataset?.rows ?? []).map((row) => row[index])
}

function displayUnitsPolicy(envelope: VisualizationEnvelope, axisID?: VisualizationCartesianAxis): VisualizationDisplayUnits {
  const spec = envelope.spec
  const axisPolicy = (spec.kind === 'cartesian' || spec.kind === 'point')
    ? spec.axes?.find((candidate) => candidate.id === axisID)?.displayUnits
    : undefined
  if (axisPolicy) return axisPolicy
  if (spec.kind === 'cartesian' || spec.kind === 'point' || spec.kind === 'proportional' || spec.kind === 'hierarchy' || spec.kind === 'polar' || spec.kind === 'geographic') {
    return spec.presentation.displayUnits ?? 'auto'
  }
  return 'auto'
}

export function rowValue(envelope: VisualizationEnvelope, ref: VisualizationFieldRef | undefined, row: unknown[]): unknown {
  if (!ref) return undefined
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  return index >= 0 ? row[index] : undefined
}

export function selectedDatasetSource(envelope: VisualizationEnvelope, dataset: NonNullable<ReturnType<typeof inlineDataset>>): unknown[][] {
  if (envelope.selection.length === 0) return [dataset.columns, ...dataset.rows]
  const schema = envelope.spec.datasets.find((candidate) => candidate.id === dataset.id)
  const identityFields = (schema?.fields ?? []).filter((candidate) => candidate.role === 'identity')
  if (identityFields.length === 0) return [dataset.columns, ...dataset.rows]
  const selected = envelope.selection.filter((entry) => entry.datum.dataset === dataset.id && entry.datum.dataRevision === envelope.dataRevision)
  if (selected.length === 0) return [dataset.columns, ...dataset.rows]
  const rows = dataset.rows.map((row) => {
    const matches = selected.some((entry) => identityFields.every((identity) => Object.is(row[dataset.columns.indexOf(identity.id)], entry.datum.identity[identity.id])))
    return [...row, matches]
  })
  return [[...dataset.columns, '__lv_selected'], ...rows]
}

export function baseOption(envelope: VisualizationEnvelope, context: RendererContext): EChartsTranslation {
  const dataset = inlineDataset(envelope)
  const metadata = resolveVisualizationMetadata(envelope)
  const description = metadata.summary ?? metadata.description
  const dataSummary = labelAccessibilitySummary(envelope, context)
  return {
    animation: false,
    aria: { enabled: true, description: [description, dataSummary].filter(Boolean).join(' ') },
    backgroundColor: 'transparent',
    color: [...context.colors.data],
    textStyle: { color: context.colors.foreground, fontFamily: context.fontFamily },
    dataset: dataset ? { id: `dataset:${dataset.id}`, source: selectedDatasetSource(envelope, dataset) } : undefined,
    tooltip: {
      trigger: tooltipTrigger(envelope),
      confine: true,
      backgroundColor: context.colors.surface,
      borderColor: context.colors.grid,
      textStyle: { color: context.colors.foreground, fontFamily: context.fontFamily },
      formatter: tooltipFormatter(envelope, context),
    },
    title: envelope.status.kind === 'error' ? { text: envelope.status.message ?? 'Visualization error', textStyle: { color: context.colors.danger } } : undefined,
    graphic: statusGraphic(envelope, context),
    visualMap: envelope.selection.length > 0 ? { show: false, dimension: '__lv_selected', pieces: [{ value: true, opacity: 1 }, { value: false, opacity: 0.35 }] } : undefined,
  }
}

function labelAccessibilitySummary(envelope: VisualizationEnvelope, context: RendererContext): string {
  const spec = envelope.spec
  if (!(
    spec.kind === 'cartesian'
    || spec.kind === 'point'
    || spec.kind === 'proportional'
    || spec.kind === 'hierarchy'
    || spec.kind === 'polar'
  )) return ''
  const policy = spec.presentation.labelPolicy
  if (policy.density === 'always' || !policy.tooltipFallback) return ''
  const dataset = inlineDataset(envelope)
  const schema = spec.datasets.find((candidate) => candidate.id === dataset?.id)
  if (!dataset || !schema || dataset.rows.length === 0) return ''
  const fields = schema.fields.filter((definition) => dataset.columns.includes(definition.id))
  const rowLimit = 6
  const rows = dataset.rows.slice(0, rowLimit).map((row) =>
    fields.map((definition) => {
      const index = dataset.columns.indexOf(definition.id)
      const formatted = formatField(envelope, { dataset: dataset.id, field: definition.id }, row[index], context)
      return `${definition.label}: ${formatted}`
    }).join(', '),
  )
  const remainder = dataset.rows.length - rows.length
  return `Data values: ${rows.join('; ')}.${remainder > 0 ? ` ${remainder} more rows.` : ''}`
}

function tooltipTrigger(envelope: VisualizationEnvelope): 'axis' | 'item' {
  if (envelope.spec.kind !== 'cartesian') return 'item'
  return envelope.spec.mark === 'heatmap' ? 'item' : 'axis'
}

function statusGraphic(envelope: VisualizationEnvelope, context: RendererContext): EChartsTranslation[] | undefined {
  if (envelope.status.kind === 'partial') {
    return [{ type: 'text', right: 8, top: 8, silent: true, style: { text: envelope.status.message ?? 'Partial data', fill: context.colors.attention, fontFamily: context.fontFamily, textAlign: 'right' } }]
  }
  if (envelope.status.kind !== 'idle' && envelope.status.kind !== 'loading' && envelope.status.kind !== 'no_data') return undefined
  const text = envelope.status.message ?? (envelope.status.kind === 'no_data' ? 'No data' : 'Loading…')
  return [{ type: 'text', left: 'center', top: 'middle', silent: true, style: { text, fill: context.colors.muted, fontFamily: context.fontFamily, textAlign: 'center' } }]
}

export function axis(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  type: 'category' | 'value' | 'time',
  context: RendererContext,
  axisID?: VisualizationCartesianAxis,
  scopeRefs?: VisualizationFieldRef[],
): EChartsTranslation {
  const displayUnit = displayUnitForField(envelope, ref, axisID, scopeRefs)
  return {
    type,
    axisLine: { lineStyle: { color: context.colors.grid } },
    axisTick: { lineStyle: { color: context.colors.grid } },
    splitLine: { lineStyle: { color: context.colors.grid } },
    axisLabel: { color: context.colors.muted, formatter: (value: unknown) => type === 'value' ? formatDisplayField(envelope, ref, value, context, displayUnit) : formatField(envelope, ref, value, context) },
    nameTextStyle: { color: context.colors.muted },
  }
}

export function legend(position: string, context: RendererContext, scroll = false): EChartsTranslation | undefined {
  if (position === 'hidden') return undefined
  return {
    show: true,
    ...(scroll ? {
      type: 'scroll',
      ...(position === 'left' || position === 'right' ? { top: 8, bottom: 8, width: 24 } : { left: 8, right: 8, height: 24 }),
      pageIconColor: context.colors.foreground,
      pageIconInactiveColor: context.colors.grid,
      pageTextStyle: { color: context.colors.muted, fontFamily: context.fontFamily },
    } : {}),
    orient: position === 'left' || position === 'right' ? 'vertical' : 'horizontal',
    [position]: 0,
    textStyle: { color: context.colors.muted, fontFamily: context.fontFamily },
  }
}

export function labelFormatter(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef | undefined,
  context: RendererContext,
  axisID?: VisualizationCartesianAxis,
  scopeRefs?: VisualizationFieldRef[],
) {
  const displayUnit = displayUnitForField(envelope, ref, axisID, scopeRefs)
  return (params: { value?: unknown }) => {
    const value = Array.isArray(params?.value) ? rowValue(envelope, ref, params.value) : params?.value
    return formatDisplayField(envelope, ref, value, context, displayUnit)
  }
}

export function toneColor(tone: string, context: RendererContext): string {
  switch (tone) {
    case 'success': return context.colors.success
    case 'warning': return context.colors.attention
    case 'danger': return context.colors.danger
    case 'ink': return context.colors.foreground
    default: return context.colors.accent
  }
}

export function escapeHTML(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;').replaceAll("'", '&#39;')
}

function tooltipFormatter(envelope: VisualizationEnvelope, context: RendererContext) {
  return (raw: unknown): string => {
    const params = Array.isArray(raw) ? raw : [raw]
    const entries: string[] = []
    for (const item of params) {
      const value = (item as { value?: unknown })?.value
      if (!Array.isArray(value)) continue
      const dataset = inlineDataset(envelope)
      if (!dataset) continue
      const schema = envelope.spec.datasets.find((candidate) => candidate.id === dataset.id)
      const authored = envelope.spec.kind === 'cartesian' || envelope.spec.kind === 'point' ? envelope.spec.tooltip : undefined
      const definitions = authored
        ? authored.flatMap((ref) => {
            if (ref.dataset !== dataset.id) return []
            const definition = schema?.fields.find((candidate) => candidate.id === ref.field)
            return definition ? [definition] : []
          })
        : schema?.fields ?? []
      for (const definition of definitions) {
        const index = dataset.columns.indexOf(definition.id)
        if (index < 0) continue
        const formatted = definition.format ? formatValue(context.locale, definition.format, value[index]) : value[index] === null || value[index] === undefined ? '—' : String(value[index])
        entries.push(`${escapeHTML(definition.label)}: ${escapeHTML(formatted)}`)
      }
      if (entries.length) break
    }
    return entries.join('<br>')
  }
}
