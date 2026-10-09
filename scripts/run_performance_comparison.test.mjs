import test from 'node:test'
import assert from 'node:assert/strict'
import { join, resolve } from 'node:path'
import { requireSuccessfulReport, runProductionPerformanceComparison } from './run_performance_comparison.mjs'
import { requiresPerformanceReview } from './performance_baseline_review.mjs'

const commit = 'a'.repeat(40)
const candidate = 'ghcr.io/flidai/leapview@sha256:' + 'b'.repeat(64)
const baseline = 'ghcr.io/flidai/leapview@sha256:' + 'c'.repeat(64)
const successful = (image) => ({ schemaVersion: 1, image, result: 'success', assertions: {
  environment: true, absoluteBudgets: true, errorFree: true, comparisonTolerance: false }, failures: [] })
const options = { candidateCommit: commit, candidateImage: candidate, evidenceDirectory: '/private/candidate' }

test('serial qualification enforcement and its regression fixtures require independent review', () => {
  assert.equal(requiresPerformanceReview(['scripts/run_performance_comparison.mjs']), true)
  assert.equal(requiresPerformanceReview(['scripts/run_performance_comparison.test.mjs']), true)
})

function harness(reference = true) {
  const calls = [], retained = []
  let failControl = false
  const referenceDirectory = resolve('/private/candidate-comparison/reference')
  const baselinePath = join(referenceDirectory, 'performance-report.json')
  const dependencies = { source: () => commit, clean: () => true, fingerprint: () => ({ stable: true }), exists: () => reference,
    read: (path) => path === '.quality/performance-reference.json' ? JSON.stringify({ image: baseline }) : Buffer.from('{}'),
    verify: (value, identities) => { assert.equal(value.image, baseline); assert.deepEqual(identities, { candidateCommit: commit, candidateImage: candidate }); return { reference: value, reportSHA256: 'd'.repeat(64) } },
    report: (path) => {
      if (path === baselinePath) {
        const value = successful(baseline)
        if (failControl) value.result = 'failure'
        return value
      }
      const value = successful(candidate)
      if (reference) { value.assertions.comparisonTolerance = true; value.comparison = { baseline: baselinePath, failures: [] } }
      return value
    },
    execute: (command, args, env) => calls.push({ command, args, env }),
    retain: (path, value) => retained.push({ path, value }), mkdir: () => {} }
  return { dependencies, calls, retained, baselinePath, failControl: () => { failControl = true } }
}

test('reviewed reference is admitted, then measured serially before the candidate uses its fresh report', () => {
  const h = harness()
  assert.equal(runProductionPerformanceComparison(options, h.dependencies), 'serial-reference-comparison')
  assert.equal(h.calls.length, 2)
  assert.deepEqual(h.calls.map((call) => call.args[call.args.indexOf('--image') + 1]), [baseline, candidate])
  assert.equal(h.calls[0].env.QUALIFICATION_PERFORMANCE_BASELINE, undefined)
  assert.equal(h.calls[1].env.QUALIFICATION_PERFORMANCE_BASELINE, h.baselinePath)
  assert.equal(h.calls[0].env.TMPDIR, resolve('.tmp/qualification/tmp'))
  assert.equal(h.calls[1].env.TMPDIR, h.calls[0].env.TMPDIR)
  assert.equal(h.retained.at(-1).value.comparison, true)
})

test('no accepted reference is explicitly absolute-only bootstrap', () => {
  const h = harness(false)
  h.dependencies.verify = () => { throw Error('must not invent a reference') }
  assert.equal(runProductionPerformanceComparison(options, h.dependencies), 'absolute-only-bootstrap')
  assert.equal(h.calls.length, 1)
  assert.equal(h.retained.at(-1).value.comparison, false)
})

test('failed reference admission or qualification stops before candidate execution', () => {
  const untrusted = harness()
  untrusted.dependencies.verify = () => { throw Error('upstream reference mismatch') }
  assert.throws(() => runProductionPerformanceComparison(options, untrusted.dependencies), /upstream reference mismatch/)
  assert.equal(untrusted.calls.length, 0)
  const failed = harness()
  failed.failControl()
  assert.throws(() => runProductionPerformanceComparison(options, failed.dependencies), /successful report/)
  assert.equal(failed.calls.length, 1)
  assert.equal(failed.retained.length, 2)
})

test('candidate report must name the measured baseline and pass actual comparison tolerance', () => {
  const value = successful(candidate)
  assert.throws(() => requireSuccessfulReport(value, candidate, '/fresh/reference.json'), /required comparison/)
  value.assertions.comparisonTolerance = true
  value.comparison = { baseline: '/stale/other.json', failures: [] }
  assert.throws(() => requireSuccessfulReport(value, candidate, '/fresh/reference.json'), /required comparison/)
  value.comparison.baseline = '/fresh/reference.json'
  value.comparison.failures.push('latency regression')
  assert.throws(() => requireSuccessfulReport(value, candidate, '/fresh/reference.json'), /required comparison/)
})

test('wrong candidate source and ambient unreviewed baseline cannot start qualification', () => {
  const h = harness()
  h.dependencies.source = () => 'e'.repeat(40)
  assert.throws(() => runProductionPerformanceComparison(options, h.dependencies), /checkout must match/)
  assert.equal(h.calls.length, 0)
  const previous = process.env.QUALIFICATION_PERFORMANCE_BASELINE
  try {
    process.env.QUALIFICATION_PERFORMANCE_BASELINE = '/unreviewed/report.json'
    assert.throws(() => runProductionPerformanceComparison(options, harness().dependencies), /ambient baseline/)
  } finally {
    if (previous === undefined) delete process.env.QUALIFICATION_PERFORMANCE_BASELINE
    else process.env.QUALIFICATION_PERFORMANCE_BASELINE = previous
  }
})

test('source mutation during reference measurement cannot qualify a candidate against different tooling', () => {
  const h = harness()
  let clean = true
  h.dependencies.clean = () => clean
  h.dependencies.execute = (command, args, env) => { h.calls.push({ command, args, env }); clean = false }
  assert.throws(() => runProductionPerformanceComparison(options, h.dependencies), /clean candidate source/)
  assert.equal(h.calls.length, 1)
})

test('ignored generated controller input mutation stops the comparison even when HEAD is unchanged', () => {
  const h = harness()
  let inputs = 'original'
  h.dependencies.fingerprint = () => ({ generatedSHA: inputs })
  h.dependencies.execute = (command, args, env) => { h.calls.push({ command, args, env }); inputs = 'changed' }
  assert.throws(() => runProductionPerformanceComparison(options, h.dependencies), /generated\/native\/embed inputs/)
  assert.equal(h.calls.length, 1)
})
