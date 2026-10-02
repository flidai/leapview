import type { VisualizationEnvelope, VisualizationField, VisualizationFieldRef, VisualizationSpec, VisualizationWindowRequest } from '../web/generated/visualization'
import { currentVisualizationSchemaVersion } from '../web/generated/visualization/schema-version'
import { defaultRendererContext } from '../web/components/dashboard/visualization/host-controller'
import catalog from '../docs/visuals/catalog.json'
import { blockStartsForAll } from '../web/components/dashboard/table/block-source'
import { decimalSignedInteger } from '../web/components/dashboard/visualization/decimal'

/** Production catalog order from docs/visuals/catalog.json; no service data. */
export const chartExamples = catalog.documents.map(({ source, title }) => ({ id: source, label: title }))

export type ChartScenario = 'standard' | 'dense' | 'long-labels' | 'missing' | 'single' | 'zero' | 'negative' | 'precision'
export interface ChartOptions {
  scenario: ChartScenario
  status: 'ready' | 'loading' | 'empty' | 'error'
  legend: 'top' | 'bottom' | 'left' | 'right' | 'hidden'
  labels: 'hidden' | 'automatic' | 'dense' | 'always'
  axes: boolean
  multiSeries: boolean
  tooltip: 'default' | 'value'
  stacked: boolean
  kpiMode: 'compact' | 'bullet' | 'progress'
}
export const defaultChartOptions: ChartOptions = {
  scenario: 'standard', status: 'ready', legend: 'bottom', labels: 'automatic', axes: true,
  multiSeries: true, tooltip: 'default', stacked: false, kpiMode: 'compact',
}
export interface ChartFixture { envelope: VisualizationEnvelope; rows: unknown[][]; note: string }
const ref = (field: string): VisualizationFieldRef => ({ dataset: 'primary', field })
const field = (id: string, numeric = false, label = id): VisualizationField => ({
  id, role: numeric ? 'metric' : 'dimension', dataType: numeric ? 'decimal' : 'string', nullable: true, label,
  ...(numeric ? { format: { kind: 'number', maximumFractionDigits: 2 } } : {}),
})

