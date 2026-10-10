// Local production-component playground, not a test runner.
// Run: bun scripts/table_media_playground.ts [port]
import { realpath } from 'node:fs/promises'
import { resolve, sep } from 'node:path'
import type { TableColumn, TableRow } from '../web/components/dashboard/table/types'
import { ensureManufacturingDemoAssets, manufacturingColumnLabels, manufacturingParts } from './table_manufacturing_fixture'

type CellContent =
  | { kind: 'image'; display: 'inline' | 'tooltip'; width: number; height: number; altField?: string }
  | { kind: 'link'; labelField?: string; newTab?: boolean }
type MediaColumn = TableColumn & { content?: CellContent }
type Fixture = { title: string; description: string; rowHeight: number; columns: MediaColumn[]; rows: TableRow[] }
const root = resolve(import.meta.dir, '..')
const output = resolve(root, '.tmp/table-media-playground')
const baselineRoot = resolve(root, '.tmp/table-media-before')
const baselineAvailable = await Bun.file(resolve(baselineRoot, 'report-table.js')).exists()
const staticRoot = await realpath(resolve(root, 'static'))
const port = Number(Bun.argv[2] ?? 18305)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Port must be an integer from 1 to 65535')

await ensureManufacturingDemoAssets(staticRoot)

const build = await Bun.build({
  entrypoints: [resolve(root, 'web/components/dashboard/table/report-table.ts')],
  target: 'browser', format: 'esm', outdir: output, naming: { entry: '[name].[ext]' },
})
if (!build.success) throw new AggregateError(build.logs, 'Could not build the production report table')
const bundleRoot = await realpath(output)
if (!await Bun.file(resolve(staticRoot, 'app.css')).exists()) throw new Error('Production CSS is missing. Run bun run build:css first.')

const columns: MediaColumn[] = [
  { key: 'photo_url', label: manufacturingColumnLabels.photo_url, width: 110, content: { kind: 'image', display: 'inline', width: 64, height: 48, altField: 'part_name' } },
  { key: 'part_name', label: manufacturingColumnLabels.part_name, role: 'row_header', width: 230 },
  { key: 'parent_assembly', label: manufacturingColumnLabels.parent_assembly, width: 190 },
  { key: 'stock', label: manufacturingColumnLabels.stock, role: 'metric', align: 'right', format: 'integer', width: 105 },
  { key: 'unit_cost', label: manufacturingColumnLabels.unit_cost, role: 'metric', align: 'right', visualizationFormat: { kind: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2 }, width: 155 },
  { key: 'datasheet_url', label: manufacturingColumnLabels.datasheet_url, width: 230, content: { kind: 'link', labelField: 'part_name', newTab: true } },
  { key: 'preview_url', label: manufacturingColumnLabels.preview_url, width: 150, content: { kind: 'image', display: 'tooltip', width: 240, height: 180, altField: 'part_name' } },
]
const rows: TableRow[] = manufacturingParts.map(part => ({ ...part }))
const manufacturing: Fixture = {
  title: 'Manufacturing parts catalog', rowHeight: 64, columns, rows,
  description: 'Five illustrative manufacturing parts with local technical drawings, stock and unit costs. Hover or focus Image preview for a larger drawing; Datasheet opens a local page with clearly labeled example specifications.',
}
const failures: Fixture = {
  ...manufacturing, title: 'Image fallbacks · missing and broken sources',
  description: 'Illustrative fallback records. One image URL points to a missing local file, one is empty, and one image is valid. Datasheet links retain the part names.',
  rows: [
    { ...rows[0], part_id: 'broken', part_name: 'Electric motor · broken image', photo_url: '/static/files/table-manufacturing-demo/missing.svg', preview_url: '/static/files/table-manufacturing-demo/missing.svg' },
    { ...rows[1], part_id: 'empty', part_name: 'Helical gear · missing image', photo_url: '', preview_url: '' },
    { ...rows[2], part_id: 'valid' },
  ],
}
const fixtures = { manufacturing, failures }

const escapeHTML = (value: string): string => value.replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]!)

