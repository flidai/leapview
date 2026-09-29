import type { ReactiveController, ReactiveControllerHost } from 'lit'

export function hasLiveRunDuration(status: string, startedAt: string | undefined, finishedAt?: string): boolean {
  const end = finishedAt?.trim()
  return (status === 'running' || status === 'prepared')
    && (!end || end === '—' || end === '-')
    && Boolean(startedAt && Number.isFinite(Date.parse(startedAt)))
}

export function displayRunDuration(
  status: string,
  startedAt: string | undefined,
  finishedAt: string | undefined,
  recordedDuration: string | undefined,
  nowMs: number,
): string {
  if (!hasLiveRunDuration(status, startedAt, finishedAt)) return recordedDuration || '—'
  const elapsedSeconds = Math.max(0, Math.round((nowMs - Date.parse(startedAt!)) / 1000))
  const seconds = elapsedSeconds % 60
  const minutes = Math.floor(elapsedSeconds / 60) % 60
  const hours = Math.floor(elapsedSeconds / 3600)
  if (hours) return `${hours}h${minutes}m${seconds}s`
  if (elapsedSeconds >= 60) return `${minutes}m${seconds}s`
  return `${seconds}s`
}

/** Keeps only visible, active-run views ticking; elapsed time is always derived from timestamps. */
export class LiveRunDurationClock implements ReactiveController {
  nowMs = Date.now()
  private timer?: ReturnType<typeof setInterval>

  constructor(
    private readonly host: ReactiveControllerHost & HTMLElement,
    private readonly hasActiveRun: () => boolean,
  ) {
    host.addController(this)
  }

  hostConnected(): void {
    this.nowMs = Date.now()
    this.host.requestUpdate()
  }

  hostUpdated(): void {
    if (!this.hasActiveRun()) {
      this.stop()
      return
    }
    if (this.timer) return
    this.nowMs = Date.now()
    this.timer = setInterval(() => {
      this.nowMs = Date.now()
      this.host.requestUpdate()
    }, 1000)
    this.host.requestUpdate()
  }

  hostDisconnected(): void {
    this.stop()
  }

  private stop(): void {
    if (!this.timer) return
    clearInterval(this.timer)
    this.timer = undefined
  }
}
