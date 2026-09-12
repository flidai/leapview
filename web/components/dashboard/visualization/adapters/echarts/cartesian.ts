import type { VisualizationConditionalFormat, VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { axis, field, fieldLabel, formatDisplayField, formatField, inlineDataset, labelFormatter, legendDecoration, selectedDatasetSource, tooltipFormatterForRow, type EChartsTranslation } from './common'
import { conditionalCategoryColor, conditionalFormatHasColor, conditionalItemColor, heatmapDefaultColor, seriesColor } from './conditional-color'
import { constrainEChartsLabelToDataRect, echartsLabelPolicy } from './label-policy'
import { categoryIdentity, type CategoryColorRegistry } from './category-colors'
import { financialOption } from './financial'
import {
  cartesianCategoryLookup,
  cartesianIsHorizontal,
  cartesianSeriesType,
  comboAxisField,
  conditionalColorChain,
  orderedCartesianCategories,
  orderedY,
  resolveCartesianCategory,
  seriesID,
  stackingMode,
  type CartesianCategory,
  type CartesianSpec,
} from './series-intent'
import { categoricalFieldValues, configureSecondaryComboAxis, finiteFieldExtent as finiteFieldExtentHelper, heatmapDataZoom, hideCartesianAxes, humanizeCategoryLabel, multiMeasureComboAxes, rawCategoricalFieldValue } from './cartesian-presentation'
import { applyDecisionContext } from './decision-context'

export { applyDecisionContext }

export function cartesianOption(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): EChartsTranslation {
  const option = applyDecisionContext(envelope, context, cartesianBaseOption(envelope, context, categoryColors))
  const spec = envelope.spec as CartesianSpec
  return spec.presentation.axisVisible === false ? hideCartesianAxes(option) : option
}

function cartesianBaseOption(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): EChartsTranslation {
  const spec = envelope.spec as CartesianSpec
  const horizontal = cartesianIsHorizontal(spec)
  const primaryY = comboAxisField(spec, 'primary') ?? spec.y[0]!
  const xRef = horizontal ? primaryY : spec.x
  const xType = axisType(envelope, xRef, horizontal ? 'value' : 'category')
  const xAxis = axis(envelope, xRef, xType, context, horizontal ? 'primary_y' : 'x', horizontal ? spec.y : [spec.x])
  const yRef = horizontal ? spec.x : primaryY
  const yType = axisType(envelope, yRef, horizontal ? 'category' : 'value')
  const yAxis = axis(envelope, yRef, yType, context, horizontal ? 'x' : 'primary_y', horizontal ? [spec.x] : spec.y)
  const stack = stackingMode(spec), axes = { grid: cartesianGrid(spec), xAxis, yAxis }
  if (stack === 'percent') applyPercentAxis(horizontal ? xAxis : yAxis, context)
  const dataZoom = spec.presentation.dataZoom === true ? [{ type: 'inside' }, { type: 'slider', ...(spec.presentation.legend === 'bottom' ? { bottom: 28 } : {}) }] : undefined
  if (spec.mark === 'histogram') {
    const value = spec.y.find((item) => item.field === 'value') ?? spec.y.at(-1)
    return { ...axes, dataZoom, series: [{ id: seriesID(value?.dataset, value?.field), type: 'bar', encode: { x: spec.x.field, y: value?.field }, ...chartLabel(envelope, value, spec, context) }] }
  }
  if (spec.mark === 'waterfall') {
    // The generated shape is [start, metric], while older direct IR may use
    // [metric, start]. Bind the visible series to the first non-offset field
    // and retain a safe fallback for malformed/direct fixtures.
    const value = spec.y.find((item) => item.field !== 'start') ?? spec.y[0]
    const start = spec.y.find((item) => item.field === 'start') ?? spec.y[0]
    const markFill = value ? conditionalItemColor(envelope, value, 'mark_fill', context) : undefined, seriesFill = value ? conditionalItemColor(envelope, value, 'series_color', context) : undefined, signedFallback = signedWaterfallColor(envelope, value, context), fill = conditionalColorChain([markFill, seriesFill], signedFallback)
    return {
      ...axes, dataZoom,
      series: [
        { id: 'series:waterfall:offset', type: 'bar', stack: 'waterfall', silent: true, itemStyle: { color: 'transparent' }, encode: { x: spec.x.field, y: start?.field } },
        {
          id: seriesID(value?.dataset, value?.field), type: 'bar', stack: 'waterfall',
          encode: { x: spec.x.field, y: value?.field },
          itemStyle: { color: fill },
          tooltip: { formatter: tooltipFormatterForRow(envelope, context, { fallbackRefs: [spec.x, ...spec.y] }) },
          ...chartLabel(envelope, value, spec, context),
        },
      ],
    }
  }
  if (spec.mark === 'candlestick') {
    return financialOption(envelope, context, axes, dataZoom)!
  }
  if (spec.mark === 'boxplot') {
    const dataset = inlineDataset(envelope, spec.x.dataset)
    const categoryIndex = dataset?.columns.indexOf(spec.x.field) ?? -1
    const valueIndices = spec.y.map((item) => dataset?.columns.indexOf(item.field) ?? -1)
    const data = (dataset?.rows ?? []).flatMap((row, rowIndex) => {
      const rawValues = valueIndices.map((index) => row[index])
      const values = rawValues.map(Number)
      if (categoryIndex < 0 || valueIndices.some((index) => index < 0) || rawValues.some((value) => value === null || value === undefined || value === '') || values.some((value) => !Number.isFinite(value))) return []
      return [{ name: formatField(envelope, spec.x, row[categoryIndex], context), value: values, __lv_dataset: dataset?.id ?? spec.x.dataset, __lv_row_index: rowIndex }]
    })
    data.sort((left, right) => left.value[Math.floor(left.value.length / 2)]! - right.value[Math.floor(right.value.length / 2)]!)
    const primary = context.colors.data[0] ?? context.colors.accent
    const rotateLabels = data.length > 4
    const authoredRotation = spec.axes?.find((candidate) => candidate.id === 'x')?.labelRotation
    return {
      ...axes,
      grid: { ...axes.grid, bottom: dataZoom ? 76 : rotateLabels ? 44 : 20 },
      xAxis: { ...axes.xAxis, data: data.map((item) => item.name), axisLabel: { ...axes.xAxis.axisLabel, interval: 0, ...(authoredRotation && authoredRotation !== 'automatic' ? {} : { rotate: rotateLabels ? 24 : 0 }) } },
      dataZoom,
      graphic: data.length === 0 ? [{ type: 'text', left: 'center', top: 'middle', silent: true, style: { text: 'No complete distribution data', fill: context.colors.muted, fontFamily: context.fontFamily, textAlign: 'center' } }] : undefined,
      series: [{
        id: `series:primary:${spec.mark}`, type: spec.mark, name: spec.title,
        data,
        itemStyle: { color: colorWithAlpha(primary, 0.24), borderColor: primary, borderWidth: 2 },
        emphasis: { itemStyle: { color: colorWithAlpha(primary, 0.4) } },
      }],
    }
  }
  if (spec.mark === 'heatmap' && spec.y.length >= 2) {
    const value = spec.y[1]!, markFill = conditionalItemColor(envelope, value, 'mark_fill', context), seriesFill = conditionalItemColor(envelope, value, 'series_color', context), gradient = conditionalGradient(envelope, value, 'mark_fill')
    const extent = finiteFieldExtentHelper(envelope, value), primary = context.colors.data[0] ?? context.colors.accent
    const cue = conditionalCueFormat(envelope, value), authoredColor = conditionalFormatHasColor(envelope, value, 'mark_fill') || conditionalFormatHasColor(envelope, value, 'series_color'), fallback = heatmapDefaultColor(envelope, value, extent, primary, context), fill = markFill || seriesFill ? conditionalColorChain([markFill, seriesFill], fallback) : undefined
    const labels = chartLabel(envelope, value, spec, context)
    const heatmapZoom = heatmapDataZoom(envelope, spec)
    const heatmapXAxis = axis(envelope, spec.x, axisType(envelope, spec.x, 'category'), context, 'x')
    const heatmapYAxis = axis(envelope, spec.y[0]!, axisType(envelope, spec.y[0]!, 'category'), context, 'primary_y')
    const formatVisualMapValue = (rawValue: unknown): string => formatDisplayField(envelope, value, rawValue, context)
    const heatmapXCategories = categoricalFieldValues(envelope, spec.x)
    const heatmapYCategories = categoricalFieldValues(envelope, spec.y[0]!)
    // ECharts treats numeric values in explicit ordinal axis data as indexes
    // rather than category values. Keep native ordinal collection for typed
    // and null categories so source values retain their governed identity.
    if (heatmapXCategories.every((category) => typeof category === 'string')) heatmapXAxis.data = heatmapXCategories
    if (heatmapYCategories.every((category) => typeof category === 'string')) heatmapYAxis.data = heatmapYCategories
    if (heatmapZoom && heatmapZoom.length > 0) {
      const authoredRotation = spec.axes?.find((candidate) => candidate.id === 'x')?.labelRotation
      heatmapXAxis.axisLabel = {
        ...heatmapXAxis.axisLabel,
        interval: 0,
        ...(authoredRotation && authoredRotation !== 'automatic' ? {} : { rotate: 24 }),
        hideOverlap: true,
        width: 88,
        overflow: 'truncate',
        ellipsis: '…',
        formatter: (category: unknown) => humanizeCategoryLabel(formatField(
          envelope,
          spec.x,
          category === '' && heatmapXCategories.some((candidate) => candidate === null || candidate === undefined)
            ? null
            : rawCategoricalFieldValue(envelope, spec.x, category),
          context,
        )),
      }
    }
    return {
      grid: { ...cartesianGrid(spec), bottom: heatmapZoom && heatmapZoom.length > 0 ? 96 : 64 },
      xAxis: heatmapXAxis, yAxis: heatmapYAxis, dataZoom: heatmapZoom,
      visualMap: gradient
        ? {
            type: 'continuous', dimension: value.field,
            min: gradient.minimum, max: gradient.maximum, calculable: true, orient: 'horizontal', left: 'center', bottom: 0,
            inRange: { color: [seriesColor('', gradient.low.color, context), seriesColor('', gradient.high.color, context)] },
            // Keep nulls visible so the conditional formatter can apply the
            // authored nullStyle instead of visualMap hiding them.
            outOfRange: { opacity: 1 },
            formatter: formatVisualMapValue,
            textStyle: { color: context.colors.muted },
          }
        : authoredColor ? undefined : {
            type: 'continuous', dimension: value.field,
            min: extent.minimum, max: extent.maximum, calculable: true, orient: 'horizontal', left: 'center', bottom: 0,
            inRange: { color: [colorWithAlpha(primary, 0.18), primary] },
            outOfRange: { opacity: cue ? 1 : 0 },
            formatter: formatVisualMapValue,
            textStyle: { color: context.colors.muted },
          },
      series: [{
        id: 'series:primary:heatmap', type: 'heatmap',
        encode: { x: spec.x.field, y: spec.y[0]?.field, value: value.field },
        itemStyle: { color: fill },
        ...labels,
        labelLayout: constrainEChartsLabelToDataRect(labels.labelLayout, spec.presentation.labelPolicy.minimumSpacing),
      }],
    }
  }
  const split = splitCartesianSeries(envelope, context, categoryColors)
  if (split) {
    const secondary = split.series.some((item) => (horizontal ? item.xAxisIndex : item.yAxisIndex) === 1)
    const primaryY = comboAxisField(spec, 'primary') ?? spec.y[0]!
    const primaryAxis = axis(envelope, primaryY, axisType(envelope, primaryY, 'value'), context, 'primary_y', spec.y)
    if (stackingMode(spec) === 'percent') applyPercentAxis(primaryAxis, context)
    if (split.scrollLegend) {
      // Crowded category-series cards surrender vertical space to a paged
      // legend. Keep value ticks legible in the remaining plot instead of
      // allowing ECharts to stack a dense automatic scale.
      primaryAxis.splitNumber = 4
      primaryAxis.axisLabel = { ...primaryAxis.axisLabel, hideOverlap: true }
    }
    const secondaryY = comboAxisField(spec, 'secondary') ?? spec.y[0]!
    const secondaryAxis = secondary ? axis(envelope, secondaryY, axisType(envelope, secondaryY, 'value'), context, 'secondary_y', spec.y) : undefined
    if (secondaryAxis) configureSecondaryComboAxis(secondaryAxis)
    return {
      dataset: split.datasets, grid: cartesianGrid(spec), ...legendDecoration(spec.presentation.legend, context, split.scrollLegend, spec.presentation, split.series.map((item) => ({ value: String(item.name), name: String(item.name) }))), xAxis: split.categoryAxis,
      yAxis: horizontal ? split.categoryAxis : secondary ? [primaryAxis, secondaryAxis!] : primaryAxis,
      ...(horizontal ? { xAxis: secondary ? [primaryAxis, secondaryAxis!] : primaryAxis } : {}),
      dataZoom, series: [...split.categoryDomainSeries, ...split.series, ...interactionHitSeries(envelope, spec, split.series)],
    }
  }
  const values = orderedY(spec)
  const normalized = stack === 'percent' ? normalizedMeasureDataset(envelope, spec, values) : undefined
  // A canonical combo visual binds each authored series entry to a result
  // field. Category-series combos are handled above by splitCartesianSeries;
  // this lookup is deliberately keyed by measure field for multi-measure
  // combos so mark and axis policies are not silently dropped.
  const comboByField = spec.mark === 'combo'
    ? new Map((spec.presentation.comboSeries ?? []).map((item) => [String(item.seriesValue), item]))
    : new Map<string, NonNullable<CartesianSpec['presentation']['comboSeries']>[number]>()
  const comboColorSlots = spec.mark === 'combo'
    ? new Map((spec.presentation.comboSeries ?? []).map((item, index) => [String(item.seriesValue), index]))
    : new Map<string, number>()
  const series = values.map((value, seriesIndex) => {
    const normalizedField = normalized?.dimensions.get(value.field)
    const combo = comboByField.get(value.field)
    const mark = combo?.mark ?? (spec.mark === 'combo' ? 'line' : spec.mark)
    const markFill = conditionalItemColor(envelope, value, 'mark_fill', context), seriesFill = conditionalItemColor(envelope, value, 'series_color', context)
    const intent = spec.presentation.seriesIntent?.find((candidate) => candidate.value === value.field)?.color
    const paletteIndex = comboColorSlots.get(value.field) ?? spec.y.findIndex((candidate) => candidate.dataset === value.dataset && candidate.field === value.field)
    const paletteColor = context.colors.data[(paletteIndex < 0 ? seriesIndex : paletteIndex) % context.colors.data.length] ?? context.colors.accent
    const fallbackColor = intent === undefined ? paletteColor : seriesColor(value.field, intent, context), markColor = conditionalColorChain([markFill, seriesFill], fallbackColor)
    const translatedLabel = normalizedField
      ? percentLabel(envelope, value, spec, context, normalized?.columnIndices.get(value.field))
      : chartLabel(envelope, value, spec, context, combo?.axis === 'secondary' ? 'secondary_y' : 'primary_y', markColor)
    return {
      id: seriesID(value.dataset, value.field), type: cartesianSeriesType(mark), name: fieldLabel(envelope, value),
      ...(horizontal ? { xAxisIndex: combo?.axis === 'secondary' ? 1 : 0 } : { yAxisIndex: combo?.axis === 'secondary' ? 1 : 0 }),
      encode: horizontal ? { x: normalizedField ?? value.field, y: spec.x.field } : { x: spec.x.field, y: normalizedField ?? value.field },
      smooth: spec.presentation.smooth, symbol: spec.presentation.showSymbols ? undefined : 'none', symbolSize: spec.presentation.symbolSize,
      stack: stack === 'none' ? undefined : stack, areaStyle: spec.presentation.area || mark === 'area' ? {} : undefined,
      itemStyle: {
        color: markColor,
      },
      barMinHeight: horizontal && translatedLabel.label.show !== false && translatedLabel.label.position === 'insideRight' ? 44 : undefined,
      step: spec.presentation.step ? 'middle' : false,
      ...translatedLabel,
    }
  })
  const comboAxes = spec.mark === 'combo'
    ? multiMeasureComboAxes(envelope, context, spec, horizontal, values, comboByField, stack, (ref, fallback) => axisType(envelope, ref, fallback))
    : undefined
  if (comboAxes) {
    const categoryAxis = axis(envelope, spec.x, axisType(envelope, spec.x, 'category'), context, 'x')
    if (horizontal) comboAxes.yAxis = categoryAxis
    else comboAxes.xAxis = categoryAxis
  }
  return {
    ...axes,
    ...(comboAxes ? { xAxis: comboAxes.xAxis, yAxis: comboAxes.yAxis } : {}),
    ...(normalized ? { dataset: { id: `dataset:${normalized.datasetID}`, source: normalized.source } } : {}),
    ...legendDecoration(spec.presentation.legend, context, false, spec.presentation, values.map((value, index) => ({ value: value.field, name: String(series[index]?.name ?? value.field) }))), dataZoom,
    series: [...series, ...interactionHitSeries(envelope, spec, series)],
  }
}

function signedWaterfallColor(envelope: VisualizationEnvelope, ref: VisualizationFieldRef | undefined, context: RendererContext) {
  const dataset = ref ? inlineDataset(envelope, ref.dataset) : undefined
  const index = ref && dataset ? dataset.columns.indexOf(ref.field) : -1
  return (params: { value?: unknown }) => {
    const value = index >= 0 && Array.isArray(params.value) ? Number(params.value[index]) : 0
    if (value < 0) return context.colors.danger
    if (value > 0) return context.colors.success
    return context.colors.accent
  }
}

function colorWithAlpha(color: string, alpha: number): string {
  const longHex = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(color)
  if (longHex) return `rgba(${Number.parseInt(longHex[1]!, 16)}, ${Number.parseInt(longHex[2]!, 16)}, ${Number.parseInt(longHex[3]!, 16)}, ${alpha})`
  const shortHex = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/i.exec(color)
  if (shortHex) return `rgba(${Number.parseInt(shortHex[1]! + shortHex[1]!, 16)}, ${Number.parseInt(shortHex[2]! + shortHex[2]!, 16)}, ${Number.parseInt(shortHex[3]! + shortHex[3]!, 16)}, ${alpha})`
  return color
}

function interactionHitSeries(envelope: VisualizationEnvelope, spec: CartesianSpec, series: EChartsTranslation[]): EChartsTranslation[] {
  if (!spec.interactions.some((interaction) => interaction.kind === 'select')) return []
  return series.flatMap((candidate, index) => {
    if (candidate.type !== 'line') return []
    const yField = typeof candidate.encode?.y === 'string' ? candidate.encode.y : spec.y[index]?.field ?? `value-${index}`
    const identity = candidate.datasetId
      ? `${spec.x.dataset}:${spec.x.field}:${encodeURIComponent(String(candidate.datasetId))}`
      : `${spec.x.dataset}:${spec.x.field}:${yField}`
    return [{
      id: `series:interaction-hit:${identity}`,
      type: 'scatter',
      ...(candidate.datasetId ? { datasetId: candidate.datasetId } : {}),
      encode: candidate.encode,
      ...(candidate.xAxisIndex !== undefined ? { xAxisIndex: candidate.xAxisIndex } : {}),
      ...(candidate.yAxisIndex !== undefined ? { yAxisIndex: candidate.yAxisIndex } : {}),
      symbolSize: Math.max(18, spec.presentation.symbolSize ?? 0),
      itemStyle: { color: 'rgba(0,0,0,0.001)' },
      emphasis: { disabled: true },
      tooltip: { show: false },
      silent: false,
      z: 10,
    }]
  })
}

function chartLabel(envelope: VisualizationEnvelope, value: CartesianSpec['y'][number] | undefined, spec: CartesianSpec, context: RendererContext, axisID: 'primary_y' | 'secondary_y' = 'primary_y', _insideFill?: unknown) {
  const authored = spec.presentation.labelPosition
  const horizontal = cartesianIsHorizontal(spec)
  const automatic = authored === undefined || authored === 'automatic'
  const position = automatic ? horizontal ? 'insideRight' : undefined : authored === 'outside' ? horizontal ? 'right' : 'top' : authored
  const baseFormatter = labelFormatter(envelope, value, context, axisID, value ? [value] : [])
  const rowCount = inlineDataset(envelope, value?.dataset ?? spec.x.dataset)?.rows.length ?? 0
  const color = value ? conditionalItemColor(envelope, value, 'label_foreground', context) : undefined, labelColorAuthored = value ? conditionalFormatHasColor(envelope, value, 'label_foreground') : false
  const translated = echartsLabelPolicy(envelope, value?.dataset ?? spec.x.dataset, spec.presentation.labelPolicy, baseFormatter, context)
  translated.label.position = position
  const insideMark = authored !== 'outside' && ['bar', 'column', 'waterfall', 'histogram'].includes(spec.mark)
  if (color) translated.label.color = insideMark ? (params: { value?: unknown }) => color(params) ?? '#fff' : color
  if (insideMark && !labelColorAuthored) {
    if (!color) translated.label.color = '#fff'
    Object.assign(translated.label, context.theme === 'light'
      ? { textBorderColor: 'rgba(0, 0, 0, 0.55)', textBorderWidth: 2 }
      : { textBorderColor: 'rgba(255, 255, 255, 0.45)', textBorderWidth: 1 })
  }
  if (spec.presentation.labelPolicy.density === 'always' && ['bar', 'column', 'waterfall', 'histogram', 'heatmap'].includes(spec.mark)) {
    translated.labelLayout = { hideOverlap: true }
  }
  if (rowCount > 24 && spec.presentation.labelPolicy.density === 'automatic' && ['bar', 'column'].includes(spec.mark)) translated.label.show = false
  if (spec.mark === 'bar' && horizontal && automatic) {
    translated.labelLayout = constrainEChartsLabelToDataRect(translated.labelLayout, spec.presentation.labelPolicy.minimumSpacing)
  }
  return translated
}

function cartesianGrid(spec: CartesianSpec): EChartsTranslation {
  const bottomLegend = spec.presentation.legend === 'bottom'
  const titleInset = spec.presentation.legendTitle === undefined ? 0 : 24
  const sideInset = spec.presentation.legendTitle === undefined ? 0 : 72
  const titlelessHorizontalBar = cartesianIsHorizontal(spec)
    && spec.mark === 'bar'
    && !(spec.axes ?? []).some((candidate) => candidate.title || candidate.unit)
  return {
    left: 12 + (spec.presentation.legend === 'left' ? sideInset : 0),
    right: 28 + (spec.presentation.legend === 'right' ? sideInset : 0),
    top: (spec.presentation.legend === 'top' ? 44 : 16) + (spec.presentation.legend === 'top' ? titleInset : 0),
    bottom: 16 + (bottomLegend ? 28 : 0) + (spec.presentation.dataZoom === true ? 42 : 0) + (bottomLegend ? titleInset : 0),
    containLabel: !titlelessHorizontalBar,
    ...(titlelessHorizontalBar ? { outerBoundsMode: 'same', outerBoundsContain: 'all' } : {}),
  }
}

function splitCartesianSeries(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): { datasets: EChartsTranslation[]; series: EChartsTranslation[]; scrollLegend: boolean; categoryAxis: EChartsTranslation; categoryDomainSeries: EChartsTranslation[] } | undefined {
  const spec = envelope.spec
  if (spec.kind !== 'cartesian' || !spec.series || spec.y.length !== 1 || envelope.dataState.kind !== 'inline') return undefined
  const horizontal = cartesianIsHorizontal(spec)
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === spec.series?.dataset)
  const seriesIndex = dataset?.columns.indexOf(spec.series.field) ?? -1
  if (!dataset || seriesIndex < 0) return undefined
  const available = orderedCartesianCategories(dataset.rows.map((row) => row[seriesIndex])), categoryLookup = cartesianCategoryLookup(available)
  categoryColors.register(envelope, spec.series, available.map((category) => category.value))
  const configured = spec.presentation.comboSeries ?? []
  const intents = spec.presentation.seriesIntent ?? []
  const orderedKeys = new Set<string>()
  const values: CartesianCategory[] = []
  const appendConfigured = (rawValue: unknown) => {
    const category = resolveCartesianCategory(categoryLookup, rawValue)
    if (!category || orderedKeys.has(category.key)) return
    orderedKeys.add(category.key)
    values.push(category)
  }
  for (const intent of [...intents]
    .filter((item) => item.order !== undefined)
    .sort((left, right) => left.order! - right.order! || left.value.localeCompare(right.value, 'en'))) appendConfigured(intent.value)
  for (const intent of intents.filter((item) => item.order === undefined)) appendConfigured(intent.value)
  for (const item of configured) appendConfigured(item.seriesValue)
  for (const category of available) appendConfigured(category.value)
  const datasets: EChartsTranslation[] = [{ id: `dataset:${dataset.id}`, source: selectedDatasetSource(envelope, dataset) }]
  const stack = stackingMode(spec)
  const normalizedSources = stack === 'percent' ? normalizedSeriesSources(envelope, dataset, spec, values) : undefined
  const series: EChartsTranslation[] = values.map((category) => {
    const token = encodeURIComponent(category.key)
    const datasetID = `dataset:series:${spec.series?.field}:${token}`
    const normalized = normalizedSources?.get(category.key)
    datasets.push(normalized
      ? { id: datasetID, source: normalized.source }
      : { id: datasetID, fromDatasetId: `dataset:${dataset.id}`, transform: { type: 'filter', config: { dimension: spec.series?.field, '=': category.value } } })
    const combo = configured.find((item) => resolveCartesianCategory(categoryLookup, item.seriesValue)?.key === category.key)
    const intent = intents.find((item) => resolveCartesianCategory(categoryLookup, item.value)?.key === category.key)
    const mark = combo?.mark ?? (spec.mark === 'combo' ? 'line' : spec.mark)
    const valueRef = spec.y[0]!
    const markFill = conditionalItemColor(envelope, valueRef, 'mark_fill', context), seriesFill = conditionalItemColor(envelope, valueRef, 'series_color', context)
    const governedSeriesColor = conditionalCategoryColor(envelope, valueRef, spec.series!, category.value, 'mark_fill', context)
      ?? conditionalCategoryColor(envelope, valueRef, spec.series!, category.value, 'series_color', context)
    const paletteColor = categoryColors.color(envelope, spec.series!, category.value, context)
    const intentColor = intent?.color ? seriesColor(category.key, intent.color, context) : paletteColor
    const fill = conditionalColorChain([markFill, seriesFill], intentColor)
    const markColor = governedSeriesColor ?? fill
    const sourceRowIndices = dataset.rows.flatMap((row, rowIndex) => categoryIdentity(row[seriesIndex]) === category.key ? [rowIndex] : [])
    return {
      id: `series:${spec.series?.dataset}:${spec.series?.field}:${token}`, datasetId: datasetID, name: category.name, type: cartesianSeriesType(mark),
      __lv_source_row_indices: sourceRowIndices,
      ...(horizontal ? { xAxisIndex: combo?.axis === 'secondary' ? 1 : 0 } : { yAxisIndex: combo?.axis === 'secondary' ? 1 : 0 }),
      encode: horizontal ? { x: normalized?.dimension ?? spec.y[0]?.field, y: spec.x.field } : { x: spec.x.field, y: normalized?.dimension ?? spec.y[0]?.field }, smooth: spec.presentation.smooth, symbol: spec.presentation.showSymbols ? undefined : 'none', symbolSize: spec.presentation.symbolSize,
      stack: stack === 'none' ? undefined : stack, areaStyle: spec.presentation.area || mark === 'area' ? {} : undefined,
      itemStyle: {
        color: markColor,
      },
      step: spec.presentation.step ? 'middle' : false,
      ...(normalized
        ? percentLabel(envelope, spec.y[0], spec, context, normalized.columnIndex)
        : chartLabel(envelope, spec.y[0], spec, context, combo?.axis === 'secondary' ? 'secondary_y' : 'primary_y', markColor)),
    }
  })
  const crowded = spec.presentation.labelPolicy.density === 'automatic'
    && spec.presentation.labelPolicy.tooltipFallback
    && (values.length > 4 || dataset.rows.length > 24)
  if (crowded) {
    for (const item of series) {
      item.label = { ...item.label, show: false }
      item.labelLayout = { hideOverlap: true }
    }
  }

  // Split-series datasets arrive in governed seriesIntent order. Without a
  // category-domain seed ECharts collects categories in that arrival order,
  // which can make an authoritative query order appear to backtrack on the
  // category axis. Seed native ordinal metadata with raw first-seen values;
  // this keeps number/string/null identity, tooltips, references, and row
  // interactions on the original dataset.
  const categoryAxis = axis(envelope, spec.x, axisType(envelope, spec.x, 'category'), context, 'x')
  const categoryDomain = sourceCategoryDomain(dataset, spec.x.field)
  const categoryDomainSeries = categoryAxis.type === 'category' && categoryDomain.length > 0
    ? [{
        id: `series:category-domain:${spec.x.dataset}:${spec.x.field}`,
        type: 'line',
        data: categoryDomain.map((value) => horizontal ? [Number.NaN, value] : [value, Number.NaN]),
        encode: { x: 0, y: 1 },
        __lv_source_row_indices: [],
        silent: true,
        animation: false,
        showSymbol: false,
        symbol: 'none',
        lineStyle: { opacity: 0 },
        label: { show: false },
        tooltip: { show: false },
        emphasis: { disabled: true },
        legendHoverLink: false,
      } satisfies EChartsTranslation]
    : []
  return { datasets, series, scrollLegend: values.length > 4, categoryAxis, categoryDomainSeries }
}

