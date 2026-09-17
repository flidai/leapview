import { css } from 'lit'

export const projectOverviewStyles = css`
  .semantic-overview-refresh-status dd {
    display: flex;
    align-items: center;
    gap: var(--base-size-6);
  }

  .semantic-overview-status-dot {
    width: var(--base-size-8);
    height: var(--base-size-8);
    flex: 0 0 auto;
    border-radius: var(--lv-radius-full);
    background: var(--lv-fg-muted);
  }

  .semantic-overview-refresh-status dd[data-tone='success'] .semantic-overview-status-dot {
    background: var(--lv-fg-success);
  }

  .semantic-overview-refresh-status dd[data-tone='warning'] .semantic-overview-status-dot {
    background: var(--lv-fg-warning);
  }

  .semantic-overview-refresh-status dd[data-tone='danger'] .semantic-overview-status-dot {
    background: var(--lv-fg-danger);
  }

  .semantic-overview-guidance {
    color: var(--lv-fg-danger);
    font: var(--lv-type-body-compact);
  }

  .semantic-overview-note {
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .semantic-overview-recent-runs {
    display: grid;
    gap: var(--base-size-12);
    padding-top: var(--base-size-16);
  }

  .semantic-overview-run-list {
    display: grid;
    gap: 0;
    margin: 0;
    padding: 0;
    list-style: none;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    overflow: hidden;
  }

  .semantic-overview-run-list li + li {
    border-top: var(--lv-border-muted);
  }

  .semantic-overview-run {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: var(--base-size-12);
    padding: var(--base-size-12);
    color: var(--lv-fg-default);
    font: var(--lv-type-body-compact);
    text-decoration: none;
  }

  .semantic-overview-run:hover,
  .semantic-overview-run:focus-visible {
    background: var(--lv-bg-control-hover);
  }

  .semantic-overview-run-state {
    width: var(--base-size-8);
    height: var(--base-size-8);
    flex: 0 0 auto;
    border-radius: var(--lv-radius-full);
    background: var(--lv-fg-muted);
  }

  .semantic-overview-run-state[data-tone='success'] { background: var(--lv-fg-success); }
  .semantic-overview-run-state[data-tone='warning'] { background: var(--lv-fg-warning); }
  .semantic-overview-run-state[data-tone='danger'] { background: var(--lv-fg-danger); }

  .semantic-overview-actions {
    display: flex;
    flex-wrap: wrap;
    gap: var(--base-size-12);
  }

  .semantic-overview-refresh-link,
  .semantic-overview-version-link,
  .semantic-overview-pipeline,
  .semantic-overview-lineage-link,
  .semantic-overview-downstream-link,
  .semantic-overview-upstream-asset,
  .semantic-overview-downstream-asset {
    width: fit-content;
    color: var(--lv-fg-accent);
    font: var(--lv-type-body-compact);
    text-decoration: none;
  }

  .semantic-overview-refresh-link:hover,
  .semantic-overview-version-link:hover,
  .semantic-overview-pipeline:hover,
  .semantic-overview-lineage-link:hover,
  .semantic-overview-downstream-link:hover,
  .semantic-overview-upstream-asset:hover,
  .semantic-overview-downstream-asset:hover {
    text-decoration: underline;
  }

  .semantic-overview-more,
  .semantic-overview-empty-impact {
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .semantic-model-summary {
    display: grid;
    gap: var(--base-size-12);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    padding: var(--base-size-16);
  }

  .semantic-model-summary-heading,
  .semantic-object-list-header {
    display: flex;
    min-width: 0;
    align-items: center;
    justify-content: space-between;
    gap: var(--base-size-16);
  }

  .semantic-model-summary-heading > div {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .semantic-model-summary-heading p {
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .semantic-model-summary-heading a {
    color: var(--lv-fg-accent);
    font: var(--lv-type-body-compact);
    text-decoration: none;
    white-space: nowrap;
  }

  .semantic-model-summary-heading a:hover {
    text-decoration: underline;
  }

  .semantic-summary-cards {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 1fr));
    gap: var(--base-size-12);
  }

  .semantic-summary-card {
    display: grid;
    min-width: 0;
    gap: var(--base-size-8);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    color: var(--lv-fg-default);
    padding: var(--base-size-12);
    text-decoration: none;
  }

  .semantic-summary-card:hover,
  .semantic-summary-card:focus-visible {
    border-color: var(--lv-line-accent, var(--lv-accent));
    background: var(--lv-bg-control-hover);
    outline: 0;
  }

  .semantic-summary-card span {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .semantic-summary-card strong {
    font: var(--lv-type-section-title);
  }

  .semantic-overview-impact-grid {
    display: grid;
    min-width: 0;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: var(--base-size-12);
    padding-top: var(--base-size-16);
  }

  .semantic-overview-impact-grid.single {
    grid-template-columns: minmax(0, 1fr);
  }

  .semantic-overview-impact {
    display: grid;
    min-width: 0;
    align-content: start;
    gap: var(--base-size-8);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel);
    padding: var(--base-size-16);
  }

  .semantic-overview-impact-fact {
    color: var(--lv-fg-default);
    font: var(--lv-type-body);
  }

  .semantic-overview-downstream-assets {
    display: grid;
    gap: var(--base-size-4);
    margin: 0;
    padding: 0;
    list-style: none;
  }

  .semantic-model-view {
    display: grid;
    min-height: 0;
    background: transparent;
  }

  .semantic-model-layout {
    display: grid;
    min-width: 0;
    grid-template-columns: minmax(0, 1fr);
    align-content: start;
  }

  .semantic-model-navigation {
    display: flex;
    min-width: 0;
    align-items: stretch;
    gap: var(--base-size-4);
    margin-top: var(--base-size-12);
    overflow-x: auto;
    overflow-y: hidden;
    border-bottom: var(--lv-border-muted);
    padding: 0 var(--base-size-16);
    scrollbar-width: thin;
  }

  .semantic-model-nav-item {
    display: inline-flex;
    flex: 0 0 auto;
    align-items: center;
    gap: var(--base-size-8);
    border: 0;
    border-bottom: 2px solid transparent;
    background: transparent;
    color: var(--lv-fg-muted);
    cursor: pointer;
    padding: var(--base-size-12) var(--base-size-8);
    font: var(--lv-type-body-compact);
    white-space: nowrap;
  }

  .semantic-model-nav-item[data-active='true'] {
    border-bottom-color: var(--lv-fg-accent);
    color: var(--lv-fg-default);
    font-weight: var(--base-text-weight-normal, 400);
  }

  .semantic-model-nav-item strong {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
    font-weight: var(--base-text-weight-normal, 400);
  }

  .semantic-model-nav-item:hover {
    color: var(--lv-fg-default);
  }

  .semantic-model-nav-item:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .semantic-model-content {
    min-width: 0;
  }

  .semantic-model-diagram-view .semantic-model-content,
  .semantic-model-diagram-view .semantic-model-section,
  .semantic-model-diagram-view .semantic-model-graph {
    height: 100%;
    min-height: 0;
    overflow: hidden;
  }

  .semantic-model-definition-page .semantic-model-view,
  .semantic-model-definition-page .semantic-model-layout {
    height: 100%;
    min-height: 0;
  }

  .semantic-model-definition-page .semantic-model-layout {
    grid-template-rows: auto minmax(0, 1fr);
    align-content: stretch;
  }

  .semantic-model-diagram-view .semantic-model-section {
    display: grid;
  }

  .semantic-model-diagram-view .semantic-model-graph {
    height: 100%;
  }

  .semantic-object-list {
    display: grid;
    min-width: 0;
    align-content: start;
    gap: var(--base-size-12);
    padding: var(--base-size-16);
  }

  .semantic-object-search-wrap {
    position: relative;
    display: flex;
    width: min(22rem, 40%);
    min-width: 12rem;
    align-items: center;
  }

  .semantic-object-search-wrap svg {
    position: absolute;
    left: var(--base-size-8);
    color: var(--lv-fg-muted);
    pointer-events: none;
  }

  .semantic-object-search {
    width: 100%;
    height: var(--control-medium-size);
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    outline: 0;
    background: var(--lv-bg-app);
    color: var(--lv-fg-default);
    padding: 0 var(--base-size-8) 0 var(--base-size-32);
    font: var(--lv-type-body-compact);
  }

  .semantic-object-search:focus-visible {
    outline: var(--focus-outline);
    outline-offset: var(--focus-outline-offset);
  }

  .semantic-model-source .details-content {
    padding: var(--base-size-16);
  }

  .drawer-page {
    min-height: 100svh;
  }

  .source-drawer-title {
    display: flex;
    min-width: 0;
    align-items: center;
    gap: var(--base-size-8);
  }

  .source-drawer-title h1 {
    font: var(--lv-type-section-title);
  }

  .source-drawer-subtitle {
    margin-top: var(--base-size-4);
    color: var(--lv-fg-muted);
    font: var(--lv-type-body-compact);
  }

  .source-drawer-body {
    display: grid;
    min-width: 0;
    align-content: start;
  }

  .version-drawer-body {
    gap: var(--base-size-20);
  }

  .version-changes pre {
    max-height: 24rem;
    margin: 0;
    overflow: auto;
    border: var(--lv-border-muted);
    border-radius: var(--lv-radius-default);
    background: var(--lv-bg-panel-muted);
    padding: var(--base-size-12);
    color: var(--lv-fg-default);
    font: var(--lv-type-code-block);
    white-space: pre;
  }

  .source-drawer-section {
    min-width: 0;
    padding-top: var(--base-size-16);
  }

  .source-drawer-section.lineage-body {
    margin-inline: calc(-1 * var(--base-size-20));
    padding-top: 0;
  }

  .source-drawer-section .details-content {
    padding-inline: 0;
  }

  .source-drawer-section .lineage-graph {
    height: 20rem;
  }

  .lineage {
    display: grid;
    min-height: 0;
    align-content: start;
  }

  .lineage-graph {
    display: block;
    height: var(--lv-lineage-graph-height);
    min-height: 0;
    border-bottom: var(--lv-border-muted);
    background: var(--lv-bg-panel);
  }

  .semantic-model-section {
    min-height: 0;
  }

  .semantic-model-graph {
    display: block;
    height: min(72svh, 48rem);
    min-height: 0;
    overflow: hidden;
    border-bottom: var(--lv-border-muted);
    background: transparent;
  }

  .lineage-grids {
    padding: var(--base-size-16);
  }

  .detail-section {
    display: grid;
    min-width: 0;
    align-content: start;
    gap: var(--base-size-12);
    border-bottom: var(--lv-border-muted);
    padding-bottom: var(--base-size-20);
  }

  .detail-section:last-child {
    border-bottom: 0;
  }

  .facts {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
    gap: var(--base-size-12) var(--base-size-20);
  }

  .facts.overview {
    grid-template-columns: repeat(auto-fit, minmax(8rem, 1fr));
  }

  .facts .wide {
    grid-column: span 2;
  }

  .facts div {
    display: grid;
    min-width: 0;
    gap: var(--base-size-4);
  }

  .facts span:first-child {
    color: var(--lv-fg-muted);
    font: var(--lv-type-caption);
  }

  .facts p,
  .facts code {
    overflow: hidden;
    color: var(--lv-fg-default);
    text-overflow: ellipsis;
    white-space: nowrap;
    font: var(--lv-type-body);
  }

  .facts .wide p,
  .facts .wide code {
    white-space: pre-wrap;
  }

  .source-drawer-body .facts,
  .source-drawer-body .facts.overview {
    grid-template-columns: minmax(0, 1fr);
    gap: var(--base-size-12);
  }

  .source-drawer-body .facts > div {
    grid-template-columns: minmax(7rem, .42fr) minmax(0, 1fr);
    align-items: start;
    gap: var(--base-size-16);
  }

  .source-drawer-body .facts .wide {
    grid-column: auto;
  }

  .source-drawer-body .facts span:first-child {
    font: var(--lv-type-body-compact);
    text-transform: none;
  }

  .source-drawer-body .facts p,
  .source-drawer-body .facts code,
  .source-drawer-body .facts .wide p,
  .source-drawer-body .facts .wide code {
    overflow-wrap: anywhere;
    text-overflow: clip;
    white-space: normal;
    font: var(--lv-type-body-compact);
  }

  @media (max-width: 720px) {
    .page {
      padding: var(--base-size-12);
    }

    .toolbar {
      align-items: stretch;
      flex-wrap: wrap;
    }

    .toolbar-filters {
      width: 100%;
      flex: 1 1 100%;
    }

    .search {
      flex: 1 1 auto;
      width: 100%;
    }

    .toolbar-actions {
      margin-left: auto;
    }

    .header,
    .breadcrumb-header {
      grid-template-columns: 1fr;
    }

    .asset-page {
      height: auto;
      min-height: 100svh;
      overflow: visible;
    }

    .asset-page.data-asset-page {
      height: 100svh;
      min-height: 0;
      overflow: hidden;
    }

    .section-body {
      overflow: visible;
    }

    .data-asset-page .section-body {
      overflow: hidden;
    }

    .graph-details-body {
      overflow: visible;
    }

    .semantic-summary-cards {
      grid-template-columns: repeat(2, minmax(0, 1fr));
    }

    .semantic-overview-panels,
    .semantic-overview-impact-grid {
      grid-template-columns: 1fr;
    }

    .semantic-object-list-header {
      align-items: stretch;
      flex-direction: column;
    }

    .semantic-object-search-wrap {
      width: 100%;
    }

    .semantic-model-graph {
      height: 32rem;
    }
  }
`
