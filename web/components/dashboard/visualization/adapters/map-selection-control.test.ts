import { expect, test } from 'bun:test'
import { JSDOM } from 'jsdom'

import { MapSelectionControl } from './map-selection-control'

test('map selection dropdown reports state and closes when focus moves outside', () => {
  const dom = new JSDOM('<!doctype html><body></body>', { pretendToBeVisual: true })
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  Object.defineProperty(globalThis, 'document', { configurable: true, value: dom.window.document })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: dom.window })
  try {
    const states: boolean[] = []
    const control = new MapSelectionControl(() => undefined, (open) => states.push(open))
    document.body.append(control.element)
    expect(Number(control.element.style.zIndex)).toBeGreaterThan(4)
    const trigger = control.element.querySelector<HTMLButtonElement>('[data-map-selection-trigger]')!

    trigger.click()
    expect(control.open).toBe(true)
    expect(states).toEqual([true])

    document.body.dispatchEvent(new dom.window.Event('pointerdown', { bubbles: true, composed: true }))
    expect(control.open).toBe(false)
    expect(states).toEqual([true, false])
    control.dispose()
  } finally {
    Object.defineProperty(globalThis, 'document', { configurable: true, value: previousDocument })
    Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    dom.window.close()
  }
})

test('map selection dropdown activates aggregate refinement options', () => {
  const dom = new JSDOM('<!doctype html><body></body>', { pretendToBeVisual: true })
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  Object.defineProperty(globalThis, 'document', { configurable: true, value: dom.window.document })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: dom.window })
  try {
    const refinements: unknown[] = []
    const control = new MapSelectionControl(() => undefined, () => undefined, (center, zoom) => refinements.push({ center, zoom }))
    document.body.append(control.element)
    control.update({ dataState: { kind: 'spatial_tiled' }, selection: [], spec: { interactions: [] } } as any, [
      { key: 'area', label: 'Zoom to area 1 · 12.8k orders', selected: false, refinement: { center: [-45, -9], zoom: 5 } },
    ])
    const trigger = control.element.querySelector<HTMLButtonElement>('[data-map-selection-trigger]')!
    const search = control.element.querySelector<HTMLInputElement>('input[type="search"]')!
    const clear = control.element.querySelector<HTMLButtonElement>('[data-map-selection-clear]')!
    expect(trigger.querySelector('[data-map-selection-label]')?.textContent).toBe('Zoom to points')
    expect(trigger.getAttribute('aria-haspopup')).toBe('listbox')
    trigger.click()
    expect(control.element.querySelector('[role="menu"]')?.hidden).toBe(true)
    expect(control.element.querySelector<HTMLButtonElement>('[aria-label="Back to selection tools"]')?.hidden).toBe(true)
    expect(control.element.querySelector<HTMLButtonElement>('[aria-label="Back to selection tools"]')?.style.display).toBe('none')
    expect(search.hidden).toBe(true)
    expect(clear.hidden).toBe(true)
    expect(clear.style.display).toBe('none')
    expect(clear.disabled).toBe(true)
    expect(control.element.textContent).not.toContain('Choose an area to zoom in')
    expect(control.element.querySelector('[role="listbox"]')?.getAttribute('aria-label')).toBe('Map areas to zoom into')
    control.element.querySelector<HTMLElement>('[role="option"]')!.click()
    expect(refinements).toEqual([{ center: [-45, -9], zoom: 5 }])
    expect(control.open).toBe(false)
    control.dispose()
  } finally {
    Object.defineProperty(globalThis, 'document', { configurable: true, value: previousDocument })
    Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    dom.window.close()
  }
})

test('map selection dropdown marks selected data and always dispatches clear', () => {
  const dom = new JSDOM('<!doctype html><body></body>', { pretendToBeVisual: true })
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  Object.defineProperty(globalThis, 'document', { configurable: true, value: dom.window.document })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: dom.window })
  try {
    const commands: any[] = []
    const control = new MapSelectionControl((command) => commands.push(command))
    document.body.append(control.element)
    const command = {
      sourceKind: 'visual', sourceId: 'orders', interactionKind: 'point_selection',
      action: 'set', toggle: true, mappings: [{ field: 'orders.zip', value: '69307' }],
    } as const
    control.update({
      visualID: 'orders', dataState: { kind: 'spatial_tiled' }, selection: [],
      spec: { interactions: [{ id: 'point_selection', kind: 'select', mode: 'multiple' }] },
    } as any, [{ key: '69307', label: 'boa vista · 69307', selected: false, command }])

    const trigger = control.element.querySelector<HTMLButtonElement>('[data-map-selection-trigger]')!
    const clear = control.element.querySelector<HTMLButtonElement>('[data-map-selection-clear]')!
    expect(clear.disabled).toBe(true)
    expect(clear.hidden).toBe(true)
    expect(clear.style.display).toBe('none')
    expect(control.element.textContent).not.toContain('Choose one or more visible points')
    expect(control.element.querySelector('[role="listbox"]')?.getAttribute('aria-label')).toBe('Visible map points')
    trigger.click()
    control.element.querySelector<HTMLElement>('[role="option"]')!.click()
    expect(trigger.querySelector('[data-map-selection-label]')?.textContent).toBe('Points (1)')
    const selectedOption = control.element.querySelector<HTMLElement>('[role="option"]')!
    const status = control.element.querySelector<HTMLElement>('[role="status"]')!
    expect(selectedOption.getAttribute('aria-selected')).toBe('true')
    expect(selectedOption.style.background).toContain('--lv-bg-accent-muted')
    expect(selectedOption.style.color).toContain('--lv-fg-default')
    expect(status.textContent).toBe('Selected: boa vista · 69307')
    expect(status.style.background).toContain('--lv-bg-accent-muted')
    expect(status.style.color).toContain('--lv-fg-default')

    expect(clear.disabled).toBe(false)
    expect(clear.style.display).toBe('flex')
    clear.click()
    expect(commands.map((candidate) => candidate.action)).toEqual(['set', 'clear'])
    expect(trigger.querySelector('[data-map-selection-label]')?.textContent).toBe('Select points')
    expect(control.open).toBe(false)
    control.dispose()
  } finally {
    Object.defineProperty(globalThis, 'document', { configurable: true, value: previousDocument })
    Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    dom.window.close()
  }
})

