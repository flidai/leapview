import { expect, test } from 'bun:test'

import type { VisualizationEnvelope, VisualizationGeographicLayer } from '../../../../generated/visualization'
import type { FeatureCollection } from 'geojson'
import { aggregateExpansionCamera, applyFeatureScales, basemapBoundaryLayer, basemapLayer, basemapThemeKey, clusterExpansionForRenderedFeatures, concreteCSSColor, coordinateGeometry, coordinateReferenceGrid, createBasemapThemeScheduler, fitMapToGeographicData, installWebGLRecovery, interactionCommandForRenderedFeatures, joinGeometry, loadMapStyleAsset, mapAccessibleData, mapAccessibleRenderedFeatures, mapInteractionCommand, mapLayer, mapLibreChromeCSS, mapOutlineLayer, mapPointerOptions, mapThemeColors, mapTooltipEntries, nextSpatialRequestSequence, normalizeFeatureWeights, pathGeometry, removeRendererFrame, resetMapToHome, sameOriginGeometryURL, setRendererFramePresented, spatialWindowAlreadyCurrent, spatialWindowRequest, tiledAggregateCountLayer, tiledAggregateHeatLayer, tiledAggregatePointLayer, tiledLayerPaintUpdates, tiledRawPrecisionVisible, updateSelectionSources, vectorTileTemplateURL, verifyGeometryDigest, waitForMapRender } from './maplibre'
import { adapterObservation } from '../telemetry'

test('MapLibre owns usable shadow-DOM styles for map navigation controls', () => {
  expect(mapLibreChromeCSS).toContain('.maplibregl-ctrl-top-right')
  expect(mapLibreChromeCSS).toContain('width:30px')
  expect(mapLibreChromeCSS).toContain('.maplibregl-ctrl-zoom-in::before')
  expect(mapLibreChromeCSS).toContain('.maplibregl-ctrl-compass')
})

test('MapLibre geometry assets are same-origin and content addressed', async () => {
  expect(sameOriginGeometryURL('/static/geometry/states.geojson', 'https://dash.example/workspaces/sales').href).toBe('https://dash.example/static/geometry/states.geojson')
  expect(() => sameOriginGeometryURL('https://attacker.example/states.geojson', 'https://dash.example/workspaces/sales')).toThrow(/same-origin/)
  await expect(verifyGeometryDigest(new TextEncoder().encode('geometry'), 'sha256:invalid')).rejects.toThrow(/canonical SHA-256/)
  await expect(verifyGeometryDigest(new TextEncoder().encode('geometry'), `sha256:${'0'.repeat(64)}`)).rejects.toThrow(/digest mismatch/)
  // Plain-HTTP development shares are not secure contexts, so browsers do
  // not expose SubtleCrypto there. Canonical, content-addressed URLs remain
  // enforced even when client-side digest verification is unavailable.
  await expect(verifyGeometryDigest(new TextEncoder().encode('geometry'), `sha256:${'0'.repeat(64)}`, null)).resolves.toBeUndefined()
})

test('MapLibre map styles rewrite only pinned same-origin PMTiles and assets', async () => {
  const style = new TextEncoder().encode(JSON.stringify({ version: 8, sources: { base: { type: 'vector', url: 'pmtiles://__LEAPVIEW_ARCHIVE__' } }, layers: [] }))
  const digest = new Uint8Array(await crypto.subtle.digest('SHA-256', style))
  const styleDigest = [...digest].map((value) => value.toString(16).padStart(2, '0')).join('')
  const asset = {
    id: 'streets', styleUrl: `/map-assets/leapview-streets/styles/${styleDigest}/style.json`, styleDigest: `sha256:${styleDigest}`,
    archiveUrl: `/map-assets/leapview-streets/archives/${'a'.repeat(64)}/basemap.pmtiles`, archiveDigest: `sha256:${'a'.repeat(64)}`, glyphsUrl: `/map-assets/leapview-streets/assets/${'b'.repeat(40)}/glyphs/{fontstack}/{range}.pbf`, spriteUrl: `/map-assets/leapview-streets/assets/${'b'.repeat(40)}/sprites/leapview`,
    source: 'OSM', license: 'ODbL', attribution: 'OSM', minimumZoom: 0, maximumZoom: 6, bounds: [-180, -85, 180, 85], labelAnchor: 'labels',
  } as const
  const previous = globalThis.fetch
  globalThis.fetch = (async () => new Response(style)) as typeof fetch
  try {
    const loaded = await loadMapStyleAsset(asset, 'https://dash.example/workspaces/maps')
    expect((loaded.sources.base as { url?: string }).url).toBe(`pmtiles://https://dash.example/map-assets/leapview-streets/archives/${'a'.repeat(64)}/basemap.pmtiles`)
    expect(loaded.glyphs).toBe(`https://dash.example/map-assets/leapview-streets/assets/${'b'.repeat(40)}/glyphs/{fontstack}/{range}.pbf`)
    await expect(loadMapStyleAsset({ ...asset, styleUrl: 'https://attacker.example/style.json' }, 'https://dash.example/maps')).rejects.toThrow(/same-origin/)
    await expect(loadMapStyleAsset({ ...asset, styleUrl: '/map-assets/leapview-streets/style.json' }, 'https://dash.example/maps')).rejects.toThrow(/content-addressed/)
  } finally { globalThis.fetch = previous }
})

test('MapLibre prevents permanent WebGL loss, repaints after restoration, and removes recovery listeners', () => {
  const canvas = new EventTarget()
  let resized = 0
  let repainted = 0
  const observations: string[] = []
  const dispose = installWebGLRecovery(canvas, {
    resize: () => { resized++ },
    triggerRepaint: () => { repainted++ },
  }, (stage) => observations.push(stage))

  const lost = new Event('webglcontextlost', { cancelable: true })
  canvas.dispatchEvent(lost)
  expect(lost.defaultPrevented).toBe(true)
  canvas.dispatchEvent(new Event('webglcontextrestored'))
  expect([resized, repainted]).toEqual([1, 1])
  expect(observations).toEqual(['webgl_context_loss', 'webgl_context_restored'])

  dispose()
  canvas.dispatchEvent(new Event('webglcontextrestored'))
  expect([resized, repainted]).toEqual([1, 1])
})

