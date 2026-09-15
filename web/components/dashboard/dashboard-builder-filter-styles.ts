import { css } from 'lit'

export const dashboardBuilderFilterStyles = css`
    .filter-validation {
      margin: var(--base-size-6) 0 0;
      color: var(--lv-fg-danger, var(--lv-fg-default));
      font: var(--lv-type-caption);
    }

    .filter-scope-heading {
      display: flex;
      align-items: center;
      justify-content: space-between;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
    }

    .filter-drop-zone {
      display: grid;
      min-height: 3.75rem;
      place-items: center;
      padding: var(--base-size-8);
      border: 1px dashed var(--lv-fg-muted);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .filter-drop-zone[data-field-dragging='true'] {
      border-color: var(--lv-fg-accent);
      color: var(--lv-fg-default);
      background: var(--lv-bg-accent-muted, var(--lv-bg-panel-muted));
    }

    .filter-add-select,
    .filter-editor select,
    .filter-editor input[type='text'] {
      width: 100%;
      min-width: 0;
      box-sizing: border-box;
      min-height: var(--control-medium-size);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-default);
      background: var(--lv-bg-input, var(--lv-bg-panel));
      font: var(--lv-type-body-compact);
    }

    .filter-reset-actions {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: flex-end;
      flex-wrap: wrap;
      gap: var(--base-size-4);
    }

    .filter-reset-button {
      min-height: var(--control-small-size);
      border-color: transparent;
      padding: 0 var(--base-size-6);
      color: var(--lv-fg-muted);
      background: transparent;
      font: var(--lv-type-caption);
    }

    .filter-reset-button:hover:not(:disabled) {
      color: var(--lv-fg-default);
      background: var(--lv-bg-control-hover);
    }

    .filter-list {
      display: grid;
      gap: var(--base-size-6);
    }

    .filter-scope-group {
      display: grid;
      gap: var(--base-size-4);
    }

    .filter-scope-empty {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .filter-item { min-width: 0; }
    .filter-settings-body { min-width: 0; }
    .filter-settings summary { overflow-wrap: anywhere; }
    @container (max-width: 220px) {
      .filter-editor .filter-scope-options, .filter-editor .filter-editor-actions { grid-template-columns: minmax(0, 1fr); }
    }
`
