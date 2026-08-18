import type { VisualizationEnvelope, VisualizationGeographicLayer, VisualizationGeometryAsset } from '../../../../generated/visualization'
import { Map as MapLibre, NavigationControl, type GeoJSONSource, type Map as MapLibreMap, type MapMouseEvent, type MapOptions, type VectorTileSource } from 'maplibre-gl'
import type { FeatureCollection } from 'geojson'
import type { OptimisticInteractionCommand } from '../../interaction-selection'
import { Change, type RendererAdapter, type RendererContext, type RendererHandle } from '../host-controller'
import { MapSelectionControl } from './map-selection-control'
import { blankMapStyle, loadGeometryAsset, loadMapStyleAsset, registerPMTilesProtocol } from './maplibre/assets'
import { applyBasemapTheme, basemapThemeKey, createBasemapThemeScheduler, mapThemeColors, scheduleBasemapThemeMutation, type BasemapColors } from './maplibre/basemap'
import { installMapLibreChromeStyles } from './maplibre/chrome'
import { coordinateGeometry, joinGeometry, pathGeometry } from './maplibre/data'
import { applyFeatureScales, mapLayer, mapOutlineLayer, paletteColors, tiledAggregateCountLayer, tiledAggregateHeatLayer, tiledAggregatePointLayer } from './maplibre/layers'
import { clusterExpansionForRenderedFeatures, interactionCommandForRenderedFeatures, mapInteractionCommand, updateSelectionSources } from './maplibre/interactions'
import { mapAccessibleData, mapAccessibleRenderedFeatures, mapTooltipEntries, type RenderedFeatureLocator } from './maplibre/overlays'
import { emitMapObservation, installWebGLRecovery, mapNow, removeRendererFrame, waitForMapIdle, waitForMapRender, type MapObservationStage } from './maplibre/lifecycle'
import { MapSpatialSelectionControl } from './maplibre/spatial-selection-control'
import { coordinateReferenceGrid, fitMapToGeographicData, fitMapToSpatialExtent, resetMapToHome, type MapHomeCamera } from './maplibre/viewport'

export { loadMapStyleAsset, sameOriginGeometryURL, verifyGeometryDigest } from './maplibre/assets'
export { applyBasemapTheme, basemapBoundaryLayer, basemapLayer, basemapThemeKey, concreteCSSColor, createBasemapThemeScheduler, mapThemeColors } from './maplibre/basemap'
export { mapLibreChromeCSS } from './maplibre/chrome'
export { coordinateGeometry, joinGeometry, pathGeometry } from './maplibre/data'
export { applyFeatureScales, mapLayer, mapOutlineLayer, normalizeFeatureWeights, tiledAggregateCountLayer, tiledAggregateHeatLayer, tiledAggregatePointLayer } from './maplibre/layers'
export { clusterExpansionForRenderedFeatures, interactionCommandForRenderedFeatures, mapInteractionCommand, updateSelectionSources } from './maplibre/interactions'
export { mapAccessibleData, mapAccessibleRenderedFeatures, mapTooltipEntries } from './maplibre/overlays'
export { installWebGLRecovery, removeRendererFrame, waitForMapIdle, waitForMapRender } from './maplibre/lifecycle'
export { coordinateReferenceGrid, fitMapToGeographicData, resetMapToHome } from './maplibre/viewport'

export function vectorTileTemplateURL(template: string, base: string): string {
  return new URL(template, base).toString()
    .replaceAll('%7Bz%7D', '{z}')
    .replaceAll('%7Bx%7D', '{x}')
    .replaceAll('%7By%7D', '{y}')
}

export function aggregateExpansionCamera(properties: Record<string, unknown> | null | undefined): { center: [number, number]; zoom: number } | undefined {
	const west = properties?.__lv_west, south = properties?.__lv_south, east = properties?.__lv_east, north = properties?.__lv_north, targetZoom = properties?.__lv_target_zoom
	if (![west, south, east, north, targetZoom].every((value) => typeof value === 'number' && Number.isFinite(value))) return undefined
	let longitudeSpan = (east as number) - (west as number)
	if (longitudeSpan < 0) longitudeSpan += 360
	let longitude = (west as number) + longitudeSpan / 2
	if (longitude >= 180) longitude -= 360
	return { center: [longitude, ((south as number) + (north as number)) / 2], zoom: targetZoom as number }
}

function isPlaceholderTileURL(value: string): boolean {
  return value.includes('/tiles/unavailable/') || value.includes('/tiles/documentation/')
}

export const adapter: RendererAdapter = {
  async mount(container, envelope, context) {
    const frame = document.createElement('div'); frame.style.cssText = 'position:relative;width:100%;height:100%;overflow:hidden;background:var(--lv-chart-surface,var(--lv-bg-panel,#fff))'
    setRendererFramePresented(frame, false)
    installMapLibreChromeStyles(frame)
    const surface = document.createElement('div'); surface.style.cssText = 'position:absolute;inset:0'
    const attribution = document.createElement('div'); attribution.dataset.mapAttribution = ''; attribution.setAttribute('role', 'note'); attribution.setAttribute('aria-label', 'Map attribution')
    attribution.style.cssText = 'position:absolute;right:6px;bottom:6px;z-index:1;max-width:calc(100% - 12px);padding:2px 5px;border-radius:4px;background:color-mix(in srgb,var(--lv-bg-panel,#fff) 88%,transparent);color:var(--lv-fg-muted,#57606a);font:var(--lv-type-caption);pointer-events:none;text-align:right'
    frame.append(surface, attribution); container.replaceChildren(frame)
    const pointerOptions = mapPointerOptions(envelope)
    const backgroundColor = getComputedStyle(frame).backgroundColor || '#f6f8fa'
    const basemap = envelope.spec.kind === 'geographic' ? envelope.spec.presentation.basemap : undefined
    const basemapStarted = mapNow()
    const style = basemap ? await loadMapStyleAsset(basemap, location.href) : blankMapStyle(backgroundColor)
    emitMapObservation(frame, 'basemap_load', mapNow() - basemapStarted, envelope, { assetID: basemap?.id ?? 'blank' })
    registerPMTilesProtocol()
    const map = new MapLibre({
      container: surface,
      style,
      attributionControl: false,
      canvasContextAttributes: { preserveDrawingBuffer: true },
      ...pointerOptions,
    })
    await new Promise<void>((resolve) => { map.once('load', () => resolve()) })
    const handle = new MapLibreHandle(container, frame, map, attribution, context)
    try {
      await handle.update(envelope, Change.All, context)
      setRendererFramePresented(frame, true)
      return handle
    } catch (error) {
      handle.dispose()
      throw error
    }
  },
}

