import { expect, test } from 'bun:test'
import { JSDOM } from 'jsdom'
import type { VisualizationEnvelope, VisualizationMapStyleAsset } from '../../../../generated/visualization'
import { MapLibreHandle } from './maplibre'
import { Change, defaultRendererContext } from '../host-controller'

type Listener = (...args: any[]) => void
type FakeLayer = { id: string; source?: string; type?: string; metadata?: Record<string, unknown>; paint: Record<string, unknown>; layout?: Record<string, unknown>; filter?: unknown }
type FakeSource = { setData: (value: unknown) => void; getClusterExpansionZoom?: (clusterID: number) => Promise<number> }
type FakeHandler = { enabled: boolean; enableCalls: number; disableCalls: number; enable: () => void; disable: () => void; isEnabled: () => boolean }

function fakeHandler(enabled = false): FakeHandler {
  const handler = { enabled, enableCalls: 0, disableCalls: 0 } as FakeHandler
  handler.enable = () => { handler.enabled = true; handler.enableCalls += 1 }
  handler.disable = () => { handler.enabled = false; handler.disableCalls += 1 }
  handler.isEnabled = () => handler.enabled
  return handler
}

class FakeMap {
  readonly layers = new Map<string, FakeLayer>()
  readonly sources = new Map<string, FakeSource>()
  readonly listeners = new Map<string, Set<Listener>>()
  readonly paintCalls: Array<{ id: string; property: string; value: unknown }> = []
  readonly sourceDataCalls: unknown[] = []
  readonly addedControls: unknown[] = []
  readonly removedControls: unknown[] = []
  readonly styleCalls: unknown[] = []
  readonly layerBefore = new Map<string, string | undefined>()
  renderedFeatures: unknown[] = []
  clusterZoomPromise: Promise<number> = Promise.resolve(8)
  readonly canvas: HTMLCanvasElement
  readonly canvasContainer: HTMLDivElement
  readonly scrollZoom = fakeHandler()
  readonly boxZoom = fakeHandler()
  readonly dragRotate = fakeHandler()
  readonly dragPan = fakeHandler()
  readonly keyboard = fakeHandler()
  readonly doubleClickZoom = fakeHandler()
  readonly touchZoomRotate = fakeHandler()
  readonly touchPitch = fakeHandler()
  private center: [number, number] = [0, 0]
  private zoom = 0
  private styleLoaded = true

