import type { DashboardVisualizationSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { DashboardVisualizationSignalDecoder } from './visualization/signal-envelope'

/** Keep the current preview intact when an older window response arrives late. */
export class BuilderVisualizationState {
  private readonly decoder = new DashboardVisualizationSignalDecoder()

  decode(signals: Record<string, DashboardVisualizationSignal>, servingStateID: string, pageID: string, filterRevision: number): Record<string, VisualizationEnvelope> {
    const matches = (signal: DashboardVisualizationSignal): boolean => Boolean(
      servingStateID && signal.servingStateID === servingStateID
      && signal.consumerIdentity === `${pageID}/${signal.visualID}`
      && signal.filterRevision === filterRevision)
    const current = Object.fromEntries(Object.entries(signals).filter(([id, signal]) => id === signal.visualID && matches(signal)))
    // Windows use separate slots so stale network patches cannot overwrite
    // the base preview, including when Lit coalesces both patches in one frame.
    for (const signal of Object.values(current)) {
      const key = `window:${servingStateID}:${pageID}:${filterRevision}:${signal.visualID}`
      const window = signals[key]
      if (window && window.visualID === signal.visualID && matches(window)) current[signal.visualID] = window
    }
    return this.decoder.decodeAll(current)
  }
}