test('MapLibre becomes renderer-ready after a rendered frame without waiting for every basemap tile to idle', async () => {
  const listeners = new Map<string, Set<() => void>>()
  let repainted = 0
  const map = {
    once: (event: string, listener: () => void) => {
      const eventListeners = listeners.get(event) ?? new Set()
      eventListeners.add(listener)
      listeners.set(event, eventListeners)
    },
    off: (event: string, listener: () => void) => listeners.get(event)?.delete(listener),
    triggerRepaint: () => { repainted++ },
  }

  const ready = waitForMapRender(map as never)
  expect(repainted).toBe(1)
  expect(listeners.get('idle')?.size).toBe(1)
  expect(listeners.get('render')?.size).toBe(1)
  listeners.get('render')?.forEach((listener) => listener())
  await ready

  expect(listeners.get('idle')?.size).toBe(0)
  expect(listeners.get('render')?.size).toBe(0)
})

test('MapLibre keeps its frame hidden and inaccessible until the final fitted frame is presented', () => {
  const attributes = new Map<string, string>()
  const frame = {
    style: { visibility: '' },
    setAttribute: (name: string, value: string) => attributes.set(name, value),
    removeAttribute: (name: string) => attributes.delete(name),
  }

  setRendererFramePresented(frame, false)
  expect(frame.style.visibility).toBe('hidden')
  expect(attributes.get('aria-hidden')).toBe('true')

  setRendererFramePresented(frame, true)
  expect(frame.style.visibility).toBe('visible')
  expect(attributes.has('aria-hidden')).toBe(false)
})

test('MapLibre observations enter the shared visualization telemetry contract', () => {
  expect(adapterObservation({ stage: 'basemap_load', durationMs: 12, visualID: 'map', rendererID: 'maplibre' })).toEqual({
    stage: 'adapter_observation', adapterStage: 'basemap_load', durationMs: 12, visualID: 'map', rendererID: 'maplibre',
  })
  expect(adapterObservation({ stage: 'unknown', durationMs: 12, visualID: 'map', rendererID: 'maplibre' })).toBeUndefined()
})

test('MapLibre point, heat, and density layers use typed in-memory coordinates without geometry fetches', () => {
  const layer = {
    id: 'stores', kind: 'point', latitude: { dataset: 'primary', field: 'lat' }, longitude: { dataset: 'primary', field: 'lon' }, value: { dataset: 'primary', field: 'value' },
  } as VisualizationGeographicLayer
  const envelope = {
    dataRevision: 9,
    dataState: { kind: 'inline', datasets: [{ id: 'primary', columns: ['lat', 'lon', 'value'], rows: [[55.67, 12.56, 3], ['invalid', 12, 9], [91, 12, 4], [20, 181, 5]] }] },
    selection: [{ datum: { dataset: 'primary', dataRevision: 9, identity: { lat: 55.67, lon: 12.56 } }, label: 'Copenhagen' }],
  } as VisualizationEnvelope
  const geometry = coordinateGeometry(envelope, layer)
  expect(geometry.features).toHaveLength(1)
  expect(geometry.features[0]?.geometry).toEqual({ type: 'Point', coordinates: [12.56, 55.67] })
  expect(geometry.features[0]?.properties?.__lv_value).toBe(3)
  expect(geometry.features[0]?.properties?.__lv_selected).toBe(true)
  expect(geometry.features[0]?.properties).toMatchObject({ __lv_dataset: 'primary', __lv_row_index: 0, __lv_layer_id: 'stores' })
})

test('MapLibre selectable features carry only a validated internal row locator', () => {
  const envelope = selectableEnvelope()
  const layer = envelope.spec.kind === 'geographic' ? envelope.spec.layers[0]! : undefined
  const geometry = {
    type: 'FeatureCollection',
    features: [
      { type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [] }, properties: { id: 'SP', publicGeometryName: 'São Paulo' } },
      { type: 'Feature', id: 'BA', geometry: { type: 'Polygon', coordinates: [] }, properties: { id: 'BA' } },
    ],
  } as FeatureCollection
  const joined = joinGeometry(envelope, layer!, geometry)
  expect(joined.features[0]?.properties).toMatchObject({
    id: 'SP', publicGeometryName: 'São Paulo', __lv_dataset: 'primary', __lv_row_index: 0, __lv_layer_id: 'states', __lv_value: 10,
  })
  expect(joined.features[0]?.properties).not.toHaveProperty('customer_secret')
  expect(joined.features[1]?.properties).not.toHaveProperty('__lv_row_index')
})

test('MapLibre hit testing selects the topmost valid point or region and rejects forged locators', () => {
  const envelope = selectableEnvelope()
  const features = [
    { layer: { id: 'lv-states' }, properties: { __lv_dataset: 'primary', __lv_row_index: 1, __lv_layer_id: 'states' } },
    { layer: { id: 'lv-states' }, properties: { __lv_dataset: 'primary', __lv_row_index: 0, __lv_layer_id: 'states' } },
  ]
  expect(interactionCommandForRenderedFeatures(envelope, features, ['lv-states'])?.mappings[0]?.value).toBe('RJ')
  expect(interactionCommandForRenderedFeatures(envelope, [{ layer: { id: 'lv-states' }, properties: { __lv_dataset: 'forged', __lv_row_index: 0, __lv_layer_id: 'states' } }], ['lv-states'])).toBeUndefined()
  expect(interactionCommandForRenderedFeatures(envelope, [{ layer: { id: 'lv-states' }, properties: { __lv_dataset: 'primary', __lv_row_index: 99, __lv_layer_id: 'states' } }], ['lv-states'])).toBeUndefined()
  expect(interactionCommandForRenderedFeatures(envelope, [{ layer: { id: 'lv-heat' }, properties: { __lv_dataset: 'primary', __lv_row_index: 0, __lv_layer_id: 'heat' } }], ['lv-states'])).toBeUndefined()
})

test('MapLibre cluster expansion selects the topmost valid cluster without requiring datum selection', () => {
  const sources = new Map([['lv-points-clusters', 'lv-points-source']])
  expect(clusterExpansionForRenderedFeatures([
    { layer: { id: 'forged' }, properties: { cluster_id: 7 }, geometry: { type: 'Point', coordinates: [1, 2] } },
    { layer: { id: 'lv-points-clusters' }, properties: { cluster_id: 9 }, geometry: { type: 'Point', coordinates: [-46.63, -23.55] } },
  ], sources)).toEqual({ sourceID: 'lv-points-source', clusterID: 9, center: [-46.63, -23.55] })
  expect(clusterExpansionForRenderedFeatures([
    { layer: { id: 'lv-points-clusters' }, properties: { cluster_id: 'forged' }, geometry: { type: 'Point', coordinates: [-46.63, -23.55] } },
  ], sources)).toBeUndefined()
})

