// Run: bun scripts/lineage_playground.ts [port]
import { realpath } from 'node:fs/promises'
import { resolve, sep } from 'node:path'

type DemoNode = { id: string; label: string; kind: string; meta?: string; selected?: boolean; href?: string }
type DemoGraph = { nodes: DemoNode[]; edges: Array<{ id: string; source: string; target: string; kind: string }> }
const root = resolve(import.meta.dir, '..')
const output = resolve(root, '.tmp/lineage-playground')
const staticRoot = await realpath(resolve(root, 'static'))
const port = Number(Bun.argv[2] ?? 8198)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('Port must be an integer from 1 to 65535')

const build = await Bun.build({
  entrypoints: [resolve(root, 'web/components/shared/asset-lineage-graph.ts')],
  target: 'browser',
  format: 'esm',
  outdir: output,
  naming: { entry: '[name].[ext]' },
})
if (!build.success) throw new AggregateError(build.logs, 'Could not build the lineage component')
const bundleRoot = await realpath(output)
if (!await Bun.file(resolve(staticRoot, 'app.css')).exists()) {
  throw new Error('Production CSS is missing. Run bun run build:css, then start the playground again.')
}

const node = (id: string, label: string, kind: string): DemoNode => ({ id, label, kind, meta: 'Demo data' })
const graph = (nodes: DemoNode[], links: Array<[string, string]>): DemoGraph => ({
  nodes,
  edges: links.map(([source, target]) => ({ id: `${source}:${target}`, source, target, kind: 'dependency' })),
})

const branching = graph([
  node('warehouse', 'Commerce warehouse', 'connection'),
  node('crm', 'Customer CRM', 'connection'),
  node('orders', 'Orders', 'source'),
  node('payments', 'Payments', 'source'),
  node('customers', 'Customers', 'source'),
  node('stg-orders', 'Clean orders', 'model'),
  node('stg-payments', 'Settled payments', 'model'),
  node('stg-customers', 'Customer profiles', 'model'),
  node('revenue', 'Daily revenue', 'model'),
  node('retention', 'Customer retention', 'model'),
  node('sales', 'Sales metrics', 'semantic_model'),
  node('customer-metrics', 'Customer metrics', 'semantic_model'),
  node('overview', 'Commerce overview', 'dashboard'),
  node('growth', 'Growth dashboard', 'dashboard'),
], [
  ['warehouse', 'orders'], ['warehouse', 'payments'], ['crm', 'customers'],
  ['orders', 'stg-orders'], ['payments', 'stg-payments'], ['customers', 'stg-customers'],
  ['stg-orders', 'revenue'], ['stg-payments', 'revenue'], ['stg-customers', 'retention'],
  ['stg-orders', 'retention'], ['revenue', 'sales'], ['retention', 'customer-metrics'],
  ['sales', 'overview'], ['customer-metrics', 'overview'], ['customer-metrics', 'growth'],
])

const domains = ['Orders', 'Payments', 'Customers', 'Products', 'Shipments', 'Returns', 'Campaigns', 'Sessions']
const denseNodes = [node('warehouse', 'Commerce warehouse', 'connection'), node('events', 'Event storage', 'connection')]
const denseLinks: Array<[string, string]> = []
domains.forEach((label, index) => {
  denseNodes.push(node(`source-${index}`, label, 'source'), node(`stage-${index}`, `Clean ${label.toLowerCase()}`, 'model'), node(`mart-${index}`, `${label} daily summary`, 'model'))
  denseLinks.push([index < 6 ? 'warehouse' : 'events', `source-${index}`], [`source-${index}`, `stage-${index}`], [`stage-${index}`, `mart-${index}`])
  if (index !== 2) denseLinks.push(['stage-2', `mart-${index}`])
})
const metricLabels = ['Revenue', 'Operations', 'Growth']
metricLabels.forEach((label, index) => {
  denseNodes.push(node(`metrics-${index}`, `${label} metrics`, 'semantic_model'), node(`dashboard-${index}`, `${label} dashboard`, 'dashboard'))
  domains.forEach((_, domainIndex) => {
    if (domainIndex % 3 === index) denseLinks.push([`mart-${domainIndex}`, `metrics-${index}`])
  })
  denseLinks.push([`metrics-${index}`, `dashboard-${index}`])
})
denseLinks.push(['metrics-0', 'dashboard-2'], ['metrics-1', 'dashboard-0'])

const fixtures = {
  branching,
  dense: graph(denseNodes, denseLinks),
  cycle: graph([
    node('source', 'Account events', 'source'), node('balance', 'Account balances', 'model'),
    node('adjustments', 'Balance adjustments', 'model'), node('report', 'Account report', 'dashboard'),
  ], [['source', 'balance'], ['balance', 'adjustments'], ['adjustments', 'balance'], ['adjustments', 'report']]),
  isolated: graph([node('standalone', 'Unconnected model', 'model')], []),
  empty: graph([], []),
}
type FixtureName = keyof typeof fixtures
const labels: Record<FixtureName, string> = { branching: 'Branching commerce', dense: 'Dense commerce · 32 assets', cycle: 'Cycle', isolated: 'Isolated asset', empty: 'Empty graph' }
const escapeHTML = (value: string): string => value.replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]!)

