// Local adapter for the production Data Explorer. No database or saved state.
import type { ExplorationFilter, ExplorationFilterValue, ExplorationSpec } from '../web/generated/exploration'
import type { DataExploreCommand, DataExplorerCommand, DataExplorerSignal, DataPreviewSignal } from '../web/generated/signals'
import { canonicalExplorationSpec, filterOperator, filterValues } from '../web/components/data/data-explorer-spec'
import { dataExplorerURL } from '../web/components/data/data-explorer-url'
import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'
import { manufacturingColumnLabels, manufacturingParts, type ManufacturingPart } from './table_manufacturing_fixture'

const datasetID = 'parts'
const returnTo = '/dashboards/parts-formatting/pages/overview'
const fieldKeys = Object.keys(manufacturingColumnLabels) as Array<keyof typeof manufacturingColumnLabels>
const fieldType = (key: string) => key === 'stock' ? 'integer' : key === 'unit_cost' ? 'decimal' : 'string'
const defaultSpec: ExplorationSpec = {
  schemaVersion: 1, mode: 'records', modelId: 'parts', datasetId: datasetID,
  dimensions: fieldKeys.map(key => ({ field: `${datasetID}.${key}` })),
  metrics: [], filters: [], sort: [], limit: 100,
}

function exploreCommand(spec: ExplorationSpec, current?: DataExploreCommand): DataExploreCommand {
  return {
    ...current, spec: canonicalExplorationSpec(spec), semanticModelId: spec.modelId, datasetId: spec.datasetId,
    dimensions: spec.dimensions.map(ref => ref.field), metrics: [],
    filters: spec.filters.map(filter => ({ field: filter.field, datasetId: filter.datasetId, operator: filterOperator(filter), values: filterValues(filter) })),
    sort: spec.sort, limit: spec.limit, requestSeq: current?.requestSeq ?? 0,
    resetVersion: current?.resetVersion ?? 0, columnWidths: current?.columnWidths ?? {},
  }
}

export function tableExploreHref(): string {
  return `${dataExplorerURL({ mode: 'explore', explore: exploreCommand(defaultSpec) } as DataExplorerCommand)}&returnTo=${encodeURIComponent(returnTo)}`
}

function fieldKey(field: string): typeof fieldKeys[number] {
  const key = field.startsWith(`${datasetID}.`) ? field.slice(datasetID.length + 1) : field
  if (!fieldKeys.includes(key as typeof fieldKeys[number])) throw new Error(`Unknown local fixture field: ${field}`)
  return key as typeof fieldKeys[number]
}

function checkKeys(value: object, allowed: string[], context: string): void {
  for (const key of Object.keys(value)) if (!allowed.includes(key)) throw new Error(`${context}.${key} is unavailable in this local records example.`)
}

function filterValue(value: ExplorationFilterValue): string | number {
  if (!value || typeof value !== 'object') throw new Error('A filter value is required.')
  checkKeys(value, ['kind', 'value'], 'Filter value')
  if (value.kind === 'string' && typeof value.value === 'string') return value.value
  if ((value.kind === 'integer' || value.kind === 'decimal') && typeof value.value === 'string'
    && (value.kind === 'integer' ? /^-?(0|[1-9]\d*)$/ : /^-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?$/).test(value.value)
    && Number.isFinite(Number(value.value))) return Number(value.value)
  throw new Error('The local fixture supports string, integer and decimal filter values.')
}

