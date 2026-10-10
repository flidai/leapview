import { expect, test } from 'bun:test'
import { interactionCommandForRowIndex } from '../web/components/dashboard/visualization/interaction-command'
import type { VisualizationEnvelope } from '../web/generated/visualization'
import {
  aggregateSamples,
  interactionInput,
  installPerformanceObserver,
  assertInteractionTrace,
  classifyRapidSupersessionNetworkFailures,
  evaluateThresholds,
  logTextAfterCursor,
  parseRefreshSummaries,
  percentile,
  refreshWasAccepted,
} from './movielens_performance'

test('percentile uses nearest-rank values', () => {
  expect(percentile([5, 1, 3, 2, 4], 50)).toBe(3)
  expect(percentile([5, 1, 3, 2, 4], 95)).toBe(5)
  expect(percentile([], 95)).toBe(0)
})

test('refresh acceptance does not require observing a transient loading state', () => {
  expect(refreshWasAccepted(7, { refreshId: 'refresh-8', generation: 8, loading: true })).toBe(true)
  expect(refreshWasAccepted(7, { refreshId: 'refresh-8', generation: 8, loading: false })).toBe(true)
  expect(refreshWasAccepted(7, { refreshId: 'refresh-7', generation: 7, loading: false })).toBe(false)
})

test('log cursor excludes refresh IDs from earlier server processes', () => {
	const previous = 'refresh-1 from previous process\n'
	const current = 'refresh-1 from current process\n'
	const bytes = new TextEncoder().encode(previous + current)
	expect(logTextAfterCursor(bytes, new TextEncoder().encode(previous).byteLength)).toBe(current)
	expect(logTextAfterCursor(bytes, bytes.byteLength + 1)).toBe(previous + current)
})

test('refresh summaries are correlated without accepting raw non-refresh logs', () => {
  const log = [
    '{"event":"dashboard_refresh","refreshId":"keep","queryCount":3,"cancellationCount":1,"stageTimingsMs":{"planning":4,"database":12}}',
    '{"event":"dashboard_refresh","refreshId":"ignore","queryCount":99}',
    'time=now level=INFO msg="unrelated"',
  ].join('\n')
  const summaries = parseRefreshSummaries(log, new Set(['keep']))
  expect(summaries).toEqual([{
    refreshId: 'keep',
    queryCount: 3,
    cancellationCount: 1,
    stageTimingsMs: { planning: 4, database: 12 },
  }])
  expect(aggregateSamples([{
    interaction: 'decade', iteration: 1, refreshId: 'keep', generation: 2,
    optimisticFeedbackMs: 4, firstTargetPaintMs: 40, criticalKPISettlementMs: 90, allTargetSettlementMs: 200,
    targets: ['visual:kpi'], excludedTargets: ['visual:source'], targetUpdates: [{ target: 'visual:kpi', order: 1 }],
    tableRowsBeforeCount: null, selectedValueTypes: ['number'],
  }], summaries)).toEqual({
    optimisticFeedbackMs: { samples: 1, p50: 4, p95: 4 },
    firstTargetPaintMs: { samples: 1, p50: 40, p95: 40 },
    criticalKPISettlementMs: { samples: 1, p50: 90, p95: 90 },
    allTargetSettlementMs: { samples: 1, p50: 200, p95: 200 },
    queryCounts: [3],
    cancellations: 1,
    stageTimingsMs: {
      database: { samples: 1, p50: 12, p95: 12 },
      planning: { samples: 1, p50: 4, p95: 4 },
    },
  })
})

