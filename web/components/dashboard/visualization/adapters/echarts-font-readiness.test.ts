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

function deferredFontLoads(): {
  fonts: FakeFonts
  resolveCue: (faces: readonly unknown[]) => void
  resolveBase: (faces: readonly unknown[]) => void
  rejectBase: (error: Error) => void
} {
  let resolveCue!: (faces: readonly unknown[]) => void
  let resolveBase!: (faces: readonly unknown[]) => void
  let rejectBase!: (error: Error) => void
  const cue = new Promise<readonly unknown[]>((resolve) => { resolveCue = resolve })
  const base = new Promise<readonly unknown[]>((resolve, reject) => {
    resolveBase = resolve
    rejectBase = reject
  })
  return {
    fonts: {
      check: () => true,
      load: (font) => font.includes("'LeapView Chart Cues'") ? cue : base,
    },
    resolveCue,
    resolveBase,
    rejectBase,
  }
}

async function flushFontLoads(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
}

test('ECharts waits for cue and base faces before a deferred cue update draws', async () => {
  const deferred = deferredFontLoads()
  await withDocumentFonts(deferred.fonts, async () => {
    const { handle, calls } = fakeHandle()
    const ordinary = ordinaryFixture()
    handle.mount(ordinary, defaultRendererContext)
    const pending = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await flushFontLoads()
    expect(calls).toHaveLength(1)
    deferred.resolveCue([{}])
    await Promise.resolve()
    expect(calls).toHaveLength(1)
    deferred.resolveBase([{}])
    await pending
    expect(calls).toHaveLength(2)
    expect(calls.at(-1)?.series[0].label.fontFamily).toBe("'LeapView Chart Cues', system-ui")
    handle.dispose()
  })
})

test('ECharts drops a superseded cue update when a non-cue update wins', async () => {
  const deferred = deferredFontLoads()
  await withDocumentFonts(deferred.fonts, async () => {
    const { handle, calls } = fakeHandle()
    const ordinary = ordinaryFixture()
    handle.mount(ordinary, defaultRendererContext)
    const staleCue = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await flushFontLoads()
    const current = structuredClone(ordinary)
    current.dataRevision = 2
    current.dataState.dataRevision = 2
    await handle.update(current, Change.Data, defaultRendererContext)
    expect(calls).toHaveLength(2)
    deferred.resolveCue([{}])
    deferred.resolveBase([{}])
    await staleCue
    expect(calls).toHaveLength(2)
    handle.dispose()
  })
})

test('ECharts drops a deferred cue update after disposal', async () => {
  const deferred = deferredFontLoads()
  await withDocumentFonts(deferred.fonts, async () => {
    const { handle, calls } = fakeHandle()
    handle.mount(ordinaryFixture(), defaultRendererContext)
    const pending = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await flushFontLoads()
    handle.dispose()
    deferred.resolveCue([{}])
    deferred.resolveBase([{}])
    await pending
    expect(calls).toHaveLength(1)
  })
})

test('a failed stale cue load does not reject or erase a newer non-cue render', async () => {
  const deferred = deferredFontLoads()
  await withDocumentFonts(deferred.fonts, async () => {
    const { handle, calls } = fakeHandle()
    const ordinary = ordinaryFixture()
    handle.mount(ordinary, defaultRendererContext)
    const staleCue = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await flushFontLoads()
    const current = structuredClone(ordinary)
    current.dataRevision = 2
    current.dataState.dataRevision = 2
    await handle.update(current, Change.Data, defaultRendererContext)
    deferred.resolveCue([{}])
    deferred.rejectBase(new Error('font unavailable'))
    await expect(staleCue).resolves.toBeUndefined()
    expect(calls).toHaveLength(2)
    handle.dispose()
  })
})

test('a current cue update reports a failed base font without drawing', async () => {
  const deferred = deferredFontLoads()
  await withDocumentFonts(deferred.fonts, async () => {
    const { handle, calls } = fakeHandle()
    handle.mount(ordinaryFixture(), defaultRendererContext)
    const pending = handle.update(cueFixture(), Change.Spec, defaultRendererContext)
    await flushFontLoads()
    deferred.resolveCue([{}])
    deferred.rejectBase(new Error('base font unavailable'))
    await expect(pending).rejects.toThrow('chart base font failed to load')
    expect(calls).toHaveLength(1)
    handle.dispose()
  })
})