function filterPredicate(filter: ExplorationFilter): (record: ManufacturingPart) => boolean {
  checkKeys(filter, ['field', 'datasetId', 'expression'], 'Filter')
  if (filter.datasetId && filter.datasetId !== datasetID) throw new Error('Only the parts dataset is available locally.')
  const key = fieldKey(filter.field)
  const valueForField = (value: ExplorationFilterValue): string | number => {
    const parsed = filterValue(value)
    if ((fieldType(key) === 'string') !== (typeof parsed === 'string')) throw new Error(`Filter values for ${key} must be ${fieldType(key)} values.`)
    return parsed
  }
  const expression = filter.expression
  if (!expression || typeof expression !== 'object') throw new Error('A filter expression is required.')
  switch (expression.kind) {
    case 'unfiltered':
      checkKeys(expression, ['kind'], 'Filter expression')
      return () => true
    case 'null_check':
      checkKeys(expression, ['kind', 'operator'], 'Filter expression')
      if (!['is_null', 'is_not_null'].includes(expression.operator)) throw new Error('Unsupported null filter operator.')
      return record => expression.operator === 'is_null' ? record[key] == null : record[key] != null
    case 'set': {
      checkKeys(expression, ['kind', 'operator', 'values'], 'Filter expression')
      if (!['in', 'not_in'].includes(expression.operator) || !Array.isArray(expression.values)) throw new Error('Unsupported set filter.')
      const values = expression.values.map(valueForField)
      // The production planner treats an empty set as no predicate.
      if (!values.length) return () => true
      return record => expression.operator === 'in' ? values.includes(record[key]) : !values.includes(record[key])
    }
    case 'comparison': {
      checkKeys(expression, ['kind', 'operator', 'value'], 'Filter expression')
      const expected = valueForField(expression.value)
      const matchesPattern = (prefix: string, suffix: string) => {
        if (typeof expected !== 'string') throw new Error('Pattern filters require a string field and value.')
        const pattern = (prefix + expected.toLowerCase() + suffix).replace(/[.*+?^${}()|[\]\\]/g, '\\$&').replaceAll('%', '[\\s\\S]*').replaceAll('_', '[\\s\\S]')
        const matcher = new RegExp(`^${pattern}$`, 'u')
        return (record: ManufacturingPart) => matcher.test(String(record[key]).toLowerCase())
      }
      switch (expression.operator) {
        case 'equals': return record => record[key] === expected
        case 'not_equals': return record => record[key] !== expected
        case 'contains': return matchesPattern('%', '%')
        case 'not_contains': { const matches = matchesPattern('%', '%'); return record => !matches(record) }
        case 'starts_with': return matchesPattern('', '%')
        case 'ends_with': return matchesPattern('%', '')
        case 'greater_than': return record => record[key] > expected
        case 'greater_than_or_equal': return record => record[key] >= expected
        case 'less_than': return record => record[key] < expected
        case 'less_than_or_equal': return record => record[key] <= expected
        default: throw new Error('Unsupported comparison filter operator.')
      }
    }
    case 'range': {
      checkKeys(expression, ['kind', 'lower', 'upper'], 'Filter expression')
      const lower = expression.lower ? valueForField(expression.lower.value) : undefined
      const upper = expression.upper ? valueForField(expression.upper.value) : undefined
      for (const bound of [expression.lower, expression.upper]) if (bound) {
        checkKeys(bound, ['value', 'inclusive'], 'Filter bound')
        if (typeof bound.inclusive !== 'boolean') throw new Error('Range bounds require an inclusive flag.')
      }
      return record => (lower === undefined || (expression.lower?.inclusive ? record[key] >= lower : record[key] > lower))
        && (upper === undefined || (expression.upper?.inclusive ? record[key] <= upper : record[key] < upper))
    }
    default: throw new Error(`Filter ${expression.kind} is unavailable in this local fixture.`)
  }
}

