import { css } from 'lit'

export const settingsSurfaceStyles = css`
  :host { display: block; color: var(--lv-fg-default); font: var(--lv-type-body); font-family: var(--fontStack-system); }
  .surface { display: grid; gap: 12px; min-width: 0; }
  h2, h3, p { margin: 0; }
  h2 { font: var(--lv-type-section-title); }
  h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
  .muted { color: var(--lv-fg-muted); }
  .error { color: var(--lv-fg-danger); }
  .table-wrap { overflow-x: auto; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); }
  table { width: 100%; min-width: 620px; border-collapse: collapse; }
  th, td { padding: var(--base-size-8) var(--base-size-12); text-align: left; border-bottom: var(--lv-border-muted); vertical-align: top; }
  th { color: var(--lv-fg-muted); font: var(--lv-type-caption); text-transform: uppercase; letter-spacing: .03em; }
  tbody tr:last-child td { border-bottom: 0; }
  a { color: var(--lv-fg-link); text-decoration: none; }
  a:hover { text-decoration: underline; }
  button, input, select { box-sizing: border-box; min-height: var(--lv-control-small); border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-bg-control); color: inherit; padding: var(--base-size-4) var(--base-size-8); font: inherit; }
  button { cursor: pointer; }
  button[disabled] { cursor: default; opacity: .55; }
  .toolbar, .form { display: flex; flex-wrap: wrap; align-items: end; gap: 8px; }
  label { display: grid; gap: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .empty { padding: var(--base-size-20) var(--base-size-12); color: var(--lv-fg-muted); }
  .actions { display: flex; flex-wrap: wrap; gap: 6px; }
  .notice { border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); }
  .danger { color: var(--lv-fg-danger); }
  code { overflow-wrap: anywhere; }
  dialog { width: min(30rem, calc(100vw - var(--base-size-32))); max-width: none; max-height: calc(100svh - var(--base-size-32)); overflow: auto; border: 0; border-radius: var(--lv-radius-large); background: transparent; color: inherit; padding: 0; }
  dialog::backdrop { background: var(--lv-modal-backdrop); }
  .modal { display: grid; overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); }
  .modal-header { display: flex; align-items: start; justify-content: space-between; gap: var(--base-size-16); border-bottom: var(--lv-border-muted); padding: var(--base-size-16) var(--base-size-20); }
  .modal-title { display: grid; gap: var(--base-size-4); }
  .modal-title h2 { font: var(--lv-type-section-title); }
  .modal-close { display: inline-flex; width: var(--control-medium-size); min-height: var(--control-medium-size); align-items: center; justify-content: center; border-color: transparent; background: transparent; color: var(--lv-fg-muted); padding: 0; }
  .modal-close:hover { border-color: var(--lv-line-muted); background: var(--lv-bg-control-hover); color: var(--lv-fg-default); }
  .modal-body { display: grid; gap: var(--base-size-16); padding: var(--base-size-20); }
  .modal-body .form { display: grid; align-items: stretch; }
  .modal-body input, .modal-body select { width: 100%; min-height: var(--control-medium-size); }
  .modal-actions { display: flex; justify-content: flex-end; gap: var(--base-size-8); }
  .primary { border-color: var(--lv-button-accent-border-rest); background: var(--lv-button-accent-bg-rest); color: var(--lv-button-accent-fg-rest); }
  .primary:hover { border-color: var(--lv-button-accent-border-hover); background: var(--lv-button-accent-bg-hover); }
  .password-result { display: grid; gap: var(--base-size-12); }
  .password-value { display: block; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); font-family: var(--fontStack-monospace); overflow-wrap: anywhere; user-select: all; }
  .status-active { color: var(--lv-fg-success); }
  .status-blocked, .status-disabled { color: var(--lv-fg-danger); }
  .primary-detail-action { display: inline-flex; align-items: center; gap: var(--base-size-6); color: var(--lv-fg-link); }
  .action-menu { position: relative; }
  .action-menu summary { display: inline-flex; min-height: var(--lv-control-small); box-sizing: border-box; align-items: center; border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-bg-control); padding: var(--base-size-4) var(--base-size-8); cursor: pointer; list-style: none; }
  .action-menu summary::-webkit-details-marker { display: none; }
  .action-menu[open] summary { background: var(--lv-bg-control-hover); }
  .action-menu-popover { position: absolute; z-index: 2; top: calc(100% + var(--base-size-4)); right: 0; display: grid; width: max-content; min-width: 180px; gap: var(--base-size-4); border: var(--lv-border-default); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-6); }
  .action-menu-popover button { width: 100%; border-color: transparent; background: transparent; text-align: left; }
  .action-menu-popover button:hover { background: var(--lv-bg-control-hover); }
  .detail-section { display: grid; min-width: 0; align-content: start; gap: var(--base-size-16); border-top: var(--lv-border-muted); padding: var(--base-size-24) 0; }
  .detail-section .table-wrap { border: 0; border-radius: 0; }
  .detail-section table { min-width: 540px; }
  .detail-section th, .detail-section td { padding-inline: 0 var(--base-size-16); }
  .detail-section table.member-table { min-width: 0; }
  .member-table th:last-child, .member-table td:last-child { width: 1%; padding-right: 0; text-align: right; white-space: nowrap; }
  .member-table td:nth-child(2) { overflow-wrap: anywhere; }
  .card-header { display: flex; align-items: start; justify-content: space-between; gap: var(--base-size-12); }
  .card-header-copy { display: grid; gap: var(--base-size-4); }
  .section-heading { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-12); }
  .section-action { display: inline-flex; align-items: center; gap: var(--base-size-6); }
  .inline-value { display: flex; min-width: 0; align-items: center; gap: var(--base-size-6); }
  .inline-value code { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .text-button { min-height: auto; flex: 0 0 auto; border-color: transparent; background: transparent; color: var(--lv-fg-link); padding: var(--base-size-2); }
  .role-source { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .detail-subsection { display: grid; gap: var(--base-size-12); }
  .detail-empty-row { display: grid; grid-template-columns: minmax(10rem, 0.45fr) minmax(0, 1fr); gap: var(--base-size-16); color: var(--lv-fg-muted); }
  .detail-empty-row strong { color: var(--lv-fg-muted); font-weight: var(--base-text-weight-normal); }
  .detail-form { width: fit-content; }
  .audit-surface { gap: var(--base-size-16); }
  .audit-toolbar { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(12rem, 100%), 1fr)); align-items: end; gap: var(--base-size-8); }
  .audit-filter { display: grid; min-width: 0; gap: var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .audit-filter input { width: 100%; min-width: 0; min-height: var(--lv-control-medium); box-sizing: border-box; border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-bg-input); color: var(--lv-fg-default); padding: 0 var(--lv-space-control); font: var(--lv-type-body-compact); }
  .audit-filter input:focus-visible { border-color: var(--lv-border-accent); outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
  .audit-filter lv-select-menu { width: 100%; }
  .audit-actions { display: flex; flex-wrap: wrap; align-items: end; gap: var(--base-size-8); }
  .audit-actions button, .audit-load-more { min-height: var(--lv-control-medium); border: var(--lv-border-default); border-radius: var(--lv-radius-small); background: var(--lv-button-bg-rest, var(--lv-bg-control)); color: var(--lv-fg-default); cursor: pointer; padding: 0 var(--lv-space-control); font: var(--lv-type-body-compact); }
  .audit-actions button:hover, .audit-load-more:hover { background: var(--lv-bg-control-hover); }
  .audit-actions button[disabled], .audit-load-more[disabled] { cursor: default; opacity: .55; }
  .audit-actions .primary { border-color: var(--lv-button-accent-border-rest); background: var(--lv-button-accent-bg-rest); color: var(--lv-button-accent-fg-rest); }
  .audit-presets { display: flex; flex-wrap: wrap; align-items: center; gap: var(--base-size-6); }
  .audit-presets-label { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .audit-preset { min-height: var(--lv-control-small); border: var(--lv-border-transparent); border-radius: 999px; background: var(--lv-bg-control); color: var(--lv-fg-default); padding: 0 var(--lv-space-control); font: var(--lv-type-body-compact); }
  .audit-preset:hover { background: var(--lv-bg-control-hover); }
  .audit-preset[aria-pressed='true'] { border-color: var(--lv-border-accent); background: var(--lv-bg-accent-muted); color: var(--lv-fg-accent); }
  .audit-preset[disabled] { cursor: default; opacity: .55; }
  .audit-results { min-width: 0; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); }
  .audit-footer { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: var(--base-size-8); border-top: var(--lv-border-muted); padding: var(--base-size-8) var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .audit-footer .error { color: var(--lv-fg-danger); }
  .audit-footer .audit-load-more { color: var(--lv-fg-default); font: var(--lv-type-caption); }
  .service-page { display: grid; min-width: 0; gap: var(--base-size-24); }
  .service-feedback { display: grid; gap: var(--base-size-8); }
  .service-detail-action { display: inline-flex; min-height: var(--control-medium-size); align-items: center; gap: var(--base-size-6); }
  lv-one-time-secret { margin-top: var(--base-size-24); }
  .service-dialog-field { display: grid; gap: var(--base-size-6); color: var(--lv-fg-default); font: var(--lv-type-body-compact); }
  .service-dialog-field > span:first-child { font-weight: var(--base-text-weight-semibold); }
  .service-expiration-control { width: fit-content; max-width: 100%; }
  .service-delete-warning { border-block: 1px solid var(--lv-fg-warning); background: var(--lv-bg-warning-muted, var(--lv-bg-panel-muted)); padding: var(--base-size-16) var(--base-size-20); line-height: 1.5; }
  .service-delete-actions { display: flex; justify-content: flex-end; gap: var(--base-size-8); padding: var(--base-size-16) var(--base-size-20); }
  .activity-list { display: grid; gap: 0; margin: 0; padding: 0; list-style: none; }
  .activity-item { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto; align-items: start; gap: var(--base-size-8); border-bottom: var(--lv-border-muted); padding: var(--base-size-8) 0; }
  .activity-item:last-child { border-bottom: 0; }
  .activity-dot { width: 8px; height: 8px; margin-top: 6px; border-radius: 50%; background: var(--lv-fg-muted); }
  .activity-copy { display: grid; gap: var(--base-size-2); }
  .audit-drawer-title { display: grid; min-width: 0; gap: var(--base-size-4); }
  .audit-drawer-title h2, .audit-drawer-title p { margin: 0; }
  .audit-drawer-title h2 { overflow-wrap: anywhere; font: var(--lv-type-section-title); }
  .audit-drawer-title p { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); overflow-wrap: anywhere; }
  .audit-drawer-body { display: grid; gap: var(--base-size-20); min-width: 0; }
  .audit-drawer-section { display: grid; gap: var(--lv-space-control); min-width: 0; }
  .audit-drawer-section h3 { margin: 0; font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
  .audit-drawer-facts { display: grid; gap: var(--lv-space-control); margin: 0; }
  .audit-drawer-fact { display: grid; grid-template-columns: minmax(7rem, .5fr) minmax(0, 1fr); gap: var(--base-size-12); align-items: start; margin: 0; }
  .audit-drawer-fact dt { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .audit-drawer-fact dd { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: var(--base-size-6); margin: 0; overflow-wrap: anywhere; }
  .audit-drawer-fact dd > code { min-width: 0; overflow-wrap: anywhere; }
  .audit-drawer-fact a { min-width: 0; overflow-wrap: anywhere; }
  .audit-drawer-copy { min-height: auto; border-color: var(--lv-border-transparent); background: transparent; color: var(--lv-fg-link); padding: var(--base-size-2); font: var(--lv-type-caption); }
  .audit-drawer-copy:hover { border-color: var(--lv-line-muted); background: var(--lv-bg-control-hover); }
  .audit-drawer-status { font-weight: var(--base-text-weight-semibold); }
  .audit-drawer-status-success { color: var(--lv-fg-success); }
  .audit-drawer-status-danger { color: var(--lv-fg-danger); }
  .audit-drawer-status-attention { color: var(--lv-fg-warning); }
  .audit-drawer-metadata { border-top: var(--lv-border-muted); padding-top: var(--base-size-12); }
  .audit-drawer-metadata summary { cursor: pointer; color: var(--lv-fg-link); font-weight: var(--base-text-weight-semibold); }
  .audit-drawer-metadata pre { max-width: 100%; box-sizing: border-box; overflow: auto; border: var(--lv-border-muted); border-radius: var(--lv-radius-small); background: var(--lv-bg-panel-muted); padding: var(--lv-space-control); white-space: pre-wrap; overflow-wrap: anywhere; }
  .audit-row-hint { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
  .detail-user-avatar { --lv-user-avatar-size: 100%; width: 100%; height: 100%; }
  @media (max-width: 760px) {
    .detail-section { padding-block: var(--base-size-20); }
    .detail-empty-row { grid-template-columns: minmax(6.5rem, 0.7fr) minmax(0, 1.3fr); }
    .activity-item { grid-template-columns: 10px minmax(0, 1fr); }
    .activity-item > time { grid-column: 2; }
    .audit-toolbar { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .audit-actions { grid-column: 1 / -1; }
  }
  @media (max-width: 480px) {
    .detail-empty-row { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); }
    .audit-toolbar { grid-template-columns: minmax(0, 1fr); }
    .audit-actions { grid-column: auto; }
    .audit-actions button { flex: 1 1 8rem; }
    .audit-presets { align-items: stretch; }
    .audit-presets-label { flex-basis: 100%; }
    .audit-preset { flex: 1 1 8rem; }
    .audit-drawer-fact { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); }
    .audit-footer { align-items: stretch; }
    .audit-footer .audit-load-more { width: 100%; }
  }
`
