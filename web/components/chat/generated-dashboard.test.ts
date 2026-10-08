import { retainedVisualExplorerHref, visualDataExplorerHref } from './visual-action-links'
import { searchActionHref } from './search-action'
import { dashboardActionLinks } from './dashboard-action-links'
import {expect,test} from 'bun:test'
import type {ChatTranscriptItemSignal} from '../../generated/signals'
import {generatedDashboardHref} from './generated-dashboard'
const tool=(name:string,extra:Partial<ChatTranscriptItemSignal>={}):ChatTranscriptItemSignal=>({id:name,kind:'tool',name,runId:'run-new',toolCallId:name,status:'complete',...extra})
test('completed new dashboard opens the exact retained preview within the current chat',()=>{
 const href=generatedDashboardHref([tool('create_dashboard_draft'),tool('edit_dashboard_source'),tool('preview_dashboard_draft')],'run-new','conversation')
 expect(href).toBe('/chats/conversation/actions/preview_dashboard_draft/open?run=run-new&embed=chat&mode=preview&createdBy=create_dashboard_draft')
})
test('history, individual visuals, unfinished drafts and failed previews do not auto-open',()=>{
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft')],'other-run','conversation')).toBe('')
 expect(generatedDashboardHref([tool('add_dashboard_visual'),tool('preview_dashboard_draft')],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([tool('create_dashboard_draft')],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft',{resultJson:'{"visualErrors":{"chart":"Missing measure"}}'})],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft',{status:'error',error:'Invalid query'})],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft',{resultJson:'{"error":"invalid"}'})],'run-new','conversation')).toBe('')
})
test('a preview from before the new draft was created must not open that draft',()=>{
 expect(generatedDashboardHref([tool('preview_dashboard_draft'),tool('create_dashboard_draft')],'run-new','conversation')).toBe('')
})
test('a recovery run opens the newly completed dashboard after creation in an earlier run',()=>{
 const created=tool('create_dashboard_draft',{runId:'interrupted',resultJson:'{"lifecycle":{"id":"dashboard-a"}}'})
 const edit=tool('edit_dashboard_source',{argumentsJson:'{"dashboardId":"dashboard-a"}'})
 const preview=tool('preview_dashboard_draft',{argumentsJson:'{"dashboardId":"dashboard-a"}'})
 const href=generatedDashboardHref([created,edit,preview],'run-new','conversation')
 expect(href).toContain('/actions/preview_dashboard_draft/open')
 expect(href).toContain('mode=preview')
 expect(generatedDashboardHref([created,preview],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([created,{...edit,argumentsJson:'{"dashboardId":"other"}'},preview],'run-new','conversation')).toBe('')
})

test('an edit after preview requires a fresh preview before opening',()=>{
 expect(generatedDashboardHref([tool('create_dashboard_draft'),tool('preview_dashboard_draft'),tool('edit_dashboard_source')],'run-new','conversation')).toBe('')
})

function recordedVisual(overrides: Record<string, unknown> = {}): ChatTranscriptItemSignal {
  return {
    id: 'tool', kind: 'tool', name: 'query_visual', status: 'complete', runId: 'run-1', toolCallId: 'call-1',
    artifact: { id: 'chart-1', type: 'bar' },
    argumentsJson: JSON.stringify({ semanticModelId: 'sales', visual: {
      type: 'bar', query: { type: 'aggregate', dimensions: [{ dimension: 'ordered_at', grain: 'month', alias: 'month' }], metrics: ['revenue'], sort: [{ field: 'month', direction: 'asc' }], limit: 50 },
    }, filters: [{ dimension: 'ordered_at', default: { type: 'comparison', operator: 'greaterThanOrEqual', value: { type: 'date', value: '2026-01-01' } } }], ...overrides }),
    resultJson: JSON.stringify({ ok: true, id: 'chart-1', type: 'bar', datasetId: 'orders', semanticModelRef: { id: 'sales' }, completeness: { limit: 17 },
      fields: [{ role: 'dimension', fieldId: 'sales.ordered_at', explorerFieldId: 'ordered_at', alias: 'month' }],
      filters: [{ fieldId: 'sales.ordered_at', resolvedDatasetId: 'orders' }],
    }),
  } as ChatTranscriptItemSignal
}

test('editable handoff preserves canonical typed filters, recorded row limit and time sorting', () => {
  const url = new URL(visualDataExplorerHref(recordedVisual()), 'https://example.test')
  expect(url.searchParams.get('v')).toBe('2')
  expect(JSON.parse(url.searchParams.get('state')!)).toMatchObject({
    modelId: 'sales', datasetId: 'orders', limit: 17,
    time: { field: 'ordered_at', grain: 'month', alias: 'month' },
    sort: [{ field: 'ordered_at', direction: 'asc' }],
    filters: [{ field: 'ordered_at', datasetId: 'orders', expression: { kind: 'comparison', operator: 'greater_than_or_equal', value: { kind: 'date', value: '2026-01-01' } } }],
  })
})

test('unmapped or relative filters retain audit access without a misleading editable query', () => {
  for (const filter of [
    { dimension: 'country', default: { type: 'comparison', operator: 'equals', value: { type: 'string', value: 'DE' } } },
    { dimension: 'ordered_at', default: { type: 'relativePeriod', direction: 'previous', count: 1, unit: 'month' } },
  ]) {
    const item = recordedVisual({ filters: [filter] })
    expect(visualDataExplorerHref(item)).toBe('')
    expect(retainedVisualExplorerHref('chat-1', item, { enabled: true, running: false })).toBe('/chats/chat-1/visuals/chart-1/explore?run=run-1')
  }
})

test('audit links wait for message persistence while older runs remain inspectable', () => {
  expect(retainedVisualExplorerHref('chat-1', recordedVisual(), { enabled: true, running: true, runId: 'run-1' })).toBe('')
  expect(retainedVisualExplorerHref('chat-1', recordedVisual(), { enabled: true, running: true, runId: 'run-2' })).toContain('run=run-1')
})

test('search handoff retains exact kinds, domain, cursor and limit', () => {
  const item = { kind: 'tool', name: 'catalog_search', status: 'complete', argumentsJson: JSON.stringify({ query: 'sales', kinds: ['model', 'dashboard'], domain: 'finance', cursor: 'page/2', limit: 7 }) } as ChatTranscriptItemSignal
  const url = new URL(searchActionHref(item)!, 'https://example.test')
  expect(url.searchParams.getAll('kind')).toEqual(['model', 'dashboard'])
  expect(Object.fromEntries(['q', 'domain', 'cursor', 'limit'].map(key => [key, url.searchParams.get(key)]))).toEqual({ q: 'sales', domain: 'finance', cursor: 'page/2', limit: '7' })
})

test('Builder handoff resolves the recorded operation within its conversation and run', () => {
  expect(dashboardActionLinks({ ...recordedVisual(), name: 'fork_dashboard' }, 'chat-1')).toEqual([
    { label: 'Open in Builder', href: '/chats/chat-1/actions/call-1/open?run=run-1' },
  ])
})

test('bounded recovery previews require retained server validation before automatic opening',()=>{
 const edit=tool('edit_dashboard_source',{argumentsJson:'{"dashboardId":"dashboard-a"}'})
 const preview=tool('preview_dashboard_draft',{argumentsJson:'{"dashboardId":"dashboard-a"}',resultJson:'visualErrors:\n  chart: Missing measure\n'})
 expect(generatedDashboardHref([edit,preview],'run-new','conversation')).toContain('mode=preview')
})

test('compact create receipt identifies its dashboard and rejects another dashboard preview',()=>{
 const created=tool('create_dashboard_draft',{resultJson:'{"id":"dashboard-a","status":"draft"}'})
 const own=tool('preview_dashboard_draft',{argumentsJson:'{"dashboardId":"dashboard-a"}'})
 const other=tool('preview_dashboard_draft',{argumentsJson:'{"dashboardId":"dashboard-b"}'})
 expect(generatedDashboardHref([created,other],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([created,own],'run-new','conversation')).toContain('createdBy=create_dashboard_draft')
})

test('field edits after preview require validation again',()=>{
 const created=tool('create_dashboard_draft',{resultJson:'{"id":"dashboard-a","status":"draft"}'})
 const preview=tool('preview_dashboard_draft',{argumentsJson:'{"dashboardId":"dashboard-a"}'})
 const field=tool('assign_dashboard_field',{argumentsJson:'{"dashboardId":"dashboard-a"}'})
 expect(generatedDashboardHref([created,preview,field],'run-new','conversation')).toBe('')
 expect(generatedDashboardHref([created,field,preview],'run-new','conversation')).toContain('createdBy=create_dashboard_draft')
})
