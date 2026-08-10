import type { VisualizationEnvelope, VisualizationGeographicLayer, VisualizationGeometryAsset } from '../../../../generated/visualization'
import { Map as MapLibre, NavigationControl, type GeoJSONSource, type Map as MapLibreMap, type MapMouseEvent, type MapOptions } from 'maplibre-gl'
import type { FeatureCollection } from 'geojson'
import type { OptimisticInteractionCommand } from '../../interaction-selection'
import { Change, type RendererAdapter, type RendererContext, type RendererHandle } from '../host-controller'
import { MapSelectionControl } from './map-selection-control'
import { blankMapStyle, loadGeometryAsset, loadMapStyleAsset, registerPMTilesProtocol } from './maplibre/assets'
import { applyBasemapTheme, basemapThemeKey, createBasemapThemeScheduler, mapThemeColors, scheduleBasemapThemeMutation, type BasemapColors } from './maplibre/basemap'
import { installMapLibreChromeStyles } from './maplibre/chrome'
import { coordinateGeometry, joinGeometry, pathGeometry } from './maplibre/data'
import { applyFeatureScales, mapLayer, mapOutlineLayer, paletteColors } from './maplibre/layers'
import { clusterExpansionForRenderedFeatures, interactionCommandForRenderedFeatures, mapInteractionCommand, updateSelectionSources } from './maplibre/interactions'
import { mapAccessibleData, mapTooltipEntries, type RenderedFeatureLocator } from './maplibre/overlays'
import { emitMapObservation, installWebGLRecovery, mapNow, removeRendererFrame, waitForMapIdle, waitForMapRender, type MapObservationStage } from './maplibre/lifecycle'
import { nextSpatialRequestSequence, spatialWindowAlreadyCurrent, spatialWindowRequest, type MapSpatialWindowRequest } from './maplibre/spatial'
import { MapSpatialSelectionControl } from './maplibre/spatial-selection-control'
import { coordinateReferenceGrid, fitMapToGeographicData, resetMapToHome, type MapHomeCamera } from './maplibre/viewport'

