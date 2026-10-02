import { mkdir } from 'node:fs/promises'
import { resolve, sep } from 'node:path'
import { buildMapLibreWorker } from '../scripts/build_maplibre_worker'
import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'

const root = resolve(import.meta.dir, '..')
const output = resolve(root, '.tmp/playground')

/** Reuse the production bundler shape, with a separate output tree. */
export async function buildPlayground() {
  await mkdir(output, { recursive: true })
  const css = Bun.spawn(['bun', 'run', 'build:css'], { cwd: root, stdout: 'ignore', stderr: 'pipe' })
  const cssErrors = new Response(css.stderr).text()
  if (await css.exited !== 0) throw new Error(await cssErrors)
  const result = await Bun.build({
    entrypoints: [resolve(root, 'playground/app.ts'), resolve(root, 'web/components/shared/monaco-editor-worker.ts')],
    root: root,
    target: 'browser', format: 'esm', splitting: true,
    external: [datastarRuntimeURL],
    outdir: output,
    naming: { entry: '[name].[ext]', chunk: 'chunks/[name]-[hash].[ext]' },
  })
  if (!result.success) throw new AggregateError(result.logs, 'Playground build failed')
  await buildMapLibreWorker(output)
}

/** Only serve browser assets; never expose the repository or proxy a backend. */
export async function playgroundResponse(request: Request): Promise<Response> {
  if (request.method !== 'GET' && request.method !== 'HEAD') return new Response('Method not allowed', { status: 405 })
  const path = new URL(request.url).pathname
  let filePath: string | undefined
  if (path === '/' || path === '/index.html') filePath = resolve(root, 'playground/index.html')
  else if (path === '/static/monaco-editor-worker.js') filePath = resolve(output, 'monaco-editor-worker.js')
  else if (path === '/static/monaco-editor-css.css') return Response.redirect(new URL('/assets/app.css', request.url), 307)
  else if (path.startsWith('/static/chunks/')) filePath = within(output, path.slice('/static/'.length))
  else if (path === '/static/geometry/br-states-ibge.geojson') filePath = resolve(root, path.slice(1))
  else if (path.startsWith('/assets/')) filePath = within(output, path.slice('/assets/'.length))
  else if (['/static/app.css', '/static/theme.js', '/static/favicon.svg'].includes(path)) filePath = resolve(root, path.slice(1))
  else if (/^\/static\/files\/inter-[\w-]+\.woff2$/.test(path)) filePath = resolve(root, path.slice(1))
  if (!filePath) return new Response('Not found', { status: 404 })
  const file = Bun.file(filePath)
  if (!await file.exists()) return new Response('Not found', { status: 404 })
  return new Response(request.method === 'HEAD' ? null : file, {
    headers: { 'Content-Type': file.type, 'Cache-Control': 'no-store' },
  })
}

function within(directory: string, requested: string): string | undefined {
  let decoded: string
  try { decoded = decodeURIComponent(requested) } catch { return undefined }
  const path = resolve(directory, decoded)
  return path.startsWith(directory + sep) ? path : undefined
}

export async function startPlayground(port = Number(process.env.PLAYGROUND_PORT || 4400)) {
  await buildPlayground()
  return Bun.serve({ hostname: '127.0.0.1', port, fetch: playgroundResponse })
}

if (import.meta.main) {
  const server = await startPlayground()
  console.log(`LeapView playground: ${server.url}\nRestart this command after source changes.`)
}
