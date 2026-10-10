// Local review with illustrative data: bun scripts/dashboard_visual_feedback_playground.ts [port]
import { realpath } from 'node:fs/promises'
import { resolve, sep } from 'node:path'
import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'
import { testVisualizationEnvelopes } from '../web/components/dashboard/dashboard-page-test-fixtures'

const root = resolve(import.meta.dir, '..')
const output = resolve(root, '.tmp/dashboard-visual-feedback-playground')
const port = Number(Bun.argv[2] ?? 18323)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Invalid port')
const build = await Bun.build({entrypoints: [resolve(root, 'web/components/dashboard/dashboard-generation.ts'), resolve(root, 'web/components/chat/chat-visual-panel.ts')], target: 'browser', format: 'esm', splitting: true, outdir: output, external: [datastarRuntimeURL], naming: { entry: '[name].[ext]', chunk: 'chunks/[name]-[hash].[ext]' }})
if (!build.success) throw new AggregateError(build.logs, 'Could not build playground')
const bundleRoot = await realpath(output)
const staticRoot = await realpath(resolve(root, 'static'))
const envelopes = testVisualizationEnvelopes()
for (const envelope of Object.values(envelopes)) {
  envelope.status = { kind: 'ready' }
  envelope.diagnostics = []
}
const chart = Object.values(envelopes).find(item => item.spec.kind === 'cartesian')
const metric = Object.values(envelopes).find(item => item.spec.kind === 'kpi')
const html = `<!doctype html><html lang="en" data-color-mode="dark" data-dark-theme="dark"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Dashboard visual feedback playground</title><link rel="stylesheet" href="/static/app.css"><script src="/static/theme.js"></script><style>body{margin:0;background:var(--lv-bg-app);color:var(--lv-fg-default);font:14px/1.5 system-ui}header{padding:16px 24px;border-bottom:var(--lv-border-default)}h1{font-size:20px;margin:0}header p{margin:6px 0;color:var(--lv-fg-muted)}nav{display:flex;gap:16px}a{color:var(--lv-fg-link)}main{padding:20px;display:grid;gap:24px}section{min-height:540px}lv-dashboard-generation{display:block;height:700px}lv-chat-visual-panel{display:block;height:100%;max-width:1000px;margin:auto}section.metric{min-height:280px;height:280px}@media(max-width:600px){main{padding:12px}lv-dashboard-generation{height:820px}}</style></head><body><header><h1>Dashboard visual feedback playground</h1><p>Production components · illustrative sample data · decorative assembly animation</p><nav><a href="#building">Building</a><a href="#chart">Chart preview</a><a href="#metric">Compact metric</a></nav></header><main><section id="building"><lv-dashboard-generation></lv-dashboard-generation></section><section id="chart"><lv-chat-visual-panel></lv-chat-visual-panel></section><section id="metric" class="metric"><lv-chat-visual-panel></lv-chat-visual-panel></section></main><script type="module">import '/bundle/dashboard-generation.js';import '/bundle/chat-visual-panel.js';const generation=document.querySelector('lv-dashboard-generation');generation.running=true;generation.prompt='Build a complete sales dashboard';const fixtures=${JSON.stringify({chart,metric}).replaceAll('<','\\u003c')};for(const [id,payload] of Object.entries(fixtures)){if(!payload)continue;const panel=document.querySelector('#'+id+' lv-chat-visual-panel');panel.payload=payload;panel.title=payload.spec.title||id;panel.dashboardAvailable=true;}</script></body></html>`
async function asset(base: string, relative: string) {
  try {
    const path = await realpath(resolve(base, relative))
    if (!path.startsWith(base + sep)) return new Response('Not found', { status: 404 })
    const file = Bun.file(path)
    if (!await file.exists()) return new Response('Not found', { status: 404 })
    return new Response(file)
  } catch { return new Response('Not found', { status: 404 }) }
}
const server = Bun.serve({hostname: '127.0.0.1',port,async fetch(request) {
  if (!['GET','HEAD'].includes(request.method)) return new Response('Method not allowed', {status:405})
  const url = new URL(request.url)
  if (url.pathname === '/') return new Response(html, {headers: {'Content-Type':'text/html; charset=utf-8'}})
  let path: string
  try { path = decodeURIComponent(url.pathname) } catch { return new Response('Bad request',{status:400}) }
  if(path.startsWith('/bundle/')) return asset(bundleRoot,path.slice(8))
  if(path.startsWith('/static/')) return asset(staticRoot,path.slice(8))
  return new Response('Not found',{status:404})
}})
console.log(`Dashboard visual feedback playground: ${server.url}`)