export { loadMapStyleAsset, sameOriginGeometryURL, verifyGeometryDigest } from './maplibre/assets'
export { applyBasemapTheme, basemapBoundaryLayer, basemapLayer, basemapThemeKey, concreteCSSColor, createBasemapThemeScheduler, mapThemeColors } from './maplibre/basemap'
export { mapLibreChromeCSS } from './maplibre/chrome'
export { coordinateGeometry, joinGeometry, pathGeometry } from './maplibre/data'
export { applyFeatureScales, mapLayer, mapOutlineLayer, normalizeFeatureWeights } from './maplibre/layers'
export { clusterExpansionForRenderedFeatures, interactionCommandForRenderedFeatures, mapInteractionCommand, updateSelectionSources } from './maplibre/interactions'
export { mapAccessibleData, mapTooltipEntries } from './maplibre/overlays'
export { installWebGLRecovery, removeRendererFrame, waitForMapIdle, waitForMapRender } from './maplibre/lifecycle'
export { nextSpatialRequestSequence, spatialWindowAlreadyCurrent, spatialWindowRequest, type MapSpatialWindowRequest } from './maplibre/spatial'
export { coordinateReferenceGrid, fitMapToGeographicData, resetMapToHome } from './maplibre/viewport'

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
  private homeCamera?: { center: [number, number]; zoom: number; bearing: number; pitch: number }
  private viewportInitialized = false
  private updateQueue: Promise<void> = Promise.resolve()
  private spatialRequestSeq = 0
  private spatialRequestTimer?: number
  private lastSpatialRequest?: { key: string; at: number }
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
    this.frame.append(this.tooltip, this.legend, this.accessibleTable)
    this.map.on('click', this.handleClick)
    this.map.on('mousemove', this.handlePointerMove)
    this.map.on('mouseout', this.handlePointerLeave)
    this.map.on('moveend', this.handleMoveEnd)
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
    this.removeOwnedMapData()
    this.sourceIDs = []
    this.layerIDs = []
    this.dynamicLayers = []
    this.selectableLayerIDs = []
    this.tooltipLayerIDs = []
    this.clusterLayerIDs = []
    this.clusterSources.clear()
    this.viewportInitialized = false
    const collections: FeatureCollection[] = []
    const coordinateCollections: FeatureCollection[] = []
    const attributions = new Set<string>()
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
    this.map.off('moveend', this.handleMoveEnd)
    this.disposeWebGLRecovery()
    if (this.spatialRequestTimer !== undefined) window.clearTimeout(this.spatialRequestTimer)
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

  private updateSelectionData(envelope: VisualizationEnvelope): FeatureCollection[] {
    const result = updateSelectionSources(envelope, this.dynamicLayers, (sourceID) => this.map.getSource(sourceID) as GeoJSONSource | undefined)
    this.map.triggerRepaint()
    return result.collections
  }

  private initializeViewport(envelope: VisualizationEnvelope, collections: FeatureCollection[]): boolean {
    if (this.viewportInitialized || envelope.spec.kind !== 'geographic') return false
    const camera = envelope.spec.presentation.camera
    const fitted = fitMapToGeographicData(this.map, collections, camera)
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

  private addDataLabelLayer(sourceID: string, layer: Extract<VisualizationGeographicLayer, { kind: 'point' | 'choropleth' }>, theme: 'auto' | 'light' | 'dark'): void {
    const id = `${sourceID}-data-label`
    this.map.addLayer({ id, source: sourceID, type: 'symbol', filter: layer.kind === 'point' ? ['all', ['!', ['has', 'point_count']], ['!=', ['get', '__lv_label'], '']] : ['!=', ['get', '__lv_label'], ''], minzoom: layer.visibility.minimumZoom, maxzoom: layer.visibility.maximumZoom, layout: {
      'text-field': ['get', '__lv_label'], 'text-font': ['Noto Sans Medium'], 'text-size': 11, 'text-offset': [0, layer.kind === 'point' ? 1.25 : 0], 'text-anchor': layer.kind === 'point' ? 'top' : 'center', 'text-optional': true,
    }, paint: { 'text-color': theme === 'dark' ? '#f0f6fc' : '#1f2328', 'text-halo-color': theme === 'dark' ? '#0d1821' : '#ffffff', 'text-halo-width': 1.25 } })
    this.layerIDs.push(id)
  }

  private updateTooltip(event: MapMouseEvent, features: readonly RenderedFeatureLocator[]): void {
    if (!this.envelope) return
    const entries = mapTooltipEntries(this.envelope, features, this.context)
    if (!entries.length) { this.tooltip.hidden = true; return }
    const fragment = document.createDocumentFragment()
    for (const entry of entries) {
      const row = document.createElement('div'); row.style.cssText = 'display:grid;grid-template-columns:minmax(64px,auto) minmax(0,1fr);gap:10px'
      const label = document.createElement('span'); label.style.color = 'var(--lv-fg-muted,#57606a)'; label.textContent = entry.label
      const value = document.createElement('strong'); value.style.cssText = 'font-weight:600;text-align:right;overflow-wrap:anywhere'; value.textContent = entry.value
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
      button.style.cssText = 'position:absolute;z-index:3;top:10px;right:50px;padding:5px 8px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:4px;background:var(--lv-bg-panel,#fff);color:var(--lv-fg-default,#1f2328);font:var(--lv-type-caption);font-weight:var(--base-text-weight-semibold);cursor:pointer;box-shadow:0 1px 2px rgba(31,35,40,.08)'
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
    const summary = document.createElement('summary')
    summary.textContent = `View map data (${data.rows.length}${data.totalRows > data.rows.length ? ` of ${data.totalRows}` : ''} rows)`
    summary.style.cssText = 'padding:6px 8px;cursor:pointer;font-weight:600;white-space:nowrap'
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
    if (!this.envelope || this.envelope.dataState.kind !== 'spatial_windowed' || !this.envelope.dataState.window) return
    if (this.spatialRequestTimer !== undefined) window.clearTimeout(this.spatialRequestTimer)
    this.spatialRequestTimer = window.setTimeout(() => {
      this.spatialRequestTimer = undefined
      if (!this.envelope || this.envelope.dataState.kind !== 'spatial_windowed' || !this.envelope.dataState.window || this.disposed) return
      const bounds = this.map.getBounds()
      const requestSeq = nextSpatialRequestSequence(this.envelope, this.spatialRequestSeq)
      const request = spatialWindowRequest(this.envelope, {
        west: bounds.getWest(), south: bounds.getSouth(), east: bounds.getEast(), north: bounds.getNorth(),
      }, this.map.getZoom(), this.map.getCanvas().clientWidth, this.map.getCanvas().clientHeight, requestSeq)
      if (!request || spatialWindowAlreadyCurrent(this.envelope, request)) return
      const key = `${request.resetVersion}:${request.windowID}`
      const now = performance.now()
      if (this.lastSpatialRequest?.key === key && now - this.lastSpatialRequest.at < 500) return
      this.spatialRequestSeq = requestSeq
      this.lastSpatialRequest = { key, at: now }
      this.container.dispatchEvent(new CustomEvent('lv-visual-spatial-window-change', { bubbles: true, composed: true, detail: request }))
    }, 120)
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

function mapHomeCamera(value: unknown): MapHomeCamera | undefined {
  if (!value || typeof value !== 'object') return undefined
  const camera = value as Partial<MapHomeCamera>
  if (!Array.isArray(camera.center) || camera.center.length !== 2 || !camera.center.every((coordinate) => typeof coordinate === 'number' && Number.isFinite(coordinate))) return undefined
  if (![camera.zoom, camera.bearing, camera.pitch].every((coordinate) => typeof coordinate === 'number' && Number.isFinite(coordinate))) return undefined
  return { center: [camera.center[0]!, camera.center[1]!], zoom: camera.zoom!, bearing: camera.bearing!, pitch: camera.pitch! }
}