  constructor(style?: { layers?: FakeLayer[] }) {
    this.canvas = document.createElement('canvas')
    this.canvasContainer = document.createElement('div')
    this.canvasContainer.append(this.canvas)
    for (const layer of style?.layers ?? [{ id: '__lv-background', type: 'background', metadata: { 'leapview:role': 'background' }, paint: {} }]) {
      this.layers.set(layer.id, { ...layer, paint: { ...(layer.paint ?? {}) } })
    }
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
  fire(event: string, ...args: any[]): void { this.emit(event, ...args) }

  triggerRepaint(): void { this.emit('render') }
  getCanvas(): HTMLCanvasElement { return this.canvas }
  getCanvasContainer(): HTMLDivElement { return this.canvasContainer }
  getStyle(): { layers: FakeLayer[] } { return { layers: [...this.layers.values()] } }
  isStyleLoaded(): boolean { return this.styleLoaded }
  setStyle(style: { layers?: FakeLayer[] }, _options?: unknown): this {
    this.styleCalls.push(style)
    this.styleLoaded = false
    this.layers.clear()
    this.sources.clear()
    for (const layer of style.layers ?? []) this.layers.set(layer.id, { ...layer, paint: { ...(layer.paint ?? {}) } })
    queueMicrotask(() => { this.styleLoaded = true; this.emit('styledata') })
    return this
  }
  getLayer(id: string): FakeLayer | undefined { return this.layers.get(id) }
  setPaintProperty(id: string, property: string, value: unknown): void {
    const layer = this.layers.get(id)
    if (!layer) return
    layer.paint[property] = value
    this.paintCalls.push({ id, property, value })
  }
  setLayoutProperty(id: string, property: string, value: unknown): void { const layer = this.layers.get(id); if (layer) (layer.layout ??= {})[property] = value }
  addSource(id: string, source: { type: string; data?: unknown }): void {
    this.sources.set(id, {
      setData: (value) => { source.data = value; this.sourceDataCalls.push(value) },
      getClusterExpansionZoom: () => this.clusterZoomPromise,
    })
  }
  getSource(id: string): FakeSource | undefined { return this.sources.get(id) }
  removeSource(id: string): void { this.sources.delete(id) }
  addLayer(layer: FakeLayer, before?: string): void { this.layerBefore.set(layer.id, before); this.layers.set(layer.id, { ...layer, paint: { ...(layer.paint ?? {}) } }) }
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
  addControl(control: unknown, _position?: string): void { this.addedControls.push(control) }
  removeControl(control: unknown): void { this.removedControls.push(control) }
  queryRenderedFeatures(): unknown[] { return this.renderedFeatures }
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

async function basemapFixture(id: string, labelAnchor: string, attribution: string): Promise<{ asset: VisualizationMapStyleAsset; style: { layers: FakeLayer[] }; body: string }> {
  const style = {
    version: 8,
    sources: { base: { type: 'vector', url: 'pmtiles://__LEAPVIEW_ARCHIVE__' } },
    layers: [
      { id: `background-${id}`, type: 'background', metadata: { 'leapview:role': 'background' }, paint: { 'background-color': '#ffffff' } },
      { id: `land-${id}`, source: 'base', type: 'fill', metadata: { 'leapview:role': 'land' }, paint: { 'fill-color': '#f4f1ea' } },
      { id: labelAnchor, source: 'base', type: 'symbol', metadata: { 'leapview:role': 'label' }, layout: {}, paint: { 'text-color': '#4b4d49', 'text-halo-color': '#f4f1ea' } },
    ],
  }
  const body = JSON.stringify(style)
  const styleDigest = await digest(new TextEncoder().encode(body))
  const digestHex = styleDigest.slice('sha256:'.length)
  return {
    style,
    body,
    asset: {
      id,
      styleUrl: `/map-assets/leapview-streets/styles/${digestHex}/style.json`, styleDigest,
      archiveUrl: `/map-assets/leapview-streets/archives/${'a'.repeat(64)}/basemap.pmtiles`, archiveDigest: `sha256:${'a'.repeat(64)}`,
      glyphsUrl: `/map-assets/leapview-streets/assets/${'b'.repeat(40)}/glyphs/{fontstack}/{range}.pbf`, spriteUrl: `/map-assets/leapview-streets/assets/${'b'.repeat(40)}/sprites/leapview`,
      source: 'OSM', license: 'ODbL', attribution, minimumZoom: 0, maximumZoom: 6, bounds: [-180, -85, 180, 85], labelAnchor,
    },
  }
}

async function labelEnvelope(theme: 'auto' | 'light' | 'dark' = 'auto', specRevision = 'sha256:labels', controls = { zoom: false, reset: false, compass: false }, roam = false, selectable = false, spatial = false, basemap?: VisualizationMapStyleAsset, cameraMode: 'fixed' | 'preserve' = 'fixed'): Promise<VisualizationEnvelope> {
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
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'Labels', description: 'Labels' }, interactions: selectable ? [{ id: 'selection', kind: 'select', mode: 'single', requiresStableIdentity: true, targets: [], mappings: [] }] : [], spatialInteractions: spatial ? [{ id: 'area', gestures: ['box'] }] : [], layers: [point, choropleth],
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, basemap, roam, theme, labelDensity: 'normal', camera: { mode: cameraMode, center: [0, 0], zoom: 2, padding: 24, minimumZoom: 0, maximumZoom: 10 }, controls },
    },
    dataState: { kind: 'inline', specRevision, dataRevision: 1, generation: 1, datasets: [{ id: 'primary', specRevision, dataRevision: 1, generation: 1, columns: ['latitude', 'longitude', 'state', 'value', 'name'], rows: [[-23.5, -46.6, 'SP', 10, 'São Paulo']], completeness: 'complete' }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as unknown as VisualizationEnvelope
}

function pointerEvent(dom: JSDOM, type: string, pointerId: number): Event {
  const event = new dom.window.Event(type, { bubbles: true, cancelable: true })
  Object.defineProperties(event, {
    button: { configurable: true, value: 0 },
    pointerId: { configurable: true, value: pointerId },
    clientX: { configurable: true, value: 0 },
    clientY: { configurable: true, value: 0 },
  })
  return event
}

function installDomGlobals(dom: JSDOM): () => void {
  const values = {
    document: dom.window.document,
    window: dom.window,
    location: dom.window.location,
    getComputedStyle: dom.window.getComputedStyle.bind(dom.window),
    requestAnimationFrame: (callback: FrameRequestCallback) => { callback(0); return 1 },
    cancelAnimationFrame: () => {},
    fetch: globalThis.fetch,
    CustomEvent: dom.window.CustomEvent,
  }
  const keys = Object.keys(values)
  const previous = new Map(keys.map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
  Object.defineProperties(globalThis, Object.fromEntries(keys.map((key) => [key, {
    configurable: true,
    enumerable: true,
    writable: true,
    value: values[key as keyof typeof values],
  }])))
  return () => {
    for (const key of keys) {
      const descriptor = previous.get(key)
      if (descriptor) Object.defineProperty(globalThis, key, descriptor)
      else Reflect.deleteProperty(globalThis, key)
    }
  }
}

test('MapLibre reconciles basemap styles without replacing controls or stale data', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', { url: 'https://dash.example/' })
  const restoreDomGlobals = installDomGlobals(dom)
  const geometryJSON = JSON.stringify({ type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [[[-47, -24], [-46, -24], [-46, -23], [-47, -23], [-47, -24]]] }, properties: { id: 'SP' } }] })
  const styleA = await basemapFixture('streets-a', 'labels-a', 'OSM A')
  const styleB = await basemapFixture('streets-b', 'labels-b', 'OSM B')
  const failed = await basemapFixture('streets-failed', 'labels-failed', 'OSM failed')
  globalThis.fetch = async (input) => {
    const url = String(input)
    if (url.includes(styleA.asset.styleUrl)) return new Response(styleA.body, { status: 200 })
    if (url.includes(styleB.asset.styleUrl)) return new Response(styleB.body, { status: 200 })
    if (url.includes(failed.asset.styleUrl)) return new Response('unavailable', { status: 503 })
    return new Response(geometryJSON, { status: 200, headers: { 'content-type': 'application/json' } })
  }
  try {
    const controls = { zoom: true, reset: true, compass: true }
    const envelopeA = await labelEnvelope('auto', 'sha256:basemap-a', controls, false, false, false, styleA.asset)
    const envelopeB = await labelEnvelope('auto', 'sha256:basemap-b', controls, false, false, false, styleB.asset, 'preserve')
    const envelopeFailed = await labelEnvelope('auto', 'sha256:basemap-failed', controls, false, false, false, failed.asset)
    const envelopeBlank = await labelEnvelope('auto', 'sha256:basemap-blank', controls)
    const container = dom.window.document.createElement('div')
    const frame = dom.window.document.createElement('div')
    const attribution = dom.window.document.createElement('div')
    const map = new FakeMap(styleA.style)
    const handle = new MapLibreHandle(container, frame, map as never, attribution, context('light'))
    const handlers = [map.scrollZoom, map.boxZoom, map.dragRotate, map.dragPan, map.keyboard, map.doubleClickZoom, map.touchZoomRotate, map.touchPitch]
    await handle.update(envelopeA, Change.All, context('light'))
    const navigation = map.addedControls[0]
    expect(map.styleCalls).toHaveLength(0)
    expect(map.getLayer('labels-a')).toBeDefined()
    expect(map.layerBefore.get('lv-states')).toBe('labels-a')
    expect(attribution.textContent).toBe('OSM A')
    expect(frame.querySelector('.lv-map-reset')).not.toBeNull()

    await handle.update(envelopeA, Change.Context, context('dark'))
    await handle.update(envelopeA, Change.Data, context('dark'))
    expect(map.styleCalls).toHaveLength(0)

    await expect(handle.update(envelopeFailed, Change.Spec, context('dark'))).rejects.toThrow(/returned 503/)
    expect(map.styleCalls).toHaveLength(0)
    expect(map.getLayer('labels-a')).toBeDefined()
    expect(map.getLayer('lv-states')).toBeDefined()

    map.jumpTo({ center: [12.5, -3.25], zoom: 5.5 })
    await handle.update(envelopeB, Change.Spec, context('dark'))
    expect(map.styleCalls).toHaveLength(1)
    expect(map.getLayer('labels-a')).toBeUndefined()
    expect(map.getLayer('labels-b')).toBeDefined()
    expect(map.layerBefore.get('lv-states')).toBe('labels-b')
    expect(attribution.textContent).toBe('OSM B')
    expect(map.addedControls[0]).toBe(navigation)
    expect(map.removedControls).toHaveLength(0)
    expect(handlers.every((handler) => !handler.enabled)).toBe(true)
    expect(map.getCenter()).toEqual({ lng: 12.5, lat: -3.25 })
    expect(map.getZoom()).toBe(5.5)

    await handle.update(envelopeB, Change.Spec, context('dark'))
    expect(map.styleCalls).toHaveLength(1)

    await handle.update(envelopeBlank, Change.Spec, context('dark'))
    expect(map.styleCalls).toHaveLength(2)
    expect(map.getLayer('labels-b')).toBeUndefined()
    expect(map.getLayer('__lv-coordinate-reference')).toBeDefined()
    expect(attribution.textContent).toBe('')
    expect(attribution.hidden).toBe(true)
    expect(map.addedControls[0]).toBe(navigation)
    expect(map.removedControls).toHaveLength(0)
    handle.dispose()
  } finally {
    restoreDomGlobals()
    dom.window.close()
  }
})

