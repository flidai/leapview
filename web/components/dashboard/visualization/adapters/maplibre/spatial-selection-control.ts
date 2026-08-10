import type { Map as MapLibreMap } from 'maplibre-gl'
import type { VisualizationEnvelope, VisualizationSpatialSelectionGeometry, VisualizationSpatialSelectionGesture } from '../../../../../generated/visualization'
import { spatialSelectionCommand, spatialSelectionGeometry, type ScreenPoint } from './spatial-selection'

type Dispatch = (command: ReturnType<typeof spatialSelectionCommand>) => void

export class MapSpatialSelectionControl {
  readonly element: HTMLDivElement
  private readonly overlay: SVGSVGElement
  private readonly path: SVGPathElement
  private envelope?: VisualizationEnvelope
  private interactionID = ''
  private active?: VisualizationSpatialSelectionGesture
  private readonly gestureButtons = new Map<VisualizationSpatialSelectionGesture, HTMLButtonElement>()
  private clearControl?: HTMLButtonElement
  private points: ScreenPoint[] = []
  private pointerID?: number
  private restoreDragPan = false
  private suppressClick = false

  constructor(private readonly map: MapLibreMap, private readonly frame: HTMLElement, private readonly dispatch: Dispatch) {
    this.element = document.createElement('div')
    this.element.setAttribute('role', 'toolbar')
    this.element.setAttribute('aria-label', 'Spatial map selection')
    this.element.style.cssText = 'position:absolute;z-index:5;left:10px;top:50px;display:flex;gap:3px;padding:3px;border:1px solid var(--lv-line-default,#d0d7de);border-radius:6px;background:color-mix(in srgb,var(--lv-bg-panel,#fff) 96%,transparent);box-shadow:0 1px 3px rgba(31,35,40,.16)'
    this.overlay = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
    this.overlay.setAttribute('aria-hidden', 'true')
    this.overlay.style.cssText = 'position:absolute;z-index:4;inset:0;width:100%;height:100%;pointer-events:none'
    this.path = document.createElementNS('http://www.w3.org/2000/svg', 'path')
    this.path.setAttribute('fill', 'color-mix(in srgb,var(--lv-accent-emphasis,#0969da) 18%,transparent)')
    this.path.setAttribute('stroke', 'var(--lv-accent-emphasis,#0969da)')
    this.path.setAttribute('stroke-width', '2')
    this.path.setAttribute('stroke-dasharray', '5 3')
    this.path.setAttribute('vector-effect', 'non-scaling-stroke')
    this.overlay.append(this.path)
    this.frame.append(this.overlay)
    this.map.getCanvas().addEventListener('pointerdown', this.handlePointerDown, true)
    this.map.getCanvas().addEventListener('pointermove', this.handlePointerMove, true)
    this.map.getCanvas().addEventListener('pointerup', this.handlePointerUp, true)
    this.map.getCanvas().addEventListener('pointercancel', this.handlePointerCancel, true)
    this.map.on('move', this.renderCanonicalGeometry)
  }

  update(envelope: VisualizationEnvelope): void {
    this.envelope = envelope
    const interaction = envelope.spec.kind === 'geographic' ? envelope.spec.spatialInteractions[0] : undefined
    if (!interaction) {
      this.element.hidden = true
      this.setActive(undefined)
      this.renderControlState()
      this.renderCanonicalGeometry()
      return
    }
    this.element.hidden = false
    this.interactionID = interaction.id
    const activeStillAllowed = this.active && interaction.gestures.includes(this.active)
    if (!activeStillAllowed) this.setActive(undefined)
    this.ensureControls(interaction.gestures)
    this.renderControlState()
    this.renderCanonicalGeometry()
  }

  consumeClick(): boolean {
    const value = this.suppressClick
    this.suppressClick = false
    return value
  }

