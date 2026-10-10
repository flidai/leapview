import { mkdir, mkdtemp, rm } from 'node:fs/promises'
import { resolve } from 'node:path'
import { playgroundBuildResponse, type PlaygroundBuild } from './server'

/** Build outside bun:test's module resolver, in a tree owned by this test server. */
export async function startTestPlayground() {
  const root = resolve(import.meta.dir, '..')
  const parent = resolve(root, '.tmp/playground')
  await mkdir(parent, { recursive: true })
  const outputDirectory = await mkdtemp(resolve(parent, 'test-'))
  try {
    const child = Bun.spawn([process.execPath, '-e', "import { buildPlayground } from './playground/server'; console.log(JSON.stringify(await buildPlayground(process.env.PLAYGROUND_TEST_OUTPUT)))"], {
      cwd: root, env: { ...process.env, PLAYGROUND_TEST_OUTPUT: outputDirectory }, stdout: 'pipe', stderr: 'pipe',
    })
    const [code, stdout, stderr] = await Promise.all([
      child.exited, new Response(child.stdout).text(), new Response(child.stderr).text(),
    ])
    if (code !== 0) throw new Error(`Playground test build failed:\n${stdout}${stderr}`)
    const build = JSON.parse(stdout) as PlaygroundBuild
    const server = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch: request => playgroundBuildResponse(request, build) })
    const stop = server.stop.bind(server)
    server.stop = async (closeActiveConnections?: boolean) => {
      await stop(closeActiveConnections)
      await rm(outputDirectory, { recursive: true, force: true })
    }
    return server
  } catch (error) {
    await rm(outputDirectory, { recursive: true, force: true })
    throw error
  }
}
