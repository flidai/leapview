import { defaultRendererContext, normalizeRendererLocale, primerCategoricalPalette, type RendererContext } from './host-controller'

/** Resolve the mounted renderer's inherited theme and authoring context. */
export function resolveHostRendererContext(host: HTMLElement, target: HTMLElement | undefined, reducedMotion: boolean): RendererContext {
  if (!target) return defaultRendererContext
  const root = host.getRootNode()
  const builderPreview = root instanceof ShadowRoot && root.host.localName === 'lv-dashboard-builder'
  const styles = getComputedStyle(target)
  const color = (name: string, fallback: string): string => styles.getPropertyValue(name).trim() || fallback
  const colorScheme = document.documentElement.style.colorScheme.trim()
  const theme = colorScheme === 'dark' || (colorScheme !== 'light' && window.matchMedia?.('(prefers-color-scheme: dark)').matches) ? 'dark' : 'light'
  return {
    locale: normalizeRendererLocale(document.documentElement.lang || 'en'),
    theme,
    echartsRenderer: builderPreview ? 'svg' : 'canvas',
    authoringPreview: builderPreview,
    reducedMotion,
    devicePixelRatio: window.devicePixelRatio || 1,
    fontFamily: styles.fontFamily || defaultRendererContext.fontFamily,
    colors: {
      foreground: color('--lv-fg-default', defaultRendererContext.colors.foreground),
      muted: color('--lv-chart-axis', defaultRendererContext.colors.muted),
      grid: color('--lv-chart-grid', defaultRendererContext.colors.grid),
      surface: color('--lv-chart-surface', defaultRendererContext.colors.surface),
      accent: color('--lv-fg-accent', defaultRendererContext.colors.accent),
      success: color('--lv-fg-success', defaultRendererContext.colors.success),
      attention: color('--lv-fg-warning', defaultRendererContext.colors.attention),
      danger: color('--lv-fg-danger', defaultRendererContext.colors.danger),
      data: primerCategoricalPalette.map(({ token }, index) => color(token, defaultRendererContext.colors.data[index]!)),
    },
  }
}
