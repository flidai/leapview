import { chromium, type Page } from '@playwright/test'
import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { dirname, join } from 'node:path'

type Sample = {
  interaction: string
  iteration: number
  refreshId: string
  generation: number
  optimisticFeedbackMs: number
  firstTargetPaintMs: number
  criticalKPISettlementMs: number
  allTargetSettlementMs: number
  targets: string[]
  excludedTargets: string[]
  targetUpdates: TargetUpdate[]
  tableRowsBeforeCount: boolean | null
  selectedValueTypes: string[]
}

export type TargetUpdate = {
  target: string
  order: number
  atMs?: number
  batch?: number
  tableStart?: number
  tableRows?: number
	cardinalityKind?: 'unknown' | 'lower_bound' | 'estimated' | 'exact'
	cardinalityValue?: number
	chunkSize?: number
}

export type InteractionTrace = Pick<Sample, 'targets' | 'excludedTargets' | 'targetUpdates'>

export type PerformanceThresholds = {
  optimisticFeedbackP95Ms: number
  firstTargetPaintP95Ms: number
  criticalKPISettlementP95Ms: number
  allTargetSettlementP95Ms: number
}

type RefreshSummary = {
  refreshId: string
  generation?: number
  queryCount: number | null
  cancellationCount: number | null
  cancellationReason?: string
  outcome?: string
  stageTimingsMs: Record<string, number>
}

const projectRoot = process.cwd()
const logPath = Bun.env.LEAPVIEW_PERF_LOG ?? join(projectRoot, '.tmp/dev-server.log')
const iterations = positiveInteger(Bun.env.LEAPVIEW_PERF_ITERATIONS, 5)

type PerformanceSuite = {
	name: string
	dashboardPath: string
	scenarios: Array<{ name: string; visualId: string }>
	rapidToggleScenario: string
}

export function percentile(values: number[], percentileRank: number): number {
  if (values.length === 0) return 0
  const sorted = [...values].sort((left, right) => left - right)
  const index = Math.max(0, Math.ceil((percentileRank / 100) * sorted.length) - 1)
  return round(sorted[Math.min(index, sorted.length - 1)])
}

export function parseRefreshSummaries(log: string, refreshIDs?: ReadonlySet<string>): RefreshSummary[] {
  const summaries: RefreshSummary[] = []
  for (const line of log.split('\n')) {
    const summary = parseJSONRefreshSummary(line) ?? parseSlogRefreshSummary(line)
    if (summary && (!refreshIDs || refreshIDs.has(summary.refreshId))) summaries.push(summary)
  }
  return summaries
}

export function logTextAfterCursor(log: Uint8Array, cursor: number): string {
	const start = cursor >= 0 && cursor <= log.byteLength ? cursor : 0
	return new TextDecoder().decode(log.subarray(start))
}

function parseJSONRefreshSummary(line: string): RefreshSummary | null {
  const objectStart = line.indexOf('{')
  if (objectStart < 0) return null
  let value: Record<string, unknown>
  try {
    value = JSON.parse(line.slice(objectStart)) as Record<string, unknown>
  } catch {
    return null
  }
  const event = stringField(value, 'event') || stringField(value, 'message')
  if (event !== 'dashboard_refresh' && event !== 'dashboard refresh') return null
  const refreshId = stringField(value, 'refreshId') || stringField(value, 'refresh_id')
  if (!refreshId) return null
  const generation = numberField(value, 'generation')
  const cancellationReason = stringField(value, 'cancellationReason') || stringField(value, 'cancellation_reason')
  const outcome = stringField(value, 'outcome')
  return {
    refreshId,
    ...(generation === null ? {} : { generation }),
    queryCount: numberField(value, 'queryCount', 'query_count'),
    cancellationCount: numberField(value, 'cancellationCount', 'cancellation_count', 'cancellations'),
    ...(cancellationReason ? { cancellationReason } : {}),
    ...(outcome ? { outcome } : {}),
    stageTimingsMs: timingFields(value.stageTimingsMs ?? value.stage_timings_ms ?? value.stageTimings),
  }
}

function parseSlogRefreshSummary(line: string): RefreshSummary | null {
  const event = slogField(line, 'event') || slogField(line, 'msg')
  if (event !== 'dashboard_refresh' && event !== 'dashboard refresh') return null
  const refreshId = slogField(line, 'refreshId') || slogField(line, 'refresh_id')
  if (!refreshId) return null
  const generation = slogNumberField(line, 'generation')
  const cancellationReason = slogField(line, 'cancellationReason') || slogField(line, 'cancellation_reason')
  const outcome = slogField(line, 'outcome')
  return {
    refreshId,
    ...(generation === null ? {} : { generation }),
    queryCount: slogNumberField(line, 'queryCount', 'query_count'),
    cancellationCount: slogNumberField(line, 'cancellationCount', 'cancellation_count', 'cancellations'),
    ...(cancellationReason ? { cancellationReason } : {}),
    ...(outcome ? { outcome } : {}),
    stageTimingsMs: slogTimingFields(line),
  }
}

