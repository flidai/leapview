import { describe, expect, test } from 'bun:test'
import {
  aggregateViewportQualification,
  validateViewportQualificationEvidence,
  validateViewportQualificationSample,
  viewportObservationError,
  type ViewportQualificationSample,
} from './viewport_qualification_contract'

const ids = Array.from({ length: 24 }, (_, index) => `visual-${index}`)
const near = ids.slice(0, 12)

function sample(variant: 'eager' | 'deferred', repetition = 1, warmup = false): ViewportQualificationSample {
  return {
    variant, repetition, warmup, expectedVisualIDs: ids, nearViewportVisualIDs: near,
    shellVisualIDs: ids, initialMountVisualIDs: variant === 'eager' ? ids : near,
    forcedSnapshotVisualIDs: ids, forcedMountVisualIDs: ids,
    scrollMountVisualIDs: ids, scrollDisposeVisualIDs: [], teardownDisposeVisualIDs: ids,
    readinessMs: 100 + repetition, taskDurationSeconds: 0.2 + repetition / 100,
    jsHeapUsedBytes: 1_000_000 + repetition, errors: [],
  }
}

describe('viewport qualification evidence', () => {
  test('accepts one warmup and five structurally complete samples per variant', () => {
    const samples = (['eager', 'deferred'] as const).flatMap((variant) => [
      sample(variant, 0, true),
      ...Array.from({ length: 5 }, (_, index) => sample(variant, index + 1)),
    ])
    expect(validateViewportQualificationEvidence(samples)).toEqual([])
    expect(aggregateViewportQualification(samples)).toEqual({
      eager: {
        samples: 5,
        readinessMs: { median: 103, p95: 105 },
        taskDurationSeconds: { median: 0.23, p95: 0.25 },
        jsHeapUsedBytes: { median: 1_000_003, p95: 1_000_005 },
      },
      deferred: {
        samples: 5,
        readinessMs: { median: 103, p95: 105 },
        taskDurationSeconds: { median: 0.23, p95: 0.25 },
        jsHeapUsedBytes: { median: 1_000_003, p95: 1_000_005 },
      },
    })
  })

  test('fails missing and malformed metrics instead of producing a false pass', () => {
    const invalid = sample('deferred')
    invalid.taskDurationSeconds = Number.NaN
    invalid.jsHeapUsedBytes = -1
    expect(validateViewportQualificationSample(invalid)).toEqual(expect.arrayContaining([
      'taskDurationSeconds must be a finite non-negative number',
      'jsHeapUsedBytes must be a finite non-negative number',
    ]))
  })

  test('rejects malformed or unknown lifecycle observations even beside valid evidence', () => {
    expect(viewportObservationError({ stage: 'mount', durationMs: 1 })).toBe('mount requires visualID')
    expect(viewportObservationError({ stage: 'dispose', durationMs: Number.NaN, visualID: 'visual-1' })).toBe('dispose durationMs must be finite and non-negative')
    expect(viewportObservationError({ stage: 'invented', durationMs: 0, visualID: 'visual-1' })).toBe('unknown observation stage "invented"')
    expect(viewportObservationError({ stage: 'mount', durationMs: 1, visualID: 'visual-1' })).toBeUndefined()
  })

  test('detects deliberate mount, capture, scroll-disposal, teardown, and browser regressions', () => {
    const invalid = sample('deferred')
    invalid.initialMountVisualIDs = ids
    invalid.forcedSnapshotVisualIDs = ids.slice(1)
    invalid.scrollMountVisualIDs = [...ids, ids[0]!]
    invalid.scrollDisposeVisualIDs = [ids[0]!]
    invalid.teardownDisposeVisualIDs = ids.slice(0, -1)
    invalid.errors = ['500 fixture.js']
    const errors = validateViewportQualificationSample(invalid).join('\n')
    expect(errors).toContain('initial deferred mounts')
    expect(errors).toContain('forced snapshots')
    expect(errors).toContain('mounts after scrolling')
    expect(errors).toContain('scrolling disposed renderers')
    expect(errors).toContain('teardown disposals')
    expect(errors).toContain('browser: 500 fixture.js')
  })

  test('rejects incomplete repetition protocols', () => {
    expect(validateViewportQualificationEvidence([sample('eager')])).toEqual(expect.arrayContaining([
      'eager must contain exactly one discarded warmup',
      'eager must contain exactly five measured repetitions',
      'deferred must contain exactly one discarded warmup',
      'deferred must contain exactly five measured repetitions',
    ]))
  })
})
