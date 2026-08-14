import { expect, test } from 'bun:test'
import type { DashboardVisualizationSignal } from '../../../generated/signals'
import { DashboardVisualizationSignalDecoder } from './signal-envelope'

test('dashboard visualization signals keep large frames opaque and reconstruct canonical envelopes', () => {
  const rows = Array.from({ length: 20_000 }, (_, index) => [`zip-${index}`, index])
  const signal = visualizationSignal({
    specRevision: 'spec-1', dataRevision: 1, generation: 1, kind: 'inline',
    datasets: [{ id: 'primary', specRevision: 'spec-1', dataRevision: 1, generation: 1, columns: ['zip', 'value'], rows, completeness: 'complete' }],
  })
  const decoder = new DashboardVisualizationSignalDecoder()

  const envelope = decoder.decode(signal)

  expect(signal.dataState.schemaVersion).toBe(1)
  expect(typeof signal.dataState.payload).toBe('string')
  expect((envelope?.dataState as any).datasets[0].rows).toHaveLength(20_000)
  expect(envelope).not.toHaveProperty('filterRevision')
  expect(envelope).not.toHaveProperty('interactionRevision')
  expect(envelope).not.toHaveProperty('servingStateID')
})

test('dashboard visualization signal decoder reuses data on status-only patches', () => {
  const decoder = new DashboardVisualizationSignalDecoder()
  const state = {
    specRevision: 'spec-1', dataRevision: 1, generation: 1, kind: 'inline',
    datasets: [{ id: 'primary', specRevision: 'spec-1', dataRevision: 1, generation: 1, columns: ['value'], rows: [[1]], completeness: 'complete' }],
  }
  const ready = decoder.decode(visualizationSignal(state))
  const loading = decoder.decode({ ...visualizationSignal(state), status: { kind: 'loading' } })

  expect(loading?.dataState).toBe(ready?.dataState)
  expect(loading?.status.kind).toBe('loading')
})

test('dashboard visualization signal decoder fails closed on transport and payload mismatches', () => {
  const decoder = new DashboardVisualizationSignalDecoder()
  const state = {
    specRevision: 'spec-1', dataRevision: 1, generation: 1, kind: 'spatial_tiled',
    schema: { id: 'primary', fields: [] }, cardinality: { kind: 'exact', count: 0 },
    extent: { west: -1, south: -1, east: 1, north: 1 }, rawDomains: [], aggregateDomains: [], tileURL: '/tiles/{z}/{x}/{y}.mvt', minimumZoom: 0, maximumZoom: 18, rawMinimumZoom: 10, featureCap: 5000, maximumTileBytes: 524288,
  }
  const valid = visualizationSignal(state)

  expect(decoder.decode({ ...valid, dataState: { ...valid.dataState, schemaVersion: 4 as 1 } })).toBeUndefined()
  expect(decoder.decode({ ...valid, dataState: { ...valid.dataState, encoding: 'cbor' as 'json' } })).toBeUndefined()
  expect(decoder.decode({ ...valid, dataState: { ...valid.dataState, kind: 'inline' } })).toBeUndefined()
  expect(decoder.decode({ ...valid, dataState: { ...valid.dataState, dataRevision: 2 } })).toBeUndefined()
  expect(decoder.decode({ ...valid, dataState: { ...valid.dataState, payload: '{invalid' } })).toBeUndefined()
})

test('dashboard visualization signal decoder ignores incomplete unknown visual patches', () => {
  const decoder = new DashboardVisualizationSignalDecoder()

  expect(decoder.decodeAll({ off_page: { status: { kind: 'error', message: 'not on page' } } as any })).toEqual({})
})

function visualizationSignal(state: Record<string, unknown>): DashboardVisualizationSignal {
  const kind = state.kind as 'inline' | 'windowed' | 'spatial_tiled'
  const specRevision = state.specRevision as string
  const dataRevision = state.dataRevision as number
  const generation = state.generation as number
  return {
    schemaVersion: 9,
    visualID: 'map',
    rendererID: 'maplibre',
    specRevision: 'spec-1',
    dataRevision: 1,
    spec: {
      kind: 'kpi', title: 'Map', datasets: [], dataBudget: { maxRows: 20_000, requiredCompleteness: 'complete' },
      accessibility: { title: 'Map', description: 'Map' }, interactions: [],
      value: { dataset: 'primary', field: 'value' }, presentation: { trend: 'neutral' },
    },
    dataState: { schemaVersion: 1, encoding: 'json', kind, specRevision, dataRevision, generation, payload: JSON.stringify(state) },
    selection: [],
    status: { kind: 'ready' },
    diagnostics: [],
    servingStateID: 'serving-1',
    streamGeneration: generation,
    filterRevision: 0,
    consumerIdentity: 'overview/map',
  }
}