function sourceCategoryDomain(dataset: NonNullable<ReturnType<typeof inlineDataset>>, fieldID: string): unknown[] {
  const index = dataset.columns.indexOf(fieldID)
  if (index < 0) return []
  const values = dataset.rows.map((row) => row[index])
  const seen = new Set<string>()
  const domain: unknown[] = []
  for (const value of values) {
    const identity = categoryIdentity(value)
    if (seen.has(identity)) continue
    seen.add(identity)
    domain.push(value)
  }
  return domain
}

function normalizedSeriesSources(
  envelope: VisualizationEnvelope,
  dataset: NonNullable<ReturnType<typeof inlineDataset>>,
  spec: CartesianSpec,
  values: readonly CartesianCategory[],
): Map<string, { source: unknown[][]; dimension: string; columnIndex: number }> {
  const source = selectedDatasetSource(envelope, dataset)
  const columns = source[0] as string[]
  const xIndex = columns.indexOf(spec.x.field)
  const seriesIndex = columns.indexOf(spec.series!.field)
  const valueIndex = columns.indexOf(spec.y[0]!.field)
  const totals = new Map<string, { positive: number; negative: number }>()
  const key = categoryIdentity
  for (const row of source.slice(1)) {
    const amount = row[valueIndex]
    if (typeof amount !== 'number' || !Number.isFinite(amount)) continue
    const category = key(row[xIndex])
    const total = totals.get(category) ?? { positive: 0, negative: 0 }
    if (amount >= 0) total.positive += amount
    else total.negative += Math.abs(amount)
    totals.set(category, total)
  }
  const dimension = uniqueDimension('__lv_percent_value', new Set(columns))
  const result = new Map<string, { source: unknown[][]; dimension: string; columnIndex: number }>()
  for (const category of values) {
    const rows = source.slice(1).filter((row) => Object.is(row[seriesIndex], category.value)).map((row) => {
      const amount = row[valueIndex]
      const total = totals.get(key(row[xIndex]))
      const denominator = typeof amount === 'number' && amount < 0 ? total?.negative : total?.positive
      const normalized = typeof amount === 'number' && Number.isFinite(amount) && denominator ? amount / denominator * 100 : null
      return [...row, normalized]
    })
    result.set(category.key, {
      source: [[...columns, dimension], ...rows],
      dimension,
      columnIndex: columns.length,
    })
  }
  return result
}

