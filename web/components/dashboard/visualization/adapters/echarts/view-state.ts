import type { VisualizationEnvelope } from '../../../../../generated/visualization'
import { proportionalConditionalCueFormat } from './proportional'

export type EChartsNavigationDefaults = Readonly<{ dataZoom: boolean; roam: boolean }>
export type EChartsViewState = Readonly<{
  dataZoom?: readonly Readonly<Record<string, unknown>>[]
  series?: readonly Readonly<{ id: string; center?: readonly unknown[]; zoom?: number }>[]
}>

export function echartsNavigationDefaults(envelope: VisualizationEnvelope): EChartsNavigationDefaults {
  return {
    dataZoom: envelope.spec.kind === 'cartesian' && envelope.spec.presentation.dataZoom === true,
    roam: envelope.spec.kind === 'hierarchy' && envelope.spec.presentation.roam === true,
  }
}

export function responsiveEChartsPatchNeedsResize(envelope: VisualizationEnvelope): boolean {
  const spec = envelope.spec
  return spec.kind === 'proportional'
    && (spec.mark === 'pie' || spec.mark === 'donut')
    && spec.presentation.labelPosition !== 'inside'
    && spec.presentation.legend === 'bottom'
    && proportionalConditionalCueFormat(envelope, spec.value) !== undefined
}

