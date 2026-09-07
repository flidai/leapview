import type { DataExploreCommand, DataExploreResultSignal, DataExploreStatusSignal } from '../../generated/signals'
import { explorationSpecFor } from './data-explorer-spec'

const suggestionSequenceByClientID = new Map<string, number>()
let fallbackDataExplorerClientID = ''
let suggestionSequenceClock = 0
let suggestionSequenceClockCounter = 0

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
  ): DataExploreResultSignal {
    const spec = explorationSpecFor(command)
    const modelID = normalizedClientID(spec.modelId)
    const key = modelID
      ? [normalizedClientID(context?.projectId), normalizedClientID(context?.generationId), modelID, normalizedClientID(spec.datasetId)].join('\x00')
      : ''
    if (key !== this.semanticResultContextKey) {
      this.semanticResultContextKey = key
      this.lastGoodSemanticResult = null
    }
    const hasData = Boolean(
      result.columns?.length || result.rows?.length || result.rowsReturned > 0 || result.sql || result.plan,
    )
    const failed = Boolean(result.error || status?.error || status?.state === 'error' || status?.state === 'cancelled')
    if (key && !failed && hasData && result.requestSeq >= (this.lastGoodSemanticResult?.requestSeq ?? 0)) {
      this.lastGoodSemanticResult = snapshotSemanticResult(result)
    }
    const cached = this.lastGoodSemanticResult
    const shouldRetain = failed || !hasData || status?.loading || status?.state === 'loading'
      || status?.state === 'stale' || status?.state === 'cancelled' || result.requestSeq < (cached?.requestSeq ?? 0)
    return cached && shouldRetain ? cached : result
  }
}

function snapshotSemanticResult(result: DataExploreResultSignal): DataExploreResultSignal {
  return {
    ...result,
    columns: [...(result.columns ?? [])],
    rows: (result.rows ?? []).map((row) => ({ ...row })),
    warnings: [...(result.warnings ?? [])],
  }
}
