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
  const data = (dataset?.rows ?? []).map((row, rowIndex) => ({
    name: formatField(envelope, spec.x, row[categoryIndex], context),
    value: valueIndices.map((index) => row[index]),
    __lv_dataset: dataset?.id ?? spec.x.dataset,
    __lv_row_index: rowIndex,
  }))
  const gain = seriesColor('', spec.presentation.gainColor ?? 'success', context)
  const loss = seriesColor('', spec.presentation.lossColor ?? 'danger', context)
  const neutral = seriesColor('', 'neutral', context)
  const candleColor = (params: { value?: unknown }): string => {
    const values = Array.isArray(params.value) ? params.value : []
    const open = Number(values[0]), close = Number(values[1])
    if (!Number.isFinite(open) || !Number.isFinite(close) || open === close) return neutral
    return close > open ? gain : loss
  }
  return {
    ...axes,
    xAxis: { ...axes.xAxis, data: (dataset?.rows ?? []).map((row) => formatField(envelope, spec.x, row[categoryIndex], context)) },
    dataZoom,
    ...legendDecoration(spec.presentation.legend, context, false, spec.presentation, [{ value: spec.title, name: spec.title }]),
    series: [{
      id: 'series:primary:candlestick', type: 'candlestick', name: spec.title, data,
      itemStyle: { color: candleColor, color0: candleColor, borderColor: candleColor, borderColor0: candleColor },
      tooltip: { formatter: tooltipFormatterForRow(envelope, context, { fallbackRefs: [spec.x, ...spec.y] }) },
      ...labels,
    }],
  }
}
