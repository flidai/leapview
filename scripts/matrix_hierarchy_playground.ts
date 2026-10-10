// Local production-component playground, not a test runner.
// Run: bun scripts/matrix_hierarchy_playground.ts [port]
// Compare: /?fixture=financial&version=before and /?fixture=financial
import { realpath } from 'node:fs/promises'
import { resolve, sep } from 'node:path'
import type { TableColumn, TableRow } from '../web/components/dashboard/table/types'

type Hierarchy =
  | { mode: 'parent_child'; idField: string; parentField: string; labelField: string; defaultExpandedDepth?: number }
  | { mode: 'nested'; childrenField: string; labelField: string; idField?: string; defaultExpandedDepth?: number }
  | { mode: 'levels'; fields: string[]; label?: string; defaultExpandedDepth?: number }
type Fixture = { title: string; description: string; hierarchy: Hierarchy; columns: TableColumn[]; rows: TableRow[] }

const root = resolve(import.meta.dir, '..')
const output = resolve(root, '.tmp/matrix-hierarchy-playground')
const port = Number(Bun.argv[2] ?? 18303)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Port must be an integer from 1 to 65535')

const build = await Bun.build({
  entrypoints: [resolve(root, 'web/components/dashboard/table/report-table.ts')],
  target: 'browser', format: 'esm', outdir: output,
  naming: { entry: '[name].[ext]' },
})
if (!build.success) throw new AggregateError(build.logs, 'Could not build the production report table')
const bundleRoot = await realpath(output)
const staticRoot = await realpath(resolve(root, 'static'))
const baselineRoot = resolve(root, '.tmp/matrix-hierarchy-before')
const baselineAvailable = await Bun.file(resolve(baselineRoot, 'report-table.js')).exists()
if (!await Bun.file(resolve(staticRoot, 'app.css')).exists()) {
  throw new Error('Production CSS is missing. Run bun run build:css, then start the playground again.')
}

const currency = (key: string, label: string, group: string): TableColumn => ({
  key, label, role: 'metric', align: 'right', group, width: 164,
  visualizationFormat: { kind: 'currency', currency: 'USD' },
})
const financial: Fixture = {
  title: 'Chart of accounts · consolidated balance sheet',
  description: 'Illustrative demo data in USD. Each account includes its own supplied balance; parents already contain their ledger rollups. Expanding a branch reveals detail without summing or changing those balances.',
  hierarchy: { mode: 'parent_child', idField: 'account_id', parentField: 'parent_id', labelField: 'account', defaultExpandedDepth: 2 },
  columns: [
    { key: 'account', label: 'Account', role: 'row_header', width: 440 },
    currency('current', 'September 2026', 'Closing balance'),
    currency('prior', 'August 2026', 'Closing balance'),
    currency('change', 'Monthly change', 'Movement'),
  ],
  rows: [
    ['1000', null, 'Assets', 12400000, 12070000],
    ['1100', '1000', 'Current assets', 5200000, 4980000],
    ['1110', '1100', 'Cash and cash equivalents', 1800000, 1720000],
    ['1111', '1110', 'Operating accounts', 1250000, 1180000],
    ['1112', '1110', 'Treasury deposits', 550000, 540000],
    ['1120', '1100', 'Accounts receivable', 2100000, 2010000],
    ['1130', '1100', 'Inventory', 1300000, 1250000],
    ['1200', '1000', 'Non-current assets', 7200000, 7090000],
    ['1210', '1200', 'Property, plant and equipment', 6600000, 6500000],
    ['1220', '1200', 'Intangible assets', 600000, 590000],
    ['2000', null, 'Liabilities', 4600000, 4490000],
    ['2100', '2000', 'Current liabilities', 1800000, 1740000],
    ['2110', '2100', 'Accounts payable', 1200000, 1160000],
    ['2120', '2100', 'Accrued expenses', 600000, 580000],
    ['2200', '2000', 'Long-term debt', 2800000, 2750000],
    ['3000', null, 'Equity', 7800000, 7580000],
    ['3100', '3000', 'Share capital', 5000000, 5000000],
    ['3200', '3000', 'Retained earnings', 2800000, 2580000],
  ].map(([account_id, parent_id, account, current, prior]) => ({
    account_id, parent_id, account: `${account_id} · ${account}`, current, prior, change: Number(current) - Number(prior),
  })),
}