test('refresh summaries accept the coordinator slog text format', () => {
  const log = [
    'time=2026-07-14T13:00:00.000Z level=INFO msg="dashboard refresh" event=dashboard_refresh refreshId=keep generation=3 queryCount=4 cancellationCount=2 stageTimingsMs="map[admissionWait:3 connectionWait:5 database:17 endToEnd:31 planning:6]" outcome=complete',
    'time=2026-07-14T13:00:01.000Z level=INFO msg="dashboard refresh" event=dashboard_refresh refreshId=ignore queryCount=99 cancellationCount=0 stageTimingsMs=map[endToEnd:1] outcome=complete',
  ].join('\n')

  expect(parseRefreshSummaries(log, new Set(['keep']))).toEqual([{
    refreshId: 'keep',
    generation: 3,
    queryCount: 4,
    cancellationCount: 2,
    outcome: 'complete',
    stageTimingsMs: {
      admissionWait: 3,
      connectionWait: 5,
      database: 17,
      endToEnd: 31,
      planning: 6,
    },
  }])
})

test('refresh summaries can return all post-cursor generations for rapid cancellation analysis', () => {
  const log = [
    '{"event":"dashboard_refresh","refreshId":"a","generation":8,"queryCount":0,"cancellationCount":1,"cancellationReason":"superseded","outcome":"canceled"}',
    '{"event":"dashboard_refresh","refreshId":"b","generation":9,"queryCount":2,"cancellationCount":0,"outcome":"complete"}',
  ].join('\n')
  expect(parseRefreshSummaries(log)).toEqual([
    {
      refreshId: 'a', generation: 8, queryCount: 0, cancellationCount: 1,
      cancellationReason: 'superseded', outcome: 'canceled', stageTimingsMs: {},
    },
    {
      refreshId: 'b', generation: 9, queryCount: 2, cancellationCount: 0,
      outcome: 'complete', stageTimingsMs: {},
    },
  ])
})

test('interaction trace accepts one bounded table window and no excluded updates', () => {
  expect(assertInteractionTrace({
    targets: ['visual:kpi', 'table:movies'],
    excludedTargets: ['visual:source'],
    targetUpdates: [
      { target: 'visual:kpi', order: 1 },
	  { target: 'table:movies', order: 2, tableStart: 0, tableRows: 50, cardinalityKind: 'lower_bound', cardinalityValue: 50, chunkSize: 50 },
    ],
  })).toEqual([])

  expect(assertInteractionTrace({
    targets: ['visual:kpi', 'table:movies'],
    excludedTargets: ['visual:source'],
    targetUpdates: [
      { target: 'visual:kpi', order: 1 },
      { target: 'visual:kpi', order: 2 },
	  { target: 'table:movies', order: 3, tableStart: 0, tableRows: 50, cardinalityKind: 'unknown', cardinalityValue: 0 },
      { target: 'visual:source', order: 4 },
    ],
  })).toEqual([
    'visual:kpi updated 2 times, want exactly 1',
	'table:movies did not publish one bounded window or rows followed by an exact count',
    'excluded target visual:source updated 1 time',
  ])

  expect(assertInteractionTrace({
    targets: ['table:movies'],
    excludedTargets: [],
    targetUpdates: [
	  { target: 'table:movies', order: 1, tableStart: 0, tableRows: 4, cardinalityKind: 'exact', cardinalityValue: 4, chunkSize: 50 },
    ],
  })).toEqual([])
})

test('interaction trace accepts explicit exact mode and rejects malformed delivery', () => {
  expect(assertInteractionTrace({
    targets: ['table:movies'],
    excludedTargets: [],
    targetUpdates: [
	  { target: 'table:movies', order: 1, tableStart: 0, tableRows: 4, cardinalityKind: 'unknown', cardinalityValue: 0 },
    ],
  })).toEqual([
	'table:movies did not publish one bounded window or rows followed by an exact count',
  ])

  expect(assertInteractionTrace({
    targets: ['table:movies'],
    excludedTargets: [],
    targetUpdates: [
	  { target: 'table:movies', order: 2, tableStart: 0, tableRows: 50, cardinalityKind: 'exact', cardinalityValue: 123 },
	  { target: 'table:movies', order: 3, tableStart: 0, tableRows: 50, cardinalityKind: 'lower_bound', cardinalityValue: 50 },
    ],
  })).toEqual([
	'table:movies did not publish one bounded window or rows followed by an exact count',
  ])

  expect(assertInteractionTrace({
    targets: ['table:movies'],
    excludedTargets: [],
    targetUpdates: [
	  { target: 'table:movies', order: 1, tableStart: 50, tableRows: 4, cardinalityKind: 'exact', cardinalityValue: 54 },
    ],
  })).toEqual([
	'table:movies did not publish one bounded window or rows followed by an exact count',
  ])
})

