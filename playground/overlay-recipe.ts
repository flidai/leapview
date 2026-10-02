import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { settingsLayoutStyles } from '../web/components/shared/settings-layout'
import { settingsFieldStyles } from '../web/components/shared/settings-field-styles'
import type { SelectMenuOption } from '../web/components/shared/select-menu'
import type { DatePickerInputDetail } from '../web/components/dashboard/filters/date-picker'
import '../web/components/shared/drawer'
import '../web/components/shared/select-menu'
import '../web/components/dashboard/filters/date-picker'
import { exampleChromeStyles, exampleDetails } from './example-chrome'
import { isFixtureDate } from './filter-fixtures'

const frequencies: SelectMenuOption[] = [
  { value: 'daily', label: 'Every day' },
  { value: 'weekly', label: 'Every week' },
  { value: 'monthly', label: 'Every month' },
  { value: 'realtime', label: 'Real time · unavailable in this workspace', disabled: true },
]
type Schedule = { name: string; frequency: string; date: string }
const initialSchedule: Schedule = { name: 'Regional performance', frequency: 'weekly', date: '2026-10-15' }

export class PlaygroundOverlayRecipe extends LitElement {
  @property() example = 'overlay'
  @state() private open = false
  @state() private values = { ...initialSchedule }
  @state() private saved = { ...initialSchedule }
  @state() private submitted = false
  @state() private wide = false
  @state() private longContent = false
  @state() private disabled = false
  @state() private logs: string[] = []
  private trigger?: HTMLElement

  static styles = [settingsLayoutStyles, settingsFieldStyles, css`
    :host { display:block; min-width:0; color:var(--lv-fg-default); font:var(--lv-type-body); }
    * { box-sizing:border-box; }
    .controls { display:flex; flex-wrap:wrap; gap:var(--base-size-12); }
    .controls label { display:flex; align-items:center; gap:var(--base-size-4); }
    .preview { padding:var(--base-size-24); border:var(--lv-border-muted); border-radius:var(--lv-radius-large); background:var(--lv-bg-panel); min-height:16rem; }
    .preview h2 { margin:0 0 var(--base-size-16); font:var(--lv-type-section-title); }
    .summary { display:grid; gap:var(--base-size-12); margin-block:var(--base-size-16); }
    .summary div { display:grid; gap:var(--base-size-4); }
    .summary dt { color:var(--lv-fg-muted); font:var(--lv-type-caption); }
    .summary dd { margin:0; overflow-wrap:anywhere; }
    form { display:grid; gap:var(--base-size-24); padding:var(--base-size-20); }
    .field { display:grid; gap:var(--base-size-8); min-width:0; }
    .actions { display:flex; flex-wrap:wrap; gap:var(--base-size-8); }
    .error { margin:0; color:var(--lv-fg-danger); font:var(--lv-type-caption); }
    .context { display:grid; gap:var(--base-size-16); padding:var(--base-size-16); border:var(--lv-border-muted); border-radius:var(--lv-radius-default); }
    .context p, .documentation p { margin:0; }
    .context p { color:var(--lv-fg-muted); }
    .documentation { display:grid; gap:var(--base-size-12); }
    pre { margin:0; white-space:pre-wrap; overflow-wrap:anywhere; max-height:20rem; overflow:auto; font:var(--lv-type-mono); }
    :host([preview-only]) .controls { display:none; }
  `, exampleChromeStyles]

  getExampleState(): Record<string, unknown> {
    return { values: { ...this.values }, saved: { ...this.saved }, submitted: this.submitted, wide: this.wide, longContent: this.longContent, disabled: this.disabled }
  }

  restoreExampleState(value: Record<string, unknown>): void {
    if (!value || typeof value !== 'object' || Array.isArray(value)) return
    const restoreSchedule = (value: unknown): Schedule => {
      if (!value || typeof value !== 'object' || Array.isArray(value)) return { ...initialSchedule }
      const schedule = value as Record<string, unknown>
      return {
        name: typeof schedule.name === 'string' ? schedule.name.slice(0, 120) : initialSchedule.name,
        frequency: frequencies.some(option => option.value === schedule.frequency && !option.disabled) ? schedule.frequency as string : initialSchedule.frequency,
        date: schedule.date === '' || isFixtureDate(schedule.date) ? schedule.date : initialSchedule.date,
      }
    }
    this.values = restoreSchedule(value.values)
    this.saved = restoreSchedule(value.saved)
    for (const key of ['submitted', 'wide', 'longContent', 'disabled'] as const) this[key] = value[key] === true
    this.open = false
    this.logs = []
  }