export function responsiveEChartsPatch(option: Record<string, any>, width: number, height: number, envelope?: VisualizationEnvelope): Record<string, any> {
  if (!option || typeof option !== 'object' || !Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return {}
  const compact = width < 480 || height < 280
  const proportional = responsiveProportionalLayout(option, width, height, envelope)
  if (option.grid === undefined && !proportional) return {}
  const grids = Array.isArray(option.grid) ? option.grid : [option.grid]
  const bottomLegend = compact && hasBottomLegend(option.legend)
  const slider = compact && hasSliderDataZoom(option.dataZoom)
  const compactBottom = 12 + (bottomLegend ? 28 : 0) + (slider ? 42 : 0)
  const grid = grids.map((value: Record<string, any>) => {
    const source = value && typeof value === 'object' && !Array.isArray(value) ? value : {}
    return {
      ...source,
      ...(compact ? {
        left: compactInset(source.left, 8),
        right: compactInset(source.right, 8),
        top: compactInset(source.top, 10),
        bottom: compactBottomInset(source.bottom, compactBottom, option.visualMap !== undefined),
      } : {}),
    }
  })
  const patch: Record<string, any> = option.grid === undefined ? {} : { grid: Array.isArray(option.grid) ? grid : grid[0] }
  // The proportional branch only needs a series layout patch. Re-emitting a
  // freshly generated legend on every resize could clear native selection
  // state while the series itself is intentionally merged by stable id.
  if (option.legend !== undefined && !proportional) patch.legend = compact ? compactLegend(option.legend) : option.legend
  if (option.dataZoom !== undefined) patch.dataZoom = compact
    ? compactDataZoom(option.dataZoom, bottomLegend, option.visualMap !== undefined)
    : stripDataZoomNavigation(option.dataZoom)
  if (proportional) Object.assign(patch, proportional)
  return patch
}

/**
 * ECharts' pie overlap pass is not aware of the bottom legend's text band.
 * Keep this narrowly scoped to the generated proportional conditional-cue
 * branch; all other series continue through the normal responsive patch.
 */
function responsiveProportionalLayout(option: Record<string, any>, width: number, height: number, envelope: VisualizationEnvelope | undefined): Record<string, any> | undefined {
  const spec = envelope?.spec
  if (!envelope || !responsiveEChartsPatchNeedsResize(envelope) || !spec || spec.kind !== 'proportional') return undefined
  const seriesList = Array.isArray(option.series) ? option.series : [option.series]
  const source = seriesList.find((candidate) => candidate && typeof candidate === 'object' && !Array.isArray(candidate) && candidate.id === `series:primary:${spec.mark}`)
  if (!source) return undefined
  return proportionalOutsideLayout(source, width, height, spec.presentation.legendTitle !== undefined)
}

function proportionalOutsideLayout(
  source: Record<string, any>,
  width: number,
  height: number,
  titledLegend: boolean,
): Record<string, any> {
  const series = source
  const labelLineLength = finiteNumber(series.labelLine?.length, 10)
  const labelLineLength2 = finiteNumber(series.labelLine?.length2, 8)
  const edgeDistance = finiteNumber(series.label?.edgeDistance, 8)
  // Keep a substantial text column on each side, but cap it so wide cards do
  // not turn the pie into a small center ornament. Narrow cards may shrink
  // the remaining center plot; normal eight-row cards use a ~25% virtual
  // per-side reservation.
  const requestedColumn = clamp(width * 0.25, 56, 160)
  const sideInset = Math.min(requestedColumn, width * 0.4)
  const legendBand = titledLegend ? 52 : 28
  const verticalInset = Math.min(legendBand, height / 2)
  const plotWidth = Math.max(0, width - sideInset * 2)
  const plotHeight = Math.max(0, height - verticalInset * 2)
  const availableRadius = Math.max(0, Math.min(plotWidth, plotHeight) / 2)
  const radius = responsivePieRadius(source.radius, availableRadius)
  // Measure the two text columns against the virtual radius budget before
  // ECharts' native pie pass runs. This keeps wrapped labels outside the ring
  // while still letting native side-aware overlap packing use real text boxes.
  const boundedEdgeDistance = Math.min(edgeDistance, width / 2)
  const textColumn = Math.max(0, width / 2 - radius[1] - boundedEdgeDistance * 2)
  const label = {
    ...(series.label && typeof series.label === 'object' && !Array.isArray(series.label) ? series.label : {}),
    width: textColumn,
    overflow: 'break',
  }
  const distanceToLabelLine = finiteNumber(series.label?.distanceToLabelLine, 4)
  const labelLayout = (params: {
    labelRect?: { x?: number; y?: number; width?: number; height?: number }
    labelLinePoints?: readonly (readonly number[])[]
  }) => {
    const anchor = params.labelLinePoints?.[0]
    const rect = params.labelRect
    const hasAnchor = anchor && Number.isFinite(anchor[0]) && Number.isFinite(anchor[1])
    const hasRect = rect && [rect.x, rect.y, rect.width, rect.height].every((value) => typeof value === 'number' && Number.isFinite(value))
    if (!hasAnchor || !hasRect) return { hideOverlap: false }
    const centerX = width / 2
    const centerY = height / 2
    const vectorX = anchor[0] - centerX
    const vectorY = anchor[1] - centerY
    const vectorLength = Math.hypot(vectorX, vectorY)
    const radial = vectorLength > 0
      ? [centerX + vectorX / vectorLength * radius[1], centerY + vectorY / vectorLength * radius[1]]
      : [centerX, centerY]
    const right = anchor[0] >= centerX
    const lane = centerX + (right ? radius[1] + distanceToLabelLine : -radius[1] - distanceToLabelLine)
    const labelY = rect.y! + rect.height! / 2
    const labelEdge = right ? rect.x! : rect.x! + rect.width!
    const endpoint = [labelEdge + (right ? -distanceToLabelLine : distanceToLabelLine), labelY]
    // Keep the guide's bend outside the overall circle. Native pie packing
    // may move a label vertically, but should not drag its leader through a
    // different sector while doing so (especially for rose charts).
    return {
      hideOverlap: false,
      labelLinePoints: [
        [anchor[0], anchor[1]],
        radial,
        [lane, radial[1]],
        [lane, labelY],
        endpoint,
      ],
    }
  }
  return {
    series: [{
      id: source.id,
      // Keep the full card as ECharts' label view rect. The radius is sized
      // from the virtual text-safe plot above, while native edge alignment
      // gets the card-wide columns needed for measured status/value labels.
      left: 0,
      right: 0,
      top: verticalInset,
      bottom: verticalInset,
      radius,
      label,
      // Keep ECharts' native side-aware pie overlap pass and only reroute its
      // measured leaders around the overall circle after packing.
      labelLayout,
      labelLine: {
        ...(source.labelLine && typeof source.labelLine === 'object' ? source.labelLine : {}),
        length: labelLineLength,
        length2: labelLineLength2,
      },
    }],
  }
}

function responsivePieRadius(value: unknown, availableRadius: number): [number, number] {
  const source = Array.isArray(value) ? value : [0, value === undefined ? '50%' : value]
  const inner = radiusPixels(source[0], availableRadius, 0)
  const outer = Math.max(inner, radiusPixels(source[1], availableRadius, availableRadius * 0.5))
  return [inner, outer]
}

function radiusPixels(value: unknown, availableRadius: number, fallback: number): number {
  if (typeof value === 'string' && value.trim().endsWith('%')) {
    const ratio = Number.parseFloat(value)
    return Number.isFinite(ratio) ? Math.max(0, Math.min(availableRadius, availableRadius * ratio / 100)) : fallback
  }
  if (typeof value === 'number' && Number.isFinite(value)) return Math.max(0, Math.min(availableRadius, value))
  return fallback
}

function finiteNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.max(minimum, Math.min(maximum, value))
}