function validateSpec(spec: ExplorationSpec): void {
  if (!spec || typeof spec !== 'object') throw new Error('An ExplorationSpec is required.')
  checkKeys(spec, ['schemaVersion', 'mode', 'modelId', 'datasetId', 'dimensions', 'metrics', 'filters', 'sort', 'limit', 'table'], 'Query')
  if (spec.schemaVersion !== 1 || spec.mode !== 'records' || spec.modelId !== 'parts' || spec.datasetId !== datasetID) throw new Error('This local example supports records from the parts semantic model and dataset only.')
  if (!Array.isArray(spec.dimensions) || !Array.isArray(spec.metrics) || !Array.isArray(spec.filters) || !Array.isArray(spec.sort)) throw new Error('Query fields, filters and sort must be arrays.')
  if (spec.metrics.length) throw new Error('Aggregated metrics are unavailable in this local records example.')
  if (!Number.isInteger(spec.limit) || spec.limit < 1 || spec.limit > 1000) throw new Error('Choose a limit between 1 and 1000.')
  const selected = new Set<string>()
  for (const ref of spec.dimensions) {
    checkKeys(ref, ['field', 'alias'], 'Selected field')
    const key = fieldKey(ref.field)
    if (ref.alias !== undefined && (typeof ref.alias !== 'string' || !ref.alias.trim())) throw new Error('Field aliases must be nonempty strings.')
    const output = ref.alias ?? key
    if (selected.has(output)) throw new Error(`Duplicate output column: ${output}`)
    selected.add(output)
  }
  for (const sort of spec.sort) {
    checkKeys(sort, ['field', 'direction'], 'Sort')
    if (!['asc', 'desc'].includes(sort.direction)) throw new Error('Sort direction must be asc or desc.')
    if (!spec.dimensions.some(ref => ref.field === sort.field || ref.alias === sort.field)) throw new Error(`Select the sort field first: ${sort.field}`)
  }
  // Display configuration uses the real explorer table presentation adapter.
  if (spec.table !== undefined) {
    if (!spec.table || typeof spec.table !== 'object' || Array.isArray(spec.table)) throw new Error('Table display configuration must be an object.')
    checkKeys(spec.table, ['columns', 'density', 'striped', 'showHeader', 'rowHeight'], 'Table display')
    if (spec.table.density !== undefined && !['compact', 'comfortable'].includes(spec.table.density)) throw new Error('Unsupported table density.')
    if (spec.table.rowHeight !== undefined && (!Number.isInteger(spec.table.rowHeight) || spec.table.rowHeight < 20 || spec.table.rowHeight > 200)) throw new Error('Table row height must be between 20 and 200.')
    for (const flag of ['striped', 'showHeader'] as const) if (spec.table[flag] !== undefined && typeof spec.table[flag] !== 'boolean') throw new Error(`${flag} must be a boolean.`)
    if (spec.table.columns !== undefined && !Array.isArray(spec.table.columns)) throw new Error('Table columns must be an array.')
    for (const column of spec.table.columns ?? []) {
      checkKeys(column, ['field', 'label', 'width', 'format'], 'Table column')
      if (!spec.dimensions.some(ref => ref.field === column.field || ref.alias === column.field)) throw new Error(`Select the display field first: ${column.field}`)
      if (column.label !== undefined && typeof column.label !== 'string') throw new Error('Table column labels must be strings.')
      if (column.width !== undefined && (!Number.isFinite(column.width) || column.width < 40 || column.width > 2000)) throw new Error('Table column width must be between 40 and 2000.')
      if (column.format !== undefined) {
        const format = column.format
        if (!format || typeof format !== 'object') throw new Error('Column format must be an object.')
        if (!['number', 'currency', 'percent', 'compact'].includes(format.kind)) throw new Error('Only number, currency, percent and compact display formats are available locally.')
        checkKeys(format, format.kind === 'currency' ? ['kind', 'currency', 'minimumFractionDigits', 'maximumFractionDigits'] : format.kind === 'compact' ? ['kind', 'maximumFractionDigits'] : ['kind', 'minimumFractionDigits', 'maximumFractionDigits'], 'Column format')
        const digits = format as { minimumFractionDigits?: number; maximumFractionDigits?: number }
        for (const value of [digits.minimumFractionDigits, digits.maximumFractionDigits]) if (value !== undefined && (!Number.isInteger(value) || value < 0 || value > 20)) throw new Error('Fraction digits must be integers between 0 and 20.')
        if ((digits.minimumFractionDigits ?? 0) > (digits.maximumFractionDigits ?? 20)) throw new Error('Minimum fraction digits cannot exceed maximum fraction digits.')
        if (format.kind === 'currency' && (typeof format.currency !== 'string' || !/^[A-Z]{3}$/.test(format.currency))) throw new Error('A three-letter currency code is required.')
      }
    }
  }
  spec.filters.forEach(filterPredicate)
}

