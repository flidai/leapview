import type {
  VisualizationEnvelope,
  VisualizationFormat,
  VisualizationFieldRef,
  VisualizationKPIQualitativeRange,
  VisualizationKPIValueBinding,
  VisualizationReferenceReducer,
  VisualizationTone,
} from '../../../../generated/visualization'
import type { RendererContext } from '../host-controller'
import { formatDisplayValue, formatValue, resolveDisplayUnitForFormat, type ResolvedDisplayUnit } from '../format'
import { decimalFraction, decimalSignedInteger, decimalToString, parseDecimal as parseKPIDecimal } from '../decimal'

export type KPIChangeStatus = 'favorable' | 'unfavorable' | 'neutral' | 'unavailable'

export interface KPITrendPoint {
  label: string
  value: number
}

type KPIValue = number | string

export interface KPIBulletRange {
  start: number
  end: number
  label: string
  tone: VisualizationTone
}

export interface KPIState {
  current?: KPIValue
  currentText: string
  comparison?: KPIValue
  comparisonText?: string
  comparisonLabel?: string
  delta?: KPIValue
  deltaText?: string
  deltaCue?: string
  changeStatus?: KPIChangeStatus
  goal?: KPIValue
  goalText?: string
  goalLabel?: string
  progress?: number
  bulletValuePosition?: number
  bulletGoalPosition?: number
  bulletMinimum?: number
  bulletMaximum?: number
  bulletRanges: KPIBulletRange[]
  rangeLabel?: string
  rangeTone?: VisualizationTone
  trend: KPITrendPoint[]
  highlightActive: boolean
  highlightAnnouncement?: string
  accessibleSummary: string
}

