import { css } from 'lit'

export const filterControlStyles = css`
    :host { display: block; min-width: 0; font: inherit; }
    fieldset { position: relative; display: grid; min-width: 0; gap: var(--base-size-6); border: 0; margin: 0; padding: 0; }
    fieldset.list.bounded {
      height: 100%;
      min-height: 0;
      grid-template-rows: auto minmax(0, 1fr) auto;
      box-sizing: border-box;
    }
    legend.visually-hidden {
      position: absolute;
      width: 1px;
      height: 1px;
      overflow: hidden;
      clip: rect(0 0 0 0);
      clip-path: inset(50%);
      white-space: nowrap;
    }
    .field-heading {
      display: flex;
      min-width: 0;
      align-items: baseline;
      justify-content: space-between;
      gap: var(--base-size-6);
      font: var(--lv-type-caption);
    }
    .field-heading[data-title='false'] { justify-content: flex-end; }
    .field-title {
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-weight: var(--base-text-weight-medium);
    }
    .filter-clear {
      flex: 0 0 auto;
      border: 0;
      border-radius: var(--lv-radius-tight, var(--lv-radius-default));
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0 var(--base-size-6);
      font: var(--lv-type-caption);
    }
    .filter-clear[data-active='false'] { display: none; }
    .filter-clear { min-height: var(--control-xsmall-size); }
    .filter-clear:hover:not(:disabled) { background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
    .filter-clear:disabled { cursor: default; opacity: .45; }
    input, select, button {
      min-height: var(--control-medium-size);
      font: var(--lv-type-body-compact);
    }
    input, select {
      width: 100%; min-width: 0; border: var(--lv-border-default);
      border-radius: var(--lv-radius-default); background: var(--lv-bg-panel);
      color: inherit; padding-inline: var(--base-size-8); box-sizing: border-box;
    }
    .simple-dropdown { display: grid; min-width: 0; gap: var(--base-size-4); }
    .dropdown-trigger {
      display: flex;
      width: 100%;
      min-width: 0;
      align-items: center;
      justify-content: space-between;
      gap: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: inherit;
      cursor: pointer;
      padding: 0 var(--base-size-8);
      text-align: left;
    }
    .dropdown-trigger:hover { background: var(--lv-bg-control-hover); }
    .dropdown-trigger svg { width: var(--base-size-16); height: var(--base-size-16); flex: 0 0 auto; transition: transform var(--lv-duration-fast); }
    .dropdown-trigger[aria-expanded='true'] svg { transform: rotate(180deg); }
    .dropdown-value { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .dropdown-popover {
      position: fixed;
      inset: auto;
      top: 0;
      left: 0;
      display: none;
      width: 240px;
      max-width: calc(100vw - var(--base-size-16));
      max-height: 320px;
      box-sizing: border-box;
      grid-template-rows: auto minmax(0, 1fr);
      gap: var(--base-size-6);
      overflow: hidden;
      margin: 0;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-overlay, var(--lv-bg-panel));
      color: var(--lv-fg-default);
      box-shadow: var(--shadow-floating-small);
      padding: var(--base-size-8);
    }
    .dropdown-popover:popover-open { display: grid; }
    .dropdown-popover[data-toolbar='false'] { grid-template-rows: minmax(0, 1fr); }
    .dropdown-toolbar {
      display: grid;
      min-width: 0;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--base-size-6);
    }
    .dropdown-search { position: relative; display: flex; align-items: center; }
    .dropdown-search svg { position: absolute; left: var(--base-size-8); width: var(--base-size-16); height: var(--base-size-16); color: var(--lv-fg-muted); pointer-events: none; }
    .dropdown-search input { padding-left: calc(var(--base-size-16) + var(--base-size-12)); }
    .dropdown-clear {
      grid-column: 2;
      justify-self: end;
      border: 0;
      border-radius: var(--lv-radius-tight, var(--lv-radius-default));
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0 var(--base-size-8);
    }
    .dropdown-clear:hover:not(:disabled) { background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
    .dropdown-clear:disabled { cursor: default; opacity: .45; }
    .dropdown-options { min-height: 0; overflow: auto; }
    .dropdown-option {
      display: grid;
      min-height: var(--control-medium-size);
      grid-template-columns: auto minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--base-size-8);
      border-radius: var(--lv-radius-tight, var(--lv-radius-default));
      cursor: pointer;
      padding: 0 var(--base-size-8);
      font: var(--lv-type-body-compact);
    }
    .dropdown-option:hover { background: var(--lv-bg-control-hover); }
    .dropdown-option input { width: auto; min-height: 0; margin: 0; }
    .dropdown-option-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .dropdown-option-count { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .dropdown-empty { margin: 0; color: var(--lv-fg-muted); padding: var(--base-size-8); font: var(--lv-type-caption); }
    .load-more-options { width: 100%; border: 0; border-radius: var(--lv-radius-tight, var(--lv-radius-default)); background: transparent; color: var(--lv-fg-link, var(--lv-accent)); cursor: pointer; padding: var(--base-size-6) var(--base-size-8); text-align: left; font: var(--lv-type-caption); }
    .load-more-options:hover:not(:disabled) { background: var(--lv-bg-control-hover); }
    .load-more-options:disabled { cursor: default; opacity: .55; }
    .options { display: grid; max-height: 220px; gap: 2px; overflow: auto; }
    fieldset.list.bounded .options { min-height: 0; max-height: 100%; }
    .option { display: flex; align-items: center; gap: 8px; border-radius: 4px; padding: 4px; }
    .option[data-unavailable='true'] { color: var(--lv-fg-muted); }
    .option input { width: auto; min-height: 0; }
    .buttons { display: flex; flex-wrap: wrap; gap: 4px; }
    .buttons button { border: var(--lv-border-default); border-radius: var(--lv-radius-default); padding: 0 var(--base-size-8); background: var(--lv-bg-panel); color: inherit; cursor: pointer; }
    .buttons button:hover:not(:disabled) { background: var(--lv-bg-control-hover); }
    .buttons button[aria-pressed='true'] { border-color: var(--lv-line-accent); background: var(--lv-bg-accent-muted, var(--bgColor-accent-muted)); }
    .buttons button:disabled { opacity: .55; cursor: default; }
    .range { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; }
    :host([data-layout-variant='stacked']) .range { grid-template-columns: minmax(0, 1fr); }
    .range label { display: grid; min-width: 0; gap: var(--base-size-4); }
    .range-error {
      grid-column: 1 / -1;
      margin: 0;
      color: var(--lv-fg-danger, var(--fgColor-danger));
      font: var(--lv-type-caption);
    }
    .field-label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }
    .input-control { display: grid; gap: var(--base-size-4); }
    .operator {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }
    .relative { display: grid; grid-template-columns: 1fr 72px 1fr; gap: 6px; }
    :host([data-layout-variant='stacked']) .relative { grid-template-columns: minmax(0, 1fr); }
    .status,
    .selection-summary {
      min-width: 0;
      overflow: hidden;
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .status { flex: 0 1 auto; }
    :host([pending]) fieldset { opacity: .78; }
    button:focus-visible, input:focus-visible, select:focus-visible { outline: var(--lv-border-width-focus) solid var(--lv-accent); outline-offset: var(--base-size-2); }
`
