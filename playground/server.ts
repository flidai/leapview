import { mkdir } from 'node:fs/promises'
import { watch, type FSWatcher } from 'node:fs'
import { resolve, sep } from 'node:path'
import { buildMapLibreWorker } from '../scripts/build_maplibre_worker'
import { datastarRuntimeURL } from '../web/components/shared/datastar-runtime'

const root = resolve(import.meta.dir, '..')
const output = resolve(root, '.tmp/playground')
let buildID = ''
let liveReload: ReturnType<typeof createLiveReload> | undefined

/** Reuse the production bundler shape, with a separate output tree. */
export async function buildPlayground() {
  const nextBuildID = crypto.randomUUID()
  await mkdir(output, { recursive: true })
  const css = Bun.spawn(['bun', 'run', 'build:css'], { cwd: root, stdout: 'ignore', stderr: 'pipe' })
  const cssErrors = new Response(css.stderr).text()
  if (await css.exited !== 0) throw new Error(await cssErrors)
  const result = await Bun.build({
    entrypoints: [resolve(root, 'playground/app.ts'), resolve(root, 'web/components/shared/monaco-editor-worker.ts')],
    root: root,
    target: 'browser', format: 'esm', splitting: true,
    external: [datastarRuntimeURL],
    define: { __PLAYGROUND_BUILD_ID__: JSON.stringify(nextBuildID) },
    outdir: output,
    naming: { entry: '[name].[ext]', chunk: 'chunks/[name]-[hash].[ext]' },
  })
  if (!result.success) throw new AggregateError(result.logs, 'Playground build failed')
  await buildMapLibreWorker(output)
  buildID = nextBuildID
}

