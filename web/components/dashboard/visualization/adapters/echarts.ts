import type { VisualizationEnvelope } from '../../../../generated/visualization'
import type { ECharts, EChartsOption } from 'echarts'
import { Change, defaultRendererContext, normalizeRendererLocale, type RendererAdapter, type RendererContext, type RendererHandle } from '../host-controller'
import { clearInteractionCommand, interactionCommandForRow } from '../interaction-command'
import { projectVisualizationHighlights } from '../highlight'
import { baseOption } from './echarts/common'
import { CategoryColorRegistry, categoryColorRegistryFor, categoryIdentity } from './echarts/category-colors'
import { cartesianOption } from './echarts/cartesian'
import { hierarchyOption } from './echarts/hierarchy'
import { polarOption } from './echarts/polar'
import { pointCategoryRowIndexes, pointOption } from './echarts/point'
import { proportionalCategories, proportionalCenterText, proportionalOption } from './echarts/proportional'
import {
  captureEChartsViewState,
  echartsNavigationDefaults,
  overlayDataZoomNavigation,
  responsiveEChartsPatch,
  type EChartsViewState,
} from './echarts/view-state'

export { interactionCommandForRow, normalizeRendererLocale }

export { captureEChartsViewState, echartsNavigationDefaults, responsiveEChartsPatch } from './echarts/view-state'
export type { EChartsNavigationDefaults, EChartsViewState } from './echarts/view-state'

export function echartsOption(envelope: VisualizationEnvelope, context: RendererContext = defaultRendererContext, categoryColors = new CategoryColorRegistry()): EChartsOption {
  const base = baseOption(envelope, context)
  let translated: Record<string, any>
  switch (envelope.spec.kind) {
    case 'cartesian': translated = cartesianOption(envelope, context, categoryColors); break
    case 'proportional': translated = proportionalOption(envelope, context, categoryColors); break
    case 'hierarchy': translated = hierarchyOption(envelope, context); break
    case 'polar': translated = polarOption(envelope, context); break
    case 'point': translated = pointOption(envelope, context, categoryColors); break
    default: throw new Error(`ECharts cannot render visualization kind ${JSON.stringify(envelope.spec.kind)}`)
  }
  const option = { ...base, ...translated } as Record<string, any>
  if (base.aria || translated.aria) option.aria = { ...(base.aria ?? {}), ...(translated.aria ?? {}) }
  if (base.graphic && translated.graphic) option.graphic = [...base.graphic, ...translated.graphic]
  applyCrossHighlight(option, envelope)
  return option as EChartsOption
}

function applyCrossHighlight(option: Record<string, any>, envelope: VisualizationEnvelope): void {
  if (envelope.dataState.kind !== 'inline' || (envelope.highlights ?? []).length === 0) return
  const datasetID = envelope.spec.datasets[0]?.id
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
  if (!dataset) return
  const projection = projectVisualizationHighlights(envelope, dataset.id, dataset.columns, dataset.rows)
  const series = Array.isArray(option.series) ? option.series : option.series ? [option.series] : []
  for (const item of series) {
    if (item.silent === true) continue
    const rowIndices = seriesRowIndices(envelope, dataset.columns, dataset.rows, item)
    const opacity = (params: { dataIndex?: number }) => {
      if (projection.matchedRows.size === 0) return 0.45
      const rowIndex = params.dataIndex === undefined ? undefined : rowIndices[params.dataIndex]
      return rowIndex !== undefined && projection.matchedRows.has(rowIndex) ? 1 : 0.2
    }
    item.itemStyle = { ...(item.itemStyle ?? {}), opacity }
    item.lineStyle = { ...(item.lineStyle ?? {}), opacity: 0.55 }
  }
  option.aria = {
    ...(option.aria ?? {}),
    enabled: true,
    description: [option.aria?.description, projection.announcement].filter(Boolean).join(' '),
  }
}

