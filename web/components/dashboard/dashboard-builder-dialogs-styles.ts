import { css } from 'lit'

export const dashboardBuilderDialogsStyles = css`
    .format-placeholder {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      padding: var(--base-size-12);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
      line-height: 1.45;
    }

    .format-controls {
      display: grid;
      gap: var(--base-size-12);
    }

    .visual-format-controls {
      margin-top: var(--base-size-4);
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-12);
    }

    .visual-query-controls {
      margin-top: var(--base-size-4);
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-12);
    }

    .query-control-list {
      display: grid;
      gap: var(--base-size-8);
    }

    .query-sort-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto auto;
      gap: var(--base-size-6);
      align-items: center;
    }

    .query-sort-row button {
      min-width: 1.8rem;
    }

    .format-controls-heading {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .inline-page-properties {
      margin-top: var(--base-size-4);
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-12);
    }

    .format-section {
      display: grid;
      gap: var(--base-size-8);
      border-bottom: var(--lv-border-muted);
      padding-bottom: var(--base-size-12);
    }

    .format-section:last-child {
      border-bottom: 0;
      padding-bottom: 0;
    }

    .format-section h3 {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .format-text-field {
      display: grid;
      gap: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .format-text-field input[type='text'],
    .format-text-field input[type='number'],
    .format-text-field select {
      width: 100%;
      min-width: 0;
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-default);
      background: var(--lv-bg-control);
      font: var(--lv-type-body-compact);
    }

    .format-toggle {
      display: flex;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
    }

    .format-toggle input {
      width: 1rem;
      height: 1rem;
      margin: 0;
      accent-color: var(--lv-data-3);
    }

    .format-toggle:has(input:disabled) {
      color: var(--lv-fg-muted);
    }

    .page-layout-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: var(--base-size-8);
    }

    .page-format-summary {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-8);
      margin: 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .visual,
    .filter-component,
    .header-component,
    .builder-placeholder {
      position: absolute;
      display: block;
      min-width: 4rem;
      min-height: 3rem;
      box-sizing: border-box;
      color: inherit;
      text-align: left;
      cursor: default;
    }

    .visual.has-preview {
      overflow: hidden;
    }

    .visual:focus-visible,
    .filter-component:focus-visible,
    .header-component:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .visual[data-field-drop='compatible'] > .grid-stack-item-content {
      outline: 2px dashed var(--lv-data-2);
      outline-offset: -3px;
      background: var(--lv-data-2-muted);
    }

    .visual[data-field-drop='incompatible'] {
      opacity: 0.55;
    }

    .visual-title {
      overflow: hidden;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .visual-type {
      margin-top: 0.2rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .visual-preview {
      position: relative;
      display: block;
      width: 100%;
      height: 100%;
      min-width: 0;
      min-height: 0;
      overflow: hidden;
    }

    .visual-preview lv-visualization-host {
      display: block;
      width: 100%;
      height: 100%;
    }

    .visual-preview-empty {
      display: flex;
      min-height: 0;
      flex-direction: column;
      align-items: center;
      justify-content: center;
      gap: var(--base-size-4);
      padding: var(--base-size-8);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .visual-preview-empty strong {
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .filter-component > .grid-stack-item-content {
      grid-template-rows: minmax(0, 1fr);
      padding: 0;
      gap: 0;
      background: var(--lv-bg-panel);
    }

    .filter-component:hover > .grid-stack-item-content {
      border-color: var(--lv-line-emphasis);
      box-shadow: 0 0 0 var(--lv-border-width-focus) var(--lv-bg-control-hover);
    }

    .filter-control-preview {
      display: grid;
      min-height: 0;
      align-content: center;
      gap: var(--base-size-6);
    }

    .filter-preview-input,
    .filter-preview-range > span {
      display: flex;
      min-width: 0;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: space-between;
      padding: 0 var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-input, var(--lv-bg-panel));
      font: var(--lv-type-caption);
    }

    .filter-preview-range {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: var(--base-size-6);
    }

    .filter-runtime-note {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .filter-component lv-slicer {
      display: block;
      min-width: 0;
      min-height: 0;
      overflow: auto;
    }

    .visual-empty {
      display: grid;
      place-items: center;
      min-height: 20rem;
      color: var(--lv-fg-muted);
      text-align: center;
    }

    .visual-empty strong {
      display: block;
      margin-bottom: 0.3rem;
      color: var(--lv-fg-default);
    }

    .properties-body {
      display: grid;
      gap: var(--base-size-12);
      padding: var(--base-size-12);
    }

    .property-group {
      display: grid;
      gap: 0.35rem;
    }

    .property-label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .property-value {
      font: var(--lv-type-body-compact);
    }

    .slot {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.5rem;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: var(--base-size-6);
      font: var(--lv-type-body-compact);
    }

    .slot-kind {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .diagnostics {
      display: grid;
      gap: 0.35rem;
    }

    .diagnostic {
      border-left: 3px solid var(--lv-fg-muted);
      padding: 0.35rem 0.5rem;
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
    }

    .diagnostic.error {
      border-color: var(--lv-fg-danger);
    }

    .diagnostic.warning {
      border-color: var(--lv-fg-warning);
    }

    .diagnostic.info {
      border-color: var(--lv-data-1);
    }

    .evidence {
      display: grid;
      gap: 0.3rem;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .secondary-details {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-8);
    }

    .secondary-details summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
      cursor: pointer;
    }

    .secondary-details-content {
      display: grid;
      gap: var(--base-size-12);
      padding-top: var(--base-size-8);
    }

    .state {
      display: grid;
      place-items: center;
      min-height: 60svh;
      padding: 2rem;
      color: var(--lv-fg-muted);
      text-align: center;
    }

    .state strong {
      display: block;
      margin-bottom: 0.35rem;
      color: var(--lv-fg-default);
    }

    @media (max-width: 960px) {
      .toolbar {
        flex-wrap: wrap;
      }

      .title-wrap {
        min-width: 10rem;
      }

      .body {
        grid-template-columns: minmax(0, 1fr);
        grid-template-rows: minmax(0, 1fr) auto auto;
      }

      .right-dock {
        grid-column: 1;
        grid-row: 3;
        max-height: 19rem;
        grid-template-columns: var(--dock-filters-flex) var(--dock-visuals-flex) var(--dock-data-flex) var(--dock-agent-flex);
        grid-template-rows: minmax(0, 1fr);
        border-top: var(--lv-border-muted);
      }

      .filters-pane,
      .properties,
      .data-pane,
      .agent-pane {
        max-height: none;
        border-top: 0;
      }

      .filters-pane {
        border-left: 0;
      }

      .data-pane,
      .agent-pane {
        border-left: var(--lv-border-muted);
      }
    }

    @media (min-width: 961px) and (max-width: 1200px) {
      .body {
        grid-template-columns: minmax(0, 1fr) minmax(20rem, 23rem);
      }

      .right-dock {
        grid-template-columns: minmax(0, 1fr);
        grid-template-rows: var(--dock-filters-row) var(--dock-visuals-row) var(--dock-data-row) var(--dock-agent-row);
      }

      .properties,
      .data-pane,
      .agent-pane {
        border-top: var(--lv-border-muted);
      }
    }

    @media (max-width: 640px) {
      :host {
        height: 100%;
        max-height: 100svh;
        overflow-x: hidden;
        overflow-y: auto;
      }

      .builder {
        height: auto;
        min-height: auto;
        grid-template-columns: minmax(0, 1fr);
      }

      .body {
        display: block;
      }

      .right-dock {
        display: block;
        grid-column: 1;
        max-height: none;
        border-top: 0;
      }

      .pane {
        max-height: none;
        border: 0;
        border-bottom: var(--lv-border-muted);
      }

      .properties {
        max-height: none;
      }

      .data-pane,
      .agent-pane {
        border-left: 0;
      }

      .agent-pane-content {
        height: min(32rem, 60svh);
      }

      .page-tab {
        max-width: 12rem;
      }

      .canvas-pane {
        min-height: 38rem;
      }

      /* The authored grid remains absolute on desktop. On a narrow viewport,
       * switch the canvas to a single-column flow so chart hosts get a real
       * viewport-sized box instead of an off-screen slice of the desktop
       * canvas. The surface stays full-bleed; the canvas scroller remains the
       * only overflow container. */
      .canvas-fit {
        width: 100% !important;
        height: auto !important;
      }

      .canvas {
        position: relative;
        inset: auto;
        display: grid;
        width: 100%;
        min-width: 0;
        min-height: 0;
        aspect-ratio: auto !important;
        transform: none !important;
        grid-template-columns: minmax(0, 1fr) !important;
        grid-auto-rows: auto;
        gap: var(--base-size-12);
      }

      .canvas .visual,
      .canvas .filter-component,
      .canvas .header-component,
      .canvas .builder-placeholder {
        position: relative;
        top: auto !important;
        right: auto !important;
        bottom: auto !important;
        left: auto !important;
        width: 100% !important;
        height: 16rem !important;
        min-width: 0;
        min-height: 12rem;
        order: var(--mobile-order, 0);
      }

      .canvas .visual[data-visual-type='kpi'] {
        height: 8rem !important;
        min-height: 8rem;
      }

      .canvas .filter-component {
        height: 8rem !important;
        min-height: 8rem;
      }

      .canvas .header-component,
      .canvas .builder-placeholder {
        height: 8rem !important;
        min-height: 8rem;
      }

      .canvas .visual > .grid-stack-item-content,
      .canvas .filter-component > .grid-stack-item-content,
      .canvas .header-component > .grid-stack-item-content,
      .canvas .builder-placeholder > .grid-stack-item-content {
        position: relative;
        inset: auto !important;
        width: 100%;
        height: 100%;
      }

      .toolbar-actions {
        width: 100%;
        flex-wrap: wrap;
        overflow: visible;
      }

      .meta { white-space: normal; }
      .meta span { min-width: 0; overflow-wrap: anywhere; }

      .dashboard-metadata-form {
        left: 0;
        right: auto;
      }
    }

`
