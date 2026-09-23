import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { LockKeyhole, Plus, Search, X } from 'lucide'
import type { PersonalPermissionPairSignal } from '../../generated/signals'
import { lucideIcon } from '../shared/lucide-icons'
import { formatTechnicalPermissionTarget, groupTokenPermissionPolicies, permissionPairKey, uniquePermissionPairs } from './personal-settings-permissions'
import type { TokenPermissionOption, TokenPermissionPolicy } from './personal-settings-permissions'

type TokenPermissionScope = 'specific' | 'current' | 'future'

export class PersonalSettingsTokenPermissionPicker extends LitElement {
  @property({ attribute: false }) policies: TokenPermissionPolicy[] = []
  @property({ attribute: false }) selectedPermissions: PersonalPermissionPairSignal[] = []
  @property({ type: Boolean, attribute: 'permission-options-ready' }) permissionOptionsReady = false
  @property({ type: Boolean }) open = false
  @state() private search = ''
  @state() private resourceSearch = ''
  @state() private activePolicyID = ''
  @state() private resourceMenuPolicyID = ''
  @state() private policyScopes: Record<string, TokenPermissionScope> = {}
  @state() private pendingPermissions: PersonalPermissionPairSignal[] | null = null
  private lastReportedIncomplete?: boolean

  override createRenderRoot(): HTMLElement { return this }
  override connectedCallback(): void {
    super.connectedCallback()
    document.addEventListener('pointerdown', this.handleDocumentPointerDown)
    window.addEventListener('keydown', this.handleWindowKeydown)
  }
  override disconnectedCallback(): void {
    document.removeEventListener('pointerdown', this.handleDocumentPointerDown)
    window.removeEventListener('keydown', this.handleWindowKeydown)
    super.disconnectedCallback()
  }
  override updated(changed: Map<PropertyKey, unknown>): void {
    if (changed.has('policies') || changed.has('selectedPermissions')) this.reconcileLocalState()
    if (changed.has('selectedPermissions')) this.pendingPermissions = null
    if (changed.has('open')) {
      if (this.open) this.querySelector<HTMLInputElement>('.permission-search input')?.focus()
      else this.search = ''
    }
    this.reportIncompletePolicyChange()
  }