function seriesRowIndices(
  envelope: VisualizationEnvelope,
  columns: readonly string[],
  rows: readonly (readonly unknown[])[],
  series: Record<string, any>,
): number[] {
  if (Array.isArray(series.__lv_source_row_indices)) return series.__lv_source_row_indices
  if (envelope.spec.kind !== 'cartesian' || !envelope.spec.series || series.name === undefined) {
    return rows.map((_, index) => index)
  }
  const index = columns.indexOf(envelope.spec.series.field)
  if (index < 0) return rows.map((_, rowIndex) => rowIndex)
  return rows.flatMap((row, rowIndex) => String(row[index]) === String(series.name) ? [rowIndex] : [])
}

export const adapter: RendererAdapter = {
  async mount(container, envelope, context) {
    const echarts = await import('echarts')
    const frame = createEChartsRendererFrame(container)
    const chart = echarts.init(frame, undefined, { renderer: 'canvas', devicePixelRatio: context.devicePixelRatio })
    const handle = new EChartsHandle(container, frame, chart, categoryColorRegistryFor(container))
    try {
      handle.mount(envelope, context)
      return handle
    } catch (error) {
      handle.dispose()
      throw error
    }
  },
}

export function createEChartsRendererFrame(container: HTMLElement, createFrame: () => HTMLElement = () => document.createElement('div')): HTMLElement {
  const frame = createFrame()
  frame.style.cssText = 'display:block;width:100%;height:100%;min-width:0;min-height:0;overflow:hidden'
  container.replaceChildren(frame)
  return frame
}

export function removeEChartsRendererFrame(container: ParentNode, frame: HTMLElement): void {
  if (frame.parentNode === container) frame.remove()
}

export class EChartsHandle implements RendererHandle {
  private envelope?: VisualizationEnvelope
  private context?: RendererContext
  private disposed = false
  private readiness: Promise<void> = Promise.resolve()
  private readinessAbort?: AbortController
  private compactLayout?: boolean
  private lastWidth = 0
  private lastHeight = 0
  private dataZoomInitialized = false
  private focusedHeatmapFullData = false
  private compactHeatmapZoom?: { start?: number; end?: number }

  constructor(private readonly container: HTMLElement, private readonly frame: HTMLElement, private readonly chart: ECharts, private readonly categoryColors: CategoryColorRegistry) {
    this.chart.on('click', this.handleClick)
    this.chart.on('brushSelected', this.handleBrushSelected)
    this.chart.on('legendselectchanged', this.handleLegendSelect)
    this.chart.on('mouseover', this.handleMouseOver)
    this.chart.on('mouseout', this.handleMouseOut)
  }

  mount(envelope: VisualizationEnvelope, context: RendererContext): void {
    this.envelope = envelope
    this.context = context
    this.readinessAbort?.abort()
    this.readinessAbort = new AbortController()
    this.readiness = waitForEChartsFrame(this.chart, 5_000, this.readinessAbort.signal)
    const option = echartsOption(envelope, context, this.categoryColors)
    this.dataZoomInitialized = hasEChartsDataZoom(option)
    this.chart.setOption(option, { notMerge: true, lazyUpdate: false })
  }

  whenReady(): Promise<void> { return this.readiness }

  update(envelope: VisualizationEnvelope, change: Change, context: RendererContext): void {
    if (this.disposed) return
    const previous = this.envelope
    const viewState = previous && preservesEChartsViewState(previous, envelope) ? this.captureViewState() : undefined
    this.envelope = envelope
    this.context = context
    const option = echartsOption(envelope, context, this.categoryColors)
    const initializeDataZoom = !this.dataZoomInitialized && hasEChartsDataZoom(option)
    const resetDataZoom = hasEmptyEChartsDataZoom(option)
    const refreshHeatmapDataZoom = isHeatmapWithDataZoom(envelope) && (change & Change.Data) !== 0 && hasEChartsDataZoom(option)
    const preserveHeatmapFocus = previous !== undefined && preservesEChartsViewState(previous, envelope)
    const resetHeatmapFocus = isHeatmapWithDataZoom(envelope)
      && (resetDataZoom || ((change & Change.Spec) !== 0 && !preserveHeatmapFocus))
    const plan = echartsUpdatePlan(change, option, initializeDataZoom, refreshHeatmapDataZoom)
    if ((change & Change.Spec) !== 0 || initializeDataZoom || resetDataZoom) this.dataZoomInitialized = hasEChartsDataZoom(option)
    if (resetHeatmapFocus) {
      this.focusedHeatmapFullData = false
      this.compactHeatmapZoom = undefined
    }
    this.chart.setOption(plan.option, plan.settings)
    if (viewState) this.restoreViewState(viewState, !resetDataZoom)
    this.applyResponsiveLayout(true)
    if ((change & Change.Spec) !== 0 || initializeDataZoom || refreshHeatmapDataZoom || resetDataZoom) this.syncHeatmapFocusZoom(true)
  }