export function resolveKPIState(envelope: VisualizationEnvelope, context: RendererContext): KPIState {
  const spec = envelope.spec
  if (spec.kind !== 'kpi') {
    return { currentText: '—', trend: [], bulletRanges: [], highlightActive: false, accessibleSummary: 'Value unavailable.' }
  }
  const current = numericScalar(envelope, spec.value)
  const comparison = spec.comparison ? numericReduction(envelope, spec.comparison) : undefined
  const comparisonVisible = spec.comparison && (comparison !== undefined || spec.presentation.missingComparison === 'show_unavailable')
  const goal = spec.goal ? numericReduction(envelope, spec.goal) : undefined
  const displayUnit = resolveDisplayUnitForFormat(
    spec.presentation.displayUnits ?? 'auto',
    fieldFormat(envelope, spec.value),
    [current, comparison, goal],
  )
  const currentText = formatDisplayField(envelope, spec.value, current, context, displayUnit)
  const comparisonText = comparisonVisible ? formatDisplayField(envelope, spec.comparison!.field, comparison, context, displayUnit) : undefined
  const delta = current === undefined || comparison === undefined
    ? undefined
    : spec.presentation.delta === 'relative'
      ? isZeroKPI(comparison) ? undefined : divideKPI(subtractKPI(current, comparison), absKPI(comparison))
      : subtractKPI(current, comparison)
  const changeStatus = spec.comparison
    ? delta === undefined ? 'unavailable' : changeStatusFor(signKPI(delta), spec.presentation.favorableDirection)
    : undefined
  const deltaText = spec.comparison && comparisonVisible
    ? delta === undefined ? 'Unavailable' : formatDelta(envelope, spec.value, delta, spec.presentation.delta, context, displayUnit)
    : undefined
  const deltaCue = delta === undefined ? undefined : signKPI(delta) > 0 ? '↑' : signKPI(delta) < 0 ? '↓' : '•'
  const goalText = spec.goal ? formatDisplayField(envelope, spec.goal.field, goal, context, displayUnit) : undefined
  const progress = current === undefined || goal === undefined || toApproximateNumber(goal) <= 0
    ? undefined
    : clamp(toApproximateNumber(current) / toApproximateNumber(goal), 0, 1)
  const bullet = spec.presentation.mode === 'bullet'
    ? bulletGeometry(toApproximateNumberOrUndefined(current), toApproximateNumberOrUndefined(goal), spec.presentation.ranges)
    : undefined
  const qualitativeRange = current === undefined
    ? undefined
    : spec.presentation.ranges.find((candidate, index) =>
      (candidate.minimum === undefined || toApproximateNumber(current) >= candidate.minimum) &&
      (candidate.maximum === undefined || toApproximateNumber(current) < candidate.maximum || index === spec.presentation.ranges.length - 1 && toApproximateNumber(current) === candidate.maximum))
  const trend = spec.trend ? trendPoints(envelope, spec.trend.category, spec.trend.value) : []

  const summary = [`Current ${formatField(envelope, spec.value, current, context)}.`]
  if (spec.comparison && comparisonVisible) {
    summary.push(`${spec.comparison.label} ${formatField(envelope, spec.comparison.field, comparison, context)}.`)
    const distinctChangeStatus = changeStatus && changeStatus.toLowerCase() !== deltaText?.toLowerCase()
      ? changeStatus
      : undefined
    summary.push(`Change ${deltaText}${distinctChangeStatus ? `, ${distinctChangeStatus}` : ''}.`)
  }
  if (spec.goal) summary.push(`${spec.goal.label} ${formatField(envelope, spec.goal.field, goal, context)}.`)
  if (qualitativeRange) summary.push(`Status ${qualitativeRange.label}.`)
  else if (spec.presentation.ranges.length > 0 && current !== undefined) summary.push('Status out of range.')
  if (trend.length > 1) summary.push(`Trend includes ${trend.length} points, from ${trend[0]!.label} to ${trend.at(-1)!.label}.`)
  const highlights = envelope.highlights ?? []
  const highlightActive = highlights.length > 0
  const highlightAnnouncement = highlightActive
    ? `${highlights.map((highlight) => highlight.label).filter(Boolean).join(' · ') || 'Selection'} highlighted. Comparison total is unchanged.`
    : undefined
  if (highlightAnnouncement) summary.push(highlightAnnouncement)

  return {
    ...(current === undefined ? {} : { current }),
    currentText,
    ...(comparison === undefined ? {} : { comparison }),
    ...(comparisonText === undefined ? {} : { comparisonText }),
    ...(spec.comparison && comparisonVisible ? { comparisonLabel: spec.comparison.label } : {}),
    ...(delta === undefined ? {} : { delta }),
    ...(deltaText === undefined ? {} : { deltaText }),
    ...(deltaCue === undefined ? {} : { deltaCue }),
    ...(changeStatus === undefined ? {} : { changeStatus }),
    ...(goal === undefined ? {} : { goal }),
    ...(goalText === undefined ? {} : { goalText }),
    ...(spec.goal ? { goalLabel: spec.goal.label } : {}),
    ...(progress === undefined ? {} : { progress }),
    ...(bullet?.valuePosition === undefined ? {} : { bulletValuePosition: bullet.valuePosition }),
    ...(bullet?.goalPosition === undefined ? {} : { bulletGoalPosition: bullet.goalPosition }),
    ...(bullet ? { bulletMinimum: bullet.minimum, bulletMaximum: bullet.maximum } : {}),
    bulletRanges: bullet?.ranges ?? [],
    ...(qualitativeRange ? { rangeLabel: qualitativeRange.label, rangeTone: qualitativeRange.tone } : {}),
    trend,
    highlightActive,
    ...(highlightAnnouncement ? { highlightAnnouncement } : {}),
    accessibleSummary: summary.join(' '),
  }
}

export function bulletGeometry(
  current: number | undefined,
  goal: number | undefined,
  ranges: VisualizationKPIQualitativeRange[],
): {
  minimum: number
  maximum: number
  valuePosition?: number
  goalPosition?: number
  ranges: KPIBulletRange[]
} {
  const candidates = [0, current, goal, ...ranges.flatMap((range) => [range.minimum, range.maximum])]
    .filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  let minimum = Math.min(...candidates)
  let maximum = Math.max(...candidates)
  if (minimum === maximum) maximum = minimum + 1
  if (ranges.at(-1)?.maximum === undefined) {
    maximum += (maximum - minimum) * 0.1
  }
  const position = (value: number | undefined) => value === undefined ? undefined : clamp((value - minimum) / (maximum - minimum), 0, 1)
  const normalizedRanges = ranges.flatMap((range) => {
    const start = position(range.minimum ?? minimum)!
    const end = position(range.maximum ?? maximum)!
    return end <= start ? [] : [{ start, end, label: range.label, tone: range.tone }]
  })
  return {
    minimum,
    maximum,
    ...(current === undefined ? {} : { valuePosition: position(current) }),
    ...(goal === undefined ? {} : { goalPosition: position(goal) }),
    ranges: normalizedRanges,
  }
}

