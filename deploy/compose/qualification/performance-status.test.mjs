import assert from 'node:assert/strict'
import test from 'node:test'
import { dashboardRevisionSettled } from './performance-status.mjs'

function snapshot({
  filterRevision = 11,
  generation = 1,
  loading = false,
  visuals = [
    { filterRevision: 11, streamGeneration: 1, status: 'ready' },
    { filterRevision: 11, streamGeneration: 1, status: 'ready' },
  ],
} = {}) {
  return {
    filterRevision,
    status: { generation, loading, error: '' },
    visuals: Object.fromEntries(visuals.map((visual, index) => [`visual-${index}`, visual])),
  }
}

test('accepts a settled current filter revision after the SSE generation resets', () => {
  assert.equal(dashboardRevisionSettled(snapshot({ generation: 1 }), 11), true)
})

test('rejects results that have not reached the current canonical filter revision', () => {
  assert.equal(dashboardRevisionSettled(snapshot({
    visuals: [{ filterRevision: 10, streamGeneration: 1, status: 'ready' }],
  }), 11), false)
})

test('rejects a mixed visual generation even when the revision matches', () => {
  assert.equal(dashboardRevisionSettled(snapshot({
    visuals: [
      { filterRevision: 11, streamGeneration: 1, status: 'ready' },
      { filterRevision: 11, streamGeneration: 2, status: 'ready' },
    ],
  }), 11), false)
})

test('rejects an in-progress dashboard refresh', () => {
  assert.equal(dashboardRevisionSettled(snapshot({ loading: true }), 11), false)
})