function signalsFor(input: DataExplorerCommand, error = ''): { page: object; dataExplorer: DataExplorerSignal; savedExplorations: object; agent: null } {
  const spec = input.explore?.spec ?? defaultSpec
  const command = exploreCommand(spec, input.explore)
  const columns = spec.dimensions.map(ref => {
    const key = fieldKey(ref.field)
    return { key: ref.alias ?? key, label: manufacturingColumnLabels[key], type: fieldType(key), nullable: false }
  })
  const predicates = error ? [] : spec.filters.map(filterPredicate)
  const records = error ? [] : manufacturingParts.filter(record => predicates.every(predicate => predicate(record)))
  const refs = spec.dimensions
  if (!error) records.sort((left, right) => {
    for (const sort of spec.sort) {
      const ref = refs.find(ref => ref.field === sort.field || ref.alias === sort.field)!
      const key = fieldKey(ref.field)
      const a = left[key], b = right[key]
      const delta = typeof a === 'number' && typeof b === 'number' ? a - b : String(a).localeCompare(String(b), 'en')
      if (delta) return sort.direction === 'desc' ? -delta : delta
    }
    return 0
  })
  const limited = records.slice(0, spec.limit)
  const rows = limited.map(record => Object.fromEntries(refs.map(ref => [ref.alias ?? fieldKey(ref.field), record[fieldKey(ref.field)]])))
  const firstSort = spec.sort[0] ? refs.find(ref => ref.field === spec.sort[0]!.field || ref.alias === spec.sort[0]!.field) : undefined
  const sort = { column: firstSort ? firstSort.alias ?? fieldKey(firstSort.field) : undefined, direction: spec.sort[0]?.direction }
  const window = command.window
  const start = window?.start ?? 0
  const count = window?.count ?? spec.limit
  const preview: DataPreviewSignal = {
    columns, totalRows: rows.length, availableRows: rows.length, chunkSize: 100, rowHeight: 32,
    loading: false, stale: false, resetVersion: window?.resetVersion ?? command.resetVersion, sort,
    totalRowLabel: String(rows.length), blocks: { [window?.block === 'all' ? 'a' : window?.block ?? 'a']: {
      start, requestSeq: window?.requestSeq ?? command.requestSeq, resetVersion: window?.resetVersion ?? command.resetVersion, sort, rows: rows.slice(start, start + count),
    } }, error,
  }
  const dataset = { id: datasetID, title: 'Parts', description: 'Five illustrative manufacturing records shared with the formatting playground.', entities: [], fieldCount: fieldKeys.length, grainEntity: 'part', grainFields: ['part_id'] }
  const semanticModel = { id: 'parts', title: 'Manufacturing', description: 'Local example fixture', datasets: [dataset] }
  const allColumns = fieldKeys.map(key => ({ key, label: manufacturingColumnLabels[key], type: fieldType(key), nullable: false }))
  const object = { key: 'semantic_model:parts/parts', resourceId: 'parts', title: 'Parts', layer: 'semantic_model', semanticModelId: 'parts', datasetId: datasetID, columnCount: fieldKeys.length, columns: allColumns, description: dataset.description, rowCountLabel: '5 example records' }
  const explorer: DataExplorerSignal = {
    command: { ...input, mode: 'explore', explore: command, objectKey: object.key },
    objects: [object], selectedKey: object.key, selectedObject: object, preview,
    explore: {
      command, semanticModels: [semanticModel], selectedSemanticModel: semanticModel, datasets: [dataset], selectedDataset: dataset,
      fields: fieldKeys.map(key => ({ id: `${datasetID}.${key}`, label: manufacturingColumnLabels[key], kind: 'dimension', datasetId: datasetID, type: fieldType(key), compatible: true, selected: refs.some(ref => ref.field === `${datasetID}.${key}`) })),
      result: { columns, rows, rowsReturned: rows.length, durationMs: 0, requestSeq: command.requestSeq, truncated: records.length > spec.limit, warnings: [], error, window: preview },
      status: { loading: false, stale: false, requestSeq: command.requestSeq, state: error ? 'error' : input.action === 'stop' ? 'cancelled' : 'success', error },
      views: {},
    }, warnings: [],
  }
  const suggestions = command.filterSuggestions
  if (suggestions) {
    const key = fieldKey(suggestions.field)
    const otherFilters = spec.filters.filter(filter => fieldKey(filter.field) !== key).map(filterPredicate)
    const suggestionRecords = manufacturingParts.filter(record => otherFilters.every(predicate => predicate(record)))
    const values = [...new Set(suggestionRecords.map(record => record[key]))].filter(value => String(value).toLowerCase().includes((suggestions.search ?? '').toLowerCase()))
    const limit = Math.min(50, Math.max(1, suggestions.limit ?? 50))
    explorer.explore.filterSuggestions = {
      field: suggestions.field, requestSeq: command.requestSeq, suggestionRequestSeq: suggestions.suggestionRequestSeq,
      loading: false, stale: false, type: fieldType(key), truncated: values.length > limit,
      values: values.slice(0, limit).map(value => ({ label: String(value), value: typeof value === 'number' ? { kind: key === 'stock' ? 'integer' : 'decimal', value: String(value) } : { kind: 'string', value } })),
    }
  }
  return {
    page: { kind: 'data', title: 'Manufacturing parts · local Data Explorer', tabs: [], context: { active: true, environment: 'local example', projectId: 'playground', projectTitle: 'Manufacturing example', generationId: 'local-fixture', objectCount: 1 } },
    dataExplorer: explorer, savedExplorations: { enabled: false, list: { items: [], includeArchived: false }, command: { action: 'create' }, save: { state: 'saved' } }, agent: null,
  }
}