  render() {
    const selectedCount = this.policies.filter((policy) => this.policyConfigured(policy)).length
    const selectedKeys = this.selectedPairKeys()
    const selectedPermissions = this.effectiveSelectedPermissions()
    const visiblePolicies = this.policies.filter((policy) => this.policyConfigured(policy, selectedKeys) || policy.id === this.activePolicyID)
    const categories = groupTokenPermissionPolicies(this.filteredPolicies())
    const categoryCounts = new Map(groupTokenPermissionPolicies(this.policies).map(([category, policies]) => [category, policies.filter((policy) => this.policyConfigured(policy, selectedKeys)).length]))
    return html`
      <div class="permissions-header">
        <div class="permissions-title"><span class="settings-label">Token permissions</span><span class="count" aria-label="${selectedCount} selected permissions">${selectedCount}</span></div>
        <div class="permission-picker">
          <button class="permission-trigger" type="button" aria-haspopup="dialog" aria-controls="token-permission-menu" aria-expanded=${String(this.open)} @click=${this.requestToggle} @keydown=${this.handleTriggerKeydown}>
            ${lucideIcon(Plus, { size: 16, strokeWidth: 2 })}<span>Add permissions</span>
          </button>
          ${this.open ? html`
            <div class="permission-backdrop" aria-hidden="true" @click=${() => this.requestClose(true)}></div>
            <div id="token-permission-menu" class="permission-menu" role="dialog" aria-labelledby="token-permission-menu-title">
              <div class="permission-menu-header">
                <div class="permission-menu-title">
                  <div class="permission-menu-heading"><span class="settings-label" id="token-permission-menu-title">Add token permission</span><span class="permission-menu-count" aria-live="polite">${selectedCount} selected</span></div>
                  <button class="permission-menu-close" type="button" aria-label="Close permission picker" @click=${() => this.requestClose(true)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button>
                </div>
                <p class="permission-menu-help">Choose an action. Its resource scope will appear in the form.</p>
                <label class="permission-search">${lucideIcon(Search, { size: 16, strokeWidth: 2 })}<input type="search" aria-label="Search permissions" placeholder="Search permissions" .value=${this.search} @input=${this.onSearchInput}></label>
              </div>
              <div class="permission-list">
                ${categories.length ? categories.map(([category, categoryPolicies], categoryIndex) => html`
                  <div class="permission-group" role="group" aria-labelledby=${`token-permission-category-${categoryIndex}`}>
                    <div class="permission-category" id=${`token-permission-category-${categoryIndex}`}><span>${category}</span> <span class="permission-category-count">${categoryCounts.get(category) ?? 0} selected</span></div>
                    ${categoryPolicies.map((policy) => html`
                      <button class="permission-option" data-policy=${policy.id} data-selected=${String(this.policyConfigured(policy, selectedKeys))} type="button" @click=${() => this.addPolicy(policy)}>
                        <span class="settings-field"><span class="settings-label">${policy.label}</span><span class="settings-description">${policy.description}</span></span>
                        ${this.policyConfigured(policy, selectedKeys) ? html`<span class="permission-option-status">Configured</span>` : nothing}
                      </button>
                    `)}
                  </div>
                `) : html`<div class="permission-empty">${this.permissionOptionsReady && this.policies.length === 0 ? 'No authorized permissions are available.' : 'No permissions match your search.'}</div>`}
              </div>
            </div>
          ` : nothing}
        </div>
      </div>
      <div class="selected-permissions" aria-live="polite">
        ${visiblePolicies.length ? html`
          <div class="permission-policy-list">${visiblePolicies.map((policy) => this.renderPolicy(policy, selectedKeys))}</div>
          ${selectedPermissions.length ? html`<details class="permission-technical-details">
            <summary>Technical details <span>${selectedPermissions.length} exact ${selectedPermissions.length === 1 ? 'permission' : 'permissions'}</span></summary>
            <div class="permission-technical-list">${selectedPermissions.map((pair) => html`<div class="permission-technical-row"><code>${pair.action}</code><span>${formatTechnicalPermissionTarget(pair)}</span></div>`)}</div>
          </details>` : nothing}
        ` : html`<div class="selected-permissions-empty"><span aria-hidden="true">${lucideIcon(LockKeyhole, { size: 28, strokeWidth: 1.8 })}</span><div class="settings-field"><span class="settings-label">No permissions added yet</span><span class="settings-description">This token will have no project or resource authority.</span></div></div>`}
      </div>
    `
  }

  private renderPolicy(policy: TokenPermissionPolicy, selectedKeys: Set<string>) {
    const resourcePolicy = policy.targetScope === 'resource'
    const fixedExplanation = policy.targetScope === 'project'
      ? 'This action applies to the current project and cannot be narrowed to an individual resource.'
      : policy.targetScope === 'instance'
        ? 'This action applies to this LeapView instance and cannot be narrowed to a project or resource.'
        : 'This permission has a predefined scope, so there is nothing else to configure.'
    const scope = this.policyScope(policy, selectedKeys)
    const selectedResources = policy.options.filter((option) => this.optionSelected(option, selectedKeys))
    const configured = this.policyConfigured(policy, selectedKeys)
    const menuOpen = this.resourceMenuPolicyID === policy.id
    const query = this.resourceSearch.trim().toLocaleLowerCase()
    const resources = policy.options.filter((option) => option.label.toLocaleLowerCase().includes(query))
    return html`
      <section class="permission-policy" data-permission=${policy.id} data-configured=${String(configured)}>
        <header class="permission-policy-header"><div class="settings-field"><span class="settings-label">${policy.label}</span><span class="settings-description">${policy.category}</span></div>
          <div class="permission-policy-actions">${configured ? nothing : html`<span class="permission-policy-status">Needs scope</span>`}<button class="permission-policy-remove danger" type="button" @click=${() => this.removePolicy(policy)}>Remove</button></div>
        </header>
        ${resourcePolicy ? html`
          <fieldset class="permission-scope-options"><legend>Resource scope</legend>
            <label class="permission-scope-option"><input type="radio" name=${`token-permission-scope-${policy.id}`} value="specific" .checked=${scope === 'specific'} ?disabled=${policy.options.length === 0} @change=${() => this.chooseScope(policy, 'specific')}><span class="settings-field"><span class="settings-label">Specific ${policy.category.toLocaleLowerCase()}</span><span class="settings-description">Choose only the resources this token needs.</span></span></label>
            <label class="permission-scope-option"><input type="radio" name=${`token-permission-scope-${policy.id}`} value="current" .checked=${scope === 'current'} ?disabled=${policy.options.length === 0} @change=${() => this.chooseScope(policy, 'current')}><span class="settings-field"><span class="settings-label">All current ${policy.category.toLocaleLowerCase()}</span><span class="settings-description">Includes ${policy.options.length} exact ${policy.options.length === 1 ? 'resource' : 'resources'}; later resources stay excluded.</span></span></label>
            ${policy.future ? html`<label class="permission-scope-option permission-scope-elevated"><input type="radio" name=${`token-permission-scope-${policy.id}`} value="future" .checked=${scope === 'future'} @change=${() => this.chooseScope(policy, 'future')}><span class="settings-field"><span class="settings-label">All current and future ${policy.category.toLocaleLowerCase()}</span><span class="settings-description">Automatically includes resources created later. Use only for durable automation.</span></span></label>` : nothing}
          </fieldset>
          ${scope === 'specific' ? html`
            <div class="permission-resource-selection">
              <div class="permission-resource-heading"><span class="settings-label">Selected ${policy.category.toLocaleLowerCase()}</span><span class="permission-menu-count">${selectedResources.length} selected</span></div>
              ${selectedResources.length ? html`<div class="permission-resource-chips">${selectedResources.map((option) => html`<span class="permission-resource-chip">${option.label}<button type="button" aria-label=${`Remove ${option.label}`} @click=${() => this.toggleResource(policy, option)}>${lucideIcon(X, { size: 14, strokeWidth: 2 })}</button></span>`)}</div>` : html`<p class="permission-resource-empty">No resources selected yet.</p>`}
              <div class="permission-resource-control">
                <button class="permission-resource-trigger" type="button" aria-haspopup="dialog" aria-expanded=${String(menuOpen)} @click=${() => this.toggleResourceMenu(policy)}>${lucideIcon(Plus, { size: 16, strokeWidth: 2 })}<span>${selectedResources.length ? `Edit ${policy.category.toLocaleLowerCase()}` : `Choose ${policy.category.toLocaleLowerCase()}`}</span></button>
                ${menuOpen ? html`
                  <div class="permission-resource-backdrop" aria-hidden="true" @click=${() => this.closeResourceMenu(true)}></div>
                  <div class="permission-resource-menu" role="dialog" aria-label=${`Choose ${policy.category.toLocaleLowerCase()}`}>
                    <div class="permission-resource-menu-header"><div class="permission-menu-title"><span class="settings-label">Choose ${policy.category.toLocaleLowerCase()}</span><button class="permission-resource-menu-close" type="button" aria-label="Close resource picker" @click=${() => this.closeResourceMenu(true)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button></div>
                      <label class="permission-search">${lucideIcon(Search, { size: 16, strokeWidth: 2 })}<input type="search" aria-label=${`Search ${policy.category.toLocaleLowerCase()}`} placeholder=${`Search ${policy.category.toLocaleLowerCase()}`} .value=${this.resourceSearch} @input=${this.onResourceSearch}></label>
                    </div>
                    <div class="permission-resource-list">${resources.length ? resources.map((option) => html`<label class="permission-resource-option"><input type="checkbox" value=${option.id} .checked=${this.optionSelected(option, selectedKeys)} @change=${() => this.toggleResource(policy, option)}><span>${option.label}</span></label>`) : html`<div class="permission-empty">No resources match your search.</div>`}</div>
                    <div class="permission-resource-menu-footer"><span class="permission-menu-count">${selectedResources.length} selected</span><button class="primary" type="button" @click=${() => this.closeResourceMenu(true)}>Done</button></div>
                  </div>
                ` : nothing}
              </div>
            </div>
          ` : nothing}
        ` : html`<div class="permission-fixed-scope"><span class="permission-fixed-scope-icon" aria-hidden="true">${lucideIcon(LockKeyhole, { size: 16, strokeWidth: 2 })}</span><span class="settings-field"><span class="settings-label">${policy.description}</span><span class="settings-description">${fixedExplanation}</span></span><span class="permission-policy-ready">Ready</span></div>`}
      </section>
    `
  }

  private filteredPolicies(): TokenPermissionPolicy[] {
    const query = this.search.trim().toLocaleLowerCase()
    return query ? this.policies.filter((policy) => `${policy.label} ${policy.description} ${policy.category} ${policy.searchText}`.toLocaleLowerCase().includes(query)) : this.policies
  }
  private effectiveSelectedPermissions(): PersonalPermissionPairSignal[] { return this.pendingPermissions ?? this.selectedPermissions }
  private selectedPairKeys(): Set<string> { return new Set(this.effectiveSelectedPermissions().map(permissionPairKey)) }
  private optionSelected(option: TokenPermissionOption, keys = this.selectedPairKeys()): boolean {
    return option.permissions.length > 0 && option.permissions.every((pair) => keys.has(permissionPairKey(pair)))
  }
  private policyConfigured(policy: TokenPermissionPolicy, keys = this.selectedPairKeys()): boolean {
    return [...policy.options, ...(policy.future ? [policy.future] : [])].some((option) => this.optionSelected(option, keys))
  }
  private policyScope(policy: TokenPermissionPolicy, keys: Set<string>): TokenPermissionScope {
    const explicit = this.policyScopes[policy.id]
    if (explicit === 'specific') return 'specific'
    if (explicit === 'current') return policy.options.length > 0 && policy.options.every((option) => this.optionSelected(option, keys)) ? 'current' : 'specific'
    if (explicit === 'future') return policy.future && this.optionSelected(policy.future, keys) ? 'future' : 'specific'
    if (policy.future && this.optionSelected(policy.future, keys)) return 'future'
    if (policy.options.length && policy.options.every((option) => this.optionSelected(option, keys))) return 'current'
    return 'specific'
  }
  private addPolicy(policy: TokenPermissionPolicy): void {
    this.activePolicyID = policy.id
    this.closeResourceMenu()
    this.requestClose(false)
    if (policy.targetScope !== 'resource') this.emitSelectedPermissions([...this.effectiveSelectedPermissions(), ...policy.options.flatMap((option) => option.permissions)])
    void this.focusPolicy(policy.id)
  }
  private removePolicy(policy: TokenPermissionPolicy): void {
    const remove = new Set([...policy.options, ...(policy.future ? [policy.future] : [])].flatMap((option) => option.permissions.map(permissionPairKey)))
    this.policyScopes = Object.fromEntries(Object.entries(this.policyScopes).filter(([id]) => id !== policy.id))
    if (this.activePolicyID === policy.id) this.activePolicyID = ''
    if (this.resourceMenuPolicyID === policy.id) this.closeResourceMenu()
    this.emitSelectedPermissions(this.effectiveSelectedPermissions().filter((pair) => !remove.has(permissionPairKey(pair))))
  }
  private chooseScope(policy: TokenPermissionPolicy, scope: TokenPermissionScope): void {
    const futureKeys = new Set(policy.future?.permissions.map(permissionPairKey) ?? [])
    const optionKeys = new Set(policy.options.flatMap((option) => option.permissions.map(permissionPairKey)))
    let selected = this.effectiveSelectedPermissions().filter((pair) => !futureKeys.has(permissionPairKey(pair)))
    if (scope === 'current') selected = [...selected, ...policy.options.flatMap((option) => option.permissions)]
    else if (scope === 'future' && policy.future) {
      selected = selected.filter((pair) => !optionKeys.has(permissionPairKey(pair)))
      selected.push(...policy.future.permissions)
    }
    this.activePolicyID = policy.id
    this.policyScopes = { ...this.policyScopes, [policy.id]: scope }
    if (scope !== 'specific') this.closeResourceMenu()
    this.emitSelectedPermissions(selected)
  }
  private toggleResource(policy: TokenPermissionPolicy, option: TokenPermissionOption): void {
    const futureKeys = new Set(policy.future?.permissions.map(permissionPairKey) ?? [])
    const optionKeys = new Set(option.permissions.map(permissionPairKey))
    let selected = this.effectiveSelectedPermissions().filter((pair) => !futureKeys.has(permissionPairKey(pair)))
    if (this.optionSelected(option)) selected = selected.filter((pair) => !optionKeys.has(permissionPairKey(pair)))
    else selected.push(...option.permissions)
    this.activePolicyID = policy.id
    this.policyScopes = { ...this.policyScopes, [policy.id]: 'specific' }
    this.emitSelectedPermissions(selected)
  }
  private emitSelectedPermissions(permissions: PersonalPermissionPairSignal[]): void {
    const selected = uniquePermissionPairs(permissions)
    this.pendingPermissions = selected
    this.dispatchEvent(new CustomEvent('lv-personal-token-permissions-change', { detail: { permissions: selected }, bubbles: true, composed: true }))
  }
  private toggleResourceMenu(policy: TokenPermissionPolicy): void {
    if (this.resourceMenuPolicyID === policy.id) { this.closeResourceMenu(); return }
    this.requestClose(false)
    this.activePolicyID = policy.id
    this.resourceMenuPolicyID = policy.id
    this.resourceSearch = ''
    void this.updateComplete.then(() => this.querySelector<HTMLInputElement>('.permission-resource-menu .permission-search input')?.focus())
  }
  private closeResourceMenu(returnFocus = false): void {
    if (!this.resourceMenuPolicyID) return
    const id = this.resourceMenuPolicyID
    this.resourceMenuPolicyID = ''
    this.resourceSearch = ''
    if (returnFocus) void this.updateComplete.then(() => Array.from(this.querySelectorAll<HTMLElement>('.permission-policy')).find((policy) => policy.dataset.permission === id)?.querySelector<HTMLButtonElement>('.permission-resource-trigger')?.focus())
  }
  private requestToggle = (): void => {
    this.closeResourceMenu()
    this.dispatchEvent(new CustomEvent('lv-personal-token-permission-picker-toggle', { bubbles: true, composed: true }))
  }
  private requestClose(returnFocus: boolean): void {
    this.dispatchEvent(new CustomEvent('lv-personal-token-permission-picker-close', { detail: { returnFocus }, bubbles: true, composed: true }))
  }
  private handleTriggerKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return
    event.preventDefault()
    if (!this.open) this.requestToggle()
  }
  private onSearchInput = (event: Event): void => { this.search = (event.currentTarget as HTMLInputElement).value }
  private onResourceSearch = (event: Event): void => { this.resourceSearch = (event.currentTarget as HTMLInputElement).value }
  private async focusPolicy(id: string): Promise<void> {
    await this.updateComplete
    const policy = Array.from(this.querySelectorAll<HTMLElement>('.permission-policy')).find((candidate) => candidate.dataset.permission === id)
    policy?.scrollIntoView({ block: 'nearest' })
    policy?.querySelector<HTMLElement>('input:checked, button')?.focus()
  }
  private reconcileLocalState(): void {
    const valid = new Set(this.policies.map((policy) => policy.id))
    this.policyScopes = Object.fromEntries(Object.entries(this.policyScopes).filter(([id]) => valid.has(id)))
    if (this.activePolicyID && !valid.has(this.activePolicyID)) this.activePolicyID = ''
    if (this.resourceMenuPolicyID && !valid.has(this.resourceMenuPolicyID)) this.closeResourceMenu()
  }
  private reportIncompletePolicyChange(): void {
    const active = this.policies.find((policy) => policy.id === this.activePolicyID)
    const hasIncompletePolicy = Boolean(active && !this.policyConfigured(active))
    if (this.lastReportedIncomplete === hasIncompletePolicy) return
    this.lastReportedIncomplete = hasIncompletePolicy
    this.dispatchEvent(new CustomEvent('lv-personal-token-permission-incomplete-change', { detail: { hasIncompletePolicy }, bubbles: true, composed: true }))
  }
  private handleDocumentPointerDown = (event: PointerEvent): void => {
    if (!this.resourceMenuPolicyID) return
    const control = this.querySelector('.permission-resource-control')
    if (control && !event.composedPath().includes(control)) this.closeResourceMenu()
  }
  private handleWindowKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'Escape' || !this.resourceMenuPolicyID) return
    event.preventDefault()
    this.closeResourceMenu(true)
  }
}

if (!customElements.get('lv-personal-token-permission-picker')) customElements.define('lv-personal-token-permission-picker', PersonalSettingsTokenPermissionPicker)
