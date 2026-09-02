import { expect, test } from 'bun:test'
import { JSDOM } from 'jsdom'

import type { VisualizationEnvelope } from '../../../../generated/visualization'
import { canonicalSpatialSelectionGeometries, MapSpatialSelectionControl } from './maplibre/spatial-selection-control'

test('MapLibre spatial controls retain every canonical geometry for one map interaction', () => {
  const box = { kind: 'box', bounds: { west: -50, south: -25, east: -40, north: -15 } } as const
  const radius = { kind: 'radius', center: { longitude: -46, latitude: -23 }, radiusMeters: 10_000 } as const
  const envelope = {
    visualID: 'map',
    highlights: [
      { sourceVisualID: 'map', interactionID: 'area', entries: [], spatialGeometry: box, label: 'Spatial selection' },
      { sourceVisualID: 'map', interactionID: 'area', entries: [], spatialGeometry: radius, label: 'Spatial selection' },
    ],
    spatialSelection: { visualID: 'map', interactionID: 'area', geometry: radius },
  } as VisualizationEnvelope

  expect(canonicalSpatialSelectionGeometries(envelope, 'area')).toEqual([box, radius])
  expect(canonicalSpatialSelectionGeometries(envelope, 'other')).toEqual([])
})

test('MapLibre spatial controls preserve the armed button and keyboard focus across envelope updates', () => {
  const dom = new JSDOM('<!doctype html><body></body>', { pretendToBeVisual: true })
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  Object.defineProperty(globalThis, 'document', { configurable: true, value: dom.window.document })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: dom.window })
  try {
    const canvas = document.createElement('canvas')
    const frame = document.createElement('div')
    frame.append(canvas)
    document.body.append(frame)
    const map = {
      getCanvas: () => canvas,
      on: () => undefined,
      off: () => undefined,
      dragPan: { isEnabled: () => true, enable: () => undefined, disable: () => undefined },
      project: () => ({ x: 0, y: 0 }),
      unproject: () => ({ lng: 0, lat: 0 }),
    }
    const envelope = {
      spec: { kind: 'geographic', spatialInteractions: [{ id: 'area', gestures: ['box', 'lasso', 'radius'] }] },
      spatialSelection: undefined,
    } as unknown as VisualizationEnvelope
    const commands: unknown[] = []
    const control = new MapSpatialSelectionControl(map as never, frame, (command) => commands.push(command))
    frame.append(control.element)
    control.update(envelope)
    const button = control.element.querySelector<HTMLButtonElement>('[aria-label="Add map area with box"]')!

    button.focus()
    expect(document.activeElement).toBe(button)
    button.click()
    expect(control.element.querySelector('[aria-label="Add map area with box"]')).toBe(button)
    expect(button.getAttribute('aria-pressed')).toBe('true')
    expect(document.activeElement).toBe(button)
    const clear = control.element.querySelector<HTMLButtonElement>('[aria-label="Clear all spatial map selections"]')!
    expect(clear.disabled).toBe(false)

    control.update({ ...envelope, status: { kind: 'loading' } } as VisualizationEnvelope)
    expect(control.element.querySelector('[aria-label="Add map area with box"]')).toBe(button)
    expect(button.getAttribute('aria-pressed')).toBe('true')
    expect(document.activeElement).toBe(button)

    clear.click()
    expect(button.getAttribute('aria-pressed')).toBe('false')
    expect(canvas.style.cursor).toBe('')
    expect(clear.disabled).toBe(true)
    expect(commands).toEqual([])

    button.click()
    document.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(button.getAttribute('aria-pressed')).toBe('false')

    button.click()
    control.setSuppressed(true)
    expect(control.element.style.display).toBe('none')
    expect(button.getAttribute('aria-pressed')).toBe('false')
    control.setSuppressed(false)
    expect(control.element.style.display).toBe('flex')
    control.dispose()
  } finally {
    Object.defineProperty(globalThis, 'document', { configurable: true, value: previousDocument })
    Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    dom.window.close()
  }
})
