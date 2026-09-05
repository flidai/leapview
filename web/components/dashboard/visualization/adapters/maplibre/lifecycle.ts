import type { Map as MapLibreMap, StyleSpecification } from 'maplibre-gl'
import type { VisualizationEnvelope } from '../../../../../generated/visualization'

export type MapObservationStage = 'basemap_load' | 'layer_shape' | 'webgl_context_loss' | 'webgl_context_restored'

export function installWebGLRecovery(
  canvas: EventTarget,
  map: Pick<MapLibreMap, 'resize' | 'triggerRepaint'>,
  observe: (stage: Extract<MapObservationStage, 'webgl_context_loss' | 'webgl_context_restored'>) => void = () => {},
): () => void {
  const lost = (event: Event) => {
    event.preventDefault()
    observe('webgl_context_loss')
  }
  const restored = () => {
    map.resize()
    map.triggerRepaint()
    observe('webgl_context_restored')
  }
  canvas.addEventListener('webglcontextlost', lost)
  canvas.addEventListener('webglcontextrestored', restored)
  return () => {
    canvas.removeEventListener('webglcontextlost', lost)
    canvas.removeEventListener('webglcontextrestored', restored)
  }
}

export function emitMapObservation(
  target: EventTarget,
  stage: MapObservationStage,
  durationMs: number,
  envelope: VisualizationEnvelope,
  detail: Readonly<Record<string, string | number>> = {},
): void {
  target.dispatchEvent(new CustomEvent('lv-map-observation', {
    bubbles: true,
    composed: true,
    detail: { stage, durationMs, visualID: envelope.visualID, rendererID: envelope.rendererID, ...detail },
  }))
}

export function mapNow(): number { return typeof performance === 'undefined' ? Date.now() : performance.now() }

export function removeRendererFrame(container: ParentNode, frame: HTMLElement): void {
  if (frame.parentNode === container) frame.remove()
}

export function waitForMapIdle(map: MapLibreMap): Promise<void> {
  return waitForMapEvent(map, ['idle'], 10_000)
}

// A renderer is ready once MapLibre has painted the configured style and data
// layers. Waiting for `idle` here is incorrect: that event also requires every
// requested basemap tile to settle, so a slow or unavailable tile can leave the
// visualization host permanently busy even though a useful frame is visible.
export function waitForMapRender(map: MapLibreMap): Promise<void> {
  return waitForMapEvent(map, ['idle', 'render'], 2_000)
}

export function setMapStyleAndWait(map: Pick<MapLibreMap, 'setStyle' | 'on' | 'off'>, style: StyleSpecification): Promise<void> {
  return new Promise((resolve, reject) => {
    let settled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const cleanup = () => {
      if (timer !== undefined) clearTimeout(timer)
      map.off('styledata', ready)
      map.off('error', fail)
      map.off('remove', removed)
    }
    const settle = (result: () => void) => {
      if (settled) return
      settled = true
      cleanup()
      result()
    }
    const ready = () => settle(resolve)
    const removed = () => settle(resolve)
    const fail = (event: { error?: unknown }) => settle(() => reject(event.error instanceof Error ? event.error : new Error('MapLibre basemap style failed to load')))
    map.on('styledata', ready)
    map.on('error', fail)
    map.on('remove', removed)
    timer = setTimeout(() => settle(() => reject(new Error('Timed out waiting for MapLibre basemap style'))), 10_000)
    try {
      map.setStyle(style, { diff: false })
    } catch (error) {
      settle(() => reject(error))
    }
  })
}

function waitForMapEvent(map: MapLibreMap, events: Array<'idle' | 'render'>, timeoutMs: number): Promise<void> {
  return new Promise((resolve) => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const finish = () => {
      if (timer !== undefined) clearTimeout(timer)
      for (const event of events) map.off(event, finish)
      resolve()
    }
    for (const event of events) map.once(event, finish)
    timer = setTimeout(finish, timeoutMs)
    map.triggerRepaint()
  })
}
