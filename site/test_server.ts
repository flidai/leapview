import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const root = process.cwd()

export interface SiteTestServer {
  readonly process: Bun.Subprocess
  stop(): Promise<void>
}

/**
 * Build and run the public site as the process that owns the listening socket.
 *
 * `go run` leaves the compiled child behind when its wrapper is terminated.
 * A temporary executable keeps the test lifecycle to one process. It remains
 * in the caller's process group so the CI watchdog also terminates it when a
 * test process is force-killed.
 */
export async function startSiteTestServer(port: number, startupDeadline = Date.now() + 60_000): Promise<SiteTestServer> {
  const directory = await mkdtemp(join(tmpdir(), 'leapview-site-test-'))
  const binary = join(directory, 'leapview-site')
  try {
    const buildTimeout = startupDeadline - Date.now()
    if (buildTimeout <= 0) throw new Error('site test server startup deadline expired before build')
    const build = Bun.spawn(['go', 'build', '-o', binary, './cmd/leapview-site'], {
      cwd: root,
      env: process.env,
      stdout: 'pipe',
      stderr: 'pipe',
      killSignal: 'SIGKILL',
      timeout: buildTimeout,
    })
    const [exitCode, stdout, stderr] = await Promise.all([
      build.exited,
      readProcessOutput(build.stdout),
      readProcessOutput(build.stderr),
    ])
    if (exitCode !== 0) {
      throw new Error(`building leapview-site failed with exit code ${exitCode}:\n${stderr || stdout}`)
    }
    if (Date.now() >= startupDeadline) throw new Error('site test server startup deadline expired after build')

    const siteProcess = Bun.spawn([binary, '-addr', `127.0.0.1:${port}`], {
      cwd: root,
      env: process.env,
      stdout: 'ignore',
      stderr: 'ignore',
    })
    let stopped = false
    return {
      process: siteProcess,
      async stop(): Promise<void> {
        if (stopped) return
        stopped = true
        try {
          await terminate(siteProcess)
        } finally {
          await rm(directory, { recursive: true, force: true })
        }
      },
    }
  } catch (error) {
    await rm(directory, { recursive: true, force: true })
    throw error
  }
}

async function terminate(siteProcess: Bun.Subprocess): Promise<void> {
  if (siteProcess.exitCode !== null) {
    await siteProcess.exited
    return
  }

  let forceKillTimer: ReturnType<typeof setTimeout> | undefined
  try {
    signal(siteProcess, 'SIGTERM')
    forceKillTimer = setTimeout(() => {
      if (siteProcess.exitCode === null) signal(siteProcess, 'SIGKILL')
    }, 5_000)
    await siteProcess.exited
  } finally {
    if (forceKillTimer !== undefined) clearTimeout(forceKillTimer)
  }
}

function signal(siteProcess: Bun.Subprocess, name: 'SIGTERM' | 'SIGKILL'): void {
  try {
    siteProcess.kill(name)
  } catch (error) {
    if ((error as NodeJS.ErrnoException)?.code !== 'ESRCH') throw error
  }
}

async function readProcessOutput(stream: ReadableStream<Uint8Array> | undefined): Promise<string> {
  if (!stream) return ''
  return new Response(stream).text()
}
