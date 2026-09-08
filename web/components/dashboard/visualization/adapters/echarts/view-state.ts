import type { VisualizationEnvelope } from '../../../../../generated/visualization'

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

export function responsiveEChartsPatch(option: Record<string, any>, width: number, height: number): Record<string, any> {
  if (!option || typeof option !== 'object' || !Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0 || option.grid === undefined) return {}
  const compact = width < 480 || height < 280
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
  const patch: Record<string, any> = { grid: Array.isArray(option.grid) ? grid : grid[0] }
  if (option.legend !== undefined) patch.legend = compact ? compactLegend(option.legend) : option.legend
  if (option.dataZoom !== undefined) patch.dataZoom = compact ? compactDataZoom(option.dataZoom, bottomLegend) : stripDataZoomNavigation(option.dataZoom)
  return patch
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

function compactDataZoom(value: unknown, bottomLegend: boolean): unknown {
  if (!Array.isArray(value)) return stripDataZoomNavigation(value)
  return value.map((entry) => {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) return entry
    const layout = stripDataZoomNavigationEntry(entry as Record<string, unknown>)
    return layout.type === 'slider' ? { ...layout, bottom: bottomLegend ? 28 : 12 } : layout
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