test('MapLibre reconciles roam handlers and interactive affordances without context churn', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', { url: 'https://dash.example/' })
  const restoreDomGlobals = installDomGlobals(dom)
  const geometryJSON = JSON.stringify({ type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [[[-47, -24], [-46, -24], [-46, -23], [-47, -23], [-47, -24]]] }, properties: { id: 'SP' } }] })
  globalThis.fetch = async () => new Response(geometryJSON, { status: 200, headers: { 'content-type': 'application/json' } })
  try {
    const roaming = await labelEnvelope('auto', 'sha256:roam-on', { zoom: false, reset: false, compass: false }, true)
    const container = dom.window.document.createElement('div')
    const frame = dom.window.document.createElement('div')
    const attribution = dom.window.document.createElement('div')
    const map = new FakeMap()
    const handle = new MapLibreHandle(container, frame, map as never, attribution, context('light'))
    const handlers = [map.scrollZoom, map.boxZoom, map.dragRotate, map.dragPan, map.keyboard, map.doubleClickZoom, map.touchZoomRotate, map.touchPitch]
    await handle.update(roaming, Change.All, context('light'))
    expect(handlers.every((handler) => handler.enabled)).toBe(true)
    expect(map.canvasContainer.classList.contains('maplibregl-interactive')).toBe(true)
    expect(map.canvas.tabIndex).toBe(0)
    const sourceIDs = [...map.sources.keys()]
    const layerIDs = [...map.layers.keys()]
    const handlerCalls = handlers.map((handler) => [handler.enableCalls, handler.disableCalls])

    await handle.update(roaming, Change.Context, context('dark'))
    expect(handlers.map((handler) => [handler.enableCalls, handler.disableCalls])).toEqual(handlerCalls)
    expect([...map.sources.keys()]).toEqual(sourceIDs)
    expect([...map.layers.keys()]).toEqual(layerIDs)

    const selectionOnly = await labelEnvelope('auto', 'sha256:roam-selection-only', { zoom: false, reset: false, compass: false }, false, true)
    await handle.update(selectionOnly, Change.Spec, context('dark'))
    expect(handlers.every((handler) => !handler.enabled)).toBe(true)
    expect(map.canvasContainer.classList.contains('maplibregl-interactive')).toBe(true)
    expect(map.canvas.tabIndex).toBe(0)

    const fixed = await labelEnvelope('auto', 'sha256:roam-off', { zoom: false, reset: false, compass: false }, false)
    await handle.update(fixed, Change.Spec, context('dark'))
    expect(handlers.every((handler) => !handler.enabled)).toBe(true)
    expect(map.canvasContainer.classList.contains('maplibregl-interactive')).toBe(false)
    expect(map.canvas.tabIndex).toBe(-1)

    const roamingAgain = await labelEnvelope('auto', 'sha256:roam-on-again', { zoom: false, reset: false, compass: false }, true)
    await handle.update(roamingAgain, Change.Spec, context('dark'))
    expect(handlers.every((handler) => handler.enabled)).toBe(true)
    expect(map.canvasContainer.classList.contains('maplibregl-interactive')).toBe(true)
    expect(map.canvas.tabIndex).toBe(0)
    handle.dispose()
  } finally {
    restoreDomGlobals()
    dom.window.close()
  }
})