export function createChartFixture(id: string, options: ChartOptions = defaultChartOptions, revision = 1, mapColors = { nullColor: defaultRendererContext.colors.muted, stroke: defaultRendererContext.colors.surface }): ChartFixture {
  const title = chartExamples.find((entry) => entry.id === id)?.label ?? 'Line chart'
  const count = options.scenario === 'single' ? 1 : options.scenario === 'dense' ? 80 : 8
  const category = (index: number) => options.scenario === 'long-labels'
    ? `Region ${index + 1} — enterprise customers and strategic accounts with an unusually long name`
    : ['North', 'South', 'East', 'West', 'Central', 'Coastal', 'Metro', 'Rural'][index] ?? `Region ${index + 1}`
  const value = (index: number): number | string | null => {
    if (options.scenario === 'missing' && index % 3 === 1) return null
    if (options.scenario === 'zero') return 0
    if (options.scenario === 'negative') return (index % 2 ? -1 : 1) * (index + 1) * 14
    if (options.scenario === 'precision') return `90071992547409${91 + index}.125`
    return 24 + ((index * 31 + 17) % 73)
  }
  let fields = [field('category', false, 'Region'), field('value', true, 'Revenue'), field('comparison', true, 'Previous period')]
  let rows: unknown[][] = Array.from({ length: count }, (_, i) => [category(i), value(i), 15 + i * 8])
  const presentation = { legend: options.legend, labelPolicy: { density: options.labels, priority: [] as Array<'selected' | 'anomaly' | 'threshold'>, maxCharacters: 24, minimumSpacing: 6, tooltipFallback: true } }
  const common = {
    title, datasets: [{ id: 'primary', fields }], dataBudget: { maxRows: 1000, requiredCompleteness: 'complete' as const },
    accessibility: { title, description: `Deterministic local ${title.toLowerCase()} fixture.`, announceChanges: true },
    interactions: [{ id: 'playground-select', kind: 'select' as const, mode: 'multiple' as const, mappings: [{ source: ref('category'), targetFieldID: 'category' }], targets: [], requiresStableIdentity: false }],
  }
  let spec: VisualizationSpec
  let note = 'Select marks to see the public interaction command and a local selection response.'
  if (id === 'pie' || id === 'donut' || id === 'funnel') {
    spec = { ...common, kind: 'proportional', mark: id, category: ref('category'), value: ref('value'),
      presentation: { ...presentation, orientation: 'vertical', rose: false, ...(id === 'donut' ? { innerRadius: 0.56, centerLabel: 'Revenue' } : {}) } }
  } else if (id === 'tree' || id === 'treemap' || id === 'sunburst' || id === 'graph' || id === 'sankey') {
    const network = id === 'graph' || id === 'sankey'
    fields = [field('category', false, network ? 'Source' : 'Node'), field('parent', false, network ? 'Target' : 'Parent'), field('value', true, 'Volume')]
    rows = network
      ? Array.from({ length: count }, (_, i) => [i < 3 ? 'Acquisition' : category(Math.floor((i - 3) / 2)), i < 3 ? category(i) : `Outcome ${i}`, value(i)])
      : [['All regions', null, count * 100], ...Array.from({ length: count }, (_, i) => [category(i), 'All regions', value(i)])]
    spec = { ...common, kind: 'hierarchy', mark: id, node: ref('category'), value: ref('value'),
      ...(network ? { source: ref('category'), target: ref('parent') } : { parent: ref('parent') }),
      presentation: { ...presentation, orientation: 'horizontal', initialDepth: 2, roam: true, layout: id === 'graph' ? 'circular' : 'standard', breadcrumb: true, focus: 'adjacency' } }
  } else if (id === 'scatter') {
    fields = [field('category', false, 'Account'), field('x', true, 'Visits'), field('value', true, 'Revenue'), field('size', true, 'Team size')]
    rows = Array.from({ length: options.scenario === 'single' ? 1 : options.scenario === 'dense' ? 500 : count * 4 }, (_, i) => [category(i), 5 + ((i * 17) % 110), value(i), 4 + (i % 14)])
    spec = { ...common, kind: 'point', identity: [ref('category')], x: ref('x'), y: ref('value'), size: ref('size'), label: ref('category'),
      sizeScale: { minimumPixels: 6, maximumPixels: 24 }, presentation: { ...presentation, axisVisible: options.axes, overplot: 'opacity', opacity: 0.7, largeMode: 'automatic', largeThreshold: 2000, brush: ['rectangle', 'lasso'] } }
  } else if (id === 'gauge' || id === 'radar') {
    if (id === 'gauge') rows = rows.slice(0, 1)
    if (id === 'radar' && options.multiSeries) {
      fields.push(field('series', false, 'Series'))
      rows = rows.flatMap((row, i) => [[...row, 'Current'], [row[0], 20 + i * 7, row[2], 'Previous']])
    }
    spec = { ...common, kind: 'polar', mark: id, value: ref('value'), ...(id === 'radar' ? { category: ref('category'), ...(options.multiSeries ? { series: ref('series') } : {}) } : {}),
      presentation: { ...presentation, showPointer: true, area: true, ...(id === 'gauge' ? { minimum: 0, maximum: 100, target: 80 } : {}) } }
    if (id === 'gauge') note = 'Precision values intentionally exercise the production out-of-domain message.'
  } else if (id === 'kpi') {
    fields = [field('category', false, 'Period'), field('value', true, 'Revenue'), field('comparison', true, 'Previous period'), field('goal', true, 'Goal')]
    rows = rows.map((row) => [...row, 100])
    spec = { ...common, kind: 'kpi', value: ref('value'), comparison: { field: ref('comparison'), reducer: 'last', label: 'Previous period' }, goal: { field: ref('goal'), reducer: 'last', label: 'Monthly target' }, trend: { category: ref('category'), value: ref('value') },
      presentation: { mode: options.kpiMode, delta: 'relative', favorableDirection: 'increase', missingComparison: 'show_unavailable', ranges: [], note: 'Local example · monthly target' } }
  } else if (id === 'map') {
    fields = [field('category', false, 'City'), field('latitude', true, 'Latitude'), field('longitude', true, 'Longitude'), field('value', true, 'Orders')]
    const cities: Array<[string, number, number]> = [['Berlin', 52.52, 13.405], ['Paris', 48.857, 2.352], ['Madrid', 40.417, -3.704], ['Rome', 41.903, 12.496], ['Prague', 50.075, 14.438], ['Vienna', 48.208, 16.374], ['Amsterdam', 52.367, 4.904], ['Copenhagen', 55.676, 12.568]]
    rows = Array.from({ length: count }, (_, i) => { const city = cities[i % cities.length]!; return [options.scenario === 'long-labels' ? category(i) : `${city[0]}${i >= 8 ? ` ${i}` : ''}`, city[1] + Math.floor(i / 8) * 0.15, city[2] + Math.floor(i / 8) * 0.15, value(i)] })
    spec = { ...common, kind: 'geographic', spatialInteractions: [], layers: [{ id: 'cities', kind: 'point', latitude: ref('latitude'), longitude: ref('longitude'), value: ref('value'), tooltip: [ref('category'), ref('value')], position: 'above_labels', visibility: { minimumZoom: 0, maximumZoom: 24 }, size: { minimumRadius: 6, maximumRadius: 20 }, color: { kind: 'sequential', palette: 'blue', reverse: false, nullColor: mapColors.nullColor }, stroke: { color: mapColors.stroke, width: 1, opacity: 1 }, cluster: { enabled: false, radius: 40, maximumZoom: 14, minimumPoints: 2, showCount: false }, opacity: 0.85 }],
      presentation: { ...presentation, roam: true, theme: 'auto', labelDensity: 'hidden', camera: { mode: 'fit_data', padding: 48, minimumZoom: 0, maximumZoom: 18 }, controls: { zoom: true, reset: true, compass: true } } }
    note = 'Real MapLibre with a blank local basemap and inline coordinates. No external tiles, styles, glyphs, or credentials.'
  } else if (id === 'table' || id === 'matrix' || id === 'pivot') {
    const grid = { rowHeight: 34, striped: true, showHeader: true }
    if (id === 'table') {
      fields.push(field('status', false, 'Status'))
      rows = Array.from({ length: options.scenario === 'single' ? 1 : options.scenario === 'dense' ? 1000 : 180 }, (_, i) => [`${category(i % 8)} / ${String(i + 1).padStart(4, '0')}`, value(i), 15 + i * 8, ['Active', 'Pending', 'Paused'][i % 3]])
      spec = { ...common, kind: 'table', columns: fields.map((item) => ({ field: ref(item.id), label: item.label, width: item.id === 'category' ? 220 : 150, formatting: item.id === 'status' ? [{ kind: 'badge', values: { Active: 'green', Pending: 'yellow', Paused: 'gray' } }] : [] })), defaultSort: [{ field: ref('category'), direction: 'ascending' }], presentation: grid }
    } else {
      fields = [field('category', false, 'Region'), ...['q1', 'q2', 'q3', 'q4'].map((quarter) => ({ ...field(quarter, true, 'Revenue'), grid: { group: quarter.toUpperCase(), metric: 'revenue', columnValue: quarter.toUpperCase(), formatting: [] } }))]
      rows = Array.from({ length: count }, (_, i) => [category(i), value(i), value(i + 1), value(i + 2), value(i + 3)])
      spec = { ...common, kind: id, rows: [ref('category')], columns: [], metrics: [ref('revenue')], metricFormatting: { revenue: [{ kind: 'data_bar', color: 'blue', minimum: 0, maximum: 100 }] }, presentation: grid }
    }
    note = 'Windowed production table: sorting, scrolling, selection, pinning, resizing and column visibility. Requests are answered from deterministic local rows.'
  } else {
    const mark = (['line', 'area', 'bar', 'column', 'candlestick', 'boxplot', 'combo', 'waterfall', 'histogram', 'heatmap'].includes(id) ? id : 'line') as Extract<VisualizationSpec, { kind: 'cartesian' }>['mark']
    let y = [ref('value'), ...(options.multiSeries ? [ref('comparison')] : [])]
    if (mark === 'candlestick' || mark === 'boxplot') {
      const names = mark === 'candlestick' ? ['open', 'close', 'low', 'high'] : ['minimum', 'q1', 'median', 'q3', 'maximum']
      fields = [field('category', false, 'Period'), ...names.map((name) => field(name, true))]
      rows = Array.from({ length: count }, (_, i) => { const base = options.scenario === 'zero' ? 0 : options.scenario === 'negative' ? -100 + i * 3 : 40 + i * 3; const span = options.scenario === 'zero' ? 0 : 10; return [category(i), ...(mark === 'candlestick' ? [base, base + (i % 2 ? -span / 2 : span / 2), base - span, base + span] : [base - span, base - span / 2, base, base + span / 2, base + span])] })
      if (options.scenario === 'missing' && rows[1]) rows[1]![2] = null
      if (options.scenario === 'precision') rows = rows.map((row, i) => [row[0], ...(mark === 'candlestick' ? ['9007199254740992.1', '9007199254740992.2', '9007199254740992.0', '9007199254740992.3'] : names.map((_, j) => `${900 + i}.${j + 1}000000000000001`))])
      y = names.map(ref)
    } else if (mark === 'heatmap') {
      fields = [field('category', false, 'Region'), field('row', false, 'Weekday'), field('value', true, 'Orders')]
      rows = Array.from({ length: count }, (_, i) => ['Mon', 'Tue', 'Wed', 'Thu', 'Fri'].map((day, j) => [category(i), day, value(i + j)])).flat()
      y = [ref('row'), ref('value')]
    } else if (mark === 'histogram') {
      fields = [field('category', false, 'Bucket'), field('start', true), field('end', true), field('count', true, 'Count')]
      rows = Array.from({ length: count }, (_, i) => [`Bin ${i + 1}`, i * 10, (i + 1) * 10, value(i)])
      y = [ref('count')]
    } else if (mark === 'waterfall') {
      fields = [field('category', false, 'Stage'), field('start', true, 'Start'), field('value', true, 'Change')]
      let running = 0
      rows = Array.from({ length: count }, (_, i) => { const delta = Number(value(i) ?? 0) * (i % 3 === 2 ? -1 : 1); const start = delta < 0 ? running + delta : running; running += delta; return [category(i), start, delta] })
      y = [ref('start'), ref('value')]
    }
    spec = { ...common, kind: 'cartesian', mark, x: ref('category'), y,
      presentation: { ...presentation, axisVisible: options.axes, smooth: false, stacked: options.stacked, showSymbols: true, dataZoom: options.scenario === 'dense', area: mark === 'area', step: false, orientation: mark === 'bar' ? 'horizontal' : 'vertical',
        ...(mark === 'combo' ? { comboSeries: [{ seriesValue: 'value', mark: 'column', axis: 'primary' }, ...(options.multiSeries ? [{ seriesValue: 'comparison', mark: 'line' as const, axis: 'secondary' as const }] : [])] } : {}) } }
  }
  spec.datasets = [{ id: 'primary', fields: spec.kind === 'matrix' || spec.kind === 'pivot' ? [field('category', false, 'Region'), field('revenue', true, 'Revenue')] : fields }]
  if (options.tooltip === 'value' && (spec.kind === 'cartesian' || spec.kind === 'point' || spec.kind === 'proportional')) spec.tooltip = [ref(fields.find((item) => item.role === 'metric')!.id)]
  if (options.status === 'empty') rows = []
  const specRevision = `playground:${id}:${revision}`
  const envelope: VisualizationEnvelope = {
    schemaVersion: currentVisualizationSchemaVersion, visualID: `playground-${id}`, rendererID: spec.kind === 'kpi' ? 'html' : spec.kind === 'geographic' ? 'maplibre' : ['table', 'matrix', 'pivot'].includes(spec.kind) ? 'tanstack' : 'echarts',
    specRevision, dataRevision: revision, spec,
    dataState: { kind: 'inline', specRevision, dataRevision: revision, generation: 1, datasets: [{ id: 'primary', specRevision, dataRevision: revision, generation: 1, columns: fields.map((item) => item.id), rows, completeness: rows.length ? 'complete' : 'empty' }] },
    selection: [], highlights: [], status: { kind: options.status === 'empty' ? 'no_data' : options.status, ...(options.status === 'error' ? { message: 'Example error: the local query could not be completed.' } : {}) }, diagnostics: [],
  }
  if (spec.kind === 'table' || spec.kind === 'matrix' || spec.kind === 'pivot') {
    envelope.dataState = { kind: 'windowed', specRevision, dataRevision: revision, generation: 1, schema: { id: 'primary', fields }, cardinality: { kind: 'exact', count: rows.length }, availableRows: rows.length, rowCap: 1000, chunkSize: 50, resetVersion: 0, sort: [{ field: ref('category'), direction: 'ascending' }], blocks: {} }
    return { envelope: answerChartWindow(envelope, rows, { visualID: envelope.visualID, specRevision, dataRevision: revision, requestSeq: 0, resetVersion: 0, start: 0, limit: 50, blockID: 'all', sort: envelope.dataState.sort }), rows, note }
  }
  return { envelope, rows, note }
}