test('MapLibre keeps semantic selection active when pan and zoom are disabled', () => {
  const envelope = selectableEnvelope()
  expect(mapPointerOptions(envelope)).toEqual({
    interactive: true,
    scrollZoom: false, boxZoom: false, dragRotate: false, dragPan: false, keyboard: false,
    doubleClickZoom: false, touchZoomRotate: false, touchPitch: false,
  })
})

test('MapLibre blank hits clear only the selection owned by that map', () => {
  const envelope = selectableEnvelope()
  expect(mapInteractionCommand(envelope, [], ['lv-states'])).toBeUndefined()
  const selected = {
    ...envelope,
    selection: [{ datum: { dataset: 'primary', dataRevision: 4, identity: { state: 'SP' } }, label: 'SP' }],
  } as VisualizationEnvelope
  expect(mapInteractionCommand(selected, [], ['lv-states'])).toEqual({
    sourceKind: 'visual', sourceId: 'state-map', interactionKind: 'point_selection', action: 'clear', toggle: false, mappings: [],
  })
})

test('MapLibre selection-only refreshes update existing sources without rebuilding map state', () => {
  const envelope = selectableEnvelope()
  const layer = envelope.spec.kind === 'geographic' ? envelope.spec.layers[0]! : undefined
  const geometry = { type: 'FeatureCollection', features: [{ type: 'Feature', id: 'SP', geometry: { type: 'Polygon', coordinates: [] }, properties: { id: 'SP' } }] } as FeatureCollection
  const updates: FeatureCollection[] = []
  const result = updateSelectionSources(envelope, [{ spec: layer!, sourceID: 'lv-states', geometry }], (sourceID) => sourceID === 'lv-states' ? { setData: (data) => updates.push(data as FeatureCollection) } : undefined)
  expect(result.updated).toBe(1)
  expect(result.collections).toEqual(updates)
  expect(updates).toHaveLength(1)
  expect(updates[0]?.features[0]?.properties).toMatchObject({ __lv_dataset: 'primary', __lv_row_index: 0, __lv_layer_id: 'states' })
})

test('MapLibre spatial requests preserve revision and viewport identity', () => {
  const envelope = {
    ...selectableEnvelope(),
    dataRevision: 12,
    dataState: {
      kind: 'spatial_windowed', specRevision: 'sha256:test', dataRevision: 12, generation: 2,
      schema: selectableEnvelope().spec.datasets[0], cardinality: { kind: 'estimate', count: 1_000_000 },
      extent: { west: -180, south: -85, east: 180, north: 85 }, rowCap: 1_000_000, featureCap: 5000,
      resetVersion: 4,
    },
  } as VisualizationEnvelope
  expect(spatialWindowRequest(envelope, { west: 170, south: -20, east: -170, north: 25 }, 3.25, 960, 540, 8)).toBeUndefined()

  const populated = {
    ...envelope,
    dataState: {
      ...envelope.dataState,
      window: {
        id: 'initial', bounds: { west: -180, south: -85, east: 180, north: 85 }, zoom: 0, width: 960, height: 540,
        precision: 'aggregated', rows: [], requestSeq: 7, resetVersion: 4,
      },
    },
  } as VisualizationEnvelope
  expect(spatialWindowRequest(populated, { west: 170, south: -20, east: -170, north: 25 }, 3.25, 960, 540, 8)).toEqual({
    visualID: 'state-map', specRevision: 'sha256:test', dataRevision: 12, requestSeq: 8, resetVersion: 4,
    bounds: { west: 170, south: -20, east: -170, north: 25 }, zoom: 3.25, width: 960, height: 540,
    windowID: '170.000000,-20.000000,-170.000000,25.000000@3.250:960x540',
  })
  expect(spatialWindowRequest(selectableEnvelope(), { west: 0, south: 0, east: 1, north: 1 }, 1, 100, 100, 1)).toBeUndefined()
})

test('MapLibre spatial requests normalize wrapped worlds and bound browser-controlled coordinates', () => {
  const envelope = {
    ...selectableEnvelope(),
    dataState: {
      kind: 'spatial_windowed', specRevision: 'sha256:test', dataRevision: 12, generation: 2,
      schema: selectableEnvelope().spec.datasets[0], cardinality: { kind: 'estimate', count: 1_000_000 },
      extent: { west: -180, south: -85, east: 180, north: 85 }, rowCap: 1_000_000, featureCap: 5000,
      resetVersion: 4,
      window: {
        id: 'initial', bounds: { west: -180, south: -85, east: 180, north: 85 }, zoom: 0, width: 100, height: 100,
        precision: 'aggregated', rows: [], requestSeq: 8, resetVersion: 4,
      },
    },
  } as VisualizationEnvelope

  expect(spatialWindowRequest(envelope, { west: 170, south: -20, east: 190, north: 25 }, 3, 20_000, 18_000, 9)).toMatchObject({
    bounds: { west: 170, south: -20, east: -170, north: 25 }, width: 16_384, height: 16_384,
    windowID: '170.000000,-20.000000,-170.000000,25.000000@3.000:16384x16384',
  })
  expect(spatialWindowRequest(envelope, { west: -540, south: -85, east: 540, north: 85 }, 1, 100, 100, 10)).toMatchObject({
    bounds: { west: -180, south: -85, east: 180, north: 85 },
  })
  expect(spatialWindowRequest(envelope, { west: 0, south: 0, east: 1, north: 1 }, 1, Number.POSITIVE_INFINITY, 100, 1)).toBeUndefined()
  expect(spatialWindowRequest(envelope, { west: 0, south: 0, east: 1, north: 1 }, 1, 100, 100, 0)).toBeUndefined()
  expect(spatialWindowRequest(envelope, { west: 0, south: 0, east: 1, north: 1 }, 1, 100, 100, 1.5)).toBeUndefined()
})

