import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { resolveVisualizationMetadata } from '../../metadata'
import { field, inlineDataset, toneColor, type EChartsTranslation } from './common'
import { parseDecimal } from '../../decimal'
import { reduceReferenceValue } from './decimal-reference'
import { cartesianIsHorizontal, type CartesianSpec } from './series-intent'

type ReferenceValue = NonNullable<CartesianSpec['referenceLines']>[number]['value']

const authoredAxisNameGap = 25

export function applyDecisionContext(envelope: VisualizationEnvelope, context: RendererContext, option: EChartsTranslation): EChartsTranslation {
  const spec = envelope.spec
  if (spec.kind !== 'cartesian' && spec.kind !== 'point') return option
  const accessibilityDetails = [
    ...(spec.referenceLines ?? []).map((line) => line.label ? `Reference line: ${line.label}.` : ''),
    ...(spec.referenceBands ?? []).map((band) => band.label ? `Reference band: ${band.label}.` : ''),
    ...(spec.eventAnnotations ?? []).map((annotation) => `Event: ${annotation.label}${annotation.description ? ` — ${annotation.description}` : ''}.`),
  ].filter(Boolean)
  if (accessibilityDetails.length > 0) {
    const authoredDescription = resolveVisualizationMetadata(envelope).description.trim()
    const description = /[.!?]$/.test(authoredDescription) ? authoredDescription : `${authoredDescription}.`
    option.aria = { enabled: true, description: [description, ...accessibilityDetails].join(' ') }
  }
  for (const authored of spec.axes ?? []) {
    const horizontal = spec.kind === 'cartesian' && cartesianIsHorizontal(spec)
    const physical = authored.id === 'x'
      ? horizontal ? 'yAxis' : 'xAxis'
      : horizontal ? 'xAxis' : 'yAxis'
    const index = authored.id === 'secondary_y' ? 1 : 0
    const target = axisAt(option, physical, index)
    if (!target) continue
    const title = [authored.title, authored.unit ? `(${authored.unit})` : ''].filter(Boolean).join(' ')
    if (title) {
      // ECharts' default `end` placement puts a vertical Y name above the
      // plot and a horizontal X name at the far edge. Both are clipped by
      // our compact grid and become misleadingly absent. Center authored
      // names on their physical axis so the existing legend/dataZoom insets
      // continue to reserve the surrounding space.
      target.name = title
      target.nameLocation = 'middle'
      target.nameGap = authoredAxisNameGap
      target.nameMoveOverlap = true
      useAuthoredAxisNameLayout(option)
    }
    if (authored.scale === 'log') target.type = 'log'
    else if (authored.scale === 'linear') target.type = 'value'
    if (authored.minimum !== undefined) target.min = authored.minimum
    if (authored.maximum !== undefined) target.max = authored.maximum
    if (authored.zero === 'include') target.scale = false
    else if (authored.zero === 'exclude') target.scale = true
    applyTickDensity(target, authored.tickDensity)
  }

  const coordinate = (axisID: 'x' | 'primary_y' | 'secondary_y') => {
    const horizontal = spec.kind === 'cartesian' && cartesianIsHorizontal(spec)
    if (axisID === 'x') return horizontal ? 'yAxis' : 'xAxis'
    return horizontal ? 'xAxis' : 'yAxis'
  }
  const usesLogScale = (axisID: 'x' | 'primary_y' | 'secondary_y'): boolean => {
    // Prefer the translated axis type so this follows the effective physical
    // axis after horizontal and secondary-axis translation.
    const physical = coordinate(axisID)
    const index = axisID === 'secondary_y' ? 1 : 0
    const translated = axisOptionAt(option, physical, index)
    return translated?.type === 'log' || spec.axes?.some((axis) => axis.id === axisID && axis.scale === 'log') === true
  }
  const markLines = [
    ...(spec.referenceLines ?? []).flatMap((line) => {
      const value = resolveReferenceValue(envelope, line.value)
      if (value === undefined || !referenceValueAllowedOnAxis(value, usesLogScale(line.axis))) return []
      return [{
        axis: line.axis,
        data: { id: `reference-line:${line.id}`, name: line.label ?? '', [coordinate(line.axis)]: value, lineStyle: { color: toneColor(line.tone, context) } },
      }]
    }),
    ...(spec.eventAnnotations ?? []).flatMap((annotation) => {
      const value = resolveReferenceValue(envelope, annotation.value)
      if (value === undefined || !referenceValueAllowedOnAxis(value, usesLogScale(annotation.axis))) return []
      return [{
        axis: annotation.axis,
        data: { id: `event-annotation:${annotation.id}`, name: annotation.label, [coordinate(annotation.axis)]: value, lineStyle: { color: toneColor(annotation.tone, context) } },
      }]
    }),
  ]
  const markAreas = (spec.referenceBands ?? []).flatMap((band) => {
    const from = resolveReferenceValue(envelope, band.from)
    const to = resolveReferenceValue(envelope, band.to)
    const logAxis = usesLogScale(band.axis)
    if (from === undefined || to === undefined || !referenceValueAllowedOnAxis(from, logAxis) || !referenceValueAllowedOnAxis(to, logAxis)) return []
    const key = coordinate(band.axis)
    return [{ axis: band.axis, data: [
      { id: `reference-band:${band.id}`, name: band.label ?? '', [key]: from, itemStyle: { color: toneColor(band.tone, context), opacity: 0.12 } },
      { [key]: to },
    ] }]
  })
  if (markLines.length === 0 && markAreas.length === 0) return option
  const series = Array.isArray(option.series) ? option.series : []
  const candidates = series.filter((candidate: EChartsTranslation) => !candidate.silent && !String(candidate.id ?? '').startsWith('series:interaction-hit:'))
  for (const secondary of [false, true]) {
    const owner = candidates.find((candidate: EChartsTranslation) => {
      const axisIndex = coordinate('primary_y') === 'xAxis' ? candidate.xAxisIndex : candidate.yAxisIndex
      return secondary ? axisIndex === 1 : axisIndex !== 1
    })
    if (!owner) continue
    const lines = markLines.filter((item) => (item.axis === 'secondary_y') === secondary).map((item) => item.data)
    const areas = markAreas.filter((item) => (item.axis === 'secondary_y') === secondary).map((item) => item.data)
    if (lines.length > 0) owner.markLine = {
      symbol: ['none', 'none'],
      label: {
        show: true,
        position: 'insideEndTop',
        // A function keeps authored braces literal (ECharts string
        // formatters treat `{value}` as a template). Preserve the native
        // numeric/text value for an intentionally unnamed reference.
        formatter: (params: { name?: string; value?: unknown }) => params.name || String(params.value ?? ''),
      },
      data: lines,
    }
    if (areas.length > 0) owner.markArea = { silent: true, data: areas }
  }
  return option
}

