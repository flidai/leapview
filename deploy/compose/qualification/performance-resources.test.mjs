import assert from 'node:assert/strict'
import test from 'node:test'

import {
  resourceMetricSnapshot,
  summarizeResourceSamples,
  validateResourcePhase,
  validateResourceSummary,
} from './performance-resources.mjs'

function snapshot(processStartTimeSeconds, cpuSeconds, offset = 0) {
  return {
    processStartTimeSeconds,
    cpuSeconds,
    residentMemoryBytes: 100 + offset,
    goroutines: 2 + offset,
    openConnections: 1 + offset,
  }
}

function samples() {
  const warm = Array.from({ length: 8 }, (_, index) => snapshot(10, index + 1))
  const cold = Array.from({ length: 3 }, (_, phase) => [
    snapshot(20 + phase, 0, phase),
    snapshot(20 + phase, 0.5, phase),
  ])
  return { warm, cold }
}

test('resource summary is derived from normal warm and cold samples', () => {
  const { warm, cold } = samples()
  const summary = summarizeResourceSamples(warm, cold)
  assert.equal(summary.cpuSeconds, 8.5)
  assert.equal(summary.peakResidentMemoryBytes, 102)
  assert.equal(summary.goroutinesBefore, 2)
  assert.equal(summary.goroutinesAfter, 2)
  assert.equal(summary.peakOpenConnections, 3)
  assert.equal(summary.metricSamples, 8)
  assert.doesNotThrow(() => validateResourceSummary(summary, warm, cold))
})

test('metric snapshots reject missing and non-integer gauges', () => {
  assert.throws(() => resourceMetricSnapshot({
    process_start_time_seconds: [1],
    process_cpu_seconds_total: [1],
    process_resident_memory_bytes: [1],
    go_goroutines: [1],
  }), /leapview_duckdb_connections_open/)
  assert.throws(() => resourceMetricSnapshot({
    process_start_time_seconds: [1],
    process_cpu_seconds_total: [1],
    process_resident_memory_bytes: [1.5],
    go_goroutines: [1],
    leapview_duckdb_connections_open: [1],
  }), /process_resident_memory_bytes is invalid/)
})

test('resource phases reject CPU resets and process identity changes', () => {
  const { warm } = samples()
  const reset = warm.map((sample) => ({ ...sample }))
  reset[3].cpuSeconds = 0.5
  assert.throws(() => validateResourcePhase(reset), /CPU counter fell/)
  const changed = warm.map((sample) => ({ ...sample }))
  changed[3].processStartTimeSeconds = 11
  assert.throws(() => validateResourcePhase(changed), /changed process identity/)
})

test('every raw resource field rejects absent, null, negative and non-finite values', () => {
  for (const field of ['processStartTimeSeconds', 'cpuSeconds', 'residentMemoryBytes', 'goroutines', 'openConnections']) {
    for (const value of [undefined, null, -1, NaN, Infinity]) {
      const phase = [snapshot(10, 1), { ...snapshot(10, 2), [field]: value }]
      assert.throws(() => validateResourcePhase(phase), /invalid/, `${field}: ${value}`)
    }
  }
  for (const field of ['residentMemoryBytes', 'goroutines', 'openConnections']) {
    assert.throws(() => validateResourcePhase([snapshot(10, 1), { ...snapshot(10, 2), [field]: 1.5 }]), /invalid/)
  }
})

test('resource summaries reject deliberate tampering', () => {
  const { warm, cold } = samples()
  const summary = summarizeResourceSamples(warm, cold)
  summary.peakOpenConnections += 1
  assert.throws(() => validateResourceSummary(summary, warm, cold), /peakOpenConnections/)
})
