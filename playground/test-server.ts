import { resolve } from 'node:path'
import { playgroundResponse } from './server'

/** Build outside bun:test's module resolver, as the repository's DOM test builds do. */
export async function startTestPlayground() {
  const build = Bun.spawn([process.execPath, '-e', "import { buildPlayground } from './playground/server'; await buildPlayground()"], {
    cwd: resolve(import.meta.dir, '..'), stdout: 'pipe', stderr: 'pipe',
  })
  const [code, stdout, stderr] = await Promise.all([
    build.exited, new Response(build.stdout).text(), new Response(build.stderr).text(),
  ])
  if (code !== 0) throw new Error(`Playground test build failed:\n${stdout}${stderr}`)
  return Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: playgroundResponse })
}