  dispose(): void {
    this.finishPointer()
    this.map.getCanvas().removeEventListener('pointerdown', this.handlePointerDown, true)
    this.map.getCanvas().removeEventListener('pointermove', this.handlePointerMove, true)
    this.map.getCanvas().removeEventListener('pointerup', this.handlePointerUp, true)
    this.map.getCanvas().removeEventListener('pointercancel', this.handlePointerCancel, true)
    this.map.off('move', this.renderCanonicalGeometry)
    this.overlay.remove()
    this.element.remove()
  }

  private ensureControls(gestures: readonly VisualizationSpatialSelectionGesture[]): void {
    const existing = [...this.gestureButtons.keys()]
    if (this.clearControl && existing.length === gestures.length && existing.every((gesture, index) => gesture === gestures[index])) return
    this.gestureButtons.clear()
    for (const gesture of gestures) this.gestureButtons.set(gesture, this.gestureButton(gesture))
    this.clearControl = this.clearButton()
    this.element.replaceChildren(...this.gestureButtons.values(), this.clearControl)
  }

  private gestureButton(gesture: VisualizationSpatialSelectionGesture): HTMLButtonElement {
    const button = document.createElement('button')
    button.type = 'button'
    button.textContent = gesture[0].toUpperCase() + gesture.slice(1)
    button.setAttribute('aria-label', `Select map data with ${gesture}`)
    button.addEventListener('click', (event) => {
      event.stopPropagation()
      this.setActive(this.active === gesture ? undefined : gesture)
      this.renderControlState()
    })
    return button
  }

  private clearButton(): HTMLButtonElement {
    const button = document.createElement('button')
    button.type = 'button'; button.textContent = 'Clear'; button.setAttribute('aria-label', 'Clear spatial map selection')
    button.addEventListener('click', (event) => {
      event.stopPropagation()
      if (!this.envelope || !this.envelope.spatialSelection) return
      this.dispatch(spatialSelectionCommand(this.envelope, this.interactionID, this.active ?? 'box'))
    })
    return button
  }

  private renderControlState(): void {
    for (const [gesture, button] of this.gestureButtons) {
      const active = this.active === gesture
      button.setAttribute('aria-pressed', String(active))
      button.style.cssText = this.buttonStyle(active)
    }
    if (this.clearControl) {
      this.clearControl.disabled = !this.envelope?.spatialSelection
      this.clearControl.style.cssText = this.buttonStyle(false)
    }
  }

  private buttonStyle(active: boolean): string {
    return `border:0;border-radius:4px;padding:4px 7px;background:${active ? 'var(--lv-accent-emphasis,#0969da)' : 'transparent'};color:${active ? '#fff' : 'var(--lv-fg-default,#1f2328)'};font:var(--lv-type-caption);font-weight:var(--base-text-weight-semibold);cursor:pointer`
  }

  private setActive(gesture?: VisualizationSpatialSelectionGesture): void {
    this.active = gesture
    this.map.getCanvas().style.cursor = gesture ? 'crosshair' : ''
    this.map.getCanvas().style.touchAction = gesture ? 'none' : ''
  }

  private readonly handlePointerDown = (event: PointerEvent): void => {
    if (!this.active || !this.envelope || this.pointerID !== undefined || event.button !== 0) return
    this.pointerID = event.pointerId
    this.points = [this.eventPoint(event)]
    this.restoreDragPan = this.map.dragPan.isEnabled()
    if (this.restoreDragPan) this.map.dragPan.disable()
    this.map.getCanvas().setPointerCapture?.(event.pointerId)
    event.preventDefault(); event.stopPropagation()
    this.renderDraft()
  }

  private readonly handlePointerMove = (event: PointerEvent): void => {
    if (event.pointerId !== this.pointerID || !this.active) return
    const point = this.eventPoint(event)
    if (this.active !== 'lasso') this.points = [this.points[0], point]
    else if (distance(this.points.at(-1)!, point) >= 4) this.points.push(point)
    event.preventDefault(); event.stopPropagation()
    this.renderDraft()
  }