const part = (id: string, assembly: string, quantity: number, cost: number, children: TableRow[] = []): TableRow => ({ id, assembly, quantity, cost, children })
const nested: Fixture = {
  title: 'Manufacturing · electric drive assembly',
  description: 'Illustrative bill of materials. Recursive children arrays describe assemblies, subassemblies and parts. Quantities and extended costs are supplied for every node; assembly costs include the shown components.',
  hierarchy: { mode: 'nested', childrenField: 'children', labelField: 'assembly', idField: 'id', defaultExpandedDepth: 2 },
  columns: [
    { key: 'assembly', label: 'Assembly / component', role: 'row_header', width: 460 },
    { key: 'quantity', label: 'Quantity', role: 'metric', align: 'right', format: 'integer', width: 160 },
    currency('cost', 'Extended cost', 'Production order'),
  ],
  rows: [part('drive', 'Electric drive unit', 1, 2480, [
    part('motor', 'Motor assembly', 1, 1420, [
      part('stator', 'Stator assembly', 1, 760, [part('windings', 'Copper windings', 12, 420), part('core', 'Laminated core', 1, 340)]),
      part('rotor', 'Rotor assembly', 1, 520, [part('shaft', 'Rotor shaft', 1, 180), part('magnets', 'Permanent magnets', 8, 340)]),
      part('bearings', 'Bearing set', 2, 140),
    ]),
    part('gearbox', 'Reduction gearbox', 1, 680, [part('gears', 'Gear train', 1, 420), part('housing', 'Cast housing', 1, 260)]),
    part('electronics', 'Control electronics', 1, 380, [part('controller', 'Inverter controller', 1, 260), part('harness', 'Wiring harness', 1, 120)]),
  ])],
}

const levels: Fixture = {
  title: 'Manufacturing · production by location',
  description: 'Illustrative leaf records grouped by four ordered dimensions. Generated branch headings have no invented metric totals; the supplied output and downtime values belong to the work cells.',
  hierarchy: { mode: 'levels', fields: ['region', 'plant', 'line', 'cell'], label: 'Production hierarchy', defaultExpandedDepth: 2 },
  columns: [
    { key: 'region', label: 'Region', role: 'row_header', width: 220 },
    { key: 'plant', label: 'Plant', role: 'row_header', width: 180 },
    { key: 'line', label: 'Line', role: 'row_header', width: 160 },
    { key: 'cell', label: 'Work cell', role: 'row_header', width: 170 },
    { key: 'units', label: 'Output units', role: 'metric', align: 'right', format: 'integer', width: 140 },
    { key: 'downtime', label: 'Downtime hours', role: 'metric', align: 'right', format: 'decimal', width: 150 },
  ],
  rows: [
    { region: 'Europe', plant: 'Berlin', line: 'Assembly', cell: 'Motor fit', units: 1240, downtime: 3.5 },
    { region: 'Europe', plant: 'Berlin', line: 'Assembly', cell: 'Final test', units: 1218, downtime: 2.1 },
    { region: 'Europe', plant: 'Berlin', line: 'Machining', cell: 'Shaft turning', units: 2560, downtime: 4.2 },
    { region: 'Europe', plant: 'Brno', line: 'Assembly', cell: 'Gearbox fit', units: 980, downtime: 1.8 },
    { region: 'Europe', plant: 'Brno', line: 'Machining', cell: 'Gear grinding', units: 2100, downtime: 3.2 },
    { region: 'Americas', plant: 'Detroit', line: 'Assembly', cell: 'Motor fit', units: 1450, downtime: 2.6 },
    { region: 'Americas', plant: 'Detroit', line: 'Final test', cell: 'Load bench', units: 1406, downtime: 1.4 },
  ],
}

