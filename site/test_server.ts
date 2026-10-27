import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const root = process.cwd()

export interface SiteTestServer {
  readonly baseURL: string
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
export async function startSiteTestServer(port = 0, startupDeadline = Date.now() + 60_000): Promise<SiteTestServer> {
  const directory = await mkdtemp(join(tmpdir(), 'leapview-site-test-'))
  const binary = join(directory, 'leapview-site')
  let siteProcess: Bun.Subprocess | undefined
  let logs: Promise<unknown> | undefined
  let output = ''
  const capture = async (stream: ReadableStream<Uint8Array> | undefined): Promise<void> => {
    if (!stream) return
    const decoder = new TextDecoder()
    for await (const chunk of stream) {
      output = (output + decoder.decode(chunk, { stream: true })).slice(-16_384)
    }
  }
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
    const [exitCode] = await Promise.all([
      build.exited,
      capture(build.stdout),
      capture(build.stderr),
    ])
    if (exitCode !== 0) {
      throw new Error(`building leapview-site failed with exit code ${exitCode}`)
    }
    if (Date.now() >= startupDeadline) throw new Error('site test server startup deadline expired after build')

    output = ''
    const child = Bun.spawn([binary, '-addr', `127.0.0.1:${port}`], {
      cwd: root,
      env: process.env,
      stdout: 'pipe',
      stderr: 'pipe',
    })
    siteProcess = child
    logs = Promise.all([capture(child.stdout), capture(child.stderr)])
    // Observe the readers immediately, then await them when the child exits.
    void logs.catch(() => {})
    let baseURL: string | undefined
    while (Date.now() < startupDeadline) {
      if (child.exitCode !== null || child.signalCode !== null) {
        await logs
        throw new Error(`site exited before becoming ready (code ${child.exitCode}, signal ${child.signalCode})`)
      }
      const address = output.match(/LeapView site listening on (127\.0\.0\.1:[1-9]\d*)\r?\n/)
      if (address) {
        const candidate = `http://${address[1]}`
        try {
          const response = await fetch(`${candidate}/healthz`, {
            signal: AbortSignal.timeout(Math.max(1, Math.min(1_000, startupDeadline - Date.now()))),
          })
          await response.body?.cancel()
          if (response.ok && Date.now() < startupDeadline && child.exitCode === null && child.signalCode === null) {
            baseURL = candidate
            break
          }
        } catch {
          // Retry until the shared build/startup deadline.
        }
      }
      await Bun.sleep(Math.max(0, Math.min(50, startupDeadline - Date.now())))
    }
    if (!baseURL) throw new Error('site test server startup deadline expired waiting for health')

    let stopping: Promise<void> | undefined
    return {
      baseURL,
      process: child,
      stop(): Promise<void> {
        return stopping ??= (async () => {
          try {
            await terminate(child)
            await logs
          } finally {
            await rm(directory, { recursive: true, force: true })
          }
        })()
      },
    }
  } catch (error) {
    try {
      if (siteProcess) await terminate(siteProcess)
      await logs
    } finally {
      await rm(directory, { recursive: true, force: true })
    }
    throw new Error(`${error instanceof Error ? error.message : error}\nSite startup output (last 16,384 characters):\n${output || '(no output)'}`, { cause: error })
  }
}

async function terminate(siteProcess: Bun.Subprocess): Promise<void> {
  if (siteProcess.exitCode !== null || siteProcess.signalCode !== null) {
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
