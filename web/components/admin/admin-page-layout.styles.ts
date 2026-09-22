import { css } from 'lit'

export const adminPageLayoutStyles = css`
    :host {
      display: block;
      min-width: 0;
      min-height: 100svh;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
      background: var(--lv-bg-app);
    }

    .route {
      display: grid;
      min-height: 100svh;
      grid-template-columns: minmax(0, 1fr);
      align-items: start;
      background: var(--lv-bg-app);
    }

    .main {
      display: grid;
      width: min(100%, var(--lv-page-content-max-width));
      min-width: 0;
      min-height: 100svh;
      align-content: start;
      gap: var(--base-size-12);
      box-sizing: border-box;
      justify-self: center;
      padding: var(--base-size-24);
    }

    .main-storage {
      width: 100%;
      grid-template-rows: minmax(0, 1fr);
      align-content: stretch;
      gap: 0;
      justify-self: stretch;
      padding: 0;
    }

    .main-settings {
      width: min(calc(100% - var(--base-size-48)), var(--lv-settings-content-max-width));
      gap: var(--base-size-24);
      padding: var(--base-size-64) 0;
    }

    .main-security {
      --lv-settings-content-max-width: 52rem;
    }

    .main-settings .page-title-block {
      display: grid;
      gap: var(--base-size-8);
    }

    .main-settings .page-header .page-detail {
      margin-top: 0;
    }

    .main-settings .page-header h1 {
      font: var(--lv-type-page-title);
    }

    header {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
    }

    h1,
    h2,
    p {
      margin: 0;
    }

    h1 {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
    }

    .main-directory {
      gap: var(--base-size-16);
      padding: var(--base-size-24);
    }

    .main-directory h1 {
      font: var(--lv-type-page-title);
    }

    .metrics {
      display: grid;
          max-width: var(--lv-page-content-max-width);
      grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
      gap: var(--base-size-12);
    }

    .metric,
    .panel {
      min-width: 0;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
    }

    .panel.table-panel {
      border: 0;
      border-radius: 0;
      background: var(--lv-bg-page);
    }

    .metric {
      display: grid;
      align-content: start;
      gap: var(--base-size-4);
      padding: var(--base-size-16);
    }

    .metric .label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: uppercase;
    }

    .metric .value {
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
      font: var(--lv-type-section-title);
    }

    .metric .meta,
    .empty {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .empty {
      padding: var(--base-size-12);
    }

    .warnings {
      display: grid;
          max-width: var(--lv-page-content-max-width);
      gap: var(--base-size-8);
    }

    .warning {
      border: var(--lv-border-attention);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-attention-muted);
      padding: var(--lv-space-control) var(--base-size-12);
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
    }

    .section {
      display: grid;
      min-width: 0;
      align-content: start;
      gap: var(--base-size-12);
    }

    .detail-section {
      gap: var(--base-size-16);
      border-top: var(--lv-border-muted);
      padding: var(--base-size-24) 0;
    }

    .detail-section .table-panel {
      border: 0;
      border-radius: 0;
    }

    .publication-drawer-title {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
    }

    .publication-drawer-title h2,
    .publication-drawer-title p {
      margin: 0;
    }

    .publication-drawer-title h2 {
      overflow: hidden;
      color: var(--lv-fg-default);
      font: var(--lv-type-section-title);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .publication-list {
      display: grid;
      min-width: 0;
      gap: var(--base-size-12);
    }

    .publication-drawer-title p {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .publication-drawer-status {
      display: inline-flex;
      width: fit-content;
      align-items: center;
      border-radius: var(--lv-radius-large);
      background: var(--lv-bg-control);
      padding: var(--base-size-2) var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-transform: capitalize;
    }

    .publication-drawer-body {
      display: grid;
      min-width: 0;
      gap: var(--base-size-24);
    }

    .publication-drawer-section {
      display: grid;
      min-width: 0;
      gap: var(--base-size-12);
    }

    .publication-drawer-section + .publication-drawer-section {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-20);
    }

    .publication-drawer-section h3 {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .publication-drawer-facts {
      display: grid;
      gap: var(--base-size-12);
      margin: 0;
    }

    .publication-drawer-fact {
      display: grid;
      min-width: 0;
      gap: var(--base-size-4);
    }

    .publication-drawer-fact dt {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .publication-drawer-fact dd {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
      margin: 0;
    }

    .publication-drawer-fact code {
      min-width: 0;
      overflow: hidden;
      color: var(--lv-fg-default);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .publication-drawer-fact a {
      min-width: 0;
      overflow: hidden;
      color: var(--lv-fg-link);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .publication-drawer-copy {
      min-height: var(--control-small-size);
      flex: 0 0 auto;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      cursor: pointer;
      padding: 0 var(--base-size-8);
      font: var(--lv-type-caption);
    }

    .publication-drawer-actions {
      display: flex;
      flex-wrap: wrap;
      gap: var(--base-size-8);
    }

    .publication-drawer-actions button,
    .publication-drawer-actions a {
      min-height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      padding: 0 var(--base-size-12);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body);
      line-height: var(--control-medium-size);
      text-decoration: none;
    }

    .publication-drawer-actions button:disabled {
      cursor: wait;
      opacity: 0.6;
    }

    .publication-history {
      display: grid;
      gap: var(--base-size-4);
      margin: 0;
      padding-left: var(--base-size-20);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .publication-history-empty {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .storage-error {
      display: grid;
      gap: var(--base-size-8);
      border: var(--lv-border-danger, var(--lv-border-muted));
      border-left: 3px solid var(--lv-fg-danger);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-danger-muted, var(--lv-bg-panel));
      padding: var(--base-size-12) var(--base-size-16);
    }

    .storage-error strong {
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
    }

    .storage-error p {
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
    }

    .storage-error details {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .storage-error summary {
      width: fit-content;
      cursor: pointer;
    }

    .storage-retry {
      width: fit-content;
      min-height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body);
      padding: 0 var(--base-size-12);
    }

    .storage-retry:hover,
    .storage-retry:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .storage-error code {
      display: block;
      margin-top: var(--base-size-8);
      overflow-wrap: anywhere;
      color: var(--lv-fg-muted);
      font-family: var(--fontStack-monospace);
    }

    h2 {
      color: var(--lv-fg-default);
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-semibold);
    }

    .card-facts {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
      gap: var(--base-size-12);
    }

    .local-user-panel {
      display: grid;
          max-width: var(--lv-page-content-max-width);
      gap: var(--base-size-12);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      padding: var(--base-size-12);
    }

    .local-user-action {
      min-height: var(--control-medium-size);
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      cursor: pointer;
      font: var(--lv-type-body);
      font-weight: var(--base-text-weight-medium);
      padding: 0 var(--base-size-12);
    }

    .section {
      min-width: 0;
    }

    .local-user-action:hover,
    .local-user-action:focus-visible {
      background: var(--lv-bg-control-hover);
      outline: 0;
    }

    .local-user-action:disabled {
      cursor: not-allowed;
      opacity: 0.64;
    }

    .local-user-result {
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
    }

    .local-user-result code {
      color: var(--lv-fg-default);
    }
`