  resize(width: number, height: number): void {
    this.chart.resize({ width, height, silent: true })
    if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return
    this.lastWidth = width
    this.lastHeight = height
    this.applyResponsiveLayout(false)
    this.syncHeatmapFocusZoom()
  }

  private applyResponsiveLayout(force: boolean): void {
    const envelope = this.envelope
    if (!envelope || !this.context || this.lastWidth <= 0 || this.lastHeight <= 0) return
    const compact = this.lastWidth < 480 || this.lastHeight < 280
    if (!force && compact === this.compactLayout) return
    this.compactLayout = compact
    const patch = responsiveEChartsPatch(echartsOption(envelope, this.context, this.categoryColors) as Record<string, any>, this.lastWidth, this.lastHeight)
    if (patch.dataZoom !== undefined) patch.dataZoom = overlayDataZoomNavigation(patch.dataZoom, this.captureViewState().dataZoom)
    if (Object.keys(patch).length > 0) this.chart.setOption(patch, { notMerge: false, lazyUpdate: !force })
  }

  private syncHeatmapFocusZoom(force = false): void {
    const envelope = this.envelope
    if (!heatmapFocusZoomEnabled(envelope, this.dataZoomInitialized)) {
      this.focusedHeatmapFullData = false
      this.compactHeatmapZoom = undefined
      return
    }
    const focused = visualizationHostIsFocused(this.container)
    if (!force && focused === this.focusedHeatmapFullData) return
    if (focused) {
      const current = ((this.chart.getOption() as Record<string, any>).dataZoom ?? [])[0] as Record<string, any> | undefined
      if (!this.focusedHeatmapFullData && current) {
        this.compactHeatmapZoom = {
          ...(Number.isFinite(current.start) ? { start: current.start } : {}),
          ...(Number.isFinite(current.end) ? { end: current.end } : {}),
        }
      }
      this.chart.setOption({ dataZoom: heatmapFocusDataZoom(true) }, { lazyUpdate: false })
    } else if (this.focusedHeatmapFullData) {
      const range = this.compactHeatmapZoom ?? {}
      this.chart.setOption({ dataZoom: heatmapFocusDataZoom(false, range) }, { lazyUpdate: false })
    }
    this.focusedHeatmapFullData = focused
  }