function documentFor(url: URL): string {
  const requested = url.searchParams.get('fixture') ?? 'branching'
  const fixture: FixtureName = Object.hasOwn(fixtures, requested) ? requested as FixtureName : 'branching'
  const source = fixtures[fixture]
  const selected = source.nodes.find(asset => asset.id === url.searchParams.get('asset'))
  const data: DemoGraph = {
    ...source,
    nodes: source.nodes.map(asset => ({ ...asset, selected: asset.id === selected?.id, href: `/?${new URLSearchParams({ fixture, asset: asset.id })}` })),
  }
  const detail = selected ? `<aside aria-label="Demo asset detail"><strong>${escapeHTML(selected.label)}</strong><span>${escapeHTML(selected.kind.replaceAll('_', ' '))} · ${source.edges.filter(edge => edge.target === selected.id).length} dependencies · ${source.edges.filter(edge => edge.source === selected.id).length} dependents</span><a href="/?fixture=${fixture}">Close asset detail</a></aside>` : ''
  return `<!doctype html>
<html lang="en" data-color-mode="light" data-light-theme="light" data-dark-theme="dark">
<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Lineage playground</title>
<link rel="stylesheet" href="/static/app.css"><link rel="stylesheet" href="/bundle/asset-lineage-graph.css">
<style>
html,body{margin:0;height:100%;}body{display:flex;flex-direction:column;height:100dvh;min-height:420px;background:var(--lv-bg-page);color:var(--lv-fg-default);font:var(--lv-type-body);}
header,aside{display:flex;align-items:center;flex-wrap:wrap;gap:12px;padding:12px 20px;border-bottom:var(--lv-border-default);background:var(--lv-bg-panel);flex-shrink:0;}
h1{margin:0;font:var(--lv-type-body);font-weight:600;}header p,aside span{margin:0;color:var(--lv-fg-muted);font:var(--lv-type-caption);}form{margin-left:auto;}label{display:flex;align-items:center;gap:8px;}
select{font:inherit;border:var(--lv-border-default);border-radius:var(--borderRadius-default);padding:6px 10px;background:var(--lv-bg-panel);color:inherit;}a{color:var(--lv-fg-link);}aside{font:var(--lv-type-caption);}main{flex:1;min-height:0;}lv-asset-lineage-graph{display:block;height:100%;width:100%;}
@media(max-width:600px){header,aside{padding:10px 12px;}header form{margin-left:0;}header p{width:100%;}}
</style></head>
<body><header><h1>Lineage playground</h1><p>Demo data · production lineage component</p><form method="get"><label>Graph <select name="fixture" onchange="this.form.submit()">${Object.entries(labels).map(([key, label]) => `<option value="${key}"${key === fixture ? ' selected' : ''}>${label}</option>`).join('')}</select></label><noscript><button type="submit">Load graph</button></noscript></form></header>
${detail}<main><lv-asset-lineage-graph></lv-asset-lineage-graph></main>
<script type="module">import '/bundle/asset-lineage-graph.js';document.querySelector('lv-asset-lineage-graph').scope='full';document.querySelector('lv-asset-lineage-graph').graph=${JSON.stringify(data).replaceAll('<', '\\u003c')};</script>
</body></html>`
}

async function assetResponse(base: string, relative: string): Promise<Response> {
  try {
    const candidate = await realpath(resolve(base, relative))
    if (!candidate.startsWith(base + sep)) return new Response('Not found', { status: 404 })
    const file = Bun.file(candidate)
    if (!await file.exists()) return new Response('Not found', { status: 404 })
    return new Response(file, { headers: { 'cache-control': 'no-store' } })
  } catch {
    return new Response('Not found', { status: 404 })
  }
}

const server = Bun.serve({
  hostname: '127.0.0.1',
  port,
  async fetch(request) {
    if (request.method !== 'GET' && request.method !== 'HEAD') return new Response('Method not allowed', { status: 405 })
    const url = new URL(request.url)
    if (url.pathname === '/') return new Response(documentFor(url), { headers: { 'content-type': 'text/html; charset=utf-8', 'cache-control': 'no-store' } })
    let pathname: string
    try { pathname = decodeURIComponent(url.pathname) } catch { return new Response('Bad request', { status: 400 }) }
    if (pathname.startsWith('/bundle/')) return assetResponse(bundleRoot, pathname.slice('/bundle/'.length))
    if (pathname.startsWith('/static/')) return assetResponse(staticRoot, pathname.slice('/static/'.length))
    return new Response('Not found', { status: 404 })
  },
})
console.log(`Lineage playground: ${server.url}`)
