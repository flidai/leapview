import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { formatField, inlineDataset, legendDecoration, tooltipFormatterForRow, type EChartsTranslation } from './common'
import { seriesColor } from './conditional-color'
import { decimalShift, parseDecimal, type DecimalParts } from '../../decimal'

/** Translate the bounded financial mark family without changing source rows. */
export function financialOption(
  envelope: VisualizationEnvelope,
  context: RendererContext,
  axes: EChartsTranslation,
  dataZoom: EChartsTranslation[] | undefined,
): EChartsTranslation | undefined {
  const spec = envelope.spec
  if (spec.kind !== 'cartesian' || spec.mark !== 'candlestick') return undefined
  const dataset = inlineDataset(envelope, spec.x.dataset)
  const categoryIndex = dataset?.columns.indexOf(spec.x.field) ?? -1
  const valueIndices = spec.y.map((item) => dataset?.columns.indexOf(item.field) ?? -1)
  const gain = seriesColor('', spec.presentation.gainColor ?? 'success', context)
  const loss = seriesColor('', spec.presentation.lossColor ?? 'danger', context)
  const neutral = seriesColor('', 'neutral', context)
  let precisionSensitive = false
  const data = (dataset?.rows ?? []).map((row, rowIndex) => {
    const value = valueIndices.map((index) => row[index])
    const item = {
      name: formatField(envelope, spec.x, row[categoryIndex], context),
      value,
      __lv_dataset: dataset?.id ?? spec.x.dataset,
      __lv_row_index: rowIndex,
    }
    const openValue = value[0]
    const closeValue = value[1]
    const comparison = candleComparison(openValue, closeValue)
    precisionSensitive ||= comparison.precisionSensitive
    const color = comparison.direction === 'gain' ? gain : comparison.direction === 'loss' ? loss : neutral
    // ECharts normal rendering still derives its sign through Number values.
    // Resolve all style slots for exact or doji rows so a precision-collapsed
    // sign cannot change the visible color. Large mode is disabled below when
    // that collapse could affect its sign-grouped paths.
    return comparison.direction !== undefined && (comparison.direction === 'neutral' || comparison.precisionSensitive)
      ? { ...item, itemStyle: resolvedCandleStyle(color) }
      : item
  })
  return {
    ...axes,
    xAxis: { ...axes.xAxis, data: (dataset?.rows ?? []).map((row) => formatField(envelope, spec.x, row[categoryIndex], context)) },
    dataZoom,
    ...legendDecoration(spec.presentation.legend, context, false, spec.presentation, [{ value: spec.title, name: spec.title }]),
    series: [{
      id: 'series:primary:candlestick', type: 'candlestick', name: spec.title, data,
      ...(precisionSensitive ? { large: false } : {}),
      itemStyle: { color: gain, color0: loss, borderColor: gain, borderColor0: loss, borderColorDoji: neutral },
      tooltip: { formatter: tooltipFormatterForRow(envelope, context, { fallbackRefs: [spec.x, ...spec.y] }) },
    }],
  }
}

type CandleDirection = 'gain' | 'loss' | 'neutral'

type CandleComparison = {
  direction?: CandleDirection
  precisionSensitive: boolean
}

function candleComparison(openValue: unknown, closeValue: unknown): CandleComparison {
  if (missingCandleValue(openValue) || missingCandleValue(closeValue)) return { precisionSensitive: false }
  const exactOpen = decimalParts(openValue)
  const exactClose = decimalParts(closeValue)
  if (!exactOpen || !exactClose) return { precisionSensitive: false }
  const exact = compareDecimalParts(exactOpen, exactClose)
  const numericOpen = Number(openValue)
  const numericClose = Number(closeValue)
  const numeric = Number.isFinite(numericOpen) && Number.isFinite(numericClose)
    ? numericOpen < numericClose ? 1 : numericOpen > numericClose ? -1 : 0
    : undefined
  const directionComparison = -exact
  return {
    direction: directionForComparison(directionComparison),
    precisionSensitive: numeric !== directionComparison,
  }
}

function missingCandleValue(value: unknown): boolean {
  return value === null || value === undefined || value === ''
}

function directionForComparison(comparison: number): CandleDirection {
  return comparison > 0 ? 'gain' : comparison < 0 ? 'loss' : 'neutral'
}

function decimalParts(value: unknown): DecimalParts | undefined {
  if (typeof value === 'string') return parseDecimal(value)
  if (typeof value !== 'number' || !Number.isFinite(value)) return undefined
  const source = String(value).toLowerCase()
  const [mantissa, exponentText] = source.split('e')
  if (!mantissa) return undefined
  const exponent = exponentText === undefined ? 0 : Number(exponentText)
  if (!parseDecimal(mantissa) || !Number.isInteger(exponent)) return undefined
  return parseDecimal(exponent === 0 ? mantissa : decimalShift(mantissa, exponent))
}

function compareDecimalParts(a: DecimalParts, b: DecimalParts): number {
  if (a.negative !== b.negative) return a.negative ? -1 : 1
  const magnitude = compareUnsignedDecimals(a.integer, a.fraction, b.integer, b.fraction)
  return a.negative ? -magnitude : magnitude
}

function compareUnsignedDecimals(leftInteger: string, leftFraction: string, rightInteger: string, rightFraction: string): number {
  if (leftInteger.length !== rightInteger.length) return leftInteger.length < rightInteger.length ? -1 : 1
  if (leftInteger !== rightInteger) return leftInteger < rightInteger ? -1 : 1
  const scale = Math.max(leftFraction.length, rightFraction.length)
  const left = leftFraction.padEnd(scale, '0')
  const right = rightFraction.padEnd(scale, '0')
  return left === right ? 0 : left < right ? -1 : 1
}

function resolvedCandleStyle(color: string): Record<string, string> {
  return { color, color0: color, borderColor: color, borderColor0: color, borderColorDoji: color }
}