let deepNode: TableRow = { id: 'depth-40', component: 'Level 40 · terminal component', quantity: 1, cost: 40, children: [] }
for (let depth = 39; depth >= 1; depth--) {
  deepNode = { id: `depth-${depth}`, component: `Level ${depth} · nested assembly`, quantity: 1, cost: 40, children: [deepNode] }
}
const deep: Fixture = {
  title: 'Depth playground · 40 nested levels',
  description: 'Illustrative depth fixture with forty recursive nodes and supplied values. Use Expand all and horizontal scrolling to inspect the complete branch; there is no fixed number of hierarchy levels.',
  hierarchy: { mode: 'nested', childrenField: 'children', labelField: 'component', idField: 'id', defaultExpandedDepth: 4 },
  columns: [
    { key: 'component', label: 'Component hierarchy', role: 'row_header', width: 460 },
    { key: 'quantity', label: 'Quantity', role: 'metric', align: 'right', format: 'integer', width: 160 },
    currency('cost', 'Extended cost', 'Supplied value'),
  ], rows: [deepNode],
}
const fixtures = { financial, nested, levels, deep }
const labels: Record<keyof typeof fixtures, string> = {
  financial: 'Chart of accounts · parent and child', nested: 'Manufacturing · nested structs',
  levels: 'Production locations · dimension levels', deep: 'Arbitrary depth · 40 levels',
}
const escaped = (value: string): string => value.replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]!)

