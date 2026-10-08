import { css } from 'lit'

export const dashboardBuilderControlsStyles = css`
    .filter-editor-actions button {
      min-height: var(--control-small-size);
      padding-inline: var(--base-size-8);
    }

    .filter-remove {
      color: var(--lv-fg-danger, var(--lv-fg-default));
    }

    .filter-placement-action {
      color: var(--lv-data-2);
    }

    .filter-pane-empty {
      margin: var(--base-size-8) 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-align: center;
    }

    .filter-count {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-normal);
    }

    .page-bar {
      display: flex;
      grid-column: 1;
      grid-row: 2;
      min-width: 0;
      min-height: var(--control-large-size);
      align-items: center;
      gap: 0;
      border-top: var(--lv-border-muted);
      background: var(--lv-bg-panel-muted);
    }

    .page-bar > button {
      flex: 0 0 auto;
      width: var(--control-large-size);
      min-height: var(--control-large-size);
      padding: 0;
      border-color: transparent;
    }

    .page-add {
      align-self: stretch;
      margin: 0;
      border-radius: 0;
      color: var(--lv-button-accent-fg-rest, var(--lv-fg-on-accent));
      background: var(--lv-button-accent-bg-rest, var(--lv-bg-accent));
    }

    .page-add:hover:not(:disabled) {
      color: var(--lv-button-accent-fg-rest, var(--lv-fg-on-accent));
      background: var(--lv-button-accent-bg-hover, var(--lv-bg-accent));
    }

    .page-bar-tools {
      display: flex;
      flex: 0 0 auto;
      align-self: stretch;
      align-items: center;
      margin-left: auto;
    }

    .page-actions {
      position: relative;
      flex: 0 0 auto;
    }

    .page-actions > summary {
      display: grid;
      width: var(--control-medium-size);
      min-height: var(--control-medium-size);
      place-items: center;
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      color: var(--lv-fg-muted);
      cursor: pointer;
      list-style: none;
    }

    .page-actions > summary::-webkit-details-marker {
      display: none;
    }

    .page-actions > summary:hover {
      color: var(--lv-fg-default);
      background: var(--lv-bg-panel-muted);
    }

    .page-actions-menu {
      position: absolute;
      z-index: 4;
      right: 0;
      bottom: calc(100% + var(--base-size-4));
      display: grid;
      min-width: 10rem;
      gap: var(--base-size-2);
      padding: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
    }

    .page-actions-menu button {
      display: flex;
      width: 100%;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: flex-start;
      gap: var(--base-size-8);
      border-color: transparent;
      background: transparent;
      text-align: left;
    }

    .page-actions-menu button:hover:not(:disabled) {
      background: var(--lv-bg-panel-muted);
    }

    .page-actions-menu .page-delete {
      color: var(--lv-fg-danger, var(--lv-fg-default));
    }

    .page-zoom {
      display: flex;
      height: 100%;
      align-items: center;
      gap: var(--base-size-2);
      padding-inline: var(--base-size-8);
      border-left: var(--lv-border-muted);
    }

    .page-zoom button {
      display: grid;
      min-width: var(--control-small-size);
      min-height: var(--control-small-size);
      place-items: center;
      border-color: transparent;
      padding: 0;
      color: var(--lv-fg-muted);
      background: transparent;
    }

    .page-zoom button:hover:not(:disabled) {
      color: var(--lv-fg-default);
      background: var(--lv-bg-control-hover);
    }

    .page-zoom-value {
      min-width: 3.25rem !important;
      padding-inline: var(--base-size-6) !important;
      color: var(--lv-fg-default) !important;
      font: var(--lv-type-caption);
      font-variant-numeric: tabular-nums;
    }

    .pane-header {
      position: sticky;
      top: 0;
      z-index: 1;
      padding: 0.9rem 0.85rem 0.6rem;
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .pane-heading-row {
      display: flex;
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
    }

    .pane-title-group {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .pane-title-icon {
      display: inline-flex;
      flex: 0 0 auto;
      color: var(--lv-fg-muted);
    }

    .pane-content[hidden],
    .pane-header-details[hidden] {
      display: none !important;
    }

    .inspector-heading {
      display: flex;
      align-items: center;
      justify-content: space-between;
      flex-wrap: wrap;
      gap: var(--base-size-8);
    }

    .inspector-title {
      display: flex;
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
    }

    .inspector-heading .pane-title {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .visual-type-badge {
      flex: 0 0 auto;
      border-radius: var(--lv-radius-full);
      padding: var(--base-size-2) var(--base-size-6);
      color: var(--lv-fg-muted);
      background: var(--lv-bg-panel-muted);
      font: var(--lv-type-caption);
    }

    .pane-title {
      margin: 0;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
    }

    .pane-hint {
      margin: 0.25rem 0 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      line-height: 1.4;
    }

    .search {
      width: 100%;
      box-sizing: border-box;
      min-height: var(--control-small-size);
      margin-top: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: 0 var(--control-small-paddingInline-normal, var(--base-size-8));
      color: var(--lv-fg-default);
      background: var(--lv-bg-input, var(--lv-bg-control));
      font: var(--lv-type-body-compact);
    }

    .field-filter {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(4.5rem, 1fr));
      gap: var(--base-size-4);
      margin-top: var(--base-size-8);
    }

    .field-filter button {
      min-height: var(--control-small-size);
      min-width: 0;
      border: 1px solid transparent;
      border-radius: var(--lv-radius-full);
      padding: 0 var(--base-size-8);
      color: var(--lv-fg-muted);
      background: transparent;
      font: var(--lv-type-caption);
      cursor: pointer;
    }

    .field-filter button:hover {
      background: var(--lv-bg-panel-muted);
      color: var(--lv-fg-default);
    }

    .field-filter button[aria-pressed='true'] {
      border-color: var(--lv-line-default);
      color: var(--lv-fg-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
      font-weight: var(--base-text-weight-semibold);
    }

    .field-results {
      padding: var(--base-size-8) var(--base-size-12) var(--base-size-16);
    }

    .field-entity + .field-entity,
    .catalog-disclosure {
      margin-top: var(--base-size-8);
    }

    .field-entity {
      border-bottom: var(--lv-border-muted);
      padding-bottom: var(--base-size-8);
    }

    .field-entity summary,
    .catalog-disclosure > summary {
      display: flex;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-semibold);
      cursor: pointer;
    }

    .field-entity-title,
    .catalog-disclosure-title {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-entity-count,
    .catalog-disclosure-count {
      flex: 0 0 auto;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .field-entity > .field-list,
    .catalog-entity > .field-list {
      margin-top: var(--base-size-4);
    }

    .field-list {
      display: grid;
      gap: var(--base-size-2);
    }

    .field {
      display: grid;
      grid-template-columns: 1.25rem minmax(0, 1fr) auto;
      align-items: start;
      column-gap: var(--base-size-8);
      width: 100%;
      box-sizing: border-box;
      border: 1px solid transparent;
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      min-height: 2.65rem;
      padding: var(--base-size-6) var(--base-size-8);
      color: inherit;
      background: transparent;
      text-align: left;
      cursor: grab;
    }

    .field:hover {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-panel-muted);
    }

    .field:disabled {
      cursor: not-allowed;
      opacity: 0.65;
    }

    .field[data-used='true'] {
      background: var(--lv-bg-success-muted, var(--lv-bg-panel-muted));
    }

    .field[data-dragging='true'] {
      border-color: var(--lv-data-2);
      background: var(--lv-data-2-muted);
    }

    .field-role-icon {
      display: inline-flex;
      width: 1.1rem;
      height: 1.1rem;
      align-items: center;
      justify-content: center;
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
    }

    .field-role-icon svg {
      width: 1rem;
      height: 1rem;
      fill: none;
      stroke: currentColor;
      stroke-linecap: round;
      stroke-linejoin: round;
      stroke-width: 1.75;
    }

    .field-copy {
      min-width: 0;
    }

    .field-label {
      display: block;
      overflow: hidden;
      font: var(--lv-type-body-compact);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-context {
      display: block;
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .field-used {
      align-self: center;
      border-radius: var(--lv-radius-full);
      padding: var(--base-size-2) var(--base-size-6);
      color: var(--lv-fg-success, var(--lv-fg-default));
      background: var(--lv-bg-panel);
      font: var(--lv-type-caption);
      white-space: nowrap;
    }

    .catalog-disclosure {
      border-top: var(--lv-border-muted);
      padding-top: var(--base-size-8);
    }

    .catalog-disclosure > summary {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .catalog-disclosure-body {
      margin-top: var(--base-size-4);
    }

    .catalog-entity + .catalog-entity {
      margin-top: var(--base-size-8);
    }

    .catalog-entity-title {
      margin: 0 0 var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      font-weight: var(--base-text-weight-semibold);
    }

    .field-browser {
      min-height: 0;
    }

`
