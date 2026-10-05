export function createBuilderGridDragHelper(): HTMLElement {
  const helper = document.createElement('div')
  helper.className = 'builder-grid-drag-helper'
  helper.setAttribute('aria-hidden', 'true')
  Object.assign(helper.style, {
    border: '2px solid var(--lv-data-3)',
    borderRadius: 'var(--lv-radius-default)',
    background: 'transparent',
    boxShadow: 'var(--lv-shadow-floating-sm)',
    boxSizing: 'border-box',
  })
  return helper
}

export function styleBuilderGridPlaceholder(root: ShadowRoot | null): void {
  const placeholder = root?.querySelector<HTMLElement>('.grid-stack-placeholder')
  const content = placeholder?.querySelector<HTMLElement>('.placeholder-content')
  if (!placeholder || !content) return
  placeholder.setAttribute('aria-hidden', 'true')
  Object.assign(content.style, {
    position: 'absolute',
    top: 'var(--gs-item-margin-top)',
    right: 'var(--gs-item-margin-right)',
    bottom: 'var(--gs-item-margin-bottom)',
    left: 'var(--gs-item-margin-left)',
    border: '2px dashed var(--lv-data-3)',
    borderRadius: 'var(--lv-radius-default)',
    background: 'color-mix(in srgb, var(--lv-data-3-muted) 70%, transparent)',
    boxSizing: 'border-box',
  })
}
