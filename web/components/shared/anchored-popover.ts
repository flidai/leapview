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
  const availableWidth = Math.max(0, window.innerWidth - padding * 2)
  const width = Math.min(
    Math.max(bounds.width, options.minWidth ?? 240),
    options.maxWidth ?? availableWidth,
    availableWidth,
  )
  const left = Math.max(padding, Math.min(bounds.left, window.innerWidth - width - padding))
  const availableBelow = window.innerHeight - bounds.bottom - gap - padding
  const availableAbove = bounds.top - gap - padding
  const openAbove = availableBelow < 220 && availableAbove > availableBelow
  const availableHeight = openAbove ? availableAbove : availableBelow
  const maxHeight = Math.max(160, Math.min(options.maxHeight ?? 320, availableHeight))
  const top = openAbove ? Math.max(padding, bounds.top - maxHeight - gap) : bounds.bottom + gap
  Object.assign(popover.style, {
    left: `${left}px`,
    top: `${top}px`,
    width: `${width}px`,
    maxHeight: `${maxHeight}px`,
  })
  popover.showPopover()
  return true
}
