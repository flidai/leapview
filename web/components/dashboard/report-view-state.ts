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

export const autoMobileLayoutQuery = '(max-width: 640px)'

export function layoutStorageKey(): string {
  return `leapview-report-layout:${location.pathname}`
}

export function zoomScaleStorageKey(): string {
  return `leapview-report-zoom-scale:${location.pathname}`
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
  return Math.min(2, Math.max(0.05, value))
}

export function zoomStorageKey(): string {
  return `leapview-report-zoom:${location.pathname}`
}