  async snapshot(): Promise<Blob> {
    const response = await fetch(this.chart.getDataURL({ type: 'png', pixelRatio: 2, backgroundColor: 'transparent' }))
    return response.blob()
  }

  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    this.readinessAbort?.abort()
    this.readinessAbort = undefined
    this.chart.off('click', this.handleClick)
    this.chart.off('brushSelected', this.handleBrushSelected)
    this.chart.off('legendselectchanged', this.handleLegendSelect)
    this.chart.off('mouseover', this.handleMouseOver)
    this.chart.off('mouseout', this.handleMouseOut)
    this.chart.dispose()
    removeEChartsRendererFrame(this.container, this.frame)
  }

  captureViewState(): EChartsViewState {
    return captureEChartsViewState(this.chart.getOption() as Record<string, any>)
  }

  restoreViewState(state: unknown, restoreDataZoom = true): void {
    if (!state || typeof state !== 'object') return
    const value = state as EChartsViewState
    const patch: Record<string, any> = {}
    if (restoreDataZoom && Array.isArray(value.dataZoom) && value.dataZoom.length > 0) patch.dataZoom = value.dataZoom
    if (Array.isArray(value.series) && value.series.length > 0) patch.series = value.series
    if (Object.keys(patch).length > 0) this.chart.setOption(patch, { notMerge: false, lazyUpdate: false })
  }

  private readonly handleClick = (params: unknown) => {
    const envelope = this.envelope
    if (!envelope) return
    const event = params as { value?: unknown; data?: { __lv_dataset?: unknown; __lv_row_index?: unknown } }
    let datasetID: string | undefined
    let row: unknown[] | undefined
    if (Array.isArray(event.value)) {
      datasetID = envelope.spec.interactions.find((candidate) => candidate.kind === 'select')?.mappings[0]?.source.dataset
      row = event.value
    } else if (typeof event.data?.__lv_dataset === 'string' && Number.isInteger(event.data.__lv_row_index)) {
      datasetID = event.data.__lv_dataset
      if (envelope.dataState.kind === 'inline') {
        row = envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)?.rows[event.data.__lv_row_index as number]
      }
    }
    if (!datasetID || !row) return
    const command = interactionCommandForRow(envelope, datasetID, row)
    if (!command) return
    this.container.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: command }))
  }

  private readonly handleBrushSelected = (params: unknown) => {
    const envelope = this.envelope
    if (!envelope) return
    for (const command of brushSelectionCommands(envelope, params)) {
      this.container.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: command }))
    }
  }

  private readonly handleLegendSelect = (params: unknown) => {
    const envelope = this.envelope
    const event = params as { name?: unknown }
    if (!envelope || envelope.spec.kind !== 'proportional' || typeof event.name !== 'string') return
    const command = legendSelectionCommand(envelope, event.name, this.context ?? defaultRendererContext)
    if (!command) return
    // Governed proportional legends are selection controls, not visibility
    // filters. Keep the sector visible while the host applies the selection
    // command; ordinary legends without a valid command keep ECharts' native
    // toggle behavior.
    this.chart.dispatchAction({ type: 'legendSelect', name: event.name })
    this.container.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: command }))
  }

  private readonly handleMouseOver = (params: unknown) => {
    const envelope = this.envelope
    const context = this.context
    const event = params as { componentType?: unknown; value?: unknown }
    if (!envelope || !context || event.componentType !== 'series' || !Array.isArray(event.value)) return
    this.setProportionalCenter(proportionalCenterText(envelope, context, event.value))
  }

  private readonly handleMouseOut = (params: unknown) => {
    const envelope = this.envelope
    const context = this.context
    const event = params as { componentType?: unknown }
    if (!envelope || !context || event.componentType !== 'series') return
    this.setProportionalCenter(proportionalCenterText(envelope, context))
  }

  private setProportionalCenter(text: string | undefined): void {
    if (text === undefined) return
    this.chart.setOption({ graphic: [{ id: 'graphic:proportional:center', style: { text } }] })
  }

}

function isHeatmapWithDataZoom(envelope: VisualizationEnvelope | undefined): boolean {
  return envelope?.spec.kind === 'cartesian'
    && envelope.spec.mark === 'heatmap'
    && envelope.spec.presentation.dataZoom === true
}

export function heatmapFocusZoomEnabled(envelope: VisualizationEnvelope | undefined, dataZoomInitialized: boolean): boolean {
  return dataZoomInitialized && isHeatmapWithDataZoom(envelope)
}

type HeatmapZoomRange = Readonly<{ start?: number; end?: number }>

export function heatmapFocusDataZoom(focused: boolean, compactRange: HeatmapZoomRange = {}): Array<Record<string, any>> {
  const range = focused ? { start: 0, end: 100 } : compactRange
  return [
    { id: 'dataZoom:heatmap:inside', type: 'inside', disabled: focused, ...range },
    {
      id: 'dataZoom:heatmap:slider', type: 'slider', show: true, bottom: 64, showDetail: false, brushSelect: false, ...range,
    },
  ]
}