  getExampleCode(): string {
    return `import { html } from 'lit'\nimport '../web/components/shared/drawer'\nimport '../web/components/shared/select-menu'\nimport '../web/components/dashboard/filters/date-picker'\n\nconst values = ${JSON.stringify(this.values, null, 2)}\nconst frequencies = ${JSON.stringify(frequencies, null, 2)}\n\n// In the owning Lit component, update values from event.detail and request a render.\n// Open: set drawer.open, await its updateComplete, then call drawer.focusFirst().\n// Close: update open and return focus to the original trigger.\nhtml\`<lv-drawer .open=\${true} .modal=\${true} size="${this.wide ? 'wide' : 'default'}"\n  label="Schedule delivery" @lv-drawer-close=\${closeDrawer}>\n  <span slot="title">Schedule delivery</span>\n  <form @submit=\${saveSchedule}>\n    <label>Report name<input .value=\${values.name} required maxlength="120"\n      @input=\${updateName}></label>\n    <lv-select-menu label="Delivery frequency" .options=\${frequencies}\n      .value=\${values.frequency} .disabled=\${${this.disabled}}\n      @lv-select-change=\${updateFrequency}></lv-select-menu>\n    <lv-date-picker label="First delivery" .value=\${values.date}\n      .disabled=\${${this.disabled}} .invalid=\${${Boolean(this.dateError)}}\n      .error=\${${JSON.stringify(this.dateError)}}\n      @lv-date-input=\${updateDate}></lv-date-picker>\n    <button type="submit">Save schedule</button>\n  </form>\n</lv-drawer>\``
  }

  private get nameError(): string { return this.submitted && !this.values.name.trim() ? 'Enter a report name.' : '' }
  private get dateError(): string {
    if (!this.submitted) return ''
    if (!this.values.date) return 'Choose the first delivery date.'
    return this.values.date < '2026-10-01' ? 'Choose October 1, 2026 or later.' : ''
  }

  private record(name: string, detail: unknown = {}): void {
    this.logs = [`${name}\n${JSON.stringify(detail, null, 2)}`, ...this.logs].slice(0, 12)
  }

  private openDrawer = async (event: Event): Promise<void> => {
    this.trigger = event.currentTarget as HTMLElement
    this.open = true
    await this.updateComplete
    const drawer = this.renderRoot.querySelector<LitElement & { focusFirst(): void }>('lv-drawer')
    await drawer?.updateComplete
    if (this.open) drawer?.focusFirst()
  }

  private closeDrawer = async (): Promise<void> => {
    this.open = false
    this.record('lv-drawer-close')
    await this.updateComplete
    if (this.isConnected) this.trigger?.focus()
  }

  private save = async (event: SubmitEvent): Promise<void> => {
    event.preventDefault()
    if (this.disabled) return
    this.submitted = true
    if (this.nameError || this.dateError) {
      this.record('validation', { name: this.nameError, date: this.dateError })
      await this.updateComplete
      if (this.nameError) this.renderRoot.querySelector<HTMLInputElement>('#schedule-name')?.focus()
      else this.renderRoot.querySelector<HTMLElement>('#schedule-error')?.focus()
      return
    }
    this.saved = { ...this.values, name: this.values.name.trim() }
    this.values = { ...this.saved }
    this.record('submit', this.saved)
    await this.closeDrawer()
  }

