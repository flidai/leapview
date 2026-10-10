// Local production-builder playground, not a test runner.
// Run: bun scripts/table_formatting_playground.ts [port]
import { realpath } from 'node:fs/promises'
import { resolve, sep } from 'node:path'
import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'
import { ensureManufacturingDemoAssets, manufacturingParts, manufacturingColumnLabels } from './table_manufacturing_fixture'
import { handleTableExplorerRequest, tableExploreHref, tableExplorerDocument } from './table_explorer_playground'

const root = resolve(import.meta.dir, '..')
const output = resolve(root, '.tmp/table-formatting-playground')
const baseline = resolve(root, '.tmp/table-formatting-before')
const staticRoot = await realpath(resolve(root, 'static'))
await ensureManufacturingDemoAssets(staticRoot)
const port = Number(Bun.argv[2] ?? 18306)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Invalid port')
const build = await Bun.build({ entrypoints: [resolve(root, 'web/components/dashboard/dashboard-builder.ts'), resolve(root, 'web/components/data/data-explorer.ts')], target: 'browser', format: 'esm', splitting: true, outdir: output, external: [datastarRuntimeURL], naming: { entry: '[name].[ext]', chunk: 'chunks/[name]-[hash].[ext]' } })
if (!build.success) throw new AggregateError(build.logs, 'Could not build dashboard builder')
const buildGo = Bun.spawn(['go', 'build', '-o', resolve(output, 'authoring-demo'), './scripts/table_formatting_demo'], { cwd: root, env: { ...process.env, GOCACHE: '/tmp/leapview-hierarchy-go-cache' }, stdout: 'inherit', stderr: 'inherit' })
if (await buildGo.exited !== 0) throw new Error('Could not build authoring adapter')
const bundleRoot = await realpath(output)
const baselineRoot = await Bun.file(resolve(baseline, 'dashboard-builder.js')).exists() ? await realpath(baseline) : null
const sizingBaseline = resolve(root, '.tmp/table-sizing-before')
const sizingBaselineRoot = await Bun.file(resolve(sizingBaseline, 'dashboard-builder.js')).exists() ? await realpath(sizingBaseline) : null

const initial = {
 apiVersion: 'leapview.dev/v1', kind: 'Dashboard', metadata: { id: 'dashboard:parts-formatting', name: 'parts-formatting', displayName: 'Manufacturing parts catalog' },
 spec: { semanticModel: 'parts', filters: [], visuals: [{ id: 'parts', type: 'table', title: 'Parts inventory · illustrative records', titleVisible: true,
 query: { type: 'records', dataset: 'parts', fields: ['photo_url', 'part_name', 'parent_assembly', 'stock', 'unit_cost', 'datasheet_url', 'preview_url'], limit: 100 },
 presentation: { type: 'table', rowHeight: 80, showHeader: true, striped: false, cellContent: {
 photo_url: { kind: 'image', display: 'inline', width: 56, height: 56, altField: 'part_name' },
 preview_url: { kind: 'image', display: 'tooltip', width: 280, height: 220, altField: 'part_name' },
 datasheet_url: { kind: 'link', labelField: 'part_name', newTab: true },
 } } }], pages: [{ id: 'overview', title: 'Parts inventory', components: [{ id: 'parts-table', type: 'visual', visual: 'parts', placement: { column: 1, row: 1, columnSpan: 12, rowSpan: 10 } }] }] }
}
async function author(input: unknown) {
 const proc = Bun.spawn([resolve(output, 'authoring-demo')], { cwd: root, stdin: new Blob([JSON.stringify(input)]), stdout: 'pipe', stderr: 'pipe' })
 const text = await new Response(proc.stdout).text()
 const result = JSON.parse(text)
 if (await proc.exited !== 0) throw new Error(result.error ?? 'Could not apply formatting')
 return result
}
const seed = await author({ document: initial, number: 1 })

