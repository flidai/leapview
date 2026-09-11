import { css, html, nothing } from 'lit'

export const dataExplorerResultStyles = css`
  .semantic-result {
    display: grid;
    min-width: 0;
    min-height: 0;
    grid-template-rows: auto auto auto minmax(0, 1fr);
    overflow: hidden;
  }

  .query-bar {
    display: grid;
    gap: var(--base-size-8);
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-12) var(--base-size-16);
    background: var(--lv-bg-app);
  }

  .result-body {
    display: grid;
    min-width: 0;
    min-height: 0;
    overflow: hidden;
  }

  .result-notices {
    display: flex;
    flex-wrap: wrap;
    gap: var(--base-size-8);
    align-items: center;
    border-bottom: var(--lv-border-muted);
    padding: var(--base-size-8) var(--base-size-16);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .result-notices:not(:has(> *)) { display: none; }

  .execution-state {
    display: inline-flex;
    align-items: center;
    gap: var(--base-size-4);
    color: var(--lv-fg-muted);
  }

  .execution-state[data-state="stale"] { color: var(--lv-fg-warning); }
  .execution-state[data-state="error"] { color: var(--lv-fg-danger); }
  .result-error { color: var(--lv-fg-danger); }

  .result-failure {
    display: inline-flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-8);
  }

  lv-data-explore-table { min-height: 0; }
`

export function renderExploreFailure(error: string, retryable: boolean, onRetry: () => void, onReset: () => void) {
  return html`
    <span class="result-failure" role="alert">
      <span class="result-error">${error}</span>
      ${retryable ? html`<button type="button" class="text-button" @click=${onRetry}>Retry</button>` : nothing}
      <button type="button" class="text-button" @click=${onReset}>Reset query</button>
    </span>
  `
}
