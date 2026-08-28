import type { VisualizationColorIntent, VisualizationConditionalFormat, VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { conditionalIconGlyph, conditionalStyleColor, resolveConditionalFormat } from '../../conditional-format'
import { resolveVisualizationMetadata } from '../../metadata'
import { axis, escapeHTML, field, fieldLabel, formatDisplayField, formatField, inlineDataset, labelFormatter, legend, selectedDatasetSource, toneColor, type EChartsTranslation } from './common'
import { echartsLabelPolicy } from './label-policy'
import type { CategoryColorRegistry } from './category-colors'

type CartesianSpec = Extract<VisualizationEnvelope['spec'], { kind: 'cartesian' }>
type ReferenceValue = NonNullable<CartesianSpec['referenceLines']>[number]['value']

export function cartesianOption(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): EChartsTranslation {
  return applyDecisionContext(envelope, context, cartesianBaseOption(envelope, context, categoryColors))
}

function cartesianBaseOption(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): EChartsTranslation {
  const spec = envelope.spec as CartesianSpec
  const horizontal = spec.presentation.orientation === 'horizontal' || spec.mark === 'bar'
  const xType = axisType(envelope, spec.x, horizontal ? 'value' : 'category')
  const xAxis = axis(envelope, horizontal ? spec.y[0]! : spec.x, xType, context, horizontal ? 'primary_y' : 'x', horizontal ? spec.y : [spec.x])
  const yAxis = axis(envelope, horizontal ? spec.x : spec.y[0]!, horizontal ? 'category' : 'value', context, horizontal ? 'x' : 'primary_y', horizontal ? [spec.x] : spec.y)
  const stack = stackingMode(spec)
  if (stack === 'percent') applyPercentAxis(horizontal ? xAxis : yAxis, context)
  const axes = { grid: cartesianGrid(spec), xAxis, yAxis }
  const dataZoom = spec.presentation.dataZoom ? [{ type: 'inside' }, { type: 'slider', ...(spec.presentation.legend === 'bottom' ? { bottom: 28 } : {}) }] : undefined
  if (spec.mark === 'histogram') {
    const value = spec.y.find((item) => item.field === 'value') ?? spec.y.at(-1)
    return { ...axes, dataZoom, series: [{ id: seriesID(value?.dataset, value?.field), type: 'bar', encode: { x: spec.x.field, y: value?.field }, ...chartLabel(envelope, value, spec, context) }] }
  }
  if (spec.mark === 'waterfall') {
    const start = spec.y.find((item) => item.field === 'start')
    const value = spec.y.find((item) => item.field === 'value') ?? spec.y[0]
    const fill = value
      ? conditionalItemColor(envelope, value, 'mark_fill', context) ?? conditionalItemColor(envelope, value, 'series_color', context)
      : undefined
    const stroke = value ? conditionalItemColor(envelope, value, 'mark_stroke', context) : undefined
    return {
      ...axes, dataZoom,
      series: [
        { id: 'series:waterfall:offset', type: 'bar', stack: 'waterfall', silent: true, itemStyle: { color: 'transparent' }, encode: { x: spec.x.field, y: start?.field } },
        {
          id: seriesID(value?.dataset, value?.field), type: 'bar', stack: 'waterfall',
          encode: { x: spec.x.field, y: value?.field },
          itemStyle: { color: fill ?? signedWaterfallColor(envelope, value, context), borderColor: stroke, borderWidth: stroke ? 2 : undefined },
          ...chartLabel(envelope, value, spec, context),
        },
      ],
    }
  }
  if (spec.mark === 'candlestick') {
    const dataset = inlineDataset(envelope, spec.x.dataset)
    const categoryIndex = dataset?.columns.indexOf(spec.x.field) ?? -1
    const valueIndices = spec.y.map((item) => dataset?.columns.indexOf(item.field) ?? -1)
    const data = (dataset?.rows ?? []).map((row, rowIndex) => ({
      name: String(row[categoryIndex]),
      value: valueIndices.map((index) => row[index]),
      __lv_dataset: dataset?.id ?? spec.x.dataset,
      __lv_row_index: rowIndex,
    }))
    return {
      ...axes, xAxis: { ...axes.xAxis, data: (dataset?.rows ?? []).map((row) => String(row[categoryIndex])) }, dataZoom,
      legend: legend(spec.presentation.legend, context),
      series: [{
        id: 'series:primary:candlestick', type: 'candlestick', name: spec.title, data,
        tooltip: { formatter: (params: { data?: { __lv_row_index?: number } }) => {
          const rowIndex = params.data?.__lv_row_index
          const row = rowIndex === undefined ? undefined : dataset?.rows[rowIndex]
          if (!row || !dataset) return ''
          return [spec.x, ...spec.y].map((ref) => {
            const value = row[dataset.columns.indexOf(ref.field)]
            return `${escapeHTML(fieldLabel(envelope, ref))}: ${escapeHTML(formatField(envelope, ref, value, context))}`
          }).join('<br>')
        } },
        ...chartLabel(envelope, spec.y[0], spec, context),
      }],
    }
  }
  if (spec.mark === 'boxplot') {
    const dataset = inlineDataset(envelope, spec.x.dataset)
    const categoryIndex = dataset?.columns.indexOf(spec.x.field) ?? -1
    const valueIndices = spec.y.map((item) => dataset?.columns.indexOf(item.field) ?? -1)
    const data = (dataset?.rows ?? []).flatMap((row, rowIndex) => {
      const rawValues = valueIndices.map((index) => row[index])
      const values = rawValues.map(Number)
      if (categoryIndex < 0 || valueIndices.some((index) => index < 0) || rawValues.some((value) => value === null || value === undefined || value === '') || values.some((value) => !Number.isFinite(value))) return []
      return [{ name: String(row[categoryIndex]), value: values, __lv_dataset: dataset?.id ?? spec.x.dataset, __lv_row_index: rowIndex }]
    })
    data.sort((left, right) => left.value[Math.floor(left.value.length / 2)]! - right.value[Math.floor(right.value.length / 2)]!)
    const primary = context.colors.data[0] ?? context.colors.accent
    const rotateLabels = data.length > 4
    return {
      ...axes,
      grid: { ...axes.grid, bottom: dataZoom ? 76 : rotateLabels ? 44 : 20 },
      xAxis: { ...axes.xAxis, data: data.map((item) => item.name), axisLabel: { ...axes.xAxis.axisLabel, interval: 0, rotate: rotateLabels ? 24 : 0 } },
      dataZoom,
      graphic: data.length === 0 ? [{ type: 'text', left: 'center', top: 'middle', silent: true, style: { text: 'No complete distribution data', fill: context.colors.muted, fontFamily: context.fontFamily, textAlign: 'center' } }] : undefined,
      series: [{
        id: `series:primary:${spec.mark}`, type: spec.mark, name: spec.title,
        data,
        itemStyle: { color: colorWithAlpha(primary, 0.24), borderColor: primary, borderWidth: 2 },
        emphasis: { itemStyle: { color: colorWithAlpha(primary, 0.4) } },
        ...chartLabel(envelope, spec.y[0], spec, context),
      }],
    }
  }
  if (spec.mark === 'heatmap' && spec.y.length >= 2) {
    const value = spec.y[1]!
    const fill = conditionalItemColor(envelope, value, 'mark_fill', context) ?? conditionalItemColor(envelope, value, 'series_color', context)
    const stroke = conditionalItemColor(envelope, value, 'mark_stroke', context)
    const gradient = conditionalGradient(envelope, value, 'mark_fill')
    const extent = finiteFieldExtent(envelope, value)
    const primary = context.colors.data[0] ?? context.colors.accent
    return {
      xAxis: axis(envelope, spec.x, 'category', context), yAxis: axis(envelope, spec.y[0]!, 'category', context),
      visualMap: gradient
        ? {
            min: gradient.minimum, max: gradient.maximum, calculable: false, orient: 'horizontal', left: 'center', bottom: 0,
            inRange: { color: [seriesColor('', gradient.low.color, context), seriesColor('', gradient.high.color, context)] },
            text: [formatDisplayField(envelope, value, gradient.maximum, context), formatDisplayField(envelope, value, gradient.minimum, context)],
            textStyle: { color: context.colors.muted },
          }
        : fill ? undefined : {
            min: extent.minimum, max: extent.maximum, calculable: false, orient: 'horizontal', left: 'center', bottom: 0,
            inRange: { color: [colorWithAlpha(primary, 0.18), primary] },
            text: [formatDisplayField(envelope, value, extent.maximum, context), formatDisplayField(envelope, value, extent.minimum, context)],
            textStyle: { color: context.colors.muted },
          },
      series: [{
        id: 'series:primary:heatmap', type: 'heatmap',
        encode: { x: spec.x.field, y: spec.y[0]?.field, value: value.field },
        itemStyle: { color: gradient ? undefined : fill, borderColor: stroke, borderWidth: stroke ? 2 : undefined },
        ...chartLabel(envelope, value, spec, context),
      }],
    }
  }
  const split = splitCartesianSeries(envelope, context, categoryColors)
  if (split) {
    const secondary = split.series.some((item) => item.yAxisIndex === 1)
    const primaryAxis = axis(envelope, spec.y[0]!, 'value', context, 'primary_y', spec.y)
    if (stackingMode(spec) === 'percent') applyPercentAxis(primaryAxis, context)
    return {
      dataset: split.datasets, legend: legend(spec.presentation.legend, context), xAxis: axis(envelope, spec.x, axisType(envelope, spec.x, 'category'), context, 'x'),
      yAxis: secondary ? [primaryAxis, axis(envelope, spec.y[0]!, 'value', context, 'secondary_y', spec.y)] : primaryAxis,
      dataZoom, series: [...split.series, ...interactionHitSeries(envelope, spec, split.series)],
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
  const hasSecondaryComboAxis = spec.mark === 'combo' && values.some((value) => comboByField.get(value.field)?.axis === 'secondary')
  const series = values.map((value, seriesIndex) => {
    const normalizedField = normalized?.dimensions.get(value.field)
    const combo = comboByField.get(value.field)
    const mark = combo?.mark ?? (spec.mark === 'combo' ? 'line' : spec.mark)
    const fill = conditionalItemColor(envelope, value, 'mark_fill', context) ?? conditionalItemColor(envelope, value, 'series_color', context)
    const stroke = conditionalItemColor(envelope, value, 'mark_stroke', context)
    const intent = spec.presentation.seriesIntent?.find((candidate) => candidate.value === value.field)?.color
    const markColor = fill ?? (intent === undefined
      ? context.colors.data[seriesIndex % context.colors.data.length] ?? context.colors.accent
      : seriesColor(value.field, intent, context))
    return {
      id: seriesID(value.dataset, value.field), type: cartesianSeriesType(mark), name: fieldLabel(envelope, value),
      yAxisIndex: combo?.axis === 'secondary' ? 1 : 0,
      encode: horizontal ? { x: normalizedField ?? value.field, y: spec.x.field } : { x: spec.x.field, y: normalizedField ?? value.field },
      smooth: spec.presentation.smooth, symbol: spec.presentation.showSymbols ? undefined : 'none', symbolSize: spec.presentation.symbolSize,
      stack: stack === 'none' ? undefined : stack, areaStyle: spec.presentation.area || mark === 'area' ? {} : undefined,
      itemStyle: {
        color: markColor,
        borderColor: stroke,
        borderWidth: stroke ? 2 : undefined,
      },
      step: spec.presentation.step ? 'middle' : false,
      ...(normalizedField
        ? percentLabel(envelope, spec, context, normalized?.columnIndices.get(value.field))
        : chartLabel(envelope, value, spec, context, combo?.axis === 'secondary' ? 'secondary_y' : 'primary_y', markColor)),
    }
  })
  return {
    ...axes,
    ...(normalized ? { dataset: { id: `dataset:${normalized.datasetID}`, source: normalized.source } } : {}),
    yAxis: hasSecondaryComboAxis
      ? [yAxis, axis(envelope, spec.y[0]!, 'value', context, 'secondary_y', values)]
      : yAxis,
    legend: legend(spec.presentation.legend, context), dataZoom,
    series: [...series, ...interactionHitSeries(envelope, spec, series)],
  }
}

function signedWaterfallColor(envelope: VisualizationEnvelope, ref: VisualizationFieldRef | undefined, context: RendererContext) {
  const dataset = ref ? inlineDataset(envelope, ref.dataset) : undefined
  const index = ref && dataset ? dataset.columns.indexOf(ref.field) : -1
  return (params: { value?: unknown[] }) => {
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

function finiteFieldExtent(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): { minimum: number; maximum: number } {
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  const values = index < 0 ? [] : (dataset?.rows ?? []).flatMap((row) => {
    const value = row[index]
    return typeof value === 'number' && Number.isFinite(value) ? [value] : []
  })
  if (values.length === 0) return { minimum: 0, maximum: 1 }
  const minimum = Math.min(...values)
  const maximum = Math.max(...values)
  if (minimum !== maximum) return { minimum, maximum }
  if (maximum > 0) return { minimum: 0, maximum }
  if (minimum < 0) return { minimum, maximum: 0 }
  return { minimum: 0, maximum: 1 }
}

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
    const horizontal = spec.kind === 'cartesian' && (spec.presentation.orientation === 'horizontal' || spec.mark === 'bar')
    const physical = authored.id === 'x'
      ? horizontal ? 'yAxis' : 'xAxis'
      : horizontal ? 'xAxis' : 'yAxis'
    const index = authored.id === 'secondary_y' ? 1 : 0
    const target = axisAt(option, physical, index)
    if (!target) continue
    const title = [authored.title, authored.unit ? `(${authored.unit})` : ''].filter(Boolean).join(' ')
    if (title) target.name = title
    if (authored.scale === 'log') target.type = 'log'
    else if (authored.scale === 'linear') target.type = 'value'
    if (authored.minimum !== undefined) target.min = authored.minimum
    if (authored.maximum !== undefined) target.max = authored.maximum
    if (authored.zero === 'include') target.scale = false
    else if (authored.zero === 'exclude') target.scale = true
    applyTickDensity(target, authored.tickDensity)
  }

  const coordinate = (axisID: 'x' | 'primary_y' | 'secondary_y') => {
    const horizontal = spec.kind === 'cartesian' && (spec.presentation.orientation === 'horizontal' || spec.mark === 'bar')
    if (axisID === 'x') return horizontal ? 'yAxis' : 'xAxis'
    return horizontal ? 'xAxis' : 'yAxis'
  }
  const markLineData = [
    ...(spec.referenceLines ?? []).flatMap((line) => {
      const value = resolveReferenceValue(envelope, line.value)
      if (value === undefined) return []
      return [{
        id: `reference-line:${line.id}`, name: line.label ?? '', [coordinate(line.axis)]: value,
        lineStyle: { color: toneColor(line.tone, context) },
      }]
    }),
    ...(spec.eventAnnotations ?? []).flatMap((annotation) => {
      const value = resolveReferenceValue(envelope, annotation.value)
      if (value === undefined) return []
      return [{
        id: `event-annotation:${annotation.id}`, name: annotation.label, [coordinate(annotation.axis)]: value,
        lineStyle: { color: toneColor(annotation.tone, context) },
      }]
    }),
  ]
  const markAreaData = (spec.referenceBands ?? []).flatMap((band) => {
    const from = resolveReferenceValue(envelope, band.from)
    const to = resolveReferenceValue(envelope, band.to)
    if (from === undefined || to === undefined) return []
    const key = coordinate(band.axis)
    return [[
      { id: `reference-band:${band.id}`, name: band.label ?? '', [key]: from, itemStyle: { color: toneColor(band.tone, context), opacity: 0.12 } },
      { [key]: to },
    ]]
  })
  if (markLineData.length === 0 && markAreaData.length === 0) return option
  const series = Array.isArray(option.series) ? option.series : []
  const owner = series.find((candidate: EChartsTranslation) => !candidate.silent && !String(candidate.id ?? '').startsWith('series:interaction-hit:'))
  if (!owner) return option
  if (markLineData.length > 0) owner.markLine = { symbol: ['none', 'none'], data: markLineData }
  if (markAreaData.length > 0) owner.markArea = { silent: true, data: markAreaData }
  return option
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

function applyTickDensity(axisOption: EChartsTranslation, density: 'automatic' | 'sparse' | 'normal' | 'dense'): void {
  if (density === 'automatic') return
  if (axisOption.type === 'category' || axisOption.type === 'time') {
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
  const values = dataset.rows.map((row) => row[index]).filter((candidate): candidate is string | number => typeof candidate === 'string' || typeof candidate === 'number')
  if (values.length === 0) return undefined
  switch (value.reducer) {
    case 'first': return values[0]
    case 'last': return values.at(-1)
    case 'minimum': return orderedReferenceValue(values, 'minimum')
    case 'maximum': return orderedReferenceValue(values, 'maximum')
    case 'mean': {
      const numbers = values.filter((candidate): candidate is number => typeof candidate === 'number' && Number.isFinite(candidate))
      return numbers.length === values.length ? numbers.reduce((sum, candidate) => sum + candidate, 0) / numbers.length : undefined
    }
    case 'median': {
      const numbers = values.filter((candidate): candidate is number => typeof candidate === 'number' && Number.isFinite(candidate)).sort((left, right) => left - right)
      if (numbers.length !== values.length) return undefined
      const middle = Math.floor(numbers.length / 2)
      return numbers.length % 2 ? numbers[middle] : (numbers[middle - 1]! + numbers[middle]!) / 2
    }
  }
}

function orderedReferenceValue(values: (string | number)[], reducer: 'minimum' | 'maximum'): string | number | undefined {
  if (values.every((value) => typeof value === 'number')) {
    return reducer === 'minimum' ? Math.min(...values as number[]) : Math.max(...values as number[])
  }
  if (values.every((value) => typeof value === 'string')) {
    return [...values as string[]].sort((left, right) => left.localeCompare(right, 'en'))[reducer === 'minimum' ? 0 : values.length - 1]
  }
  return undefined
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
  const horizontal = spec.presentation.orientation === 'horizontal' || spec.mark === 'bar'
  const automatic = authored === undefined || authored === 'automatic'
  const position = automatic ? horizontal ? 'insideRight' : undefined : authored === 'outside' ? horizontal ? 'right' : 'top' : authored
  const baseFormatter = labelFormatter(envelope, value, context, axisID, spec.y)
  const cue = value ? conditionalCueFormat(envelope, value) : undefined
  const color = value ? conditionalItemColor(envelope, value, 'label_foreground', context) : undefined
  const formatter = cue
      ? (params: { value?: unknown }) => {
          const row = Array.isArray(params.value) ? params.value : []
          const result = resolveConditionalForRow(envelope, cue, row)
          const glyph = result?.style.icon ? conditionalIconGlyph(result.style.icon) : ''
          return [glyph, baseFormatter(params)].filter(Boolean).join(' ')
        }
      : baseFormatter
  const translated = echartsLabelPolicy(envelope, value?.dataset ?? spec.x.dataset, spec.presentation.labelPolicy, formatter, context)
  translated.label.position = position
  if (color) translated.label.color = color
  else if (authored !== 'outside' && ['bar', 'column', 'waterfall', 'histogram'].includes(spec.mark)) {
    translated.label.color = '#fff'
    translated.label.textBorderColor = 'rgba(0, 0, 0, 0.55)'
    translated.label.textBorderWidth = 2
  }
  return translated
}

function cartesianGrid(spec: CartesianSpec): EChartsTranslation {
  const bottomLegend = spec.presentation.legend === 'bottom'
  return {
    left: 12,
    right: 16,
    top: spec.presentation.legend === 'top' ? 44 : 16,
    bottom: 16 + (bottomLegend ? 28 : 0) + (spec.presentation.dataZoom ? 42 : 0),
    containLabel: true,
  }
}

function splitCartesianSeries(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): { datasets: EChartsTranslation[]; series: EChartsTranslation[] } | undefined {
  const spec = envelope.spec
  if (spec.kind !== 'cartesian' || !spec.series || spec.y.length !== 1 || envelope.dataState.kind !== 'inline') return undefined
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === spec.series?.dataset)
  const seriesIndex = dataset?.columns.indexOf(spec.series.field) ?? -1
  if (!dataset || seriesIndex < 0) return undefined
  const available = [...new Set(dataset.rows.map((row) => row[seriesIndex]).filter((value): value is string | number | boolean => typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean'))]
  categoryColors.register(envelope, spec.series, available)
  const configured = new Map((spec.presentation.comboSeries ?? []).map((item) => [String(item.seriesValue), item]))
  const intents = new Map((spec.presentation.seriesIntent ?? []).map((item) => [String(item.value), item]))
  const authoredOrder = (spec.presentation.seriesIntent ?? [])
    .filter((item) => item.order !== undefined)
    .sort((left, right) => left.order! - right.order! || left.value.localeCompare(right.value, 'en'))
    .map((item) => String(item.value))
  const configuredOrder = [
    ...authoredOrder,
    ...(spec.presentation.comboSeries ?? []).map((item) => String(item.seriesValue)).filter((value) => !authoredOrder.includes(value)),
  ]
  const values = [
    ...configuredOrder.filter((value) => available.some((candidate) => String(candidate) === value)),
    ...available.filter((value) => !configuredOrder.includes(String(value))).sort((left, right) => String(left).localeCompare(String(right), 'en')),
  ]
  const datasets: EChartsTranslation[] = [{ id: `dataset:${dataset.id}`, source: selectedDatasetSource(envelope, dataset) }]
  const stack = stackingMode(spec)
  const normalizedSources = stack === 'percent' ? normalizedSeriesSources(envelope, dataset, spec, values) : undefined
  const series = values.map((value) => {
    const token = encodeURIComponent(String(value))
    const datasetID = `dataset:series:${spec.series?.field}:${token}`
    const normalized = normalizedSources?.get(String(value))
    datasets.push(normalized
      ? { id: datasetID, source: normalized.source }
      : { id: datasetID, fromDatasetId: `dataset:${dataset.id}`, transform: { type: 'filter', config: { dimension: spec.series?.field, '=': value } } })
    const combo = configured.get(String(value))
    const intent = intents.get(String(value))
    const mark = combo?.mark ?? (spec.mark === 'combo' ? 'line' : spec.mark)
    const valueRef = spec.y[0]!
    const fill = conditionalItemColor(envelope, valueRef, 'mark_fill', context) ?? conditionalItemColor(envelope, valueRef, 'series_color', context)
    const stroke = conditionalItemColor(envelope, valueRef, 'mark_stroke', context)
    const markColor = fill ?? (intent?.color ? seriesColor(String(value), intent.color, context) : categoryColors.color(envelope, spec.series!, value, context))
    return {
      id: `series:${spec.series?.dataset}:${spec.series?.field}:${token}`, datasetId: datasetID, name: String(value), type: cartesianSeriesType(mark), yAxisIndex: combo?.axis === 'secondary' ? 1 : 0,
      encode: { x: spec.x.field, y: normalized?.dimension ?? spec.y[0]?.field }, smooth: spec.presentation.smooth, symbol: spec.presentation.showSymbols ? undefined : 'none',
      stack: stack === 'none' ? undefined : stack, areaStyle: spec.presentation.area || mark === 'area' ? {} : undefined,
      itemStyle: {
        color: markColor,
        borderColor: stroke,
        borderWidth: stroke ? 2 : undefined,
      },
      step: spec.presentation.step ? 'middle' : false,
      ...(normalized
        ? percentLabel(envelope, spec, context, normalized.columnIndex)
        : chartLabel(envelope, spec.y[0], spec, context, combo?.axis === 'secondary' ? 'secondary_y' : 'primary_y', markColor)),
    }
  })
  return { datasets, series }
}

function normalizedSeriesSources(
  envelope: VisualizationEnvelope,
  dataset: NonNullable<ReturnType<typeof inlineDataset>>,
  spec: CartesianSpec,
  values: (string | number | boolean)[],
): Map<string, { source: unknown[][]; dimension: string; columnIndex: number }> {
  const source = selectedDatasetSource(envelope, dataset)
  const columns = source[0] as string[]
  const xIndex = columns.indexOf(spec.x.field)
  const seriesIndex = columns.indexOf(spec.series!.field)
  const valueIndex = columns.indexOf(spec.y[0]!.field)
  const totals = new Map<string, { positive: number; negative: number }>()
  const key = (value: unknown) => `${typeof value}:${String(value)}`
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
  for (const value of values) {
    const rows = source.slice(1).filter((row) => Object.is(row[seriesIndex], value)).map((row) => {
      const amount = row[valueIndex]
      const total = totals.get(key(row[xIndex]))
      const denominator = typeof amount === 'number' && amount < 0 ? total?.negative : total?.positive
      const normalized = typeof amount === 'number' && Number.isFinite(amount) && denominator ? amount / denominator * 100 : null
      return [...row, normalized]
    })
    result.set(String(value), {
      source: [[...columns, dimension], ...rows],
      dimension,
      columnIndex: columns.length,
    })
  }
  return result
}

function orderedY(spec: CartesianSpec): CartesianSpec['y'] {
  const order = new Map(
    (spec.presentation.seriesIntent ?? [])
      .filter((intent) => intent.order !== undefined)
      .map((intent) => [intent.value, intent.order!] as const),
  )
  return [...spec.y].sort((left, right) => {
    const leftOrder = order.get(left.field)
    const rightOrder = order.get(right.field)
    if (leftOrder !== undefined && rightOrder !== undefined) return leftOrder - rightOrder
    if (leftOrder !== undefined) return -1
    if (rightOrder !== undefined) return 1
    return spec.y.indexOf(left) - spec.y.indexOf(right)
  })
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

function stackingMode(spec: CartesianSpec): 'none' | 'normal' | 'percent' {
  return spec.presentation.stacking ?? (spec.presentation.stacked ? 'normal' : 'none')
}

function applyPercentAxis(axisOption: EChartsTranslation, context: RendererContext): void {
  const formatter = new Intl.NumberFormat(context.locale, { maximumFractionDigits: 1 })
  axisOption.axisLabel = { ...axisOption.axisLabel, formatter: (value: unknown) => typeof value === 'number' ? `${formatter.format(value)}%` : String(value) }
}

function percentLabel(envelope: VisualizationEnvelope, spec: CartesianSpec, context: RendererContext, columnIndex = -1) {
  const formatter = new Intl.NumberFormat(context.locale, { maximumFractionDigits: 1 })
  return echartsLabelPolicy(
    envelope,
    spec.x.dataset,
    spec.presentation.labelPolicy,
    (params: { value?: unknown }) => {
      const value = Array.isArray(params.value) ? params.value.at(columnIndex) : params.value
      return typeof value === 'number' ? `${formatter.format(value)}%` : ''
    },
    context,
  )
}

function seriesColor(value: string, intent: VisualizationColorIntent | undefined, context: RendererContext): string {
  switch (intent) {
    case 'accent': return context.colors.accent
    case 'neutral': return context.colors.muted
    case 'ink': return context.colors.foreground
    case 'success': return context.colors.success
    case 'warning': return context.colors.attention
    case 'danger': return context.colors.danger
  }
  if (intent?.startsWith('data_')) {
    const index = Number(intent.slice(5)) - 1
    if (Number.isInteger(index) && context.colors.data.length > 0) return context.colors.data[index % context.colors.data.length]!
  }
  let hash = 2166136261
  for (let index = 0; index < value.length; index++) hash = Math.imul(hash ^ value.charCodeAt(index), 16777619)
  return context.colors.data.length > 0 ? context.colors.data[(hash >>> 0) % context.colors.data.length]! : context.colors.accent
}

function conditionalItemColor(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  target: VisualizationConditionalFormat['target'],
  context: RendererContext,
): ((params: { value?: unknown }) => string | undefined) | undefined {
  const format = envelope.spec.conditionalFormatting?.find((candidate) =>
    candidate.target === target && candidate.field.dataset === ref.dataset && candidate.field.field === ref.field)
  if (!format) return undefined
  return (params) => {
    if (!Array.isArray(params.value)) return undefined
    const result = resolveConditionalForRow(envelope, format, params.value)
    return result ? conditionalStyleColor(result.style, (intent) => seriesColor('', intent, context)) : undefined
  }
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
  return envelope.spec.conditionalFormatting?.find((format) =>
    format.field.dataset === ref.dataset
    && format.field.field === ref.field
    && conditionalRuleHasIcon(format))
}

function conditionalRuleHasIcon(format: VisualizationConditionalFormat): boolean {
  const rule = format.rule
  if (rule.nullStyle.icon) return true
  if (rule.kind === 'gradient') return Boolean(rule.low.icon || rule.high.icon)
  if (rule.defaultStyle.icon) return true
  if (rule.kind === 'rules') return rule.rules.some((candidate) => Boolean(candidate.style.icon))
  return Object.values(rule.values).some((style) => Boolean(style.icon))
}

function resolveConditionalForRow(envelope: VisualizationEnvelope, format: VisualizationConditionalFormat, row: unknown[]) {
  const dataset = inlineDataset(envelope, format.field.dataset)
  return dataset ? resolveConditionalFormat(format, dataset.columns, row) : undefined
}


function axisType(envelope: VisualizationEnvelope, ref: CartesianSpec['x'], fallback: 'category' | 'value'): 'category' | 'value' | 'time' {
  const dataType = field(envelope, ref)?.dataType
  return dataType === 'temporal' || dataType === 'date' ? 'time' : fallback
}

function seriesID(dataset = 'primary', value = 'value'): string { return `series:${dataset}:${value}` }

function cartesianSeriesType(mark: CartesianSpec['mark']): string {
  switch (mark) {
    case 'bar': case 'column': case 'waterfall': case 'histogram': return 'bar'
    case 'candlestick': return 'candlestick'
    case 'boxplot': return 'boxplot'
    default: return 'line'
  }
}
