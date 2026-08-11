import type { VisualizationEnvelope, VisualizationSpec } from '../../../generated/visualization'
import { currentVisualizationSchemaVersion } from '../../../generated/visualization/schema-version'

export { currentVisualizationSchemaVersion }

export enum Change {
  None = 0,
  Spec = 1 << 0,
  Data = 1 << 1,
  Selection = 1 << 2,
  Status = 1 << 3,
  Context = 1 << 4,
  All = Spec | Data | Selection | Status | Context,
}

export type RendererLocale = 'en-US' | 'pt-BR'
export type RendererTheme = 'light' | 'dark'
export type RendererContext = Readonly<{
  locale: RendererLocale
  theme: RendererTheme
  reducedMotion: boolean
  devicePixelRatio: number
  fontFamily: string
  colors: Readonly<{
    foreground: string
    muted: string
    grid: string
    surface: string
    accent: string
    success: string
    attention: string
    danger: string
    data: readonly string[]
  }>
}>

export const defaultRendererContext: RendererContext = Object.freeze({
  locale: 'en-US', theme: 'light', reducedMotion: true, devicePixelRatio: 1, fontFamily: 'system-ui',
  colors: Object.freeze({
    foreground: '#24292f', muted: '#57606a', grid: '#d8dee4', surface: '#ffffff', accent: '#0969da',
    success: '#1a7f37', attention: '#9a6700', danger: '#cf222e',
    data: Object.freeze(['#006edb', '#eb670f', '#30a147', '#ce2c85', '#856d4c', '#a830e8', '#179b9b', '#b88700', '#df0c24', '#808fa3', '#64762d', '#167e53', '#9d615c', '#866e04', '#894ceb', '#d43511', '#527a29']),
  }),
})

export function normalizeRendererLocale(value: string): RendererLocale {
  const locale = value.trim().replaceAll('_', '-').toLowerCase()
  if (locale === 'en' || locale === 'en-us') return 'en-US'
  if (locale === 'pt' || locale === 'pt-br') return 'pt-BR'
  throw new Error(`unsupported visualization locale ${JSON.stringify(value)}`)
}

export type RendererCapabilities = Readonly<{
  snapshot: boolean
  windowed: boolean
  interactive: boolean
}>

export interface RendererHandle {
  whenReady?(): Promise<void>
  update(envelope: VisualizationEnvelope, change: Change, context: RendererContext): void | Promise<void>
  resize(width: number, height: number, devicePixelRatio: number): void
  snapshot(): Promise<Blob>
  captureViewState?(): unknown
  restoreViewState?(state: unknown): void | Promise<void>
  dispose(): void
}

export interface RendererAdapter {
  mount(container: HTMLElement, envelope: VisualizationEnvelope, context: RendererContext): RendererHandle | Promise<RendererHandle>
}

export type RendererRegistration = Readonly<{
  id: string
  version: string
  schemaVersion: VisualizationEnvelope['schemaVersion']
  kinds: readonly VisualizationSpec['kind'][]
  capabilities: RendererCapabilities
  load(): Promise<RendererAdapter>
}>

type LoadedRegistration = RendererRegistration & { adapter?: Promise<RendererAdapter> }

export class RendererRegistry {
  readonly #registrations = new Map<string, LoadedRegistration>()