test('MapLibre keeps spatial drag-pan disabled through a roam update during a gesture', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', { url: 'https://dash.example/' })
  const restoreDomGlobals = installDomGlobals(dom)
  const geometryJSON = JSON.stringify({ type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [[[-47, -24], [-46, -24], [-46, -23], [-47, -23], [-47, -24]]] }, properties: { id: 'SP' } }] })
  globalThis.fetch = async () => new Response(geometryJSON, { status: 200, headers: { 'content-type': 'application/json' } })
  try {
    const roaming = await labelEnvelope('auto', 'sha256:spatial-roam-on', { zoom: false, reset: false, compass: false }, true, false, true)
    const container = dom.window.document.createElement('div')
    const frame = dom.window.document.createElement('div')
    const attribution = dom.window.document.createElement('div')
    const map = new FakeMap()
    const handle = new MapLibreHandle(container, frame, map as never, attribution, context('light'))
    await handle.update(roaming, Change.All, context('light'))
    const control = frame.querySelector<HTMLElement>('[data-map-spatial-selection-control]')!
    const button = control.querySelector<HTMLButtonElement>('[aria-label="Add map area with box"]')!
    button.click()
    expect(button.getAttribute('aria-pressed')).toBe('true')
    map.canvas.dispatchEvent(pointerEvent(dom, 'pointerdown', 11))
    expect(map.dragPan.enabled).toBe(false)

    const fixed = await labelEnvelope('auto', 'sha256:spatial-roam-off', { zoom: false, reset: false, compass: false }, false, false, true)
    await handle.update(fixed, Change.Spec, context('light'))
    expect(map.dragPan.enabled).toBe(false)
    map.canvas.dispatchEvent(pointerEvent(dom, 'pointerup', 11))
    expect(map.dragPan.enabled).toBe(false)
    handle.dispose()
  } finally {
    restoreDomGlobals()
    dom.window.close()
  }
})