test('MapLibre viewport requests advance server sequence and suppress an already-current window', () => {
	const envelope = {
		...selectableEnvelope(),
		dataRevision: 14,
		dataState: {
			kind: 'spatial_windowed', specRevision: 'sha256:test', dataRevision: 14, generation: 3,
			schema: selectableEnvelope().spec.datasets[0], cardinality: { kind: 'exact', count: 1_000_000 },
			extent: { west: -180, south: -85, east: 180, north: 85 }, rowCap: 1_000_000, featureCap: 5000, resetVersion: 6,
			window: {
				id: '-74.000000,-34.000000,-34.000000,6.000000@5.000:1200x700',
				bounds: { west: -74, south: -34, east: -34, north: 6 }, zoom: 5, width: 1200, height: 700,
				precision: 'aggregated', rows: [], requestSeq: 21, resetVersion: 6,
			},
		},
	} as VisualizationEnvelope

	expect(nextSpatialRequestSequence(envelope, 3)).toBe(22)
	const current = spatialWindowRequest(envelope, { west: -74, south: -34, east: -34, north: 6 }, 5, 1200, 700, 22)!
	expect(spatialWindowAlreadyCurrent(envelope, current)).toBe(true)
	expect(spatialWindowAlreadyCurrent(envelope, { ...current, resetVersion: 7 })).toBe(false)
	expect(spatialWindowAlreadyCurrent(envelope, { ...current, windowID: '-73.000000,-34.000000,-34.000000,6.000000@5.000:1200x700' })).toBe(false)
})

test('a superseded MapLibre mount cannot remove the winning renderer frame', () => {
  const container = {} as ParentNode
  let staleRemoved = false
  const staleFrame = {
    parentNode: null,
    remove: () => { staleRemoved = true },
  }
  removeRendererFrame(container, staleFrame as unknown as HTMLElement)
  expect(staleRemoved).toBe(false)

  let ownedRemoved = false
  const ownedFrame = { parentNode: container, remove: () => { ownedRemoved = true } }
  removeRendererFrame(container, ownedFrame as unknown as HTMLElement)
  expect(ownedRemoved).toBe(true)
})

test('MapLibre normalizes finite measure values without losing raw tooltip values', () => {
  const data = {
    type: 'FeatureCollection',
    features: [
      { type: 'Feature', geometry: { type: 'Point', coordinates: [-70, -20] }, properties: { __lv_value: 10 } },
      { type: 'Feature', geometry: { type: 'Point', coordinates: [-60, -10] }, properties: { __lv_value: 20 } },
      { type: 'Feature', geometry: { type: 'Point', coordinates: [-50, 0] }, properties: { __lv_value: 30 } },
      { type: 'Feature', geometry: { type: 'Point', coordinates: [-40, 10] }, properties: { __lv_value: null } },
    ],
  } as FeatureCollection

  const normalized = normalizeFeatureWeights(data)
  expect(normalized.features.map((feature) => feature.properties?.__lv_value)).toEqual([10, 20, 30, null])
  expect(normalized.features.map((feature) => feature.properties?.__lv_weight)).toEqual([0, 0.5, 1, 0])

  const fixed = normalizeFeatureWeights(data, { domainMinimum: 0, domainMaximum: 40 })
  expect(fixed.features.map((feature) => feature.properties?.__lv_weight)).toEqual([0.25, 0.5, 0.75, 0])

  const diverging = normalizeFeatureWeights(data, { domainMinimum: 0, domainMidpoint: 10, domainMaximum: 30 })
  expect(diverging.features.map((feature) => feature.properties?.__lv_weight)).toEqual([0.5, 0.75, 1, 0])
})

test('MapLibre categorical scales assign deterministic colors by category', () => {
  const layer = {
    id: 'stores', kind: 'point', latitude: { dataset: 'primary', field: 'lat' }, longitude: { dataset: 'primary', field: 'lon' },
    category: { dataset: 'primary', field: 'category' }, color: { kind: 'categorical', palette: 'teal', reverse: false, nullColor: '#ccc' },
    tooltip: [], position: 'above_labels', visibility: { minimumZoom: 0, maximumZoom: 24 },
    size: { minimumRadius: 4, maximumRadius: 18 }, stroke: { color: '#fff', width: 1, opacity: 1 },
    cluster: { enabled: false, radius: 40, maximumZoom: 14, minimumPoints: 2, showCount: true }, opacity: .8,
  } as VisualizationGeographicLayer
  const envelope = {
    ...selectableEnvelope(),
    dataState: { kind: 'inline', datasets: [{ id: 'primary', columns: ['lat', 'lon', 'category'], rows: [[1, 1, 'B'], [2, 2, 'A'], [3, 3, 'B']] }] },
  } as VisualizationEnvelope
  const decorated = applyFeatureScales(coordinateGeometry(envelope, layer), layer)
  const colors = decorated.features.map((feature) => feature.properties?.__lv_color)
  expect(colors[0]).toBe(colors[2])
  expect(colors[0]).not.toBe(colors[1])
  expect(mapLayer('lv-stores', layer).paint['circle-color']).toEqual(['coalesce', ['get', '__lv_color'], '#ccc'])
})

test('MapLibre tooltips use compiled fields and contractual formatting without exposing other columns', () => {
  const envelope = selectableEnvelope()
  const entries = mapTooltipEntries(envelope, [{ layer: { id: 'lv-states' }, properties: { __lv_dataset: 'primary', __lv_row_index: 0, __lv_layer_id: 'states' } }])
  expect(entries).toEqual([{ label: 'State', value: 'SP' }, { label: 'Revenue', value: '10' }])
  expect(entries.some((entry) => entry.value.includes('governed'))).toBe(false)
})

test('MapLibre aggregate tooltips lead with the business measure and retain location count', () => {
	const envelope = tiledPointEnvelope()
	const entries = mapTooltipEntries(envelope, [{
		layer: { id: 'lv-orders' },
		properties: { __lv_aggregate: true, __lv_coordinate_count: 12, revenue: 1_250 },
	}])
	expect(entries).toEqual([
		{ label: 'Precision', value: 'Aggregated area' },
		{ label: 'Contained locations', value: '12' },
		{ label: 'Revenue', value: '1250' },
	])
})

