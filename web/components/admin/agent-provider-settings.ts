import { LitElement, css, html } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { AdminAgentSignal, AdminAgentProviderInput } from '../../generated/signals'
import { browserCommandFailure } from '../shared/command-failure'
import { settingsFieldStyles } from '../shared/settings-field-styles'

export class AgentProviderSettings extends LitElement {
  @property({ attribute: false }) agent: AdminAgentSignal | null = null
  @state() private draft: AdminAgentProviderInput = { enabled: true, model: '', baseUrl: '', apiMode: 'chat-completions', reasoningEffort: '', apiKey: '', removeKey: false }
  @state() private token = ''
  @state() private restoreRevision = 0
  @state() private busy = false
  @state() private message = ''
  private initialized = false
  private draftRevision = 0
  private sentToken = ''
  private sentRevision = -1

  static styles = [settingsFieldStyles, css`
    :host { display: block; }
    fieldset { border: 0; padding: 0; margin: 0; display: grid; gap: var(--base-size-16); }
    label { display: grid; gap: var(--base-size-8); font: var(--lv-type-body); }
    label.checkbox { display: flex; align-items: center; justify-content: space-between; }
    input[type="checkbox"] { width: var(--base-size-16); height: var(--base-size-16); margin: 0; }
    details { display: grid; gap: var(--base-size-12); }
    details label { margin-top: var(--base-size-12); }
    summary { cursor: pointer; color: var(--lv-fg-muted); }
    h3 { font: var(--lv-type-body); font-weight: var(--base-text-weight-semibold); }
    input, select { padding: var(--base-size-8); color: var(--lv-fg-default); background: var(--lv-bg-panel); border: var(--lv-border-default); border-radius: var(--lv-radius-small); }
    .actions { display: flex; flex-wrap: wrap; gap: var(--base-size-12); }
    button { padding: var(--base-size-8) var(--base-size-12); cursor: pointer; color: var(--lv-fg-default); background: var(--lv-bg-panel); border: var(--lv-border-default); border-radius: var(--lv-radius-small); }
    button:disabled { opacity: .5; cursor: default; }
    button.primary { color: var(--lv-fg-on-accent); background: var(--lv-bg-accent); border-color: var(--lv-bg-accent); }
    p { color: var(--lv-fg-muted); }
  `]