function initialCommand(): DataExplorerCommand {
  return { mode: 'explore', action: 'run', explore: exploreCommand(structuredClone(defaultSpec)), offset: 0, limit: 100, count: 100, start: 0, block: 'all', requestSeq: 0, resetVersion: 0, sort: {}, visibleColumns: [], columnWidths: {} }
}

export async function handleTableExplorerRequest(request: Request): Promise<Response | null> {
  if (new URL(request.url).pathname !== '/explore/local-query') return null
  if (request.method !== 'POST') return new Response('Method not allowed', { status: 405 })
  if (request.headers.get('origin') !== new URL(request.url).origin) return new Response('Forbidden', { status: 403 })
  if (Number(request.headers.get('content-length') ?? 0) > 100_000) return new Response('Request too large', { status: 413 })
  try {
    const body = await request.text()
    if (body.length > 100_000) return new Response('Request too large', { status: 413 })
    const command = JSON.parse(body) as DataExplorerCommand
    if (command.mode !== 'explore' || !command.explore || !['configure', 'run', 'stop'].includes(command.action ?? command.explore.action ?? 'run')) throw new Error('Only local records exploration commands are supported.')
    validateSpec(command.explore.spec)
    const window = command.explore.window
    if (window && (!Number.isInteger(window.start) || window.start < 0 || !Number.isInteger(window.count) || window.count < 1 || window.count > 1000 || !['all', 'a', 'b', 'c'].includes(window.block))) throw new Error('Unsupported local table window.')
    return Response.json(signalsFor(command))
  } catch (error) {
    return Response.json({ error: error instanceof Error ? error.message : String(error) }, { status: 400 })
  }
}