function slogField(line: string, name: string): string {
  const escaped = name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  const match = line.match(new RegExp(`(?:^|\\s)${escaped}=(?:"((?:\\\\.|[^"])*)"|([^\\s]+))`))
  if (!match) return ''
  return match[1] === undefined ? match[2] : match[1].replace(/\\"/g, '"').replace(/\\\\/g, '\\')
}

function slogNumberField(line: string, ...names: string[]): number | null {
  for (const name of names) {
    const raw = slogField(line, name)
    if (!raw) continue
    const value = Number(raw)
    if (Number.isFinite(value)) return value
  }
  return null
}

function slogTimingFields(line: string): Record<string, number> {
	const raw = slogField(line, 'stageTimingsMs') || slogField(line, 'stage_timings_ms') || slogField(line, 'stageTimings')
	const match = raw.match(/^map\[([^\]]*)\]$/)
	if (!match) return {}
  const fields: Record<string, number> = {}
  for (const entry of match[1].trim().split(/\s+/)) {
    const separator = entry.lastIndexOf(':')
    if (separator <= 0) continue
    const value = Number(entry.slice(separator + 1))
    if (Number.isFinite(value)) fields[entry.slice(0, separator)] = value
  }
  return fields
}

export function aggregateSamples(samples: Sample[], summaries: RefreshSummary[]) {
  const stageValues = new Map<string, number[]>()
  const cancellationCounts = summaries.map((summary) => summary.cancellationCount).filter((value): value is number => value !== null)
  for (const summary of summaries) {
    for (const [name, duration] of Object.entries(summary.stageTimingsMs)) {
      stageValues.set(name, [...(stageValues.get(name) ?? []), duration])
    }
  }
  return {
    optimisticFeedbackMs: timingStats(samples.map((sample) => sample.optimisticFeedbackMs)),
    firstTargetPaintMs: timingStats(samples.map((sample) => sample.firstTargetPaintMs)),
    criticalKPISettlementMs: timingStats(samples.map((sample) => sample.criticalKPISettlementMs)),
    allTargetSettlementMs: timingStats(samples.map((sample) => sample.allTargetSettlementMs)),
    queryCounts: summaries.map((summary) => summary.queryCount).filter((value): value is number => value !== null),
    cancellations: cancellationCounts.length > 0 ? cancellationCounts.reduce((sum, value) => sum + value, 0) : null,
    stageTimingsMs: Object.fromEntries([...stageValues].sort(([left], [right]) => left.localeCompare(right)).map(([name, values]) => [name, timingStats(values)])),
  }
}

export function assertInteractionTrace(trace: InteractionTrace): string[] {
  const errors: string[] = []
  const counts = new Map<string, number>()
  for (const update of trace.targetUpdates) counts.set(update.target, (counts.get(update.target) ?? 0) + 1)
  for (const target of trace.targets) {
    const updates = trace.targetUpdates.filter((update) => update.target === target)
    if (target.startsWith('visual:') && updates.length !== 1) {
      errors.push(`${target} updated ${updates.length} times, want exactly 1`)
    }
	if (target.startsWith('table:')) {
		if (!isValidTableDelivery(updates)) errors.push(`${target} did not publish one bounded window or rows followed by an exact count`)
	}
  }
  for (const target of trace.excludedTargets) {
    const count = counts.get(target) ?? 0
    if (count > 0) errors.push(`excluded target ${target} updated ${count} time${count === 1 ? '' : 's'}`)
  }
  return errors
}

function isValidTableDelivery(updates: TargetUpdate[]): boolean {
	if (updates.length === 1) {
		const [window] = updates
		return window.tableStart === 0
			&& typeof window.tableRows === 'number'
			&& (window.cardinalityKind === 'lower_bound' || window.cardinalityKind === 'exact')
			&& (window.cardinalityValue ?? -1) >= window.tableRows
	}
	if (updates.length !== 2) return false
	const [rows, count] = updates
	return rows.tableStart === 0
		&& count.tableStart === 0
		&& rows.cardinalityKind !== 'exact'
		&& count.cardinalityKind === 'exact'
		&& typeof rows.tableRows === 'number'
		&& count.tableRows === rows.tableRows
		&& (count.cardinalityValue ?? -1) >= rows.tableRows
		&& rows.order < count.order
}

