import type { VisualizationEnvelope } from '../../../generated/visualization'

/** Forward table layout measurements through the host's content-fit contract. */
export function applyTableContentSize(host: HTMLElement, envelope: VisualizationEnvelope | undefined, event: Event, renderer: HTMLElement | undefined): void {
  if (!host.isConnected || event.currentTarget !== renderer) return
  if (!envelope || !['table', 'matrix', 'pivot'].includes(envelope.spec.kind)) return
  const { height, naturalHeight } = (event as CustomEvent<{ height: number; naturalHeight: number }>).detail
  if (!Number.isFinite(height) || height < 0 || !Number.isFinite(naturalHeight) || naturalHeight < 0) return
  event.stopPropagation()
  host.setAttribute('data-table-fit', '')
  host.style.setProperty('--lv-table-content-height', `${height}px`)
  host.dispatchEvent(new CustomEvent('lv-visualization-size-change', {
    bubbles: true, composed: true, detail: { visualID: envelope.visualID, height, naturalHeight },
  }))
}
