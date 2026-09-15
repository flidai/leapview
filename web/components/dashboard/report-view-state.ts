export type ZoomMode = 'fit-width' | 'fit-page' | 'actual-size' | 'custom'
export type LayoutMode = 'auto' | 'desktop' | 'mobile'
export type ResolvedLayout = Exclude<LayoutMode, 'auto'>
export type PresentationMode = ZoomMode | 'mobile'

export type ZoomCommand = {
  layout?: LayoutMode
  mode?: ZoomMode
  scale?: number
}

export type ZoomState = {
  layoutMode: LayoutMode
  layout: ResolvedLayout
  mode: PresentationMode
  scale: number
}

/**
 * Resolve the dashboard-local target used for report view commands and state.
 * Controls and canvases can live in different component shadow roots, so
 * walking through shadow hosts lets both sides resolve the same marked scope.
 */
export function reportViewEventScope(element: Element): EventTarget {
  let current: Node | null = element
  while (current) {
    if (current instanceof Element) {
      const scope = current.closest('[data-report-view-scope]')
      if (scope) return scope
      current = current.parentNode
      continue
    }
    if (current instanceof ShadowRoot) {
      current = current.host
      continue
    }
    break
  }
  return element.getRootNode()
}

/**
 * Keep the legacy document event path available for one report view. If a
 * document has several scoped views, only a lone direct-document view may use
 * the fallback; otherwise an unscoped event would be ambiguous.
 */
export function reportViewDocumentFallbackAllowed(element: Element, tagName: string): boolean {
  if (typeof document === 'undefined') return false
  let total = 0
  let unscoped = 0
  const visit = (root: ParentNode): void => {
    for (const element of root.querySelectorAll('*')) {
      if (element.matches(tagName)) {
        total += 1
        if (reportViewEventScope(element) === document) unscoped += 1
      }
      if (element.shadowRoot) visit(element.shadowRoot)
    }
  }
  visit(document)
  return total === 1 || (reportViewEventScope(element) === document && unscoped === 1)
}

export const autoMobileLayoutQuery = '(max-width: 640px)'
export const reportViewPathChangedEvent = 'lv-report-view-path-change'

export function layoutStorageKey(): string {
  return `leapview-report-layout:${reportViewPathname()}`
}

export function zoomScaleStorageKey(): string {
  return `leapview-report-zoom-scale:${reportViewPathname()}`
}

export function storedZoomMode(): ZoomMode {
  try {
    const value = localStorage.getItem(zoomStorageKey())
    if (value === 'fit-width' || value === 'fit-page' || value === 'actual-size' || value === 'custom') {
      return value
    }
  } catch {
    // Ignore storage failures.
  }
  return 'fit-width'
}

export function storedLayoutMode(): LayoutMode {
  try {
    const value = localStorage.getItem(layoutStorageKey())
    if (value === 'auto' || value === 'desktop' || value === 'mobile') return value
  } catch {
    // Ignore storage failures.
  }
  return 'auto'
}

export function resolvedLayoutMode(mode: LayoutMode, mobileViewport?: boolean): ResolvedLayout {
  if (mode !== 'auto') return mode
  const mobile = mobileViewport ?? (typeof window !== 'undefined' && window.matchMedia(autoMobileLayoutQuery).matches)
  return mobile ? 'mobile' : 'desktop'
}

export function storedCustomScale(): number {
  try {
    return clampScale(Number(localStorage.getItem(zoomScaleStorageKey()) || 0.6))
  } catch {
    return 0.6
  }
}

export function clampScale(value: number): number {
  if (!Number.isFinite(value)) return 1
  return Math.min(2, Math.max(0.1, value))
}

export function clampFittedScale(value: number): number {
  if (!Number.isFinite(value)) return 1
  // Fit modes may shrink a report to its viewport, but upscaling the entire
  // surface makes DOM text and renderer canvases interpolate and look soft.
  // Users can still opt into enlargement through the explicit zoom presets.
  return Math.min(1, Math.max(0.05, value))
}

export function zoomStorageKey(): string {
  return `leapview-report-zoom:${reportViewPathname()}`
}

/** Return the route portion used to scope report-view preferences. */
export function reportViewPathname(): string {
  return typeof window !== 'undefined' ? window.location.pathname : ''
}
