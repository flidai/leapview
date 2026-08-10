import { css } from 'lit'

export const catalogListStyles = css`
  .catalog-list {
    display: grid;
    min-width: 0;
    overflow: hidden;
    margin: 0;
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-page);
    box-shadow: inset 0 0 0 var(--borderWidth-default) var(--lv-line-muted);
    padding: 0;
    list-style: none;
  }

  .catalog-list li {
    min-width: 0;
  }

  .catalog-row {
    position: relative;
    display: grid;
    box-sizing: border-box;
    height: 4.5rem;
    min-width: 0;
    grid-template-columns: var(--control-medium-size) minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--base-size-12);
    padding: var(--base-size-12) var(--base-size-16);
    color: var(--lv-fg-default);
    text-decoration: none;
    transition: background-color var(--motion-transition-stateChange);
  }

  .catalog-list li + li .catalog-row::before {
    position: absolute;
    top: 0;
    right: var(--base-size-16);
    left: var(--base-size-16);
    height: var(--borderWidth-default);
    background: var(--lv-line-muted);
    content: '';
  }

  .catalog-row:hover,
  .catalog-row:focus-visible {
    background: var(--lv-bg-control-hover);
  }

  .catalog-row:focus-visible {
    z-index: 1;
    outline: var(--borderWidth-thick) solid var(--borderColor-accent-emphasis, var(--lv-line-accent));
    outline-offset: calc(-1 * var(--borderWidth-thick));
  }

  .catalog-icon,
  .catalog-chevron {
    display: grid;
    place-items: center;
  }

  .catalog-icon {
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-link);
  }

  .catalog-copy {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .catalog-title,
  .catalog-description {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .catalog-title {
    font: var(--lv-type-body-compact);
    font-weight: var(--base-text-weight-semibold);
  }

  .catalog-description,
  .catalog-meta {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .catalog-trailing {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-12);
  }

  .catalog-meta {
    white-space: nowrap;
  }

  .catalog-chevron {
    width: var(--base-size-16);
    height: var(--base-size-16);
    color: var(--lv-fg-muted);
  }
`
