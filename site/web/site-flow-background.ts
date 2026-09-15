import { LitElement, css, html } from 'lit'
import {
  flowCoverTransform,
  flowFieldSettings,
  generateFlowLinePoints,
  type FlowPoint,
} from './site-flow-field'

type RGB = [red: number, green: number, blue: number]

class SiteFlowBackground extends LitElement {
  private canvas?: HTMLCanvasElement
  private context?: CanvasRenderingContext2D
  private resizeObserver?: ResizeObserver
  private intersectionObserver?: IntersectionObserver
  private motionQuery?: MediaQueryList
  private animationFrame?: number
  private previousTimestamp?: number
  private elapsedSeconds = 0
  private inViewport = true

  static styles = css`
    :host {
      --site-flow-line-start: var(--lv-data-1);
      --site-flow-line-end: var(--lv-data-7);
      position: absolute;
      inset-block-start: 0;
      left: 50%;
      display: block;
      width: 100%;
      height: min(100%, 62rem);
      max-width: 120rem;
      overflow: hidden;
      pointer-events: none;
      contain: strict;
      transform: translateX(-50%);
    }

    @media (max-width: 56.25rem) {
      :host {
        height: min(100%, 50rem);
      }
    }

    canvas {
      display: block;
      width: 100%;
      height: 100%;
    }

    .palette {
      position: absolute;
      width: 0;
      height: 0;
      overflow: hidden;
      visibility: hidden;
    }

    [data-color='line-start'] {
      color: var(--site-flow-line-start);
    }

    [data-color='line-end'] {
      color: var(--site-flow-line-end);
    }

  `

  connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('visibilitychange', this.handleVisibilityChange)
    document.addEventListener('leapview-theme-applied', this.handleThemeApplied)
  }

  disconnectedCallback(): void {
    document.removeEventListener('visibilitychange', this.handleVisibilityChange)
    document.removeEventListener('leapview-theme-applied', this.handleThemeApplied)
    this.motionQuery?.removeEventListener('change', this.handleMotionChange)
    this.resizeObserver?.disconnect()
    this.intersectionObserver?.disconnect()
    this.stopAnimation()
    super.disconnectedCallback()
  }

  firstUpdated(): void {
    this.canvas = this.renderRoot.querySelector('canvas') ?? undefined
    this.context = this.canvas?.getContext('2d') ?? undefined
    if (!this.canvas || !this.context) return

    this.motionQuery = window.matchMedia('(prefers-reduced-motion: reduce)')
    this.motionQuery.addEventListener('change', this.handleMotionChange)
    this.resizeObserver = new ResizeObserver(this.resize)
    this.resizeObserver.observe(this)
    this.intersectionObserver = new IntersectionObserver(this.handleIntersection, { rootMargin: '10% 0px' })
    this.intersectionObserver.observe(this)
    this.resize()
    this.syncAnimation()
  }

  render() {
    return html`
      <canvas aria-hidden="true"></canvas>
      <span class="palette" aria-hidden="true">
        <i data-color="line-start"></i>
        <i data-color="line-end"></i>
      </span>
    `
  }

  private readonly resize = (): void => {
    if (!this.canvas || !this.context) return
    const bounds = this.getBoundingClientRect()
    const dpr = Math.min(window.devicePixelRatio || 1, 2)
    const width = Math.max(1, Math.round(bounds.width * dpr))
    const height = Math.max(1, Math.round(bounds.height * dpr))
    if (this.canvas.width !== width || this.canvas.height !== height) {
      this.canvas.width = width
      this.canvas.height = height
    }
    this.draw(this.motionQuery?.matches ? 0 : this.elapsedSeconds)
  }

  private readonly handleIntersection = (entries: IntersectionObserverEntry[]): void => {
    this.inViewport = entries[0]?.isIntersecting ?? true
    this.syncAnimation()
  }

  private readonly handleVisibilityChange = (): void => this.syncAnimation()

  private readonly handleMotionChange = (): void => {
    this.previousTimestamp = undefined
    this.syncAnimation()
  }

  private readonly handleThemeApplied = (): void => this.draw(this.motionQuery?.matches ? 0 : this.elapsedSeconds)

  private syncAnimation(): void {
    if (!this.canvas || !this.context) return
    if (this.motionQuery?.matches || document.hidden || !this.inViewport) {
      this.stopAnimation()
      this.draw(this.motionQuery?.matches ? 0 : this.elapsedSeconds)
      return
    }
    if (this.animationFrame === undefined) this.animationFrame = requestAnimationFrame(this.animateFrame)
  }

  private readonly animateFrame = (timestamp: number): void => {
    if (this.previousTimestamp !== undefined) {
      this.elapsedSeconds += Math.min(timestamp - this.previousTimestamp, 50) / 1000
    }
    this.previousTimestamp = timestamp
    this.draw(this.elapsedSeconds)
    this.animationFrame = requestAnimationFrame(this.animateFrame)
  }

  private stopAnimation(): void {
    if (this.animationFrame !== undefined) cancelAnimationFrame(this.animationFrame)
    this.animationFrame = undefined
    this.previousTimestamp = undefined
  }

  private draw(time: number): void {
    if (!this.canvas || !this.context) return
    const context = this.context
    const lineStart = this.color('line-start')
    const lineEnd = this.color('line-end')
    const startRGB = this.rgb(lineStart)
    const endRGB = this.rgb(lineEnd)
    const transform = flowCoverTransform(this.canvas.width, this.canvas.height)

    context.setTransform(1, 0, 0, 1, 0, 0)
    context.clearRect(0, 0, this.canvas.width, this.canvas.height)
    context.save()
    context.setTransform(
      transform.scale,
      0,
      0,
      transform.scale,
      transform.offsetX,
      transform.offsetY,
    )
    context.lineCap = 'round'
    context.lineJoin = 'round'
    context.lineWidth = 1

    const reveal = this.motionQuery?.matches ? 1 : Math.min(1, time / 1.5)
    for (let line = 0; line < flowFieldSettings.lineCount; line++) {
      const points = generateFlowLinePoints(line, time)
      const ratio = line / Math.max(1, flowFieldSettings.lineCount - 1)
      const mixed = startRGB && endRGB ? this.mix(startRGB, endRGB, ratio) : undefined
      const solid = mixed ? this.rgba(mixed, 1) : lineStart
      const clear = mixed ? this.rgba(mixed, 0) : 'transparent'
      const first = points[0]
      const last = points.at(-1)
      if (!first || !last) continue
      const gradient = context.createLinearGradient(first.x, first.y, last.x, last.y)
      gradient.addColorStop(0, clear)
      gradient.addColorStop(flowFieldSettings.edgeFade, solid)
      gradient.addColorStop(1 - flowFieldSettings.edgeFade, solid)
      gradient.addColorStop(1, clear)
      context.strokeStyle = gradient
      context.globalAlpha = reveal
      this.strokeCurve(context, points)
    }

    context.restore()
  }

  private strokeCurve(context: CanvasRenderingContext2D, points: FlowPoint[]): void {
    const first = points[0]
    const last = points[points.length - 1]
    if (!first || !last) return
    context.beginPath()
    context.moveTo(first.x, first.y)
    for (let index = 1; index < points.length - 1; index++) {
      const point = points[index]
      const next = points[index + 1]
      if (!point || !next) continue
      context.quadraticCurveTo(point.x, point.y, (point.x + next.x) / 2, (point.y + next.y) / 2)
    }
    context.lineTo(last.x, last.y)
    context.stroke()
  }

  private color(name: string): string {
    const probe = this.renderRoot.querySelector<HTMLElement>(`[data-color='${name}']`)
    return probe ? getComputedStyle(probe).color : getComputedStyle(this).color
  }

  private rgb(color: string): RGB | undefined {
    const channels = color.match(/[\d.]+/g)?.slice(0, 3).map(Number)
    if (!channels || channels.length !== 3 || channels.some((channel) => !Number.isFinite(channel))) return undefined
    return [channels[0]!, channels[1]!, channels[2]!]
  }

  private mix(start: RGB, end: RGB, ratio: number): RGB {
    return start.map((channel, index) => Math.round(channel + (end[index]! - channel) * ratio)) as RGB
  }

  private rgba(color: RGB, alpha: number): string {
    return `rgba(${color[0]}, ${color[1]}, ${color[2]}, ${alpha})`
  }
}

if (!customElements.get('lv-site-flow-background')) {
  customElements.define('lv-site-flow-background', SiteFlowBackground)
}
