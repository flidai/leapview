import type { VisualizationAxisConfiguration, VisualizationAxisLabelRotation, VisualizationCartesianAxis, VisualizationDateDisplayUnit, VisualizationDisplayUnits, VisualizationEnvelope, VisualizationField, VisualizationFieldRef, VisualizationPresentation } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { formatDisplayValue, formatValue, resolveDisplayUnitForFormat, type ResolvedDisplayUnit } from '../../format'
import { resolveVisualizationMetadata } from '../../metadata'
import { formatTooltipEntries } from '../../tooltip-format'

export type EChartsTranslation = Record<string, any>
export type LegendKnownValue = Readonly<{ value: string; name: string }>

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
    aria: { enabled: true, description: [description, dataSummary, completenessAccessibilitySummary(envelope)].filter(Boolean).join(' ') },
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
  if (!dataset || !schema) return ''
  if (dataset.rows.length === 0) {
    const incomplete = dataset.completeness === 'partial' || dataset.completeness === 'truncated'
    return incomplete ? '' : 'No data rows are available.'
  }
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

export function completenessAccessibilitySummary(envelope: VisualizationEnvelope): string {
  if (envelope.dataState.kind !== 'inline') return ''
  const datasets = envelope.dataState.datasets
  const partial = datasets.find((dataset) => dataset.completeness === 'partial')
  if (partial) return `Data is partial; ${partial.rows.length.toLocaleString()} rows are currently available.`
  const truncated = datasets.find((dataset) => dataset.completeness === 'truncated')
  if (truncated) return `Data is truncated; showing ${truncated.rows.length.toLocaleString()} rows.`
  if (datasets.length === 0 || datasets.every((dataset) => dataset.rows.length === 0 || dataset.completeness === 'empty')) return 'No data rows are available.'
  return ''
}

function tooltipTrigger(envelope: VisualizationEnvelope): 'axis' | 'item' {
  if (envelope.spec.kind !== 'cartesian') return 'item'
  return envelope.spec.mark === 'heatmap' ? 'item' : 'axis'
}