  connectedCallback() { super.connectedCallback(); document.addEventListener('datastar-fetch', this.onFetch) }
  disconnectedCallback() { document.removeEventListener('datastar-fetch', this.onFetch); this.draft = { ...this.draft, apiKey: '' }; super.disconnectedCallback() }
  protected willUpdate() {
    const a = this.agent
    if (!a) return
    if (!this.initialized) {
      this.draft = { enabled: a.enabled, model: a.model || '', baseUrl: a.baseUrl || '', apiMode: a.apiMode || 'chat-completions', reasoningEffort: a.reasoningEffort || '', apiKey: '', removeKey: false }
      this.normalizeReasoning()
      this.draftRevision = a.configurationRevision ?? 0
      this.initialized = true
    }
    if (this.busy && (a.testToken && a.testToken !== this.sentToken || (a.configurationRevision ?? 0) !== this.sentRevision)) {
      this.busy = false
      this.token = a.testToken || ''
      this.message = a.testMessage || ''
      if (!this.token) { this.draft = { enabled: a.enabled, model: a.model || '', baseUrl: a.baseUrl || '', apiMode: a.apiMode || 'chat-completions', reasoningEffort: a.reasoningEffort || '', apiKey: '', removeKey: false }; this.restoreRevision = 0; this.draftRevision = a.configurationRevision ?? 0 }
    }
  }
  private onFetch = (event: Event) => {
    if (!this.busy) return
    const failure = browserCommandFailure(event, 'Agent configuration')
    if (failure) { this.busy = false; this.message = failure.message; this.token = '' }
  }
  private change(key: keyof AdminAgentProviderInput, value: string | boolean) {
    this.draft = { ...this.draft, [key]: value }; this.normalizeReasoning(); this.token = ''; this.message = ''; this.restoreRevision = 0
  }
  private get deepSeekV4() { return this.draft.apiMode === 'chat-completions' && this.draft.model.trim().toLowerCase().startsWith('deepseek-v4') }
  private normalizeReasoning() {
    if (this.draft.apiMode === 'chat-completions') this.draft = { ...this.draft, reasoningEffort: this.deepSeekV4 ? 'none' : '' }
  }
  private send(action: 'test' | 'save') {
    if (!this.agent?.canWrite || this.busy) return
    this.busy = true; this.message = action === 'test' ? 'Testing connection…' : 'Saving configuration…'
    this.sentToken = this.agent.testToken || ''; this.sentRevision = this.draftRevision
    this.dispatchEvent(new CustomEvent('lv-agent-config-command', { bubbles: true, composed: true, detail: { action, provider: { ...this.draft }, expectedRevision: this.sentRevision, restoreRevision: this.restoreRevision, testToken: this.token } }))
  }
  render() {
    const a = this.agent
    if (!a) return html``
    return html`
      <h3>Model configuration</h3>
      <p>${a.adminManaged ? 'Managed by LeapView admins. Changes apply to new requests.' : 'Currently deployment managed. Saving here transfers control to LeapView admins.'}</p>
      ${!a.configurationAvailable ? html`<p role="status">An operator must configure credential encryption before admin-managed settings are available.</p>` : ''}
      <fieldset ?disabled=${!a.canWrite || !a.configurationAvailable || this.busy}>
        <label class="checkbox"><span>Agent enabled</span><input type="checkbox" .checked=${this.draft.enabled} @change=${(e: Event) => this.change('enabled', (e.target as HTMLInputElement).checked)}></label>
        <label>Provider preset<select @change=${(e: Event) => {
          const value = (e.target as HTMLSelectElement).value
          if (value === 'openai') this.draft = { ...this.draft, baseUrl: 'https://api.openai.com/v1', apiMode: 'responses', model: '', reasoningEffort: '', apiKey: '' }
          if (value === 'deepseek') this.draft = { ...this.draft, baseUrl: 'https://api.deepseek.com', apiMode: 'chat-completions', model: '', reasoningEffort: '', apiKey: '' }
          this.token = ''; this.message = ''; this.restoreRevision = 0
        }}><option value="custom">Custom / current configuration</option><option value="openai">OpenAI</option><option value="deepseek">DeepSeek (non-thinking)</option></select></label>
        <label>Model identifier<input .value=${this.draft.model} @input=${(e: Event) => this.change('model', (e.target as HTMLInputElement).value)}></label>
        <details><summary>Advanced connection settings</summary>
        <label>Provider endpoint<input type="url" .value=${this.draft.baseUrl} @input=${(e: Event) => this.change('baseUrl', (e.target as HTMLInputElement).value)}></label>
        <label>API mode<select .value=${this.draft.apiMode} @change=${(e: Event) => this.change('apiMode', (e.target as HTMLSelectElement).value)}><option value="responses">Responses</option><option value="chat-completions">Chat Completions</option></select></label>
        </details>
        <label>Reasoning<select .value=${this.draft.reasoningEffort} @change=${(e: Event) => this.change('reasoningEffort', (e.target as HTMLSelectElement).value)}>
          ${!this.deepSeekV4 ? html`<option value="">Provider default</option>` : ''}
          ${this.draft.apiMode === 'responses' || this.deepSeekV4 ? html`<option value="none">Disabled</option>` : ''}
          ${this.draft.apiMode === 'responses' ? ['low', 'medium', 'high', 'xhigh', 'max'].map(effort => html`<option value=${effort}>${effort}</option>`) : ''}
        </select></label>
        <label>${a.credentialConfigured ? 'Replace API key (leave blank to keep)' : 'API key'}<input type="password" autocomplete="new-password" .value=${this.draft.apiKey || ''} @input=${(e: Event) => this.change('apiKey', (e.target as HTMLInputElement).value)}></label>
        ${a.credentialConfigured ? html`<label class="checkbox"><span>Remove saved API key</span><input type="checkbox" .checked=${this.draft.removeKey || false} @change=${(e: Event) => this.change('removeKey', (e.target as HTMLInputElement).checked)}></label>` : ''}
        <div class="actions"><button type="button" @click=${() => this.send('test')}>${this.draft.enabled ? 'Test connection' : 'Validate settings'}</button><button class="primary" type="button" ?disabled=${!this.token} @click=${() => this.send('save')}>${this.restoreRevision ? 'Restore tested revision' : 'Save and activate'}</button>
          ${(a.configurationRevision ?? 0) > 1 ? html`<button type="button" @click=${() => { this.restoreRevision = this.draftRevision - 1; this.token = ''; this.send('test') }}>Test previous configuration</button>` : ''}
        </div>
      </fieldset>
      <p role="status" aria-live="polite">${this.message}</p>
    `
  }
}
if (!customElements.get('lv-agent-provider-settings')) customElements.define('lv-agent-provider-settings', AgentProviderSettings)
