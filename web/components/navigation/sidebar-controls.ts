import { css } from 'lit'

/** Shared Back and Search controls for primary and contextual sidebars. */
export const sidebarControlStyles = css`
  .sidebar-control-back {
    position: relative;
    box-sizing: border-box;
    display: grid;
    min-width: 0;
    flex: 1 1 auto;
    grid-template-columns: var(--lv-sidebar-control-icon-column, calc(var(--control-xsmall-size) + var(--base-size-2))) minmax(0, 1fr);
    min-height: var(--lv-sidebar-control-height, var(--control-medium-size));
    align-items: center;
    gap: var(--lv-sidebar-control-gap, var(--base-size-8));
    border: var(--lv-border-transparent);
    border-radius: var(--lv-radius-default);
    color: var(--lv-fg-muted);
    padding: 0 var(--control-xsmall-paddingInline-normal);
    text-decoration: none;
    font: var(--lv-sidebar-control-font, var(--lv-type-body));
  }

  .sidebar-control-back:hover,
  .sidebar-control-back:focus-visible {
    background: var(--control-bgColor-hover);
    color: var(--lv-fg-default);
    outline: 0;
  }

  .sidebar-control-back:focus-visible,
  .sidebar-search input:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .sidebar-control-back-icon,
  .sidebar-search-icon {
    display: grid;
    width: var(--control-xsmall-size);
    height: var(--control-xsmall-size);
    place-items: center;
  }

  .sidebar-control-back-icon {
    flex: 0 0 auto;
  }

  .sidebar-control-back-icon svg,
  .sidebar-search-icon svg {
    width: var(--base-size-16);
    height: var(--base-size-16);
  }

  .sidebar-control-back-label {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .sidebar-search {
    position: relative;
    display: grid;
    min-width: 0;
  }

  .sidebar-search-icon {
    position: absolute;
    top: 50%;
    left: var(--control-xsmall-paddingInline-normal);
    z-index: 1;
    color: var(--lv-fg-muted);
    pointer-events: none;
    transform: translateY(-50%);
  }

  .sidebar-search input {
    box-sizing: border-box;
    width: 100%;
    min-height: var(--lv-sidebar-control-height, var(--control-medium-size));
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-control, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
    padding: 0 var(--control-xsmall-paddingInline-normal) 0 calc(var(--control-xsmall-size) + var(--lv-sidebar-control-search-gap, var(--base-size-8)));
    font: var(--lv-sidebar-control-font, var(--lv-type-body));
  }

  .sidebar-search input::placeholder {
    color: var(--lv-fg-muted);
    opacity: 1;
  }

  .sidebar-search input:focus {
    border-color: var(--lv-fg-accent);
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }
`
