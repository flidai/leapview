import { expect, test } from 'bun:test'
import { JSDOM } from 'jsdom'
import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { MapLibreHandle } from './maplibre'
import { Change, defaultRendererContext } from '../host-controller'

type Listener = (...args: any[]) => void
type FakeLayer = { id: string; source?: string; type?: string; metadata?: Record<string, unknown>; paint: Record<string, unknown>; layout?: Record<string, unknown>; filter?: unknown }

class FakeMap {
  readonly layers = new Map<string, FakeLayer>()
  readonly sources = new Map<string, { setData: (value: unknown) => void }>()
  readonly listeners = new Map<string, Set<Listener>>()
  readonly paintCalls: Array<{ id: string; property: string; value: unknown }> = []
  readonly sourceDataCalls: unknown[] = []
  readonly canvas: HTMLCanvasElement
  private center: [number, number] = [0, 0]
  private zoom = 0

  constructor() {
    this.canvas = document.createElement('canvas')
    this.layers.set('__lv-background', { id: '__lv-background', type: 'background', metadata: { 'leapview:role': 'background' }, paint: {} })
  }

  once(event: string, listener: Listener): this {
    if (event === 'load') queueMicrotask(() => listener())
    else this.on(event, listener)
    return this
  }

  on(event: string, listener: Listener): this {
    const listeners = this.listeners.get(event) ?? new Set<Listener>()
    listeners.add(listener)
    this.listeners.set(event, listeners)
    return this
  }

  off(event: string, listener: Listener): this { this.listeners.get(event)?.delete(listener); return this }

  private emit(event: string, ...args: any[]): void { for (const listener of [...(this.listeners.get(event) ?? [])]) listener(...args) }

  triggerRepaint(): void { this.emit('render') }
  getCanvas(): HTMLCanvasElement { return this.canvas }
  getStyle(): { layers: FakeLayer[] } { return { layers: [...this.layers.values()] } }
  getLayer(id: string): FakeLayer | undefined { return this.layers.get(id) }
  setPaintProperty(id: string, property: string, value: unknown): void {
    const layer = this.layers.get(id)
    if (!layer) return
    layer.paint[property] = value
    this.paintCalls.push({ id, property, value })
  }
  setLayoutProperty(id: string, property: string, value: unknown): void { const layer = this.layers.get(id); if (layer) (layer.layout ??= {})[property] = value }
  addSource(id: string, source: { type: string; data?: unknown }): void {
    this.sources.set(id, { setData: (value) => { source.data = value; this.sourceDataCalls.push(value) } })
  }
  getSource(id: string): { setData: (value: unknown) => void } | undefined { return this.sources.get(id) }
  removeSource(id: string): void { this.sources.delete(id) }
  addLayer(layer: FakeLayer): void { this.layers.set(layer.id, { ...layer, paint: { ...(layer.paint ?? {}) } }) }
  removeLayer(id: string): void { this.layers.delete(id) }
  setMinZoom(_value: number): void {}
  setMaxZoom(_value: number): void {}
  resize(): void {}
  fitBounds(bounds: [[number, number], [number, number]]): void { this.center = [(bounds[0][0] + bounds[1][0]) / 2, (bounds[0][1] + bounds[1][1]) / 2] }
  jumpTo(options: { center?: [number, number]; zoom?: number }): void { if (options.center) this.center = options.center; if (options.zoom !== undefined) this.zoom = options.zoom }
  getCenter(): { lng: number; lat: number } { return { lng: this.center[0], lat: this.center[1] } }
  getZoom(): number { return this.zoom }
  getBearing(): number { return 0 }
  getPitch(): number { return 0 }
  stop(): void {}
  easeTo(options: { center?: [number, number]; zoom?: number }): void { this.jumpTo(options) }
  addControl(_control: unknown, _position?: string): void {}
  removeControl(_control: unknown): void {}
  queryRenderedFeatures(): unknown[] { return [] }
  getFilter(_id: string): unknown { return undefined }
  setFilter(_id: string, _filter: unknown): void {}
  setLayerZoomRange(_id: string, _minimum: number, _maximum: number): void {}
  remove(): void { this.listeners.clear() }
}

function context(theme: 'light' | 'dark') {
  return { ...defaultRendererContext, theme }
}