function normalizedMeasureDataset(
  envelope: VisualizationEnvelope,
  spec: CartesianSpec,
  values: CartesianSpec['y'],
): { datasetID: string; source: unknown[][]; dimensions: Map<string, string>; columnIndices: Map<string, number> } | undefined {
  const dataset = inlineDataset(envelope, spec.x.dataset)
  if (!dataset || values.some((value) => value.dataset !== dataset.id)) return undefined
  const source = selectedDatasetSource(envelope, dataset)
  const columns = source[0] as string[]
  const indices = values.map((value) => columns.indexOf(value.field))
  if (indices.some((index) => index < 0)) return undefined
  const dimensions = new Map<string, string>()
  const columnIndices = new Map<string, number>()
  const reserved = new Set(columns)
  for (const value of values) {
    const base = `__lv_percent_${value.field.replace(/[^a-zA-Z0-9_]/g, '_')}`
    const dimension = uniqueDimension(base, reserved)
    reserved.add(dimension)
    dimensions.set(value.field, dimension)
    columnIndices.set(value.field, columns.length + columnIndices.size)
  }
  const rows = source.slice(1).map((row) => {
    let positive = 0
    let negative = 0
    for (const index of indices) {
      const amount = row[index]
      if (typeof amount !== 'number' || !Number.isFinite(amount)) continue
      if (amount >= 0) positive += amount
      else negative += Math.abs(amount)
    }
    const normalized = indices.map((index) => {
      const amount = row[index]
      if (typeof amount !== 'number' || !Number.isFinite(amount)) return null
      const denominator = amount < 0 ? negative : positive
      return denominator ? amount / denominator * 100 : null
    })
    return [...row, ...normalized]
  })
  return {
    datasetID: dataset.id,
    source: [[...columns, ...values.map((value) => dimensions.get(value.field)!)], ...rows],
    dimensions,
    columnIndices,
  }
}