type RendererFramePresentationTarget = Pick<HTMLElement, 'style' | 'setAttribute' | 'removeAttribute'>
type TiledPrecisionLayerFamily = 'hidden' | 'raw' | 'aggregate'
type TiledLayerVisibilityTarget = Pick<MapLibreMap, 'getLayer' | 'setLayoutProperty'>

export function setRendererFramePresented(frame: RendererFramePresentationTarget, presented: boolean): void {
  frame.style.visibility = presented ? 'visible' : 'hidden'
  if (presented) frame.removeAttribute('aria-hidden')
  else frame.setAttribute('aria-hidden', 'true')
}

export function mapPointerOptions(envelope: VisualizationEnvelope): Pick<MapOptions, 'interactive' | 'scrollZoom' | 'boxZoom' | 'dragRotate' | 'dragPan' | 'keyboard' | 'doubleClickZoom' | 'touchZoomRotate' | 'touchPitch'> {
  const roam = envelope.spec.kind === 'geographic' ? envelope.spec.presentation.roam : false
  const selectable = envelope.spec.kind === 'geographic' && (envelope.spec.interactions.some((candidate) => candidate.kind === 'select') || envelope.spec.spatialInteractions.length > 0)
  return {
    interactive: roam || selectable,
    scrollZoom: roam,
    boxZoom: roam,
    dragRotate: roam,
    dragPan: roam,
    keyboard: roam,
    doubleClickZoom: roam,
    touchZoomRotate: roam,
    touchPitch: roam,
  }
}