/** Respond through the same windowed envelope contract used by the host. */
export function answerChartWindow(envelope: VisualizationEnvelope, rows: unknown[][], request: VisualizationWindowRequest): VisualizationEnvelope {
  const state = envelope.dataState
  if (state.kind !== 'windowed' || request.visualID !== envelope.visualID || request.specRevision !== envelope.specRevision) return envelope
  if (request.resetVersion < state.resetVersion) return envelope
  if (request.blockID !== 'all' && (state.blocks[request.blockID]?.requestSeq ?? -1) > request.requestSeq) return envelope
  if (request.blockID === 'all' && request.resetVersion === state.resetVersion && Object.values(state.blocks).some((block) => block.requestSeq > request.requestSeq)) return envelope
  const sort = request.sort[0]
  const column = state.schema.fields.findIndex((item) => item.id === sort?.field.field)
  const numeric = state.schema.fields[column]?.role === 'metric'
  const sorted = [...rows].sort((a, b) => {
    const left = a[column], right = b[column]
    const order = left == null ? right == null ? 0 : 1 : right == null ? -1 : numeric ? compareMetric(left, right) : String(left).localeCompare(String(right), 'en')
    return sort?.direction === 'descending' ? -order : order
  })
  const blocks: typeof state.blocks = request.blockID === 'all' ? {} : { ...state.blocks }
  const ids = request.blockID === 'all' ? ['a', 'b', 'c'] : [request.blockID]
  const starts = request.blockID === 'all' ? blockStartsForAll(request.start, state.chunkSize) : [request.start]
  ids.forEach((id, index) => {
    const start = starts[index]!
    blocks[id] = { id, start, rows: sorted.slice(start, start + state.chunkSize), requestSeq: request.requestSeq, resetVersion: request.resetVersion, sort: request.sort }
  })
  const dataRevision = envelope.dataRevision + 1
  return { ...envelope, dataRevision, dataState: { ...state, dataRevision, blocks, resetVersion: request.resetVersion, sort: request.sort } }
}

function compareMetric(left: unknown, right: unknown): number {
  const a = decimalSignedInteger(String(left)), b = decimalSignedInteger(String(right))
  const scale = Math.max(a.scale, b.scale)
  const x = a.signedDigits * 10n ** BigInt(scale - a.scale)
  const y = b.signedDigits * 10n ** BigInt(scale - b.scale)
  return x < y ? -1 : x > y ? 1 : 0
}
