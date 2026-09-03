import { expect, test } from 'bun:test'

import type { VisualizationEnvelope } from '../../../generated/visualization'
import { Change, currentVisualizationSchemaVersion, defaultRendererContext, primerCategoricalPalette, RendererRegistry, VisualizationController, type RendererHandle } from './host-controller'

test('categorical palette preserves the established Primer token order', () => {
  expect(primerCategoricalPalette.map(({ name }) => name)).toEqual([
    'blue', 'orange', 'green', 'pink', 'brown', 'plum', 'teal', 'yellow', 'red',
    'gray', 'olive', 'pine', 'auburn', 'lemon', 'purple', 'coral', 'lime',
  ])
  expect(primerCategoricalPalette.every(({ token }) => token.startsWith('--data-') && token.endsWith('-color-emphasis'))).toBe(true)
  expect(defaultRendererContext.colors.data).toEqual(primerCategoricalPalette.map(({ fallback }) => fallback))
})

function envelope(dataRevision: number, specRevision = 'sha256:spec', rendererID = 'test'): VisualizationEnvelope {
  return {
    schemaVersion: currentVisualizationSchemaVersion,
    visualID: 'revenue',
    rendererID,
    specRevision,
    dataRevision,
    spec: {
      kind: 'kpi',
      title: 'Revenue',
      datasets: [{ id: 'primary', fields: [{ id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Revenue' }] }],
      dataBudget: { maxRows: 1, requiredCompleteness: 'complete' },
      accessibility: { title: 'Revenue', description: 'Current revenue' },
      interactions: [],
      value: { dataset: 'primary', field: 'value' },
      presentation: { trend: 'neutral' },
    },
    dataState: { kind: 'inline', specRevision, dataRevision, generation: 1, datasets: [] },
    selection: [],
    status: { kind: 'ready' },
    diagnostics: [],
  }
}

function envelopeWithRows(dataRevision: number, rows: unknown[][], status: VisualizationEnvelope['status']): VisualizationEnvelope {
  const value = envelope(dataRevision)
  return {
    ...value,
    dataState: {
      kind: 'inline', specRevision: value.specRevision, dataRevision, generation: 1,
      datasets: [{
        id: 'primary', specRevision: value.specRevision, dataRevision, generation: 1,
        columns: ['value'], rows, completeness: rows.length > 0 ? 'complete' : 'empty',
      }],
    },
    status,
  }
}

test('controller mounts lazily, rejects stale revisions, and disposes deterministically', async () => {
  const updates: Change[] = []
  const observations: string[] = []
  let mounts = 0
  let disposals = 0
  const handle: RendererHandle = {
    update: (_value, change) => updates.push(change),
    resize: () => {},
    snapshot: async () => new Blob(),
    dispose: () => { disposals++ },
  }
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({ mount: () => { mounts++; return handle } }),
  })
  const controller = new VisualizationController(registry, {} as HTMLElement, undefined, (value) => observations.push(value.stage))

  expect(await controller.apply(envelope(2))).toBe(true)
  expect(await controller.apply(envelope(1))).toBe(false)
  expect(await controller.apply(envelope(3))).toBe(true)
  expect(mounts).toBe(1)
  expect(updates).toEqual([Change.Data])
  expect(observations).toContain('renderer_load')
  expect(observations).toContain('mount')
  expect(observations).toContain('stale_result_drop')
  expect(observations).toContain('update')

  controller.dispose()
  controller.dispose()
  expect(disposals).toBe(1)
  expect(observations).toContain('dispose')
})

test('controller coalesces resize and applies the latest size after a lazy mount', async () => {
  const sizes: number[][] = []
  let release!: (adapter: { mount: () => RendererHandle }) => void
  const loading = new Promise<{ mount: () => RendererHandle }>((resolve) => { release = resolve })
  const handle: RendererHandle = {
    update: () => {}, resize: (...size) => sizes.push(size), snapshot: async () => new Blob(), dispose: () => {},
  }
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: () => loading,
  })
  const controller = new VisualizationController(registry, {} as HTMLElement)
  const applying = controller.apply(envelope(1))
  controller.resize(100, 80, 1)
  controller.resize(240, 160, 2)
  release({ mount: () => handle })
  await applying
  await Promise.resolve()
  expect(sizes).toEqual([[240, 160, 2]])
})