  private readonly handlePointerUp = (event: PointerEvent): void => {
    if (event.pointerId !== this.pointerID || !this.active || !this.envelope) return
    if (this.active === 'lasso' && distance(this.points.at(-1)!, this.eventPoint(event)) >= 4) this.points.push(this.eventPoint(event))
    const geometry = spatialSelectionGeometry(this.active, this.points, (point) => {
      const coordinate = this.map.unproject([point.x, point.y])
      return { longitude: coordinate.lng, latitude: coordinate.lat }
    })
    event.preventDefault(); event.stopPropagation()
    this.suppressClick = true
    this.finishPointer()
    if (geometry) this.dispatch(spatialSelectionCommand(this.envelope, this.interactionID, this.active, geometry))
    this.renderCanonicalGeometry()
  }

  private readonly handlePointerCancel = (event: PointerEvent): void => {
    if (event.pointerId !== this.pointerID) return
    event.preventDefault(); event.stopPropagation()
    this.finishPointer()
    this.renderCanonicalGeometry()
  }

  private finishPointer(): void {
    if (this.pointerID !== undefined) this.map.getCanvas().releasePointerCapture?.(this.pointerID)
    this.pointerID = undefined
    this.points = []
    if (this.restoreDragPan) this.map.dragPan.enable()
    this.restoreDragPan = false
  }

  private eventPoint(event: PointerEvent): ScreenPoint {
    const bounds = this.map.getCanvas().getBoundingClientRect()
    return { x: event.clientX - bounds.left, y: event.clientY - bounds.top }
  }

  private renderDraft(): void {
    if (!this.active || this.points.length < 2) { this.path.setAttribute('d', ''); return }
    const start = this.points[0], end = this.points.at(-1)!
    if (this.active === 'box') {
      this.path.setAttribute('d', `M${start.x},${start.y}L${end.x},${start.y}L${end.x},${end.y}L${start.x},${end.y}Z`)
    } else if (this.active === 'radius') {
      const radius = distance(start, end)
      this.path.setAttribute('d', circlePath(start.x, start.y, radius))
    } else {
      this.path.setAttribute('d', this.points.map((point, index) => `${index ? 'L' : 'M'}${point.x},${point.y}`).join(' ') + 'Z')
    }
  }

  private readonly renderCanonicalGeometry = (): void => {
    if (this.pointerID !== undefined) return
    const geometry = this.envelope?.spatialSelection?.geometry
    this.path.setAttribute('d', geometry ? this.projectGeometry(geometry) : '')
  }

  private projectGeometry(geometry: VisualizationSpatialSelectionGeometry): string {
    if (geometry.kind === 'box') {
      const northwest = this.map.project([geometry.bounds.west, geometry.bounds.north])
      const southeast = this.map.project([geometry.bounds.east, geometry.bounds.south])
      return `M${northwest.x},${northwest.y}L${southeast.x},${northwest.y}L${southeast.x},${southeast.y}L${northwest.x},${southeast.y}Z`
    }
    if (geometry.kind === 'lasso') {
      return geometry.points.map((coordinate, index) => {
        const point = this.map.project([coordinate.longitude, coordinate.latitude])
        return `${index ? 'L' : 'M'}${point.x},${point.y}`
      }).join(' ') + 'Z'
    }
    const center = this.map.project([geometry.center.longitude, geometry.center.latitude])
    const latitudeRadians = geometry.center.latitude * Math.PI / 180
    const longitudeDelta = geometry.radiusMeters / (111_320 * Math.max(0.01, Math.cos(latitudeRadians)))
    const edge = this.map.project([Math.min(180, geometry.center.longitude + longitudeDelta), geometry.center.latitude])
    return circlePath(center.x, center.y, Math.abs(edge.x - center.x))
  }
}

function distance(left: ScreenPoint, right: ScreenPoint): number { return Math.hypot(right.x - left.x, right.y - left.y) }
function circlePath(x: number, y: number, radius: number): string {
  return radius > 0 ? `M${x - radius},${y}a${radius},${radius} 0 1,0 ${radius * 2},0a${radius},${radius} 0 1,0 ${-radius * 2},0` : ''
}