export function kpiSparklinePath(points: KPITrendPoint[], width = 100, height = 28): string {
  if (points.length === 0) return ''
  // Sparkline coordinates are deliberately approximate geometry.
  const values = points.map((point) => toApproximateNumber(point.value))
  const minimum = Math.min(...values)
  const maximum = Math.max(...values)
  const span = maximum - minimum
  return points.map((point, index) => {
    const x = points.length === 1 ? width / 2 : index / (points.length - 1) * width
    const y = span === 0 ? height / 2 : height - (toApproximateNumber(point.value) - minimum) / span * height
    return `${index === 0 ? 'M' : 'L'}${roundCoordinate(x)},${roundCoordinate(y)}`
  }).join(' ')
}

function numericReduction(envelope: VisualizationEnvelope, binding: VisualizationKPIValueBinding): KPIValue | undefined {
  const values = numericValues(envelope, binding.field)
  if (values.length === 0) return undefined
  return reduce(values, binding.reducer)
}

function numericScalar(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): KPIValue | undefined {
  return numericValues(envelope, ref)[0]
}

function numericValues(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): KPIValue[] {
  if (envelope.dataState.kind !== 'inline') return []
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  if (!dataset || index < 0) return []
  return dataset.rows.flatMap((row): KPIValue[] => {
    const value = row[index]
    if (typeof value === 'number' && Number.isFinite(value)) return [value]
    if (typeof value === 'string' && parseKPIDecimal(value)) return [value]
    return []
  })
}

function reduce(values: KPIValue[], reducer: VisualizationReferenceReducer): KPIValue {
  switch (reducer) {
    case 'first': return values[0]!
    case 'last': return values.at(-1)!
    case 'minimum': return values.reduce((best, value) => compareKPI(value, best) < 0 ? value : best)
    case 'maximum': return values.reduce((best, value) => compareKPI(value, best) > 0 ? value : best)
    case 'mean': return meanKPI(values)
    case 'median': {
      const sorted = [...values].sort(compareKPI)
      const middle = Math.floor(sorted.length / 2)
      return sorted.length % 2 === 0 ? meanKPI([sorted[middle - 1]!, sorted[middle]!]) : sorted[middle]!
    }
  }
}

function trendPoints(envelope: VisualizationEnvelope, category: VisualizationFieldRef, value: VisualizationFieldRef): KPITrendPoint[] {
  if (envelope.dataState.kind !== 'inline' || category.dataset !== value.dataset) return []
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === category.dataset)
  const categoryIndex = dataset?.columns.indexOf(category.field) ?? -1
  const valueIndex = dataset?.columns.indexOf(value.field) ?? -1
  if (!dataset || categoryIndex < 0 || valueIndex < 0) return []
  return dataset.rows.flatMap((row) => {
    const numeric = row[valueIndex]
    const approximate = typeof numeric === 'number'
      ? numeric
      : typeof numeric === 'string' && parseKPIDecimal(numeric)
        ? Number(numeric)
        : Number.NaN
    if (!Number.isFinite(approximate)) return []
    const rawLabel = row[categoryIndex]
    return [{ label: rawLabel === null || rawLabel === undefined ? '—' : String(rawLabel), value: approximate }]
  })
}

function changeStatusFor(delta: number, direction: 'increase' | 'decrease' | 'neutral'): KPIChangeStatus {
  if (delta === 0 || direction === 'neutral') return 'neutral'
  const favorable = direction === 'increase' ? delta > 0 : delta < 0
  return favorable ? 'favorable' : 'unfavorable'
}

function formatField(envelope: VisualizationEnvelope, ref: VisualizationFieldRef, value: KPIValue | undefined, context: RendererContext): string {
  const field = envelope.spec.datasets.find((dataset) => dataset.id === ref.dataset)?.fields.find((candidate) => candidate.id === ref.field)
  if (value === undefined) return '—'
  return field?.format ? formatValue(context.locale, field.format, value) : String(value)
}

function fieldFormat(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): VisualizationFormat | undefined {
  return envelope.spec.datasets.find((dataset) => dataset.id === ref.dataset)?.fields.find((candidate) => candidate.id === ref.field)?.format
}

function formatDisplayField(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  value: KPIValue | undefined,
  context: RendererContext,
  displayUnit: ResolvedDisplayUnit,
): string {
  if (value === undefined) return '—'
  return formatDisplayValue(context.locale, fieldFormat(envelope, ref) ?? { kind: 'number' }, value, displayUnit)
}

function formatDelta(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  delta: KPIValue,
  mode: 'absolute' | 'relative',
  context: RendererContext,
  displayUnit: ResolvedDisplayUnit,
): string {
  if (mode === 'relative') {
    const sign = signKPI(delta)
    const rounded = formatValue(context.locale, { kind: 'percent', minimumFractionDigits: 0, maximumFractionDigits: 1 }, absKPI(delta))
    return `${sign > 0 ? '+' : sign < 0 ? '−' : ''}${rounded}`
  }
  const formatted = formatDisplayField(envelope, ref, absKPI(delta), context, displayUnit)
  const sign = signKPI(delta)
  return `${sign > 0 ? '+' : sign < 0 ? '−' : ''}${formatted}`
}