function uniqueDimension(base: string, reserved: Set<string>): string {
  if (!reserved.has(base)) return base
  let suffix = 2
  while (reserved.has(`${base}_${suffix}`)) suffix++
  return `${base}_${suffix}`
}

function applyPercentAxis(axisOption: EChartsTranslation, context: RendererContext): void {
  const formatter = new Intl.NumberFormat(context.locale, { maximumFractionDigits: 1 })
  axisOption.axisLabel = { ...axisOption.axisLabel, formatter: (value: unknown) => typeof value === 'number' ? `${formatter.format(value)}%` : String(value) }
}

function percentLabel(
  envelope: VisualizationEnvelope,
  value: CartesianSpec['y'][number] | undefined,
  spec: CartesianSpec,
  context: RendererContext,
  columnIndex = -1,
) {
  const formatter = new Intl.NumberFormat(context.locale, { maximumFractionDigits: 1 })
  const baseFormatter = (params: { value?: unknown }) => {
    const normalized = Array.isArray(params.value) ? params.value.at(columnIndex) : params.value
    return typeof normalized === 'number' ? `${formatter.format(normalized)}%` : ''
  }
  const color = value ? conditionalItemColor(envelope, value, 'label_foreground', context) : undefined
  const translated = echartsLabelPolicy(
    envelope,
    spec.x.dataset,
    spec.presentation.labelPolicy,
    baseFormatter,
    context,
  )
  if (color) translated.label.color = color
  return translated
}