test('rapid supersession permits at most the first select abort after server cancellation is proven', () => {
  const abort = 'net::ERR_ABORTED POST http://localhost:8185/dashboards/ratings-overview/commands/select'
  expect(classifyRapidSupersessionNetworkFailures([abort], true)).toEqual({
    expectedAborts: [abort],
    unexpectedFailures: [],
  })
  expect(classifyRapidSupersessionNetworkFailures([abort], false)).toEqual({
    expectedAborts: [],
    unexpectedFailures: [abort],
  })
  expect(classifyRapidSupersessionNetworkFailures([abort, abort], true)).toEqual({
    expectedAborts: [abort],
    unexpectedFailures: [abort],
  })
  expect(classifyRapidSupersessionNetworkFailures([
    'net::ERR_FAILED POST http://localhost:8185/dashboards/ratings-overview/commands/select',
  ], true)).toEqual({
    expectedAborts: [],
    unexpectedFailures: ['net::ERR_FAILED POST http://localhost:8185/dashboards/ratings-overview/commands/select'],
  })
})

test('performance thresholds are opt-in and report every breached phase', () => {
  const result = {
    optimisticFeedbackMs: { samples: 5, p50: 3, p95: 12 },
    firstTargetPaintMs: { samples: 5, p50: 20, p95: 75 },
    criticalKPISettlementMs: { samples: 5, p50: 80, p95: 250 },
    allTargetSettlementMs: { samples: 5, p50: 120, p95: 480 },
  }
  expect(evaluateThresholds(result, {
    optimisticFeedbackP95Ms: 16,
    firstTargetPaintP95Ms: 100,
    criticalKPISettlementP95Ms: 300,
    allTargetSettlementP95Ms: 500,
  })).toEqual([])
  expect(evaluateThresholds(result, {
    optimisticFeedbackP95Ms: 10,
    firstTargetPaintP95Ms: 50,
    criticalKPISettlementP95Ms: 200,
    allTargetSettlementP95Ms: 400,
  })).toEqual([
    'optimistic feedback p95 12ms exceeds 10ms',
    'first target paint p95 75ms exceeds 50ms',
    'critical KPI settlement p95 250ms exceeds 200ms',
    'all-target settlement p95 480ms exceeds 400ms',
  ])
})

function envelope() {
  return {
    visualID: 'source', dataRevision: 4,
    spec: {
      kind: 'cartesian', datasets: [{ id: 'actual', fields: [
        { id: 'month', role: 'identity' }, { id: 'label', role: 'dimension' },
      ] }],
      interactions: [{ id: 'interaction-0', kind: 'select', mode: 'multiple', requiresStableIdentity: true,
        mappings: [{ source: { dataset: 'actual', field: 'month' }, label: { dataset: 'actual', field: 'label' },
          targetFieldID: 'purchase_month', targetDatasetID: 'orders', grain: 'month' }],
        targets: [{ visualID: 'filtered', effect: 'filter' }, { visualID: 'highlighted', effect: 'highlight' },
          { visualID: 'ignored', effect: 'none' }],
      }],
    },
    dataState: { kind: 'inline', datasets: [{ id: 'actual', columns: ['month', 'label'], rows: [[2017, 'Jan']] }] },
    selection: [],
  } as unknown as VisualizationEnvelope
}

function pageFor(element: any) {
  return { locator: () => ({ evaluate: (callback: any, arg: any) => callback(element, arg) }) } as any
}