class MapLibreHandle implements RendererHandle {
  private sourceIDs: string[] = []
  private layerIDs: string[] = []
  private dynamicLayers: Array<{ spec: VisualizationGeographicLayer; sourceID: string; geometry?: FeatureCollection }> = []
  private tiledSourceID?: string
  private tiledRawLayerIDs: string[] = []
  private tiledAggregateLayerIDs: string[] = []
  private tiledRawVisible?: boolean
  private tiledTileTemplate?: string
  private tiledSourceTransitioning = false
  private selectableLayerIDs: string[] = []
  private tooltipLayerIDs: string[] = []
  private clusterLayerIDs: string[] = []
  private clusterSources = new Map<string, string>()
  private envelope?: VisualizationEnvelope
  private selectionControl?: MapSelectionControl
  private spatialSelectionControl?: MapSpatialSelectionControl
  private navigationControl?: NavigationControl
  private resetButton?: HTMLButtonElement
  private readonly tooltip: HTMLDivElement
  private readonly legend: HTMLDivElement
  private readonly accessibleTable: HTMLDetailsElement
  private readonly mapError: HTMLDivElement
  private homeCamera?: { center: [number, number]; zoom: number; bearing: number; pitch: number }
  private viewportInitialized = false
  private updateQueue: Promise<void> = Promise.resolve()
  private lastBasemapThemeKey = ''
  private disposed = false
  private readonly disposeWebGLRecovery: () => void
  constructor(private readonly container: HTMLElement, private readonly frame: HTMLElement, private readonly map: MapLibreMap, private readonly attribution: HTMLElement, private context: RendererContext) {
    this.tooltip = document.createElement('div')
    this.tooltip.setAttribute('role', 'tooltip')
    this.tooltip.hidden = true
    this.tooltip.style.cssText = 'position:absolute;z-index:4;max-width:280px;padding:8px 10px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:color-mix(in srgb,var(--lv-bg-panel,#fff) 96%,transparent);box-shadow:var(--lv-shadow-floating,0 8px 24px rgba(140,149,159,.2));color:var(--lv-fg-default,#1f2328);font:var(--lv-type-caption);pointer-events:none'
    this.legend = document.createElement('div')
    this.legend.setAttribute('role', 'note')
    this.legend.dataset.mapLegend = ''
    this.legend.hidden = true
    this.legend.style.cssText = 'position:absolute;z-index:3;right:10px;bottom:28px;min-width:132px;max-width:220px;padding:8px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:color-mix(in srgb,var(--lv-bg-panel,#fff) 94%,transparent);color:var(--lv-fg-default,#1f2328);font:var(--lv-type-secondary)'
    this.accessibleTable = document.createElement('details')
    this.accessibleTable.dataset.mapDataTable = ''
    this.accessibleTable.style.cssText = 'position:absolute;z-index:3;left:10px;bottom:28px;max-width:min(520px,calc(100% - 20px));max-height:55%;overflow:auto;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:color-mix(in srgb,var(--lv-bg-panel,#fff) 96%,transparent);color:var(--lv-fg-default,#1f2328);font:var(--lv-type-secondary);box-shadow:0 1px 3px rgba(31,35,40,.12)'
    this.mapError = document.createElement('div')
    this.mapError.hidden = true
    this.mapError.style.cssText = 'position:absolute;z-index:5;left:50%;top:50%;transform:translate(-50%,-50%);max-width:min(360px,calc(100% - 32px));padding:12px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:var(--lv-bg-panel,#fff);color:var(--lv-fg-default,#1f2328);font:var(--lv-type-secondary);box-shadow:var(--lv-shadow-floating,0 8px 24px rgba(140,149,159,.2));text-align:center'
    this.frame.append(this.tooltip, this.legend, this.accessibleTable, this.mapError)
    this.map.on('click', this.handleClick)
    this.map.on('mousemove', this.handlePointerMove)
    this.map.on('mouseout', this.handlePointerLeave)
    this.map.on('zoom', this.handleZoom)
    this.map.on('moveend', this.handleMoveEnd)
    this.map.on('error', this.handleMapError)
    this.map.on('sourcedata', this.handleSourceData)
    this.disposeWebGLRecovery = installWebGLRecovery(this.map.getCanvas(), this.map, (stage) => {
      if (this.envelope) emitMapObservation(this.frame, stage, 0, this.envelope)
    })
  }
  update(envelope: VisualizationEnvelope, change: Change = Change.All, context: RendererContext = this.context): Promise<void> {
    if (this.disposed) return Promise.resolve()
    this.context = context
    const pending = this.updateQueue.then(() => this.applyUpdate(envelope, change))
    this.updateQueue = pending.catch(() => {})
    return pending
  }
  private async applyUpdate(envelope: VisualizationEnvelope, change: Change): Promise<void> {
    if (this.disposed) return
    if (envelope.spec.kind !== 'geographic') throw new Error(`MapLibre cannot render ${envelope.spec.kind}`)
    this.envelope = envelope
    this.updateAccessibleFallback(envelope)
    this.map.setMinZoom(envelope.spec.presentation.camera.minimumZoom)
    this.map.setMaxZoom(envelope.spec.presentation.camera.maximumZoom)
    await this.applyTheme()
    if (this.disposed) return
    this.updateSelectionControl(envelope)
    this.updateSpatialSelectionControl(envelope)
    if ((change & (Change.Spec | Change.Data)) === 0) {
      if ((change & Change.Selection) !== 0) this.updateSelectionData(envelope)
      return
    }
    if ((change & Change.Spec) === 0 && (change & Change.Data) !== 0 && this.dynamicLayers.length > 0) {
      const collections = this.updateSelectionData(envelope)
      const fitted = this.initializeViewport(envelope, collections)
      this.updateLegend(envelope)
      if (fitted) this.handleMoveEnd()
      await waitForMapRender(this.map)
      return
    }
    if ((change & Change.Spec) === 0 && (change & Change.Data) !== 0 && this.tiledSourceID && envelope.dataState.kind === 'spatial_tiled') {
      const tileTemplate = vectorTileTemplateURL(envelope.dataState.tileURL, location.href)
      const sourceTransition = tiledSourceTransition(this.tiledTileTemplate, tileTemplate)
      this.tiledTileTemplate = tileTemplate
      if (sourceTransition === 'replace') this.hideTiledLayersForSourceTransition()
      const source = this.map.getSource(this.tiledSourceID) as VectorTileSource | undefined
      let sourceUpdated = false
      try {
        source?.setTiles([tileTemplate])
        sourceUpdated = source !== undefined
      } catch {
        sourceUpdated = false
      }
			this.updateTiledLayerStyles(envelope)
      const sourceLifecycle = tiledSourceLifecycle(sourceTransition, sourceUpdated)
      if (sourceLifecycle === 'error') {
        this.hideTiledLayersForSourceTransition()
        this.showMapError()
      } else if (sourceLifecycle === 'stable') {
        this.tiledRawVisible = undefined
        this.syncTiledPrecisionVisibility()
        this.hideMapError()
      }
      const fitted = this.initializeViewport(envelope, [])
      this.updateLegend(envelope)
      if (fitted && sourceLifecycle !== 'error') this.handleMoveEnd()
      await waitForMapRender(this.map)
      if (sourceLifecycle === 'stable') this.updateAccessibleTiledFeatures(envelope)
      return
    }
    this.removeOwnedMapData()
    this.sourceIDs = []
    this.layerIDs = []
    this.dynamicLayers = []
    this.selectableLayerIDs = []
    this.tooltipLayerIDs = []
    this.tiledRawLayerIDs = []
    this.tiledAggregateLayerIDs = []
    this.tiledRawVisible = undefined
    this.tiledTileTemplate = undefined
    this.tiledSourceTransitioning = false
    this.clusterLayerIDs = []
    this.clusterSources.clear()
    this.tiledSourceID = undefined
    this.viewportInitialized = false
    const collections: FeatureCollection[] = []
    const coordinateCollections: FeatureCollection[] = []
    const attributions = new Set<string>()
    if (envelope.dataState.kind === 'spatial_tiled') {
      const id = `lv-${envelope.visualID}-tiles`
      const tiles = isPlaceholderTileURL(envelope.dataState.tileURL) ? [] : [vectorTileTemplateURL(envelope.dataState.tileURL, location.href)]
      this.map.addSource(id, { type: 'vector', tiles, minzoom: envelope.dataState.minimumZoom, maxzoom: envelope.dataState.maximumZoom, promoteId: '__lv_id' })
      this.sourceIDs.push(id)
      this.tiledSourceID = id
      this.tiledTileTemplate = tiles[0]
    }
    if (envelope.spec.presentation.basemap) attributions.add(envelope.spec.presentation.basemap.attribution)
    for (const layer of envelope.spec.layers) {
      const shapeStarted = mapNow()
      const collection = await this.addLayer(envelope, layer)
      emitMapObservation(this.frame, 'layer_shape', mapNow() - shapeStarted, envelope, { layerID: layer.id, featureCount: collection.features.length })
      if (this.disposed) return
      collections.push(collection)
      if (layer.kind !== 'choropleth') coordinateCollections.push(collection)
      if ('geometry' in layer && layer.geometry.attribution) attributions.add(layer.geometry.attribution)
    }
    if (!envelope.spec.presentation.basemap) this.addCoordinateReferenceGrid(coordinateCollections)
    this.attribution.textContent = [...attributions].join(' · ')
    this.attribution.hidden = attributions.size === 0
    this.initializeViewport(envelope, collections)
    this.updateMapControls(envelope)
    this.updateLegend(envelope)
    this.handleMoveEnd()
    if (this.disposed) return
    await waitForMapRender(this.map)
    this.updateAccessibleTiledFeatures(envelope)
  }
  resize(): void { this.map.resize() }
  async snapshot(): Promise<Blob> {
    await waitForMapIdle(this.map)
    const canvas = this.map.getCanvas()
    return new Promise((resolve, reject) => canvas.toBlob((blob) => blob ? resolve(blob) : reject(new Error('MapLibre snapshot failed')), 'image/png'))
  }
  captureViewState(): MapHomeCamera {
    const center = this.map.getCenter()
    return { center: [center.lng, center.lat], zoom: this.map.getZoom(), bearing: this.map.getBearing(), pitch: this.map.getPitch() }
  }
  restoreViewState(state: unknown): void {
    const camera = mapHomeCamera(state)
    if (camera) resetMapToHome(this.map, camera)
  }
  dispose(): void {
    if (this.disposed) return
    this.disposed = true
    this.map.off('click', this.handleClick)
    this.map.off('mousemove', this.handlePointerMove)
    this.map.off('mouseout', this.handlePointerLeave)
    this.map.off('zoom', this.handleZoom)
    this.map.off('moveend', this.handleMoveEnd)
    this.map.off('error', this.handleMapError)
    this.map.off('sourcedata', this.handleSourceData)
    this.disposeWebGLRecovery()
    this.selectionControl?.dispose()
    this.spatialSelectionControl?.dispose()
    if (this.navigationControl) this.map.removeControl(this.navigationControl)
    this.resetButton?.remove()
    this.map.remove()
    removeRendererFrame(this.container, this.frame)
  }