async function digest(value: Uint8Array): Promise<string> {
  const bytes = new Uint8Array(await crypto.subtle.digest('SHA-256', value))
  return `sha256:${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
}

async function labelEnvelope(theme: 'auto' | 'light' | 'dark' = 'auto', specRevision = 'sha256:labels'): Promise<VisualizationEnvelope> {
  const geometryJSON = JSON.stringify({ type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [[[-47, -24], [-46, -24], [-46, -23], [-47, -23], [-47, -24]]] }, properties: { id: 'SP' } }] })
  const geometryBytes = new TextEncoder().encode(geometryJSON)
  const geometryDigest = await digest(geometryBytes)
  const geometry = { id: 'states', url: '/states.geojson', digest: geometryDigest, attribution: '' }
  const point = {
    id: 'points', kind: 'point', latitude: { dataset: 'primary', field: 'latitude' }, longitude: { dataset: 'primary', field: 'longitude' }, label: { dataset: 'primary', field: 'name' }, tooltip: [],
    position: 'above_labels', visibility: { minimumZoom: 0, maximumZoom: 24 }, color: { kind: 'sequential', palette: 'blue', reverse: false, nullColor: '#d0d7de' },
    size: { minimumRadius: 4, maximumRadius: 18 }, stroke: { color: '#fff', width: 1, opacity: 1 }, cluster: { enabled: false, radius: 40, maximumZoom: 14, minimumPoints: 2, showCount: true }, opacity: .8,
  }
  const choropleth = {
    id: 'states', kind: 'choropleth', geometry, join: { dataset: 'primary', field: 'state' }, value: { dataset: 'primary', field: 'value' }, label: { dataset: 'primary', field: 'name' }, tooltip: [],
    position: 'below_labels', visibility: { minimumZoom: 0, maximumZoom: 24 }, color: { kind: 'sequential', palette: 'blue', reverse: false, nullColor: '#d0d7de' }, stroke: { color: '#fff', width: 1, opacity: 1 }, opacity: .82,
  }
  return {
    schemaVersion: 10, visualID: 'label-theme-map', rendererID: 'maplibre', specRevision, dataRevision: 1,
    spec: {
      kind: 'geographic', title: 'Labels', datasets: [{ id: 'primary', fields: [
        { id: 'latitude', role: 'dimension', dataType: 'decimal', nullable: false, label: 'Latitude' }, { id: 'longitude', role: 'dimension', dataType: 'decimal', nullable: false, label: 'Longitude' },
        { id: 'state', role: 'identity', dataType: 'string', nullable: false, label: 'State' }, { id: 'value', role: 'metric', dataType: 'decimal', nullable: false, label: 'Value' }, { id: 'name', role: 'dimension', dataType: 'string', nullable: false, label: 'Name' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Labels', description: 'Labels' }, interactions: [], spatialInteractions: [], layers: [point, choropleth],
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, roam: false, theme, labelDensity: 'normal', camera: { mode: 'fixed', center: [0, 0], zoom: 2, padding: 24, minimumZoom: 0, maximumZoom: 10 }, controls: { zoom: false, reset: false, compass: false } },
    },
    dataState: { kind: 'inline', specRevision, dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision, dataRevision: 1, generation: 1, columns: ['latitude', 'longitude', 'state', 'value', 'name'], rows: [[-23.5, -46.6, 'SP', 10, 'São Paulo']], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as unknown as VisualizationEnvelope
}

function layerPaint(map: FakeMap, id: string): { text: unknown; halo: unknown } {
  const layer = map.getLayer(id)
  return { text: layer?.paint['text-color'], halo: layer?.paint['text-halo-color'] }
}

test('MapLibre mounted labels resolve theme and repaint in place on context-only updates', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', { url: 'https://dash.example/' })
  const previous = { document: globalThis.document, window: globalThis.window, location: globalThis.location, getComputedStyle: globalThis.getComputedStyle, requestAnimationFrame: globalThis.requestAnimationFrame, cancelAnimationFrame: globalThis.cancelAnimationFrame, fetch: globalThis.fetch, CustomEvent: globalThis.CustomEvent }
  Object.assign(globalThis, { document: dom.window.document, window: dom.window, location: dom.window.location, getComputedStyle: dom.window.getComputedStyle.bind(dom.window), requestAnimationFrame: (callback: FrameRequestCallback) => { callback(0); return 1 }, cancelAnimationFrame: () => {}, CustomEvent: dom.window.CustomEvent })
  const geometryJSON = JSON.stringify({ type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [[[-47, -24], [-46, -24], [-46, -23], [-47, -23], [-47, -24]]] }, properties: { id: 'SP' } }] })
  globalThis.fetch = async () => new Response(geometryJSON, { status: 200, headers: { 'content-type': 'application/json' } })
  try {
    const envelope = await labelEnvelope()
    const container = dom.window.document.createElement('div')
    const frame = dom.window.document.createElement('div')
    const attribution = dom.window.document.createElement('div')
    const map = new FakeMap()
    const handle = new MapLibreHandle(container, frame, map as never, attribution, context('dark'))
    await handle.update(envelope, Change.All, context('dark'))
    const sourcesBefore = [...map.sources.keys()]
    const layersBefore = [...map.layers.keys()]
    expect(layerPaint(map, 'lv-points-data-label')).toEqual({ text: '#f0f6fc', halo: '#0d1821' })
    expect(layerPaint(map, 'lv-states-data-label')).toEqual({ text: '#f0f6fc', halo: '#0d1821' })

    await handle.update(envelope, Change.Context, context('light'))
    expect(layerPaint(map, 'lv-points-data-label')).toEqual({ text: '#1f2328', halo: '#ffffff' })
    expect(layerPaint(map, 'lv-states-data-label')).toEqual({ text: '#1f2328', halo: '#ffffff' })
    expect([...map.sources.keys()]).toEqual(sourcesBefore)
    expect([...map.layers.keys()]).toEqual(layersBefore)

    await handle.update(envelope, Change.Context, context('dark'))
    map.removeLayer('lv-states-data-label')
    await handle.update(envelope, Change.Context, context('light'))
    expect(map.getLayer('lv-states-data-label')).toBeUndefined()
    expect(layerPaint(map, 'lv-points-data-label')).toEqual({ text: '#1f2328', halo: '#ffffff' })

    const explicit = await labelEnvelope('dark', 'sha256:explicit-label-theme')
    await handle.update(explicit, Change.Spec, context('light'))
    const darkPaint = layerPaint(map, 'lv-points-data-label')
    expect(darkPaint).toEqual({ text: '#f0f6fc', halo: '#0d1821' })
    await handle.update(explicit, Change.Context, context('dark'))
    expect(layerPaint(map, 'lv-points-data-label')).toEqual(darkPaint)
    handle.dispose()
  } finally {
    globalThis.document = previous.document
    globalThis.window = previous.window
    globalThis.location = previous.location
    globalThis.getComputedStyle = previous.getComputedStyle
    globalThis.requestAnimationFrame = previous.requestAnimationFrame
    globalThis.cancelAnimationFrame = previous.cancelAnimationFrame
    globalThis.fetch = previous.fetch
    globalThis.CustomEvent = previous.CustomEvent
    dom.window.close()
  }
})