export function evaluateThresholds(
  result: Pick<ReturnType<typeof aggregateSamples>, 'optimisticFeedbackMs' | 'firstTargetPaintMs' | 'criticalKPISettlementMs' | 'allTargetSettlementMs'>,
  thresholds: PerformanceThresholds,
): string[] {
  const phases: Array<[string, number, number]> = [
    ['optimistic feedback', result.optimisticFeedbackMs.p95, thresholds.optimisticFeedbackP95Ms],
    ['first target paint', result.firstTargetPaintMs.p95, thresholds.firstTargetPaintP95Ms],
    ['critical KPI settlement', result.criticalKPISettlementMs.p95, thresholds.criticalKPISettlementP95Ms],
    ['all-target settlement', result.allTargetSettlementMs.p95, thresholds.allTargetSettlementP95Ms],
  ]
  return phases
    .filter(([, actual, limit]) => actual > limit)
    .map(([name, actual, limit]) => `${name} p95 ${actual}ms exceeds ${limit}ms`)
}

export function classifyRapidSupersessionNetworkFailures(
  failures: string[],
  supersessionProven: boolean,
): { expectedAborts: string[]; unexpectedFailures: string[] } {
  const expectedAborts: string[] = []
  const unexpectedFailures: string[] = []
  const selectAbort = /^net::ERR_ABORTED POST https?:\/\/[^/]+\/dashboards\/[^/]+\/commands\/select$/
  for (const failure of failures) {
    if (supersessionProven && expectedAborts.length === 0 && selectAbort.test(failure)) {
      expectedAborts.push(failure)
    } else {
      unexpectedFailures.push(failure)
    }
  }
  return { expectedAborts, unexpectedFailures }
}