test('MapLibre tiled layers reuse one native source and stable server-provided domains', () => {
  const envelope = tiledPointEnvelope()
  const layer = envelope.spec.kind === 'geographic' ? envelope.spec.layers[0]! : undefined
  const style = mapLayer('lv-orders', layer!, envelope.dataState.kind === 'spatial_tiled' ? envelope.dataState : undefined, 'lv-map-tiles')
  expect(style.source).toBe('lv-map-tiles')
  expect(style['source-layer']).toBe('primary')
  expect(style.filter).toContainEqual(['==', ['boolean', ['get', '__lv_aggregate'], false], false])
  expect(style.minzoom).toBe(10)
  expect(JSON.stringify(style.paint['circle-radius'])).toContain('__lv_aggregate')
	expect(JSON.stringify(style.paint['circle-radius'])).toContain('revenue')
	expect(JSON.stringify(style.paint['circle-radius'])).not.toContain('__lv_coordinate_count')
  expect(JSON.stringify(style.paint['circle-radius'])).toContain('["boolean",["get","__lv_selected"],false]')
  expect(JSON.stringify(style.paint['circle-radius'])).toContain('500')
  expect(JSON.stringify(style.paint['circle-radius'])).toContain('5000')
	expect(JSON.stringify(style.paint['circle-color'])).toContain('__lv_aggregate')
	expect(JSON.stringify(style.paint['circle-color'])).toContain('#54aeff')
  expect(JSON.stringify(style.paint['circle-opacity'])).toContain('["boolean",["get","__lv_has_selection"],false]')
  expect(vectorTileTemplateURL(envelope.dataState.kind === 'spatial_tiled' ? envelope.dataState.tileURL : '', 'https://example.test/dashboard')).toBe('https://example.test/workspaces/sales/dashboards/orders/visuals/orders-map/tiles/revision/{z}/{x}/{y}.mvt')
})

test('MapLibre switches the complete tiled map to one precision family at the global transition', () => {
	expect(tiledRawPrecisionVisible(6.999, 7)).toBe(false)
	expect(tiledRawPrecisionVisible(7, 7)).toBe(true)
	expect(tiledRawPrecisionVisible(18, 19)).toBe(false)
})

test('MapLibre refreshes tiled paint domains when governed metadata replaces the loading placeholder', () => {
  const exact = tiledPointEnvelope()
  if (exact.dataState.kind !== 'spatial_tiled') throw new Error('tiled state fixture is unavailable')
  const loading = {
    ...exact,
    status: { kind: 'loading' },
    dataState: { ...exact.dataState, aggregateDomains: [], rawDomains: [], rawMinimumZoom: 5 },
  } as VisualizationEnvelope

  const loadingPaint = tiledLayerPaintUpdates(loading, 'lv-map-tiles')[0]?.paint
  const exactPaint = tiledLayerPaintUpdates(exact, 'lv-map-tiles')[0]?.paint
  expect(exactPaint).toBeDefined()
  expect(exactPaint).not.toEqual(loadingPaint)
  expect(JSON.stringify(loadingPaint?.['circle-radius'])).not.toContain('5000')
  expect(JSON.stringify(exactPaint?.['circle-radius'])).toContain('5000')
	expect(tiledLayerPaintUpdates(loading, 'lv-map-tiles')[0]?.minzoom).toBe(5)
	expect(tiledLayerPaintUpdates(exact, 'lv-map-tiles')[0]?.minzoom).toBe(10)
	expect(tiledLayerPaintUpdates(exact, 'lv-map-tiles').find((update) => update.id === 'lv-orders-aggregate')?.maxzoom).toBe(10)
})

test('MapLibre tiled point aggregates encode and label the authored business measure', () => {
	const envelope = tiledPointEnvelope()
	const layer = envelope.spec.kind === 'geographic' ? envelope.spec.layers[0]! : undefined
	if (!layer || layer.kind !== 'point') throw new Error('point layer fixture is unavailable')
	if (envelope.dataState.kind !== 'spatial_tiled') throw new Error('tiled state fixture is unavailable')
	const aggregate = tiledAggregatePointLayer('lv-orders-aggregate', 'lv-map-tiles', layer, envelope.dataState)
	const count = tiledAggregateCountLayer('lv-orders-aggregate-count', 'lv-map-tiles', layer, envelope.dataState)
	expect(aggregate.filter).toContainEqual(['==', ['boolean', ['get', '__lv_aggregate'], false], true])
	expect(aggregate.maxzoom).toBe(10)
	expect(count['source-layer']).toBe('primary')
	expect(count.filter).toEqual(['==', ['boolean', ['get', '__lv_aggregate'], false], true])
	expect(count.maxzoom).toBe(10)
	expect(JSON.stringify(count.layout['text-field'])).toContain('revenue')
	expect(JSON.stringify(count.layout['text-field'])).toContain('1000')
	expect(JSON.stringify(count.layout['text-field'])).toContain('k')
	expect(JSON.stringify(count.layout['text-field'])).not.toContain('__lv_coordinate_count')
	expect(count.layout['text-size']).toBe(11)
	expect(count.layout['text-allow-overlap']).toBe(true)
})

test('MapLibre tiled density blends occupied aggregate cells without changing raw heat styling', () => {
	const envelope = tiledPointEnvelope()
	if (envelope.dataState.kind !== 'spatial_tiled') throw new Error('tiled state fixture is unavailable')
	const density = {
		id: 'customers', kind: 'density',
		latitude: { dataset: 'primary', field: 'latitude' }, longitude: { dataset: 'primary', field: 'longitude' },
		value: { dataset: 'primary', field: 'revenue' }, tooltip: [], position: 'above_labels',
		visibility: { minimumZoom: 0, maximumZoom: 18 }, color: { kind: 'sequential', palette: 'blue', reverse: false, nullColor: '#d0d7de' },
		heat: { radius: 22, intensity: 1.35 }, opacity: .86,
	} as VisualizationGeographicLayer
	if (density.kind !== 'density') throw new Error('density layer fixture is unavailable')
	const raw = mapLayer('lv-customers', density, envelope.dataState, 'lv-map-tiles')
	const aggregate = tiledAggregateHeatLayer('lv-customers-aggregate', 'lv-map-tiles', density, envelope.dataState)
	expect(raw.filter).toEqual(['==', ['boolean', ['get', '__lv_aggregate'], false], false])
	expect(raw.minzoom).toBe(10)
	expect(raw.paint['heatmap-radius']).toBe(22)
	expect(aggregate.filter).toEqual(['==', ['boolean', ['get', '__lv_aggregate'], false], true])
	expect(aggregate.maxzoom).toBe(10)
	expect(aggregate.paint['heatmap-radius']).toBe(72)
	const aggregateIntensity = aggregate.paint['heatmap-intensity'] as unknown[]
	expect(aggregateIntensity.slice(0, 4)).toEqual(['interpolate', ['linear'], ['zoom'], 0])
	expect(aggregateIntensity[4] as number).toBeCloseTo(8.1)
	expect(aggregateIntensity.slice(5)).toEqual([10, 1.35])
	expect(raw.paint['heatmap-intensity']).toBe(1.35)
	expect(JSON.stringify(aggregate.paint['heatmap-weight'])).toContain('sqrt')
	expect(JSON.stringify(aggregate.paint['heatmap-weight'])).toContain('5000')
})

