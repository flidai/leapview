export function collectVisualShowcaseMetrics(element: HTMLElement) {
  const showcase = element as HTMLElement & { shadowRoot: ShadowRoot }
  return Array.from(showcase.shadowRoot?.querySelectorAll('article') ?? []).map((card) => {
    const host = card.querySelector('lv-visualization-host') as HTMLElement & {
      envelope?: {
        visualID?: string
        spec?: { kind?: string; mark?: string; y?: Array<{ dataset: string; field: string }> }
        dataState?: { kind?: string; datasets?: Array<{ id: string; columns: string[]; rows: unknown[][] }> }
      }
      shadowRoot: ShadowRoot
    }
    const renderer = host.shadowRoot?.querySelector<HTMLElement>('.renderer')
    const canvases = Array.from(host.shadowRoot?.querySelectorAll<HTMLCanvasElement>('canvas') ?? [])
    const svgs = Array.from(host.shadowRoot?.querySelectorAll<SVGSVGElement>('.renderer svg') ?? [])
    const svgMarks = svgs.reduce((count, svg) => {
      const visibleMarks = Array.from(svg.querySelectorAll<SVGGraphicsElement>(
        'path, rect, circle, ellipse, polygon, polyline, line',
      )).filter((mark) => {
        const bounds = mark.getBoundingClientRect()
        const style = getComputedStyle(mark)
        const hasPaint = (paint: string) => paint !== 'none' && paint !== 'transparent'
          && !/rgba\([^)]*,\s*0(?:\.0+)?\)/.test(paint)
        return style.display !== 'none' && style.visibility !== 'hidden' && Number(style.opacity) > 0
          && bounds.width > 0 && bounds.height > 0 && (hasPaint(style.fill) || hasPaint(style.stroke))
      })
      return count + visibleMarks.length
    }, 0)
    const table = renderer?.querySelector<HTMLElement>('lv-report-table')
    const bounds = renderer?.getBoundingClientRect()
    let sampledPixels = 0
    let coloredPixels = 0
    for (const canvas of canvases) {
      if (sampledPixels > 10 && coloredPixels > 0) break
      const context = canvas.getContext('2d', { willReadFrequently: true })
      if (context && canvas.width > 0 && canvas.height > 0) {
        const pixels = context.getImageData(0, 0, canvas.width, canvas.height).data
        for (let index = 0; index < pixels.length; index += 4) {
          if (pixels[index + 3]! < 32) continue
          sampledPixels++
          const maximum = Math.max(pixels[index]!, pixels[index + 1]!, pixels[index + 2]!)
          const minimum = Math.min(pixels[index]!, pixels[index + 1]!, pixels[index + 2]!)
          if (maximum - minimum >= 24) {
            coloredPixels++
          }
          if (sampledPixels > 10 && coloredPixels > 0) break
        }
      }
    }
    return {
      visualID: host.envelope?.visualID,
      kind: host.envelope?.spec?.kind,
      mark: host.envelope?.spec?.mark,
      alert: host.shadowRoot?.querySelector('[role="alert"]')?.textContent?.trim() ?? '',
      width: Math.round(bounds?.width ?? 0),
      height: Math.round(bounds?.height ?? 0),
      canvasWidth: Math.max(0, ...canvases.map((canvas) => canvas.width)),
      canvasHeight: Math.max(0, ...canvases.map((canvas) => canvas.height)),
      svgWidth: Math.max(0, ...svgs.map((svg) => svg.getBoundingClientRect().width)),
      svgHeight: Math.max(0, ...svgs.map((svg) => svg.getBoundingClientRect().height)),
      svgMarks,
      sampledPixels,
      coloredPixels,
      mapFrame: host.shadowRoot?.querySelectorAll('.maplibregl-map .maplibregl-canvas').length ?? 0,
      tableText: table?.shadowRoot?.textContent?.replace(/\s+/g, ' ').trim().length ?? 0,
      rendererText: renderer?.textContent?.replace(/\s+/g, ' ').trim().length ?? 0,
    }
  })
}
