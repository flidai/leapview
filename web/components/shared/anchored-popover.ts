export type AnchoredPopoverOptions = {
  minWidth?: number
  maxWidth?: number
  maxHeight?: number
  gap?: number
  viewportPadding?: number
  align?: 'start' | 'end'
}

const activePopovers = new WeakMap<HTMLElement, () => void>()

export function toggleAnchoredPopover(
  trigger: HTMLElement,
  popover: HTMLElement,
  options: AnchoredPopoverOptions = {},
): boolean {
  activePopovers.get(popover)?.()
  if (popover.matches(':popover-open')) {
    popover.hidePopover()
    return false
  }
  const controller = new AbortController()
  const view = trigger.ownerDocument.defaultView ?? window
  const padding = options.viewportPadding ?? 8
  let frame = 0
  const position = () => {
    if (!trigger.isConnected || !popover.isConnected) { cleanup(); return }
    const bounds = trigger.getBoundingClientRect()
    const scale = anchoredPopoverScale(trigger, bounds)
    const gap = (options.gap ?? 4) * scale
    const availableWidth = Math.max(0, view.innerWidth - padding * 2) / scale
    const width = Math.min(Math.max(bounds.width / scale, options.minWidth ?? 240), options.maxWidth ?? availableWidth, availableWidth)
    const below = Math.max(0, view.innerHeight - bounds.bottom - gap - padding)
    const above = Math.max(0, bounds.top - gap - padding)
    // Keep dropdowns below their controls whenever there is room for a few
    // choices. Scroll long lists rather than flipping based on an estimate.
    const openAbove = below < 96 * scale && above > below
    const maxHeight = Math.min(options.maxHeight ?? 320, (openAbove ? above : below) / scale)
    Object.assign(popover.style, {
      left: `${Math.max(padding, Math.min(options.align === 'end' ? bounds.right - width * scale : bounds.left, view.innerWidth - width * scale - padding))}px`,
      width: `${width}px`,
      maxHeight: `${maxHeight}px`,
      transform: scale === 1 ? 'none' : `scale(${scale})`,
      transformOrigin: 'top left',
    })
    // Measure the actual visible height, including short or asynchronously
    // loaded lists. This keeps an upward menu adjacent to its own trigger.
    const height = popover.getBoundingClientRect().height
    popover.style.top = `${openAbove ? Math.max(padding, bounds.top - height - gap) : bounds.bottom + gap}px`
  }
  const schedulePosition = () => {
    if (!frame) frame = view.requestAnimationFrame(() => { frame = 0; position() })
  }
  const observer = new ResizeObserver(schedulePosition)
  const cleanup = () => {
    controller.abort()
    observer.disconnect()
    if (frame) view.cancelAnimationFrame(frame)
    activePopovers.delete(popover)
  }
  activePopovers.set(popover, cleanup)
  // Size before showing to avoid a first-frame flash at the default position.
  position()
  popover.showPopover()
  position()
  observer.observe(popover)
  observer.observe(trigger)
  view.addEventListener('resize', schedulePosition, { signal: controller.signal })
  trigger.ownerDocument.addEventListener('scroll', schedulePosition, { capture: true, signal: controller.signal })
  popover.addEventListener('toggle', (event: Event) => {
    if ((event as Event & { newState: string }).newState === 'closed') cleanup()
  }, { signal: controller.signal })
  return true
}

function anchoredPopoverScale(trigger: HTMLElement, bounds: DOMRect): number {
  const style = getComputedStyle(trigger)
  // offsetWidth rounds to integer pixels and can mistake a fractional layout
  // width for an ancestor scale, shrinking otherwise unscaled menu rows.
  const borderBoxWidth = Number.parseFloat(style.width) + (style.boxSizing === 'border-box' ? 0 :
    Number.parseFloat(style.paddingLeft) + Number.parseFloat(style.paddingRight) +
    Number.parseFloat(style.borderLeftWidth) + Number.parseFloat(style.borderRightWidth))
  if (!Number.isFinite(borderBoxWidth) || borderBoxWidth <= 0 || bounds.width <= 0) return 1
  const scale = bounds.width / borderBoxWidth
  if (!Number.isFinite(scale) || scale <= 0) return 1
  return Math.abs(scale - 1) < 0.001 ? 1 : scale
}
