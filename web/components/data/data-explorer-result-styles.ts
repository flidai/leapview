import { css } from 'lit'

// Shared result-area styles stay separate from the Explorer shell so the
// result-view controls can evolve without growing the page component.
export const dataExplorerResultStyles = css`
  .query-row {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: var(--base-size-8);
    align-items: start;
  }

  .query-label {
    min-width: 5rem;
    padding-top: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-medium);
    text-transform: uppercase;
  }

  .selection-shelf,
  .filter-pills {
    min-width: 0;
    flex-wrap: wrap;
  }

  .query-summary {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .chip {
    display: inline-flex;
    max-width: 18rem;
    align-items: center;
    gap: var(--base-size-4);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-full);
    background: var(--lv-bg-control);
    padding: var(--base-size-4) var(--base-size-8);
    font: var(--lv-type-caption);
  }

  .chip.metric {
    border-color: var(--lv-line-accent, var(--lv-line-muted));
    background: var(--lv-bg-accent-muted);
    color: var(--lv-fg-accent);
  }

  .filter-editor {
    display: grid;
    grid-template-columns: minmax(8rem, 1fr) minmax(8rem, 1fr) minmax(12rem, 2fr) auto;
    gap: var(--base-size-8);
    align-items: end;
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-12) var(--base-size-16);
    background: var(--lv-bg-panel-muted);
  }

  .result-meta {
    display: flex;
    flex-wrap: wrap;
    gap: var(--base-size-8);
    align-items: center;
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-8) var(--base-size-16);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .result-view-switch {
    display: inline-flex;
    flex-wrap: wrap;
    gap: var(--base-size-4);
    margin-left: auto;
  }

  .result-view-switch button {
    border: var(--lv-border-default);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-control);
    color: var(--lv-fg-muted);
    padding: var(--base-size-4) var(--base-size-8);
    cursor: pointer;
    font: var(--lv-type-caption);
  }

  .result-view-switch button[aria-pressed='true'] {
    background: var(--lv-bg-accent-muted);
    color: var(--lv-fg-accent);
  }

  .result-visual-layout {
    display: grid;
    min-width: 0;
    min-height: 0;
    grid-template-rows: minmax(0, 1fr);
    overflow: hidden;
  }

  .result-visual-layout.paginated { grid-template-rows: auto minmax(0, 1fr); }

  .chart-pagination,
  .chart-page-actions {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: var(--base-size-8);
  }

  .chart-pagination {
    justify-content: space-between;
    padding: var(--base-size-8) var(--base-size-12);
    border-bottom: var(--lv-border-muted);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .result-visual {
    box-sizing: border-box;
    min-width: 0;
    min-height: 0;
    overflow: auto;
    overscroll-behavior: contain;
    padding: var(--base-size-12);
  }

  .result-visual:focus-visible {
    outline: 2px solid var(--lv-fg-accent);
    outline-offset: -2px;
  }

  .result-visual lv-visualization-host {
    display: block;
    min-height: max(18rem, var(--explorer-visual-min-height, 0px));
    height: 100%;
  }

  .execution-state {
    display: inline-flex;
    align-items: center;
    gap: var(--base-size-4);
    color: var(--lv-fg-muted);
  }

  .execution-state[data-state="stale"] { color: var(--lv-fg-warning); }
  .execution-state[data-state="error"] { color: var(--lv-fg-danger); }

  .result-error {
    color: var(--lv-fg-danger);
  }

  .result-failure {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-8);
  }

  lv-data-explore-table {
    min-height: 0;
  }
`
