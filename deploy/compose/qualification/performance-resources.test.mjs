import { test } from 'node:test'
import assert from 'node:assert/strict'
import { coldResourceDelta, summarizeResources } from './performance-resources.mjs'

const measurement = (cpuSeconds = 0, residentMemoryBytes = 64, goroutines = 10, openConnections = 0) =>
  ({ cpuSeconds, residentMemoryBytes, goroutines, openConnections })
const warm = () => Array.from({ length: 8 }, (_, index) => measurement(index * 0.25, 64 + index, 10 + index, index % 3))
const cold = () => coldResourceDelta([measurement(0, 80), measurement(0.126, 90)])

test('retains actual warm and separate restarted-cold snapshots, with existing aggregation', () => {
  const samples = warm()
  const coldEvidence = cold()
  // Altered cold aggregates cannot replace their recorded measurements.
  coldEvidence.cpuSeconds = 0
  coldEvidence.peakResidentMemoryBytes = 1
  const report = summarizeResources(samples, [coldEvidence, cold()])
  assert.equal(report.schemaVersion, 1)
  assert.equal(report.cpuSeconds, 2.01)
  assert.equal(report.peakResidentMemoryBytes, 90)
  assert.equal(report.goroutinesBefore, 10)
  assert.equal(report.goroutinesAfter, 17)
  assert.equal(report.peakOpenConnections, 2)
  assert.equal(report.metricSamples, 8)
  assert.deepEqual(report.measurements, samples)
  assert.deepEqual(report.coldMeasurements[0], coldEvidence.measurements)
  samples[0].cpuSeconds = 1000
  assert.equal(report.measurements[0].cpuSeconds, 0)
})

test('measured zero CPU and connections remain valid', () => {
  const report = summarizeResources(Array.from({ length: 8 }, () => measurement()), [coldResourceDelta([measurement(), measurement()])])
  assert.equal(report.cpuSeconds, 0)
  assert.equal(report.peakOpenConnections, 0)
})

for (const [field, bad] of [
  ['cpuSeconds', undefined], ['cpuSeconds', null], ['cpuSeconds', -1], ['cpuSeconds', NaN], ['cpuSeconds', Infinity],
  ['residentMemoryBytes', undefined], ['residentMemoryBytes', 0], ['residentMemoryBytes', -1], ['residentMemoryBytes', 1.5],
  ['goroutines', undefined], ['goroutines', 0], ['goroutines', -1],
  ['openConnections', undefined], ['openConnections', null], ['openConnections', -1], ['openConnections', 0.5],
]) {
  test(`rejects ${field}=${String(bad)} in actual warm and cold evidence`, () => {
    const samples = warm()
    samples[0][field] = bad
    assert.throws(() => summarizeResources(samples, [cold()]), new RegExp(field))
    assert.throws(() => coldResourceDelta([samples[0], measurement()]), new RegExp(field))
  })
}

test('missing/short snapshots and process resets cannot become zero usage', () => {
  assert.throws(() => summarizeResources([], [cold()]), /8 resource samples/)
  assert.throws(() => summarizeResources(warm().slice(0, 7), [cold()]), /8 resource samples/)
  assert.throws(() => summarizeResources(warm(), []), /cold resource evidence/)
  assert.throws(() => summarizeResources(warm(), [{}]), /2 resource samples/)
  assert.throws(() => coldResourceDelta([measurement()]), /2 resource samples/)
  assert.throws(() => coldResourceDelta([measurement(1), measurement(0)]), /decreased within one process/)
  const samples = warm()
  samples[4].cpuSeconds = 0
  assert.throws(() => summarizeResources(samples, [cold()]), /decreased within one process/)
})

test('finite counters cannot overflow their rounded CPU aggregate', () => {
  assert.throws(() => coldResourceDelta([measurement(), measurement(1e308)]), /cpuSeconds aggregate must be finite/)
})
