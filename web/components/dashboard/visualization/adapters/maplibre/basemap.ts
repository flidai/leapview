import type { Map as MapLibreMap } from 'maplibre-gl'

export type BasemapColors = Readonly<{ boundary: string; land: string; background?: string; water?: string; road?: string; building?: string; label?: string }>
export type MapTheme = 'auto' | 'light' | 'dark'
export type DataLabelColors = Readonly<{ text: string; halo: string }>

type FrameScheduler = (callback: () => void) => void

export function basemapThemeKey(colors: BasemapColors, background: string, labelDensity: 'hidden' | 'normal' | 'dense'): string {
  return [colors.background, colors.land, colors.water, colors.boundary, colors.road, colors.building, colors.label, background, labelDensity].join('\u0000')
}

// Updating several MapLibre styles synchronously can overwhelm the browser's
// shared WebGL process on map-heavy dashboards. Serialize style mutations one
// animation frame at a time while allowing each map to keep its own viewport.
export function createBasemapThemeScheduler(scheduleFrame: FrameScheduler): (update: () => void) => Promise<void> {
  let tail = Promise.resolve()
  return (update) => {
    const pending = tail.then(() => new Promise<void>((resolve, reject) => {
      scheduleFrame(() => {
        try {
          update()
          resolve()
        } catch (error) {
          reject(error)
        }
      })
    }))
    tail = pending.catch(() => {})
    return pending
  }
}

export const scheduleBasemapThemeMutation = createBasemapThemeScheduler((callback) => {
  if (typeof requestAnimationFrame === 'function' && document.visibilityState !== 'hidden') {
    requestAnimationFrame(() => callback())
    return
  }
  queueMicrotask(callback)
})

export function effectiveMapTheme(theme: MapTheme, resolved: 'light' | 'dark'): 'light' | 'dark' {
  return theme === 'auto' ? resolved : theme
}

export function mapThemeColors(theme: MapTheme, resolved: 'light' | 'dark'): BasemapColors {
  const effective = effectiveMapTheme(theme, resolved)
  if (effective === 'dark') return { background: '#0d1821', land: '#18232d', water: '#0d1821', boundary: '#657383', road: '#394957', building: '#263540', label: '#d6dee6' }
  return { background: '#aad3df', land: '#f4f1ea', water: '#aad3df', boundary: '#8f918d', road: '#ffffff', building: '#dedbd4', label: '#4b4d49' }
}

export function mapDataLabelColors(theme: MapTheme, resolved: 'light' | 'dark'): DataLabelColors {
  return effectiveMapTheme(theme, resolved) === 'dark'
    ? { text: '#f0f6fc', halo: '#0d1821' }
    : { text: '#1f2328', halo: '#ffffff' }
}

export function basemapLayer(id: string, colors: BasemapColors): any {
  return { id, source: id, type: 'fill', paint: { 'fill-color': colors.land, 'fill-opacity': 1 } }
}

export function basemapBoundaryLayer(id: string, source: string, boundary: string): any {
  return { id, source, type: 'line', paint: { 'line-color': boundary, 'line-opacity': 0.92, 'line-width': 1.5 } }
}

export function concreteCSSColor(resolved: string, fallback: string): string {
  return resolved.trim() || fallback
}

export function applyBasemapTheme(map: Pick<MapLibreMap, 'getStyle' | 'getLayer' | 'setPaintProperty' | 'setLayoutProperty'>, colors: BasemapColors, background: string, labelDensity: 'hidden' | 'normal' | 'dense' = 'normal'): void {
  for (const layer of map.getStyle().layers ?? []) {
    if (!map.getLayer(layer.id)) continue
    const role = (layer.metadata as Record<string, unknown> | undefined)?.['leapview:role']
    if (role === 'background' && layer.type === 'background') map.setPaintProperty(layer.id, 'background-color', colors.background ?? background)
    if (role === 'land' && layer.type === 'fill') map.setPaintProperty(layer.id, 'fill-color', colors.land)
    if (role === 'water' && layer.type === 'fill') map.setPaintProperty(layer.id, 'fill-color', colors.water ?? '#cce8f7')
    if (role === 'water' && layer.type === 'line') map.setPaintProperty(layer.id, 'line-color', colors.water ?? '#7bb9dc')
    if (role === 'boundary' && layer.type === 'line') map.setPaintProperty(layer.id, 'line-color', colors.boundary)
    if (role === 'road' && layer.type === 'line') map.setPaintProperty(layer.id, 'line-color', colors.road ?? '#ffffff')
    if (role === 'building' && layer.type === 'fill') map.setPaintProperty(layer.id, 'fill-color', colors.building ?? '#d8dee4')
    if (role === 'label' && layer.type === 'symbol') {
      map.setLayoutProperty(layer.id, 'visibility', labelVisibility(layer.id, labelDensity))
      map.setPaintProperty(layer.id, 'text-color', colors.label ?? '#57606a')
      map.setPaintProperty(layer.id, 'text-halo-color', colors.land)
    }
  }
}

function labelVisibility(id: string, density: 'hidden' | 'normal' | 'dense'): 'none' | 'visible' {
  if (density === 'hidden') return 'none'
  if (density === 'dense') return 'visible'
  return /^(address_label|pois|places_subplace|roads_labels_minor)$/.test(id) ? 'none' : 'visible'
}

export function applyDataLabelTheme(map: Pick<MapLibreMap, 'getLayer' | 'setPaintProperty'>, layerIDs: readonly string[], colors: DataLabelColors): void {
  for (const id of layerIDs) {
    if (!map.getLayer(id)) continue
    map.setPaintProperty(id, 'text-color', colors.text)
    map.setPaintProperty(id, 'text-halo-color', colors.halo)
  }
}
