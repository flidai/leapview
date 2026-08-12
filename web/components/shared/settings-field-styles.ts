import { css } from 'lit'

/** Shared Primer-backed typography for label/value rows in settings surfaces. */
export const settingsFieldStyles = css`
  .settings-field {
    display: grid;
    min-width: 0;
    gap: var(--base-size-2);
  }

  .settings-label {
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-semibold);
  }

  .settings-description {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .settings-value {
    min-width: 0;
    overflow-wrap: anywhere;
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }
`