export function tableExplorerDocument(search = ''): string {
  const command = initialCommand()
  let error = ''
  try {
    const params = new URLSearchParams(search)
    for (const key of params.keys()) {
      if (!['v', 'mode', 'state', 'returnTo'].includes(key)) throw new Error(`URL option ${key} is unavailable in this local example.`)
      if (params.getAll(key).length !== 1) throw new Error(`URL option ${key} must appear once.`)
    }
    if (params.has('state')) {
      if (params.get('v') !== '2' || params.get('mode') !== 'explore') throw new Error('Use the canonical v2 exploration URL.')
      const spec = JSON.parse(params.get('state')!) as ExplorationSpec
      validateSpec(spec)
      command.explore = exploreCommand(spec)
    } else if (params.has('v') || params.has('mode')) throw new Error('The exploration URL requires canonical state.')
  } catch (failure) { error = failure instanceof Error ? failure.message : String(failure) }
  const signals = JSON.stringify(signalsFor(command, error)).replaceAll('<', '\\u003c')
  return `<!doctype html><html lang="en" data-color-mode="light" data-light-theme="light" data-dark-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Manufacturing parts · local Data Explorer</title><link rel="stylesheet" href="/static/app.css"><style>
html,body{height:100%;margin:0}body{font-family:system-ui,sans-serif;background:var(--lv-bg-app,#f6f8fa);color:var(--lv-fg-default,#1f2328)}header{box-sizing:border-box;min-height:76px;padding:12px 24px;border-bottom:1px solid var(--lv-line-default,#d1d9e0);display:flex;align-items:center;justify-content:space-between;gap:24px}h1{margin:0 0 4px;font-size:18px}p{margin:0;font-size:12px;color:var(--lv-fg-muted,#59636e)}a{font-size:13px;color:var(--lv-fg-accent,#0969da)}main{height:calc(100% - 76px)}lv-data-explorer{display:block;height:100%}#notice{font-size:12px;color:var(--lv-fg-danger,#cf222e)}
</style></head><body><header><div><h1>Manufacturing parts · local Data Explorer</h1><p>Production explorer · five shared example records · local fixture queries · saving, sharing and export unavailable</p><span id="notice" role="status"></span></div><a href="${returnTo}">Back to formatting playground</a></header><main><lv-data-explorer></lv-data-explorer></main>
<script type="module" src="${datastarRuntimeURL}"></script><script type="module">
import '/bundle/data-explorer.js';
import {mergePatch} from '${datastarRuntimeURL}';
const explorer=document.querySelector('lv-data-explorer'),notice=document.querySelector('#notice');
let current=${signals};let latestRequest=0;
function render(signals){current=signals;mergePatch({dataExplorer:null,page:null,savedExplorations:null,agent:null});mergePatch(signals);}
render(current);
function disableAnalyze(){const button=explorer.shadowRoot?.querySelector('.mode-switch button:last-child');if(button && !button.disabled){button.disabled=true;button.title='Aggregate analysis is unavailable for this local records example.';}}
await explorer.updateComplete;disableAnalyze();
new MutationObserver(disableAnalyze).observe(explorer.shadowRoot,{childList:true,subtree:true});
explorer.addEventListener('lv-data-explorer-command',async event=>{
 const command=event.detail,sequence=++latestRequest;
 try{
  const response=await fetch('/explore/local-query',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(command)});
  const result=await response.json();if(!response.ok)throw new Error(result.error ?? 'Local query failed');
  if(sequence!==latestRequest)return;notice.textContent='';render(result);
 }catch(error){
  if(sequence!==latestRequest)return;notice.textContent=error.message;
  const failed=structuredClone(current),requestSeq=command.explore?.requestSeq ?? 0;
  failed.dataExplorer.explore.command={...failed.dataExplorer.explore.command,action:command.action ?? 'configure',requestSeq};
  failed.dataExplorer.command={...failed.dataExplorer.command,action:command.action ?? 'configure',explore:failed.dataExplorer.explore.command};
  failed.dataExplorer.explore.result={...failed.dataExplorer.explore.result,error:error.message,requestSeq};
  failed.dataExplorer.explore.status={state:'error',loading:false,stale:false,error:error.message,requestSeq};render(failed);
 }finally{document.dispatchEvent(new CustomEvent('datastar-fetch',{detail:{type:'finished',el:explorer}}));}
});
</script></body></html>`
}