  register(registration: RendererRegistration): void {
    if (!registration.id || !registration.version || registration.schemaVersion !== currentVisualizationSchemaVersion || registration.kinds.length === 0) {
      throw new Error(`renderer registration requires identity, version, schema version ${currentVisualizationSchemaVersion}, and kinds`)
    }
    if (this.#registrations.has(registration.id)) throw new Error(`renderer ${JSON.stringify(registration.id)} is already registered`)
    this.#registrations.set(registration.id, { ...registration })
  }

  resolve(envelope: VisualizationEnvelope): LoadedRegistration {
    const registration = this.#registrations.get(envelope.rendererID)
    if (!registration) throw new Error(`unknown visualization renderer ${JSON.stringify(envelope.rendererID)}`)
    if (registration.schemaVersion !== envelope.schemaVersion) {
      throw new Error(`renderer ${JSON.stringify(envelope.rendererID)} does not support schema version ${envelope.schemaVersion}`)
    }
    if (!registration.kinds.includes(envelope.spec.kind)) {
      throw new Error(`renderer ${JSON.stringify(envelope.rendererID)} does not support kind ${JSON.stringify(envelope.spec.kind)}`)
    }
    if ((envelope.dataState.kind === 'windowed' || envelope.dataState.kind === 'spatial_windowed') && !registration.capabilities.windowed) {
      throw new Error(`renderer ${JSON.stringify(envelope.rendererID)} does not support windowed data`)
    }
    return registration
  }

  load(registration: LoadedRegistration): Promise<RendererAdapter> {
    registration.adapter ??= registration.load()
    return registration.adapter
  }
}

export type EnvelopeValidator = (value: unknown) => value is VisualizationEnvelope
export type VisualizationObservation = Readonly<{
  stage: 'validation_failure' | 'stale_result_drop' | 'renderer_load' | 'mount' | 'update' | 'resize' | 'dispose' | 'adapter_error' | 'adapter_observation'
  durationMs: number
  rendererID?: string
  kind?: VisualizationSpec['kind']
  visualID?: string
  adapterStage?: string
  assetID?: string
  layerID?: string
  featureCount?: number
}>
export type VisualizationObserver = (observation: VisualizationObservation) => void

export class VisualizationController {
  readonly #registry: RendererRegistry
  readonly #container: HTMLElement
  readonly #validate: EnvelopeValidator
  readonly #observe?: VisualizationObserver
  #envelope?: VisualizationEnvelope
  #handle?: RendererHandle
  #context?: RendererContext
  #loadGeneration = 0
  #disposed = false
  #pendingResize?: readonly [number, number, number]
  #pendingViewState?: { value: unknown }
  #resizeFrame?: number
  #applyQueue: Promise<void> = Promise.resolve()

  constructor(registry: RendererRegistry, container: HTMLElement, validate: EnvelopeValidator = validateEnvelopeBoundary, observe?: VisualizationObserver) {
    this.#registry = registry
    this.#container = container
    this.#validate = validate
    this.#observe = observe
  }

