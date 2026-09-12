const resourceMetricDefinitions = [
  ['processStartTimeSeconds', 'process_start_time_seconds', { integer: false, positive: true }],
  ['cpuSeconds', 'process_cpu_seconds_total', { integer: false, positive: false }],
  ['residentMemoryBytes', 'process_resident_memory_bytes', { integer: true, positive: false }],
  ['goroutines', 'go_goroutines', { integer: true, positive: false }],
  ['openConnections', 'leapview_duckdb_connections_open', { integer: true, positive: false }],
]

export function resourceMetricSnapshot(values) {
  return Object.fromEntries(resourceMetricDefinitions.map(([field, name, options]) => [
    field,
    readMetric(values, name, options),
  ]))
}

export function validateResourcePhase(samples) {
  if (!Array.isArray(samples) || samples.length < 2) {
    throw new Error('resource phase requires at least two metric snapshots')
  }
  let previousCPU = null
  const processStartTimeSeconds = samples[0].processStartTimeSeconds
  for (const sample of samples) {
    validateResourceSnapshot(sample)
    if (sample.processStartTimeSeconds !== processStartTimeSeconds) {
      throw new Error('resource phase changed process identity')
    }
    if (previousCPU !== null && sample.cpuSeconds < previousCPU) {
      throw new Error('resource phase CPU counter fell')
    }
    previousCPU = sample.cpuSeconds
  }
  return samples
}

export function summarizeResourceSamples(warmSamples, coldSamples) {
  validateResourcePhase(warmSamples)
  if (!Array.isArray(coldSamples)) throw new Error('cold resource phases are required')
  for (const samples of coldSamples) validateResourcePhase(samples)
  const phases = [warmSamples, ...coldSamples]
  const allSamples = phases.flat()
  // Match the Go verifier's phase order and parenthesized deltas so floating
  // point accumulation cannot disagree at a rounding boundary.
  const cpuSeconds = phases.reduce((total, samples) => total + (samples.at(-1).cpuSeconds - samples[0].cpuSeconds), 0)
  return {
    peakResidentMemoryBytes: Math.max(...allSamples.map((sample) => sample.residentMemoryBytes)),
    cpuSeconds: round(cpuSeconds),
    goroutinesBefore: warmSamples[0].goroutines,
    goroutinesAfter: warmSamples.at(-1).goroutines,
    peakOpenConnections: Math.max(...allSamples.map((sample) => sample.openConnections)),
    metricSamples: warmSamples.length,
    metricSnapshots: warmSamples,
    coldMetricSnapshots: coldSamples,
  }
}

export function summarizeResourcePhase(samples) {
  validateResourcePhase(samples)
  return {
    cpuSeconds: round(samples.at(-1).cpuSeconds - samples[0].cpuSeconds),
    peakResidentMemoryBytes: Math.max(...samples.map((sample) => sample.residentMemoryBytes)),
    metricSnapshots: samples,
  }
}

export function validateResourceSummary(summary, warmSamples, coldSamples) {
  const expected = summarizeResourceSamples(warmSamples, coldSamples)
  for (const field of [
    'peakResidentMemoryBytes',
    'cpuSeconds',
    'goroutinesBefore',
    'goroutinesAfter',
    'peakOpenConnections',
    'metricSamples',
  ]) {
    if (summary[field] !== expected[field]) {
      throw new Error(`resource summary ${field} does not match raw samples`)
    }
  }
  return true
}

function readMetric(values, name, options) {
  const samples = values[name]
  if (!Array.isArray(samples) || samples.length !== 1) {
    throw new Error(`metrics must contain exactly one ${name} sample`)
  }
  const value = samples[0]
  if (!Number.isFinite(value) || value < 0 || (options.positive && value <= 0) || (options.integer && !Number.isInteger(value))) {
    throw new Error(`metric ${name} is invalid`)
  }
  return value
}

function validateResourceSnapshot(sample) {
  if (!sample || typeof sample !== 'object') throw new Error('resource snapshot is invalid')
  for (const [field, , options] of resourceMetricDefinitions) {
    const value = sample[field]
    if (!Number.isFinite(value) || value < 0 || (options.positive && value <= 0) || (options.integer && !Number.isInteger(value))) {
      throw new Error(`resource snapshot ${field} is invalid`)
    }
  }
}

function round(value) {
  return Math.round(value * 100) / 100
}