function useAuthoredAxisNameLayout(option: EChartsTranslation): void {
  const grids = Array.isArray(option.grid) ? option.grid : [option.grid]
  for (const grid of grids) {
    if (!grid || typeof grid !== 'object' || Array.isArray(grid)) continue
    grid.containLabel = false
    grid.outerBoundsMode = 'same'
    grid.outerBoundsContain = 'all'
  }
}

function axisAt(option: EChartsTranslation, key: 'xAxis' | 'yAxis', index: number): EChartsTranslation | undefined {
  const current = option[key]
  if (Array.isArray(current)) return current[index]
  if (index === 0) return current
  if (!current) return undefined
  const secondary = structuredClone(current)
  option[key] = [current, secondary]
  return secondary
}

function axisOptionAt(option: EChartsTranslation, key: 'xAxis' | 'yAxis', index: number): EChartsTranslation | undefined {
  const current = option[key]
  if (Array.isArray(current)) return current[index]
  return index === 0 ? current : undefined
}

function referenceValueAllowedOnAxis(value: string | number, logScale: boolean): boolean {
  if (!logScale) return true
  if (typeof value === 'number') return Number.isFinite(value) && value > 0
  const decimal = parseDecimal(value)
  if (!decimal || decimal.negative) return false
  return decimal.integer !== '0' || /[1-9]/.test(decimal.fraction)
}

function applyTickDensity(axisOption: EChartsTranslation, density: 'automatic' | 'sparse' | 'normal' | 'dense'): void {
  if (density === 'automatic') return
  if (axisOption.type === 'category') {
    axisOption.axisLabel = { ...axisOption.axisLabel, interval: density === 'sparse' ? 2 : density === 'dense' ? 0 : 'auto' }
    return
  }
  axisOption.splitNumber = density === 'sparse' ? 3 : density === 'dense' ? 8 : 5
}

function resolveReferenceValue(envelope: VisualizationEnvelope, value: ReferenceValue): string | number | undefined {
  if (value.kind === 'number' || value.kind === 'text') return value.value
  const dataset = inlineDataset(envelope, value.field.dataset)
  const index = dataset?.columns.indexOf(value.field.field) ?? -1
  if (!dataset || index < 0) return undefined
  const dataType = field(envelope, value.field)?.dataType
  const numeric = dataType === 'integer' || dataType === 'decimal' || dataType === 'float'
  const values = dataset.rows.flatMap((row): (string | number)[] => {
    const candidate = row[index]
    if (typeof candidate === 'number') return Number.isFinite(candidate) ? [candidate] : []
    if (typeof candidate !== 'string') return []
    if (!numeric) return [candidate]
    return parseDecimal(candidate) ? [candidate] : []
  })
  if (values.length === 0) return undefined
  return reduceReferenceValue(values, value.reducer)
}
