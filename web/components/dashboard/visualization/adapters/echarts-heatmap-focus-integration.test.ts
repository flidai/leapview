import { afterEach, beforeEach, expect, test } from 'bun:test'
import * as echarts from 'echarts'

import { Change, defaultRendererContext } from '../host-controller'
import { CategoryColorRegistry } from './echarts/category-colors'
import { EChartsHandle } from './echarts'
import { cartesianFixture } from './echarts-test-fixtures'

class FocusShadowRoot {}

let shadowRootDescriptor: PropertyDescriptor | undefined

beforeEach(() => {
  shadowRootDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'ShadowRoot')
  Object.defineProperty(globalThis, 'ShadowRoot', { configurable: true, writable: true, value: FocusShadowRoot })
})

afterEach(() => {
  if (shadowRootDescriptor) Object.defineProperty(globalThis, 'ShadowRoot', shadowRootDescriptor)
  else Reflect.deleteProperty(globalThis, 'ShadowRoot')
  shadowRootDescriptor = undefined
})

type FocusState = { focused: boolean }

const heatmapRows = (offset = 0): unknown[][] => [
  ['A', 'R1', offset + 1], ['B', 'R1', offset + 2], ['C', 'R1', offset + 3], ['D', 'R1', offset + 4],
  ['E', 'R1', offset + 5], ['F', 'R1', offset + 6], ['G', 'R1', offset + 7], ['H', 'R1', offset + 8],
]

function heatmapEnvelope(rows = heatmapRows()): any {
  const envelope = cartesianFixture('heatmap', ['label', 'row', 'value']) as any
  envelope.spec.presentation.dataZoom = true
  envelope.dataState.datasets[0].rows = rows
  return envelope
}

function focusedContainer(state: FocusState): HTMLElement {
  const root = new FocusShadowRoot() as any
  root.host = { getAttribute: (name: string) => name === 'slot' && state.focused ? 'focus-visual' : null }
  return { getRootNode: () => state.focused ? root : { host: root.host } } as any
}

function dataZoomRange(chart: echarts.EChartsType): { start?: number; end?: number; startValue?: unknown; endValue?: unknown; disabled?: boolean } {
  const value = ((chart.getOption() as any).dataZoom ?? [])[0] ?? {}
  return { start: value.start, end: value.end, startValue: value.startValue, endValue: value.endValue, disabled: value.disabled }
}

function mountedHeatmap(): { handle: EChartsHandle; chart: echarts.EChartsType; state: FocusState; envelope: any } {
  const state: FocusState = { focused: false }
  const chart = echarts.init(null, null, { renderer: 'svg', ssr: true, width: 640, height: 360 })
  const handle = new EChartsHandle(focusedContainer(state), {} as any, chart, new CategoryColorRegistry())
  const envelope = heatmapEnvelope()
  handle.mount(envelope, defaultRendererContext)
  handle.resize(640, 360)
  return { handle, chart, state, envelope }
}

function selectCompactRange(chart: echarts.EChartsType): ReturnType<typeof dataZoomRange> {
  chart.dispatchAction({ type: 'dataZoom', dataZoomIndex: 0, startValue: 'B', endValue: 'D' })
  return dataZoomRange(chart)
}

test('heatmap focus restores the pre-focus compact range after a non-empty data refresh', () => {
  const { handle, chart, state, envelope } = mountedHeatmap()
  try {
    const compact = selectCompactRange(chart)
    state.focused = true
    handle.resize(640, 360)
    expect(dataZoomRange(chart)).toMatchObject({ start: 0, end: 100, disabled: true })

    const updated = structuredClone(envelope)
    updated.dataRevision = 2
    updated.dataState.datasets[0].rows = heatmapRows(100)
    handle.update(updated, Change.Data, defaultRendererContext)

    state.focused = false
    handle.resize(640, 360)
    const restored = dataZoomRange(chart)
    expect(restored.start).toBeCloseTo(compact.start!, 8)
    expect(restored.end).toBeCloseTo(compact.end!, 8)
    expect(restored.startValue).toBe(1)
    expect(restored.endValue).toBe(3)
    expect(restored.disabled).toBe(false)
  } finally {
    handle.dispose()
  }
})

test('empty heatmap data clears focus zoom instead of restoring a stale range', () => {
  const { handle, chart, state, envelope } = mountedHeatmap()
  try {
    selectCompactRange(chart)
    state.focused = true
    handle.resize(640, 360)

    const empty = structuredClone(envelope)
    empty.dataRevision = 2
    empty.dataState.datasets[0].rows = []
    handle.update(empty, Change.Data, defaultRendererContext)

    expect(((chart.getOption() as any).dataZoom ?? [])).toHaveLength(0)
    state.focused = false
    handle.resize(640, 360)
    expect(((chart.getOption() as any).dataZoom ?? [])).toHaveLength(0)
  } finally {
    handle.dispose()
  }
})

test('removing heatmap data zoom clears focus state and controls', () => {
  const { handle, chart, state, envelope } = mountedHeatmap()
  try {
    selectCompactRange(chart)
    state.focused = true
    handle.resize(640, 360)

    const withoutZoom = structuredClone(envelope)
    withoutZoom.spec.presentation.dataZoom = false
    withoutZoom.specRevision = 'sha256:without-data-zoom'
    handle.update(withoutZoom, Change.Spec, defaultRendererContext)

    expect(((chart.getOption() as any).dataZoom ?? [])).toHaveLength(0)
    state.focused = false
    handle.resize(640, 360)
    expect(((chart.getOption() as any).dataZoom ?? [])).toHaveLength(0)
  } finally {
    handle.dispose()
  }
})

test('compatible theme updates preserve a compact heatmap range', () => {
  const { handle, chart, envelope } = mountedHeatmap()
  try {
    const compact = selectCompactRange(chart)
    const darkContext = {
      ...defaultRendererContext,
      theme: 'dark' as const,
      colors: { ...defaultRendererContext.colors, foreground: '#f0f6fc', surface: '#0d1117' },
    }
    handle.update(structuredClone(envelope), Change.Context, darkContext)
    const restored = dataZoomRange(chart)
    expect(restored.start).toBeCloseTo(compact.start!, 8)
    expect(restored.end).toBeCloseTo(compact.end!, 8)
    expect(restored.startValue).toBe(1)
    expect(restored.endValue).toBe(3)
  } finally {
    handle.dispose()
  }
})

test('responsive compact threshold changes preserve a selected heatmap range', () => {
  const { handle, chart } = mountedHeatmap()
  try {
    const compact = selectCompactRange(chart)
    handle.resize(390, 240)
    const narrow = dataZoomRange(chart)
    expect(narrow.start).toBeCloseTo(compact.start!, 8)
    expect(narrow.end).toBeCloseTo(compact.end!, 8)
    expect(narrow.startValue).toBe(1)
    expect(narrow.endValue).toBe(3)

    handle.resize(640, 360)
    const restored = dataZoomRange(chart)
    expect(restored.start).toBeCloseTo(compact.start!, 8)
    expect(restored.end).toBeCloseTo(compact.end!, 8)
    expect(restored.startValue).toBe(1)
    expect(restored.endValue).toBe(3)
  } finally {
    handle.dispose()
  }
})