test('MapLibre reconciles navigation and reset controls across spec updates without context churn', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', { url: 'https://dash.example/' })
  const restoreDomGlobals = installDomGlobals(dom)
  const geometryJSON = JSON.stringify({ type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [[[-47, -24], [-46, -24], [-46, -23], [-47, -23], [-47, -24]]] }, properties: { id: 'SP' } }] })
  globalThis.fetch = async () => new Response(geometryJSON, { status: 200, headers: { 'content-type': 'application/json' } })
  try {
    const all = await labelEnvelope('auto', 'sha256:controls-all', { zoom: true, reset: true, compass: true })
    const container = dom.window.document.createElement('div')
    const frame = dom.window.document.createElement('div')
    const attribution = dom.window.document.createElement('div')
    const map = new FakeMap()
    const handle = new MapLibreHandle(container, frame, map as never, attribution, context('light'))
    await handle.update(all, Change.All, context('light'))
    expect(map.addedControls).toHaveLength(1)
    expect((map.addedControls[0] as { options: { showZoom: boolean; showCompass: boolean } }).options).toMatchObject({ showZoom: true, showCompass: true })
    expect(frame.querySelector('.lv-map-reset')).not.toBeNull()

    await handle.update(all, Change.Context, context('dark'))
    expect(map.addedControls).toHaveLength(1)
    expect(map.removedControls).toHaveLength(0)
    await handle.update(all, Change.Spec, context('dark'))
    expect(map.addedControls).toHaveLength(1)
    expect(map.removedControls).toHaveLength(0)

    map.jumpTo({ center: [12.5, -3.25], zoom: 5.5 })
    const resetOnly = await labelEnvelope('auto', 'sha256:controls-reset', { zoom: false, reset: true, compass: false }, false, false, false, undefined, 'preserve')
    await handle.update(resetOnly, Change.Spec, context('dark'))
    expect(map.getCenter()).toEqual({ lng: 12.5, lat: -3.25 })
    expect(map.getZoom()).toBe(5.5)
    expect(map.addedControls).toHaveLength(1)
    expect(map.removedControls).toHaveLength(1)
    expect(frame.querySelector('.lv-map-reset')).not.toBeNull()

    const navOnly = await labelEnvelope('auto', 'sha256:controls-nav', { zoom: true, reset: false, compass: true })
    await handle.update(navOnly, Change.Spec, context('dark'))
    expect(map.addedControls).toHaveLength(2)
    expect(map.removedControls).toHaveLength(1)
    expect(frame.querySelector('.lv-map-reset')).toBeNull()
    expect((map.addedControls[1] as { options: { showZoom: boolean; showCompass: boolean } }).options).toMatchObject({ showZoom: true, showCompass: true })

    const compassOnly = await labelEnvelope('auto', 'sha256:controls-compass', { zoom: false, reset: false, compass: true })
    await handle.update(compassOnly, Change.Spec, context('dark'))
    expect(map.addedControls).toHaveLength(3)
    expect(map.removedControls).toHaveLength(2)
    expect((map.addedControls[2] as { options: { showZoom: boolean; showCompass: boolean } }).options).toMatchObject({ showZoom: false, showCompass: true })

    const none = await labelEnvelope('auto', 'sha256:controls-none')
    await handle.update(none, Change.Spec, context('dark'))
    expect(map.removedControls).toHaveLength(3)
    expect(frame.querySelector('.lv-map-reset')).toBeNull()
    handle.dispose()
    expect(map.removedControls).toHaveLength(3)
  } finally {
    restoreDomGlobals()
    dom.window.close()
  }
})

