export type AnchoredPopoverOptions = {
  minWidth?: number
  maxWidth?: number
  maxHeight?: number
  gap?: number
  viewportPadding?: number
}

export function toggleAnchoredPopover(
  trigger: HTMLElement,
  popover: HTMLElement,
  options: AnchoredPopoverOptions = {},
): boolean {
  if (popover.matches(':popover-open')) {
    popover.hidePopover()
    return false
  }
  const gap = options.gap ?? 4
  const padding = options.viewportPadding ?? 8
  const bounds = trigger.getBoundingClientRect()
  const scale = anchoredPopoverScale(trigger, bounds)
  const visualGap = gap * scale
  const availableWidth = Math.max(0, window.innerWidth - padding * 2) / scale
  const triggerWidth = bounds.width / scale
  const width = Math.min(
    Math.max(triggerWidth, options.minWidth ?? 240),
    options.maxWidth ?? availableWidth,
    availableWidth,
  )
  const visualWidth = width * scale
  const left = Math.max(padding, Math.min(bounds.left, window.innerWidth - visualWidth - padding))
  const availableBelow = window.innerHeight - bounds.bottom - visualGap - padding
  const availableAbove = bounds.top - visualGap - padding
  const openAbove = availableBelow < 220 * scale && availableAbove > availableBelow
  const availableHeight = (openAbove ? availableAbove : availableBelow) / scale
  const maxHeight = Math.max(160, Math.min(options.maxHeight ?? 320, availableHeight))
  const top = openAbove ? Math.max(padding, bounds.top - maxHeight * scale - visualGap) : bounds.bottom + visualGap
  Object.assign(popover.style, {
    left: `${left}px`,
    top: `${top}px`,
    width: `${width}px`,
    maxHeight: `${maxHeight}px`,
    transform: scale === 1 ? 'none' : `scale(${scale})`,
    transformOrigin: 'top left',
  })
  popover.showPopover()
  return true
}

function anchoredPopoverScale(trigger: HTMLElement, bounds: DOMRect): number {
  if (trigger.offsetWidth <= 0 || bounds.width <= 0) return 1
  const scale = bounds.width / trigger.offsetWidth
  if (!Number.isFinite(scale) || scale <= 0) return 1
  return Math.abs(scale - 1) < 0.001 ? 1 : scale
}
