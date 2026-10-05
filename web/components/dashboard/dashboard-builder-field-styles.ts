import { css } from 'lit'

export const dashboardBuilderFieldStyles = css`
  .visual-requirements {
    margin: 0;
    padding: var(--base-size-6) var(--base-size-8);
    border-radius: var(--lv-radius-default);
    color: var(--lv-fg-muted);
    background: var(--lv-bg-panel-muted);
    font: var(--lv-type-caption);
  }

  .visual-field-help {
    display: grid;
    gap: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .visual-field-help button {
    width: fit-content;
    padding: 0;
    border: 0;
    color: var(--lv-fg-accent);
    background: none;
    font: inherit;
    cursor: pointer;
  }

  .visual-field-help button:hover,
  .visual-field-help button:focus-visible {
    text-decoration: underline;
  }
`