  private addCoordinateReferenceGrid(collections: FeatureCollection[]): void {
    const data = coordinateReferenceGrid(collections)
    if (data.features.length === 0) return
    let id = '__lv-coordinate-reference'
    while (this.map.getSource(id) || this.map.getLayer(id)) id += '-'
    this.map.addSource(id, { type: 'geojson', data })
    this.map.addLayer({
      id,
      source: id,
      type: 'line',
      paint: { 'line-color': '#8c959f', 'line-opacity': 0.22, 'line-width': 1, 'line-dasharray': [2, 3] },
    }, this.sourceIDs[0])
    this.sourceIDs.push(id)
    this.layerIDs.push(id)
  }

  private async addLayer(envelope: VisualizationEnvelope, layer: VisualizationGeographicLayer): Promise<FeatureCollection> {
    if (envelope.dataState.kind === 'spatial_tiled' && (layer.kind === 'point' || layer.kind === 'heat' || layer.kind === 'density')) {
      if (!this.tiledSourceID) throw new Error('spatial tiled map source is unavailable')
      const id = `lv-${layer.id}`
      const basemap = envelope.spec.kind === 'geographic' ? envelope.spec.presentation.basemap : undefined
      const before = layer.position === 'below_labels' && basemap?.labelAnchor && this.map.getLayer(basemap.labelAnchor)
        ? basemap.labelAnchor : undefined
      this.map.addLayer(mapLayer(id, layer, envelope.dataState, this.tiledSourceID), before)
      this.layerIDs.push(id)
			this.tiledRawLayerIDs.push(id)
			if (layer.kind === 'point') {
				const aggregateID = `${id}-aggregate`
				this.map.addLayer(tiledAggregatePointLayer(aggregateID, this.tiledSourceID, layer, envelope.dataState), before)
				this.layerIDs.push(aggregateID)
				this.tiledAggregateLayerIDs.push(aggregateID)
				this.selectableLayerIDs.push(aggregateID)
				if (layer.tooltip.length > 0) this.tooltipLayerIDs.push(aggregateID)
			}
			if (layer.kind === 'heat' || layer.kind === 'density') {
				const aggregateID = `${id}-aggregate`
				this.map.addLayer(tiledAggregateHeatLayer(aggregateID, this.tiledSourceID, layer, envelope.dataState), before)
				this.layerIDs.push(aggregateID)
				this.tiledAggregateLayerIDs.push(aggregateID)
				if (layer.tooltip.length > 0) this.tooltipLayerIDs.push(aggregateID)
			}
			if (layer.kind === 'point' && layer.cluster.enabled && layer.cluster.showCount) {
				const countID = `${id}-aggregate-count`
				this.map.addLayer(tiledAggregateCountLayer(countID, this.tiledSourceID, layer, envelope.dataState), before)
				this.layerIDs.push(countID)
				this.tiledAggregateLayerIDs.push(countID)
				this.selectableLayerIDs.push(countID)
			}
      if (layer.kind === 'point' && layer.label) this.tiledRawLayerIDs.push(this.addDataLabelLayer(this.tiledSourceID, layer, envelope.spec.kind === 'geographic' ? envelope.spec.presentation.theme : 'auto', true))
      if (layer.kind === 'point') this.selectableLayerIDs.push(id)
      if (layer.tooltip.length > 0) this.tooltipLayerIDs.push(id)
      return { type: 'FeatureCollection', features: [] }
    }
    let data: FeatureCollection
    let geometry: FeatureCollection | undefined
    if (layer.kind === 'choropleth') {
      if (!layer.geometry || !layer.join) throw new Error(`choropleth layer ${JSON.stringify(layer.id)} requires geometry and join`)
      geometry = await this.loadGeometry(layer.geometry)
      if (this.disposed) return { type: 'FeatureCollection', features: [] }
      data = joinGeometry(envelope, layer, geometry)
    } else if (layer.kind === 'reference') {
      geometry = await this.loadGeometry(layer.geometry)
      data = geometry
    } else if (layer.kind === 'path') {
      data = pathGeometry(envelope, layer)
    } else {
      data = coordinateGeometry(envelope, layer)
    }
    data = applyFeatureScales(data, layer)
    const id = `lv-${layer.id}`
    const sourceOptions: any = { type: 'geojson', data }
    if (layer.kind === 'point' && layer.cluster.enabled) Object.assign(sourceOptions, { cluster: true, clusterRadius: layer.cluster.radius, clusterMaxZoom: layer.cluster.maximumZoom, clusterMinPoints: layer.cluster.minimumPoints })
    this.map.addSource(id, sourceOptions)
    const before = layer.position === 'below_labels' && envelope.spec.kind === 'geographic' && envelope.spec.presentation.basemap?.labelAnchor && this.map.getLayer(envelope.spec.presentation.basemap.labelAnchor)
      ? envelope.spec.presentation.basemap.labelAnchor : undefined
    this.map.addLayer(mapLayer(id, layer), before)
    this.sourceIDs.push(id)
    this.layerIDs.push(id)
    if (layer.kind === 'reference') {
      const lineID = `${id}-line`, pointID = `${id}-point`
      this.map.addLayer({ id: lineID, source: id, type: 'line', filter: ['==', ['geometry-type'], 'LineString'], minzoom: layer.visibility.minimumZoom, maxzoom: layer.visibility.maximumZoom, paint: {
        'line-color': layer.stroke.color, 'line-width': layer.stroke.width, 'line-opacity': layer.opacity * layer.stroke.opacity,
      } }, before)
      this.map.addLayer({ id: pointID, source: id, type: 'circle', filter: ['==', ['geometry-type'], 'Point'], minzoom: layer.visibility.minimumZoom, maxzoom: layer.visibility.maximumZoom, paint: {
        'circle-color': paletteColors(layer.color)[2], 'circle-radius': Math.max(3, layer.stroke.width * 2), 'circle-opacity': layer.opacity,
        'circle-stroke-color': layer.stroke.color, 'circle-stroke-width': layer.stroke.width, 'circle-stroke-opacity': layer.stroke.opacity,
      } }, before)
      this.layerIDs.push(lineID, pointID)
    }
    if (layer.kind === 'point' && layer.cluster.enabled) this.addClusterLayers(id, layer, before)
    if (layer.label && (layer.kind === 'point' || layer.kind === 'choropleth')) this.addDataLabelLayer(id, layer, envelope.spec.kind === 'geographic' ? envelope.spec.presentation.theme : 'auto')
    if (layer.kind === 'choropleth') {
      const outlineID = `${id}-selected-outline`
      this.map.addLayer(mapOutlineLayer(outlineID, id))
      this.layerIDs.push(outlineID)
    }
    if (layer.kind === 'point' || layer.kind === 'choropleth') this.selectableLayerIDs.push(id)
    if (layer.tooltip.length > 0 && layer.kind !== 'reference') this.tooltipLayerIDs.push(id)
    this.dynamicLayers.push({ spec: layer, sourceID: id, geometry })
    return data
  }

