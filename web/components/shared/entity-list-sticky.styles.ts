export const entityListStickyStyles = `
  .entity-list-table thead th:first-child,
  .entity-list-table-row > th:first-child { position: sticky; left: 0; background: var(--lv-bg-page); }
  .entity-list-table thead th:first-child { z-index: 2; }
  .entity-list-table-row > th:first-child { z-index: 1; transition: background-color var(--motion-transition-stateChange); }
  .entity-list-table-row:hover,
  .entity-list-table-row:focus-within,
  .entity-list-table-row:hover > th:first-child,
  .entity-list-table-row:focus-within > th:first-child { background: var(--lv-bg-control-hover); }
`
