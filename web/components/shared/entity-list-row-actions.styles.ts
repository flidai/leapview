export const entityListRowActionsStyles = `
  .entity-list-row-actions {
    display: flex;
    justify-content: flex-end;
    gap: var(--base-size-4);
  }

  .entity-list-row-action,
  .entity-list-row-pin {
    display: inline-flex;
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    align-items: center;
    justify-content: center;
    border: 0;
    border-radius: var(--lv-radius-default);
    background: transparent;
    color: var(--lv-fg-muted);
    cursor: pointer;
  }

  .entity-list-row-action:hover:not(:disabled),
  .entity-list-row-action:focus-visible,
  .entity-list-row-pin:hover,
  .entity-list-row-pin:focus-visible {
    background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
    color: var(--lv-fg-default);
  }

  .entity-list-row-action:focus-visible,
  .entity-list-row-pin:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .entity-list-row-action:disabled {
    cursor: not-allowed;
    opacity: 0.45;
  }

  .entity-list-row-pin[aria-pressed='false'] {
    opacity: 0;
  }

  .entity-list-table-row:hover .entity-list-row-pin,
  .entity-list-table-row:focus-within .entity-list-row-pin,
  .entity-list-row-pin:focus-visible {
    opacity: 1;
  }

  .entity-list-row-pin[aria-pressed='true'] {
    color: var(--lv-fg-accent);
  }

  @media (hover: none) {
    .entity-list-row-pin { opacity: 1; }
  }

  .entity-list-cell.is-center .entity-list-row-actions {
    justify-content: center;
  }
`
