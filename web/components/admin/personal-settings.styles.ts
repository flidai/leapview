import { css } from 'lit'

export const personalSettingsStyles = css`
    :host { display: block; color: var(--lv-fg-default); font: var(--lv-type-body); }
    .settings { display: grid; gap: var(--base-size-20); width: 100%; min-width: 0; }
    section { display: grid; gap: var(--base-size-20); }
    h2, h3, p { margin: 0; }
    h2 { font: var(--lv-type-section-title); }
    h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .card { display: grid; gap: 0; overflow: visible; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); }
    .row { display: grid; min-height: var(--base-size-48); box-sizing: border-box; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-16); padding: var(--base-size-8) var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .row:first-child { border-radius: var(--lv-radius-large) var(--lv-radius-large) 0 0; }
    .row:last-child { border-bottom: 0; }
    .profile-row { min-height: var(--base-size-64); padding: var(--base-size-12) var(--base-size-20); }
    .account-section { gap: var(--base-size-12); }
    .account-row { min-height: var(--base-size-64); padding: var(--base-size-12) var(--base-size-20); }
    .account-id { max-width: 22rem; overflow: hidden; color: var(--lv-fg-muted); font: var(--lv-type-caption); text-overflow: ellipsis; white-space: nowrap; }
    .profile-email { max-width: 22rem; justify-self: end; text-align: right; }
    .profile-name-form { min-width: 0; justify-self: end; }
    .profile-name-control { display: flex; min-width: 0; align-items: center; justify-content: flex-end; gap: var(--base-size-8); }
    .profile-name-control input { width: min(13rem, 40vw); min-height: var(--control-medium-size, var(--base-size-32)); text-align: center; font: var(--lv-type-body); }
    .muted { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    input, select { min-width: 0; min-height: var(--control-small-size); box-sizing: border-box; border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: 0 var(--control-small-paddingInline-normal); color: var(--lv-fg-default); background: var(--lv-bg-input); font: var(--lv-type-body-compact); }
    button { min-height: var(--control-small-size); border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: 0 var(--control-small-paddingInline-normal); color: var(--lv-fg-default); background: var(--lv-button-bg-rest); cursor: pointer; font: var(--lv-type-body-compact); }
    button.primary { color: var(--lv-fg-on-accent); border-color: var(--lv-bg-accent); background: var(--lv-bg-accent); }
    .button-link { display: inline-flex; min-height: var(--control-small-size); box-sizing: border-box; align-items: center; justify-content: center; border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: 0 var(--control-small-paddingInline-normal); color: var(--lv-fg-default); background: var(--lv-button-bg-rest); text-decoration: none; font: var(--lv-type-body-compact); }
    .button-link.primary { color: var(--lv-fg-on-accent); border-color: var(--lv-bg-accent); background: var(--lv-bg-accent); }
    button.danger { color: var(--lv-fg-danger); }
    button:disabled { cursor: not-allowed; opacity: .55; }
    form { display: grid; gap: var(--base-size-8); }
    .form-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(12rem, 1fr)); gap: var(--base-size-8); }
    .actions { display: flex; flex-wrap: wrap; gap: var(--base-size-8); align-items: center; }
    .security-page { gap: var(--base-size-32); }
    .security-section { display: grid; gap: var(--base-size-12); }
    .security-section-heading { display: flex; min-width: 0; align-items: end; justify-content: space-between; gap: var(--base-size-16); }
    .security-session-actions { display: flex; min-width: 0; align-items: end; justify-content: end; gap: var(--base-size-8); }
    .security-section-heading-copy { display: grid; min-width: 0; gap: var(--base-size-4); }
    .security-section-heading h2 { font: var(--lv-type-section-title); }
    .security-session-count { flex: 0 0 auto; color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .security-password-row { display: grid; min-width: 0; grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-16); border-top: var(--lv-border-muted); border-bottom: var(--lv-border-muted); padding: var(--base-size-16) 0; }
    .security-password-copy { display: grid; min-width: 0; gap: var(--base-size-4); }
    .security-password-state { color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }
    .security-session-list { display: grid; overflow: hidden; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); }
    .security-session-group { display: grid; }
    .security-session-group + .security-session-group { border-top: var(--lv-border-muted); }
    .security-session-group-label { padding: var(--base-size-8) var(--base-size-16); color: var(--lv-fg-muted); background: var(--lv-bg-panel-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); letter-spacing: .035em; text-transform: uppercase; }
    .security-session { display: grid; min-width: 0; grid-template-columns: var(--base-size-32) minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-12); padding: var(--base-size-12) var(--base-size-16); border-top: var(--lv-border-muted); }
    .security-session-icon { display: grid; width: var(--base-size-32); height: var(--base-size-32); place-items: center; border-radius: var(--lv-radius-full); color: var(--lv-fg-muted); background: var(--lv-bg-control); }
    .security-session-icon svg { width: var(--base-size-16); height: var(--base-size-16); }
    .security-session-main { display: grid; min-width: 0; gap: var(--base-size-4); border: 0; background: transparent; padding: var(--base-size-4); text-align: left; }
    .security-session-main:hover, .security-session-main:focus-visible { background: var(--lv-bg-control-hover); outline: 0; }
    .security-session-title { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: var(--base-size-8); }
    .security-session-title strong { overflow-wrap: anywhere; }
    .security-session-meta { color: var(--lv-fg-muted); font: var(--lv-type-caption); line-height: var(--base-text-lineHeight-snug); }
    .security-badge { display: inline-flex; align-items: center; border-radius: var(--lv-radius-full); padding: var(--base-size-2) var(--base-size-8); color: var(--lv-fg-success); background: var(--lv-bg-success-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); }
    .security-empty { grid-template-columns: minmax(0, 1fr); min-height: var(--base-size-48); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .session-drawer-title, .session-drawer-body, .session-drawer-section { display: grid; min-width: 0; gap: var(--base-size-8); }
    .session-drawer-title h2, .session-drawer-title p, .session-drawer-section h3 { margin: 0; }
    .session-drawer-title h2 { font: var(--lv-type-section-title); overflow-wrap: anywhere; }
    .session-drawer-title p { color: var(--lv-fg-muted); font: var(--lv-type-body-compact); }
    .session-drawer-body { gap: var(--base-size-24); }
    .session-drawer-section h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .session-drawer-facts { display: grid; gap: var(--base-size-12); margin: 0; }
    .session-drawer-fact { display: grid; grid-template-columns: minmax(7rem, .5fr) minmax(0, 1fr); gap: var(--base-size-12); margin: 0; }
    .session-drawer-fact dt { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .session-drawer-fact dd { min-width: 0; margin: 0; overflow-wrap: anywhere; }
    .session-drawer-fact code { font: var(--lv-type-code-block); }
    .password-fields { display: grid; gap: var(--base-size-12); }
    .password-field { display: grid; gap: var(--base-size-4); }
    .password-field input { width: 100%; min-height: var(--control-large-size, var(--base-size-40)); }
    .token-page { gap: var(--base-size-24); }
    .token-page-header { display: flex; min-width: 0; align-items: center; justify-content: space-between; gap: var(--base-size-16); padding-bottom: var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .token-page-header h2 { font: var(--lv-type-page-title); }
    .token-page-heading { display: grid; min-width: 0; gap: var(--base-size-4); }
    .token-page-heading h2 { font: var(--lv-type-page-title); }
    .token-page-intro { max-width: 52rem; color: var(--lv-fg-muted); }
    .token-create-header { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: center; gap: var(--base-size-12); padding-bottom: var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .token-back { display: inline-grid; width: var(--control-medium-size); min-height: var(--control-medium-size); box-sizing: border-box; place-items: center; border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: 0; color: var(--lv-fg-default); background: var(--lv-button-bg-rest); }
    .token-back svg, .token-row-icon svg { width: var(--base-size-16); height: var(--base-size-16); }
    .token-form { width: 100%; gap: var(--base-size-32); }
    .token-details { display: grid; width: min(100%, 52rem); gap: var(--base-size-24); }
    .token-field { display: grid; min-width: 0; align-content: start; gap: var(--base-size-6); }
    .token-field input, .token-field select, .token-field textarea { width: 100%; }
    .token-field input, .token-field select { min-height: var(--control-xlarge-size, var(--base-size-40)); }
    .token-field textarea { min-height: 8rem; box-sizing: border-box; resize: vertical; border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: var(--base-size-8) var(--control-small-paddingInline-normal); color: var(--lv-fg-default); background: var(--lv-bg-input); font: var(--lv-type-body-compact); }
    .token-expiration-field { width: fit-content; max-width: 100%; }
    lv-select-menu { max-width: 100%; }
    .custom-expiration { margin-top: var(--base-size-4); }
    .token-list { overflow: hidden; }
    .token-row { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: start; gap: var(--base-size-12); padding: var(--base-size-20); border-bottom: var(--lv-border-muted); }
    .token-row:last-child { border-bottom: 0; }
    .token-row-icon { display: grid; width: var(--base-size-32); height: var(--base-size-32); place-items: center; border-radius: var(--lv-radius-full); color: var(--lv-fg-muted); background: var(--lv-bg-control); }
    .token-row-content { display: grid; min-width: 0; gap: var(--base-size-6); }
    .token-name { color: var(--lv-fg-accent); font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .token-description { color: var(--lv-fg-default); font: var(--lv-type-body-compact); }
    .token-meta { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    lv-one-time-secret { margin-top: var(--base-size-8); }
    dialog { width: min(34rem, calc(100vw - var(--base-size-32))); max-width: none; border: 0; padding: 0; color: var(--lv-fg-default); background: transparent; }
    dialog::backdrop { background: var(--lv-modal-backdrop); }
    .token-confirm { display: grid; overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); box-shadow: var(--lv-shadow-floating-lg); }
    .token-confirm-header { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-16); padding: var(--base-size-16) var(--base-size-20); border-bottom: var(--lv-border-muted); }
    .token-confirm-header h2 { font: var(--lv-type-section-title); }
    .token-confirm-close { display: grid; width: var(--control-medium-size); min-height: var(--control-medium-size); place-items: center; padding: 0; color: var(--lv-fg-muted); background: transparent; }
    .token-confirm-body { display: grid; gap: var(--base-size-8); padding: var(--base-size-20); font: var(--lv-type-body); }
    .token-confirm-actions { display: flex; justify-content: flex-end; gap: var(--base-size-8); padding: var(--base-size-16) var(--base-size-20); border-top: var(--lv-border-muted); }
    .token-delete-warning { padding: var(--base-size-20); border-top: 1px solid var(--lv-fg-warning); border-bottom: 1px solid var(--lv-fg-warning); background: var(--lv-bg-warning-muted, var(--lv-bg-panel-muted)); font: var(--lv-type-body); }
    .token-delete-warning p { line-height: 1.5; }
    .token-delete-actions { display: grid; padding: var(--base-size-16) var(--base-size-20); }
    .token-delete-actions .danger { min-height: var(--control-xlarge-size, var(--base-size-40)); font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    .scope-description { min-height: var(--base-size-16); }
    .permissions { display: grid; gap: var(--base-size-16); padding-top: var(--base-size-24); border-top: var(--lv-border-muted); }
    .permissions-heading { display: grid; gap: var(--base-size-6); }
    .permissions-heading h3 { font: var(--lv-type-section-title); }
    .permissions-card { display: grid; overflow: visible; border: var(--lv-border-muted); border-radius: var(--lv-radius-large); background: var(--lv-bg-panel); }
    .permissions-header { display: flex; min-width: 0; align-items: center; justify-content: space-between; gap: var(--base-size-12); padding: var(--base-size-12) var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .permissions-title { display: flex; min-width: 0; align-items: center; gap: var(--base-size-8); }
    .count { display: inline-grid; min-width: var(--base-size-24); height: var(--base-size-24); box-sizing: border-box; place-items: center; border-radius: var(--lv-radius-full); padding: 0 var(--base-size-8); color: var(--lv-fg-muted); background: var(--lv-bg-control); font: var(--lv-type-caption); }
    .permission-picker { position: relative; flex: none; }
    .permission-trigger { display: inline-flex; align-items: center; gap: var(--base-size-8); }
    .permission-trigger:hover, .permission-trigger:focus-visible, .permission-trigger[aria-expanded="true"] { border-color: var(--lv-border-accent); outline: 0; }
    .permission-trigger svg, .permission-remove svg, .permission-search svg, .permission-access-trigger svg { width: var(--base-size-16); height: var(--base-size-16); }
    .permission-backdrop { display: none; }
    .permission-menu { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-6)); right: 0; display: grid; width: min(28rem, calc(100vw - var(--base-size-32))); max-height: min(32rem, var(--permission-menu-max-height, calc(100svh - var(--base-size-64)))); box-sizing: border-box; grid-template-rows: auto minmax(0, 1fr); overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); }
    .permission-menu-header { display: grid; gap: var(--base-size-12); padding: var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .permission-menu-title { display: flex; align-items: center; justify-content: space-between; gap: var(--base-size-8); }
    .permission-menu-heading { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: var(--base-size-8); }
    .permission-menu-count { color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .permission-menu-close { display: none; width: var(--control-small-size); min-height: var(--control-small-size); place-items: center; padding: 0; color: var(--lv-fg-muted); background: transparent; }
    .permission-search { position: relative; display: grid; align-items: center; }
    .permission-search svg { position: absolute; left: var(--base-size-12); z-index: 1; color: var(--lv-fg-muted); pointer-events: none; }
    .permission-search input { width: 100%; min-height: var(--control-xlarge-size, var(--base-size-40)); padding-left: var(--base-size-40); font: var(--lv-type-body); }
    .permission-search input:focus-visible { border-color: var(--lv-border-accent); outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .permission-list { display: grid; min-height: 0; align-content: start; overflow-y: auto; overscroll-behavior: contain; scrollbar-color: var(--lv-scrollbar-thumb) transparent; scrollbar-width: thin; }
    .permission-group + .permission-group { border-top: var(--lv-border-muted); }
    .permission-category { display: flex; min-height: var(--base-size-32); box-sizing: border-box; align-items: center; justify-content: space-between; gap: var(--base-size-8); padding: var(--base-size-8) var(--base-size-12) var(--base-size-4); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); }
    .permission-category-count { font-weight: var(--base-text-weight-normal); font-variant-numeric: tabular-nums; }
    .permission-option { display: grid; min-width: 0; min-height: var(--base-size-48); box-sizing: border-box; grid-template-columns: var(--base-size-20) minmax(0, 1fr); align-items: start; gap: var(--base-size-8); padding: var(--base-size-8) var(--base-size-12); cursor: pointer; }
    .permission-option:hover { background: var(--lv-bg-control-hover); }
    .permission-option[data-selected="true"] { background: var(--lv-bg-accent-muted, var(--lv-bg-control-hover)); }
    .permission-option:focus-within { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .permission-option input[type="checkbox"] { width: var(--base-size-16); height: var(--base-size-16); min-height: 0; margin: var(--base-size-2) 0 0; padding: 0; accent-color: var(--lv-bg-accent); }
    .selected-permissions { display: grid; }
    .selected-permission { position: relative; display: grid; min-height: var(--base-size-64); grid-template-columns: minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-12); padding: var(--base-size-12) var(--base-size-16); border-bottom: var(--lv-border-muted); }
    .selected-permission:last-child { border-bottom: 0; }
    .permission-row-actions { display: flex; align-items: center; justify-content: flex-end; gap: var(--base-size-8); }
    .permission-access-picker { position: relative; }
    .permission-access-trigger, .permission-access-fixed { min-width: 10.5rem; box-sizing: border-box; padding-inline: var(--base-size-12); }
    .permission-access-trigger { display: inline-flex; align-items: center; justify-content: space-between; gap: var(--base-size-6); }
    .permission-access-trigger:hover:not(:disabled), .permission-access-trigger:focus-visible, .permission-access-trigger[aria-expanded="true"] { border-color: var(--lv-border-accent); outline: 0; }
    .permission-access-fixed { display: inline-flex; min-height: var(--control-small-size); align-items: center; border-radius: var(--lv-radius-small); color: var(--lv-fg-muted); background: var(--lv-bg-control); font: var(--lv-type-body-compact); }
    .permission-access-prefix { color: var(--lv-fg-muted); font-weight: var(--base-text-weight-normal); }
    .permission-access-value { color: var(--lv-fg-default); font-weight: var(--base-text-weight-semibold); }
    .permission-access-fixed .permission-access-value { color: var(--lv-fg-muted); }
    .permission-access-menu { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-6)); right: 0; display: grid; width: 12rem; overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-4); }
    button.permission-access-option { display: flex; width: 100%; min-height: var(--control-medium-size); align-items: center; justify-content: space-between; gap: var(--base-size-8); border-color: transparent; background: transparent; padding-inline: var(--base-size-8); text-align: left; }
    button.permission-access-option:hover, button.permission-access-option:focus-visible, button.permission-access-option[aria-selected="true"] { background: var(--lv-bg-control-hover); outline: 0; }
    .permission-access-check { display: inline-grid; width: var(--base-size-16); height: var(--base-size-16); place-items: center; color: var(--lv-fg-accent); }
    .permission-remove { display: grid; width: var(--control-small-size); min-height: var(--control-small-size); place-items: center; padding: 0; color: var(--lv-fg-muted); background: transparent; }
    .permission-remove:hover, .permission-remove:focus-visible { color: var(--lv-fg-danger); border-color: var(--lv-fg-danger); outline: 0; }
    .permission-empty { display: grid; min-height: var(--base-size-48); place-items: center start; padding: var(--base-size-8) var(--base-size-12); color: var(--lv-fg-muted); font: var(--lv-type-caption); }
    .selected-permissions-empty { display: grid; min-height: 10rem; place-content: center; justify-items: center; gap: var(--base-size-8); padding: var(--base-size-24); text-align: center; }
    .selected-permissions-empty svg { color: var(--lv-fg-muted); }
    .token-actions { padding-top: var(--base-size-20); border-top: var(--lv-border-muted); }
    .theme-picker { position: relative; display: inline-block; max-width: 100%; justify-self: end; }
    .theme-trigger { display: inline-grid; width: auto; max-width: 100%; min-height: var(--control-medium-size); grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: var(--base-size-8); padding-inline: var(--base-size-8); text-align: left; }
    .theme-trigger:hover, .theme-trigger:focus-visible, .theme-trigger[aria-expanded="true"] { border-color: var(--lv-border-accent); outline: 0; }
    .theme-trigger-label { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .theme-trigger > svg { width: var(--base-size-16); height: var(--base-size-16); color: var(--lv-fg-muted); }
    .theme-preview { display: inline-grid; width: var(--base-size-40); height: var(--base-size-24); box-sizing: border-box; grid-template-columns: auto auto; place-content: center; align-items: center; gap: var(--base-size-2); overflow: hidden; border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: 0 var(--base-size-4); font: var(--lv-type-body-compact); font-weight: var(--base-text-weight-semibold); line-height: 1; }
    .theme-preview[data-tone="light"] { color: var(--fgColor-black); background: var(--bgColor-white); }
    .theme-preview[data-tone="dark"] { color: var(--fgColor-white); background: var(--bgColor-black); }
    .theme-preview[data-tone="system"] { color: var(--fgColor-white); background: linear-gradient(135deg, var(--bgColor-white) 0 48%, var(--bgColor-black) 52% 100%); }
    .theme-preview-dot { width: var(--base-size-6); height: var(--base-size-6); border-radius: var(--lv-radius-full); background: var(--lv-bg-accent); }
    .theme-menu { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-6)); right: 0; display: grid; width: min(22rem, calc(100vw - var(--base-size-32))); max-height: min(32rem, calc(100svh - var(--base-size-64))); overflow-y: auto; overscroll-behavior: contain; border: var(--lv-border-default); border-radius: var(--lv-radius-large); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-6); scrollbar-color: var(--lv-scrollbar-thumb) transparent; scrollbar-width: thin; }
    .theme-group { display: grid; gap: var(--base-size-2); padding: var(--base-size-4) 0; border-bottom: var(--lv-border-muted); }
    .theme-group:last-child { border-bottom: 0; }
    .theme-group-label { padding: var(--base-size-4) var(--base-size-8); color: var(--lv-fg-muted); font: var(--lv-type-caption); font-weight: var(--base-text-weight-semibold); }
    button.theme-option { display: grid; width: 100%; min-height: var(--control-medium-size); grid-template-columns: auto minmax(0, 1fr) var(--base-size-16); align-items: center; gap: var(--base-size-8); border-color: transparent; background: transparent; padding-inline: var(--base-size-8); text-align: left; }
    button.theme-option:hover, button.theme-option:focus-visible, button.theme-option[aria-selected="true"] { background: var(--lv-bg-control-hover); outline: 0; }
    .theme-option-label { overflow-wrap: anywhere; }
    .theme-check { display: inline-grid; width: var(--base-size-16); height: var(--base-size-16); place-items: center; color: var(--lv-fg-accent); }
    .avatar-control { position: relative; display: inline-grid; justify-items: end; }
    .avatar-trigger { display: grid; width: var(--base-size-32); height: var(--base-size-32); min-height: var(--base-size-32); place-items: center; border: 0; border-radius: var(--lv-radius-full); padding: 0; background: transparent; }
    .avatar-trigger:hover { box-shadow: var(--lv-shadow-resting-sm); }
    .avatar-trigger:focus-visible { outline: var(--focus-outline); outline-offset: var(--focus-outline-offset); }
    .avatar-trigger lv-user-avatar { --lv-user-avatar-size: var(--base-size-32); pointer-events: none; }
    .avatar-input { display: none; }
    .avatar-menu { position: absolute; z-index: var(--z-index-dropdown); top: calc(100% + var(--base-size-6)); right: 0; display: grid; width: var(--overlay-width-xsmall); border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-overlay); box-shadow: var(--lv-shadow-floating-lg); padding: var(--base-size-4); }
    .avatar-menu-item { display: grid; min-height: var(--control-medium-size); grid-template-columns: var(--base-size-16) minmax(0, 1fr); align-items: center; gap: var(--base-size-8); border: var(--lv-border-transparent); border-radius: var(--lv-radius-small); background: transparent; padding: 0 var(--base-size-8); text-align: left; }
    .avatar-menu-item:hover, .avatar-menu-item:focus-visible { background: var(--lv-bg-control-hover); outline: 0; }
    .avatar-menu-item svg { width: var(--base-size-16); height: var(--base-size-16); color: var(--lv-fg-muted); }
    .avatar-menu-item.danger, .avatar-menu-item.danger svg { color: var(--lv-fg-danger); }
    .notice { padding: var(--base-size-8) var(--base-size-12); border-radius: var(--lv-radius-small); background: var(--lv-bg-success-muted); color: var(--lv-fg-success); }
    .error { color: var(--lv-fg-danger); }
    @media (max-width: 40rem) {
      .row { grid-template-columns: 1fr; gap: var(--base-size-12); padding: var(--base-size-16); }
      .profile-email { max-width: none; justify-self: stretch; text-align: left; }
      .profile-name-form { width: 100%; justify-self: stretch; }
      .profile-name-control { justify-content: stretch; }
      .profile-name-control input { width: auto; flex: 1 1 auto; }
      .theme-picker { width: 100%; min-width: 0; justify-self: stretch; }
      .theme-trigger { width: 100%; }
      .theme-menu { right: auto; left: 0; }
      .avatar-control { justify-self: start; }
      .token-page-header { align-items: stretch; flex-direction: column; }
      .token-page-header > button { align-self: start; }
      .token-row { grid-template-columns: auto minmax(0, 1fr); }
      .token-row > .danger { grid-column: 2; justify-self: start; }
      .security-section-heading { align-items: stretch; flex-direction: column; }
      .security-session-actions { align-items: stretch; flex-direction: column; }
      .security-session-actions > button { align-self: start; }
      .security-password-row { grid-template-columns: minmax(0, 1fr); }
      .security-password-row > button { justify-self: start; }
      .security-session { grid-template-columns: var(--base-size-32) minmax(0, 1fr); }
      .security-session > .session-action { grid-column: 2; justify-self: start; }
      .session-drawer-fact { grid-template-columns: minmax(0, 1fr); gap: var(--base-size-4); }
      .permissions-header { align-items: start; }
      .permission-backdrop { position: fixed; z-index: var(--z-index-dropdown); inset: 0; display: block; background: var(--lv-modal-backdrop); }
      .permission-menu { position: fixed; z-index: var(--z-index-modal); top: auto; right: var(--base-size-16); bottom: var(--base-size-16); left: var(--base-size-16); width: auto; max-height: calc(100svh - var(--base-size-32)); }
      .permission-menu-close { display: grid; }
      .selected-permission { grid-template-columns: minmax(0, 1fr); }
      .permission-row-actions { width: 100%; justify-content: space-between; }
      .permission-access-menu { right: auto; left: 0; }
    }
`
