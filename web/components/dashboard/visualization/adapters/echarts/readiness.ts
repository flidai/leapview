import type { ECharts } from 'echarts'

type EChartsFrameChart = Pick<ECharts, 'on' | 'off' | 'getWidth' | 'getHeight'>

export class EChartsReadinessError extends Error {
  constructor(readonly reason: 'timeout' | 'invalid_layout', readonly width: number, readonly height: number) {
    super(reason === 'invalid_layout'
      ? `ECharts cannot render its first frame with invalid layout ${width}x${height}`
      : 'ECharts did not complete its first frame')
    this.name = 'EChartsReadinessError'
  }
}

export function waitForEChartsFrame(chart: EChartsFrameChart, timeoutMs = 5_000, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    let timer: ReturnType<typeof setTimeout> | undefined
    let settled = false
    const cleanup = () => {
      if (timer !== undefined) clearTimeout(timer)
      chart.off('rendered', rendered)
      signal?.removeEventListener('abort', aborted)
    }
    const complete = (action: () => void) => {
      if (settled) return
      settled = true
      cleanup()
      action()
    }
    const rendered = () => {
      const width = chart.getWidth()
      const height = chart.getHeight()
      if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0) return
      complete(resolve)
    }
    const aborted = () => { complete(resolve) }
    chart.on('rendered', rendered)
    if (signal?.aborted) {
      aborted()
      return
    }
    signal?.addEventListener('abort', aborted, { once: true })
    timer = setTimeout(() => {
      const width = chart.getWidth()
      const height = chart.getHeight()
      complete(() => reject(new EChartsReadinessError(width > 0 && height > 0 ? 'timeout' : 'invalid_layout', width, height)))
    }, timeoutMs)
  })
}