test('map selection dropdown reports canonical selections that are outside the visible map', () => {
  const dom = new JSDOM('<!doctype html><body></body>', { pretendToBeVisual: true })
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  Object.defineProperty(globalThis, 'document', { configurable: true, value: dom.window.document })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: dom.window })
  try {
    const control = new MapSelectionControl(() => undefined)
    document.body.append(control.element)
    control.update({
      dataState: { kind: 'spatial_tiled' },
      selection: [{ datum: { identity: { zip: '69307' } }, label: 'boa vista · 69307' }],
      spec: { interactions: [{ id: 'point_selection', kind: 'select', mode: 'multiple' }] },
    } as any, [])
    expect(control.element.querySelector('[data-map-selection-label]')?.textContent).toBe('Points (1)')
    expect(control.element.querySelector<HTMLElement>('[role="status"]')?.textContent).toBe('1 selected map point')
    expect(control.element.querySelector<HTMLButtonElement>('[data-map-selection-clear]')?.disabled).toBe(false)
    control.dispose()
  } finally {
    Object.defineProperty(globalThis, 'document', { configurable: true, value: previousDocument })
    Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    dom.window.close()
  }
})

test('map selection dropdown combines point and spatial tools and reports the active mode', async () => {
  const dom = new JSDOM('<!doctype html><body></body>', { pretendToBeVisual: true })
  const previousDocument = globalThis.document
  const previousWindow = globalThis.window
  Object.defineProperty(globalThis, 'document', { configurable: true, value: dom.window.document })
  Object.defineProperty(globalThis, 'window', { configurable: true, value: dom.window })
  try {
    let active: 'box' | 'lasso' | 'radius' | undefined
    const toggled: string[] = []
    const spatial = {
      availableGestures: ['box', 'lasso', 'radius'],
      get activeGesture() { return active },
      selectedAreaCount: 0,
      toggleGesture(gesture: 'box' | 'lasso' | 'radius') { active = gesture; toggled.push(gesture) },
      clearSelections() { active = undefined },
    }
    const control = new MapSelectionControl(() => undefined)
    document.body.append(control.element)
    control.update({ dataState: { kind: 'spatial_tiled' }, selection: [], spec: { interactions: [] } } as any, [])
    control.setSpatialSelectionControl(spatial as any)
    const trigger = control.element.querySelector<HTMLButtonElement>('[data-map-selection-trigger]')!
    trigger.click()
    expect(document.getElementById(trigger.getAttribute('aria-controls')!)?.style.width).toBe('max-content')
    expect([...control.element.querySelectorAll('[role="menuitem"]')].map((item) => item.textContent?.trim())).toEqual([
      'Select visible points', 'Draw box selection', 'Draw lasso selection', 'Draw radius selection',
    ])
    const menuItems = [...control.element.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    trigger.focus()
    trigger.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    expect(document.activeElement).toBe(menuItems[0])
    menuItems[0]!.focus()
    control.update({ dataState: { kind: 'spatial_tiled' }, selection: [], spec: { interactions: [] } } as any, [])
    await Promise.resolve()
    const refreshedMenuItems = [...control.element.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
    expect(document.activeElement).toBe(refreshedMenuItems[0])
    refreshedMenuItems[0]!.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }))
    expect(document.activeElement).toBe(refreshedMenuItems[1])
    refreshedMenuItems[1]!.dispatchEvent(new dom.window.KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    expect(control.open).toBe(false)
    expect(document.activeElement).toBe(trigger)
    trigger.click()
    const lasso = [...control.element.querySelectorAll<HTMLButtonElement>('[role="menuitem"]')]
      .find((item) => item.textContent?.includes('Draw lasso selection'))!
    lasso.click()
    expect(toggled).toEqual(['lasso'])
    expect(control.open).toBe(false)
    expect(trigger.querySelector('[data-map-selection-label]')?.textContent).toBe('Lasso select')
    expect(trigger.querySelector('svg')).not.toBeNull()
    control.dispose()
  } finally {
    Object.defineProperty(globalThis, 'document', { configurable: true, value: previousDocument })
    Object.defineProperty(globalThis, 'window', { configurable: true, value: previousWindow })
    dom.window.close()
  }
})