function visualizationHostIsFocused(container: HTMLElement): boolean {
  const root = container.getRootNode()
  return root instanceof ShadowRoot && root.host.getAttribute('slot') === 'focus-visual'
}

export function preservesEChartsViewState(previous: VisualizationEnvelope, next: VisualizationEnvelope): boolean {
  const previousMark = 'mark' in previous.spec ? previous.spec.mark : undefined
  const nextMark = 'mark' in next.spec ? next.spec.mark : undefined
  if (previous.spec.kind !== next.spec.kind || previousMark !== nextMark) return false
  if (echartsNavigationChannelSignature(previous.spec) !== echartsNavigationChannelSignature(next.spec)) return false
  if (echartsNavigationPresentationSignature(previous.spec) !== echartsNavigationPresentationSignature(next.spec)) return false
  const oldNavigation = echartsNavigationDefaults(previous)
  const nextNavigation = echartsNavigationDefaults(next)
  return oldNavigation.dataZoom && nextNavigation.dataZoom || oldNavigation.roam && nextNavigation.roam
}

/**
 * Navigation state is meaningful only while the chart's camera has the same
 * semantic coordinate system.  Keep this separate from the renderer option:
 * legend/label styling can change without invalidating a user's zoom, while an
 * axis domain, orientation, or hierarchy layout change cannot.
 */
function echartsNavigationPresentationSignature(spec: VisualizationEnvelope['spec']): string {
  switch (spec.kind) {
    case 'cartesian':
      return JSON.stringify({
        displayUnits: spec.presentation.displayUnits,
        orientation: spec.presentation.orientation,
        stacked: spec.presentation.stacked,
        stacking: spec.presentation.stacking,
        comboSeries: spec.presentation.comboSeries,
        axes: [...(spec.axes ?? [])]
          .sort((left, right) => left.id.localeCompare(right.id))
          .map((axis) => [
            axis.id, axis.type, axis.scale, axis.zero, axis.inversion,
            axis.minimum, axis.maximum, axis.unit, axis.displayUnits, axis.dateUnit,
          ]),
      })
    case 'hierarchy':
      return JSON.stringify({
        orientation: spec.presentation.orientation,
        layout: spec.presentation.layout,
        initialDepth: spec.presentation.initialDepth,
        nodeGap: spec.presentation.nodeGap,
        curveness: spec.presentation.curveness,
      })
    default:
      return ''
  }
}

function echartsNavigationChannelSignature(spec: VisualizationEnvelope['spec']): string {
  const channels: Record<string, unknown> = {}
  const ref = (value: unknown): string | undefined => {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
    const candidate = value as { dataset?: unknown; field?: unknown }
    return typeof candidate.dataset === 'string' && typeof candidate.field === 'string'
      ? `${candidate.dataset}:${candidate.field}` : undefined
  }
  const refs = (values: unknown): Array<string | undefined> => Array.isArray(values) ? values.map(ref) : [ref(values)]
  switch (spec.kind) {
    case 'cartesian':
      channels.x = ref(spec.x)
      channels.y = refs(spec.y)
      channels.series = ref(spec.series)
      break
    case 'hierarchy':
      channels.node = ref(spec.node)
      channels.parent = ref(spec.parent)
      channels.source = ref(spec.source)
      channels.target = ref(spec.target)
      channels.value = ref(spec.value)
      break
    default:
      break
  }
  return JSON.stringify({
    datasets: spec.datasets.map((dataset) => ({ id: dataset.id, fields: dataset.fields.map((field) => [field.id, field.dataType]) })),
    channels,
  })
}

