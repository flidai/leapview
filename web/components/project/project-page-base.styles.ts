import { css } from 'lit'

export const projectBaseStyles = css`
  :host {
    display: block;
    min-width: 0;
    min-height: 100svh;
    color: var(--lv-fg-default);
    font-family: var(--fontStack-system);
    background: var(--lv-bg-app);
  }

  .page,
  .asset-page {
    display: grid;
    width: min(100%, var(--lv-page-content-max-width));
    min-width: 0;
    min-height: 100svh;
    align-content: start;
    gap: var(--base-size-16);
    box-sizing: border-box;
    margin-inline: auto;
    background: var(--lv-bg-app);
    padding: var(--base-size-24);
  }

  .asset-page {
    width: 100%;
    grid-template-rows: auto auto;
    gap: 0;
    height: auto;
    margin-inline: 0;
    padding: 0;
    overflow: visible;
  }

  .asset-page.data-asset-page {
    height: 100svh;
    min-height: 0;
    grid-template-rows: auto minmax(0, 1fr);
    overflow: hidden;
  }

  .asset-page.semantic-model-definition-page {
    height: 100svh;
    min-height: 0;
    grid-template-rows: auto minmax(0, 1fr);
    overflow: hidden;
  }

  .connection-feedback {
    margin-bottom: var(--base-size-16);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    padding: var(--base-size-12) var(--base-size-16);
    font: var(--lv-type-body-compact);
  }

  .connection-feedback.error {
    border-color: var(--lv-line-danger-muted);
    color: var(--lv-fg-danger);
  }

  .connection-feedback.success {
    border-color: var(--lv-line-success-muted);
    color: var(--lv-fg-success);
  }

  .catalog {
    gap: var(--base-size-16);
  }

  .header,
  .breadcrumb-header {
    display: grid;
    min-width: 0;
    grid-template-columns: minmax(0, 1fr) auto;
    align-items: center;
    gap: var(--base-size-8);
  }

  .breadcrumb-header {
    position: relative;
    border-bottom: var(--lv-border-muted);
    padding: var(--lv-space-control) var(--base-size-16);
  }

  .breadcrumb-header::before,
  .asset-body > .tabs::before {
    position: absolute;
    right: 100%;
    bottom: -1px;
    box-sizing: border-box;
    width: var(--lv-chrome-rule-gutter, 0);
    height: 0;
    border-bottom: var(--lv-border-muted);
    content: '';
    pointer-events: none;
  }

  .asset-body > .tabs::before {
    border-bottom: var(--lv-border-default);
  }

  .title-block {
    min-width: 0;
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

  h2 {
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
    font-weight: var(--base-text-weight-semibold);
  }

  .eyebrow {
    margin-bottom: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    text-transform: uppercase;
  }

  .detail,
  .muted {
    margin-top: var(--base-size-4);
    overflow: hidden;
    color: var(--lv-fg-muted);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body-compact);
  }

  .actions,
  .row-actions {
    display: inline-flex;
    min-width: 0;
    align-items: center;
    justify-content: flex-end;
    gap: var(--base-size-8);
  }

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

  .action-link,
  .icon-link,
  .icon-button {
    display: inline-grid;
    place-items: center;
    border-radius: var(--lv-radius-default);
    text-decoration: none;
  }

  .action-link {
    display: inline-flex;
    min-height: var(--lv-button-height);
    align-items: center;
    justify-content: center;
    gap: var(--base-size-6);
    border: var(--borderWidth-default) solid var(--lv-button-accent-border-rest);
    border-radius: var(--lv-button-radius);
    background: var(--lv-button-accent-bg-rest);
    color: var(--lv-button-accent-fg-rest);
    padding: 0 var(--lv-button-padding-inline-sm);
    cursor: pointer;
    font: var(--lv-type-body);
    white-space: nowrap;
  }

  .action-link:hover {
    border-color: var(--lv-button-accent-border-hover);
    background: var(--lv-button-accent-bg-hover);
  }

  .action-link:active {
    border-color: var(--lv-button-accent-border-active);
    background: var(--lv-button-accent-bg-active);
  }

  .action-link:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .icon-link,
  .icon-button {
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    padding: 0;
  }

  .icon-link {
    border-color: transparent;
    background: transparent;
    color: var(--lv-fg-muted);
    cursor: pointer;
  }

  .icon-link:hover,
  .icon-link:focus-visible {
    border-color: var(--lv-line-muted);
    background: var(--lv-bg-control-hover);
    color: var(--lv-fg-default);
    outline: 0;
  }

  .icon-link:disabled {
    opacity: 0.6;
    cursor: wait;
  }

  .icon-button {
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
  }

  button,
  input {
    font: inherit;
  }

  .toolbar {
    display: flex;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-8);
  }

  .visually-hidden {
    position: absolute;
    width: 1px;
    height: 1px;
    overflow: hidden;
    clip: rect(0 0 0 0);
    white-space: nowrap;
    clip-path: inset(50%);
  }

  .toolbar-filters,
  .toolbar-actions {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
  }

  .toolbar-filters {
    flex: 1 1 auto;
  }

  .toolbar-actions {
    flex: 0 0 auto;
  }

  .search {
    position: relative;
    display: flex;
    min-width: 12rem;
    width: min(100%, 19rem);
    flex: 0 1 19rem;
    align-items: center;
  }

  .search input[type='search'],
  .asset-filter select {
    box-sizing: border-box;
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .search input[type='search'] {
    width: 100%;
    min-width: 0;
    padding: 0 var(--base-size-12) 0 var(--base-size-36);
    outline: 0;
    line-height: var(--base-text-lineHeight-tight);
  }

  .search input[type='search']::placeholder {
    color: var(--lv-fg-muted);
    opacity: 1;
  }

  .search input[type='search']:focus-visible,
  .asset-filter select:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset, var(--base-size-2));
  }

  .asset-filter {
    display: flex;
    min-width: 0;
  }

  .asset-filter select {
    min-width: 4.75rem;
    padding: 0 var(--base-size-8);
  }

  .search-icon {
    position: absolute;
    top: 50%;
    left: var(--base-size-12);
    display: grid;
    width: var(--base-size-16);
    height: var(--base-size-16);
    place-items: center;
    color: var(--lv-fg-muted);
    pointer-events: none;
    transform: translateY(-50%);
  }

  .tabs {
    display: flex;
    min-width: 0;
    flex-wrap: wrap;
    gap: var(--base-size-24);
    border-bottom: var(--lv-border-default);
  }

  .tabs a {
    display: inline-flex;
    min-height: var(--control-xlarge-size);
    align-items: center;
    gap: var(--base-size-8);
    border-bottom: 2px solid transparent;
    color: var(--lv-fg-muted);
    font: var(--lv-type-body);
    text-decoration: none;
  }

  .tabs a.active {
    border-bottom-color: var(--lv-accent);
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-medium);
  }

  .count {
    display: inline-grid;
    min-width: var(--base-size-16);
    place-items: center;
    border-radius: var(--lv-radius-full);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-muted);
    padding: 0 var(--base-size-6);
    font: var(--lv-type-caption);
  }

  code {
    color: var(--lv-fg-muted);
    font: var(--lv-type-code-inline);
  }

  .asset-glyph {
    display: inline-grid;
    width: var(--control-medium-size);
    height: var(--control-medium-size);
    flex: 0 0 auto;
    place-items: center;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-muted);
  }

  .asset-glyph.inline {
    width: var(--base-size-20);
    height: var(--base-size-20);
  }

  .asset-kind-catalog {
    background: var(--lv-asset-catalog-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-catalog-border, var(--lv-line-muted));
    color: var(--lv-asset-catalog-accent, var(--lv-fg-muted));
  }

  .asset-kind-connection {
    background: var(--lv-asset-connection-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-connection-border, var(--lv-line-muted));
    color: var(--lv-asset-connection-accent, var(--lv-fg-muted));
  }

  .asset-kind-dashboard {
    background: var(--lv-asset-dashboard-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-dashboard-border, var(--lv-line-muted));
    color: var(--lv-asset-dashboard-accent, var(--lv-fg-muted));
  }

  .asset-kind-dimension {
    background: var(--lv-asset-dimension-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-dimension-border, var(--lv-line-muted));
    color: var(--lv-asset-dimension-accent, var(--lv-fg-muted));
  }

  .asset-kind-filter {
    background: var(--lv-asset-filter-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-filter-border, var(--lv-line-muted));
    color: var(--lv-asset-filter-accent, var(--lv-fg-muted));
  }

  .asset-kind-metric {
    background: var(--lv-asset-metric-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-metric-border, var(--lv-line-muted));
    color: var(--lv-asset-metric-accent, var(--lv-fg-muted));
  }

  .asset-kind-model {
    background: var(--lv-asset-model-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-model-border, var(--lv-line-muted));
    color: var(--lv-asset-model-accent, var(--lv-fg-muted));
  }

  .asset-kind-page {
    background: var(--lv-asset-page-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-page-border, var(--lv-line-muted));
    color: var(--lv-asset-page-accent, var(--lv-fg-muted));
  }

  .asset-kind-semantic-model {
    background: var(--lv-asset-semantic-model-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-semantic-model-border, var(--lv-line-muted));
    color: var(--lv-asset-semantic-model-accent, var(--lv-fg-muted));
  }

  .asset-kind-source {
    background: var(--lv-asset-source-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-source-border, var(--lv-line-muted));
    color: var(--lv-asset-source-accent, var(--lv-fg-muted));
  }

  .asset-kind-table {
    background: var(--lv-asset-table-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-table-border, var(--lv-line-muted));
    color: var(--lv-asset-table-accent, var(--lv-fg-muted));
  }

  .asset-kind-visual {
    background: var(--lv-asset-visual-bg, var(--lv-bg-panel-muted));
    border-color: var(--lv-asset-visual-border, var(--lv-line-muted));
    color: var(--lv-asset-visual-accent, var(--lv-fg-muted));
  }

  .empty {
    color: var(--lv-fg-muted);
    padding: var(--base-size-12);
    font: var(--lv-type-body);
  }

  .asset-body {
    display: grid;
    min-width: 0;
    min-height: 0;
    grid-template-rows: auto auto;
  }

  .data-asset-page .asset-body {
    grid-template-rows: auto minmax(0, 1fr);
    overflow: hidden;
  }

  .semantic-model-definition-page .asset-body {
    min-height: 0;
    grid-template-rows: auto minmax(0, 1fr);
    overflow: hidden;
  }

  .semantic-model-definition-page .definition-body {
    min-height: 0;
    overflow: hidden;
  }

  .asset-body > .tabs {
    position: relative;
    padding-inline: var(--base-size-16);
  }

  .section-body {
    min-height: 0;
    overflow: visible;
    padding: var(--base-size-16);
  }

  .lineage-body {
    padding: 0;
  }

  .data-body {
    min-height: 0;
    overflow: hidden;
    padding: 0;
  }

  .data-body lv-data-explorer {
    height: 100%;
  }

  .graph-details-body,
  .definition-body,
  .semantic-model-body {
    padding: 0;
  }

  .details,
  .details-content,
  .lineage-grids {
    display: grid;
    align-content: start;
    gap: var(--base-size-24);
  }

  .details-content {
    padding: var(--base-size-16);
  }

  .semantic-model-details-page {
    gap: 0;
  }

  .semantic-model-overview {
    box-sizing: border-box;
    width: min(100%, var(--lv-page-content-max-width));
  }

  .semantic-overview-panels {
    display: grid;
    min-width: 0;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--base-size-12);
  }

  .semantic-overview-panels.single,
  .semantic-overview-impact-grid.single {
    grid-template-columns: minmax(0, 1fr);
  }

  .semantic-overview-panel {
    display: grid;
    min-width: 0;
    align-content: start;
    gap: var(--base-size-12);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    padding: var(--base-size-16);
  }

  .record-table-pagination {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: var(--base-size-12);
    padding-top: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .record-table-pagination button {
    min-height: var(--control-medium-size);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    color: var(--lv-fg-default);
    cursor: pointer;
    padding: 0 var(--base-size-12);
    font: inherit;
  }

  .record-table-pagination button:disabled {
    cursor: default;
    opacity: .5;
  }

  .semantic-overview-description {
    max-width: 52rem;
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .semantic-overview-properties {
    display: grid;
    gap: var(--base-size-12);
    margin: 0;
  }

  .semantic-overview-properties div {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .semantic-overview-properties dt {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .semantic-overview-properties dd {
    min-width: 0;
    margin: 0;
    overflow-wrap: anywhere;
    color: var(--lv-fg-default);
    font: var(--lv-type-body-compact);
  }

  .semantic-overview-properties code {
    font: var(--lv-type-code-inline);
  }

  .semantic-overview-tags dd,
  .semantic-overview-pipeline-row dd {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-6);
  }

  .semantic-overview-tag {
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-full);
    background: var(--lv-bg-panel-muted);
    padding: 1px var(--base-size-8);
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

`
