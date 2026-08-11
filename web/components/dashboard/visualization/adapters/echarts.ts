import type { VisualizationEnvelope } from '../../../../generated/visualization'
import type { ECharts, EChartsOption } from 'echarts'
import { Change, defaultRendererContext, normalizeRendererLocale, type RendererAdapter, type RendererContext, type RendererHandle } from '../host-controller'
import { clearInteractionCommand, interactionCommandForRow } from '../interaction-command'
import { projectVisualizationHighlights } from '../highlight'
import { baseOption } from './echarts/common'
import { CategoryColorRegistry, categoryColorRegistryFor } from './echarts/category-colors'
import { cartesianOption } from './echarts/cartesian'
import { hierarchyOption } from './echarts/hierarchy'
import { polarOption } from './echarts/polar'
import { pointOption } from './echarts/point'
import { proportionalCenterText, proportionalOption } from './echarts/proportional'

export { interactionCommandForRow, normalizeRendererLocale }

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
    const rowIndices = seriesRowIndices(envelope, dataset.columns, dataset.rows, item)
    const opacity = (params: { dataIndex?: number }) => {
      if (projection.matchedRows.size === 0) return 0.45
      const rowIndex = params.dataIndex === undefined ? undefined : rowIndices[params.dataIndex]
      return rowIndex !== undefined && projection.matchedRows.has(rowIndex) ? 1 : 0.2
    }
    item.itemStyle = { ...(item.itemStyle ?? {}), opacity }
    item.lineStyle = { ...(item.lineStyle ?? {}), opacity: 0.55 }
  }
  option.aria = { ...(option.aria ?? {}), enabled: true, description: projection.announcement }
}

function seriesRowIndices(
  envelope: VisualizationEnvelope,
  columns: readonly string[],
  rows: readonly (readonly unknown[])[],
  series: Record<string, any>,
): number[] {
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

class EChartsHandle implements RendererHandle {
  private envelope?: VisualizationEnvelope
  private context?: RendererContext
  private disposed = false
  private readiness: Promise<void> = Promise.resolve()
  private readinessAbort?: AbortController

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
    this.chart.setOption(echartsOption(envelope, context, this.categoryColors), { notMerge: true, lazyUpdate: false })
  }

  whenReady(): Promise<void> { return this.readiness }

  update(envelope: VisualizationEnvelope, change: Change, context: RendererContext): void {
    if (this.disposed) return
    this.envelope = envelope
    this.context = context
    const option = echartsOption(envelope, context, this.categoryColors)
    const plan = echartsUpdatePlan(change, option)
    this.chart.setOption(plan.option, plan.settings)
  }

  resize(width: number, height: number): void { this.chart.resize({ width, height, silent: true }) }

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
    this.chart.dispatchAction({ type: 'legendSelect', name: event.name })
    const command = legendSelectionCommand(envelope, event.name)
    if (!command) return
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

export function legendSelectionCommand(envelope: VisualizationEnvelope, categoryName: string) {
  if (envelope.spec.kind !== 'proportional' || envelope.dataState.kind !== 'inline') return undefined
  const ref = envelope.spec.category
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === ref.dataset)
  const categoryIndex = dataset?.columns.indexOf(ref.field) ?? -1
  if (!dataset || categoryIndex < 0) return undefined
  const row = dataset.rows.find((candidate) => String(candidate[categoryIndex]) === categoryName)
  return row ? interactionCommandForRow(envelope, dataset.id, row) : undefined
}

export function brushSelectionCommands(envelope: VisualizationEnvelope, params: unknown) {
  if (envelope.spec.kind !== 'point' || envelope.dataState.kind !== 'inline') return []
  const datasetID = envelope.spec.x.dataset
  const dataset = envelope.dataState.datasets.find((candidate) => candidate.id === datasetID)
  if (!dataset) return []
  const event = params as { batch?: Array<{ selected?: Array<{ dataIndex?: number[] }> }> }
  const indexes = new Set<number>()
  for (const batch of event.batch ?? []) {
    for (const selected of batch.selected ?? []) {
      for (const index of selected.dataIndex ?? []) {
        if (Number.isInteger(index) && index >= 0 && index < dataset.rows.length) indexes.add(index)
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

export function echartsUpdatePlan(change: Change, option: EChartsOption): EChartsUpdatePlan {
  if ((change & Change.Spec) !== 0) {
    return { option: option as Record<string, any>, settings: { notMerge: true, lazyUpdate: false } }
  }
  const source = option as Record<string, any>
  const patch: Record<string, any> = {}
  const replaceMerge: string[] = []
  if ((change & Change.Data) !== 0) {
    patch.dataset = source.dataset
    patch.series = source.series
    patch.visualMap = source.visualMap ?? []
    patch.graphic = source.graphic ?? []
    if (source.aria !== undefined) patch.aria = source.aria
    for (const key of ['xAxis', 'yAxis', 'radar']) {
      if (source[key] !== undefined) patch[key] = source[key]
    }
    replaceMerge.push('dataset', 'series', 'visualMap', 'graphic')
  } else if ((change & Change.Selection) !== 0) {
    patch.dataset = source.dataset
    patch.visualMap = source.visualMap ?? []
    replaceMerge.push('dataset', 'visualMap')
  }
  if ((change & Change.Status) !== 0) {
    patch.title = source.title ?? []
    patch.graphic = source.graphic ?? []
    replaceMerge.push('title', 'graphic')
  }
  if ((change & Change.Context) !== 0) Object.assign(patch, echartsContextPatch(source))
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
