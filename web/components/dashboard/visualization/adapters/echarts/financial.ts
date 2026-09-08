import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { formatField, inlineDataset, legendDecoration, tooltipFormatterForRow, type EChartsTranslation } from './common'
import { seriesColor } from './conditional-color'

/** Translate the bounded financial mark family without changing source rows. */
export function financialOption(
  envelope: VisualizationEnvelope,
  context: RendererContext,
  axes: EChartsTranslation,
  dataZoom: EChartsTranslation[] | undefined,
  labels: EChartsTranslation,
): EChartsTranslation | undefined {
  const spec = envelope.spec
  if (spec.kind !== 'cartesian' || spec.mark !== 'candlestick') return undefined
  const dataset = inlineDataset(envelope, spec.x.dataset)
  const categoryIndex = dataset?.columns.indexOf(spec.x.field) ?? -1
  const valueIndices = spec.y.map((item) => dataset?.columns.indexOf(item.field) ?? -1)
  const gain = seriesColor('', spec.presentation.gainColor ?? 'success', context)
  const loss = seriesColor('', spec.presentation.lossColor ?? 'danger', context)
  const neutral = seriesColor('', 'neutral', context)
  const data = (dataset?.rows ?? []).map((row, rowIndex) => {
    const value = valueIndices.map((index) => row[index])
    const item = {
      name: formatField(envelope, spec.x, row[categoryIndex], context),
      value,
      __lv_dataset: dataset?.id ?? spec.x.dataset,
      __lv_row_index: rowIndex,
    }
    // ECharts switches to its large candlestick path at 600 rows, where
    // per-item styles are intentionally skipped. Keep the equal-value color
    // truthful in that path with the series-level doji border fallback too.
    const openValue = value[0]
    const closeValue = value[1]
    const numericOpen = Number(openValue)
    const numericClose = Number(closeValue)
    const isEqual = openValue !== null && openValue !== undefined && openValue !== '' &&
      closeValue !== null && closeValue !== undefined && closeValue !== '' &&
      Number.isFinite(numericOpen) && Number.isFinite(numericClose) && numericOpen === numericClose
    return isEqual
      ? { ...item, itemStyle: { color: neutral, color0: neutral, borderColor: neutral, borderColor0: neutral } }
      : item
  })
  return {
    ...axes,
    xAxis: { ...axes.xAxis, data: (dataset?.rows ?? []).map((row) => formatField(envelope, spec.x, row[categoryIndex], context)) },
    dataZoom,
    ...legendDecoration(spec.presentation.legend, context, false, spec.presentation, [{ value: spec.title, name: spec.title }]),
    series: [{
      id: 'series:primary:candlestick', type: 'candlestick', name: spec.title, data,
      itemStyle: { color: gain, color0: loss, borderColor: gain, borderColor0: loss, borderColorDoji: neutral },
      tooltip: { formatter: tooltipFormatterForRow(envelope, context, { fallbackRefs: [spec.x, ...spec.y] }) },
      ...labels,
    }],
  }
}