test('MapLibre applies a fixed camera during tiled placeholder bootstrap', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', { url: 'https://dash.example/' })
  const restoreDomGlobals = installDomGlobals(dom)
  const geometryJSON = JSON.stringify({ type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [[[-47, -24], [-46, -24], [-46, -23], [-47, -23], [-47, -24]]] }, properties: { id: 'SP' } }] })
  globalThis.fetch = async () => new Response(geometryJSON, { status: 200, headers: { 'content-type': 'application/json' } })
  try {
    const inline = await labelEnvelope('auto', 'sha256:tiled-bootstrap', { zoom: false, reset: false, compass: false })
    const tiled = {
      ...inline,
      status: { kind: 'loading' },
      dataState: {
        kind: 'spatial_tiled', specRevision: inline.specRevision, dataRevision: 1, generation: 1,
        schema: inline.spec.datasets[0], cardinality: { kind: 'exact', count: 1 }, extent: { west: -180, south: -85, east: 180, north: 85 },
        rawDomains: [], aggregateDomains: [], tileURL: '/tiles/unavailable/bootstrap/{z}/{x}/{y}.mvt', minimumZoom: 0, maximumZoom: 18, rawMinimumZoom: 10, featureCap: 5000, maximumTileBytes: 524288,
      },
    } as unknown as VisualizationEnvelope
    const container = dom.window.document.createElement('div')
    const frame = dom.window.document.createElement('div')
    const attribution = dom.window.document.createElement('div')
    const map = new FakeMap()
    const handle = new MapLibreHandle(container, frame, map as never, attribution, context('light'))
    await handle.update(tiled, Change.All, context('light'))
    expect(map.getCenter()).toEqual({ lng: 0, lat: 0 })
    expect(map.getZoom()).toBe(2)
    handle.dispose()
  } finally {
    restoreDomGlobals()
    dom.window.close()
  }
})

