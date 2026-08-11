import type { VisualizationEnvelope, VisualizationFieldRef } from '../../../../../generated/visualization'
import type { RendererContext } from '../../host-controller'
import { axis, field, formatField, inlineDataset, labelFormatter, legend, type EChartsTranslation } from './common'
import { applyDecisionContext } from './cartesian'
import { echartsLabelPolicy } from './label-policy'
import type { CategoryColorRegistry } from './category-colors'

type PointSpec = Extract<VisualizationEnvelope['spec'], { kind: 'point' }>

export function pointOption(envelope: VisualizationEnvelope, context: RendererContext, categoryColors: CategoryColorRegistry): EChartsTranslation {
  const spec = envelope.spec as PointSpec
  const dataset = inlineDataset(envelope, spec.x.dataset)
  const rows = dataset?.rows ?? []
  const large = spec.presentation.brush.length === 0 && (
    spec.presentation.largeMode === 'always'
    || spec.presentation.largeMode === 'automatic' && rows.length >= spec.presentation.largeThreshold
  )
  const labels = spec.label
    ? echartsLabelPolicy(envelope, spec.label.dataset, spec.presentation.labelPolicy, labelFormatter(envelope, spec.label, context), context)
    : { label: { show: false }, labelLayout: { hideOverlap: true } }
  const option: EChartsTranslation = {
    grid: { left: 12, right: spec.colorScale?.kind === 'quantitative' ? 54 : 16, top: 16, bottom: 16, containLabel: true },
    xAxis: pointAxis(envelope, spec.x, pointAxisType(envelope, spec.x), context),
    yAxis: axis(envelope, spec.y, 'value', context, 'primary_y'),
    legend: legend(spec.presentation.legend, context),
    series: [{
      id: 'series:primary:point',
      type: 'scatter',
      encode: {
        x: spec.x.field,
        y: spec.y.field,
        ...(spec.color ? { itemName: spec.color.field } : {}),
        ...(spec.series ? { itemGroupId: spec.series.field } : {}),
        ...(spec.tooltip ? { tooltip: spec.tooltip.map((item) => item.field) } : {}),
      },
      symbolSize: pointSymbolSize(envelope, spec),
      itemStyle: {
        opacity: spec.presentation.overplot === 'opacity' ? spec.presentation.opacity : 1,
        ...(spec.colorScale?.kind === 'categorical' && spec.color ? { color: categoricalPointColor(envelope, spec.color, context, categoryColors) } : {}),
      },
      ...labels,
      label: { ...labels.label, position: 'top' },
      large,
      largeThreshold: spec.presentation.largeThreshold,
      progressiveThreshold: spec.presentation.largeThreshold,
    }],
    ...(spec.colorScale?.kind === 'quantitative' && spec.color ? {
      visualMap: {
        type: 'continuous',
        dimension: spec.color.field,
        ...pointColorDomain(envelope, spec.color, spec.colorScale.minimum, spec.colorScale.maximum),
        calculable: true,
        right: 0,
        top: 'middle',
        textStyle: { color: context.colors.muted },
        inRange: { color: context.colors.data },
      },
    } : {}),
    ...(spec.presentation.brush.length > 0 ? pointBrush(spec) : {}),
  }
  return applyDecisionContext(envelope, context, option)
}

function pointAxisType(envelope: VisualizationEnvelope, ref: VisualizationFieldRef): 'value' | 'time' {
  const dataType = field(envelope, ref)?.dataType
  return dataType === 'temporal' || dataType === 'date' ? 'time' : 'value'
}

function pointAxis(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  type: 'value' | 'time',
  context: RendererContext,
): EChartsTranslation {
  const result = axis(envelope, ref, type, context, 'x')
  if (type !== 'time') return result
  const definition = field(envelope, ref)
  const dateFormatter = new Intl.DateTimeFormat(context.locale, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    timeZone: 'UTC',
  })
  result.splitNumber = 6
  result.axisLabel = {
    ...result.axisLabel,
    hideOverlap: true,
    formatter: (value: unknown) => {
      if (definition?.format) return formatField(envelope, ref, value, context)
      const date = new Date(value as string | number)
      return Number.isFinite(date.getTime()) ? dateFormatter.format(date) : formatField(envelope, ref, value, context)
    },
  }
  return result
}

function pointSymbolSize(envelope: VisualizationEnvelope, spec: PointSpec): number | ((value: unknown[]) => number) {
  if (!spec.size || !spec.sizeScale) return 10
  const dataset = inlineDataset(envelope, spec.size.dataset)
  const index = dataset?.columns.indexOf(spec.size.field) ?? -1
  const values = (dataset?.rows ?? []).map((row) => row[index]).filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  const minimum = spec.sizeScale.minimum ?? (values.length ? Math.min(...values) : 0)
  const maximum = spec.sizeScale.maximum ?? (values.length ? Math.max(...values) : minimum)
  return (value: unknown[]) => {
    const raw = Number(value[index])
    if (!Number.isFinite(raw) || maximum <= minimum) return spec.sizeScale!.minimumPixels
    const ratio = Math.max(0, Math.min(1, (raw - minimum) / (maximum - minimum)))
    return spec.sizeScale!.minimumPixels + ratio * (spec.sizeScale!.maximumPixels - spec.sizeScale!.minimumPixels)
  }
}

function pointColorDomain(
  envelope: VisualizationEnvelope,
  ref: VisualizationFieldRef,
  authoredMinimum?: number,
  authoredMaximum?: number,
): { min: number, max: number } {
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  const values = (dataset?.rows ?? [])
    .map((row) => row[index])
    .filter((value): value is number => typeof value === 'number' && Number.isFinite(value))
  let min = authoredMinimum ?? (values.length ? Math.min(...values) : 0)
  let max = authoredMaximum ?? (values.length ? Math.max(...values) : 1)
  if (min < max) return { min, max }
  if (authoredMinimum !== undefined && authoredMaximum === undefined) max = min + Math.max(1, Math.abs(min) * 0.01)
  else if (authoredMaximum !== undefined && authoredMinimum === undefined) min = max - Math.max(1, Math.abs(max) * 0.01)
  else {
    const delta = Math.max(1, Math.abs(min) * 0.01)
    min -= delta
    max += delta
  }
  return { min, max }
}

function categoricalPointColor(envelope: VisualizationEnvelope, ref: VisualizationFieldRef, context: RendererContext, categoryColors: CategoryColorRegistry) {
  const dataset = inlineDataset(envelope, ref.dataset)
  const index = dataset?.columns.indexOf(ref.field) ?? -1
  categoryColors.register(envelope, ref, index < 0 ? [] : (dataset?.rows ?? []).map((row) => row[index]))
  return (params: { value?: unknown[] }) => {
    const value = Array.isArray(params.value) ? params.value[index] : undefined
    return categoryColors.color(envelope, ref, value, context)
  }
}

function pointBrush(spec: PointSpec): EChartsTranslation {
  const toolbox = spec.presentation.brush.map((gesture) => gesture === 'rectangle' ? 'rect' : 'polygon')
  return {
    brush: { toolbox, brushMode: 'multiple', transformable: false, throttleType: 'debounce', throttleDelay: 120 },
    toolbox: { feature: { brush: { type: toolbox } }, right: 8, top: 0 },
  }
}
