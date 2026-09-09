export const viewportQualificationVisualCount = 24

export type ViewportQualificationVariant = 'eager' | 'deferred'

export type ViewportQualificationSample = {
  variant: ViewportQualificationVariant
  repetition: number
  warmup: boolean
  expectedVisualIDs: string[]
  nearViewportVisualIDs: string[]
  shellVisualIDs: string[]
  initialMountVisualIDs: string[]
  forcedSnapshotVisualIDs: string[]
  forcedMountVisualIDs: string[]
  scrollMountVisualIDs: string[]
  scrollDisposeVisualIDs: string[]
  teardownDisposeVisualIDs: string[]
  readinessMs: number
  taskDurationSeconds: number
  jsHeapUsedBytes: number
  errors: string[]
}

export type ViewportQualificationSummary = {
  samples: number
  readinessMs: TimingSummary
  taskDurationSeconds: TimingSummary
  jsHeapUsedBytes: TimingSummary
}

export type TimingSummary = { median: number, p95: number }

const observationStages = new Set([
  'validation_failure', 'stale_result_drop', 'renderer_load', 'mount', 'update',
  'resize', 'dispose', 'adapter_error', 'adapter_observation',
])

export function viewportObservationError(value: unknown): string | undefined {
  if (!value || typeof value !== 'object') return 'observation must be an object'
  const observation = value as Record<string, unknown>
  if (typeof observation.stage !== 'string' || !observationStages.has(observation.stage)) {
    return `unknown observation stage ${JSON.stringify(observation.stage)}`
  }
  if (typeof observation.durationMs !== 'number' || !Number.isFinite(observation.durationMs) || observation.durationMs < 0) {
    return `${observation.stage} durationMs must be finite and non-negative`
  }
  if ((observation.stage === 'mount' || observation.stage === 'dispose')
    && (typeof observation.visualID !== 'string' || observation.visualID.length === 0)) {
    return `${observation.stage} requires visualID`
  }
  if (observation.stage === 'adapter_error' || observation.stage === 'validation_failure') {
    return `${observation.stage} reported for ${String(observation.visualID ?? 'unknown visual')}`
  }
  return undefined
}

export function validateViewportQualificationSample(sample: ViewportQualificationSample): string[] {
  const errors: string[] = []
  const expected = sample.expectedVisualIDs
  if (expected.length !== viewportQualificationVisualCount || new Set(expected).size !== expected.length) {
    errors.push(`expected visual identity must contain ${viewportQualificationVisualCount} unique IDs`)
  }
  exactIDs('accessible shells', sample.shellVisualIDs, expected, errors)
  if (sample.errors.length) errors.push(...sample.errors.map((error) => `browser: ${error}`))
  for (const [name, value] of [
    ['readinessMs', sample.readinessMs],
    ['taskDurationSeconds', sample.taskDurationSeconds],
    ['jsHeapUsedBytes', sample.jsHeapUsedBytes],
  ] as const) {
    if (!Number.isFinite(value) || value < 0) errors.push(`${name} must be a finite non-negative number`)
  }
  const near = sample.nearViewportVisualIDs
  if (near.length === 0 || near.length >= expected.length || near.some((id) => !expected.includes(id))) {
    errors.push('near-viewport identity must be a non-empty strict subset of expected visuals')
  }
  if (sample.variant === 'eager') exactIDs('initial eager mounts', sample.initialMountVisualIDs, expected, errors)
  else exactIDs('initial deferred mounts', sample.initialMountVisualIDs, near, errors)
  exactIDs('forced snapshots', sample.forcedSnapshotVisualIDs, expected, errors)
  exactIDs('forced mounts', sample.forcedMountVisualIDs, expected, errors)
  exactIDs('mounts after scrolling', sample.scrollMountVisualIDs, expected, errors)
  exactIDs('teardown disposals', sample.teardownDisposeVisualIDs, expected, errors)
  if (sample.scrollDisposeVisualIDs.length) {
    errors.push(`scrolling disposed renderers: ${sample.scrollDisposeVisualIDs.join(', ')}`)
  }
  return errors
}

export function validateViewportQualificationEvidence(samples: ViewportQualificationSample[]): string[] {
  const errors: string[] = []
  for (const sample of samples) {
    if (sample.variant !== 'eager' && sample.variant !== 'deferred') errors.push('sample variant must be eager or deferred')
    if (typeof sample.warmup !== 'boolean') errors.push('sample warmup must be a boolean')
    errors.push(...validateViewportQualificationSample(sample).map((error) => `${sample.variant} repetition ${sample.repetition}: ${error}`))
  }
  for (const variant of ['eager', 'deferred'] as const) {
    const warmups = samples.filter((sample) => sample.variant === variant && sample.warmup)
    const measured = samples.filter((sample) => sample.variant === variant && !sample.warmup)
    if (warmups.length !== 1) errors.push(`${variant} must contain exactly one discarded warmup`)
    if (warmups.some((sample) => sample.repetition !== 0)) errors.push(`${variant} warmup must be repetition 0`)
    if (measured.length !== 5) errors.push(`${variant} must contain exactly five measured repetitions`)
    if (JSON.stringify(measured.map((sample) => sample.repetition).sort((a, b) => a - b)) !== '[1,2,3,4,5]') {
      errors.push(`${variant} must contain unique repetitions 1 through 5`)
    }
  }
  return errors
}

export function aggregateViewportQualification(samples: ViewportQualificationSample[]): Record<ViewportQualificationVariant, ViewportQualificationSummary> {
  const errors = validateViewportQualificationEvidence(samples)
  if (errors.length) throw new Error(errors.join('\n'))
  return Object.fromEntries((['eager', 'deferred'] as const).map((variant) => {
    const measured = samples.filter((sample) => sample.variant === variant && !sample.warmup)
    if (measured.length !== 5) throw new Error(`${variant} requires five measured samples`)
    return [variant, {
      samples: measured.length,
      readinessMs: timing(measured.map((sample) => sample.readinessMs)),
      taskDurationSeconds: timing(measured.map((sample) => sample.taskDurationSeconds)),
      jsHeapUsedBytes: timing(measured.map((sample) => sample.jsHeapUsedBytes)),
    }]
  })) as Record<ViewportQualificationVariant, ViewportQualificationSummary>
}

function exactIDs(label: string, actual: string[], expected: string[], errors: string[]): void {
  const actualSorted = [...actual].sort()
  const expectedSorted = [...expected].sort()
  if (actual.length !== new Set(actual).size || JSON.stringify(actualSorted) !== JSON.stringify(expectedSorted)) {
    errors.push(`${label} ${JSON.stringify(actualSorted)}, want ${JSON.stringify(expectedSorted)}`)
  }
}

function timing(values: number[]): TimingSummary {
  if (values.some((value) => !Number.isFinite(value) || value < 0)) throw new Error('timing samples must be finite and non-negative')
  const sorted = [...values].sort((left, right) => left - right)
  return { median: round(sorted[Math.floor(sorted.length / 2)]!), p95: round(sorted[Math.ceil(sorted.length * 0.95) - 1]!) }
}

function round(value: number): number { return Math.round(value * 1000) / 1000 }
