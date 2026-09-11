import { expect, test } from 'bun:test'
import type { DataExploreResultSignal, DataExploreStatusSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DataExplorerClientState } from './data-explorer-client'
import { emptyDataExploreCommand } from './data-explorer-spec'
import { headers } from '../shared/command'

function memoryStorage(): Storage {
  const values = new Map<string, string>()
  return {
    get length() { return values.size }, clear: () => values.clear(),
    getItem: (key) => values.get(key) ?? null, key: (index) => [...values.keys()][index] ?? null,
    removeItem: (key) => values.delete(key), setItem: (key, value) => values.set(key, value),
  }
}

const command = { ...emptyDataExploreCommand, requestSeq: 1, spec: { ...emptyDataExploreCommand.spec, modelId: 'sales', datasetId: 'orders' } }
const successfulStatus: DataExploreStatusSignal = { loading: false, requestSeq: 1, stale: false, state: 'success' }
const result = (requestSeq: number, rows: Record<string, unknown>[]): DataExploreResultSignal => ({ columns: [{ key: 'status', label: 'Status' }], rows, rowsReturned: rows.length, durationMs: 1, requestSeq, truncated: false, warnings: [] })
const envelope = { spec: { kind: 'table' } } as VisualizationEnvelope

test('retains visualization envelopes with the last good result and clears them across semantic contexts', () => {
  const state = new DataExplorerClientState()
  state.semanticViews(command, result(1, [{ status: 'paid' }]), { table: envelope }, 'table', 'table', successfulStatus, { projectId: 'p', generationId: 'g1' })
  const retained = state.semanticViews(command, result(2, []), {}, 'table', 'table', { loading: false, requestSeq: 2, stale: false, state: 'error', error: 'failed' }, { projectId: 'p', generationId: 'g1' })
  expect(retained.views.table).toBe(envelope)

  const nextContext = state.semanticViews(command, result(1, []), {}, 'table', 'table', successfulStatus, { projectId: 'p', generationId: 'g2' })
  expect(nextContext.views).toEqual({})
})

test('does not replace aligned last-good caches with stale, active, failed, or mismatched responses', () => {
  const state = new DataExplorerClientState()
  const context = { projectId: 'p', generationId: 'g1' }
  const goodCommand = { ...command, requestSeq: 1 }
  const good = result(1, [{ status: 'paid' }])
  const originalViews = { table: envelope }
  state.semanticViews(goodCommand, good, originalViews, 'table', 'table', successfulStatus, context)

  const cases: Array<{ name: string; command: typeof goodCommand; result: DataExploreResultSignal; status: DataExploreStatusSignal; executionState?: 'pending' | 'running' }> = [
    { name: 'stale', command: { ...goodCommand, requestSeq: 2 }, result: result(2, [{ status: 'stale' }]), status: { loading: false, requestSeq: 2, stale: true, state: 'stale' } },
    { name: 'loading', command: { ...goodCommand, requestSeq: 3 }, result: result(3, [{ status: 'loading' }]), status: { loading: true, requestSeq: 3, stale: false, state: 'loading' } },
    { name: 'pending', command: { ...goodCommand, requestSeq: 4 }, result: result(4, [{ status: 'pending' }]), status: { ...successfulStatus, requestSeq: 4 }, executionState: 'pending' },
    { name: 'running', command: { ...goodCommand, requestSeq: 5 }, result: result(5, [{ status: 'running' }]), status: { ...successfulStatus, requestSeq: 5 }, executionState: 'running' },
    { name: 'cancelled', command: { ...goodCommand, requestSeq: 6 }, result: result(6, [{ status: 'cancelled' }]), status: { loading: false, requestSeq: 6, stale: false, state: 'cancelled' } },
    { name: 'error', command: { ...goodCommand, requestSeq: 7 }, result: result(7, [{ status: 'error' }]), status: { loading: false, requestSeq: 7, stale: false, state: 'error', error: 'failed' } },
    // The response and status agree with one another, but belong to an older
    // command sequence and therefore cannot become the last-good result.
    { name: 'sequence-mismatch', command: { ...goodCommand, requestSeq: 8 }, result: result(7, [{ status: 'misaligned' }]), status: { loading: false, requestSeq: 7, stale: false, state: 'success' } },
  ]

  for (const candidate of cases) {
    const retained = state.semanticViews(candidate.command, candidate.result, { table: { ...envelope } }, 'table', 'table', candidate.status, context, candidate.executionState)
    expect(retained.views.table, candidate.name).toBe(envelope)
    expect(state.semanticResult(candidate.command, candidate.result, candidate.status, context, candidate.executionState).rows).toEqual([{ status: 'paid' }])
  }
})

