import type { DashboardVisualizationSignal } from '../../generated/signals'
import type { VisualizationEnvelope, WindowedVisualizationDataState } from '../../generated/visualization'
import { DashboardVisualizationSignalDecoder } from './visualization/signal-envelope'

type RetainedWindow = { base: VisualizationEnvelope; window: VisualizationEnvelope; accepted: VisualizationEnvelope }

/** Keep the current preview intact when an older window response arrives late. */
export class BuilderVisualizationState {
  private readonly decoder = new DashboardVisualizationSignalDecoder()
  private readonly windowDecoder = new DashboardVisualizationSignalDecoder()
  private readonly retained = new Map<string, RetainedWindow>()
  private context = ''

  decode(signals: Record<string, DashboardVisualizationSignal>, servingStateID: string, pageID: string, filterRevision: number): Record<string, VisualizationEnvelope> {
    const context = JSON.stringify([servingStateID, pageID, filterRevision])
    if (this.context !== context) {
      this.retained.clear()
      this.context = context
    }
    const matches = (signal: DashboardVisualizationSignal): boolean => Boolean(
      servingStateID && signal.servingStateID === servingStateID
      && signal.consumerIdentity === `${pageID}/${signal.visualID}`
      && signal.filterRevision === filterRevision)
    const baseSignals = Object.fromEntries(Object.entries(signals).filter(([id, signal]) => id === signal.visualID && matches(signal)))
    const current = this.decoder.decodeAll(baseSignals, servingStateID)
    const windowSignals: Record<string, DashboardVisualizationSignal> = {}
    for (const [id, base] of Object.entries(current)) {
      const window = signals[`window:${servingStateID}:${pageID}:${filterRevision}:${id}`]
      if (window && window.visualID === id && matches(window) && window.specRevision === base.specRevision) windowSignals[id] = window
    }
    const windows = this.windowDecoder.decodeAll(windowSignals, servingStateID)
    for (const [id, window] of Object.entries(windows)) {
      const base = current[id]!
      const retained = this.retained.get(id)
      // Selection/status metadata can change without replacing the base rows.
      const sameBase = retained?.base.specRevision === base.specRevision && retained.base.dataState === base.dataState
      const previous = sameBase ? retained.accepted : base
      const accepted = sameBase && retained.window === window
        ? retained.accepted : acceptWindow(previous, window)
      this.retained.set(id, { base, window, accepted })
      current[id] = accepted
    }
    for (const id of this.retained.keys()) {
      if (!windows[id]) this.retained.delete(id)
    }
    return current
  }
}

function acceptWindow(previous: VisualizationEnvelope, next: VisualizationEnvelope): VisualizationEnvelope {
  if (next.dataRevision < previous.dataRevision) return previous
  const before = previous.dataState
  const after = next.dataState
  if (before.kind !== 'windowed' || after.kind !== 'windowed' || next.dataRevision !== previous.dataRevision) return next
  if (after.resetVersion < before.resetVersion) return previous
  if (after.resetVersion > before.resetVersion) return next
  // Rapid sorts may share a reset number until the first response arrives.
  // Order those sorts by request, but keep independent scrolling blocks.
  if (JSON.stringify(after.sort) !== JSON.stringify(before.sort)) {
    return lastRequest(after) > lastRequest(before) ? next : previous
  }
  const blocks = { ...before.blocks }
  for (const [id, block] of Object.entries(after.blocks)) {
    if (!blocks[id] || block.requestSeq >= blocks[id].requestSeq) blocks[id] = block
  }
  return { ...next, dataState: { ...after, blocks } }
}

function lastRequest(state: WindowedVisualizationDataState): number {
  return Math.max(0, ...Object.values(state.blocks).map(block => block.requestSeq))
}