export async function runPerformanceSuite(): Promise<void> {
	const suite = await loadPerformanceSuite()
	const scenarios = suite.scenarios
	const outputPath = Bun.env.LEAPVIEW_PERF_OUTPUT ?? join(projectRoot, `.tmp/${suite.name}-performance.json`)
	const baseURL = await resolveBaseURL()
	const logCursor = await refreshLogCursor()
	const dashboardURL = new URL(suite.dashboardPath, baseURL).toString()
  const browser = await chromium.launch()
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  const browserHealth = collectBrowserHealth(page)
  const samples: Sample[] = []
  let rapidToggle: RapidToggleResult | null = null
  let rapidNetworkFailures: string[] = []
  try {
    const response = await page.goto(dashboardURL, { waitUntil: 'domcontentloaded', timeout: 120_000 })
	if (!response?.ok()) throw new Error(`${suite.name} dashboard returned status ${response?.status() ?? 'unknown'}`)
    await waitForDashboardIdle(page, 600_000)
    await installPerformanceObserver(page)

    // Warm the compiled runtime, filter-option cache, reader pool, and every fixed interaction shape.
    for (const scenario of scenarios) {
      await runInteraction(page, scenario.visualId, 0)
      await clearSelections(page)
    }

    for (const scenario of scenarios) {
      for (let iteration = 0; iteration < iterations; iteration++) {
        const sample = await runInteraction(page, scenario.visualId, iteration)
        samples.push({ interaction: scenario.name, iteration: iteration + 1, ...sample })
        await clearSelections(page)
      }
    }
    const rapidNetworkStart = browserHealth.failedNetworkResponses.length
	const rapidScenario = scenarios.find((scenario) => scenario.name === suite.rapidToggleScenario) ?? scenarios.at(-1)
	if (!rapidScenario) throw new Error('performance suite requires at least one scenario')
	rapidToggle = await runRapidToggle(page, rapidScenario.visualId)
    rapidNetworkFailures = browserHealth.failedNetworkResponses.splice(rapidNetworkStart)
    await clearSelections(page)
  } finally {
    await browser.close()
  }

  const allSummaries = await readRefreshSummaries(undefined, logCursor)
  const refreshIDs = new Set(samples.map((sample) => sample.refreshId).filter(Boolean))
  const summaries = allSummaries.filter((summary) => refreshIDs.has(summary.refreshId))
  const byInteraction = Object.fromEntries(scenarios.map((scenario) => {
    const selected = samples.filter((sample) => sample.interaction === scenario.name)
    const selectedIDs = new Set(selected.map((sample) => sample.refreshId))
    return [scenario.name, aggregateSamples(selected, summaries.filter((summary) => selectedIDs.has(summary.refreshId)))]
  }))
  const overall = aggregateSamples(samples, summaries)
  const rapidSummaries = rapidToggle
    ? allSummaries.filter((summary) => (summary.generation ?? -1) > rapidToggle!.generationBefore && (summary.generation ?? Infinity) <= rapidToggle!.generation)
    : []
  if (rapidToggle) {
    rapidToggle.cancellationObserved = rapidSummaries.some((summary) => summary.outcome === 'canceled' && summary.cancellationReason === 'superseded')
    rapidToggle.queryCounts = rapidSummaries.map((summary) => summary.queryCount).filter((value): value is number => value !== null)
  }
  const canceledRapidGenerationObserved = Boolean(rapidToggle && rapidSummaries.some((summary) =>
    summary.generation === rapidToggle!.generationBefore + 1
    && summary.outcome === 'canceled'
    && summary.cancellationReason === 'superseded',
  ))
  const completedRapidGenerationObserved = Boolean(rapidToggle && rapidSummaries.some((summary) =>
    summary.generation === rapidToggle!.generation && summary.outcome === 'complete',
  ))
  const rapidSupersessionProven = Boolean(
    rapidToggle
    && rapidToggle.commandRequests === 2
    && rapidToggle.finalSelectionMatchesB
    && canceledRapidGenerationObserved
    && completedRapidGenerationObserved,
  )
  const rapidNetworkHealth = classifyRapidSupersessionNetworkFailures(rapidNetworkFailures, rapidSupersessionProven)
  browserHealth.expectedRapidSupersessionAborts.push(...rapidNetworkHealth.expectedAborts)
  browserHealth.failedNetworkResponses.push(...rapidNetworkHealth.unexpectedFailures)
  const correctnessFailures = samples.flatMap((sample) =>
    assertInteractionTrace(sample).map((failure) => `${sample.interaction} iteration ${sample.iteration}: ${failure}`),
  )
  if (rapidToggle && !rapidToggle.cancellationObserved) correctnessFailures.push('rapid A→B interaction did not record a superseded generation')
  if (rapidToggle && (!canceledRapidGenerationObserved || !completedRapidGenerationObserved)) correctnessFailures.push('rapid A→B interaction did not record both canceled A and completed B generations')
  if (rapidToggle && !rapidToggle.finalSelectionMatchesB) correctnessFailures.push('rapid A→B interaction did not retain the typed B selection')
  if (rapidToggle && rapidToggle.commandRequests !== 2) correctnessFailures.push(`rapid A→B issued ${rapidToggle.commandRequests} commands, want 2`)
  correctnessFailures.push(...browserHealth.consoleErrors.map((message) => `browser console: ${message}`))
  correctnessFailures.push(...browserHealth.failedNetworkResponses.map((message) => `network: ${message}`))

  const thresholdMode = booleanEnvironment(Bun.env.LEAPVIEW_PERF_ENFORCE_THRESHOLDS)
  const thresholdLimits = performanceThresholdsFromEnvironment()
  const thresholdFailures = thresholdMode ? evaluateThresholds(overall, thresholdLimits) : []
  const maxQueries = positiveInteger(Bun.env.LEAPVIEW_PERF_MAX_QUERIES, 4)
  if (thresholdMode) {
    for (const summary of summaries) {
      if (summary.queryCount !== null && summary.queryCount > maxQueries) {
        thresholdFailures.push(`refresh ${summary.refreshId} executed ${summary.queryCount} queries, exceeds ${maxQueries}`)
      }
    }
  }
  const result = {
    generatedAt: new Date().toISOString(),
    baseURL,
    iterations,
    samples,
    overall,
    interactions: byInteraction,
    rapidToggle: rapidToggle ? { ...rapidToggle, summaries: rapidSummaries } : null,
    browserHealth,
    assertions: {
      deterministicFailures: correctnessFailures,
      thresholds: { enabled: thresholdMode, limits: thresholdLimits, maxQueries, failures: thresholdFailures },
    },
    observability: {
      refreshSummariesFound: summaries.length,
      refreshSummariesExpected: refreshIDs.size,
      logPath,
    },
  }
  await mkdir(dirname(outputPath), { recursive: true })
  await writeFile(outputPath, `${JSON.stringify(result, null, 2)}\n`)
  console.log(JSON.stringify(result, null, 2))
	if (correctnessFailures.length > 0) throw new Error(`${suite.name} deterministic QA failed:\n${correctnessFailures.join('\n')}`)
	if (thresholdFailures.length > 0) throw new Error(`${suite.name} performance thresholds failed:\n${thresholdFailures.join('\n')}`)
}

async function runInteraction(page: Page, visualId: string, datumOffset: number): Promise<Omit<Sample, 'interaction' | 'iteration'>> {
  const before = await dashboardStatus(page)
  const input = await interactionInput(page, visualId, datumOffset)
  await beginPerformanceTrace(page, input)
  await page.locator('lv-dashboard-page').evaluate((element: any, command) => {
    element.dispatchEvent(new CustomEvent('lv-interaction-select', {
      bubbles: true,
      composed: true,
      detail: command,
    }))
  }, input.command)

  await page.waitForFunction(() => (window as any).__ldPerfObserver?.active?.optimisticAt !== null, undefined, { timeout: 10_000 })
  const accepted = await waitForStatus(page, (status) => refreshWasAccepted(before.generation, status), 10_000)
  const settled = accepted.loading
    ? await waitForStatus(page, (status) => status.generation === accepted.generation && !status.loading, 600_000)
    : accepted
  const trace = await finishPerformanceTrace(page)
  return {
    refreshId: settled.refreshId,
    generation: settled.generation,
    optimisticFeedbackMs: trace.optimisticFeedbackMs,
    firstTargetPaintMs: trace.firstTargetPaintMs,
    criticalKPISettlementMs: trace.criticalKPISettlementMs,
    allTargetSettlementMs: trace.allTargetSettlementMs,
    targets: trace.targets,
    excludedTargets: trace.excludedTargets,
    targetUpdates: trace.targetUpdates,
    tableRowsBeforeCount: trace.tableRowsBeforeCount,
    selectedValueTypes: trace.selectedValueTypes,
  }
}

