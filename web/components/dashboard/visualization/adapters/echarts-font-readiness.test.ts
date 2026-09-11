import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { Change, defaultRendererContext } from '../host-controller'
import { EChartsHandle } from './echarts'
import { CategoryColorRegistry } from './echarts/category-colors'
import { cartesianFixture, proportionalFixture } from './echarts-test-fixtures'

type FakeFonts = {
  check(font: string, text?: string): boolean
  load(font: string, text?: string): Promise<readonly unknown[]>
}

function ordinaryFixture(): VisualizationEnvelope {
  const envelope = cartesianFixture('line')
  if (envelope.spec.kind !== 'cartesian') throw new Error('Expected cartesian fixture')
  envelope.spec.presentation.dataZoom = false
  return envelope
}

function cueFixture(): VisualizationEnvelope {
  const envelope = proportionalFixture('donut')
  if (envelope.spec.kind !== 'proportional') throw new Error('Expected proportional fixture')
  envelope.spec.conditionalFormatting = [{
    id: 'value-status', target: 'mark_fill', field: { dataset: 'primary', field: 'value' },
    rule: { kind: 'rules', rules: [{ operator: 'greater_than', value: 0, style: { icon: 'circle' } }], nullStyle: {}, defaultStyle: {} },
  }]
  return envelope
}

function fakeHandle(): { handle: EChartsHandle; calls: Record<string, any>[] } {
  let current: Record<string, any> = {}
  const calls: Record<string, any>[] = []
  const chart = {
    on() {}, off() {}, resize() {}, dispose() {},
    setOption(option: Record<string, any>) { calls.push(option); current = { ...current, ...option } },
    getOption() { return current },
  }
  return { calls, handle: new EChartsHandle({}, {}, chart as any, new CategoryColorRegistry()) }
}

async function withDocumentFonts<T>(fonts: FakeFonts, callback: () => Promise<T>): Promise<T> {
  const previous = Object.getOwnPropertyDescriptor(globalThis, 'document')
  Object.defineProperty(globalThis, 'document', { configurable: true, value: { fonts } })
  try {
    return await callback()
  } finally {
    if (previous) Object.defineProperty(globalThis, 'document', previous)
    else Reflect.deleteProperty(globalThis, 'document')
  }
}

test('ECharts waits for the cue face before a deferred cue update draws', async () => {
  let resolveLoad!: (faces: readonly unknown[]) => void
  const fonts: FakeFonts = { check: () => true, load: () => new Promise((resolve) => { resolveLoad = resolve }) }
  await withDocumentFonts(fonts, async () => {
    const { handle, calls } = fakeHandle()
    const ordinary = ordinaryFixture()
    handle.mount(ordinary, defaultRendererContext)
    const pending = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await Promise.resolve()
    expect(calls).toHaveLength(1)
    resolveLoad([{}])
    await pending
    expect(calls).toHaveLength(2)
    expect(calls.at(-1)?.series[0].label.fontFamily).toBe("'LeapView Chart Cues', system-ui")
    handle.dispose()
  })
})

test('ECharts drops a superseded cue update when a non-cue update wins', async () => {
  let resolveLoad!: (faces: readonly unknown[]) => void
  const fonts: FakeFonts = { check: () => true, load: () => new Promise((resolve) => { resolveLoad = resolve }) }
  await withDocumentFonts(fonts, async () => {
    const { handle, calls } = fakeHandle()
    const ordinary = ordinaryFixture()
    handle.mount(ordinary, defaultRendererContext)
    const staleCue = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await Promise.resolve()
    const current = structuredClone(ordinary)
    current.dataRevision = 2
    current.dataState.dataRevision = 2
    await handle.update(current, Change.Data, defaultRendererContext)
    expect(calls).toHaveLength(2)
    resolveLoad([{}])
    await staleCue
    expect(calls).toHaveLength(2)
    handle.dispose()
  })
})

test('ECharts drops a deferred cue update after disposal', async () => {
  let resolveLoad!: (faces: readonly unknown[]) => void
  const fonts: FakeFonts = { check: () => true, load: () => new Promise((resolve) => { resolveLoad = resolve }) }
  await withDocumentFonts(fonts, async () => {
    const { handle, calls } = fakeHandle()
    handle.mount(ordinaryFixture(), defaultRendererContext)
    const pending = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await Promise.resolve()
    handle.dispose()
    resolveLoad([{}])
    await pending
    expect(calls).toHaveLength(1)
  })
})

test('a failed stale cue load does not reject or erase a newer non-cue render', async () => {
  let rejectLoad!: (error: Error) => void
  const fonts: FakeFonts = { check: () => false, load: () => new Promise((_, reject) => { rejectLoad = reject }) }
  await withDocumentFonts(fonts, async () => {
    const { handle, calls } = fakeHandle()
    const ordinary = ordinaryFixture()
    handle.mount(ordinary, defaultRendererContext)
    const staleCue = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await Promise.resolve()
    const current = structuredClone(ordinary)
    current.dataRevision = 2
    current.dataState.dataRevision = 2
    await handle.update(current, Change.Data, defaultRendererContext)
    rejectLoad(new Error('font unavailable'))
    await expect(staleCue).resolves.toBeUndefined()
    expect(calls).toHaveLength(2)
    handle.dispose()
  })
})
