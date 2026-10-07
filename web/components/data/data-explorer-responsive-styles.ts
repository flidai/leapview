import { css } from 'lit'

export const dataExplorerResponsiveStyles = css`
  :host { container-type: inline-size; container-name: explorer; }
  .main { container-type: inline-size; container-name: explorer-main; }
  .semantic-fields-toggle { display: none; }

  @media (max-width: 760px) {
    .header { grid-template-columns: minmax(0, 1fr); }
    .header-actions { flex-wrap: wrap; gap: var(--base-size-4); }
    .explorer { display: block; position: relative; min-height: 0; }
    .browser-resizer { display: none; }
    .browser, .main { min-height: 0; }
    .browser {
      position: absolute; z-index: var(--zIndex-sticky, 50); inset: 0 auto 0 0;
      width: min(320px, calc(100% - 44px)) !important; box-shadow: var(--lv-shadow-floating-sm);
    }
    .browser-collapsed .browser { bottom: auto; width: 44px !important; height: 44px; box-shadow: none; }
    .main { width: calc(100% - 44px); height: 100%; margin-left: 44px; }
    .filter-editor, .diagnostics { grid-template-columns: 1fr; }
  }

  @container explorer (max-width: 900px) {
    .header { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-8); }
    .header-actions { flex-wrap: wrap; gap: var(--base-size-4); }
    .route.semantic .main { width: 100%; margin-left: 0; }
    .saved-explorations { position: relative; padding: var(--base-size-8) var(--base-size-12); }
    .saved-exploration-picker { min-width: 0; }
    .saved-exploration-picker > summary { max-width: 100%; }
    .saved-exploration-status { margin: 0; }
  }

  @container explorer-main (max-width: 900px) {
    .semantic-fields-toggle {
      display: inline-flex; align-items: center; gap: var(--base-size-6);
      min-height: var(--control-medium-size); border: var(--lv-border-default);
      border-radius: var(--lv-radius-default); padding: 0 var(--base-size-8);
      color: var(--lv-fg-default); background: var(--lv-bg-control); font: var(--lv-type-body); cursor: pointer;
    }
    .semantic-layout { position: relative; grid-template-columns: minmax(0, 1fr) 38px; grid-template-rows: auto minmax(0, 1fr); }
    .semantic-fields-toggle { grid-column: 1; grid-row: 1; justify-content: flex-start; border-radius: 0; border: 0; border-bottom: var(--lv-border-muted); padding: var(--base-size-4) var(--base-size-12); }
    .semantic-fields-toggle svg { margin-left: auto; }
    .semantic-fields { display: none; }
    .semantic-fields.is-open {
      display: block; position: absolute; z-index: var(--zIndex-sticky, 50); top: calc(var(--control-medium-size) + 8px); bottom: 0; left: 0;
      box-sizing: border-box; width: min(320px, calc(100% - 38px)); max-height: none; border-right: var(--lv-border-default); box-shadow: var(--lv-shadow-floating-sm);
    }
    .semantic-result { min-height: 0; grid-column: 1; grid-row: 2; grid-template-rows: auto auto minmax(10rem, 1fr); }
    .semantic-filter-dock { grid-column: 2; grid-row: 1 / -1; }
    .filters-open .semantic-filter-dock { position: absolute; grid-area: auto; z-index: var(--zIndex-sticky, 50); inset: 0 0 0 auto; box-sizing: border-box; width: min(320px, 85%); box-shadow: var(--lv-shadow-floating-sm); }
    .query-bar { padding: var(--base-size-8) var(--base-size-12); }
    .result-meta { padding: var(--base-size-8) var(--base-size-12); }
  }
`