test('MapLibre aggregate clicks jump directly to the server refinement zoom', () => {
	expect(aggregateExpansionCamera({ __lv_west: -54, __lv_south: -18, __lv_east: -36, __lv_north: 0, __lv_target_zoom: 5 })).toEqual({ center: [-45, -9], zoom: 5 })
	expect(aggregateExpansionCamera({ __lv_west: 170, __lv_south: -10, __lv_east: -170, __lv_north: 10, __lv_target_zoom: 6 })).toEqual({ center: [-180, 0], zoom: 6 })
	expect(aggregateExpansionCamera({ __lv_west: -54, __lv_south: -18, __lv_east: -36, __lv_north: 0 })).toBeUndefined()
})

test('MapLibre tiled accessibility deduplicates visible raw and aggregate features', () => {
  const envelope = tiledPointEnvelope()
  const data = mapAccessibleRenderedFeatures(envelope, [
    { layer: { id: 'lv-orders' }, properties: { __lv_id: 'raw:11', __lv_aggregate: false, order_id: 'o1', revenue: 42, latitude: -23.5, longitude: -46.6 } },
    { layer: { id: 'lv-orders' }, properties: { __lv_id: 'raw:11', __lv_aggregate: false, order_id: 'o1', revenue: 42, latitude: -23.5, longitude: -46.6 } },
    { layer: { id: 'lv-orders' }, properties: { __lv_id: 'aggregate:2:10:7', __lv_aggregate: true, __lv_coordinate_count: 12, revenue: 500 } },
  ])
  expect(data.totalRows).toBe(15_000)
  expect(data.rows).toHaveLength(2)
  expect({ visible: data.visibleRows, raw: data.rawRows, aggregate: data.aggregateRows }).toEqual({ visible: 2, raw: 1, aggregate: 1 })
  expect(data.rows.map((row) => row[0])).toEqual(['Raw point', 'Aggregated area'])
	expect(data.columns[1]).toEqual({ id: '__lv_coordinate_count', label: 'Contained locations' })
	expect(data.rows.map((row) => row[1])).toEqual(['1', '12'])
})

test('MapLibre tiled interaction submits raw identities and never aggregate cells', () => {
  const envelope = tiledPointEnvelope()
  const raw = { layer: { id: 'lv-orders' }, properties: { __lv_id: 11, __lv_aggregate: false, order_id: 'o1' } }
  const aggregate = { layer: { id: 'lv-orders' }, properties: { __lv_id: 22, __lv_aggregate: true, order_id: 'not-a-row' } }
  expect(mapInteractionCommand(envelope, [raw], ['lv-orders'])?.mappings[0]?.value).toBe('o1')
  expect(mapInteractionCommand(envelope, [aggregate], ['lv-orders'])).toBeUndefined()
})

test('MapLibre exposes a bounded formatted tabular equivalent without unrelated fields', () => {
  const data = mapAccessibleData(selectableEnvelope(), 1)
  expect(data.totalRows).toBe(2)
  expect(data.columns.map((column) => column.label)).toEqual(['State', 'Revenue'])
  expect(data.rows).toEqual([['SP', '10']])
  expect(data.columns.some((column) => column.id === 'customer_secret')).toBe(false)
})

test('MapLibre paths group and deterministically order valid coordinates', () => {
  const envelope = selectableEnvelope()
  const path = {
    id: 'route', kind: 'path', latitude: { dataset: 'primary', field: 'lat' }, longitude: { dataset: 'primary', field: 'lon' }, path: { dataset: 'primary', field: 'state' }, order: { dataset: 'primary', field: 'value' }, value: { dataset: 'primary', field: 'value' }, tooltip: [], position: 'below_labels', visibility: { minimumZoom: 0, maximumZoom: 24 }, color: { kind: 'sequential', palette: 'blue', reverse: false, nullColor: '#ccc' }, stroke: { color: '#0969da', width: 3, opacity: 1 }, line: { width: 3, curvature: 0 }, opacity: .8,
  } as VisualizationGeographicLayer
  const withCoordinates = { ...envelope, dataState: { ...envelope.dataState, datasets: [{ ...(envelope.dataState as any).datasets[0], columns: ['state', 'value', 'lat', 'lon'], rows: [['SP', 2, -20, -40], ['SP', 1, -21, -41], ['RJ', 1, null, -42]] }] } } as VisualizationEnvelope
  const result = pathGeometry(withCoordinates, path as Extract<VisualizationGeographicLayer, { kind: 'path' }>)
  expect(result.features).toHaveLength(1)
  expect(result.features[0]?.id).toBe(0)
  expect(result.features[0]?.properties?.__lv_path).toBe('SP')
  expect(result.features[0]?.geometry).toEqual({ type: 'LineString', coordinates: [[-41, -21], [-40, -20]] })
  const style = mapLayer('lv-route', path)
  expect(JSON.stringify(style.paint['line-color'])).not.toContain('#ddf4ff')
  expect(JSON.stringify(style.paint['line-color'])).toContain('#54aeff')
  expect(style.paint['line-width']).toEqual(['interpolate', ['linear'], ['sqrt', ['get', '__lv_weight']], 0, 1.5, 1, 5])
})

test('MapLibre fits the combined valid feature extent with bounded padding and zoom', () => {
  const calls: unknown[][] = []
  const map = { fitBounds: (...args: unknown[]) => calls.push(args) }
  const data = {
    type: 'FeatureCollection',
    features: [
      { type: 'Feature', geometry: { type: 'Polygon', coordinates: [[[-74, -34], [-34, -34], [-34, 5], [-74, 5], [-74, -34]]] }, properties: {} },
      { type: 'Feature', geometry: { type: 'Point', coordinates: [-46.63, -23.55] }, properties: {} },
    ],
  } as FeatureCollection

  expect(fitMapToGeographicData(map, [data])).toBe(true)
  expect(calls).toEqual([[[[-74, -34], [-34, 5]], { padding: 24, duration: 0, maxZoom: 10 }]])
  expect(fitMapToGeographicData(map, [{ type: 'FeatureCollection', features: [] }])).toBe(false)
})