function documentFor(url: URL): string {
  const requested = url.searchParams.get('fixture') ?? 'financial'
  const fixture = Object.hasOwn(fixtures, requested) ? requested as keyof typeof fixtures : 'financial'
  const before = url.searchParams.get('version') === 'before'
  const payload = JSON.stringify(fixtures[fixture]).replaceAll('<', '\\u003c')
  return `<!doctype html>
<html lang="en" data-color-mode="light" data-light-theme="light" data-dark-theme="dark">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Matrix hierarchy playground</title><link rel="stylesheet" href="/static/app.css">
<style>
*{box-sizing:border-box}html,body{margin:0;min-height:100%;}body{background:var(--lv-bg-page,#f6f8fa);color:var(--lv-fg-default,#1f2328);font:14px/1.5 system-ui,sans-serif;}
main{max-width:1420px;margin:0 auto;padding:28px 36px 36px;}header{display:flex;align-items:flex-start;justify-content:space-between;gap:24px;margin-bottom:20px;}h1{font-size:25px;line-height:1.25;margin:0 0 6px;letter-spacing:-.5px}.subtitle{margin:0;color:var(--lv-fg-muted,#59636e);font-size:13px}.version{display:inline-block;font-size:11px;letter-spacing:.5px;text-transform:uppercase;font-weight:650;background:var(--lv-bg-panel,#fff);border:1px solid var(--lv-line-default,#d1d9e0);border-radius:5px;padding:4px 8px;margin-bottom:10px;}
.controls{display:flex;gap:12px;align-items:center;flex-wrap:wrap;justify-content:flex-end;}label{display:flex;align-items:center;gap:8px;font-size:13px;}select,button{font:inherit;color:inherit;background:var(--lv-bg-panel,#fff);border:1px solid var(--lv-line-default,#d1d9e0);border-radius:6px;padding:7px 10px;}button{cursor:pointer}a{color:var(--lv-fg-link,#0969da);font-size:13px;white-space:nowrap;}.description{margin:0 0 18px;max-width:1030px;color:var(--lv-fg-muted,#59636e);font-size:13px;line-height:1.6;}.table-card{height:auto;--lv-visual-height:auto;--lv-table-max-height:calc(670px - 2px);min-height:0;border:1px solid var(--lv-line-default,#d1d9e0);border-radius:8px;background:var(--lv-bg-panel,#fff);overflow:hidden;box-shadow:0 3px 14px rgba(31,35,40,.035);}lv-report-table{display:block;height:auto;width:100%;}body[data-version='before'] .table-card{--lv-visual-height:100%;height:670px;min-height:340px}body[data-version='before'] lv-report-table{height:100%;}
details{margin-top:20px;border:1px solid var(--lv-line-default,#d1d9e0);border-radius:8px;background:var(--lv-bg-panel,#fff);}summary{padding:13px 16px;cursor:pointer;font-weight:600;}.editor{padding:0 16px 16px}.editor p{margin:0 0 10px;color:var(--lv-fg-muted,#59636e);font-size:13px;}textarea{display:block;width:100%;height:340px;resize:vertical;padding:12px;border:1px solid var(--lv-line-default,#d1d9e0);border-radius:6px;font:12px/1.6 ui-monospace,monospace;color:inherit;background:var(--lv-bg-page,#f6f8fa);tab-size:2;}.editor-actions{display:flex;gap:10px;align-items:center;margin-top:12px;}#editor-status{font-size:13px;color:var(--lv-fg-muted,#59636e)}#editor-status.error{color:var(--lv-fg-danger,#cf222e)}.primary{background:var(--lv-bg-accent,#0969da);color:white;border-color:transparent}
@media(max-width:760px){main{padding:20px 14px;}header{display:block}.controls{margin-top:16px;justify-content:flex-start}.table-card{--lv-table-max-height:calc(620px - 2px)}body[data-version='before'] .table-card{height:620px}h1{font-size:22px;}}
</style></head><body data-version="${before ? 'before' : 'after'}"><main>
<header><div><span class="version">${before ? 'Before · flat rows' : 'After · expandable hierarchy'}</span><h1>Matrix hierarchy playground</h1><p class="subtitle">Production report table · illustrative demo data · local preview</p></div>
<div class="controls"><form method="get"><label>Example <select name="fixture" onchange="this.form.submit()">${Object.entries(labels).map(([key, label]) => `<option value="${key}"${key === fixture ? ' selected' : ''}>${escaped(label)}</option>`).join('')}</select></label>${before ? '<input type="hidden" name="version" value="before">' : ''}</form>${baselineAvailable ? `<a href="/?fixture=${fixture}${before ? '' : '&version=before'}">${before ? 'View expandable version' : 'View flat baseline'}</a>` : ''}</div></header>
<p class="description" id="description"></p><section class="table-card" aria-label="Production matrix preview"><lv-report-table></lv-report-table></section>
<details${url.searchParams.get('editor') === 'open' ? ' open' : ''}><summary>Edit hierarchy and source records</summary><div class="editor"><p>Change the hierarchy field bindings, columns or records below, then apply. Nested children arrays and parent IDs are interpreted by the production component. Reset restores this example.</p><textarea id="source" aria-label="Hierarchy fixture JSON" spellcheck="false"></textarea><div class="editor-actions"><button class="primary" id="apply" type="button">Apply JSON</button><button id="reset" type="button">Reset example</button><span id="editor-status" role="status" aria-live="polite">Ready</span></div></div></details>
</main><script type="module">
import '${before ? '/before/' : '/bundle/'}report-table.js';
const initial = ${payload};
const before = ${before};
const component = document.querySelector('lv-report-table');
const editor = document.querySelector('#source');
const status = document.querySelector('#editor-status');
let revision = 0;
let current = initial;
function flattenNested(rows, field) {
  const result = [];
  const stack = [...rows].reverse();
  while(stack.length){const row=stack.pop();result.push(row);const children=row[field];if(Array.isArray(children))for(let i=children.length-1;i>=0;i--)stack.push(children[i]);}
  return result;
}
function applyFixture(input){
  if(!input || !Array.isArray(input.rows) || !Array.isArray(input.columns) || !input.hierarchy)throw new Error('Provide rows, columns and hierarchy.');
  if(!['levels','parent_child','nested'].includes(input.hierarchy.mode))throw new Error('Hierarchy mode must be levels, parent_child or nested.');
  if(input.columns.some(column=>!column || typeof column.key!=='string' || typeof column.label!=='string'))throw new Error('Every column needs a key and label.');
  const hierarchy=input.hierarchy;
  if(hierarchy.mode==='levels' && (!Array.isArray(hierarchy.fields) || !hierarchy.fields.length))throw new Error('Level hierarchies need at least one field.');
  for(const field of hierarchy.mode==='parent_child'?['idField','parentField','labelField']:hierarchy.mode==='nested'?['childrenField','labelField']:[]){if(typeof hierarchy[field]!=='string' || !hierarchy[field])throw new Error('Hierarchy needs '+field+'.');}
  const rows = before && hierarchy.mode==='nested' ? flattenNested(input.rows,hierarchy.childrenField) : input.rows;
  current=input;revision++;
  const sort={key:input.columns[0]?.key || '',direction:'asc'};
  const block=(start,blockRows)=>({start,rows:blockRows,requestSeq:0,resetVersion:revision,sort});
  component.table={id:'hierarchy-demo',version:2,type:'matrix',title:input.title || 'Hierarchy preview',
    style:{density:'comfortable',zebra:false,grid:'rows',showHeader:true},
    hierarchy:before?undefined:hierarchy,columns:input.columns,
    cardinality:{kind:'exact',value:rows.length},availableRows:rows.length,isCapped:false,rowCap:10000,
    chunkSize:Math.max(50,rows.length),rowHeight:34,resetVersion:revision,sort,
    blocks:{a:block(0,rows),b:block(Math.max(50,rows.length),[]),c:block(Math.max(50,rows.length)*2,[])},
    loadingBlock:'',error:'',selection:[]};
  document.querySelector('#description').textContent=input.description || 'Illustrative demo data. Values are supplied in source records.';
  status.className='';status.textContent='Applied '+rows.length+' source records';
}
editor.value=JSON.stringify(initial,null,2);
document.querySelector('#apply').addEventListener('click',()=>{try{applyFixture(JSON.parse(editor.value));}catch(error){status.className='error';status.textContent=error.message;}});
document.querySelector('#reset').addEventListener('click',()=>{editor.value=JSON.stringify(initial,null,2);applyFixture(structuredClone(initial));});
// The flat baseline still emits the production window command. Locally answer
// it from the same fixture so its sort and scrollbar remain usable offline.
component.addEventListener('lv-visual-window-change',event=>{
  if(!before)return;
  const request=event.detail;
  const rows=current.hierarchy.mode==='nested'?flattenNested(current.rows,current.hierarchy.childrenField):[...current.rows];
  rows.sort((a,b)=>{const left=a[request.sort.key],right=b[request.sort.key];const order=typeof left==='number'&&typeof right==='number'?left-right:String(left??'').localeCompare(String(right??''));return request.sort.direction==='desc'?-order:order;});
  const sort=request.sort;const resetVersion=request.resetVersion;const chunkSize=Math.max(50,rows.length);
  const block=(start,blockRows)=>({start,rows:blockRows,requestSeq:request.requestSeq,resetVersion,sort});
  component.table={...component.table,sort,resetVersion,blocks:{a:block(0,rows),b:block(chunkSize,[]),c:block(chunkSize*2,[])}};
});
applyFixture(initial);
</script></body></html>`
}