export function legendSelectionCommand(envelope: VisualizationEnvelope, categoryName: string, context: RendererContext = defaultRendererContext) {
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') return undefined
  const ref = envelope.spec.category
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === ref.dataset)
  const categoryIndex = dataset?.columns.indexOf(ref.field) ?? -1
  if (!dataset || categoryIndex < 0) return undefined
  const values = dataset.rows.map((candidate) => candidate[categoryIndex])
  const categories = proportionalCategories(values, envelope, ref, context)
  const matches = categories.filter((candidate) => candidate.name === categoryName || candidate.rawName === categoryName || candidate.label === categoryName)
  if (matches.length !== 1) return undefined
  const row = dataset.rows.find((candidate) => categoryIdentity(candidate[categoryIndex]) === matches[0]!.identity)
  return row ? interactionCommandForRow(envelope, dataset.id, row) : undefined
}

export function brushSelectionCommands(envelope: VisualizationEnvelope, params: unknown) {
  if (envelope.spec.kind !== 'point' || envelope.dataState.kind !== 'inline') return []
  const datasetID = envelope.spec.x.dataset
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
  if (!dataset) return []
  const categoryRows = pointCategoryRowIndexes(envelope, dataset.columns, dataset.rows)
  const event = params as { batch?: Array<{ seriesIndex?: number; selected?: Array<{ seriesIndex?: number; dataIndex?: number[] }> }> }
  const indexes = new Set<number>()
  for (const batch of event.batch ?? []) {
    for (const selected of batch.selected ?? []) {
      for (const index of selected.dataIndex ?? []) {
        if (!Number.isInteger(index) || index < 0) continue
        const seriesIndex = selected.seriesIndex ?? batch.seriesIndex
        const sourceIndex = categoryRows !== undefined && seriesIndex !== undefined
          ? categoryRows[seriesIndex]?.[index]
          : index
        if (sourceIndex !== undefined && sourceIndex >= 0 && sourceIndex < dataset.rows.length) indexes.add(sourceIndex)
      }
    }
  }
  const commands = [...indexes].sort((left, right) => left - right).flatMap((index) => {
    const command = interactionCommandForRow(envelope, datasetID, dataset.rows[index]!)
    return command ? [command] : []
  })
  if (commands.length === 0) {
    const clear = clearInteractionCommand(envelope)
    return clear ? [clear] : []
  }
  commands[0] = { ...commands[0]!, action: 'replace' }
  return commands
}

export type EChartsUpdatePlan = Readonly<{
  option: Record<string, any>
  settings: { notMerge: boolean; lazyUpdate: boolean; replaceMerge?: string[] }
}>

export function echartsUpdatePlan(change: Change, option: EChartsOption, initializeDataZoom = false, refreshHeatmapDataZoom = false): EChartsUpdatePlan {
  if ((change & Change.Spec) !== 0) {
    return { option: option as Record<string, any>, settings: { notMerge: true, lazyUpdate: false } }
  }
  const source = option as Record<string, any>
  const patch: Record<string, any> = {}
  const replaceMerge: string[] = []
  if ((change & Change.Data) !== 0) {
    patch.dataset = source.dataset
    patch.series = source.series
    patch.legend = source.legend ?? []
    patch.visualMap = source.visualMap ?? []
    patch.graphic = source.graphic ?? []
    const resetDataZoom = hasEmptyEChartsDataZoom(option)
    const replaceDataZoom = initializeDataZoom || refreshHeatmapDataZoom
    if (replaceDataZoom) {
      if (source.dataZoom !== undefined) patch.dataZoom = source.dataZoom
      if (source.grid !== undefined) patch.grid = source.grid
    }
    if (resetDataZoom) {
      patch.dataZoom = []
      if (source.grid !== undefined) patch.grid = source.grid
    }
    if (source.aria !== undefined) patch.aria = source.aria
    for (const key of ['xAxis', 'yAxis', 'radar']) {
      if (source[key] !== undefined) patch[key] = source[key]
    }
    replaceMerge.push('dataset', 'series', 'legend', 'visualMap', 'graphic')
    if (replaceDataZoom && source.dataZoom !== undefined) replaceMerge.push('dataZoom')
    if (refreshHeatmapDataZoom && source.grid !== undefined) replaceMerge.push('grid')
    if (resetDataZoom) {
      replaceMerge.push('dataZoom')
      if (source.grid !== undefined) replaceMerge.push('grid')
    }
  } else if ((change & Change.Selection) !== 0) {
    patch.dataset = source.dataset
    patch.visualMap = source.visualMap ?? []
    replaceMerge.push('dataset', 'visualMap')
    // Scatter label callbacks capture selection; merge fresh callbacks without resetting native series state.
    const labels = (source.series ?? [])
      .filter((series: Record<string, any>) => series.type === 'scatter')
      .map(({ id, label, labelLayout }: Record<string, any>) => ({ id, label, labelLayout }))
    if (labels.length > 0) patch.series = labels
  }
  if ((change & Change.Highlight) !== 0) {
    patch.series = source.series
    if (source.aria !== undefined) patch.aria = source.aria
    if (!replaceMerge.includes('series')) replaceMerge.push('series')
  }
  if ((change & Change.Status) !== 0) {
    patch.title = source.title ?? []
    patch.graphic = source.graphic ?? []
    replaceMerge.push('title', 'graphic')
  }
  if ((change & Change.Context) !== 0) Object.assign(patch, echartsContextPatch(source))
  if ((change & (Change.Context | Change.Status)) !== 0 && source.aria !== undefined) patch.aria = source.aria
  return {
    option: patch,
    settings: { notMerge: false, lazyUpdate: (change & Change.Data) === 0, ...(replaceMerge.length ? { replaceMerge } : {}) },
  }
}

