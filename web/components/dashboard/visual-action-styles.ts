import { css } from 'lit'

const visualMenuViewportGap = 56

export function resetVisualOptionsMenuPlacement(details: HTMLDetailsElement): void {
  const menu = details.querySelector<HTMLElement>('.menu')
  details.removeAttribute('data-menu-open-up')
  details.style.removeProperty('--lv-visual-menu-max-height')
  menu?.style.removeProperty('top')
  menu?.style.removeProperty('bottom')
}

export function positionVisualOptionsMenu(details: HTMLDetailsElement): void {
  const menu = details.querySelector<HTMLElement>('.menu')
  const summary = details.querySelector<HTMLElement>('summary')
  if (!details.open || !menu || !summary) return

  resetVisualOptionsMenuPlacement(details)
  const summaryRect = summary.getBoundingClientRect()
  const renderedScale = summary.offsetHeight > 0 ? summaryRect.height / summary.offsetHeight : 1
  const scale = Number.isFinite(renderedScale) && renderedScale > 0 ? renderedScale : 1
  const gap = 4 * scale
  const spaceBelow = Math.max(0, window.innerHeight - summaryRect.bottom - gap - visualMenuViewportGap)
  const spaceAbove = Math.max(0, summaryRect.top - gap - visualMenuViewportGap)
  const naturalHeight = menu.scrollHeight * scale
  const openUp = naturalHeight > spaceBelow && spaceAbove > spaceBelow
  const availableHeight = openUp ? spaceAbove : spaceBelow

  details.toggleAttribute('data-menu-open-up', openUp)
  details.style.setProperty('--lv-visual-menu-max-height', `${availableHeight / scale}px`)
  menu.style.top = openUp ? `${-(menu.offsetHeight + 4)}px` : `${details.offsetHeight + 4}px`
  menu.style.bottom = 'auto'
}

export const visualActionStyles = css`
  .visual-actions {
    display: flex;
    flex: 0 0 auto;
    align-items: center;
    gap: var(--base-size-4);
    margin-inline-end: var(--lv-visual-focus-close-space, 0);
  }

  .icon-action {
    display: grid;
    flex: 0 0 auto;
    width: var(--lv-visual-action-target, var(--lv-button-height-xs, var(--control-xsmall-size)));
    min-width: var(--lv-visual-action-target, var(--lv-button-height-xs, var(--control-xsmall-size)));
    height: var(--lv-visual-action-target, var(--lv-button-height-xs, var(--control-xsmall-size)));
    min-height: var(--lv-visual-action-target, var(--lv-button-height-xs, var(--control-xsmall-size)));
    place-items: center;
    border: var(--borderWidth-default, var(--lv-border-width)) solid var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest, var(--lv-line-muted)));
    border-radius: var(--lv-radius-tight);
    background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest, var(--lv-bg-panel)));
    color: var(--lv-button-invisible-icon-rest, var(--lv-icon-muted, var(--lv-fg-muted)));
    cursor: pointer;
    padding: 0;
    font: inherit;
    line-height: 1;
  }

  .icon-action[data-visualization-expand] { display: var(--lv-visual-expand-display, grid); }

  .icon-action svg {
    width: var(--base-size-16);
    height: var(--base-size-16);
  }

  .icon-action:hover,
  .icon-action:focus-visible {
    border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover, var(--lv-line-default)));
    background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover, var(--lv-bg-panel-muted)));
    color: var(--lv-icon-default, var(--lv-fg-default));
    outline: var(--focus-outline, var(--lv-border-default));
    outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
    outline-offset: var(--focus-outline-offset, var(--base-size-2));
  }

  .visual-options .menu {
    box-sizing: border-box;
    max-height: var(--lv-visual-menu-max-height, calc(100vh - var(--base-size-16)));
    overflow-y: auto;
    overscroll-behavior: contain;
  }

`