	private updateTiledLayerStyles(envelope: VisualizationEnvelope): void {
		if (!this.tiledSourceID) return
		for (const update of tiledLayerPaintUpdates(envelope, this.tiledSourceID)) this.applyLayerStyle(update)
	}

	private applyLayerStyle(update: TiledLayerStyleUpdate): void {
		if (!this.map.getLayer(update.id)) return
		if (update.filter) this.map.setFilter(update.id, update.filter as never)
		if (update.minzoom !== undefined && update.maxzoom !== undefined) this.map.setLayerZoomRange(update.id, update.minzoom, update.maxzoom)
		for (const [property, value] of Object.entries(update.paint ?? {})) this.map.setPaintProperty(update.id, property, value)
	}

  private updateSelectionData(envelope: VisualizationEnvelope): FeatureCollection[] {
    const result = updateSelectionSources(envelope, this.dynamicLayers, (sourceID) => this.map.getSource(sourceID) as GeoJSONSource | undefined)
    this.map.triggerRepaint()
    return result.collections
  }

  private initializeViewport(envelope: VisualizationEnvelope, collections: FeatureCollection[]): boolean {
    if (this.viewportInitialized || envelope.spec.kind !== 'geographic') return false
    // Bootstrap envelopes use a world-sized placeholder extent. Deferring the
    // home camera until governed metadata arrives prevents ready tiled maps
    // from remaining at the bootstrap zoom-0 view.
    if (envelope.dataState.kind === 'spatial_tiled' && (envelope.status.kind === 'loading' || isPlaceholderTileURL(envelope.dataState.tileURL))) return false
    // The host mounts while its dashboard grid is still settling. Refresh the
    // transform from the laid-out container before calculating fitBounds;
    // otherwise MapLibre can retain its constructor-time zoom-0 dimensions.
    this.map.resize()
    const camera = envelope.spec.presentation.camera
    const fitted = envelope.dataState.kind === 'spatial_tiled'
      ? fitMapToSpatialExtent(this.map, envelope.dataState.extent, camera)
      : fitMapToGeographicData(this.map, collections, camera)
    if (!fitted && camera.mode !== 'preserve') return false
    this.viewportInitialized = true
    this.captureHomeCamera()
    return fitted
  }

  private removeOwnedMapData(): void {
    for (const id of [...this.layerIDs].reverse()) if (this.map.getLayer(id)) this.map.removeLayer(id)
    for (const id of [...this.sourceIDs].reverse()) if (this.map.getSource(id)) this.map.removeSource(id)
  }

  private updateSelectionControl(envelope: VisualizationEnvelope): void {
    const selectable = envelope.spec.interactions.some((candidate) => candidate.kind === 'select')
    if (!selectable) {
      this.selectionControl?.dispose()
      this.selectionControl = undefined
      return
    }
    this.selectionControl ??= new MapSelectionControl((command) => this.dispatchInteraction(command))
    if (!this.selectionControl.element.isConnected) this.frame.append(this.selectionControl.element)
    this.selectionControl.update(envelope)
  }

  private updateSpatialSelectionControl(envelope: VisualizationEnvelope): void {
    const selectable = envelope.spec.kind === 'geographic' && envelope.spec.spatialInteractions.length > 0
    if (!selectable) {
      this.spatialSelectionControl?.dispose()
      this.spatialSelectionControl = undefined
      return
    }
    this.spatialSelectionControl ??= new MapSpatialSelectionControl(this.map, this.frame, (command) => {
      this.container.dispatchEvent(new CustomEvent('lv-interaction-spatial-select', { bubbles: true, composed: true, detail: command }))
    })
    if (!this.spatialSelectionControl.element.isConnected) this.frame.append(this.spatialSelectionControl.element)
    this.spatialSelectionControl.update(envelope)
  }

  private addClusterLayers(sourceID: string, layer: Extract<VisualizationGeographicLayer, { kind: 'point' }>, before?: string): void {
    const clusterID = `${sourceID}-clusters`, countID = `${sourceID}-cluster-count`
    this.map.addLayer({ id: clusterID, source: sourceID, type: 'circle', filter: ['has', 'point_count'], minzoom: layer.visibility.minimumZoom, maxzoom: layer.visibility.maximumZoom, paint: {
      'circle-color': '#0969da', 'circle-opacity': 0.88, 'circle-stroke-color': layer.stroke.color, 'circle-stroke-width': Math.max(layer.stroke.width, 1.5),
      'circle-radius': ['step', ['get', 'point_count'], 14, 10, 18, 50, 23, 250, 29],
    } }, before)
    this.map.addLayer({ id: countID, source: sourceID, type: 'symbol', filter: ['has', 'point_count'], minzoom: layer.visibility.minimumZoom, maxzoom: layer.visibility.maximumZoom, layout: {
      'text-field': layer.cluster.showCount ? ['get', 'point_count_abbreviated'] : '', 'text-font': ['Noto Sans Medium'], 'text-size': 11,
    }, paint: { 'text-color': '#ffffff', 'text-halo-color': '#0550ae', 'text-halo-width': 0.5 } })
    this.layerIDs.push(clusterID, countID)
    this.clusterLayerIDs.push(countID, clusterID)
    this.clusterSources.set(clusterID, sourceID)
    this.clusterSources.set(countID, sourceID)
  }

	private addDataLabelLayer(sourceID: string, layer: Extract<VisualizationGeographicLayer, { kind: 'point' | 'choropleth' }>, theme: 'auto' | 'light' | 'dark', tiled = false): string {
    const id = `${sourceID}-data-label`
    const labelField = tiled && layer.label ? layer.label.field : '__lv_label'
    this.map.addLayer({ id, source: sourceID, ...(tiled ? { 'source-layer': 'primary' } : {}), type: 'symbol', filter: layer.kind === 'point' ? ['all', ['!', ['has', 'point_count']], ['!', ['boolean', ['get', '__lv_aggregate'], false]], ['!=', ['get', labelField], '']] : ['!=', ['get', labelField], ''], minzoom: layer.visibility.minimumZoom, maxzoom: layer.visibility.maximumZoom, layout: {
      'text-field': ['get', labelField], 'text-font': ['Noto Sans Medium'], 'text-size': 11, 'text-offset': [0, layer.kind === 'point' ? 1.25 : 0], 'text-anchor': layer.kind === 'point' ? 'top' : 'center', 'text-optional': true,
    }, paint: { 'text-color': theme === 'dark' ? '#f0f6fc' : '#1f2328', 'text-halo-color': theme === 'dark' ? '#0d1821' : '#ffffff', 'text-halo-width': 1.25 } })
    this.layerIDs.push(id)
		return id
  }