test('performance input uses the same canonical interaction ID and mapping as product row translation', async () => {
  const visual = envelope()
  const input = await interactionInput(pageFor({ shadowRoot: { querySelectorAll: () => [{ envelope: visual }] } }), 'source', 0)
  const product = interactionCommandForRowIndex(visual, 'actual', 0)!
  expect(input.command).toEqual({ ...product, action: 'replace', toggle: false })
  expect(input.targets).toEqual(['filtered'])
})

test('performance input selects the mapped dataset instead of the first inline dataset', async () => {
  const visual = envelope()
  if (visual.dataState.kind === 'inline') visual.dataState.datasets.unshift({ id: 'unrelated', columns: ['month', 'label'], rows: [[9999, 'decoy']] } as any)
  const input = await interactionInput(pageFor({ shadowRoot: { querySelectorAll: () => [{ envelope: visual }] } }), 'source', 0)
  expect(input.command.mappings).toEqual([{ field: 'purchase_month', dataset: 'orders', grain: 'month', value: 2017, label: 'Jan' }])
})

test('performance input includes only the compiled filter targets', async () => {
  const visual = envelope()
  const input = await interactionInput(pageFor({ shadowRoot: { querySelectorAll: () => [{ envelope: visual }] } }), 'source', 0)
  expect(input.targets).toEqual(['filtered'])
})

test('performance input rejects a missing or unmapped select interaction', async () => {
  const visual = envelope(); visual.spec.interactions = []
  await expect(interactionInput(pageFor({ shadowRoot: { querySelectorAll: () => [{ envelope: visual }] } }), 'source', 0)).rejects.toThrow()
})

test('observer detects current typed data revisions, payload changes and KPI targets', async () => {
  const previous = (globalThis as any).window
  ;(globalThis as any).window = {}
  try {
    const visual = envelope()
    const signals: any = {
      visuals: { filtered: { dataRevision: 1, dataState: { kind: 'inline', datasets: [{ rows: [[1]] }] }, spec: { kind: 'kpi' } } },
      componentStatus: { 'visual:filtered': { generation: 1, loading: false } },
      page: { components: [{ visual: 'filtered' }] },
    }
    const element = { signals, shadowRoot: { querySelectorAll: () => [{ envelope: visual }] }, requestUpdate: () => {}, updateComplete: Promise.resolve() }
    await installPerformanceObserver(pageFor(element))
    const observer = (globalThis as any).window.__ldPerfObserver
    observer.begin({ visualId: 'source', targets: ['filtered'], command: { mappings: [] } })
    expect(observer.active.criticalKPIs).toEqual(['visual:filtered'])
    signals.visuals.filtered.dataRevision = 2
    observer.capture()
    signals.visuals.filtered.dataState.datasets[0].rows = [[2]]
    observer.capture()
    expect(observer.active.targetUpdates.map((update: any) => update.target)).toEqual(['visual:filtered', 'visual:filtered'])
  } finally { (globalThis as any).window = previous }
})

test('observer detects dataRevision and dataState changes without legacy version/data fields', async () => {
  const previous = (globalThis as any).window
  ;(globalThis as any).window = {}
  try {
    const signals: any = {
      visuals: { filtered: { dataRevision: 1, dataState: { kind: 'inline', datasets: [{ rows: [[1]] }] }, spec: { kind: 'cartesian' } } },
      componentStatus: { 'visual:filtered': { generation: 1, loading: false } },
      page: { components: [{ visual: 'filtered' }] },
    }
    const element = { signals, shadowRoot: { querySelectorAll: () => [{ envelope: envelope() }] }, requestUpdate: () => {}, updateComplete: Promise.resolve() }
    await installPerformanceObserver(pageFor(element))
    const observer = (globalThis as any).window.__ldPerfObserver
    observer.begin({ visualId: 'source', targets: ['filtered'], command: { mappings: [] } })
    signals.visuals.filtered.dataRevision = 2
    observer.capture()
    signals.visuals.filtered.dataState.datasets[0].rows = [[2]]
    observer.capture()
    expect(observer.active.targetUpdates.map((update: any) => update.target)).toEqual(['visual:filtered', 'visual:filtered'])
  } finally { (globalThis as any).window = previous }
})