function compactInset(value: unknown, fallback: number): unknown {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.min(value, fallback)
  if (typeof value === 'string') return value
  return fallback
}

function compactBottomInset(value: unknown, fallback: number, preserveExisting: boolean): unknown {
  if (preserveExisting && typeof value === 'number' && Number.isFinite(value)) return Math.max(value, fallback)
  if (typeof value === 'string') return value
  return fallback
}

function hasBottomLegend(value: unknown): boolean {
  const legends = Array.isArray(value) ? value : [value]
  return legends.some((entry) => entry && typeof entry === 'object' && !Array.isArray(entry) && (entry as Record<string, unknown>).bottom !== undefined)
}

function hasSliderDataZoom(value: unknown): boolean {
  if (!Array.isArray(value)) return false
  return value.some((entry) => entry && typeof entry === 'object' && !Array.isArray(entry) && (entry as Record<string, unknown>).type === 'slider')
}

function compactLegend(value: unknown): unknown {
  const legends = Array.isArray(value) ? value : [value]
  const result = legends.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const legend = entry as Record<string, unknown>
    return legend.bottom === undefined ? legend : { ...legend, bottom: 0 }
  })
  return Array.isArray(value) ? result : result[0]
}

function compactDataZoom(value: unknown, bottomLegend: boolean, preserveExisting: boolean): unknown {
  if (!Array.isArray(value)) return stripDataZoomNavigation(value)
  return value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const layout = stripDataZoomNavigationEntry(entry as Record<string, unknown>)
    if (layout.type !== 'slider') return layout
    const fallback = bottomLegend ? 28 : 12
    const bottom = preserveExisting && typeof layout.bottom === 'number' && Number.isFinite(layout.bottom)
      ? Math.max(layout.bottom, fallback)
      : fallback
    return { ...layout, bottom }
  })
}

function stripDataZoomNavigation(value: unknown): unknown {
  if (!Array.isArray(value)) {
    return value && typeof value === 'object' ? stripDataZoomNavigationEntry(value as Record<string, unknown>) : value
  }
  return value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    return stripDataZoomNavigationEntry(entry as Record<string, unknown>)
  })
}

function stripDataZoomNavigationEntry(entry: Record<string, unknown>): Record<string, unknown> {
  const layout = { ...entry }
  delete layout.start
  delete layout.end
  delete layout.startValue
  delete layout.endValue
  return layout
}

export function overlayDataZoomNavigation(layout: unknown, state: EChartsViewState['dataZoom']): unknown {
  if (!Array.isArray(layout) || !Array.isArray(state)) return layout
  return layout.map((entry, index) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const current = state[index]
    if (!current) return entry
    const result = { ...(entry as Record<string, unknown>) }
    for (const key of ['start', 'end', 'startValue', 'endValue']) {
      if (current[key] !== undefined) result[key] = current[key]
    }
    return result
  })
}

export function captureEChartsViewState(option: Record<string, any>): EChartsViewState {
  if (!option || typeof option !== 'object') return {}
  const dataZoom = Array.isArray(option.dataZoom)
    ? option.dataZoom.flatMap((value: Record<string, any>) => {
        if (!value || typeof value !== 'object' || Array.isArray(value)) return []
        const state = Object.fromEntries(
          ['start', 'end', 'startValue', 'endValue'].flatMap((key) => value[key] === undefined ? [] : [[key, value[key]]]),
        )
        return Object.keys(state).length > 0 ? [state] : []
      })
    : undefined
  const series = Array.isArray(option.series)
    ? option.series.flatMap((value: Record<string, any>) => {
        if (!value || typeof value !== 'object' || Array.isArray(value)) return []
        if (typeof value.id !== 'string') return []
        const center = Array.isArray(value.center) ? [...value.center] : undefined
        const zoom = typeof value.zoom === 'number' && Number.isFinite(value.zoom) ? value.zoom : undefined
        return center === undefined && zoom === undefined ? [] : [{ id: value.id, ...(center ? { center } : {}), ...(zoom === undefined ? {} : { zoom }) }]
      })
    : undefined
  return {
    ...(dataZoom && dataZoom.length > 0 ? { dataZoom } : {}),
    ...(series && series.length > 0 ? { series } : {}),
  }
}