  get envelope(): VisualizationEnvelope | undefined { return this.#envelope }

  apply(next: VisualizationEnvelope, context: RendererContext = defaultRendererContext): Promise<boolean> {
    if (this.#disposed) return Promise.reject(new Error('visualization controller is disposed'))
    const pending = this.#applyQueue.then(() => this.#applyEnvelope(next, context))
    this.#applyQueue = pending.then(() => undefined, () => undefined)
    return pending
  }

  async #applyEnvelope(next: VisualizationEnvelope, context: RendererContext): Promise<boolean> {
    if (this.#disposed) return false
    if (!this.#validate(next)) {
      this.#record('validation_failure', 0, next)
      throw new Error('invalid visualization envelope')
    }
    const registration = this.#registry.resolve(next)
    const previous = this.#envelope
    if (previous && previous.specRevision === next.specRevision && next.dataRevision < previous.dataRevision) {
      this.#record('stale_result_drop', 0, next)
      return false
    }
    const change = changes(previous, next) | (sameJSON(this.#context, context) ? Change.None : Change.Context)
    if (change === Change.None) return false

    if (!this.#handle || previous?.rendererID !== next.rendererID) {
      this.#handle?.dispose()
      this.#handle = undefined
      const generation = ++this.#loadGeneration
      const loadStarted = now()
      const adapter = await this.#registry.load(registration)
      this.#record('renderer_load', now() - loadStarted, next)
      if (this.#disposed || generation !== this.#loadGeneration) return false
      const mountStarted = now()
      let handle: RendererHandle
      try {
        handle = await adapter.mount(this.#container, next, context)
      } catch (error) {
        this.#record('adapter_error', now() - mountStarted, next)
        throw error
      }
      if (this.#disposed || generation !== this.#loadGeneration) {
        handle.dispose()
        return false
      }
      this.#handle = handle
      this.#flushResize()
      try {
        await handle.whenReady?.()
      } catch (error) {
        if (this.#handle === handle) {
          handle.dispose()
          this.#handle = undefined
        }
        if (this.#disposed || generation !== this.#loadGeneration) return false
        this.#record('adapter_error', now() - mountStarted, next)
        throw error
      }
      this.#record('mount', now() - mountStarted, next)
      if (this.#disposed || generation !== this.#loadGeneration || this.#handle !== handle) {
        if (this.#handle === handle) {
          handle.dispose()
          this.#handle = undefined
        }
        return false
      }
      if (this.#pendingViewState) {
        const pending = this.#pendingViewState
        await handle.restoreViewState?.(pending.value)
        if (this.#pendingViewState === pending) this.#pendingViewState = undefined
      }
      this.#envelope = next
      this.#context = context
      return true
    }

    const updateStarted = now()
    try {
      await this.#handle.update(next, change, context)
    } catch (error) {
      this.#record('adapter_error', now() - updateStarted, next)
      this.#handle.dispose()
      this.#handle = undefined
      throw error
    }
    this.#envelope = next
    this.#context = context
    this.#record('update', now() - updateStarted, next)
    return true
  }

  resize(width: number, height: number, devicePixelRatio = 1): void {
    if (width < 0 || height < 0 || !Number.isFinite(devicePixelRatio) || devicePixelRatio <= 0) return
    this.#pendingResize = [width, height, devicePixelRatio]
    if (this.#resizeFrame !== undefined) return
    if (typeof requestAnimationFrame === 'function') {
      this.#resizeFrame = requestAnimationFrame(() => { this.#resizeFrame = undefined; this.#flushResize() })
    } else {
      queueMicrotask(() => { this.#resizeFrame = undefined; this.#flushResize() })
      this.#resizeFrame = -1
    }
  }

  snapshot(): Promise<Blob> {
    if (!this.#handle) return Promise.reject(new Error('visualization renderer is not mounted'))
    return this.#handle.snapshot()
  }

  captureViewState(): unknown {
    return this.#handle?.captureViewState?.()
  }

  restoreViewState(state: unknown): void | Promise<void> {
    if (this.#handle?.restoreViewState) return this.#handle.restoreViewState(state)
    this.#pendingViewState = { value: state }
  }

  dispose(): void {
    if (this.#disposed) return
    this.#disposed = true
    this.#loadGeneration++
    if (this.#resizeFrame !== undefined && this.#resizeFrame >= 0 && typeof cancelAnimationFrame === 'function') cancelAnimationFrame(this.#resizeFrame)
    this.#resizeFrame = undefined
    this.#pendingResize = undefined
    this.#pendingViewState = undefined
    const disposeStarted = now()
    this.#handle?.dispose()
    if (this.#handle) this.#record('dispose', now() - disposeStarted, this.#envelope)
    this.#handle = undefined
    this.#envelope = undefined
    this.#context = undefined
  }

  #flushResize(): void {
    if (!this.#handle || !this.#pendingResize) return
    const [width, height, devicePixelRatio] = this.#pendingResize
    this.#pendingResize = undefined
    const started = now()
    this.#handle.resize(width, height, devicePixelRatio)
    this.#record('resize', now() - started, this.#envelope)
  }

  #record(stage: VisualizationObservation['stage'], durationMs: number, envelope?: VisualizationEnvelope): void {
    this.#observe?.({ stage, durationMs, rendererID: envelope?.rendererID, kind: envelope?.spec.kind, visualID: envelope?.visualID })
  }
}

function now(): number { return typeof performance === 'undefined' ? Date.now() : performance.now() }

function changes(previous: VisualizationEnvelope | undefined, next: VisualizationEnvelope): Change {
  if (!previous) return Change.All
  let result = Change.None
  if (previous.rendererID !== next.rendererID || previous.specRevision !== next.specRevision) result |= Change.Spec
  if (previous.specRevision !== next.specRevision || previous.dataRevision !== next.dataRevision || !sameJSON(previous.dataState, next.dataState)) result |= Change.Data
  if (!sameJSON(previous.selection, next.selection) || !sameJSON(previous.spatialSelection, next.spatialSelection)) result |= Change.Selection
  if (!sameJSON(previous.status, next.status) || !sameJSON(previous.diagnostics, next.diagnostics)) result |= Change.Status
  return result
}

function sameJSON(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true
  return JSON.stringify(left) === JSON.stringify(right)
}

// Generated JSON Schema validation is injected by the element in production.
// This structural guard protects direct controller users and enforces the
// revision invariants before any lazy renderer code runs.
export function validateEnvelopeBoundary(value: unknown): value is VisualizationEnvelope {
  if (!value || typeof value !== 'object') return false
  const envelope = value as Partial<VisualizationEnvelope>
  if (envelope.schemaVersion !== currentVisualizationSchemaVersion || typeof envelope.visualID !== 'string' || envelope.visualID.length === 0) return false
  if (typeof envelope.rendererID !== 'string' || envelope.rendererID.length === 0 || typeof envelope.specRevision !== 'string') return false
  if (!envelope.spec || !envelope.dataState || typeof envelope.dataRevision !== 'number' || envelope.dataRevision < 0) return false
  if (envelope.dataState.specRevision !== envelope.specRevision || envelope.dataState.dataRevision !== envelope.dataRevision) return false
  return Array.isArray(envelope.selection) && !!envelope.status && Array.isArray(envelope.diagnostics)
}