test('MapLibre reset cancels queued camera motion before restoring the home camera', () => {
  const calls: string[] = []
  const home = { center: [-46.63, -23.55] as [number, number], zoom: 5, bearing: 0, pitch: 0 }
  const map = {
    stop: () => { calls.push('stop') },
    jumpTo: (camera: unknown) => { calls.push(`jump:${JSON.stringify(camera)}`) },
  }
  resetMapToHome(map, home)
  expect(calls).toEqual(['stop', `jump:${JSON.stringify(home)}`])
})

test('MapLibre coordinate maps get a bounded geographic reference grid', () => {
  const data = {
    type: 'FeatureCollection',
    features: [
      { type: 'Feature', geometry: { type: 'Point', coordinates: [-73.9, -33.7] }, properties: {} },
      { type: 'Feature', geometry: { type: 'Point', coordinates: [-35.1, 4.2] }, properties: {} },
    ],
  } as FeatureCollection

  const grid = coordinateReferenceGrid([data])
  expect(grid.features.length).toBeGreaterThanOrEqual(8)
  expect(grid.features.every((feature) => feature.geometry.type === 'LineString')).toBe(true)
  expect(grid.features.flatMap((feature) => feature.geometry.type === 'LineString' ? feature.geometry.coordinates : [])
    .every(([longitude, latitude]) => longitude! >= -180 && longitude! <= 180 && latitude! >= -90 && latitude! <= 90)).toBe(true)
  expect(coordinateReferenceGrid([{ type: 'FeatureCollection', features: [] }]).features).toEqual([])
})

test('MapLibre heat palettes increase monotonically from transparent to dark', () => {
  for (const kind of ['heat', 'density'] as const) {
    const layer = mapLayer('observations', kind)
    expect(layer.type).toBe('heatmap')
    expect(layer.paint['heatmap-color'].slice(0, 3)).toEqual(['interpolate', ['linear'], ['heatmap-density']])
    expect(layer.paint['heatmap-color'][3]).toBe(0)
    expect(layer.paint['heatmap-color'].at(-1)).toBe('#0550ae')
  }
})

test('MapLibre translates typed palettes and heat styling without renderer options', () => {
  const choropleth = {
    id: 'states', kind: 'choropleth', color: { kind: 'sequential', palette: 'teal', reverse: false, nullColor: '#ccc' },
    stroke: { color: '#fff', width: 2, opacity: .9 }, opacity: .7,
  } as VisualizationGeographicLayer
  const fill = mapLayer('states', choropleth)
  expect(JSON.stringify(fill.paint['fill-color'])).toContain('#006d77')
  expect(fill.paint['fill-opacity']).toContain(.7)

  const heat = {
    id: 'heat', kind: 'heat', color: { kind: 'sequential', palette: 'orange', reverse: false, nullColor: '#ccc' },
    heat: { radius: 18, intensity: 1.7 }, opacity: .65,
  } as VisualizationGeographicLayer
  const translated = mapLayer('heat', heat)
  expect(translated.paint['heatmap-radius']).toBe(18)
  expect(translated.paint['heatmap-intensity']).toBe(1.7)
  expect(translated.paint['heatmap-opacity']).toBe(.65)
  expect(translated.paint['heatmap-color']).toContain('#bc4c00')
})

test('MapLibre point and region styles strongly distinguish a current selection', () => {
  const point = mapLayer('customers', 'point')
  expect(point.paint['circle-radius'][2]).toBe(13)
  expect(point.paint['circle-opacity']).toContain(0.3)
  const region = mapLayer('states', 'choropleth')
  expect(region.paint['fill-opacity']).toContain(0.4)
  expect(mapOutlineLayer('states-selected', 'states')).toMatchObject({
    source: 'states', type: 'line', filter: ['==', ['get', '__lv_selected'], true],
    paint: { 'line-color': '#bf3989', 'line-width': 3 },
  })
})

test('MapLibre renders the typed basemap below data with theme-derived land and boundaries', () => {
  expect(concreteCSSColor('', '#afb8c1')).toBe('#afb8c1')
  expect(concreteCSSColor('rgb(1, 2, 3)', '#afb8c1')).toBe('rgb(1, 2, 3)')
  expect(basemapLayer('world', { boundary: 'rgb(175, 184, 193)', land: 'rgb(234, 238, 242)' })).toEqual({
    id: 'world',
    source: 'world',
    type: 'fill',
    paint: {
      'fill-color': 'rgb(234, 238, 242)',
      'fill-opacity': 1,
    },
  })
  expect(basemapBoundaryLayer('world-boundaries', 'world', 'rgb(175, 184, 193)')).toEqual({
    id: 'world-boundaries',
    source: 'world',
    type: 'line',
    paint: {
      'line-color': 'rgb(175, 184, 193)',
      'line-opacity': 0.92,
      'line-width': 1.5,
    },
  })
})

test('MapLibre auto basemaps follow the resolved application color scheme', () => {
  expect(mapThemeColors('auto', 'dark')).toEqual(mapThemeColors('dark', 'light'))
  expect(mapThemeColors('auto', 'light')).toEqual(mapThemeColors('light', 'dark'))
  expect(mapThemeColors('auto', 'dark')).not.toEqual(mapThemeColors('auto', 'light'))
})

test('MapLibre coalesces unchanged themes and serializes WebGL style mutations by frame', async () => {
  const colors = mapThemeColors('auto', 'dark')
  expect(basemapThemeKey(colors, '#0d1821', 'normal')).toBe(basemapThemeKey({ ...colors }, '#0d1821', 'normal'))
  expect(basemapThemeKey(colors, '#0d1821', 'normal')).not.toBe(basemapThemeKey(colors, '#0d1821', 'dense'))

  const frames: Array<() => void> = []
  const schedule = createBasemapThemeScheduler((callback) => frames.push(callback))
  const applied: string[] = []
  const first = schedule(() => applied.push('first'))
  const second = schedule(() => applied.push('second'))
  await Promise.resolve()
  expect(frames).toHaveLength(1)
  frames.shift()!()
  await first
  await Promise.resolve()
  expect(frames).toHaveLength(1)
  expect(applied).toEqual(['first'])
  frames.shift()!()
  await second
  expect(applied).toEqual(['first', 'second'])
})