type InteractionInput = {
  visualId: string
  command: {
    sourceKind: 'visual'
    sourceId: string
    interactionKind: string
    action: 'replace'
    toggle: false
    mappings: Array<{ field: string; fact?: string; grain?: string; value: string | number | boolean | null; label: string }>
  }
  targets: string[]
}

type PerformanceTraceResult = {
  optimisticFeedbackMs: number
  firstTargetPaintMs: number
  criticalKPISettlementMs: number
  allTargetSettlementMs: number
  targets: string[]
  excludedTargets: string[]
  targetUpdates: TargetUpdate[]
  tableRowsBeforeCount: boolean | null
  selectedValueTypes: string[]
}

type RapidToggleResult = PerformanceTraceResult & {
  generationBefore: number
  generation: number
  refreshId: string
  commandRequests: number
  finalSelectionMatchesB: boolean
  cancellationObserved: boolean
  queryCounts: number[]
}

async function interactionInput(page: Page, visualId: string, datumOffset: number): Promise<InteractionInput> {
  return page.locator('lv-dashboard-page').evaluate((element: any, input) => {
    const host = Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []).find((candidate: any) => candidate.envelope?.visualID === input.visualId) as any
    const payload = host?.envelope
    if (payload?.dataState?.kind !== 'inline') throw new Error(`visual ${input.visualId} is not inline`)
    const dataset = payload.dataState.datasets[0]
    const rows = dataset?.rows ?? []
    if (rows.length === 0) throw new Error(`visual ${input.visualId} has no data`)
    const values = rows[input.datumOffset % rows.length]
    const datum = Object.fromEntries((dataset.columns ?? []).map((column: string, index: number) => [column, values[index]]))
    const interaction = payload.spec.interactions.find((candidate: any) => candidate.kind === 'select')
    const mappings = (interaction?.mappings ?? []).map((mapping: any) => {
      const value = datum[mapping.source.field]
      if (value === undefined || (typeof value === 'object' && value !== null)) throw new Error(`visual ${input.visualId} mapping ${mapping.targetFieldID} has no scalar value`)
      const labelValue = mapping.label ? datum[mapping.label.field] : value
      return {
        field: mapping.targetFieldID,
        ...(mapping.targetFactID !== undefined ? { fact: mapping.targetFactID } : {}),
        ...(mapping.grain !== undefined ? { grain: mapping.grain } : {}),
        value,
        label: labelValue === null ? '' : String(labelValue),
      }
    })
    return {
      visualId: input.visualId,
      command: {
        sourceKind: 'visual' as const,
        sourceId: input.visualId,
        interactionKind: payload.interaction?.kind || 'point_selection',
        action: 'replace' as const,
        toggle: false as const,
        mappings,
      },
      targets: [...(payload.interaction?.targets ?? [])],
    }
  }, { visualId, datumOffset })
}

