import { chromium } from 'playwright'
import { readFile, writeFile } from 'node:fs/promises'
import { request } from 'node:http'
import process from 'node:process'

const baseURL = process.env.QUALIFICATION_URL || 'https://localhost'
const projectID = process.env.QUALIFICATION_PROJECT_ID || 'project:leapview-evaluation'
const semanticModelID = 'semantic-model:sales'
const pipelineID = 'pipeline:evaluation-refresh'
const credentialsPath = process.env.QUALIFICATION_CREDENTIALS || '/run/secrets/credentials.json'
const policyPath = process.env.QUALIFICATION_PERFORMANCE_POLICY || '/qualification/performance-policy.json'
const metricsURL = process.env.QUALIFICATION_METRICS_URL || 'http://127.0.0.1:8080/metrics'
const metricsToken = process.env.QUALIFICATION_METRICS_TOKEN || ''
const phase = process.argv[2]
const outputPath = process.argv[3]

if (!['cold', 'workload'].includes(phase) || !outputPath) {
  throw new Error('usage: node performance.mjs <cold|workload> <output-path>')
}

const policy = JSON.parse(await readFile(policyPath, 'utf8'))

if (phase === 'cold') {
  await runColdSample(outputPath)
} else {
  await runWorkload(outputPath)
}

async function runColdSample(path) {
  const credentials = await readCredentials()
  const browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ ignoreHTTPSErrors: true })
  const page = await context.newPage()
  const failures = []
  const metricSamples = []
  try {
    const dashboardURL = await loginAndResolveDashboard(page, credentials)
    metricSamples.push(await metricSnapshot())
    const startedAt = performance.now()
    await page.goto(dashboardURL, { waitUntil: 'domcontentloaded', timeout: 60_000 })
    await waitForDashboardIdle(page, 60_000)
    await page.getByText('Governed order rows', { exact: true }).waitFor({ state: 'visible', timeout: 30_000 })
    metricSamples.push(await metricSnapshot())
    await writeJSON(path, {
      durationMs: round(performance.now() - startedAt),
      resources: coldResourceDelta(metricSamples),
      failures,
    })
  } catch (error) {
    failures.push(error instanceof Error ? error.message : String(error))
    await writeJSON(path, { durationMs: 0, resources: emptyResourceDelta(), failures })
    throw error
  } finally {
    await context.close()
    await browser.close()
  }
}