test('controller sends context-only changes without replacing visualization data', async () => {
  const updates: Change[] = []
  const handle: RendererHandle = {
    update: (_value, change) => updates.push(change), resize: () => {}, snapshot: async () => new Blob(), dispose: () => {},
  }
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({ mount: () => handle }),
  })
  const controller = new VisualizationController(registry, {} as HTMLElement)
  await controller.apply(envelope(1), defaultRendererContext)
  await controller.apply(envelope(1), { ...defaultRendererContext, theme: 'dark' })
  expect(updates).toEqual([Change.Context])
})

test('controller disposes a failed update and remounts the same envelope on retry', async () => {
  const observations: Array<{ stage: string; visualID?: string }> = []
  let mounts = 0
  let disposals = 0
  let failUpdate = true
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({
      mount: () => {
        mounts++
        return {
          update: () => {
            if (failUpdate) {
              failUpdate = false
              throw new Error('renderer update failed')
            }
          },
          resize: () => {},
          snapshot: async () => new Blob(),
          dispose: () => { disposals++ },
        }
      },
    }),
  })
  const controller = new VisualizationController(registry, {} as HTMLElement, undefined, (value) => observations.push(value))

  await controller.apply(envelope(1))
  await expect(controller.apply(envelope(2))).rejects.toThrow('renderer update failed')

  expect(controller.envelope?.dataRevision).toBe(1)
  expect(disposals).toBe(1)
  expect(await controller.apply(envelope(2))).toBe(true)
  expect(controller.envelope?.dataRevision).toBe(2)
  expect(mounts).toBe(2)
  expect(observations.find((value) => value.stage === 'adapter_error')?.visualID).toBe('revenue')
})

test('controller updates data when a loading envelope is populated at the same revision', async () => {
  const updates: Change[] = []
  const handle: RendererHandle = {
    update: (_value, change) => updates.push(change), resize: () => {}, snapshot: async () => new Blob(), dispose: () => {},
  }
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({ mount: () => handle }),
  })
  const controller = new VisualizationController(registry, {} as HTMLElement)
  await controller.apply(envelopeWithRows(1, [], { kind: 'loading' }))
  await controller.apply(envelopeWithRows(1, [[42]], { kind: 'ready' }))

  expect(updates).toEqual([Change.Data | Change.Status])
})

test('controller does not serialize an unchanged shared data frame for status-only updates', async () => {
  const updates: Change[] = []
  let serializations = 0
  const handle: RendererHandle = {
    update: (_value, change) => updates.push(change), resize: () => {}, snapshot: async () => new Blob(), dispose: () => {},
  }
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({ mount: () => handle }),
  })
  const controller = new VisualizationController(registry, {} as HTMLElement)
  const initial = envelopeWithRows(1, [[42]], { kind: 'loading' })
  const sharedDataState = initial.dataState as VisualizationEnvelope['dataState'] & { toJSON?: () => unknown }
  sharedDataState.toJSON = () => {
    serializations++
    return { kind: 'inline' }
  }

  await controller.apply(initial)
  await controller.apply({ ...initial, status: { kind: 'ready' } })

  expect(updates).toEqual([Change.Status])
  expect(serializations).toBe(0)
})

test('controller transfers renderer view state across a lazy focus mount', async () => {
  const camera = { center: [-46.63, -23.55], zoom: 7 }
  const restored: unknown[] = []
  const handle: RendererHandle = {
    update: () => {}, resize: () => {}, snapshot: async () => new Blob(), dispose: () => {},
    captureViewState: () => camera,
    restoreViewState: (state) => { restored.push(state) },
  }
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({ mount: () => handle }),
  })
  const controller = new VisualizationController(registry, {} as HTMLElement)
  controller.restoreViewState(camera)
  await controller.apply(envelope(1))

  expect(restored).toEqual([camera])
  expect(controller.captureViewState()).toBe(camera)
})