function documentFor(url: URL): string {
  const fixture = url.searchParams.get('fixture') === 'failures' ? 'failures' : 'manufacturing'
  const before = url.searchParams.get('version') === 'before'
  const payload = JSON.stringify(fixtures[fixture]).replaceAll('<', '\\u003c')
  return `<!doctype html><html lang="en" data-color-mode="light" data-light-theme="light" data-dark-theme="dark"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Table images and links playground</title><link rel="stylesheet" href="/static/app.css">
<style>
*{box-sizing:border-box}body{margin:0;background:var(--lv-bg-page,#f6f8fa);color:var(--lv-fg-default,#1f2328);font:14px/1.5 system-ui,sans-serif;}main{max-width:1430px;margin:auto;padding:30px 36px;}header{display:flex;justify-content:space-between;align-items:flex-start;gap:24px;margin-bottom:18px;}h1{margin:0 0 6px;font-size:25px;letter-spacing:-.5px}.subtitle,.description{color:var(--lv-fg-muted,#59636e);font-size:13px}.subtitle{margin:0}.description{max-width:1060px;line-height:1.6;margin:0 0 20px}.version{display:inline-block;font-size:11px;font-weight:650;letter-spacing:.5px;text-transform:uppercase;background:var(--lv-bg-panel,#fff);border:1px solid var(--lv-line-default,#d1d9e0);border-radius:5px;padding:4px 8px;margin-bottom:10px}.controls{display:flex;align-items:center;gap:12px;flex-wrap:wrap;justify-content:flex-end}label{display:flex;align-items:center;gap:7px;font-size:13px}select,button,input{font:inherit;color:inherit}select,button,input[type=number]{padding:6px 9px;border:1px solid var(--lv-line-default,#d1d9e0);border-radius:6px;background:var(--lv-bg-panel,#fff)}input[type=number]{width:66px}a{color:var(--lv-fg-link,#0969da);font-size:13px;white-space:nowrap}button{cursor:pointer}.dimension-controls{display:flex;align-items:center;gap:20px;margin-bottom:14px;flex-wrap:wrap}.dimension-controls span{font-size:12px;color:var(--lv-fg-muted,#59636e)}.table-card{height:auto;--lv-visual-height:auto;--lv-table-max-height:calc(630px - 2px);border:1px solid var(--lv-line-default,#d1d9e0);border-radius:8px;background:var(--lv-bg-panel,#fff);overflow:hidden;box-shadow:0 3px 14px rgba(31,35,40,.035)}lv-report-table{display:block;height:auto;width:100%}body[data-version='before'] .table-card{--lv-visual-height:100%;height:630px}body[data-version='before'] lv-report-table{height:100%}details{margin-top:20px;border:1px solid var(--lv-line-default,#d1d9e0);border-radius:8px;background:var(--lv-bg-panel,#fff)}summary{padding:13px 16px;cursor:pointer;font-weight:600}.editor{padding:0 16px 16px}.editor p{font-size:13px;color:var(--lv-fg-muted,#59636e);margin:0 0 10px}textarea{display:block;width:100%;height:340px;resize:vertical;padding:12px;border:1px solid var(--lv-line-default,#d1d9e0);border-radius:6px;background:var(--lv-bg-page,#f6f8fa);color:inherit;font:12px/1.6 ui-monospace,monospace}.editor-actions{display:flex;align-items:center;gap:10px;margin-top:12px}#status{font-size:13px;color:var(--lv-fg-muted,#59636e)}#status.error{color:var(--lv-fg-danger,#cf222e)}.primary{background:var(--lv-bg-accent,#0969da);color:white;border-color:transparent}@media(max-width:760px){main{padding:20px 14px}header{display:block}.controls{margin-top:16px;justify-content:flex-start}.table-card{--lv-table-max-height:calc(650px - 2px)}body[data-version='before'] .table-card{height:650px}h1{font-size:22px}}
</style></head><body data-version="${before ? 'before' : 'after'}"><main><header><div><span class="version">${before ? 'Before · URL strings' : 'After · images and links'}</span><h1>Table images and links</h1><p class="subtitle">Production report table · illustrative manufacturing parts · local preview</p></div><div class="controls"><form method="get"><label>Example <select name="fixture" onchange="this.form.submit()"><option value="manufacturing"${fixture === 'manufacturing' ? ' selected' : ''}>Manufacturing parts</option><option value="failures"${fixture === 'failures' ? ' selected' : ''}>Broken and missing images</option></select></label>${before ? '<input type="hidden" name="version" value="before">' : ''}</form>${baselineAvailable ? `<a href="/?fixture=${fixture}${before ? '' : '&version=before'}">${before ? 'View images and links' : 'View URL baseline'}</a>` : ''}</div></header>
<p class="description" id="description">${escapeHTML(fixtures[fixture].description)}</p><div class="dimension-controls"><label>Row height <select id="row-height"><option value="48">48 px</option><option value="64" selected>64 px</option><option value="80">80 px</option></select></label><label>Inline width <input id="image-width" type="number" min="16" max="160" step="8" value="64"></label><label>Inline height <input id="image-height" type="number" min="16" max="68" step="4" value="48"></label><span>Inline height fits inside the selected row.</span></div>
<section class="table-card" aria-label="Production manufacturing parts table preview"><lv-report-table></lv-report-table></section><details${url.searchParams.get('editor') === 'open' ? ' open' : ''}><summary>Edit columns, image display and source records</summary><div class="editor"><p>Cell content binds image URLs or hyperlinks to fields. Change image display to inline or tooltip, edit dimensions and labels, or replace records below.</p><textarea id="source" aria-label="Table fixture JSON" spellcheck="false"></textarea><div class="editor-actions"><button id="apply" class="primary" type="button">Apply JSON</button><button id="reset" type="button">Reset example</button><span id="status" role="status" aria-live="polite">Ready</span></div></div></details></main>
<script type="module">
import '${before ? '/before/' : '/bundle/'}report-table.js';
const initial=${payload};const before=${before};const table=document.querySelector('lv-report-table');table.tableId='manufacturing-demo';
const editor=document.querySelector('#source'),status=document.querySelector('#status'),heightControl=document.querySelector('#row-height'),widthControl=document.querySelector('#image-width'),imageHeightControl=document.querySelector('#image-height');
let current=structuredClone(initial),revision=0;
function applyFixture(input){
  if(!input || !Array.isArray(input.columns) || !Array.isArray(input.rows))throw new Error('Provide columns and rows.');
  if(input.columns.some(column=>!column || typeof column.key!=='string' || typeof column.label!=='string'))throw new Error('Every column needs a key and label.');
  for(const column of input.columns){if(column.content && !['image','link'].includes(column.content.kind))throw new Error('Cell content kind must be image or link.');if(column.content?.kind==='image' && !['inline','tooltip'].includes(column.content.display))throw new Error('Image display must be inline or tooltip.');}
  const rowHeight=Number(input.rowHeight??64);if(!Number.isFinite(rowHeight) || rowHeight<32 || rowHeight>120)throw new Error('Row height must be from 32 to 120 pixels.');
  current=structuredClone(input);revision++;
  const columns=input.columns.map(column=>{
    if(before){const copy={...column};delete copy.content;return copy;}
    if(column.content?.kind==='image' && column.content.display==='inline')return {...column,content:{...column.content,width:Math.max(16,Math.min(160,Number(column.content.width)||64)),height:Math.max(16,Math.min(rowHeight-12,Number(column.content.height)||48))}};
    return column;
  });
  const sort={key:'part_name',direction:'asc'},chunkSize=Math.max(50,input.rows.length),block=(start,rows)=>({start,rows,requestSeq:0,resetVersion:revision,sort});
  table.table={id:'manufacturing-demo',version:2,type:'table',title:input.title||'Manufacturing parts catalog',style:{density:'comfortable',zebra:false,grid:'rows',showHeader:true},columns,
    cardinality:{kind:'exact',value:input.rows.length},availableRows:input.rows.length,isCapped:false,rowCap:10000,chunkSize,rowHeight,resetVersion:revision,sort,
    blocks:{a:block(0,input.rows.map(row=>({...row,__rowKey:row.part_id}))),b:block(chunkSize,[]),c:block(chunkSize*2,[])},loadingBlock:'',error:'',selection:[]};
  document.querySelector('#description').textContent=input.description||'Illustrative manufacturing parts records.';
  if([...heightControl.options].some(option=>Number(option.value)===rowHeight))heightControl.value=String(rowHeight);
  imageHeightControl.max=String(rowHeight-12);
  const inline=columns.find(column=>column.content?.kind==='image' && column.content.display==='inline');
  if(inline){widthControl.value=String(inline.content.width);imageHeightControl.value=String(inline.content.height);}
  status.className='';status.textContent='Applied '+input.rows.length+' records';
}
function setDimensions(){try{
  const next=structuredClone(current);next.rowHeight=Number(heightControl.value);
  for(const column of next.columns){if(column.content?.kind==='image' && column.content.display==='inline'){column.content.width=Number(widthControl.value);column.content.height=Math.min(Number(imageHeightControl.value),next.rowHeight-12);}}
  applyFixture(next);editor.value=JSON.stringify(current,null,2);
}catch(error){status.className='error';status.textContent=error.message;}}
heightControl.addEventListener('change',setDimensions);widthControl.addEventListener('change',setDimensions);imageHeightControl.addEventListener('change',setDimensions);
document.querySelector('#apply').addEventListener('click',()=>{try{applyFixture(JSON.parse(editor.value));}catch(error){status.className='error';status.textContent=error.message;}});
document.querySelector('#reset').addEventListener('click',()=>{editor.value=JSON.stringify(initial,null,2);applyFixture(structuredClone(initial));});
table.addEventListener('lv-visual-window-change',event=>{
  const request=event.detail,rows=current.rows.map(row=>({...row,__rowKey:row.part_id}));rows.sort((a,b)=>{const left=a[request.sort.key],right=b[request.sort.key];const order=typeof left==='number'&&typeof right==='number'?left-right:String(left??'').localeCompare(String(right??''));return request.sort.direction==='desc'?-order:order;});
  const sort=request.sort,resetVersion=request.resetVersion,chunkSize=Math.max(50,rows.length),block=(start,data)=>({start,rows:data,requestSeq:request.requestSeq,resetVersion,sort});
  table.table={...table.table,sort,resetVersion,blocks:{a:block(0,rows),b:block(chunkSize,[]),c:block(chunkSize*2,[])}};
});
editor.value=JSON.stringify(initial,null,2);applyFixture(initial);
</script></body></html>`
}