/** Only serve browser assets; never expose the repository or proxy a backend. */
export async function playgroundResponse(request: Request): Promise<Response> {
  if (request.method !== 'GET' && request.method !== 'HEAD') return new Response('Method not allowed', { status: 405 })
  const path = new URL(request.url).pathname
  if (path === '/__playground/events') return request.method === 'HEAD' || !liveReload ? new Response(null, { status: 204 }) : liveReload.response(request)
  if ((path === '/' || path === '/index.html') && liveReload && !buildID) return initialBuildResponse(request)
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

type BuildEvent = { type: 'rebuilding' | 'rebuilt' | 'error'; buildID: string; message?: string }

function createLiveReload() {
  const clients = new Set<ReadableStreamDefaultController<Uint8Array>>()
  const watchers: FSWatcher[] = []
  const encoder = new TextEncoder()
  let latest: BuildEvent = { type: 'rebuilding', buildID }
  let timer: ReturnType<typeof setTimeout> | undefined
  let pending = false
  let running = false
  let closed = false
  const frame = (event: BuildEvent) => encoder.encode(`event: ${event.type}\ndata: ${JSON.stringify(event)}\n\n`)
  const publish = (event: BuildEvent) => {
    latest = event
    for (const client of clients) {
      try { client.enqueue(frame(event)) } catch { clients.delete(client) }
    }
  }
  const fail = (error: unknown) => {
    const message = error instanceof AggregateError
      ? `${error.message}\n${error.errors.map(item => String(item)).join('\n')}`
      : error instanceof Error ? error.message : String(error)
    console.error(message)
    publish({ type: 'error', buildID, message })
  }
  const rebuild = async () => {
    if (running || closed || !pending) return
    running = true
    try {
      do {
        pending = false
        publish({ type: 'rebuilding', buildID })
        try {
          await buildPlayground()
          // A save during the build gets one more pass before browsers reload.
          if (!pending && !closed) publish({ type: 'rebuilt', buildID })
        } catch (error) {
          if (!closed) fail(error)
        }
      } while (pending && !closed)
    } finally {
      running = false
    }
  }
  const schedule = () => {
    if (closed) return
    pending = true
    clearTimeout(timer)
    timer = setTimeout(() => { void rebuild() }, 160)
  }
  // Watch source trees, never the repository recursively (node_modules/.git/.tmp).
  for (const directory of ['playground', 'web', 'static', 'docs/visuals', 'scripts']) {
    const watcher = watch(resolve(root, directory), { recursive: true }, (_event, name) => {
      if (!name) { schedule(); return }
      const path = `${directory}/${String(name).replaceAll('\\', '/')}`
      if (/(^|\/)(node_modules|\.git|\.tmp|generated|dist|build)(\/|$)/.test(path)) return
      if (path === 'static/app.css' || /\.(?:test|gen)\.[^.]+$/.test(path)) return
      if (/\.(?:tsx?|jsx?|mjs|css|html|json|svg|geojson)$/.test(path)) schedule()
    })
    watcher.on('error', fail)
    watchers.push(watcher)
  }
  const configuration = watch(root, (_event, name) => {
    if (name && /^(?:package\.json|bun\.lockb?|bunfig\.toml|tsconfig.*\.json)$/.test(String(name))) schedule()
  })
  configuration.on('error', fail)
  watchers.push(configuration)
  return {
    start() { pending = true; return rebuild() },
    response(request: Request) {
      let client: ReadableStreamDefaultController<Uint8Array>
      const dispose = () => { clients.delete(client); request.signal.removeEventListener('abort', abort) }
      const abort = () => { dispose(); try { client.close() } catch { /* Already disconnected. */ } }
      const stream = new ReadableStream<Uint8Array>({
        start(controller) {
          client = controller
          if (request.signal.aborted) { controller.close(); return }
          clients.add(controller)
          request.signal.addEventListener('abort', abort, { once: true })
          controller.enqueue(frame(latest))
        },
        cancel: dispose,
      })
      return new Response(stream, { headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store', 'X-Accel-Buffering': 'no' } })
    },
    close() {
      closed = true
      clearTimeout(timer)
      watchers.forEach(watcher => watcher.close())
      for (const client of clients) { try { client.close() } catch { /* Already disconnected. */ } }
      clients.clear()
    },
  }
}

/** Keep an initially broken build recoverable even before the app bundle exists. */
function initialBuildResponse(request: Request) {
  const html = `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Playground build</title><body><p role="status">Building the playground…</p><pre role="alert" style="white-space:pre-wrap"></pre><script>
    const events = new EventSource('/__playground/events');
    events.addEventListener('rebuilt', () => location.reload());
    events.addEventListener('rebuilding', () => { document.querySelector('p').textContent = 'Building the playground…'; });
    events.addEventListener('error', event => { if (event.data) { document.querySelector('p').textContent = 'Build failed. Save a correction to retry.'; document.querySelector('pre').textContent = JSON.parse(event.data).message; } });
  </script></body></html>`
  return new Response(request.method === 'HEAD' ? null : html, { headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' } })
}

export async function startPlayground(port = Number(process.env.PLAYGROUND_PORT || 4400), options: { watch?: boolean } = {}) {
  const reload = options.watch ? createLiveReload() : undefined
  if (reload) liveReload = reload
  else await buildPlayground()
  let server: ReturnType<typeof Bun.serve>
  try {
    server = Bun.serve({
      hostname: '127.0.0.1', port,
      fetch(request, server) {
        if (new URL(request.url).pathname === '/__playground/events') server.timeout(request, 0)
        return playgroundResponse(request)
      },
    })
  } catch (error) {
    reload?.close()
    if (liveReload === reload) liveReload = undefined
    throw error
  }
  if (reload) {
    const stop = server.stop.bind(server)
    server.stop = (closeActiveConnections?: boolean) => {
      reload.close()
      if (liveReload === reload) liveReload = undefined
      return stop(closeActiveConnections)
    }
    void reload.start()
  }
  return server
}

if (import.meta.main) {
  const server = await startPlayground(undefined, { watch: true })
  console.log(`LeapView playground: ${server.url}\nWatching sources; successful builds reload the current tab.`)
  for (const signal of ['SIGINT', 'SIGTERM'] as const) process.once(signal, () => { void server.stop(true) })
}