  private updateTooltip(event: MapMouseEvent, features: readonly RenderedFeatureLocator[]): void {
    if (!this.envelope) return
    const entries = mapTooltipEntries(this.envelope, features, this.context)
    if (!entries.length) { this.tooltip.hidden = true; return }
    const fragment = document.createDocumentFragment()
    for (const entry of entries) {
      const row = document.createElement('div'); row.style.cssText = 'display:grid;grid-template-columns:minmax(64px,auto) minmax(0,1fr);gap:10px'
      const label = document.createElement('span'); label.style.color = 'var(--lv-fg-muted,#57606a)'; label.textContent = entry.label
      const value = document.createElement('strong'); value.style.cssText = 'font-weight:var(--base-text-weight-semibold);text-align:right;overflow-wrap:anywhere'; value.textContent = entry.value
      row.append(label, value); fragment.append(row)
    }
    this.tooltip.replaceChildren(fragment)
    this.tooltip.hidden = false
    this.tooltip.style.left = `${Math.min(event.point.x + 12, Math.max(8, this.frame.clientWidth - 292))}px`
    this.tooltip.style.top = `${Math.min(event.point.y + 12, Math.max(8, this.frame.clientHeight - this.tooltip.offsetHeight - 8))}px`
  }

  private updateMapControls(envelope: VisualizationEnvelope): void {
    if (envelope.spec.kind !== 'geographic' || this.navigationControl || this.resetButton) return
    const controls = envelope.spec.presentation.controls
    if (controls.zoom || controls.compass) {
      this.navigationControl = new NavigationControl({ showZoom: controls.zoom, showCompass: controls.compass, visualizePitch: false })
      this.map.addControl(this.navigationControl, 'top-right')
    }
    if (controls.reset) {
      const button = document.createElement('button')
      button.type = 'button'; button.className = 'lv-map-reset'; button.textContent = 'Reset view'; button.setAttribute('aria-label', 'Reset map view')
      button.style.cssText = 'position:absolute;z-index:3;top:10px;right:50px;padding:5px 8px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:4px;background:var(--lv-bg-panel,#fff);color:var(--lv-fg-default,#1f2328);font:var(--lv-type-caption);font-weight:var(--base-text-weight-medium);cursor:pointer;box-shadow:0 1px 2px rgba(31,35,40,.08)'
      button.addEventListener('click', () => { if (this.homeCamera) resetMapToHome(this.map, this.homeCamera) })
      this.frame.append(button); this.resetButton = button
    }
  }

  private captureHomeCamera(): void {
    const center = this.map.getCenter()
    this.homeCamera = { center: [center.lng, center.lat], zoom: this.map.getZoom(), bearing: this.map.getBearing(), pitch: this.map.getPitch() }
  }

  private updateLegend(envelope: VisualizationEnvelope): void {
    if (envelope.spec.kind !== 'geographic' || envelope.spec.presentation.legend === 'hidden') { this.legend.hidden = true; return }
    const rows: HTMLElement[] = []
    for (const layer of envelope.spec.layers) {
      const value = 'value' in layer ? layer.value : undefined
      const category = 'category' in layer ? layer.category : undefined
      const field = value ?? category
      if (!field) continue
      const schema = envelope.spec.datasets.find((candidate) => candidate.id === field.dataset)
      const definition = schema?.fields.find((candidate) => candidate.id === field.field)
      const item = document.createElement('div'); item.style.cssText = 'display:grid;gap:4px;margin-bottom:7px'
      const title = document.createElement('strong'); title.textContent = definition?.label ?? field.field
      const colors = 'color' in layer ? paletteColors(layer.color) : paletteColors()
      const scale = document.createElement('span'); scale.style.cssText = `display:block;width:100%;height:8px;border-radius:999px;background:linear-gradient(90deg,${colors.join(',')})`
      item.append(title, scale); rows.push(item)
    }
    this.legend.replaceChildren(...rows); this.legend.hidden = rows.length === 0
    const position = envelope.spec.presentation.legend
    this.legend.style.left = position === 'left' ? '10px' : ''
    this.legend.style.right = position === 'right' ? '10px' : ''
    this.legend.style.top = position === 'top' ? '10px' : ''
    this.legend.style.bottom = position === 'bottom' ? '28px' : position === 'top' ? '' : '28px'
  }

  private updateAccessibleFallback(envelope: VisualizationEnvelope): void {
    const data = mapAccessibleData(envelope, 100, this.context)
    this.renderAccessibleData(envelope, data, false)
  }

  private updateAccessibleTiledFeatures(envelope: VisualizationEnvelope): void {
    if (envelope.dataState.kind !== 'spatial_tiled' || !this.tiledSourceID) return
    const layers = this.layerIDs.filter((id) => this.map.getLayer(id)?.source === this.tiledSourceID)
    const features = layers.length > 0 ? this.map.queryRenderedFeatures({ layers }) : []
    this.renderAccessibleData(envelope, mapAccessibleRenderedFeatures(envelope, features, 100, this.context), true)
  }