test('MapLibre rechecks cluster camera policy after asynchronous expansion', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', { url: 'https://dash.example/' })
  const restoreDomGlobals = installDomGlobals(dom)
  const geometryJSON = JSON.stringify({ type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [[[-47, -24], [-46, -24], [-46, -23], [-47, -23], [-47, -24]]] }, properties: { id: 'SP' } }] })
  globalThis.fetch = async () => new Response(geometryJSON, { status: 200, headers: { 'content-type': 'application/json' } })
  try {
    let resolveZoom!: (zoom: number) => void
    const pendingZoom = new Promise<number>((resolve) => { resolveZoom = resolve })
    const roaming = await labelEnvelope('auto', 'sha256:cluster-roaming', { zoom: false, reset: false, compass: false }, true, false, false, undefined, 'preserve')
    const fixed = await labelEnvelope('auto', 'sha256:cluster-fixed', { zoom: false, reset: false, compass: false }, false)
    const point = roaming.spec.kind === 'geographic' ? roaming.spec.layers.find((layer) => layer.kind === 'point') : undefined
    if (!point || roaming.spec.kind !== 'geographic') throw new Error('point map fixture is unavailable')
    const clustered = { ...roaming, spec: { ...roaming.spec, layers: roaming.spec.layers.map((layer) => layer.kind === 'point' ? { ...layer, cluster: { ...layer.cluster, enabled: true } } : layer) } } as VisualizationEnvelope
    const container = dom.window.document.createElement('div')
    const frame = dom.window.document.createElement('div')
    const attribution = dom.window.document.createElement('div')
    const map = new FakeMap()
    map.clusterZoomPromise = pendingZoom
    const handle = new MapLibreHandle(container, frame, map as never, attribution, context('light'))
    await handle.update(clustered, Change.All, context('light'))
    map.renderedFeatures = [{ layer: { id: 'lv-points-clusters' }, properties: { cluster_id: 7 }, geometry: { type: 'Point', coordinates: [12, 3] } }]
    map.fire('click', { point: { x: 0, y: 0 } })
    await handle.update(fixed, Change.Spec, context('light'))
    expect(map.getZoom()).toBe(2)
    resolveZoom(8)
    await pendingZoom
    await Promise.resolve()
    expect(map.getZoom()).toBe(2)
    expect(map.getCenter()).toEqual({ lng: 0, lat: 0 })
    handle.dispose()
  } finally {
    restoreDomGlobals()
    dom.window.close()
  }
})