async function installPerformanceObserver(page: Page): Promise<void> {
  await page.locator('lv-dashboard-page').evaluate((element: any) => {
    if ((window as any).__ldPerfObserver) return
    const observer: any = {
      active: null,
      capture() {
        const active = observer.active
        if (!active) return
        const signals = element.signals ?? {}
        active.batch += 1
        for (const target of [...active.targets, ...active.excludedTargets]) {
          const componentStatus = signals.componentStatus?.[target]
          let signature = ''
          let tableStart: number | undefined
          let tableRows: number | undefined
			let cardinalityKind: TargetUpdate['cardinalityKind']
			let cardinalityValue: number | undefined
			let chunkSize: number | undefined
          if (target.startsWith('visual:')) {
            const visual = signals.visuals?.[target.slice(7)]
            signature = visual ? JSON.stringify([
              componentStatus?.generation ?? null,
              Boolean(componentStatus?.loading),
              visual.version ?? null,
              visual.data ?? null,
            ]) : ''
          } else if (target.startsWith('table:')) {
            const table = signals.tables?.[target.slice(6)]
            const block = table?.blocks?.a
            tableStart = typeof block?.start === 'number' ? block.start : undefined
            tableRows = Array.isArray(block?.rows) ? block.rows.length : 0
				cardinalityKind = table?.cardinality?.kind
				cardinalityValue = typeof table?.cardinality?.value === 'number' ? table.cardinality.value : undefined
				chunkSize = typeof table?.chunkSize === 'number' ? table.chunkSize : undefined
            signature = table ? JSON.stringify([
              componentStatus?.generation ?? null,
              Boolean(componentStatus?.loading),
              table.version ?? null,
              block?.requestSeq ?? null,
              tableStart ?? null,
              tableRows,
					cardinalityKind ?? null,
					cardinalityValue ?? null,
            ]) : ''
          }
          if (!signature || active.signatures[target] === signature) continue
          if (componentStatus?.loading) {
            active.signatures[target] = signature
            continue
          }
          if (active.initialized) {
            active.order += 1
            active.targetUpdates.push({
              target,
              order: active.order,
              atMs: Math.round((performance.now() - active.startedAt) * 100) / 100,
              batch: active.batch,
				...(target.startsWith('table:') ? { tableStart, tableRows, cardinalityKind, cardinalityValue, chunkSize } : {}),
            })
          }
          active.signatures[target] = signature
        }
        active.initialized = true

        const source = Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []).find((candidate: any) => candidate.envelope?.visualID === active.visualId) as any
        const envelope = source?.envelope
        const interaction = envelope?.spec?.interactions?.find((candidate: any) => candidate.kind === 'select')
        const selected = (envelope?.selection ?? []).flatMap((entry: any) => (interaction?.mappings ?? []).map((mapping: any) => ({
          field: mapping.targetFieldID, fact: mapping.targetFactID, grain: mapping.grain, value: entry.datum?.identity?.[mapping.source.field],
        })))
        const matches = active.expectedMappings.every((expected: any) => selected.some((actual: any) =>
          actual.field === expected.field
          && (actual.fact ?? '') === (expected.fact ?? '')
          && (actual.grain ?? '') === (expected.grain ?? '')
          && Object.is(actual.value, expected.value),
        ))
        if (matches && active.optimisticAt === null) {
          active.optimisticAt = performance.now()
          active.selectedValueTypes = selected.map((mapping: any) => mapping.value === null ? 'null' : typeof mapping.value)
        }
      },
      begin(input: any) {
        const signals = element.signals ?? {}
        const components = signals.page?.components ?? []
        const keyFor = (id: string) => {
          const component = components.find((candidate: any) => candidate.visual === id || candidate.table === id)
          return component?.table ? `table:${component.table}` : component?.visual ? `visual:${component.visual}` : ''
        }
        const targets = input.targets.map(keyFor).filter(Boolean)
        const allTargets = components.map((component: any) => component.table ? `table:${component.table}` : component.visual ? `visual:${component.visual}` : '').filter(Boolean)
        const criticalKPIs = input.targets
          .filter((id: string) => signals.visuals?.[id]?.shape === 'single_value')
          .map((id: string) => `visual:${id}`)
        observer.active = {
          visualId: input.visualId,
          expectedMappings: input.command.mappings,
          targets,
          excludedTargets: allTargets.filter((target: string) => !targets.includes(target)),
          criticalKPIs,
          signatures: {},
          targetUpdates: [],
          order: 0,
          batch: 0,
          initialized: false,
          optimisticAt: null,
          selectedValueTypes: [],
          startedAt: performance.now(),
        }
        observer.capture()
      },
      finish() {
        observer.capture()
        const active = observer.active
        observer.active = null
        return active
      },
    }
    const originalRequestUpdate = element.requestUpdate.bind(element)
    element.requestUpdate = (...args: any[]) => {
      observer.capture()
      const result = originalRequestUpdate(...args)
      queueMicrotask(() => Promise.resolve(element.updateComplete).then(() => observer.capture()))
      return result
    }
    ;(window as any).__ldPerfObserver = observer
  })
}

async function beginPerformanceTrace(page: Page, input: InteractionInput): Promise<void> {
  await page.evaluate((value) => (window as any).__ldPerfObserver.begin(value), input)
}

async function finishPerformanceTrace(page: Page): Promise<PerformanceTraceResult> {
  return page.evaluate(() => {
    const trace = (window as any).__ldPerfObserver.finish()
    const elapsed = (at: number | null) => at === null ? 0 : Math.round((at - trace.startedAt) * 100) / 100
    const targetUpdates = trace.targetUpdates as TargetUpdate[]
    const firstTargetPaintMs = targetUpdates.length > 0 ? targetUpdates[0].atMs ?? 0 : 0
    const criticalUpdates = targetUpdates.filter((update) => trace.criticalKPIs.includes(update.target))
    const tableUpdates = targetUpdates.filter((update) => update.target.startsWith('table:'))
	const rows = tableUpdates.find((update) => (update.tableRows ?? 0) > 0 && update.cardinalityKind !== 'exact')
	const count = tableUpdates.find((update) => update.cardinalityKind === 'exact')
    return {
      optimisticFeedbackMs: elapsed(trace.optimisticAt),
      firstTargetPaintMs,
      criticalKPISettlementMs: criticalUpdates.length > 0 ? Math.max(...criticalUpdates.map((update) => update.atMs ?? 0)) : firstTargetPaintMs,
      allTargetSettlementMs: Math.round((performance.now() - trace.startedAt) * 100) / 100,
      targets: trace.targets,
      excludedTargets: trace.excludedTargets,
      targetUpdates,
      tableRowsBeforeCount: tableUpdates.length === 0 ? null : Boolean(rows && count && rows.order < count.order),
      selectedValueTypes: trace.selectedValueTypes,
    }
  })
}