  private renderAccessibleData(envelope: VisualizationEnvelope, data: ReturnType<typeof mapAccessibleData> & Partial<{ visibleRows: number; aggregateRows: number; rawRows: number }>, visible: boolean): void {
    const summary = document.createElement('summary')
    if (visible) {
      const visibleRows = data.visibleRows ?? data.rows.length
      const aggregates = data.aggregateRows ?? 0
      const raw = data.rawRows ?? 0
      const precision = aggregates > 0 && raw === 0
        ? `${visibleRows} visible aggregate cells`
        : raw > 0 && aggregates === 0
          ? `${visibleRows} visible raw points`
          : `${visibleRows} visible features: ${raw} raw points, ${aggregates} aggregate cells`
      summary.textContent = `View visible map data (${precision}${data.totalRows > 0 ? `; ${data.totalRows} total coordinates` : ''})`
    } else {
      summary.textContent = `View map data (${data.rows.length}${data.totalRows > data.rows.length ? ` of ${data.totalRows}` : ''} rows)`
    }
    summary.style.cssText = 'padding:6px 8px;cursor:pointer;font-weight:var(--base-text-weight-medium);white-space:nowrap'
    const table = document.createElement('table')
    table.style.cssText = 'border-collapse:collapse;min-width:100%;background:var(--lv-bg-panel,#fff)'
    const caption = document.createElement('caption')
    caption.textContent = envelope.spec.accessibility.summary ?? envelope.spec.accessibility.description
    caption.style.cssText = 'padding:6px 8px;text-align:left;color:var(--lv-fg-muted,#57606a)'
    const header = document.createElement('tr')
    for (const column of data.columns) {
      const cell = document.createElement('th'); cell.scope = 'col'; cell.textContent = column.label
      cell.style.cssText = 'padding:5px 8px;border-top:1px solid var(--lv-line-subtle,#d8dee4);border-bottom:1px solid var(--lv-line-default,#d0d7de);text-align:left;white-space:nowrap'
      header.append(cell)
    }
    const head = document.createElement('thead'); head.append(header)
    const body = document.createElement('tbody')
    for (const row of data.rows) {
      const element = document.createElement('tr')
      for (const value of row) {
        const cell = document.createElement('td'); cell.textContent = value
        cell.style.cssText = 'padding:4px 8px;border-bottom:1px solid var(--lv-line-subtle,#d8dee4);white-space:nowrap'
        element.append(cell)
      }
      body.append(element)
    }
    table.append(caption, head, body)
    this.accessibleTable.replaceChildren(summary, table)
  }

  private dispatchInteraction(command: OptimisticInteractionCommand): void {
    this.container.dispatchEvent(new CustomEvent('lv-interaction-select', { bubbles: true, composed: true, detail: command }))
  }

  private readonly handleClick = (event: MapMouseEvent) => {
    if (!this.envelope) return
    if (this.spatialSelectionControl?.consumeClick()) return
    if (this.envelope.dataState.kind === 'spatial_tiled') {
      const features = this.selectableLayerIDs.length ? this.map.queryRenderedFeatures(event.point, { layers: this.selectableLayerIDs }) : []
      const aggregate = features.find((feature) => feature.properties?.__lv_aggregate === true)
      if (aggregate) {
				const expansion = aggregateExpansionCamera(aggregate.properties)
				if (expansion) this.map.easeTo({ ...expansion, duration: 250 })
        return
      }
    }
    const clusters = this.clusterLayerIDs.length ? this.map.queryRenderedFeatures(event.point, { layers: this.clusterLayerIDs }) : []
    const expansion = clusterExpansionForRenderedFeatures(clusters, this.clusterSources)
    if (expansion) {
      const source = this.map.getSource(expansion.sourceID) as GeoJSONSource | undefined
      void source?.getClusterExpansionZoom(expansion.clusterID).then((zoom) => this.map.easeTo({ center: expansion.center, zoom }))
      return
    }
    if (this.selectableLayerIDs.length === 0) return
    const features = this.map.queryRenderedFeatures(event.point, { layers: this.selectableLayerIDs })
    const command = mapInteractionCommand(this.envelope, features, this.selectableLayerIDs)
    if (command) this.dispatchInteraction(command)
  }

  private readonly handleMoveEnd = () => {
    if (!this.envelope || this.envelope.dataState.kind !== 'spatial_tiled') return
    this.syncTiledPrecisionVisibility()
    this.updateAccessibleTiledFeatures(this.envelope)
  }

  private readonly handleZoom = () => { this.syncTiledPrecisionVisibility() }

  private syncTiledPrecisionVisibility(): void {
    if (this.envelope?.dataState.kind !== 'spatial_tiled' || !this.tiledSourceID) return
    const family = tiledPrecisionLayerFamily(this.tiledSourceTransitioning, this.map.getZoom(), this.envelope.dataState.rawMinimumZoom)
    if (family === 'hidden') return
    const rawVisible = family === 'raw'
    if (this.tiledRawVisible === rawVisible) return
    applyTiledPrecisionLayerVisibility(this.map, this.tiledRawLayerIDs, this.tiledAggregateLayerIDs, family)
    this.tiledRawVisible = rawVisible
  }

  private readonly handlePointerMove = (event: MapMouseEvent) => {
    if (!this.envelope) return
    const layers = [...new Set([...this.selectableLayerIDs, ...this.tooltipLayerIDs])]
    if (layers.length === 0) return
    const features = this.map.queryRenderedFeatures(event.point, { layers })
    this.map.getCanvas().style.cursor = interactionCommandForRenderedFeatures(this.envelope, features, this.selectableLayerIDs) ? 'pointer' : ''
    this.updateTooltip(event, features)
  }

  private readonly handlePointerLeave = () => { this.map.getCanvas().style.cursor = ''; this.tooltip.hidden = true }

  private readonly handleMapError = (event: { sourceId?: string; error?: { message?: string } }) => {
    if (!this.tiledSourceID || this.envelope?.dataState.kind !== 'spatial_tiled') return
    if (isPlaceholderTileURL(this.envelope.dataState.tileURL)) return
    const message = event.error?.message ?? ''
    if (event.sourceId !== this.tiledSourceID && !message.includes(this.envelope.dataState.tileURL.split('/{z}')[0]!)) return
    this.showMapError()
  }

  private readonly handleSourceData = (event: { sourceId?: string; isSourceLoaded?: boolean; sourceDataType?: string }) => {
    if (event.sourceId !== this.tiledSourceID || !event.isSourceLoaded || !tiledSourceDataReady(event.sourceDataType, event.isSourceLoaded) || !this.envelope) return
    this.hideMapError()
    if (this.tiledSourceTransitioning) {
      this.tiledSourceTransitioning = false
      this.tiledRawVisible = undefined
    }
    this.syncTiledPrecisionVisibility()
    this.updateAccessibleTiledFeatures(this.envelope)
  }

  private hideTiledLayersForSourceTransition(): void {
    this.tiledSourceTransitioning = true
    applyTiledPrecisionLayerVisibility(this.map, this.tiledRawLayerIDs, this.tiledAggregateLayerIDs, 'hidden')
    this.tiledRawVisible = undefined
  }

  private retryTiledSource(): void {
    if (!this.tiledSourceID || this.envelope?.dataState.kind !== 'spatial_tiled') return
    const source = this.map.getSource(this.tiledSourceID) as VectorTileSource | undefined
    if (!source) {
      this.showMapError()
      return
    }
    this.hideTiledLayersForSourceTransition()
    try {
      source.setTiles([vectorTileTemplateURL(this.envelope.dataState.tileURL, location.href)])
    } catch {
      this.showMapError()
      return
    }
    this.map.triggerRepaint()
  }

