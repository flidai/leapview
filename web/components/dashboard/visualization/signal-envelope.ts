import type {
  VisualizationDataState,
  VisualizationDataStateTransport,
  VisualizationEnvelope,
} from '../../../generated/visualization'
import type { DashboardVisualizationSignal } from '../../../generated/signals'

type CachedDataState = {
  payload: string
  value: VisualizationDataState
}

type CachedEnvelope = {
  fingerprint: string
  payload: string
  value: VisualizationEnvelope
}

/**
 * Reconstructs canonical visualization envelopes while keeping large data
 * frames opaque to Datastar's recursively reactive signal graph.
 */
export class DashboardVisualizationSignalDecoder {
  private readonly dataStates = new Map<string, CachedDataState>()
  private readonly envelopes = new Map<string, CachedEnvelope>()

  decodeAll(signals: Record<string, DashboardVisualizationSignal>): Record<string, VisualizationEnvelope> {
    const envelopes: Record<string, VisualizationEnvelope> = {}
    const active = new Set(Object.keys(signals))
    for (const [id, signal] of Object.entries(signals)) {
      const envelope = this.decode(signal)
      if (envelope) envelopes[id] = envelope
    }
    for (const id of this.dataStates.keys()) {
      if (!active.has(id)) this.dataStates.delete(id)
    }
    for (const id of this.envelopes.keys()) {
      if (!active.has(id)) this.envelopes.delete(id)
    }
    return envelopes
  }

  decode(signal: DashboardVisualizationSignal): VisualizationEnvelope | undefined {
    if (!validTransportHeader(signal.dataState, signal)) return undefined
    let dataState = this.dataStates.get(signal.visualID)
    if (!dataState || dataState.payload !== signal.dataState.payload) {
      const decoded = decodeDataState(signal.dataState)
      if (!decoded) return undefined
      dataState = { payload: signal.dataState.payload, value: decoded }
      this.dataStates.set(signal.visualID, dataState)
    }
    const {
      dataState: _,
      servingStateID: _servingStateID,
      streamGeneration: _streamGeneration,
      filterRevision: _filterRevision,
      interactionRevision: _interactionRevision,
      consumerIdentity: _consumerIdentity,
      ...envelope
    } = signal
    const fingerprint = JSON.stringify(envelope)
    const cached = this.envelopes.get(signal.visualID)
    if (cached?.payload === signal.dataState.payload && cached.fingerprint === fingerprint) {
      return cached.value
    }
    const value = { ...envelope, dataState: dataState.value } as VisualizationEnvelope
    this.envelopes.set(signal.visualID, { fingerprint, payload: signal.dataState.payload, value })
    return value
  }
}

function validTransportHeader(transport: VisualizationDataStateTransport | undefined, signal: DashboardVisualizationSignal): boolean {
  return transport !== undefined
    && transport.schemaVersion === 1
    && transport.encoding === 'json'
		&& (transport.kind === 'inline' || transport.kind === 'windowed' || transport.kind === 'spatial_tiled')
    && transport.specRevision === signal.specRevision
    && transport.dataRevision === signal.dataRevision
    && Number.isSafeInteger(transport.dataRevision) && transport.dataRevision >= 0
    && Number.isSafeInteger(transport.generation) && transport.generation >= 0
}

function decodeDataState(transport: VisualizationDataStateTransport): VisualizationDataState | undefined {
  try {
    const value = JSON.parse(transport.payload)
    if (!value || typeof value !== 'object' || Array.isArray(value)) return undefined
    if (value.kind !== transport.kind
      || value.specRevision !== transport.specRevision
      || value.dataRevision !== transport.dataRevision
      || value.generation !== transport.generation) return undefined
    return value as VisualizationDataState
  } catch {
    return undefined
  }
}
