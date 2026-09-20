import { css, html, nothing, type TemplateResult } from 'lit'
import { ifDefined } from 'lit/directives/if-defined.js'

/** Native settings markup: callers retain form ownership, events, and permissions. */
export function renderSettingsSection(options: {
  label: string
  heading?: string
  description?: string
  appearance?: 'panel' | 'card' | 'plain'
  className?: string
  content: TemplateResult
}) {
  return html`<section class=${`settings-section ${options.className ?? ''}`} data-appearance=${options.appearance ?? 'panel'} aria-label=${options.label}>
    ${options.heading ? html`<header class="settings-section-heading"><h2>${options.heading}</h2>${options.description ? html`<p class="settings-description">${options.description}</p>` : nothing}</header>` : nothing}
    ${options.content}
  </section>`
}

export function renderSettingsRow(options: {
  label: string
  description?: string
  control: TemplateResult
  controlId?: string
  labelId?: string
  layout?: 'field' | 'action' | 'stacked'
  className?: string
}) {
  return html`<div class=${`settings-row ${options.className ?? ''}`} data-layout=${options.layout ?? 'field'}>
    <div class="settings-field">
      ${options.controlId
        ? html`<label class="settings-label" for=${options.controlId} id=${ifDefined(options.labelId)}>${options.label}</label>`
        : html`<span class="settings-label" id=${ifDefined(options.labelId)}>${options.label}</span>`}
      ${options.description ? html`<span class="settings-description">${options.description}</span>` : nothing}
    </div>
    ${options.control}
  </div>`
}

export function renderSettingsActions(content: TemplateResult, options: { className?: string; stack?: boolean } = {}) {
  return html`<div class=${`settings-actions ${options.className ?? ''}`} data-stack=${options.stack ? 'narrow' : nothing}>${content}</div>`
}

/** Import beside settingsFieldStyles in each consuming shadow root. */
export const settingsLayoutStyles = css`
  .settings-stack { display: grid; min-width: 0; gap: var(--base-size-24); container: settings / inline-size; }
  .settings-section { display: grid; min-width: 0; gap: var(--base-size-16); box-sizing: border-box; border: var(--lv-border-muted); border-radius: var(--lv-radius-default); background: var(--lv-bg-panel); padding: var(--base-size-20); }
  .settings-section[data-appearance="plain"] { border: 0; border-radius: 0; background: transparent; padding: 0; }
  .settings-section[data-appearance="card"] { gap: 0; padding: 0; border-radius: var(--lv-radius-large); }
  .settings-section-heading { display: grid; min-width: 0; gap: var(--base-size-4); }
  .settings-section-heading h2, .settings-section-heading p { margin: 0; overflow-wrap: anywhere; }
  .settings-section-heading h2 { font: var(--lv-type-section-title); color: var(--lv-fg-default); }
  .settings-rows { display: grid; min-width: 0; }
  .settings-row { display: grid; min-width: 0; box-sizing: border-box; grid-template-columns: minmax(0, .7fr) minmax(0, 1.3fr); align-items: center; gap: var(--base-size-16); padding: var(--base-size-12) 0; border-bottom: var(--lv-border-muted); }
  .settings-row:last-child { border-bottom: 0; }
  .settings-row .settings-field { overflow-wrap: anywhere; }
  .settings-row > * { min-width: 0; max-width: 100%; }
  .settings-row[data-layout="action"] { grid-template-columns: minmax(0, 1fr) fit-content(60%); }
  .settings-row[data-layout="stacked"] { grid-template-columns: minmax(0, 1fr); align-content: start; gap: var(--base-size-4); border: 0; padding: 0; }
  .settings-section[data-appearance="card"] > .settings-row { min-height: var(--base-size-64); padding: var(--base-size-12) var(--base-size-20); }
  .settings-actions { display: flex; min-width: 0; flex-wrap: wrap; align-items: center; gap: var(--base-size-8); }
  .settings-actions > * { min-width: 0; max-width: 100%; }
  .settings-actions > input.settings-input { flex: 1 1 12rem; }
  .settings-input { box-sizing: border-box; min-width: 0; max-width: 100%; min-height: var(--control-medium-size); border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: var(--base-size-4) var(--base-size-8); color: var(--lv-fg-default); background: var(--lv-bg-input); font: var(--lv-type-body-compact); }
  .settings-button { display: inline-flex; box-sizing: border-box; min-height: var(--control-medium-size); align-items: center; justify-content: center; gap: var(--base-size-6); border: var(--lv-border-default); border-radius: var(--lv-radius-small); padding: var(--base-size-4) var(--base-size-12); color: var(--lv-button-fg-rest); background: var(--lv-button-bg-rest); cursor: pointer; text-decoration: none; font: var(--lv-type-body-compact); }
  .settings-button.primary { border-color: var(--lv-bg-accent); color: var(--lv-fg-on-accent); background: var(--lv-bg-accent); }
  .settings-button.danger { color: var(--lv-fg-danger); }
  .settings-button:hover:not(:disabled):not(.disabled) { background: var(--lv-button-bg-hover, var(--lv-button-bg-rest)); }
  .settings-button.primary:hover:not(:disabled) { background: var(--lv-button-accent-bg-hover, var(--lv-bg-accent)); }
  .settings-input:focus-visible, .settings-button:focus-visible, .settings-button:focus-within { outline: var(--borderWidth-thick) solid var(--focus-outlineColor, var(--lv-fg-accent)); outline-offset: var(--borderWidth-thick); }
  .settings-button:disabled, .settings-button.disabled { cursor: not-allowed; opacity: .55; }
  @container settings (max-width: 30rem) {
    .settings-section { padding: var(--base-size-12); }
    .settings-row:not([data-layout="stacked"]) { grid-template-columns: minmax(0, 1fr); align-items: start; gap: var(--base-size-8); }
    .settings-section[data-appearance="card"] > .settings-row { padding: var(--base-size-16); }
    .settings-row > :last-child { justify-self: stretch; }
    .settings-actions[data-stack="narrow"] { flex-direction: column; align-items: stretch; }
    .settings-actions[data-stack="narrow"] > input.settings-input { flex: 0 1 auto; width: 100%; }
  }
`