function tiledPointEnvelope(): VisualizationEnvelope {
  const layer = {
    id: 'orders', kind: 'point', latitude: { dataset: 'primary', field: 'latitude' }, longitude: { dataset: 'primary', field: 'longitude' }, value: { dataset: 'primary', field: 'revenue' }, label: { dataset: 'primary', field: 'order_id' }, tooltip: [{ dataset: 'primary', field: 'order_id' }, { dataset: 'primary', field: 'revenue' }], position: 'above_labels', visibility: { minimumZoom: 0, maximumZoom: 18 }, color: { kind: 'sequential', palette: 'blue', reverse: false, nullColor: '#d0d7de' }, size: { minimumRadius: 4, maximumRadius: 18 }, stroke: { color: '#fff', width: 1, opacity: 1 }, cluster: { enabled: false, radius: 40, maximumZoom: 14, minimumPoints: 2, showCount: true }, opacity: .8,
  } as VisualizationGeographicLayer
  return {
    schemaVersion: 10, visualID: 'orders-map', rendererID: 'maplibre', specRevision: 'sha256:tiles', dataRevision: 7,
    spec: {
      kind: 'geographic', title: 'Orders', datasets: [{ id: 'primary', fields: [
        { id: 'order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' },
        { id: 'latitude', role: 'dimension', dataType: 'decimal', nullable: false, label: 'Latitude' },
        { id: 'longitude', role: 'dimension', dataType: 'decimal', nullable: false, label: 'Longitude' },
        { id: 'revenue', role: 'measure', dataType: 'decimal', nullable: false, label: 'Revenue' },
      ] }],
      dataBudget: { maxRows: 0, requiredCompleteness: 'complete' }, accessibility: { title: 'Orders', description: 'Order locations' },
      interactions: [{ id: 'point_selection', kind: 'select', mode: 'single', requiresStableIdentity: true, targets: ['detail'], mappings: [{ source: { dataset: 'primary', field: 'order_id' }, targetFieldID: 'orders.order_id', targetFactID: 'orders' }] }],
      layers: [layer], spatialInteractions: [],
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, roam: true, theme: 'auto', labelDensity: 'normal', camera: { mode: 'fit_data', padding: 24, minimumZoom: 0, maximumZoom: 18 }, controls: { zoom: true, reset: true, compass: true } },
    },
    dataState: {
      kind: 'spatial_tiled', specRevision: 'sha256:tiles', dataRevision: 7, generation: 1,
      schema: { id: 'primary', fields: [
        { id: 'order_id', role: 'identity', dataType: 'string', nullable: false, label: 'Order' },
        { id: 'latitude', role: 'dimension', dataType: 'decimal', nullable: false, label: 'Latitude' },
        { id: 'longitude', role: 'dimension', dataType: 'decimal', nullable: false, label: 'Longitude' },
        { id: 'revenue', role: 'measure', dataType: 'decimal', nullable: false, label: 'Revenue' },
      ] },
      cardinality: { kind: 'exact', count: 15_000 }, extent: { west: -47, south: -24, east: -46, north: -23 },
      rawDomains: [{ field: 'revenue', minimum: 0, maximum: 500, total: 5000 }], aggregateDomains: [{ field: 'revenue', minimum: 0, maximum: 5000, total: 5000 }],
      tileURL: '/workspaces/sales/dashboards/orders/visuals/orders-map/tiles/revision/{z}/{x}/{y}.mvt', minimumZoom: 0, maximumZoom: 18, rawMinimumZoom: 10, featureCap: 5000, maximumTileBytes: 524288,
    },
    selection: [], highlights: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}

function selectableEnvelope(): VisualizationEnvelope {
  return {
    schemaVersion: 9, visualID: 'state-map', rendererID: 'maplibre', specRevision: 'sha256:test', dataRevision: 4,
    spec: {
      kind: 'geographic', title: 'States', datasets: [{ id: 'primary', fields: [
        { id: 'state', role: 'identity', dataType: 'string', nullable: false, label: 'State' },
        { id: 'value', role: 'measure', dataType: 'decimal', nullable: false, label: 'Revenue' },
        { id: 'customer_secret', role: 'dimension', dataType: 'string', nullable: true, label: 'Secret' },
      ] }],
      dataBudget: { maxRows: 100, requiredCompleteness: 'complete' }, accessibility: { title: 'States', description: 'States' },
      interactions: [{ id: 'point_selection', kind: 'select', mode: 'single', requiresStableIdentity: true, targets: ['detail'], mappings: [
        { source: { dataset: 'primary', field: 'state' }, targetFieldID: 'customers.state', targetFactID: 'customers' },
      ] }],
      layers: [{ id: 'states', kind: 'choropleth', geometry: {} as any, join: { dataset: 'primary', field: 'state' }, value: { dataset: 'primary', field: 'value' }, tooltip: [{ dataset: 'primary', field: 'state' }, { dataset: 'primary', field: 'value' }], position: 'below_labels', visibility: { minimumZoom: 0, maximumZoom: 24 }, color: { kind: 'sequential', palette: 'blue', reverse: false, nullColor: '#d0d7de' }, stroke: { color: '#fff', width: 1.5, opacity: 1 }, opacity: .82 }],
      presentation: { legend: 'hidden', labelPolicy: { density: 'hidden', priority: [], maxCharacters: 24, minimumSpacing: 0, tooltipFallback: true }, roam: false, theme: 'auto', labelDensity: 'normal', camera: { mode: 'fit_data', padding: 24, minimumZoom: 0, maximumZoom: 10 }, controls: { zoom: false, reset: false, compass: false } },
    },
    dataState: { kind: 'inline', specRevision: 'sha256:test', dataRevision: 4, generation: 1, datasets: [{
      id: 'primary', specRevision: 'sha256:test', dataRevision: 4, generation: 1, columns: ['state', 'value', 'customer_secret'], rows: [['SP', 10, 'governed-a'], ['RJ', 20, 'governed-b']], completeness: 'complete',
    }] },
    selection: [], status: { kind: 'ready' }, diagnostics: [],
  } as VisualizationEnvelope
}
