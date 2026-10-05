import type { MapValueRange } from './value-range'

export function rangeInput(label: string, range: MapValueRange, value: number): HTMLInputElement {
  const input = document.createElement('input')
  input.type = 'range'
  input.className = 'lv-map-range-input'
  const span = range.maximum - range.minimum
  const positions = Math.max(1, Math.floor(span / range.step))
  input.min = '0'
  input.max = String(positions)
  // Integer positions keep both endpoints reachable without native rounding.
  input.step = '1'
  input.value = String((value - range.minimum) / span * positions)
  input.setAttribute('aria-label', label)
  return input
}

// Native range inputs round very large absolute values. Store small integer
// positions in the controls and retain actual values in renderer state.
export function mapRangeInputValue(input: HTMLInputElement, range: MapValueRange): number {
  const position = Number(input.value), positions = Number(input.max)
  if (position <= 0) return range.minimum
  if (position >= positions) return range.maximum
  return range.minimum + (range.maximum - range.minimum) * position / positions
}