function signKPI(value: KPIValue): number {
  if (typeof value === 'number') return Math.sign(value)
  const parsed = parseKPIDecimal(value)
  if (!parsed) return 0
  if (parsed.integer !== '0' || /[1-9]/.test(parsed.fraction)) return parsed.negative ? -1 : 1
  return 0
}

function isZeroKPI(value: KPIValue): boolean { return signKPI(value) === 0 }

function compareKPI(left: KPIValue, right: KPIValue): number {
  if (typeof left === 'number' && typeof right === 'number') return left - right
  const a = toDecimalKPI(left), b = toDecimalKPI(right)
  if (a.negative !== b.negative) return a.negative ? -1 : 1
  const sign = a.negative ? -1 : 1
  const scale = Math.max(a.scale, b.scale)
  const av = a.digits * 10n ** BigInt(scale - a.scale)
  const bv = b.digits * 10n ** BigInt(scale - b.scale)
  return (av < bv ? -1 : av > bv ? 1 : 0) * sign
}

function toDecimalKPI(value: KPIValue): { negative: boolean; digits: bigint; scale: number } {
  const parsed = decimalSignedInteger(value)
  return { negative: parsed.signedDigits < 0n, digits: parsed.digits, scale: parsed.scale }
}

function decimalKPIString(decimal: { negative: boolean; digits: bigint; scale: number }): string {
  return decimalToString(decimal.negative, decimal.digits, decimal.scale)
}

function subtractKPI(left: KPIValue, right: KPIValue): KPIValue {
  if (typeof left === 'number' && typeof right === 'number') return left - right
  const a = toDecimalKPI(left), b = toDecimalKPI(right)
  const scale = Math.max(a.scale, b.scale)
  const av = (a.negative ? -1n : 1n) * a.digits * 10n ** BigInt(scale - a.scale)
  const bv = (b.negative ? -1n : 1n) * b.digits * 10n ** BigInt(scale - b.scale)
  const result = av - bv
  return decimalKPIString({ negative: result < 0n, digits: result < 0n ? -result : result, scale })
}

function absKPI(value: KPIValue): KPIValue {
  if (typeof value === 'number') return Math.abs(value)
  return value.startsWith('-') ? value.slice(1) : value
}

function divideKPI(left: KPIValue, right: KPIValue): KPIValue {
  if (typeof left === 'number' && typeof right === 'number') return left / right
  const numerator = toSignedDecimalKPI(left)
  const denominator = toSignedDecimalKPI(right)
  if (denominator.digits === 0n) return '0'
  return decimalFractionKPI(numerator.signedDigits, 10n ** BigInt(numerator.scale), denominator.signedDigits, 10n ** BigInt(denominator.scale), 18)
}

function meanKPI(values: KPIValue[]): KPIValue {
  if (values.every((value) => typeof value === 'number')) return values.reduce((sum, value) => sum + (value as number), 0) / values.length
  const decimals = values.map(toDecimalKPI)
  const scale = Math.max(...decimals.map((value) => value.scale))
  const sum = decimals.reduce((total, value) => total + (value.negative ? -1n : 1n) * value.digits * 10n ** BigInt(scale - value.scale), 0n)
  return decimalFractionKPI(sum, 10n ** BigInt(scale), BigInt(values.length), 1n, 18)
}

function toSignedDecimalKPI(value: KPIValue): { signedDigits: bigint; digits: bigint; scale: number } {
  const decimal = toDecimalKPI(value)
  return { signedDigits: (decimal.negative ? -1n : 1n) * decimal.digits, digits: decimal.digits, scale: decimal.scale }
}

function decimalFractionKPI(
  numerator: bigint,
  numeratorScale: bigint,
  denominator: bigint,
  denominatorScale: bigint,
  maximumScale: number,
): string {
  return decimalFraction(numerator, numeratorScale, denominator, denominatorScale, maximumScale)
}

function toApproximateNumber(value: KPIValue): number {
  if (typeof value === 'number') return value
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : 0
}

function toApproximateNumberOrUndefined(value: KPIValue | undefined): number | undefined {
  return value === undefined ? undefined : toApproximateNumber(value)
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimum, value))
}

function roundCoordinate(value: number): number {
  return Math.round(value * 100) / 100
}