async function asset(base: string, relative: string, head: boolean): Promise<Response> {
  try {
    const candidate = await realpath(resolve(base, relative))
    const actualBase = await realpath(base)
    if (!candidate.startsWith(actualBase + sep)) return new Response('Not found', { status: 404 })
    const file = Bun.file(candidate)
    if (!await file.exists()) return new Response('Not found', { status: 404 })
    return new Response(head ? null : file, { headers: { 'content-type': file.type, 'cache-control': 'no-store' } })
  } catch { return new Response('Not found', { status: 404 }) }
}
const server = Bun.serve({
  hostname: '127.0.0.1', port,
  async fetch(request) {
    if (!['GET', 'HEAD'].includes(request.method)) return new Response('Method not allowed', { status: 405 })
    const url = new URL(request.url)
    const head = request.method === 'HEAD'
    if (url.pathname === '/' && url.searchParams.get('version') === 'before' && !baselineAvailable) return new Response('Before bundle unavailable. Open / for the current playground.', { status: 404 })
    if (url.pathname === '/') return new Response(head ? null : documentFor(url), { headers: { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' } })
    let path: string
    try { path = decodeURIComponent(url.pathname) } catch { return new Response('Bad request', { status: 400 }) }
    if (path.startsWith('/bundle/')) return asset(bundleRoot, path.slice('/bundle/'.length), head)
    if (path.startsWith('/before/')) return asset(baselineRoot, path.slice('/before/'.length), head)
    if (path.startsWith('/static/')) return asset(staticRoot, path.slice('/static/'.length), head)
    return new Response('Not found', { status: 404 })
  },
})
console.log(`Matrix hierarchy playground: ${server.url}`)
console.log(`Flat baseline: ${server.url}?fixture=financial&version=before`)