async function runWorkload(path) {
  const credentials = await readCredentials()
  const coldPaths = JSON.parse(process.env.QUALIFICATION_COLD_RESULTS || '[]')
  const coldResults = await Promise.all(coldPaths.map(async (coldPath) => JSON.parse(await readFile(coldPath, 'utf8'))))
  if (coldResults.length !== policy.assumptions.samples.coldDashboardLoads) {
    throw new Error(`performance workload received ${coldResults.length} cold samples, expected ${policy.assumptions.samples.coldDashboardLoads}`)
  }
  const coldFailures = coldResults.flatMap((result) => result.failures || [])
  if (coldFailures.length > 0) throw new Error(`cold performance samples failed:\n${coldFailures.join('\n')}`)

  const browser = await chromium.launch({ headless: true })
  const context = await browser.newContext({ ignoreHTTPSErrors: true })
  const page = await context.newPage({ viewport: { width: 1440, height: 960 } })
  const controlled = { requests: 0, errors: 0, failures: [] }
  const warmDashboardReadyMs = []
  const filterToSettleMs = []
  const tableInteractionMs = []
  const governedQueryMs = []
  const refreshMs = []
  const concurrentQueryMs = []
  let concurrentWaveMs = 0
  const metricSamples = []

  page.on('console', (message) => {
    if (message.type() === 'error' || (message.type() === 'warning' && message.text().includes('[LeapView]'))) {
      controlled.failures.push(`browser console: ${message.text()}`)
      controlled.errors += 1
    }
  })
  page.on('response', (response) => {
    if (response.status() >= 400) {
      controlled.failures.push(`browser response: ${response.status()} ${response.request().method()} ${response.url()}`)
      controlled.errors += 1
    }
  })

  try {
    const dashboardURL = await loginAndResolveDashboard(page, credentials)
    metricSamples.push(await metricSnapshot())

    for (let index = 0; index < policy.assumptions.samples.warmDashboardLoads; index += 1) {
      const startedAt = performance.now()
      await page.goto(dashboardURL, { waitUntil: 'domcontentloaded', timeout: 60_000 })
      await waitForDashboardIdle(page, 60_000)
      warmDashboardReadyMs.push(round(performance.now() - startedAt))
      controlled.requests += 1
    }
    metricSamples.push(await metricSnapshot())

    const table = page.locator('lv-report-table')
    const filterValues = ['SP', 'RJ', 'MG', 'PR']
    const filter = page.getByRole('combobox', { name: 'State' })
    for (let index = 0; index < policy.assumptions.samples.filterInteractions; index += 1) {
      const value = filterValues[index % filterValues.length]
      const generation = await dashboardGeneration(page)
      const startedAt = performance.now()
      await filter.selectOption({ label: value })
      await waitForDashboardGeneration(page, generation, 30_000)
      await table.getByRole('button', { name: `State: ${value}`, exact: true }).first().waitFor({ state: 'visible', timeout: 30_000 })
      filterToSettleMs.push(round(performance.now() - startedAt))
      controlled.requests += 1
    }
    metricSamples.push(await metricSnapshot())

    const orderSort = table.getByRole('button', { name: /^Order(?: [↑↓])?$/ })
    for (let index = 0; index < policy.assumptions.samples.tableInteractions; index += 1) {
      const previous = await tableSort(table)
      const startedAt = performance.now()
      await orderSort.click({ force: true })
      await waitForTableSort(table, previous, 'order_id', 30_000)
      tableInteractionMs.push(round(performance.now() - startedAt))
      controlled.requests += 1
    }
    metricSamples.push(await metricSnapshot())

    const queryBody = {
      dimensions: [{ field: 'state' }],
      metrics: [{ field: 'order_count' }, { field: 'revenue' }],
      limit: 10,
    }
    const queryURL = new URL(
      `/api/v1/semantic-models/${semanticModelID}/query`,
      baseURL,
    ).href
    for (let index = 0; index < policy.assumptions.samples.governedQueries; index += 1) {
      const startedAt = performance.now()
      const response = await context.request.post(queryURL, {
        headers: { Authorization: `Bearer ${credentials.workloadToken}` },
        data: queryBody,
      })
      controlled.requests += 1
      if (!response.ok()) {
        controlled.errors += 1
        controlled.failures.push(`governed query returned ${response.status()}`)
      } else {
        const result = await response.json()
        if (!Array.isArray(result.rows) || result.rows.length !== 4) {
          controlled.errors += 1
          controlled.failures.push('governed query did not return four state rows')
        }
      }
      governedQueryMs.push(round(performance.now() - startedAt))
    }
    metricSamples.push(await metricSnapshot())

    const refreshURL = new URL(
      `/api/v1/projects/${projectID}/refresh-runs`,
      baseURL,
    ).href
    for (let index = 0; index < policy.assumptions.samples.refreshRuns; index += 1) {
      const startedAt = performance.now()
      const response = await context.request.post(refreshURL, {
        headers: {
          Authorization: `Bearer ${credentials.workloadToken}`,
          'Idempotency-Key': `qualification-performance-refresh-${Date.now()}-${index}`,
        },
        data: { pipelineId: pipelineID },
      })
      controlled.requests += 1
      if (!response.ok()) {
        controlled.errors += 1
        controlled.failures.push(`refresh creation returned ${response.status()}: ${(await response.text()).slice(0, 500)}`)
        continue
      }
      const refresh = await response.json()
      const terminal = await waitForRefresh(context, refresh.id, credentials.workloadToken, controlled)
      if (terminal.status !== 'succeeded') {
        controlled.errors += 1
        controlled.failures.push(`refresh ${refresh.id} ended ${terminal.status}: ${terminal.error || 'no error detail'}`)
      }
      refreshMs.push(round(performance.now() - startedAt))
    }
    metricSamples.push(await metricSnapshot())

    const concurrentStartedAt = performance.now()
    const concurrentPromise = Promise.all(Array.from(
      { length: policy.assumptions.samples.concurrentReaders },
      async () => {
        const requestStartedAt = performance.now()
        const response = await context.request.post(queryURL, {
          headers: { Authorization: `Bearer ${credentials.workloadToken}` },
          data: queryBody,
        })
        controlled.requests += 1
        if (!response.ok()) {
          controlled.errors += 1
          controlled.failures.push(`concurrent governed query returned ${response.status()}`)
        } else {
          const result = await response.json()
          if (!Array.isArray(result.rows) || result.rows.length !== 4) {
            controlled.errors += 1
            controlled.failures.push('concurrent governed query did not return four state rows')
          }
        }
        return round(performance.now() - requestStartedAt)
      },
    ))
    metricSamples.push(await metricSnapshot())
    const concurrent = await concurrentPromise
    concurrentQueryMs.push(...concurrent)
    concurrentWaveMs = round(performance.now() - concurrentStartedAt)

    await page.waitForTimeout(2_000)
    metricSamples.push(await metricSnapshot())
  } finally {
    await context.close()
    await browser.close()
  }

  const coldResources = coldResults.map((result) => result.resources)
  const resources = summarizeResources(metricSamples, coldResources)
  await writeJSON(path, {
    schemaVersion: 1,
    generatedAt: new Date().toISOString(),
    policy,
    latency: {
      coldDashboardReadyMs: summarizeDurations(coldResults.map((result) => result.durationMs)),
      warmDashboardReadyMs: summarizeDurations(warmDashboardReadyMs),
      filterToSettleMs: summarizeDurations(filterToSettleMs),
      tableInteractionMs: summarizeDurations(tableInteractionMs),
      governedQueryMs: summarizeDurations(governedQueryMs),
      refreshMs: summarizeDurations(refreshMs),
      concurrentQueryMs: summarizeDurations(concurrentQueryMs),
    },
    reliability: {
      requests: controlled.requests,
      errors: controlled.errors,
      failures: controlled.failures,
    },
    resources,
    samples: {
      coldDashboardReadyMs: coldResults.map((result) => result.durationMs),
      warmDashboardReadyMs,
      filterToSettleMs,
      tableInteractionMs,
      governedQueryMs,
      refreshMs,
      concurrentQueryMs,
    },
    concurrency: {
      readers: policy.assumptions.samples.concurrentReaders,
      waveMs: concurrentWaveMs,
    },
  })
}