function conditionalGradient(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  target: VisualizationConditionalFormat['target'],
) {
  const format = envelope.spec.conditionalFormatting?.find((candidate) =>
    candidate.target === target && candidate.field.dataset === ref.dataset && candidate.field.field === ref.field)
  return format?.rule.kind === 'gradient' ? format.rule : undefined
}

function conditionalCueFormat(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): VisualizationConditionalFormat | undefined {
  const formats = envelope.spec.conditionalFormatting ?? []
  for (const target of ['icon', 'label_foreground', 'mark_fill', 'series_color'] as const) {
    const format = formats.find((candidate) =>
      candidate.target === target
      && candidate.field.dataset === ref.dataset
      && candidate.field.field === ref.field
      && conditionalRuleHasIcon(candidate))
    if (format) return format
  }
  return undefined
}

function conditionalRuleHasIcon(format: VisualizationConditionalFormat): boolean {
  const rule = format.rule
  if (rule.nullStyle.icon) return true
  if (rule.kind === 'gradient') return Boolean(rule.low.icon || rule.high.icon)
  if (rule.defaultStyle.icon) return true
  if (rule.kind === 'rules') return rule.rules.some((candidate) => Boolean(candidate.style.icon))
  return Object.values(rule.values).some((style) => Boolean(style.icon))
}


function axisType(envelope: VisualizationEnvelope, ref: CartesianSpec['x'], fallback: 'category' | 'value'): 'category' | 'value' | 'time' {
  const dataType = field(envelope, ref)?.dataType
  return dataType === 'temporal' || dataType === 'date' ? 'time' : fallback
}
