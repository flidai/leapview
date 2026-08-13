import type { Feature, FeatureCollection, Geometry, Position } from 'geojson'
import type { VisualizationMapCamera, VisualizationSpatialBounds } from '../../../../../generated/visualization'

type GeographicViewport = {
  fitBounds(bounds: [[number, number], [number, number]], options: { padding: number; duration: number; maxZoom: number }): unknown
  jumpTo?(options: { center?: [number, number]; zoom?: number }): unknown
}

export type MapHomeCamera = Readonly<{ center: [number, number]; zoom: number; bearing: number; pitch: number }>

export function resetMapToHome(map: { stop(): unknown; jumpTo(camera: MapHomeCamera): unknown }, home: MapHomeCamera): void {
  map.stop()
  map.jumpTo(home)
}

export function fitMapToGeographicData(map: GeographicViewport, collections: FeatureCollection[], camera?: VisualizationMapCamera): boolean {
  if (camera?.mode === 'preserve') return false
  if (camera?.mode === 'fixed' && camera.center && camera.center.length === 2) {
    map.jumpTo?.({ center: [camera.center[0]!, camera.center[1]!], zoom: camera.zoom })
    return true
  }
  const extent = geographicExtent(collections)
  if (!extent) return false
  let [[west, south], [east, north]] = extent
  if (west === east) { west -= 0.01; east += 0.01 }
  if (south === north) { south -= 0.01; north += 0.01 }
  map.fitBounds([[west, south], [east, north]], { padding: camera?.padding ?? 24, duration: 0, maxZoom: camera?.maximumZoom ?? 10 })
  return true
}

export function fitMapToSpatialExtent(map: GeographicViewport, extent: VisualizationSpatialBounds, camera?: VisualizationMapCamera): boolean {
	if (camera?.mode === 'preserve') return false
	if (camera?.mode === 'fixed' && camera.center && camera.center.length === 2) {
		map.jumpTo?.({ center: [camera.center[0]!, camera.center[1]!], zoom: camera.zoom })
		return true
	}
	let { west, south, east, north } = extent
	if (![west, south, east, north].every(Number.isFinite)) return false
	if (west === east) { west -= 0.01; east += 0.01 }
	if (south === north) { south -= 0.01; north += 0.01 }
	map.fitBounds([[west, south], [east, north]], { padding: camera?.padding ?? 24, duration: 0, maxZoom: camera?.maximumZoom ?? 10 })
	return true
}

export function coordinateReferenceGrid(collections: FeatureCollection[]): FeatureCollection {
  const extent = geographicExtent(collections)
  if (!extent) return { type: 'FeatureCollection', features: [] }
  const [[west, south], [east, north]] = extent
  const longitudeStep = referenceGridStep(east - west)
  const latitudeStep = referenceGridStep(north - south)
  const features: Feature<Geometry>[] = []
  for (let longitude = Math.ceil(west / longitudeStep) * longitudeStep; longitude <= east; longitude += longitudeStep) {
    features.push({ type: 'Feature', geometry: { type: 'LineString', coordinates: [[longitude, south], [longitude, north]] }, properties: {} })
  }
  for (let latitude = Math.ceil(south / latitudeStep) * latitudeStep; latitude <= north; latitude += latitudeStep) {
    features.push({ type: 'Feature', geometry: { type: 'LineString', coordinates: [[west, latitude], [east, latitude]] }, properties: {} })
  }
  return { type: 'FeatureCollection', features }
}

function referenceGridStep(span: number): number {
  const target = Math.max(span / 5, 0.0001)
  const magnitude = 10 ** Math.floor(Math.log10(target))
  const normalized = target / magnitude
  const interval = normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10
  return interval * magnitude
}

function geographicExtent(collections: FeatureCollection[]): [[number, number], [number, number]] | undefined {
  let west = Infinity, south = Infinity, east = -Infinity, north = -Infinity
  const include = (position: Position) => {
    const [longitude, latitude] = position
    if (typeof longitude !== 'number' || !Number.isFinite(longitude) || longitude < -180 || longitude > 180 || typeof latitude !== 'number' || !Number.isFinite(latitude) || latitude < -90 || latitude > 90) return
    west = Math.min(west, longitude); east = Math.max(east, longitude); south = Math.min(south, latitude); north = Math.max(north, latitude)
  }
  const coordinates = (value: unknown): void => {
    if (!Array.isArray(value)) return
    if (value.length >= 2 && typeof value[0] === 'number' && typeof value[1] === 'number') { include(value as Position); return }
    for (const child of value) coordinates(child)
  }
  const geometry = (value: Geometry | null): void => {
    if (!value) return
    if (value.type === 'GeometryCollection') { for (const child of value.geometries) geometry(child); return }
    coordinates(value.coordinates)
  }
  for (const collection of collections) for (const feature of collection.features) geometry(feature.geometry)
  return [west, south, east, north].every(Number.isFinite) ? [[west, south], [east, north]] : undefined
}
