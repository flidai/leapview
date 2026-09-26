import { css } from 'lit'

// Shared admin tab treatment used by Agent and Access settings.
export const tabBarStyles = css`
  .tab-bar {
    display: flex;
    align-items: center;
    gap: var(--base-size-4);
    border-bottom: var(--lv-border-muted);
    padding: 0;
  }

  .tab-bar button {
    position: relative;
    min-height: auto;
    border: 0;
    border-radius: var(--lv-radius-small) var(--lv-radius-small) 0 0;
    background: transparent;
    padding: var(--base-size-8) var(--base-size-12);
    color: var(--lv-fg-muted);
    cursor: pointer;
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-medium);
  }

  .tab-bar button:hover {
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-default);
  }

  .tab-bar button.is-active {
    color: var(--lv-fg-accent);
  }

  .tab-bar button.is-active::after {
    position: absolute;
    right: var(--base-size-8);
    bottom: -1px;
    left: var(--base-size-8);
    height: 2px;
    background: var(--lv-fg-accent);
    content: '';
  }

  .tab-bar button:focus-visible {
    outline: 2px solid var(--lv-fg-accent);
    outline-offset: -2px;
  }
`