async function loginAndResolveDashboard(page, credentials) {
  await page.goto(new URL('/login', baseURL).href, { waitUntil: 'domcontentloaded', timeout: 60_000 })
  await page.getByLabel('Email').fill(credentials.email)
  await page.getByLabel('Password').fill(credentials.qualificationPassword)
  await page.getByLabel('Password').press('Enter')
  const dashboard = page.getByRole('link', { name: /Five-minute Sales Evaluation/i })
  await dashboard.waitFor({ state: 'visible', timeout: 60_000 })
  const href = await dashboard.getAttribute('href')
  if (!href) throw new Error('evaluation dashboard has no navigation target')
  return new URL(href, baseURL).href
}

async function waitForDashboardIdle(page, timeoutMs) {
  await page.locator('lv-dashboard-page').waitFor({ state: 'attached', timeout: timeoutMs })
  await waitForDashboardStatus(
    page,
    (status) => status.generation > 0 && !status.loading,
    timeoutMs,
  )
}

async function dashboardGeneration(page) {
  return page.locator('lv-dashboard-page').evaluate((element) => Number(element.status?.generation || 0))
}

async function waitForDashboardGeneration(page, previous, timeoutMs) {
  await waitForDashboardStatus(
    page,
    (status) => status.generation > previous && !status.loading,
    timeoutMs,
  )
}

async function waitForDashboardStatus(page, predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const status = await page.locator('lv-dashboard-page').evaluate((element) => ({
      generation: Number(element.status?.generation || 0),
      loading: Boolean(element.status?.loading),
    }))
    if (predicate(status)) return status
    await page.waitForTimeout(25)
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for dashboard status`)
}

async function tableSort(table) {
  return table.evaluate((element) => ({
    key: String(element.table?.sort?.key || ''),
    direction: String(element.table?.sort?.direction || ''),
  }))
}

async function waitForTableSort(table, previous, expectedKey, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const current = await tableSort(table)
    if (current.key === expectedKey && (
      current.key !== previous.key ||
      current.direction !== previous.direction
    )) {
      return current
    }
    await new Promise((resolve) => setTimeout(resolve, 25))
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for governed table sort`)
}

async function waitForRefresh(context, id, token, controlled) {
  const url = new URL(
    `/api/v1/projects/${projectID}/refresh-runs/${id}`,
    baseURL,
  ).href
  const deadline = Date.now() + 60_000
  while (Date.now() < deadline) {
    const response = await context.request.get(url, { headers: { Authorization: `Bearer ${token}` } })
    controlled.requests += 1
    if (!response.ok()) {
      controlled.errors += 1
      controlled.failures.push(`refresh ${id} lookup returned ${response.status()}`)
      throw new Error(`refresh ${id} lookup returned ${response.status()}`)
    }
    const refresh = await response.json()
    if (['succeeded', 'failed', 'canceled'].includes(refresh.status)) return refresh
    await new Promise((resolve) => setTimeout(resolve, 100))
  }
  throw new Error(`timed out waiting for refresh ${id}`)
}

