export const recordTableDataStyles = `
  lv-record-table .variant-data {
    background: var(--lv-windowed-table-surface, var(--lv-bg-panel));
  }

  lv-record-table .variant-data .record-table th,
  lv-record-table .variant-data .record-table td {
    height: var(--lv-windowed-row-height, 32px);
    border-right: var(--lv-border-muted);
    padding: 0 var(--base-size-8);
    vertical-align: middle;
  }

  lv-record-table .variant-data .record-table th {
    border-bottom: var(--lv-border-emphasis, var(--lv-border-default));
    background: var(--lv-windowed-table-surface, var(--lv-bg-panel));
    font-weight: var(--base-text-weight-medium);
    text-transform: uppercase;
  }

  lv-record-table .variant-data .record-table th:last-child,
  lv-record-table .variant-data .record-table td:last-child {
    border-right: 0;
  }

  lv-record-table .variant-data .record-table tbody tr {
    background: var(--lv-bg-app);
  }

  lv-record-table .variant-data .record-table tbody tr:nth-child(even) {
    background: color-mix(in srgb, var(--lv-table-stripe, var(--lv-bg-panel-muted)), var(--lv-bg-app) 74%);
  }

  lv-record-table .variant-data .record-table tbody tr:not(:last-child) td {
    border-bottom: var(--lv-border-muted);
  }
`