test('registry rejects duplicate IDs and unsupported capabilities fail closed', async () => {
  const registry = new RendererRegistry()
  const registration = {
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['table'] as const,
    capabilities: { snapshot: false, windowed: true, interactive: true },
    load: async () => ({ mount: () => { throw new Error('not reached') } }),
  }
  registry.register(registration)
  expect(() => registry.register(registration)).toThrow()
  expect(() => registry.register({
    ...registration,
    id: 'future',
    schemaVersion: (currentVisualizationSchemaVersion + 1) as typeof currentVisualizationSchemaVersion,
  })).toThrow(new RegExp(`schema version ${currentVisualizationSchemaVersion}`))

  const controller = new VisualizationController(registry, {} as HTMLElement)
  await expect(controller.apply(envelope(1))).rejects.toThrow(/does not support kind/)
})

test('controller serializes newer envelopes behind an asynchronous mount', async () => {
  let release!: (handle: RendererHandle) => void
  const mounted: number[] = []
  const updated: number[] = []
  const disposed: number[] = []
  const handle: RendererHandle = {
    update: (value) => { updated.push(value.dataRevision) }, resize: () => {}, snapshot: async () => new Blob(['mounted']), dispose: () => { disposed.push(1) },
  }
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({ mount: (_container, value) => {
      mounted.push(value.dataRevision)
      return new Promise<RendererHandle>((resolve) => { release = resolve })
    } }),
  })
  const controller = new VisualizationController(registry, {} as HTMLElement)
  const first = controller.apply(envelope(1))
  await Promise.resolve()
  const second = controller.apply(envelope(2))
  await Promise.resolve()
  await Promise.resolve()

  expect(mounted).toEqual([1])
  release(handle)
  await Promise.all([first, second])

  expect(mounted).toEqual([1])
  expect(updated).toEqual([2])
  expect(await (await controller.snapshot()).text()).toBe('mounted')
  expect(disposed).toEqual([])
})

test('repeated mount and dispose cycles release every renderer exactly once', async () => {
  let mounts = 0
  let disposals = 0
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'], capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({ mount: () => {
      mounts++
      return { update: () => {}, resize: () => {}, snapshot: async () => new Blob(), dispose: () => { disposals++ } }
    } }),
  })
  for (let cycle = 0; cycle < 100; cycle++) {
    const controller = new VisualizationController(registry, {} as HTMLElement)
    await controller.apply(envelope(cycle + 1))
    controller.dispose()
    controller.dispose()
  }
  expect(mounts).toBe(100)
  expect(disposals).toBe(100)
})

test('controller owns and cancels a renderer while first-frame readiness is pending', async () => {
  let releaseReady = () => {}
  let disposals = 0
  const resizes: Array<readonly [number, number, number]> = []
  const handle = {
    whenReady: () => new Promise<void>((resolve) => { releaseReady = resolve }),
    update: () => {},
    resize: (width: number, height: number, ratio: number) => { resizes.push([width, height, ratio]) },
    snapshot: async () => new Blob(),
    dispose: () => {
      disposals++
      releaseReady()
    },
  }
  const registry = new RendererRegistry()
  registry.register({
    id: 'test', version: '1.0.0', schemaVersion: currentVisualizationSchemaVersion, kinds: ['kpi'],
    capabilities: { snapshot: true, windowed: false, interactive: false },
    load: async () => ({ mount: () => handle }),
  })
  const controller = new VisualizationController(registry, {} as HTMLElement)
  controller.resize(640, 360, 2)
  let settled = false
  const applying = controller.apply(envelope(1)).then((applied) => {
    settled = true
    return applied
  })
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()

  expect(resizes).toEqual([[640, 360, 2]])
  expect(settled).toBe(false)

  controller.dispose()
  expect(await applying).toBe(false)
  expect(disposals).toBe(1)
})
