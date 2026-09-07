import { expect, test } from 'bun:test'
import type { DataExploreResultSignal, DataExploreStatusSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DataExplorerClientState } from './data-explorer-client'
import { emptyDataExploreCommand } from './data-explorer-spec'

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
