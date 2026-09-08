import type { DataExploreCommand, DataExploreResultSignal, DataExploreStatusSignal } from '../../generated/signals'
import type { VisualizationEnvelope } from '../../generated/visualization'
import { explorationSpecFor } from './data-explorer-spec'

const suggestionSequenceByClientID = new Map<string, number>()
let fallbackDataExplorerClientID = ''
let suggestionSequenceClock = 0
let suggestionSequenceClockCounter = 0

export type DataExploreExecutionState = 'idle' | 'pending' | 'running' | 'stopped'

function nextSuggestionSequenceClock(): number {
  const now = Date.now()
  if (now === suggestionSequenceClock) suggestionSequenceClockCounter += 1
  else {
    suggestionSequenceClock = now
    suggestionSequenceClockCounter = 0
  }
  // Keep the timestamp-based fallback safely below Number.MAX_SAFE_INTEGER
  // while leaving enough room to order bursts within one millisecond.
  return now * 1000 + suggestionSequenceClockCounter
}

function normalizedClientID(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

/** Owns browser-local identity and monotonic side-channel tokens for one explorer. */
export class DataExplorerClientState {
  private generatedClientID = ''
  private suggestionRequestSeq = 0
  private currentRunID = ''
  private semanticResultContextKey = ''
  private lastGoodSemanticResult: DataExploreResultSignal | null = null
  private lastGoodSemanticViews: Record<string, VisualizationEnvelope> = {}
  private lastGoodRecommendedView = 'table'
  private lastGoodDefaultView = 'table'
  private lastGoodSemanticViewsRequestSeq = 0

  clientID(hydrated?: unknown): string {
    const hydratedID = normalizedClientID(hydrated)
    if (hydratedID) return hydratedID
    if (this.generatedClientID) return this.generatedClientID
    if (fallbackDataExplorerClientID) {
      this.generatedClientID = fallbackDataExplorerClientID
      return this.generatedClientID
    }
    const storageKey = 'leapview-data-explorer-client-id'
    try {
      const stored = normalizedClientID(window.sessionStorage.getItem(storageKey))
      if (stored) {
        this.generatedClientID = stored
        return stored
      }
    } catch {
      // Private browsing and embedded documents may deny session storage.
    }
    const random = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`
    this.generatedClientID = `explorer-${random}`
    fallbackDataExplorerClientID = this.generatedClientID
    try {
      window.sessionStorage.setItem(storageKey, this.generatedClientID)
    } catch {
      // The component-local ID remains stable for this mounted explorer.
    }
    return this.generatedClientID
  }

  invalidateSuggestions(): number {
    this.suggestionRequestSeq += 1
    return this.suggestionRequestSeq
  }

  nextSuggestionSequence(clientID?: unknown): number {
    const id = this.clientID(clientID)
    const storageKey = `leapview-data-explorer-suggestion-seq:${id}`
    const local = this.suggestionRequestSeq
    const remembered = suggestionSequenceByClientID.get(id) ?? 0
    let persisted = 0
    try {
      const value = Number(window.sessionStorage.getItem(storageKey))
      if (Number.isSafeInteger(value) && value >= 0) persisted = value
    } catch {
      // The component-local clock below still orders requests in this page.
    }
    const requestSeq = Math.max(local + 1, remembered + 1, persisted + 1, nextSuggestionSequenceClock())
    this.suggestionRequestSeq = requestSeq
    suggestionSequenceByClientID.set(id, requestSeq)
    try {
      window.sessionStorage.setItem(storageKey, String(requestSeq))
    } catch {
      // A timestamp-based token remains safe when session storage is denied.
    }
    return requestSeq
  }

  isSuggestionCurrent(requestSeq: number): boolean {
    return requestSeq === this.suggestionRequestSeq
  }

  isSuggestionCurrentOrNewer(requestSeq: number): boolean {
    return requestSeq >= this.suggestionRequestSeq
  }

  nextRunID(): string {
    const random = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
      ? crypto.randomUUID()
      : `${Date.now()}-${Math.random().toString(36).slice(2)}`
    this.currentRunID = `explore-${random}`
    return this.currentRunID
  }

  runID(): string {
    return this.currentRunID
  }

  clearRunID(): void {
    this.currentRunID = ''
  }

  /** Keeps one successful result for this mounted explorer while a newer command is in flight. */
  semanticResult(
    command: DataExploreCommand,
    result: DataExploreResultSignal,
    status?: DataExploreStatusSignal,
    context?: { projectId?: unknown; generationId?: unknown },
    executionState: DataExploreExecutionState = 'idle',
  ): DataExploreResultSignal {
    const spec = explorationSpecFor(command)
    const modelID = normalizedClientID(spec.modelId)
    const key = modelID
      ? [normalizedClientID(context?.projectId), normalizedClientID(context?.generationId), modelID, normalizedClientID(spec.datasetId)].join('\x00')
      : ''
    if (key !== this.semanticResultContextKey) {
      this.semanticResultContextKey = key
      this.lastGoodSemanticResult = null
      this.lastGoodSemanticViews = {}
      this.lastGoodRecommendedView = 'table'
      this.lastGoodDefaultView = 'table'
      this.lastGoodSemanticViewsRequestSeq = 0
    }
    const hasData = Boolean(
      result.columns?.length || result.rows?.length || result.rowsReturned > 0 || result.sql || result.plan,
    )
    const cacheable = isCacheableSemanticResponse(command, result, status, executionState)
    const previousRequestSeq = this.lastGoodSemanticResult?.requestSeq ?? -1
    if (key && cacheable && hasData && result.requestSeq >= previousRequestSeq) {
      const newResult = result.requestSeq > previousRequestSeq
      this.lastGoodSemanticResult = snapshotSemanticResult(result)
      // The view cache is valid only for the result it was compiled from. A
      // new accepted result must clear the old envelopes before semanticViews
      // has a chance to install the matching set from this signal.
      if (newResult) {
        this.lastGoodSemanticViews = {}
        this.lastGoodRecommendedView = 'table'
        this.lastGoodDefaultView = 'table'
        this.lastGoodSemanticViewsRequestSeq = 0
      }
    }
    const cached = this.lastGoodSemanticResult
    const shouldRetain = Boolean(cached) && (!cacheable || !hasData || result.requestSeq < (cached?.requestSeq ?? 0))
    return cached && shouldRetain ? cached : result
  }

  /** Retains the IR envelopes that belong to the retained semantic result. */
  semanticViews(
    command: DataExploreCommand,
    result: DataExploreResultSignal,
    views: Record<string, VisualizationEnvelope> | undefined,
    recommendedView: string | undefined,
    defaultView: string | undefined,
    status?: DataExploreStatusSignal,
    context?: { projectId?: unknown; generationId?: unknown },
    executionState: DataExploreExecutionState = 'idle',
  ): { views: Record<string, VisualizationEnvelope>; recommendedView: string; defaultView: string } {
    // Synchronize context and result retention before deciding which envelope
    // set belongs on screen.
    const visibleResult = this.semanticResult(command, result, status, context, executionState)
    const currentViews = views ?? {}
    const cacheable = isCacheableSemanticResponse(command, result, status, executionState)
    const hasCurrentViews = Object.keys(currentViews).length > 0
    const resultRequestSeq = this.lastGoodSemanticResult?.requestSeq
    if (cacheable && resultRequestSeq === result.requestSeq && result.requestSeq >= this.lastGoodSemanticViewsRequestSeq) {
      this.lastGoodSemanticViews = hasCurrentViews ? { ...currentViews } : {}
      this.lastGoodRecommendedView = hasCurrentViews ? recommendedView || defaultView || 'table' : 'table'
      this.lastGoodDefaultView = hasCurrentViews ? defaultView || 'table' : 'table'
      this.lastGoodSemanticViewsRequestSeq = hasCurrentViews ? result.requestSeq : 0
    }
    const retainingResult = visibleResult !== result
    if (retainingResult && Object.keys(this.lastGoodSemanticViews).length > 0) {
      return {
        views: this.lastGoodSemanticViews,
        recommendedView: this.lastGoodRecommendedView,
        defaultView: this.lastGoodDefaultView,
      }
    }
    return { views: currentViews, recommendedView: recommendedView || defaultView || 'table', defaultView: defaultView || 'table' }
  }
}

function isCacheableSemanticResponse(
  command: DataExploreCommand,
  result: DataExploreResultSignal,
  status: DataExploreStatusSignal | undefined,
  executionState: DataExploreExecutionState,
): boolean {
  if (!status || executionState !== 'idle') return false
  const statusState = String(status.state)
  if (statusState !== 'success' || status.loading || status.stale) return false
  if (status.error || result.error) return false
  const commandRequestSeq = Number(command.requestSeq)
  const resultRequestSeq = Number(result.requestSeq)
  const statusRequestSeq = Number(status.requestSeq)
  if (![commandRequestSeq, resultRequestSeq, statusRequestSeq].every((value) => Number.isSafeInteger(value) && value >= 0)) return false
  return commandRequestSeq === resultRequestSeq && resultRequestSeq === statusRequestSeq
}

function snapshotSemanticResult(result: DataExploreResultSignal): DataExploreResultSignal {
  return {
    ...result,
    columns: [...(result.columns ?? [])],
    rows: (result.rows ?? []).map((row) => ({ ...row })),
    warnings: [...(result.warnings ?? [])],
  }
}