  private showMapError(): void {
		this.mapError.setAttribute('role', 'alert')
    if (this.mapError.childElementCount === 0) {
      const message = document.createElement('div')
      message.textContent = 'Map data could not be loaded.'
      const retry = document.createElement('button')
      retry.type = 'button'; retry.textContent = 'Retry'; retry.style.cssText = 'margin-top:8px;padding:5px 9px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:4px;background:var(--lv-bg-panel,#fff);color:inherit;cursor:pointer'
      retry.addEventListener('click', () => this.retryTiledSource())
      this.mapError.append(message, retry)
    }
    this.mapError.hidden = false
  }

  private hideMapError(): void {
    this.mapError.hidden = true
		this.mapError.removeAttribute('role')
    this.mapError.replaceChildren()
  }

  private async loadGeometry(asset: VisualizationGeometryAsset): Promise<FeatureCollection> {
    return loadGeometryAsset(asset, location.href)
  }

  private async applyTheme(): Promise<void> {
    const labelDensity = this.envelope?.spec.kind === 'geographic' ? this.envelope.spec.presentation.labelDensity : 'normal'
    const colors = this.currentBasemapColors()
    const background = getComputedStyle(this.frame).backgroundColor || '#ffffff'
    const key = basemapThemeKey(colors, background, labelDensity)
    if (key === this.lastBasemapThemeKey) return
    await scheduleBasemapThemeMutation(() => {
      if (this.disposed) return
      applyBasemapTheme(this.map, colors, background, labelDensity)
      this.map.triggerRepaint()
    })
    if (!this.disposed) this.lastBasemapThemeKey = key
  }

  private currentBasemapColors(): BasemapColors {
    const theme = this.envelope?.spec.kind === 'geographic' ? this.envelope.spec.presentation.theme : 'auto'
    return mapThemeColors(theme, this.context.theme)
  }

}

type TiledLayerStyleUpdate = { id: string; paint?: Record<string, unknown>; filter?: unknown[]; minzoom?: number; maxzoom?: number }

export function tiledRawPrecisionVisible(zoom: number, rawMinimumZoom: number): boolean {
	return zoom >= rawMinimumZoom
}

/** A new tile capability is a new source generation; never reuse rendered tiles across it. */
export function tiledSourceTransition(previousTileTemplate: string | undefined, nextTileTemplate: string): 'stable' | 'replace' {
  return previousTileTemplate !== undefined && previousTileTemplate !== nextTileTemplate ? 'replace' : 'stable'
}

export function tiledSourceLifecycle(transition: 'stable' | 'replace', sourceUpdated: boolean): 'stable' | 'waiting' | 'error' {
  if (!sourceUpdated) return 'error'
  return transition === 'replace' ? 'waiting' : 'stable'
}

/** Only MapLibre's idle source event proves replacement content is settled. */
export function tiledSourceDataReady(sourceDataType: string | undefined, isSourceLoaded: boolean): boolean {
  return sourceDataType === 'idle' && isSourceLoaded
}

export function tiledPrecisionLayerFamily(transitioning: boolean, zoom: number, rawMinimumZoom: number): TiledPrecisionLayerFamily {
  if (transitioning) return 'hidden'
  return tiledRawPrecisionVisible(zoom, rawMinimumZoom) ? 'raw' : 'aggregate'
}

export function applyTiledPrecisionLayerVisibility(target: TiledLayerVisibilityTarget, rawLayerIDs: string[], aggregateLayerIDs: string[], family: TiledPrecisionLayerFamily): void {
  const rawVisible = family === 'raw'
  const aggregateVisible = family === 'aggregate'
  for (const id of rawLayerIDs) if (target.getLayer(id)) target.setLayoutProperty(id, 'visibility', rawVisible ? 'visible' : 'none')
  for (const id of aggregateLayerIDs) if (target.getLayer(id)) target.setLayoutProperty(id, 'visibility', aggregateVisible ? 'visible' : 'none')
}

export function tiledLayerPaintUpdates(envelope: VisualizationEnvelope, sourceID: string): TiledLayerStyleUpdate[] {
	if (envelope.dataState.kind !== 'spatial_tiled' || envelope.spec.kind !== 'geographic') return []
	const updates: TiledLayerStyleUpdate[] = []
	for (const layer of envelope.spec.layers) {
		if (layer.kind !== 'point' && layer.kind !== 'heat' && layer.kind !== 'density') continue
		const id = `lv-${layer.id}`
		const raw = mapLayer(id, layer, envelope.dataState, sourceID)
		updates.push({ id, paint: raw.paint, filter: raw.filter, minzoom: raw.minzoom, maxzoom: raw.maxzoom })
		if (layer.kind === 'point') {
			const aggregateID = `${id}-aggregate`
			const aggregate = tiledAggregatePointLayer(aggregateID, sourceID, layer, envelope.dataState)
			updates.push({ id: aggregateID, paint: aggregate.paint, filter: aggregate.filter, minzoom: aggregate.minzoom, maxzoom: aggregate.maxzoom })
			if (layer.cluster.enabled && layer.cluster.showCount) {
				const countID = `${id}-aggregate-count`
				const count = tiledAggregateCountLayer(countID, sourceID, layer, envelope.dataState)
				updates.push({ id: countID, filter: count.filter, minzoom: count.minzoom, maxzoom: count.maxzoom })
			}
		}
		if (layer.kind === 'heat' || layer.kind === 'density') {
			const aggregateID = `${id}-aggregate`
			const aggregate = tiledAggregateHeatLayer(aggregateID, sourceID, layer, envelope.dataState)
			updates.push({ id: aggregateID, paint: aggregate.paint, filter: aggregate.filter, minzoom: aggregate.minzoom, maxzoom: aggregate.maxzoom })
		}
	}
	return updates
}

function mapHomeCamera(value: unknown): MapHomeCamera | undefined {
  if (!value || typeof value !== 'object') return undefined
  const camera = value as Partial<MapHomeCamera>
  if (!Array.isArray(camera.center) || camera.center.length !== 2 || !camera.center.every((coordinate) => typeof coordinate === 'number' && Number.isFinite(coordinate))) return undefined
  if (![camera.zoom, camera.bearing, camera.pitch].every((coordinate) => typeof coordinate === 'number' && Number.isFinite(coordinate))) return undefined
  return { center: [camera.center[0]!, camera.center[1]!], zoom: camera.zoom!, bearing: camera.bearing!, pitch: camera.pitch! }
}