function echartsContextPatch(option: Record<string, any>): Record<string, any> {
  const patch: Record<string, any> = {}
  for (const key of ['backgroundColor', 'color', 'textStyle', 'tooltip', 'legend', 'xAxis', 'yAxis', 'radar', 'graphic', 'title']) {
    if (option[key] !== undefined) patch[key] = option[key]
  }
  if (Array.isArray(option.series)) {
    patch.series = option.series.map((raw: Record<string, any>) => {
      const { data: _data, links: _links, encode: _encode, datasetId: _datasetID, ...series } = raw
      return series
    })
  }
  return patch
}

function hasEChartsDataZoom(option: EChartsOption): boolean {
  const dataZoom = (option as Record<string, any>).dataZoom
  return Array.isArray(dataZoom) ? dataZoom.length > 0 : dataZoom !== undefined
}

function hasEmptyEChartsDataZoom(option: EChartsOption): boolean {
  const dataZoom = (option as Record<string, any>).dataZoom
  return Array.isArray(dataZoom) && dataZoom.length === 0
}

type EChartsFrameChart = Pick<ECharts, 'on' | 'off' | 'getWidth' | 'getHeight'>

export class EChartsReadinessError extends Error {
  constructor(readonly reason: 'timeout' | 'invalid_layout', readonly width: number, readonly height: number) {
    super(reason === 'invalid_layout'
      ? `ECharts cannot render its first frame with invalid layout ${width}x${height}`
      : 'ECharts did not complete its first frame')
    this.name = 'EChartsReadinessError'
  }
}

export function waitForEChartsFrame(chart: EChartsFrameChart, timeoutMs = 5_000, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    let timer: ReturnType<typeof setTimeout> | undefined
    let settled = false
    const cleanup = () => {
      if (timer !== undefined) clearTimeout(timer)
      chart.off('rendered', rendered)
      signal?.removeEventListener('abort', aborted)
    }
    const complete = (action: () => void) => {
      if (settled) return
      settled = true
      cleanup()
      action()
    }
    const rendered = () => {
      const width = chart.getWidth()
      const height = chart.getHeight()
      if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return
      complete(resolve)
    }
    const aborted = () => { complete(resolve) }
    chart.on('rendered', rendered)
    if (signal?.aborted) {
      aborted()
      return
    }
    signal?.addEventListener('abort', aborted, { once: true })
    timer = setTimeout(() => {
      const width = chart.getWidth()
      const height = chart.getHeight()
      complete(() => reject(new EChartsReadinessError(width > 0 && height > 0 ? 'timeout' : 'invalid_layout', width, height)))
    }, timeoutMs)
  })
}
