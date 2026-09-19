import { css } from 'lit'

export const adminPageQueryStyles = css`
    .query-audit {
      display: grid;
      min-width: 0;
      gap: var(--base-size-12);
    }

    .query-filters {
      display: flex;
      flex-wrap: wrap;
      align-items: end;
      gap: var(--base-size-8);
      padding-block: var(--base-size-4);
    }

    .query-filter-shortcuts,
    .query-filter-summary,
    .query-time-presets {
      display: flex;
      min-width: 0;
      flex-wrap: wrap;
      align-items: center;
      gap: var(--base-size-6);
    }

    .query-filter-shortcuts,
    .query-time-presets {
      width: 100%;
    }

    .query-filter-shortcuts {
      order: -1;
    }

    .query-filter-summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .query-filter-summary-label {
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
    }

    .query-filter-chip {
      display: inline-flex;
      min-height: var(--base-size-24);
      align-items: center;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-panel);
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-default);
      font: var(--lv-type-caption);
    }

    .query-filter-clear,
    .query-filter-shortcut,
    .query-time-preset {
      min-height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body-compact);
      padding: 0 var(--base-size-8);
    }

    .query-filter-shortcut {
      border-color: var(--lv-border-accent);
      background: var(--lv-bg-accent-muted, var(--lv-bg-panel));
    }

    .query-filter-clear {
      color: var(--lv-fg-link);
    }

    .query-filter-clear:hover,
    .query-filter-clear:focus-visible,
    .query-filter-shortcut:hover,
    .query-filter-shortcut:focus-visible,
    .query-time-preset:hover,
    .query-time-preset:focus-visible {
      background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
      outline: 0;
    }

    .query-time-presets-label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
    }

    .query-date-filters {
      display: flex;
      flex: 1 1 18rem;
      flex-wrap: wrap;
      align-items: end;
      gap: var(--base-size-8);
    }

    .query-date-filter {
      display: grid;
      flex: 1 1 8rem;
      gap: var(--base-size-4);
      min-width: 8rem;
    }

    .query-date-filter label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
    }

    .query-date-filter input {
      min-width: 0;
      min-height: var(--lv-control-medium);
      box-sizing: border-box;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small);
      background: var(--lv-bg-input);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      padding: 0 var(--lv-space-control);
    }

    .query-detail-title {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
    }

    .query-detail-subtitle {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .query-filter {
      display: grid;
      flex: 1 1 16rem;
      gap: var(--base-size-4);
      min-width: 0;
    }

    .query-filter label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
    }

    .query-filter input {
      min-width: 0;
      min-height: var(--lv-control-medium);
      box-sizing: border-box;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small);
      background: var(--lv-bg-input);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      padding: 0 var(--lv-space-control);
    }

    .query-history-footer {
      display: flex;
      min-height: 2.75rem;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-12);
      border-top: var(--lv-border-muted);
      padding: var(--base-size-8) var(--base-size-12);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
    }

    .query-history-error {
      color: var(--lv-fg-danger);
    }

    .query-history-load-more {
      min-height: var(--lv-control-medium);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: inherit;
      padding: 0 var(--base-size-12);
    }

    .query-history-load-more:hover,
    .query-history-load-more:focus-visible {
      background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
      outline: 0;
    }

    .query-history-load-more:disabled {
      cursor: not-allowed;
      opacity: 0.64;
    }

    .query-detail-copy-row {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .query-detail-status {
      display: inline-flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-6);
      color: var(--lv-fg-default);
      font-weight: var(--base-text-weight-semibold);
    }

    .query-detail-status svg {
      display: block;
      width: var(--base-size-16);
      height: var(--base-size-16);
    }

    .query-detail-status-success svg {
      color: var(--lv-fg-success);
    }

    .query-detail-status-danger svg {
      color: var(--lv-fg-danger);
    }

    .query-detail-status-attention svg {
      color: var(--lv-fg-warning);
    }

    .query-detail-status-muted svg {
      color: var(--lv-fg-muted);
    }

    .query-detail-copy {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      font: inherit;
    }

    .query-detail-copy {
      width: var(--base-size-20);
      height: var(--base-size-20);
      flex: none;
      padding: 0;
    }

    .query-detail-copy:hover,
    .query-detail-copy:focus-visible {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-control-hover, var(--lv-bg-panel-muted));
      color: var(--lv-fg-default);
      outline: 0;
    }

    .query-detail-body {
      display: grid;
      align-content: start;
      gap: var(--base-size-16);
      min-width: 0;
    }

    .query-detail-section {
      display: grid;
      gap: var(--base-size-8);
      min-width: 0;
    }

    .query-detail-section h2,
    .query-detail-section summary {
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .query-detail-facts {
      display: grid;
      gap: var(--base-size-6);
    }

    .query-detail-fact {
      display: grid;
      grid-template-columns: minmax(7rem, 0.44fr) minmax(0, 1fr);
      gap: var(--base-size-12);
      min-width: 0;
      align-items: start;
      font: var(--lv-type-body-compact);
    }

    .query-detail-fact span {
      color: var(--lv-fg-muted);
    }

    .query-detail-fact code,
    .query-detail-fact strong {
      min-width: 0;
      overflow-wrap: anywhere;
    }

    .query-detail-fact code,
    .query-detail-code {
      font-family: var(--fontStack-monospace);
    }

    .query-detail-code {
      max-height: 15rem;
      min-width: 0;
      overflow: auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-default);
      margin: 0;
      padding: var(--base-size-12);
      font: var(--lv-type-body);
      white-space: pre;
    }

    .query-detail-error {
      border-color: var(--lv-line-danger-muted, var(--lv-line-muted));
      background: var(--lv-bg-danger-muted, var(--lv-bg-panel-muted));
    }

    .query-detail-raw {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-12);
    }

    .query-detail-raw summary {
      cursor: pointer;
    }

    @media (max-width: 640px) {
      .route {
        grid-template-columns: 1fr;
      }

      .main {
        padding: var(--base-size-16);
      }

      .main-settings {
        width: 100%;
        gap: var(--base-size-24);
        padding: var(--base-size-32) var(--base-size-16) var(--base-size-64);
      }

      .main-profile {
        width: 100%;
      }

      .main-settings .page-header h1 {
        font: var(--lv-type-page-title);
      }

      .local-user-action {
        width: 100%;
      }

      .query-filters > lv-filter-menu {
        flex: 1 1 auto;
      }

      .query-filter {
        flex-basis: 100%;
      }

      .query-history-footer {
        align-items: stretch;
        flex-direction: column;
      }

      .query-history-load-more {
        width: 100%;
      }
    }
`
