import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { displayUnitForField, formatDisplayField, formatField, inlineDataset, legend, toneColor, type EChartsTranslation } from './common'
import { echartsLabelPolicy, truncateVisualizationLabel } from './label-policy'
import { parseDecimal } from '../../decimal'

export function polarOption(envelope: VisualizationEnvelope, context: RendererContext): EChartsTranslation {
  const spec = envelope.spec
  if (spec.kind !== 'polar') return {}
  if (spec.mark === 'gauge') {
    if (spec.presentation.minimum === undefined || spec.presentation.maximum === undefined) {
      throw new Error('Gauge rendering requires an explicit minimum and maximum')
    }
    const dataset = inlineDataset(envelope, spec.value.dataset)
    const valueIndex = dataset?.columns.indexOf(spec.value.field) ?? -1
    const value = valueIndex >= 0 ? dataset?.rows[0]?.[valueIndex] : undefined
    const approximateValue = approximateNumericValue(value)
    const minimum = spec.presentation.minimum, maximum = spec.presentation.maximum
    const displayUnit = displayUnitForField(envelope, spec.value, undefined, [spec.value], [minimum, maximum, spec.presentation.target])
    if (approximateValue !== undefined && (approximateValue < minimum || approximateValue > maximum)) {
      const formattedValue = formatField(envelope, spec.value, value, context)
      const formattedMinimum = formatField(envelope, spec.value, minimum, context)
      const formattedMaximum = formatField(envelope, spec.value, maximum, context)
      return {
        series: [],
        graphic: [{
          type: 'text',
          left: 'center',
          top: 'middle',
          silent: true,
          style: {
            text: `Value ${formattedValue} is outside configured gauge domain ${formattedMinimum}–${formattedMaximum}`,
            fill: context.colors.danger,
            fontFamily: context.fontFamily,
            textAlign: 'center',
          },
        }],
      }
    }
    const span = maximum - minimum
    const authoredColors = (spec.presentation.thresholds ?? []).map((threshold) => [Math.max(0, Math.min(1, span > 0 ? (threshold.value - minimum) / span : 1)), toneColor(threshold.tone, context)])
    const colors = authoredColors.length ? authoredColors : [[1, context.colors.accent]]
    const labelPolicy = spec.presentation.labelPolicy
    const showDetail = labelPolicy.density !== 'hidden'
    const series: Record<string, any>[] = [{
      id: 'series:polar:gauge', type: 'gauge', min: minimum, max: maximum,
      data: [{ value, __lv_dataset: dataset?.id ?? 'primary', __lv_row_index: 0 }], pointer: { show: spec.presentation.showPointer },
      progress: { show: true, width: spec.presentation.progressWidth }, axisLine: { lineStyle: { color: colors } },
      detail: {
        show: showDetail,
        formatter: (raw: unknown) => truncateVisualizationLabel(
          formatDisplayField(envelope, spec.value, raw, context, displayUnit),
          labelPolicy.maxCharacters,
          context.locale,
        ),
        color: context.colors.foreground,
        fontSize: labelPolicy.density === 'dense' ? 14 : undefined,
      },
    }]
    if (spec.presentation.target !== undefined) {
      const targetLabel = `Target ${formatDisplayField(envelope, spec.value, spec.presentation.target, context, displayUnit)}`
      series.push({
        id: 'series:polar:gauge:target',
        name: 'Target',
        type: 'gauge',
        min: minimum,
        max: maximum,
        silent: true,
        axisLine: { show: false },
        axisTick: { show: false },
        splitLine: { show: false },
        axisLabel: { show: false },
        progress: { show: false },
        pointer: { show: true, length: '72%', width: 4, itemStyle: { color: context.colors.foreground } },
        anchor: { show: false },
        title: { show: showDetail, offsetCenter: [0, '64%'], color: context.colors.muted, fontFamily: context.fontFamily },
        detail: { show: false },
        data: [{
          value: spec.presentation.target,
          name: targetLabel,
          pointer: { show: true, length: '72%', width: 4, itemStyle: { color: context.colors.foreground } },
          title: { show: showDetail },
          detail: { show: false },
        }],
      })
    }
    return {
      series,
    }
  }
  const dataset = inlineDataset(envelope, spec.value.dataset)
  if (!dataset) return {}
  const categoryIndex = spec.category ? dataset.columns.indexOf(spec.category.field) : -1
  const valueIndex = dataset.columns.indexOf(spec.value.field)
  const seriesIndex = spec.series ? dataset.columns.indexOf(spec.series.field) : -1
  const categories = [...new Set(dataset.rows.map((row, index) => String(categoryIndex >= 0 ? row[categoryIndex] : index + 1)))]
  const seriesValues = [...new Set(dataset.rows.map((row) => String(seriesIndex >= 0 ? row[seriesIndex] : spec.title)))]
  const values = seriesValues.map((series) => ({
    name: series,
    value: categories.map((category) => dataset.rows.find((row, index) => String(seriesIndex >= 0 ? row[seriesIndex] : spec.title) === series && String(categoryIndex >= 0 ? row[categoryIndex] : index + 1) === category)?.[valueIndex] ?? null),
  }))
  const configuredMaximum = spec.presentation.maximum
  const observedMaximum = Math.max(0, ...values.flatMap((series) => series.value.flatMap((value) => {
    const approximate = approximateNumericValue(value)
    return approximate === undefined ? [] : [approximate]
  })))
  const sharedMaximum = configuredMaximum ?? niceRadarMaximum(observedMaximum)
  const maxima = categories.map(() => sharedMaximum)
  const labels = echartsLabelPolicy(
    envelope,
    spec.value.dataset,
    spec.presentation.labelPolicy,
    (params) => Array.isArray(params.value) ? params.value.map((value) => formatDisplayField(envelope, spec.value, value, context)).join(', ') : '',
    context,
  )
  return {
    dataset: undefined, legend: legend(spec.presentation.legend, context),
    radar: {
      indicator: categories.map((name, index) => ({ name, max: maxima[index], color: context.colors.muted })),
      axisLine: { lineStyle: { color: context.colors.grid } },
      splitLine: { lineStyle: { color: context.colors.grid } },
      splitArea: { areaStyle: { color: [context.colors.surface, context.colors.grid], opacity: 0.18 } },
    },
    series: [{ id: 'series:polar:radar', type: 'radar', data: values, areaStyle: spec.presentation.area ? {} : undefined, ...labels }],
  }
}

function niceRadarMaximum(value: number): number {
  if (value <= 0) return 1
  const magnitude = 10 ** Math.floor(Math.log10(value))
  const normalized = value / magnitude
  const factor = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10
  return factor * magnitude
}

function approximateNumericValue(value: unknown): number | undefined {
  if (typeof value === 'number') return Number.isFinite(value) ? value : undefined
  if (typeof value !== 'string' || !parseDecimal(value)) return undefined
  const approximate = Number(value)
  return Number.isFinite(approximate) ? approximate : undefined
}
