import { css } from 'lit'

export const tableStyles = css`
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
  a.primary { display: inline-flex; box-sizing: border-box; min-height: var(--lv-control-small); align-items: center; justify-content: center; border: var(--lv-border-width) solid var(--lv-button-accent-border-rest); border-radius: var(--lv-radius-small); padding: var(--base-size-4) var(--base-size-8); }
  a.primary:hover { text-decoration: none; }
  .password-result { display: grid; gap: var(--base-size-12); }
  .password-value { display: block; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel-muted); padding: var(--base-size-12); font-family: var(--fontStack-monospace); overflow-wrap: anywhere; user-select: all; }
  .secret-result { display: grid; gap: var(--base-size-8); border: var(--lv-border-width) solid var(--lv-line-success-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-success-muted); padding: var(--base-size-12); }
  .secret-result code { display: block; overflow-wrap: anywhere; user-select: all; }
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
  .activity-list { display: grid; gap: 0; margin: 0; padding: 0; list-style: none; }
  .activity-item { display: grid; grid-template-columns: 10px minmax(0, 1fr) auto; align-items: start; gap: var(--base-size-8); border-bottom: var(--lv-border-muted); padding: var(--base-size-8) 0; }
  .activity-item:last-child { border-bottom: 0; }
  .activity-dot { width: 8px; height: 8px; margin-top: 6px; border-radius: 50%; background: var(--lv-fg-muted); }
  .activity-copy { display: grid; gap: var(--base-size-2); }
  .detail-user-avatar { --lv-user-avatar-size: 100%; width: 100%; height: 100%; }
  .audit-row { cursor: pointer; }
  .audit-table th:first-child,
  .audit-table td:first-child { min-width: 9rem; white-space: nowrap; }
  .audit-row:hover, .audit-row:focus-visible { background: var(--lv-bg-control-hover); outline: 0; }
  .audit-row td:nth-child(2), .audit-row td:nth-child(3), .audit-row td:nth-child(4) { max-width: 14rem; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .audit-detail { width: min(36rem, calc(100vw - var(--base-size-32))); }
  .audit-detail-grid { display: grid; gap: var(--base-size-8); }
  .audit-detail-grid div { display: grid; grid-template-columns: minmax(7rem, .45fr) minmax(0, 1fr); gap: var(--base-size-12); }
  .audit-detail-grid dt { color: var(--lv-fg-muted); }
  .audit-detail-grid dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
  @media (max-width: 760px) {
    .detail-section { padding-block: var(--base-size-20); }
    .detail-empty-row { grid-template-columns: minmax(6.5rem, 0.7fr) minmax(0, 1.3fr); }
    .activity-item { grid-template-columns: 10px minmax(0, 1fr); }
    .activity-item > time { grid-column: 2; }
  }
  @media (max-width: 480px) {
    .detail-empty-row { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); }
  }
`