async function runRapidToggle(page: Page, visualId: string): Promise<RapidToggleResult> {
  const before = await dashboardStatus(page)
  const first = await interactionInput(page, visualId, 0)
  const second = await interactionInput(page, visualId, 1)
  if (JSON.stringify(first.command.mappings) === JSON.stringify(second.command.mappings)) throw new Error(`${visualId} needs two distinct rows for rapid-toggle QA`)
  await beginPerformanceTrace(page, second)
  let commandRequests = 0
  const onRequest = (request: { url(): string; method(): string }) => {
    const url = new URL(request.url())
    if (request.method() === 'POST' && url.pathname.endsWith('/commands/select')) commandRequests += 1
  }
  page.on('request', onRequest)
  try {
    await page.locator('lv-dashboard-page').evaluate((element: any, commands) => {
      for (const command of commands) {
        element.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: command }))
      }
    }, [first.command, second.command])
    const settled = await waitForStatus(page, (status) => status.generation >= before.generation + 2 && !status.loading, 600_000)
    const trace = await finishPerformanceTrace(page)
    const finalSelectionMatchesB = await page.locator('lv-dashboard-page').evaluate((element: any, expected) => {
      const host = Array.from(element.shadowRoot?.querySelectorAll('lv-visualization-host') ?? []).find((candidate: any) => candidate.envelope?.visualID === expected.visualId) as any
      const envelope = host?.envelope
      const interaction = envelope?.spec?.interactions?.find((candidate: any) => candidate.kind === 'select')
      const selected = (envelope?.selection ?? []).flatMap((entry: any) => (interaction?.mappings ?? []).map((mapping: any) => ({
        field: mapping.targetFieldID, fact: mapping.targetFactID, grain: mapping.grain, value: entry.datum?.identity?.[mapping.source.field],
      })))
      return expected.command.mappings.every((mapping: any) => selected.some((candidate: any) =>
        candidate.field === mapping.field
        && (candidate.fact ?? '') === (mapping.fact ?? '')
        && (candidate.grain ?? '') === (mapping.grain ?? '')
        && Object.is(candidate.value, mapping.value)
        && (candidate.value === null ? 'null' : typeof candidate.value) === (mapping.value === null ? 'null' : typeof mapping.value),
      ))
    }, second)
    return {
      ...trace,
      generationBefore: before.generation,
      generation: settled.generation,
      refreshId: settled.refreshId,
      commandRequests,
      finalSelectionMatchesB,
      cancellationObserved: false,
      queryCounts: [],
    }
  } finally {
    page.off('request', onRequest)
  }
}

async function clearSelections(page: Page): Promise<void> {
  const status = await dashboardStatus(page)
  await page.locator('lv-dashboard-page').evaluate((element: Element) => {
    element.dispatchEvent(new CustomEvent('lv-selection-clear', { bubbles: true, composed: true }))
  })
  await waitForStatus(page, (next) => next.generation > status.generation && !next.loading, 600_000)
}

async function waitForDashboardIdle(page: Page, timeoutMs: number): Promise<void> {
  await page.waitForSelector('lv-dashboard-page')
  await waitForStatus(page, (status) => status.generation > 0 && !status.loading, timeoutMs)
}

export type DashboardStatusSnapshot = { refreshId: string; generation: number; loading: boolean }

export function refreshWasAccepted(beforeGeneration: number, status: DashboardStatusSnapshot): boolean {
  return status.generation > beforeGeneration
}

async function dashboardStatus(page: Page): Promise<DashboardStatusSnapshot> {
  return page.locator('lv-dashboard-page').evaluate((element: any) => ({
    refreshId: String(element.status?.refreshId ?? ''),
    generation: Number(element.status?.generation ?? 0),
    loading: Boolean(element.status?.loading),
  }))
}

async function waitForStatus(page: Page, predicate: (status: DashboardStatusSnapshot) => boolean, timeoutMs: number): Promise<DashboardStatusSnapshot> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const status = await dashboardStatus(page)
    if (predicate(status)) return status
    await page.waitForTimeout(10)
  }
  throw new Error(`timed out after ${timeoutMs}ms waiting for dashboard refresh state`)
}

