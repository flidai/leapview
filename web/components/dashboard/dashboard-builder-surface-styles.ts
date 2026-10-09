import { css } from 'lit'
import { dashboardBuilderToolbarStyles } from './dashboard-builder-toolbar-styles'
import { dashboardBuilderFilterStyles } from './dashboard-builder-filter-styles'

export const dashboardBuilderSurfaceStyles = css`
    .saved-library-tabs { display:flex; gap:4px; padding:8px; border-bottom:var(--lv-border-default); }
    .saved-library-tabs[hidden], .saved-visuals-frame[hidden] { display:none; }
    .saved-library-tabs button { flex:1; padding:7px 4px; font:inherit; font-size: var(--text-body-size-small); color:var(--lv-fg-muted); background:transparent; border:0; border-radius:5px; cursor:pointer; }
    .saved-library-tabs button[aria-pressed='true'] { background:var(--lv-bg-control-active); color:var(--lv-fg-default); }
    .saved-visuals-frame { display:block; border:0; width:100%; min-width:0; flex:1; min-height:0; box-sizing:border-box; }
    .data-pane { display:flex; flex-direction:column; }
    .visual-import-frame { display:none; }

    :host {
      position: relative;
      display: block;
      min-height: 100svh;
      color: var(--lv-fg-default);
      background: var(--lv-bg-app);
      font-family: var(--fontStack-system);
    }

    .sr-only {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }

    .builder {
      display: grid;
      height: 100svh;
      min-height: 100svh;
      grid-template-rows: auto minmax(0, 1fr);
    }

    .toolbar {
      display: flex;
      align-items: center;
      gap: var(--base-size-12);
      min-height: var(--control-medium-size);
      padding: var(--base-size-8) var(--base-size-16);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-panel);
    }

    .terminal-failure {
      display: flex;
      align-items: center;
      flex-wrap: wrap;
      gap: var(--base-size-8);
      padding: var(--base-size-8) var(--base-size-16);
      border-bottom: var(--lv-border-muted);
      background: var(--lv-bg-danger-muted, var(--lv-bg-panel-muted));
      color: var(--lv-fg-danger, var(--lv-fg-default));
      font: var(--lv-type-body-compact);
    }

    .terminal-failure span {
      min-width: 0;
      flex: 1 1 18rem;
    }

    .back {
      color: inherit;
      font: var(--lv-type-body-compact);
      text-decoration: none;
      white-space: nowrap;
    }

    .back:focus-visible,
    button:focus-visible,
    input:focus-visible,
    [role='button']:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    ${dashboardBuilderToolbarStyles}

    .toolbar-actions {
      display: flex;
      align-items: center;
      gap: 0.4rem;
    }

    .magic-fill { display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
    .magic-fill svg { color: var(--lv-fg-accent); flex-shrink: 0; }
    .magic-fill-result { margin: 0; padding: 6px 12px; border-bottom: var(--lv-border-default); color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }

    @media (max-width: 640px) {
      .appearance-popover {
        position: fixed;
        top: calc(var(--control-medium-size) + var(--base-size-16));
        right: var(--base-size-8);
        left: var(--base-size-8);
        width: auto;
      }
    }

    .icon-action,
    .pane-collapse {
      width: var(--lv-button-height-xs, var(--control-xsmall-size));
      min-height: var(--lv-button-height-xs, var(--control-xsmall-size));
      flex: 0 0 auto;
      padding: 0;
      border-color: var(--lv-button-invisible-border-rest, var(--control-transparent-borderColor-rest));
      color: var(--lv-button-invisible-icon-rest, var(--lv-fg-muted));
      background: var(--lv-button-invisible-bg-rest, var(--control-transparent-bgColor-rest));
    }

    .icon-action:hover,
    .pane-collapse:hover {
      border-color: var(--lv-button-invisible-border-hover, var(--control-transparent-borderColor-hover));
      color: var(--lv-fg-default);
      background: var(--lv-button-invisible-bg-hover, var(--control-transparent-bgColor-hover));
    }

    button,
    .button {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--lv-button-padding-inline, var(--base-size-12));
      color: var(--lv-button-fg-rest);
      background: var(--lv-button-bg-rest);
      font: var(--lv-type-body-compact);
      text-decoration: none;
      cursor: pointer;
    }

    button:hover,
    .button:hover {
      background: var(--lv-button-bg-hover);
    }

    button.primary {
      border-color: var(--lv-button-accent-border-rest);
      color: var(--lv-button-accent-fg-rest);
      background: var(--lv-button-accent-bg-rest);
    }

    button.primary:hover {
      background: var(--lv-button-accent-bg-hover);
    }

    .more-actions {
      position: relative;
    }

    .more-actions summary {
      display: inline-flex;
      min-height: var(--control-medium-size);
      box-sizing: border-box;
      align-items: center;
      border: var(--lv-border-default);
      border-radius: var(--lv-button-radius, var(--lv-radius-default));
      padding: 0 var(--lv-button-padding-inline, var(--base-size-12));
      color: var(--lv-button-fg-rest);
      background: var(--lv-button-bg-rest);
      font: var(--lv-type-body-compact);
      cursor: pointer;
      list-style: none;
    }

    .more-actions summary::-webkit-details-marker {
      display: none;
    }

    .more-actions summary:focus-visible {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .more-actions summary:hover {
      background: var(--lv-button-bg-hover);
    }

    .more-menu {
      position: absolute;
      z-index: 2;
      top: calc(100% + var(--base-size-6));
      right: 0;
      display: grid;
      min-width: 10rem;
      gap: var(--base-size-2);
      padding: var(--base-size-4);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      box-shadow: var(--lv-shadow-floating-sm);
    }

    .more-menu button,
    .more-menu .button {
      justify-content: flex-start;
      width: 100%;
      border-color: transparent;
      background: transparent;
      text-align: left;
    }

    .more-menu button:hover,
    .more-menu .button:hover {
      background: var(--lv-bg-panel-muted);
    }

    .more-menu .archive-action,
    .more-menu .delete-action {
      color: var(--lv-fg-danger);
    }

    button:disabled {
      cursor: not-allowed;
      opacity: 0.55;
    }

    .body {
      display: grid;
      min-height: 0;
      grid-template-columns: minmax(0, 1fr) max-content;
      grid-template-rows: minmax(0, 1fr) auto;
    }

    .right-dock {
      display: grid;
      grid-column: 2;
      grid-row: 1 / span 2;
      min-width: 0;
      min-height: 0;
      grid-template-columns: var(--dock-filters-width) var(--dock-visuals-width) var(--dock-data-width) var(--dock-agent-width);
      background: var(--lv-bg-panel);
    }

    .pane {
      position: relative;
      min-width: 0;
      min-height: 0;
      overflow: auto;
      background: var(--lv-bg-panel);
    }

    .properties {
      border-left: var(--lv-border-muted);
    }

    .filters-pane {
      border-left: var(--lv-border-muted);
    }

    .data-pane,
    .agent-pane {
      border-left: var(--lv-border-muted);
    }

    .agent-pane {
      display: grid;
      overflow: hidden;
      grid-template-rows: auto minmax(0, 1fr);
    }

    .agent-pane[data-collapsed='true'] {
      display: block;
    }

    .agent-pane-content {
      min-height: 0;
      overflow: hidden;
    }

    .agent-pane-content lv-chat-drawer {
      height: 100%;
      min-height: 0;
    }

    .filter-pane-body {
      display: grid;
      align-content: start;
      gap: var(--base-size-8);
      padding: var(--base-size-8) var(--base-size-12) var(--base-size-16);
    }

    ${dashboardBuilderFilterStyles}

    .filter-scope-options {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: var(--base-size-4);
    }

    .filter-scope-option {
      position: relative;
      display: flex !important;
      min-height: var(--control-small-size);
      align-items: center;
      justify-content: center;
      box-sizing: border-box;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      padding: 0 var(--base-size-6);
      color: var(--lv-fg-default) !important;
      background: var(--lv-bg-panel);
      cursor: pointer;
    }

    .filter-scope-option:has(input:checked) {
      border-color: var(--lv-line-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
      font-weight: var(--base-text-weight-semibold);
    }

    .filter-scope-option:has(input:disabled) {
      cursor: not-allowed;
      opacity: 0.55;
    }

    .filter-scope-option input {
      position: absolute;
      width: 1px;
      height: 1px;
      opacity: 0;
    }

    .filter-scope-option:has(input:focus-visible) {
      outline: 2px solid var(--lv-fg-accent);
      outline-offset: 2px;
    }

    .filter-scope-option:has(input:checked) span { font-weight: var(--base-text-weight-semibold); }

    .filter-scope-option span {
      font: var(--lv-type-caption);
      text-align: center;
    }

    .filter-card {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      width: 100%;
      min-height: var(--control-medium-size);
      align-items: center;
      gap: var(--base-size-8);
      padding: var(--base-size-6) var(--base-size-8);
      border: 1px solid transparent;
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      color: var(--lv-fg-default);
      background: transparent;
      text-align: left;
    }

    .filter-card[aria-pressed='true'] {
      border-color: var(--lv-line-default);
      background: var(--lv-bg-control, var(--lv-bg-panel-muted));
    }

    .filter-card-preview {
      min-width: 0;
      border-radius: var(--lv-radius-default);
    }

    .filter-card-preview lv-filter-pane-card {
      display: block;
      width: 100%;
    }

    .filter-card-title {
      overflow: hidden;
      font: var(--lv-type-body-compact);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .filter-card-meta {
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .filter-editor {
      container-type: inline-size;
      min-width: 0;
      display: grid;
      gap: var(--base-size-8);
      margin-top: var(--base-size-4);
      padding-top: var(--base-size-12);
      border-top: var(--lv-border-muted);
    }

    .filter-editor label {
      min-width: 0;
      display: grid;
      gap: var(--base-size-4);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .filter-editor .filter-toggle {
      display: flex;
      align-items: center;
      justify-content: space-between;
    }

    .filter-add-trigger { width: 100%; text-align: left; }
    .filter-add-menu {
      display: grid; gap: 6px; width: 100%; min-width: 0; max-width: 100%; padding: 6px;
      box-sizing: border-box; border: var(--lv-border-muted); border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel); color: var(--lv-fg-default);
    }
    .filter-add-menu[hidden] { display: none; }
    .filter-add-search { width: 100%; min-width: 0; box-sizing: border-box; min-height: 30px; padding: 4px 6px; font-size: var(--text-body-size-small); border: var(--lv-border-default); border-radius: 4px; background: var(--lv-bg-control); color: var(--lv-fg-default); }
    .filter-add-options { min-width: 0; max-height: min(240px, 35vh); overflow-y: auto; overscroll-behavior: contain; scrollbar-width: thin; }
    .filter-add-menu .filter-add-option {
      display: block; width: 100%; min-width: 0; height: auto; min-height: 30px; padding: 5px 6px;
      text-align: left; white-space: normal; overflow-wrap: anywhere; border: 0; box-shadow: none;
      background: transparent; border-radius: 4px; font-size: var(--text-body-size-small); line-height: 18px; font-weight: var(--base-text-weight-normal);
    }
    .filter-add-menu .filter-add-option:hover:not(:disabled), .filter-add-menu .filter-add-option:focus-visible { background: var(--lv-bg-control-hover); }
    .filter-add-menu .filter-add-option:focus-visible { outline-offset: -2px; }
    .filter-add-empty { margin: 0; padding: 6px; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .filter-settings {
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-small, var(--lv-radius-default));
      background: var(--lv-bg-panel);
    }

    .filter-settings summary {
      padding: var(--base-size-8);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-compact);
      cursor: pointer;
    }

    .filter-settings-body {
      display: grid;
      gap: var(--base-size-8);
      padding: 0 var(--base-size-8) var(--base-size-8);
    }

    .filter-editor-actions {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: var(--base-size-6);
    }

`