function statusGraphic(envelope: VisualizationEnvelope, context: RendererContext): EChartsTranslation[] | undefined {
  if (envelope.status.kind === 'partial') {
    return [{ type: 'text', right: 8, top: 8, silent: true, style: { text: envelope.status.message ?? 'Partial data', fill: context.colors.attention, fontFamily: context.fontFamily, textAlign: 'right' } }]
  }
  const inline = envelope.dataState.kind === 'inline'
  const datasets = inline ? (envelope.dataState as Extract<VisualizationEnvelope['dataState'], { kind: 'inline' }>).datasets : []
  const truncated = datasets.some((dataset) => dataset.completeness === 'truncated')
  const partial = datasets.some((dataset) => dataset.completeness === 'partial')
  if (partial || truncated) {
    return [{ type: 'text', right: 8, top: 8, silent: true, style: { text: partial ? 'Partial data' : 'Truncated data', fill: context.colors.attention, fontFamily: context.fontFamily, textAlign: 'right' } }]
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
  const policy = axisPolicy(envelope, axisID)
  const resolvedType = policy?.type && policy.type !== 'automatic' ? policy.type : type
  const displayUnit = displayUnitForField(envelope, ref, axisID, scopeRefs)
  const result: EChartsTranslation = {
    type: resolvedType,
    axisLine: { lineStyle: { color: context.colors.grid } },
    axisTick: { lineStyle: { color: context.colors.grid } },
    splitLine: { lineStyle: { color: context.colors.grid } },
    axisLabel: { color: context.colors.muted, formatter: (value: unknown) => resolvedType === 'value'
      ? formatDisplayField(envelope, ref, value, context, displayUnit)
      : resolvedType === 'time' ? formatAxisDate(envelope, ref, value, context, policy?.dateUnit) : formatField(envelope, ref, value, context) },
    nameTextStyle: { color: context.colors.muted },
  }
  applyAxisVisibility(result, policy)
  applyAxisRotation(result, policy?.labelRotation)
  if (policy?.inversion === 'inverted') result.inverse = true
  return result
}

function axisPolicy(envelope: VisualizationEnvelope, axisID?: VisualizationCartesianAxis): VisualizationAxisConfiguration | undefined {
  if (!axisID || (envelope.spec.kind !== 'cartesian' && envelope.spec.kind !== 'point')) return undefined
  return envelope.spec.axes?.find((candidate) => candidate.id === axisID)
}

function applyAxisVisibility(axisOption: EChartsTranslation, policy: VisualizationAxisConfiguration | undefined): void {
  if (policy?.ticks === 'hidden') axisOption.axisTick = { ...axisOption.axisTick, show: false }
  else if (policy?.ticks === 'visible') axisOption.axisTick = { ...axisOption.axisTick, show: true }
  if (policy?.grid === 'hidden') axisOption.splitLine = { ...axisOption.splitLine, show: false }
  else if (policy?.grid === 'visible') axisOption.splitLine = { ...axisOption.splitLine, show: true }
}

function applyAxisRotation(axisOption: EChartsTranslation, rotation: VisualizationAxisLabelRotation | undefined): void {
  switch (rotation) {
    case 'horizontal': axisOption.axisLabel = { ...axisOption.axisLabel, rotate: 0 }; break
    case 'diagonal': axisOption.axisLabel = { ...axisOption.axisLabel, rotate: 45 }; break
    case 'vertical': axisOption.axisLabel = { ...axisOption.axisLabel, rotate: 90 }; break
  }
}

function formatAxisDate(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  value: unknown,
  context: RendererContext,
  unit: VisualizationDateDisplayUnit | undefined,
): string {
  if (value === null || value === undefined) return '—'
  const definition = field(envelope, ref)
  if ((!unit || unit === 'automatic') && definition?.format && typeof value === 'string') return formatField(envelope, ref, value, context)
  const date = new Date(typeof value === 'number' ? value : String(value))
  if (!Number.isFinite(date.getTime())) return formatField(envelope, ref, value, context)
  if ((!unit || unit === 'automatic') && definition?.format?.kind === 'temporal') {
    const canonical = definition.dataType === 'date' ? date.toISOString().slice(0, 10) : date.toISOString()
    return formatField(envelope, ref, canonical, context)
  }
  const year = date.getUTCFullYear()
  if (unit === 'quarter') return `Q${Math.floor(date.getUTCMonth() / 3) + 1} ${year}`
  if (unit === 'week') {
    const week = isoWeek(date)
    return `W${week.number} ${week.year}`
  }
  const options: Intl.DateTimeFormatOptions = unit === 'year'
    ? { year: 'numeric' }
    : unit === 'month'
      ? { year: 'numeric', month: 'short' }
      : unit === 'day'
        ? { year: 'numeric', month: 'short', day: 'numeric' }
        : unit === 'hour'
          ? { month: 'short', day: 'numeric', hour: 'numeric', timeZone: 'UTC' }
          : unit === 'minute'
            ? { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', timeZone: 'UTC' }
            : unit === 'second'
              ? { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit', second: '2-digit', timeZone: 'UTC' }
              : { year: 'numeric', month: 'short', day: 'numeric', timeZone: 'UTC' }
  return new Intl.DateTimeFormat(context.locale, { ...options, timeZone: 'UTC' }).format(date)
}

function isoWeek(date: Date): { number: string; year: number } {
  const thursday = new Date(Date.UTC(date.getUTCFullYear(), date.getUTCMonth(), date.getUTCDate()))
  thursday.setUTCDate(thursday.getUTCDate() + 4 - (thursday.getUTCDay() || 7))
  const first = new Date(Date.UTC(thursday.getUTCFullYear(), 0, 1))
  const week = Math.ceil(((thursday.getTime() - first.getTime()) / 86400000 + 1) / 7)
  return { number: String(week).padStart(2, '0'), year: thursday.getUTCFullYear() }
}

export function legend(
  position: string,
  context: RendererContext,
  scroll = false,
  presentation?: Pick<VisualizationPresentation, 'legendTitle' | 'legendItems'>,
  knownValues?: readonly LegendKnownValue[],
): EChartsTranslation | undefined {
  if (position === 'hidden') return undefined
  const configuredItems = presentation?.legendItems
  const uniqueKnownValues = knownValues?.filter((item, index, values) => values.findIndex((candidate) => candidate.value === item.value) === index)
  const known = uniqueKnownValues ? new Map(uniqueKnownValues.map((item) => [item.value, item.name])) : undefined
  const selectedItems = configuredItems
    ? configuredItems.filter((item) => known === undefined || known.has(item.value))
    : undefined
  const result: EChartsTranslation = {
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
  if (selectedItems) {
    const configuredValues = new Set(selectedItems.map((item) => item.value))
    const orderedValues = [
      ...selectedItems.map((item) => ({ value: item.value, name: known?.get(item.value) ?? item.value })),
      ...(uniqueKnownValues ?? []).filter((item) => !configuredValues.has(item.value)),
    ]
    result.data = orderedValues.map((item) => ({ name: item.name }))
    const labels = new Map(selectedItems.map((item) => {
      const canonicalName = known?.get(item.value) ?? item.value
      return [canonicalName, item.label ?? canonicalName]
    }))
    result.formatter = (value: string) => labels.get(value) ?? value
  }
  return result
}

export function legendDecoration(
  position: string,
  context: RendererContext,
  scroll = false,
  presentation?: Pick<VisualizationPresentation, 'legendTitle' | 'legendItems'>,
  knownValues?: readonly LegendKnownValue[],
): EChartsTranslation {
  const result: EChartsTranslation = { legend: legend(position, context, scroll, presentation, knownValues) }
  const title = presentation?.legendTitle
  if (title === undefined || position === 'hidden') return result
  const titleGraphic: EChartsTranslation = {
    type: 'text', silent: true,
    style: { text: title, fill: context.colors.foreground, fontFamily: context.fontFamily, fontSize: 12, fontWeight: 600 },
  }
  if (position === 'bottom') {
    titleGraphic.left = 8
    titleGraphic.bottom = 28
  } else if (position === 'left' || position === 'right') {
    titleGraphic.top = 4
    titleGraphic[position] = 8
    if (result.legend) result.legend.top = scroll ? 28 : 24
  } else {
    titleGraphic.left = 8
    titleGraphic.top = 4
    if (result.legend) result.legend.top = scroll ? 28 : 24
  }
  result.graphic = [titleGraphic]
  return result
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

type TooltipFormatterOptions = Readonly<{ fallbackRefs?: readonly VisualizationFieldRef[] }>

export function tooltipFormatterForRow(
  envelope: VisualizationEnvelope,
  context: RendererContext,
  options: TooltipFormatterOptions = {},
) {
  return (raw: unknown): string => {
    const params = Array.isArray(raw) ? raw : [raw]
    for (const item of params) {
      const event = item as { value?: unknown; data?: { __lv_dataset?: unknown; __lv_row_index?: unknown } }
      const locatorDataset = typeof event.data?.__lv_dataset === 'string' ? event.data.__lv_dataset : undefined
      const locatorIndex = event.data?.__lv_row_index
      const locatedDataset = locatorDataset ? inlineDataset(envelope, locatorDataset) : undefined
      const locatedRow = locatedDataset && Number.isInteger(locatorIndex) ? locatedDataset.rows[locatorIndex as number] : undefined
      const dataset = locatedDataset ?? inlineDataset(envelope)
      const row = locatedRow ?? (Array.isArray(event.value) ? event.value : undefined)
      if (!dataset || !row) continue
      const refs = tooltipRefs(envelope, dataset.id, options.fallbackRefs)
      const entries = formatTooltipEntries(envelope, row, dataset.id, context, refs, envelope.spec.tooltipItems)
      if (entries.length) return entries.map((entry) => `${escapeHTML(entry.label)}: ${escapeHTML(entry.value)}`).join('<br>')
      if (envelope.spec.tooltipItems !== undefined && envelope.spec.tooltipItems.length === 0) return ''
    }
    return ''
  }
}

function tooltipRefs(envelope: VisualizationEnvelope, datasetID: string, fallbackRefs?: readonly VisualizationFieldRef[]): VisualizationFieldRef[] {
  if (envelope.spec.tooltipItems !== undefined) return envelope.spec.tooltipItems.map((item) => item.field)
  if ((envelope.spec.kind === 'cartesian' || envelope.spec.kind === 'point' || envelope.spec.kind === 'proportional') && envelope.spec.tooltip !== undefined) return envelope.spec.tooltip
  if (fallbackRefs !== undefined) return [...fallbackRefs]
  const schema = envelope.spec.datasets.find((candidate) => candidate.id === datasetID)
  return (schema?.fields ?? []).map((definition) => ({ dataset: datasetID, field: definition.id }))
}

function tooltipFormatter(envelope: VisualizationEnvelope, context: RendererContext) {
  return tooltipFormatterForRow(envelope, context)
}