function documentFor(before: boolean, sizingComparison = false) {
 const availableBaseline = sizingComparison ? sizingBaselineRoot : baselineRoot
 return `<!doctype html><html lang="en" data-color-mode="light" data-light-theme="light" data-dark-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Manufacturing parts formatting playground</title><link rel="stylesheet" href="/static/app.css"><style>
html,body{margin:0;height:100%;font-family:system-ui,sans-serif}body{background:var(--lv-bg-app,#f6f8fa);color:var(--lv-fg-default,#1f2328)}header{height:76px;padding:12px 24px;border-bottom:1px solid var(--lv-line-default,#d1d9e0);display:flex;justify-content:space-between;gap:20px;align-items:center}h1{font-size:18px;margin:0 0 4px}p{font-size:12px;margin:0;color:var(--lv-fg-muted,#59636e)}a{color:var(--lv-fg-accent,#0969da);font-size:13px}button{font:inherit;padding:5px 10px;border:1px solid var(--lv-line-default,#d1d9e0);border-radius:6px;background:var(--lv-bg-panel,#fff);cursor:pointer}main{height:calc(100% - 76px)}lv-dashboard-builder{height:100%;display:block}header nav{display:flex;align-items:center;gap:18px}#notice{max-width:520px}#notice.error{color:var(--lv-fg-danger,#cf222e)}
</style></head><body><header><div><h1>${sizingComparison ? (before ? 'Before · table fills its allocated height' : 'After · table fits its rows') : (before ? 'Before · image controls missing' : 'Manufacturing parts · images and formatting')}</h1><p>Production editor · part illustrations · illustrative stock and costs · local example</p></div><nav><span id="notice" role="status"></span><a href="http://127.0.0.1:18303/?fixture=nested">View assembly hierarchy</a><button id="reset">Reset example</button>${availableBaseline ? `<a href="/?${sizingComparison ? 'comparison=sizing&' : ''}${before ? '' : 'version=before'}">${before ? 'View after' : 'View before'}</a>` : ''}</nav></header><main><lv-dashboard-builder back-href="/"></lv-dashboard-builder></main>
<script type="module" src="${datastarRuntimeURL}"></script><script type="module">
import '${before ? sizingComparison ? '/sizing-before/' : '/before/' : '/bundle/'}dashboard-builder.js';
import {mergePatch} from '${datastarRuntimeURL}';
const before=${before};const sizingComparison=${sizingComparison};const seed=${JSON.stringify(seed).replaceAll('<', '\\u003c')};let current=structuredClone(seed);
const storageKey='leapview:parts-formatting:local';
try{const saved=JSON.parse(sessionStorage.getItem(storageKey)||'null');if(saved?.document?.metadata?.id===seed.document.metadata.id&&saved.revision)current=saved;}catch{}
const names=${JSON.stringify(manufacturingColumnLabels)};
const records=${JSON.stringify(manufacturingParts).replaceAll('<', '\\u003c')};
const numericFields=new Set(['stock','unit_cost']);
function formatOptions(options){return options.filter(o=>(!before || sizingComparison || !o.Key.startsWith('cellContent.')) && !['stock','unit_cost'].some(field=>o.Key.startsWith('cellContent.'+field+'.'))).map(o=>({key:o.Key,label:o.Label,section:o.Section.startsWith('Column · ')?'Column · '+(names[o.Section.slice(9)]??o.Section.slice(9)):o.Section,control:o.Control,value:o.Value,placeholder:o.Placeholder||undefined,description:o.Description||undefined,min:o.Minimum??undefined,max:o.Maximum??undefined,step:o.Step??undefined,choices:(o.Choices||[]).map(c=>({value:c.Value,label:names[c.Value]??c.Label}))}));}
function signalsFor(state){
 const authored=state.document.spec.visuals.find(visual=>visual.id==='parts'),presentation=authored.presentation,revision=state.revision;
 const fields=Object.keys(names).map(id=>({id,label:names[id],role:numericFields.has(id)?'metric':'dimension',dataType:id==='stock'?'integer':id==='unit_cost'?'decimal':'string',...(id==='unit_cost'?{format:{kind:'currency',currency:'USD',minimumFractionDigits:2,maximumFractionDigits:2}}:{}),nullable:false}));
 const columns=Object.keys(names).map(id=>({field:{dataset:'primary',field:id},label:names[id],width:({photo_url:100,part_name:225,parent_assembly:190,stock:95,unit_cost:155,datasheet_url:240,preview_url:145})[id],formatting:[]}));
 const rows=records.map(record=>Object.keys(names).map(id=>id==='unit_cost'?record[id].toFixed(2):record[id]));
 const specRevision=revision.id;
 const dataState={kind:'inline',specRevision,dataRevision:revision.number,generation:1,datasets:[{id:'primary',specRevision,dataRevision:revision.number,generation:1,columns:Object.keys(names),rows,completeness:'complete'}]};
 const envelope={schemaVersion:14,visualID:'parts-table',rendererID:'tanstack',specRevision,dataRevision:revision.number,spec:{kind:'table',title:authored.title,datasets:[{id:'primary',fields}],dataBudget:{maxRows:100,requiredCompleteness:'complete'},accessibility:{title:'Manufacturing parts',description:'Illustrative manufacturing inventory with images, parent assemblies, stock, unit costs and example datasheets.'},interactions:[],columns,presentation:{rowHeight:presentation.rowHeight??48,showHeader:presentation.showHeader??true,striped:presentation.striped??false,cellContent:presentation.cellContent}},dataState:{schemaVersion:1,encoding:'json',kind:'inline',specRevision,dataRevision:revision.number,generation:1,payload:JSON.stringify(dataState)},selection:[],highlights:[],status:{kind:'ready'},diagnostics:[],servingStateID:'demo-generation',streamGeneration:1,filterRevision:0,interactionRevision:0,consumerIdentity:'overview/parts-table'};
 const visual={id:'parts-table',visualId:'parts-table',title:authored.title,titleVisible:authored.titleVisible,type:'table',datasetId:'parts',legendVisible:false,axisVisible:false,dataLabelsVisible:false,formatOptions:formatOptions(state.options),placement:{col:1,row:1,colSpan:12,rowSpan:10},slots:Object.keys(names).map(id=>({id,label:names[id],kind:'detail',fieldId:'parts.'+id,required:false})),queryOptions:{supportsSort:true,supportsLimit:true,limit:100,sort:[]},filters:[]};
 return {builder:{projectId:'playground',dashboardId:'parts-formatting',draftId:'demo-draft',revision,title:'Manufacturing parts catalog',lifecycle:'draft',visibility:'private',hasUnpublishedChanges:true,appearance:{icon:'table',color:'green'},origin:{kind:'ui',label:'Playground'},semanticModel:{id:'parts',title:'Manufacturing',datasets:[{id:'parts',title:'Parts',fields:fields.map(f=>({id:'parts.'+f.id,label:f.label,kind:numericFields.has(f.id)?'metric':'dimension',dataType:f.dataType}))}]},visualCatalog:[{type:'table',label:'Table',group:'Tables',referenceHref:'/docs/visuals/table',roles:['detail']}],filters:[],pages:[{id:'overview',title:'Parts inventory',canvas:{width:1200,height:800},grid:{columns:12,rowHeight:48,gap:16,padding:16},visuals:[visual],filterComponents:[]}],selectedPageId:'overview',selectedVisualId:'parts-table',capabilities:{canEdit:true,canShare:false,canPublish:false,canArchive:false,canPreview:false,canExport:false,canAddPage:false,canAddVisual:false},diagnostics:[],preview:{active:false,mode:'draft',loading:false},save:{state:'saved',message:'Saved locally · revision '+revision.number}},builderVisuals:{'parts-table':envelope},status:{loading:false,error:'',generation:1,lastUpdated:'',refreshId:'',setupRequired:false,progressPercent:100},runtime:{kind:'dashboard_builder',projectId:'playground',servingStateId:'demo-generation',dashboardId:'parts-formatting'}};
}
function render(){
 try{sessionStorage.setItem(storageKey,JSON.stringify(current));}catch{}
 mergePatch({builder:null,builderVisuals:null});mergePatch(signalsFor(current));
}
render();
// The standalone fixture has no deployed dashboard session. Configure the
// production host's existing action with a local records-query destination.
const builder=document.querySelector('lv-dashboard-builder');await builder.updateComplete;
function connectExplore(){for(const host of builder.shadowRoot.querySelectorAll('lv-visualization-host'))host.exploreHref=${JSON.stringify(tableExploreHref())};}
const exploreObserver=new MutationObserver(connectExplore);
exploreObserver.observe(builder.shadowRoot,{childList:true,subtree:true});connectExplore();
window.addEventListener('pagehide',()=>exploreObserver.disconnect());
window.addEventListener('pageshow',()=>{exploreObserver.observe(builder.shadowRoot,{childList:true,subtree:true});connectExplore();});
document.querySelector('lv-dashboard-builder').addEventListener('lv-builder-command',async event=>{
 const detail=event.detail;if(detail.action==='select_visual'||detail.action==='select_page')return;
 if(detail.action!=='update_visual_format'||!detail.formatKey){document.querySelector('#notice').textContent='Use the column Format controls in this example.';render();document.dispatchEvent(new CustomEvent('datastar-fetch',{detail:{type:'finished',el:event.currentTarget}}));return;}
 try{const response=await fetch('/format',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({document:current.document,number:current.revision.number,formatKey:detail.formatKey,formatValue:detail.formatValue})});const result=await response.json();if(!response.ok)throw new Error(result.error);current=result;render();const notice=document.querySelector('#notice');notice.className='';notice.textContent='Saved locally · revision '+current.revision.number;}
 catch(error){const notice=document.querySelector('#notice');notice.className='error';notice.textContent=error.message;render();}
 finally{document.dispatchEvent(new CustomEvent('datastar-fetch',{detail:{type:'finished',el:document.querySelector('lv-dashboard-builder')}}));}
});
document.querySelector('#reset').addEventListener('click',()=>{current=structuredClone(seed);render();document.querySelector('#notice').textContent='Example reset.';});
window.tableFormattingDefinition=()=>current.document;
</script></body></html>`
}
async function asset(base: string, relative: string, head: boolean) {
 try { const path = await realpath(resolve(base, relative)); if (!path.startsWith(base + sep)) return new Response('Not found', { status: 404 }); const file = Bun.file(path); return new Response(head ? null : file, { headers: { 'Content-Type': file.type, 'Cache-Control': 'no-store' } }) } catch { return new Response('Not found', { status: 404 }) }
}
const server = Bun.serve({ hostname: '127.0.0.1', port, async fetch(request) {
 const url = new URL(request.url)
 const explorerResponse = await handleTableExplorerRequest(request)
 if (explorerResponse) return explorerResponse
 if (url.pathname === '/format' && request.method === 'POST') {
  const origin = request.headers.get('origin')
  let loopbackOrigin = false
  try { const parsed = new URL(origin ?? ''); loopbackOrigin = parsed.protocol === 'http:' && ['127.0.0.1', 'localhost'].includes(parsed.hostname) } catch {}
  if (!loopbackOrigin || Number(request.headers.get('content-length') ?? 0) > 100_000) return new Response('Forbidden', { status: 403 })
  try { const body = await request.text(); if (body.length > 100_000) return Response.json({error:'Request too large'}, { status: 413 }); return Response.json(await author(JSON.parse(body))) } catch (error) { return Response.json({ error: String(error) }, { status: 400 }) }
 }
 if (!['GET', 'HEAD'].includes(request.method)) return new Response('Method not allowed', { status: 405 })
 const head = request.method === 'HEAD'
 if (url.pathname === '/explore') return new Response(head ? null : tableExplorerDocument(url.search), { headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } })
 if (url.pathname === '/' && url.searchParams.get('version') === 'before' && !(url.searchParams.get('comparison') === 'sizing' ? sizingBaselineRoot : baselineRoot)) return new Response('Before bundle unavailable. Open / for the current playground.', { status: 404 })
 if (url.pathname === '/' || url.pathname === '/dashboards/parts-formatting/pages/overview') return new Response(head ? null : documentFor(url.searchParams.get('version') === 'before', url.searchParams.get('comparison') === 'sizing'), { headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } })
 if (url.pathname.startsWith('/bundle/')) return asset(bundleRoot, url.pathname.slice(8), head)
 if (url.pathname.startsWith('/sizing-before/') && sizingBaselineRoot) return asset(sizingBaselineRoot, url.pathname.slice(15), head)
 if (url.pathname.startsWith('/before/') && baselineRoot) return asset(baselineRoot, url.pathname.slice(8), head)
 if (url.pathname === '/static/app.css' || url.pathname === datastarRuntimeURL.split('?')[0] || url.pathname.startsWith('/static/files/')) return asset(staticRoot, url.pathname.slice(8), head)
 return new Response('Not found', { status: 404 })
} })
console.log('Manufacturing parts playground: '+server.url)