test('keeps result and view caches aligned after a higher-sequence stale response then failure', () => {
  const state = new DataExplorerClientState()
  const context = { projectId: 'p', generationId: 'g2' }
  const goodCommand = { ...command, requestSeq: 10 }
  const goodViews = { table: envelope }
  state.semanticViews(goodCommand, result(10, [{ status: 'good' }]), goodViews, 'table', 'table', { ...successfulStatus, requestSeq: 10 }, context)

  const staleViews = { table: { ...envelope, visualID: 'stale-table' } }
  const stale = state.semanticViews(
    { ...goodCommand, requestSeq: 11 }, result(11, [{ status: 'stale' }]), staleViews, 'table', 'table',
    { loading: false, requestSeq: 11, stale: true, state: 'stale' }, context,
  )
  expect(stale.views.table).toBe(envelope)

  const failedViews = { table: { ...envelope, visualID: 'failed-table' } }
  const failed = state.semanticViews(
    { ...goodCommand, requestSeq: 12 }, result(12, [{ status: 'failed' }]), failedViews, 'table', 'table',
    { loading: false, requestSeq: 12, stale: false, state: 'error', error: 'query failed' }, context,
  )
  expect(failed.views.table).toBe(envelope)
  expect(state.semanticResult({ ...goodCommand, requestSeq: 12 }, result(12, [{ status: 'failed' }]), { loading: false, requestSeq: 12, stale: false, state: 'error', error: 'query failed' }, context).rows).toEqual([{ status: 'good' }])
})

test('preserves semantic sequence across same-tab navigation before Analyze configure', () => {
  const storage = memoryStorage()
  const clientID = 'same-tab-navigation'
  const firstPage = new DataExplorerClientState(storage)
  const retryRequestSeq = firstPage.nextRequestSequence(clientID, 0)
  const resetRequestSeq = firstPage.nextRequestSequence(clientID, retryRequestSeq)
  expect(resetRequestSeq).toBeGreaterThan(retryRequestSeq)

  // A full page navigation remounts the component and hydrates the URL with a
  // fresh command baseline, while sessionStorage retains the tab identity.
  const navigatedPage = new DataExplorerClientState(storage)
  const analyzeConfigureRequestSeq = navigatedPage.nextRequestSequence(clientID, 0)
  expect(analyzeConfigureRequestSeq).toBeGreaterThan(resetRequestSeq)
  expect(analyzeConfigureRequestSeq).not.toBe(0)
})

test('adopts an explicit command sequence without double incrementing it', () => {
  const storage = memoryStorage()
  const state = new DataExplorerClientState(storage)
  const explicitRequestSeq = state.nextRequestSequence('explicit-sequence', 1000)

  expect(explicitRequestSeq).toBe(1000)
  expect(state.nextRequestSequence('explicit-sequence', 1001)).toBe(1001)
  expect(state.nextRequestSequence('explicit-sequence', 0)).toBe(1002)
})

test('keeps suggestion sequencing independent from semantic command storage', () => {
  const storage = memoryStorage()
  const clientID = 'independent-lanes'
  const state = new DataExplorerClientState(storage)
  const semanticRequestSeq = state.nextRequestSequence(clientID, 4)
  const suggestionRequestSeq = state.nextSuggestionSequence(clientID)

  expect(suggestionRequestSeq).toBeGreaterThan(0)
  expect(storage.getItem('leapview-data-explorer-request-seq')).toBe(String(semanticRequestSeq))
  expect(state.nextRequestSequence(clientID, 0)).toBe(semanticRequestSeq + 1)
})

test('ignores malformed persisted semantic sequence values', () => {
  const storage = memoryStorage()
  const clientID = 'malformed-sequence'
  storage.setItem('leapview-data-explorer-request-seq', 'not-a-sequence')
  const state = new DataExplorerClientState(storage)

  expect(state.nextRequestSequence(clientID, 0)).toBeGreaterThan(0)
})

test('keeps hydrated command and request-header identities aligned', () => {
  const storage = memoryStorage()
  const clientID = 'pre-existing-tab'
  storage.setItem('leapview-data-explorer-client', clientID)
  const target = globalThis as typeof globalThis & { sessionStorage?: Storage }
  const previous = Object.getOwnPropertyDescriptor(target, 'sessionStorage')
  Object.defineProperty(target, 'sessionStorage', { configurable: true, get: () => storage })
  try {
    const state = new DataExplorerClientState(storage)
    expect(state.clientID()).toBe(clientID)
    expect(headers()['X-LeapView-Data-Explorer-Client']).toBe(clientID)
  } finally {
    if (previous) Object.defineProperty(target, 'sessionStorage', previous)
    else delete target.sessionStorage
  }
})

test('falls back to the in-memory semantic clock when storage is unavailable', () => {
  const storage = {
    getItem: () => { throw new Error('storage unavailable') },
    setItem: () => { throw new Error('storage unavailable') },
  } as unknown as Storage
  const state = new DataExplorerClientState(storage)
  const first = state.nextRequestSequence('storage-failure', 2000)

  expect(first).toBe(2000)
  expect(state.nextRequestSequence('storage-failure', 0)).toBe(2001)
})

test('fails closed rather than repeating an exhausted safe sequence', () => {
  const storage = memoryStorage()
  storage.setItem('leapview-data-explorer-request-seq', String(Number.MAX_SAFE_INTEGER))
  const state = new DataExplorerClientState(storage)

  expect(() => state.nextRequestSequence('exhausted-sequence', 0)).toThrow('request sequence exhausted')
})
