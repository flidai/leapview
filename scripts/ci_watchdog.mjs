import { spawn, spawnSync } from 'node:child_process'
import { pathToFileURL } from 'node:url'

const timeoutExitCode = 124

export function parseArguments(arguments_) {
  const separator = arguments_.indexOf('--')
  if (separator < 0) throw new Error('missing -- command separator')

  const optionArguments = arguments_.slice(0, separator)
  const [command, ...commandArguments] = arguments_.slice(separator + 1)
  if (!command) throw new Error('missing command after -- separator')

  const values = {
    attempts: 2,
    graceSeconds: 5,
    timeoutSeconds: 300,
  }
  for (let index = 0; index < optionArguments.length; index += 2) {
    const option = optionArguments[index]
    const rawValue = optionArguments[index + 1]
    if (rawValue === undefined) throw new Error(`missing value for ${option}`)
    const value = positiveInteger(option, rawValue)
    switch (option) {
      case '--attempts':
        values.attempts = value
        break
      case '--grace-seconds':
        values.graceSeconds = value
        break
      case '--timeout-seconds':
        values.timeoutSeconds = value
        break
      default:
        throw new Error(`unknown option ${option}`)
    }
  }

  return {
    attempts: values.attempts,
    command,
    commandArguments,
    graceMilliseconds: values.graceSeconds * 1_000,
    timeoutMilliseconds: values.timeoutSeconds * 1_000,
  }
}

export async function runWithWatchdog(options, dependencies = {}) {
  const spawnProcess = dependencies.spawnProcess ?? spawn
  const diagnostic = dependencies.diagnostic ?? ((message) => process.stderr.write(`${message}\n`))

  for (let attempt = 1; attempt <= options.attempts; attempt += 1) {
    const result = await runAttempt(options, attempt, spawnProcess, diagnostic)
    if (!result.timedOut || attempt === options.attempts) return result
    diagnostic(`[ci-watchdog] retrying after timed-out attempt ${attempt}/${options.attempts}`)
  }

  throw new Error('unreachable watchdog attempt state')
}

async function runAttempt(options, attempt, spawnProcess, diagnostic) {
  diagnostic(`[ci-watchdog] attempt ${attempt}/${options.attempts}: ${formatCommand(options.command, options.commandArguments)}`)
  const useProcessGroup = process.platform !== 'win32'
  const child = spawnProcess(options.command, options.commandArguments, {
    detached: useProcessGroup,
    stdio: 'inherit',
  })
  const processGroupID = child.pid

  return await new Promise((resolve) => {
    let forceKillTimer
    let closeResult
    let cleanupComplete = false
    let settled = false
    let timedOut = false

    const settle = (result) => {
      if (settled) return
      settled = true
      clearTimeout(timeoutTimer)
      clearTimeout(forceKillTimer)
      resolve({
        attempt,
        code: timedOut ? timeoutExitCode : (result.code ?? 1),
        error: result.error,
        signal: result.signal,
        timedOut,
      })
    }

    const timeoutTimer = setTimeout(() => {
      timedOut = true
      diagnostic(
        `[ci-watchdog] attempt ${attempt}/${options.attempts} exceeded ${formatDuration(options.timeoutMilliseconds)}; sending SIGQUIT for diagnostics`,
      )
      snapshotProcessGroup(processGroupID, diagnostic)
      signalProcess(child, processGroupID, 'SIGQUIT', useProcessGroup)
      forceKillTimer = setTimeout(() => {
        diagnostic(`[ci-watchdog] diagnostic grace period ended after ${formatDuration(options.graceMilliseconds)}; sending SIGKILL to the process group`)
        signalProcess(child, processGroupID, 'SIGKILL', useProcessGroup)
        cleanupComplete = true
        if (closeResult) settle(closeResult)
      }, options.graceMilliseconds)
    }, options.timeoutMilliseconds)

    const finish = (code, signal, error) => {
      closeResult = { code, error, signal }
      if (!timedOut || cleanupComplete) settle(closeResult)
    }

    child.once('error', (error) => finish(1, null, error))
    child.once('close', (code, signal) => finish(code, signal))
  })
}

function signalProcess(child, processGroupID, signal, useProcessGroup) {
  try {
    if (useProcessGroup) process.kill(-processGroupID, signal)
    else child.kill(signal)
  } catch (error) {
    if (error?.code !== 'ESRCH') throw error
  }
}

function snapshotProcessGroup(processGroupID, diagnostic) {
  if (process.platform === 'win32') return

  const result = spawnSync('ps', ['-axo', 'pid=,ppid=,pgid=,stat=,etime=,pcpu=,pmem=,comm='], {
    encoding: 'utf8',
    timeout: 2_000,
  })
  if (result.error) {
    diagnostic(`[ci-watchdog] could not capture process-group diagnostics: ${result.error.message}`)
    return
  }

  const processes = result.stdout
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => Number(line.split(/\s+/, 4)[2]) === processGroupID)
  diagnostic(`[ci-watchdog] process group ${processGroupID} before termination:`)
  for (const processLine of processes) diagnostic(`[ci-watchdog]   ${processLine}`)
}

function positiveInteger(option, rawValue) {
  const value = Number(rawValue)
  if (!Number.isSafeInteger(value) || value <= 0) {
    throw new Error(`${option} must be a positive integer, got ${JSON.stringify(rawValue)}`)
  }
  return value
}

function formatCommand(command, arguments_) {
  return [command, ...arguments_].map((value) => JSON.stringify(value)).join(' ')
}

function formatDuration(milliseconds) {
  if (milliseconds % 1_000 === 0) return `${milliseconds / 1_000}s`
  return `${milliseconds}ms`
}

async function main() {
  let options
  try {
    options = parseArguments(process.argv.slice(2))
  } catch (error) {
    process.stderr.write(`ci-watchdog: ${error.message}\n`)
    process.stderr.write(
      'usage: node scripts/ci_watchdog.mjs [--timeout-seconds N] [--attempts N] [--grace-seconds N] -- command [args...]\n',
    )
    process.exitCode = 2
    return
  }

  const result = await runWithWatchdog(options)
  if (result.error) process.stderr.write(`[ci-watchdog] failed to start command: ${result.error.message}\n`)
  process.exitCode = result.code
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main()
}