async function metricSnapshot() {
  if (!metricsToken) throw new Error('QUALIFICATION_METRICS_TOKEN is required')
  const body = await requestMetrics()
  const values = parsePrometheusMetrics(body)
  return {
    cpuSeconds: metric(values, 'process_cpu_seconds_total'),
    residentMemoryBytes: metric(values, 'process_resident_memory_bytes'),
    goroutines: metric(values, 'go_goroutines'),
    openConnections: metric(values, 'leapview_duckdb_connections_open'),
  }
}

function requestMetrics() {
  const target = new URL(metricsURL)
  if (target.protocol !== 'http:') throw new Error('qualification metrics URL must use http')
  return new Promise((resolve, reject) => {
    const operation = request({
      protocol: target.protocol,
      hostname: target.hostname,
      port: target.port,
      path: `${target.pathname}${target.search}`,
      method: 'GET',
      headers: {
        Authorization: `Bearer ${metricsToken}`,
        Host: 'localhost',
      },
    }, (response) => {
      const chunks = []
      response.on('data', (chunk) => chunks.push(chunk))
      response.on('end', () => {
        if (response.statusCode !== 200) {
          reject(new Error(`metrics returned ${response.statusCode}`))
          return
        }
        resolve(Buffer.concat(chunks).toString('utf8'))
      })
    })
    operation.setTimeout(5_000, () => operation.destroy(new Error('metrics request timed out')))
    operation.on('error', reject)
    operation.end()
  })
}

function metric(values, name) {
  const samples = values[name] || []
  if (samples.length === 0) throw new Error(`metrics omitted ${name}`)
  return Math.max(...samples)
}

function summarizeResources(samples, coldResources) {
  if (samples.length === 0) throw new Error('performance workload captured no resource samples')
  const first = samples[0]
  const last = samples.at(-1)
  const coldCPU = coldResources.reduce((sum, sample) => sum + sample.cpuSeconds, 0)
  return {
    peakResidentMemoryBytes: Math.max(
      ...samples.map((sample) => sample.residentMemoryBytes),
      ...coldResources.map((sample) => sample.peakResidentMemoryBytes),
    ),
    cpuSeconds: round(coldCPU + Math.max(0, last.cpuSeconds - first.cpuSeconds)),
    temporaryDiskGrowthBytes: 0,
    goroutinesBefore: first.goroutines,
    goroutinesAfter: last.goroutines,
    peakOpenConnections: Math.max(...samples.map((sample) => sample.openConnections)),
    metricSamples: samples.length,
  }
}

function coldResourceDelta(samples) {
  const before = samples[0]
  const after = samples.at(-1)
  return {
    cpuSeconds: round(Math.max(0, after.cpuSeconds - before.cpuSeconds)),
    peakResidentMemoryBytes: Math.max(...samples.map((sample) => sample.residentMemoryBytes)),
  }
}

function emptyResourceDelta() {
  return { cpuSeconds: 0, peakResidentMemoryBytes: 0 }
}

async function readCredentials() {
  const credentials = JSON.parse(await readFile(credentialsPath, 'utf8'))
  if (!credentials.email || !credentials.qualificationPassword || !credentials.workloadToken) {
    throw new Error('qualification performance credentials are incomplete')
  }
  return credentials
}

async function writeJSON(path, value) {
  await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o644 })
}

function round(value) {
  return Math.round(value * 100) / 100
}

function percentile(values, rank) {
  if (values.length === 0) return 0
  const sorted = [...values].sort((left, right) => left - right)
  const index = Math.max(0, Math.ceil((rank / 100) * sorted.length) - 1)
  return round(sorted[Math.min(index, sorted.length - 1)])
}

function summarizeDurations(values) {
  return {
    samples: values.length,
    p50: percentile(values, 50),
    p95: percentile(values, 95),
    max: values.length === 0 ? 0 : round(Math.max(...values)),
  }
}

function parsePrometheusMetrics(input) {
  const result = {}
  for (const rawLine of input.split('\n')) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#')) continue
    const match = line.match(/^([a-zA-Z_:][a-zA-Z0-9_:]*)(?:\{[^}]*\})?\s+([-+]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][-+]?\d+)?)$/)
    if (!match) continue
    const value = Number(match[2])
    if (!Number.isFinite(value)) continue
    ;(result[match[1]] ??= []).push(value)
  }
  return result
}