function collectBrowserHealth(page: Page): { consoleErrors: string[]; failedNetworkResponses: string[]; expectedRapidSupersessionAborts: string[] } {
  const health = {
    consoleErrors: [] as string[],
    failedNetworkResponses: [] as string[],
    expectedRapidSupersessionAborts: [] as string[],
  }
  page.on('console', (message) => {
    if (message.type() === 'error' || (message.type() === 'warning' && message.text().includes('[LeapView]'))) {
      health.consoleErrors.push(`${message.type()}: ${message.text()}`)
    }
  })
  page.on('response', (response) => {
    if (response.status() >= 400) health.failedNetworkResponses.push(`${response.status()} ${response.request().method()} ${response.url()}`)
  })
  page.on('requestfailed', (request) => {
    if (new URL(request.url()).pathname === '/updates') return
    health.failedNetworkResponses.push(`${request.failure()?.errorText ?? 'request failed'} ${request.method()} ${request.url()}`)
  })
  return health
}

async function resolveBaseURL(): Promise<string> {
  if (Bun.env.LEAPVIEW_BASE_URL) return Bun.env.LEAPVIEW_BASE_URL
  try {
    const port = (await readFile(join(projectRoot, '.tmp/dev-server.port'), 'utf8')).trim()
    if (/^\d+$/.test(port)) return `http://localhost:${port}`
  } catch {}
  return 'http://localhost:8195'
}

async function loadPerformanceSuite(): Promise<PerformanceSuite> {
	const path = Bun.env.LEAPVIEW_PERF_SCENARIO ?? join(projectRoot, 'scripts/performance/movielens.json')
	const value = JSON.parse(await readFile(path, 'utf8')) as Partial<PerformanceSuite>
	if (!value.name || !value.dashboardPath || !Array.isArray(value.scenarios) || value.scenarios.length === 0) {
		throw new Error(`invalid dashboard performance scenario ${path}`)
	}
	for (const scenario of value.scenarios) {
		if (!scenario?.name || !scenario.visualId) throw new Error(`invalid interaction in dashboard performance scenario ${path}`)
	}
	return {
		name: value.name,
		dashboardPath: value.dashboardPath,
		scenarios: value.scenarios,
		rapidToggleScenario: value.rapidToggleScenario ?? value.scenarios[value.scenarios.length - 1].name,
	}
}

async function refreshLogCursor(): Promise<number> {
	try {
		return (await readFile(logPath)).byteLength
	} catch {
		return 0
	}
}

async function readRefreshSummaries(refreshIDs: ReadonlySet<string> | undefined, cursor: number): Promise<RefreshSummary[]> {
	try {
		const log = await readFile(logPath)
		return parseRefreshSummaries(logTextAfterCursor(log, cursor), refreshIDs)
	} catch {
    return []
  }
}

function timingStats(values: number[]) {
  return { samples: values.length, p50: percentile(values, 50), p95: percentile(values, 95) }
}

function positiveInteger(raw: string | undefined, fallback: number): number {
  const value = Number(raw)
  return Number.isInteger(value) && value > 0 ? value : fallback
}

function positiveNumber(raw: string | undefined, fallback: number): number {
  const value = Number(raw)
  return Number.isFinite(value) && value > 0 ? value : fallback
}

function booleanEnvironment(raw: string | undefined): boolean {
  return /^(1|true|yes|on)$/i.test(raw?.trim() ?? '')
}

function performanceThresholdsFromEnvironment(): PerformanceThresholds {
  return {
    optimisticFeedbackP95Ms: positiveNumber(Bun.env.LEAPVIEW_PERF_MAX_OPTIMISTIC_FEEDBACK_P95_MS, 16),
    firstTargetPaintP95Ms: positiveNumber(Bun.env.LEAPVIEW_PERF_MAX_FIRST_TARGET_PAINT_P95_MS, 500),
    criticalKPISettlementP95Ms: positiveNumber(Bun.env.LEAPVIEW_PERF_MAX_CRITICAL_KPI_P95_MS, 1_000),
    allTargetSettlementP95Ms: positiveNumber(Bun.env.LEAPVIEW_PERF_MAX_ALL_TARGET_P95_MS, 1_000),
  }
}

function stringField(value: Record<string, unknown>, name: string): string {
  return typeof value[name] === 'string' ? value[name] as string : ''
}

function numberField(value: Record<string, unknown>, ...names: string[]): number | null {
  for (const name of names) {
    if (typeof value[name] === 'number' && Number.isFinite(value[name])) return value[name] as number
  }
  return null
}

function timingFields(value: unknown): Record<string, number> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return Object.fromEntries(Object.entries(value).filter((entry): entry is [string, number] => typeof entry[1] === 'number' && Number.isFinite(entry[1])))
}

function round(value: number): number {
  return Math.round(value * 100) / 100
}

if (import.meta.main) await runPerformanceSuite()