async function asset(base: string, relative: string, head: boolean): Promise<Response> {
  try {
    const candidate = await realpath(resolve(base, relative)), actualBase = await realpath(base)
    if (!candidate.startsWith(actualBase + sep)) return new Response('Not found', { status: 404 })
    const file = Bun.file(candidate)
    if (!await file.exists()) return new Response('Not found', { status: 404 })
    return new Response(head ? null : file, { headers: { 'content-type': file.type, 'cache-control': 'no-store' } })
  } catch { return new Response('Not found', { status: 404 }) }
}
const server = Bun.serve({ hostname: '127.0.0.1', port, async fetch(request) {
  if (!['GET', 'HEAD'].includes(request.method)) return new Response('Method not allowed', { status: 405 })
  const url = new URL(request.url), head = request.method === 'HEAD'
  if (url.pathname === '/' && url.searchParams.get('version') === 'before' && !baselineAvailable) return new Response('Before bundle unavailable. Open / for the current playground.', { status: 404 })
  if (url.pathname === '/') return new Response(head ? null : documentFor(url), { headers: { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' } })
  let path: string
  try { path = decodeURIComponent(url.pathname) } catch { return new Response('Bad request', { status: 400 }) }
  if (path.startsWith('/bundle/')) return asset(bundleRoot, path.slice('/bundle/'.length), head)
  if (path.startsWith('/before/')) return asset(baselineRoot, path.slice('/before/'.length), head)
  if (path.startsWith('/static/')) return asset(staticRoot, path.slice('/static/'.length), head)
  return new Response('Not found', { status: 404 })
} })
console.log(`Table media playground: ${server.url}`)
console.log(`URL baseline: ${server.url}?version=before`)