  render() {
    return html`<div class="controls" aria-label="Overlay recipe controls">
      <label><input type="checkbox" .checked=${this.wide} @change=${(event: Event) => { this.wide = (event.target as HTMLInputElement).checked }}>Wide drawer</label>
      <label><input type="checkbox" .checked=${this.longContent} @change=${(event: Event) => { this.longContent = (event.target as HTMLInputElement).checked }}>Long content</label>
      <label><input type="checkbox" .checked=${this.disabled} @change=${(event: Event) => { this.disabled = (event.target as HTMLInputElement).checked }}>Read-only fields</label>
      <button class="settings-button" @click=${() => { this.values = { ...this.values, name: '', date: '' }; this.submitted = true }}>Missing required fields</button>
      <button class="settings-button" @click=${() => { this.values = { ...initialSchedule }; this.saved = { ...initialSchedule }; this.submitted = false; this.logs = [] }}>Reset</button>
    </div>
    <section class="preview" part="preview" aria-label="Drawer and controls composition">
      <h2>Scheduled report</h2>
      <dl class="summary"><div><dt>Report</dt><dd>${this.saved.name}</dd></div><div><dt>Delivery</dt><dd>${frequencies.find(item => item.value === this.saved.frequency)?.label} · ${this.saved.date || 'No date'}</dd></div></dl>
      <button class="settings-button primary" @click=${this.openDrawer}>Edit schedule</button>
      <lv-drawer .open=${this.open} .modal=${true} .size=${this.wide ? 'wide' : 'default'} label="Schedule delivery" @lv-drawer-close=${this.closeDrawer}>
        <div slot="title" class="settings-section-heading"><h2>Schedule delivery</h2><p class="settings-description">Regional performance report</p></div>
        <form novalidate @submit=${this.save}>
          ${this.nameError || this.dateError ? html`<p id="schedule-error" class="error" role="alert" tabindex="-1">Complete the required fields before saving.</p>` : nothing}
          <div class="field"><label class="settings-label" for="schedule-name">Report name</label>
            <input id="schedule-name" class="settings-input" maxlength="120" required .value=${this.values.name} ?disabled=${this.disabled} aria-invalid=${this.nameError ? 'true' : 'false'} aria-describedby=${this.nameError ? 'name-error' : nothing} @input=${(event: Event) => { this.values = { ...this.values, name: (event.target as HTMLInputElement).value } }}>
            ${this.nameError ? html`<p class="error" id="name-error" role="alert">${this.nameError}</p>` : nothing}
          </div>
          <div class="field"><span class="settings-label">Delivery frequency</span>
            <lv-select-menu label="Delivery frequency" .options=${frequencies} .value=${this.values.frequency} .disabled=${this.disabled} @lv-select-change=${(event: CustomEvent<{ value: string }>) => { this.values = { ...this.values, frequency: event.detail.value }; this.record(event.type, event.detail) }}></lv-select-menu>
          </div>
          ${this.longContent ? html`<section class="context" aria-label="Delivery details"><strong>Delivery details</strong>${['Recipients receive the latest saved report with its selected regional filters.', 'Daily, weekly and monthly schedules use the same first delivery date.', 'A report name appears in both the delivery subject and the saved schedule.', 'These local values make it possible to review a long drawer on a narrow screen.', 'The date picker below remains available after scrolling this content.'].map(text => html`<p>${text}</p>`)}</section>` : nothing}
          <div class="field"><span class="settings-label">First delivery</span>
            <lv-date-picker label="First delivery" .value=${this.values.date} .disabled=${this.disabled} .invalid=${Boolean(this.dateError)} .error=${this.dateError} @lv-date-input=${(event: CustomEvent<DatePickerInputDetail>) => { this.values = { ...this.values, date: event.detail.value }; this.record(event.type, event.detail) }}></lv-date-picker>
            <span class="settings-description">October 1, 2026 or later.</span>
          </div>
          <div class="actions"><button type="submit" class="settings-button primary" ?disabled=${this.disabled}>Save schedule</button><button type="button" class="settings-button" @click=${this.closeDrawer}>Close drawer</button></div>
        </form>
      </lv-drawer>
    </section>
    ${exampleDetails(html`<section class="documentation">
      <p>Production composition: <code>web/components/shared/drawer.ts</code>, <code>web/components/shared/select-menu.ts</code>, <code>web/components/dashboard/filters/date-picker.ts</code>.</p>
      <p>The drawer owns keyboard and outside-click handling. Its slotted form uses production settings styles. Dropdown and calendar popovers retain their own keyboard navigation; the local owner handles required fields, saves values and restores trigger focus.</p>
      <p>Inputs: drawer open/modal/size; select options/value/disabled; date value/invalid/error/disabled. Events: <code>lv-drawer-close</code>, <code>lv-select-change</code>, <code>lv-date-input</code>, native input and submit. Values and validation state can be shared; transient open popovers and event history are omitted.</p>
      <pre aria-label="Public event log">${this.logs.join('\n\n')}</pre>
    </section>`)}`
  }
}

customElements.define('playground-overlay-recipe', PlaygroundOverlayRecipe)
