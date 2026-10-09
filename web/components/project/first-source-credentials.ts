import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import type { FirstSourceCredentialCommandSignal, FirstSourceCredentialSignal } from '../../generated/signals'
import { DatastarLit } from '../shared/datastar-lit'
import { browserCommandFailure, ownsBrowserCommandFetch } from '../shared/command-failure'

class FirstSourceCredentials extends DatastarLit(LitElement) {
  @property({ attribute: 'connection-id' }) connectionId = ''
  @property({ attribute: 'source-host' }) sourceHost = ''
  @property({ attribute: 'source-database' }) sourceDatabase = ''
  @property({ attribute: 'source-identity' }) sourceIdentity = ''
  @state() private versionId = ''
  @state() private operationId = ''
  @state() private sourceDigest = ''
  @state() private attestationDigest = ''
  @state() private planKey = ''
  @state() private pending = false
  @state() private failure = ''
  static styles = css`
    :host { display:block; max-width:52rem; margin:0 auto; padding:var(--base-size-24); color:var(--lv-fg-default); }
    section, form, label { display:grid; gap:var(--base-size-8); }
    section { margin:var(--base-size-24) 0; }
    input, select, button { font:inherit; padding:var(--base-size-8); border:var(--lv-border-default); border-radius:var(--lv-radius-default); background:var(--lv-bg-panel); color:inherit; }
    input,select { min-width:0; } button { cursor:pointer; } button:disabled { opacity:.6; cursor:default; }
    .actions { display:flex; flex-wrap:wrap; gap:var(--base-size-8); } .hint { color:var(--lv-fg-muted); }
    [role=alert] { color:var(--lv-fg-danger); } h1,h2,p { margin:0; } h2 { font:var(--lv-type-section-title); }
  `
  connectedCallback() { super.connectedCallback(); document.addEventListener('datastar-fetch', this.fetchResult) }
  disconnectedCallback() { document.removeEventListener('datastar-fetch', this.fetchResult); super.disconnectedCallback() }
  private get current() { return this.signal<FirstSourceCredentialSignal | undefined>('firstSourceCredentials', undefined) }
  updated() {
    const value = this.current
    if (value?.command.action === 'status' && !value.error && value.command.versionId && this.versionId !== value.command.versionId) this.versionId = value.command.versionId
    if (!value?.error && (value?.phase === 'aborted' || value?.phase === 'not_found') && this.operationId === value.command.operationId) this.operationId = ''
  }
  private get command(): FirstSourceCredentialCommandSignal {
    const value = this.current
    const terminal = !value?.error && (value?.phase === 'aborted' || value?.phase === 'not_found')
    return { action:'', password:'', versionId:this.versionId || value?.command.versionId || '', receiptId:value?.command.receiptId || '', operationId:this.operationId || (!terminal && value?.command.operationId) || '', sourceDigest:this.sourceDigest, sourceAttestationDigest:this.attestationDigest, planIdempotencyKey:this.planKey }
  }
  private emit(action: string, fields: Partial<FirstSourceCredentialCommandSignal> = {}, read = false) {
    this.pending = true; this.failure = ''
    this.dispatchEvent(new CustomEvent(read ? 'lv-first-source-query' : 'lv-first-source-command', { bubbles:true, composed:true, detail:{...this.command, action, ...fields} }))
  }
  private readonly fetchResult = (event: Event) => {
    if (!this.pending || !ownsBrowserCommandFetch(this,event)) return
    const failure = browserCommandFailure(event,'Credential action')
    if (failure) { this.failure = failure.message; this.pending = false }
    if ((event as CustomEvent).detail?.type === 'finished') this.pending = false
  }
  private save(event: SubmitEvent) {
    event.preventDefault()
    const form = event.currentTarget as HTMLFormElement
    if (!form.reportValidity()) return
    const password = String(new FormData(form).get('password') || '')
    form.reset(); this.versionId=''; this.operationId=''
    this.emit('save',{password,versionId:'',receiptId:'',operationId:''})
  }
  private prepare(event: SubmitEvent) {
    event.preventDefault()
    if (!(event.currentTarget as HTMLFormElement).reportValidity()) return
    this.operationId = crypto.randomUUID()
    this.emit('prepare')
  }
  render() {
    const value = this.current
    const command = this.command
    const inFlight = Boolean(command.operationId) && value?.phase !== 'aborted' && value?.phase !== 'not_found'
    const tested = Boolean(command.receiptId) && value?.command.versionId === command.versionId && Date.parse(value?.receiptExpiresAt || '') > Date.now()
    return html`
      <h1>Connect your first production source</h1>
      <p>${this.connectionId} · ${this.sourceHost} · ${this.sourceDatabase} · ${this.sourceIdentity}</p>
      <p class="hint">The operator has admitted this connection. Save and test its password, then prepare the source you intend to publish. Publication requires an independent reviewer.</p>
      ${this.failure || value?.error ? html`<p role="alert">${this.failure || value?.error}</p>` : nothing}
      ${value?.message ? html`<p role="status">${value.message}</p>` : nothing}
      <section><h2>Connection password</h2><form @submit=${this.save}>
        <label>Password<input name="password" type="password" autocomplete="new-password" required ?disabled=${inFlight || this.pending}></label>
        <button ?disabled=${inFlight || this.pending}>Save draft</button>
      </form><label>Saved draft<select .value=${command.versionId} ?disabled=${inFlight || this.pending} @change=${(e:Event)=>{this.versionId=(e.target as HTMLSelectElement).value}}>
        <option value="">Choose a draft</option>
        ${command.versionId && !value?.drafts?.some(d=>d.versionId===command.versionId) ? html`<option value=${command.versionId}>${command.versionId}</option>` : nothing}
        ${(value?.drafts || []).map(d=>html`<option value=${d.versionId}>${d.versionId} · ${d.createdAt}</option>`)}
      </select></label><div class="actions"><button ?disabled=${this.pending} @click=${()=>this.emit('list',{},true)}>Refresh drafts</button><button ?disabled=${!command.versionId || this.pending} @click=${()=>this.emit('validate')}>Test draft</button></div></section>
      <section><h2>Prepare first publication</h2><p class="hint">Use the source and attestation digests returned by your authenticated source upload, and keep the plan key for the matching delivery command.</p>
      <form @submit=${this.prepare}>
        <label>Source digest<input name="sourceDigest" required pattern="sha256:[0-9a-f]{64}" .value=${this.sourceDigest} ?disabled=${inFlight} @input=${(e:Event)=>this.sourceDigest=(e.target as HTMLInputElement).value}></label>
        <label>Source attestation digest<input name="attestationDigest" required pattern="sha256:[0-9a-f]{64}" .value=${this.attestationDigest} ?disabled=${inFlight} @input=${(e:Event)=>this.attestationDigest=(e.target as HTMLInputElement).value}></label>
        <label>Delivery plan key<input name="planKey" required maxlength="255" .value=${this.planKey} ?disabled=${inFlight} @input=${(e:Event)=>this.planKey=(e.target as HTMLInputElement).value}></label>
        <button ?disabled=${!tested || inFlight || this.pending}>Prepare first publication</button>
      </form>${command.operationId ? html`<label>Preparation ID<input readonly .value=${command.operationId}></label><p>Status: ${value?.phase || 'Awaiting confirmation'}</p><p class="hint">Keep this ID. Supply it as firstSourcePreparationId when creating the matching delivery plan.</p>` : nothing}
      ${inFlight ? html`<div class="actions"><button ?disabled=${this.pending} @click=${()=>this.emit('status',{},true)}>Check preparation</button><button ?disabled=${!tested || this.pending} @click=${()=>this.emit('renew')}>Renew preparation after test</button><button ?disabled=${this.pending} @click=${()=>this.emit('abort')}>Cancel preparation</button></div>` : nothing}
      </section><section><h2>Recover an interrupted preparation</h2><form @submit=${(e:SubmitEvent)=>{e.preventDefault();if((e.currentTarget as HTMLFormElement).reportValidity())this.emit('status',{},true)}}>
        <label>Preparation ID<input name="recoveryId" required pattern="[0-9a-fA-F-]{36}" @input=${(e:Event)=>this.operationId=(e.target as HTMLInputElement).value.trim()}></label><button ?disabled=${this.pending}>Recover preparation</button>
      </form></section>
    `
  }
}
if (!customElements.get('lv-first-source-credentials')) customElements.define('lv-first-source-credentials',FirstSourceCredentials)
