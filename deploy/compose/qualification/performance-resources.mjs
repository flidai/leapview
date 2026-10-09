// One warm process: before work, six phase boundaries, then settling. Each
// restarted cold process contributes its own before/after pair.
export const warmResourceSamples = 8
export const coldResourceSamples = 2

function validateMeasurements(samples, expected, label) {
  if (!Array.isArray(samples) || samples.length !== expected) {
    throw new Error(`${label} requires ${expected} resource samples`)
  }
  for (const [index, sample] of samples.entries()) {
    if (!sample || typeof sample !== 'object') throw new Error(`${label}[${index}] is missing`)
    if (!Number.isFinite(sample.cpuSeconds) || sample.cpuSeconds < 0) {
      throw new Error(`${label}[${index}].cpuSeconds must be finite and nonnegative`)
    }
    for (const [field, minimum] of [['residentMemoryBytes', 1], ['goroutines', 1], ['openConnections', 0]]) {
      if (!Number.isSafeInteger(sample[field]) || sample[field] < minimum) {
        throw new Error(`${label}[${index}].${field} must be an integer at least ${minimum}`)
      }
    }
    if (index > 0 && sample.cpuSeconds < samples[index - 1].cpuSeconds) {
      throw new Error(`${label}[${index}].cpuSeconds decreased within one process`)
    }
  }
}

const round = (value) => {
  const rounded = Math.round(value * 100) / 100
  if (!Number.isFinite(rounded)) throw new Error('resource cpuSeconds aggregate must be finite')
  return rounded
}

export function coldResourceDelta(samples) {
  validateMeasurements(samples, coldResourceSamples, 'cold measurements')
  return {
    cpuSeconds: round(samples.at(-1).cpuSeconds - samples[0].cpuSeconds),
    peakResidentMemoryBytes: Math.max(...samples.map((sample) => sample.residentMemoryBytes)),
    measurements: samples.map((sample) => ({ ...sample })),
  }
}

export function summarizeResources(samples, coldResources) {
  validateMeasurements(samples, warmResourceSamples, 'warm measurements')
  if (!Array.isArray(coldResources) || coldResources.length === 0) {
    throw new Error('cold resource evidence is missing')
  }
  // Recompute from real snapshots, never trust a cold artifact's aggregates.
  const cold = coldResources.map((resource) => coldResourceDelta(resource?.measurements))
  const first = samples[0]
  const last = samples.at(-1)
  const coldCPU = cold.reduce((sum, sample) => sum + sample.cpuSeconds, 0)
  return {
    schemaVersion: 1,
    peakResidentMemoryBytes: Math.max(
      ...samples.map((sample) => sample.residentMemoryBytes),
      ...cold.map((sample) => sample.peakResidentMemoryBytes),
    ),
    cpuSeconds: round(coldCPU + (last.cpuSeconds - first.cpuSeconds)),
    temporaryDiskGrowthBytes: 0,
    goroutinesBefore: first.goroutines,
    goroutinesAfter: last.goroutines,
    peakOpenConnections: Math.max(...samples.map((sample) => sample.openConnections)),
    metricSamples: samples.length,
    measurements: samples.map((sample) => ({ ...sample })),
    coldMeasurements: cold.map((sample) => sample.measurements),
  }
}
