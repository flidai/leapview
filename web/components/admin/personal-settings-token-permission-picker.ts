import { LitElement, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { ChevronDown, LockKeyhole, Plus, Search, X } from 'lucide'
import type { PersonalPermissionPairSignal } from '../../generated/signals'
import { lucideIcon } from '../shared/lucide-icons'
import { groupTokenPermissionPolicies, permissionPairKey, uniquePermissionPairs } from './personal-settings-permissions'
import type { TokenPermissionOption, TokenPermissionPolicy } from './personal-settings-permissions'

type TokenPermissionScope = 'specific' | 'current' | 'future'
type ScopeDraft = { scope: TokenPermissionScope | ''; resourceIDs: string[] }

export class PersonalSettingsTokenPermissionPicker extends LitElement {
  @property({ attribute: false }) policies: TokenPermissionPolicy[] = []
  @property({ attribute: false }) selectedPermissions: PersonalPermissionPairSignal[] = []
  @property({ type: Boolean, attribute: 'permission-options-ready' }) permissionOptionsReady = false
  @property({ type: Boolean }) open = false
  @state() private search = ''
  @state() private resourceSearch = ''
  @state() private selectedResourcePolicyIDs: string[] = []
  @state() private scopeMenuPolicyID = ''
  @state() private scopeDraft: ScopeDraft = { scope: '', resourceIDs: [] }
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
    const selectedKeys = this.selectedPairKeys()
    const visiblePolicies = this.policies.filter((policy) => this.policySelected(policy, selectedKeys))
    const selectedCount = visiblePolicies.length
    const categories = groupTokenPermissionPolicies(this.filteredPolicies())
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
                  <span class="settings-label" id="token-permission-menu-title">Add permission</span>
                  <button class="permission-menu-close" type="button" aria-label="Close permission picker" @click=${() => this.requestClose(true)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button>
                </div>
                <label class="permission-search">${lucideIcon(Search, { size: 16, strokeWidth: 2 })}<input type="search" aria-label="Search permissions" placeholder="Search permissions" .value=${this.search} @input=${this.onSearchInput}></label>
              </div>
              <div class="permission-list">
                ${categories.length ? categories.map(([category, categoryPolicies], categoryIndex) => html`
                  <div class="permission-group" role="group" aria-labelledby=${`token-permission-category-${categoryIndex}`}>
                    <div class="permission-category" id=${`token-permission-category-${categoryIndex}`}>${category}</div>
                    ${categoryPolicies.map((policy) => html`
                      <label class="permission-option" data-policy=${policy.id} data-selected=${String(this.policySelected(policy, selectedKeys))}>
                        <input type="checkbox" .checked=${this.policySelected(policy, selectedKeys)} @change=${(event: Event) => this.togglePolicy(policy, (event.currentTarget as HTMLInputElement).checked)}>
                        <span>${policy.label}</span>
                      </label>
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
        ` : html`<div class="selected-permissions-empty"><span aria-hidden="true">${lucideIcon(LockKeyhole, { size: 28, strokeWidth: 1.8 })}</span><div class="settings-field"><span class="settings-label">No permissions added yet</span><span class="settings-description">This token will have no project or resource authority.</span></div></div>`}
      </div>
    `
  }

  private renderPolicy(policy: TokenPermissionPolicy, selectedKeys: Set<string>) {
    const resourcePolicy = policy.targetScope === 'resource'
    const scope = this.policyScope(policy, selectedKeys)
    const selectedResources = policy.options.filter((option) => this.optionSelected(option, selectedKeys))
    const configured = this.policyConfigured(policy, selectedKeys)
    const menuOpen = this.scopeMenuPolicyID === policy.id
    const query = this.resourceSearch.trim().toLocaleLowerCase()
    const resources = policy.options.filter((option) => option.label.toLocaleLowerCase().includes(query))
    return html`
      <section class="permission-policy" data-permission=${policy.id} data-configured=${String(configured)}>
        <div class="permission-policy-header">
          <div class="settings-field permission-policy-text">
            <span class="settings-label">${policy.label}</span>
            <span class="settings-description permission-policy-description">${policy.description}</span>
          </div>
          <div class="permission-policy-actions">
            ${resourcePolicy ? html`
              <div class="permission-scope-control">
                <button class="permission-scope-trigger" type="button" aria-label=${`Scope for ${policy.label}: ${configured ? this.scopeSummary(policy, scope, selectedResources) : 'Choose scope'}`} aria-haspopup="dialog" aria-expanded=${String(menuOpen)} @click=${() => this.toggleScopeMenu(policy)}><span>${configured ? this.scopeSummary(policy, scope, selectedResources) : 'Choose scope'}</span>${lucideIcon(ChevronDown, { size: 16, strokeWidth: 2 })}</button>
                ${menuOpen ? html`
                  <div class="permission-scope-backdrop" aria-hidden="true" @click=${() => this.closeScopeMenu(true)}></div>
                  <div class="permission-scope-menu" role="dialog" aria-label=${`Scope for ${policy.label}`}>
                    <div class="permission-scope-menu-header"><span class="settings-label">Apply to</span><button class="permission-scope-menu-close" type="button" aria-label="Close scope picker" @click=${() => this.closeScopeMenu(true)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button></div>
                    <div class="permission-scope-menu-body">
                      ${policy.options.length ? html`<label class="permission-scope-option"><input type="radio" name=${`scope-${policy.id}`} value="current" .checked=${this.scopeDraft.scope === 'current'} @change=${() => this.selectScope(policy, 'current')}><span>All current ${policy.category.toLocaleLowerCase()}</span></label>` : nothing}
                      ${policy.future ? html`<label class="permission-scope-option"><input type="radio" name=${`scope-${policy.id}`} value="future" .checked=${this.scopeDraft.scope === 'future'} @change=${() => this.selectScope(policy, 'future')}><span>Current and future ${policy.category.toLocaleLowerCase()}</span></label>` : nothing}
                      ${policy.options.length ? html`
                        <div class="permission-scope-specific"><span>Specific ${policy.category.toLocaleLowerCase()}</span></div>
                        <label class="permission-search">${lucideIcon(Search, { size: 16, strokeWidth: 2 })}<input type="search" aria-label=${`Search ${policy.category.toLocaleLowerCase()}`} placeholder=${`Search ${policy.category.toLocaleLowerCase()}`} .value=${this.resourceSearch} @input=${this.onResourceSearch}></label>
                        <div class="permission-resource-list">${resources.length ? resources.map((option) => html`<label class="permission-resource-option"><input type="checkbox" value=${option.id} .checked=${this.scopeDraft.scope === 'specific' && this.scopeDraft.resourceIDs.includes(option.id)} @change=${() => this.toggleResource(policy, option.id)}><span>${option.label}</span></label>`) : html`<div class="permission-empty">No resources match your search.</div>`}</div>
                      ` : nothing}
                    </div>
                  </div>
                ` : nothing}
              </div>
            ` : html`<span class="permission-scope-fixed">${this.scopeSummary(policy, scope, selectedResources)}</span>`}
            <button class="permission-policy-remove" type="button" aria-label=${`Remove ${policy.label}`} @click=${() => this.removePolicy(policy)}>${lucideIcon(X, { size: 16, strokeWidth: 2 })}</button>
          </div>
        </div>
      </section>
    `
  }

  private scopeSummary(policy: TokenPermissionPolicy, scope: TokenPermissionScope, resources: TokenPermissionOption[]): string {
    if (policy.targetScope !== 'resource') return policy.targetScope === 'project' ? 'Current project' : policy.targetScope === 'instance' ? 'This instance' : policy.description
    if (scope === 'future') return `All current and future ${policy.category.toLocaleLowerCase()}`
    if (scope === 'current') return `All current ${policy.category.toLocaleLowerCase()} (${resources.length})`
    return resources.length === 1 ? `Specific: ${resources[0].label}` : `Specific: ${resources.length} ${policy.category.toLocaleLowerCase()}`
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
  private policySelected(policy: TokenPermissionPolicy, keys = this.selectedPairKeys()): boolean {
    return this.selectedResourcePolicyIDs.includes(policy.id) || this.policyConfigured(policy, keys)
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
  private togglePolicy(policy: TokenPermissionPolicy, checked: boolean): void {
    if (!checked) { this.removePolicy(policy); return }
    if (policy.targetScope === 'resource') {
      this.selectedResourcePolicyIDs = [...this.selectedResourcePolicyIDs, policy.id]
      this.reportIncompletePolicyChange()
    }
    else this.emitSelectedPermissions([...this.effectiveSelectedPermissions(), ...policy.options.flatMap((option) => option.permissions)])
  }
  private removePolicy(policy: TokenPermissionPolicy): void {
    const remove = new Set([...policy.options, ...(policy.future ? [policy.future] : [])].flatMap((option) => option.permissions.map(permissionPairKey)))
    this.policyScopes = Object.fromEntries(Object.entries(this.policyScopes).filter(([id]) => id !== policy.id))
    this.selectedResourcePolicyIDs = this.selectedResourcePolicyIDs.filter((id) => id !== policy.id)
    if (this.scopeMenuPolicyID === policy.id) this.closeScopeMenu()
    this.emitSelectedPermissions(this.effectiveSelectedPermissions().filter((pair) => !remove.has(permissionPairKey(pair))))
    this.reportIncompletePolicyChange()
  }
  private selectScope(policy: TokenPermissionPolicy, scope: 'current' | 'future'): void {
    this.scopeDraft = { ...this.scopeDraft, scope }
    this.applyScope(policy)
    this.closeScopeMenu(true)
  }
  private toggleResource(policy: TokenPermissionPolicy, id: string): void {
    const resourceIDs = this.scopeDraft.resourceIDs.includes(id)
      ? this.scopeDraft.resourceIDs.filter((resourceID) => resourceID !== id)
      : [...this.scopeDraft.resourceIDs, id]
    this.scopeDraft = { scope: 'specific', resourceIDs }
    this.applyScope(policy)
  }
  private applyScope(policy: TokenPermissionPolicy): void {
    const policyKeys = new Set([...policy.options, ...(policy.future ? [policy.future] : [])].flatMap((option) => option.permissions.map(permissionPairKey)))
    const selected = this.effectiveSelectedPermissions().filter((pair) => !policyKeys.has(permissionPairKey(pair)))
    if (this.scopeDraft.scope === 'current') selected.push(...policy.options.flatMap((option) => option.permissions))
    else if (this.scopeDraft.scope === 'future') selected.push(...(policy.future?.permissions ?? []))
    else if (this.scopeDraft.scope === 'specific') selected.push(...policy.options.filter((option) => this.scopeDraft.resourceIDs.includes(option.id)).flatMap((option) => option.permissions))
    this.policyScopes = { ...this.policyScopes, [policy.id]: this.scopeDraft.scope as TokenPermissionScope }
    this.emitSelectedPermissions(selected)
    this.rememberSelectedResourcePolicy(policy)
    this.reportIncompletePolicyChange()
  }
  private rememberSelectedResourcePolicy(policy: TokenPermissionPolicy): void {
    if (!this.selectedResourcePolicyIDs.includes(policy.id)) this.selectedResourcePolicyIDs = [...this.selectedResourcePolicyIDs, policy.id]
  }
  private emitSelectedPermissions(permissions: PersonalPermissionPairSignal[]): void {
    const selected = uniquePermissionPairs(permissions)
    this.pendingPermissions = selected
    this.dispatchEvent(new CustomEvent('lv-personal-token-permissions-change', { detail: { permissions: selected }, bubbles: true, composed: true }))
  }
  private toggleScopeMenu(policy: TokenPermissionPolicy): void {
    if (this.scopeMenuPolicyID === policy.id) { this.closeScopeMenu(); return }
    this.requestClose(false)
    const selectedKeys = this.selectedPairKeys()
    const configured = this.policyConfigured(policy, selectedKeys)
    const scope = configured ? this.policyScope(policy, selectedKeys) : ''
    this.scopeDraft = { scope, resourceIDs: scope === 'specific' ? policy.options.filter((option) => this.optionSelected(option, selectedKeys)).map((option) => option.id) : [] }
    this.scopeMenuPolicyID = policy.id
    this.resourceSearch = ''
    void this.updateComplete.then(() => {
      this.fitScopeMenu()
      this.querySelector<HTMLInputElement>('.permission-scope-menu input:checked, .permission-scope-menu input[type="radio"], .permission-scope-menu input[type="search"]')?.focus()
    })
  }
  private fitScopeMenu(): void {
    if (window.matchMedia('(max-width: 40rem)').matches) return
    const menu = this.querySelector<HTMLElement>('.permission-scope-menu')
    const trigger = this.querySelector<HTMLElement>('.permission-scope-trigger[aria-expanded="true"]')
    if (!menu || !trigger) return
    const viewportPadding = 16
    const gap = 6
    const availableBelow = Math.max(0, window.innerHeight - trigger.getBoundingClientRect().bottom - gap - viewportPadding)
    const availableAbove = Math.max(0, trigger.getBoundingClientRect().top - gap - viewportPadding)
    const placeAbove = availableBelow < 224 && availableAbove > availableBelow
    menu.dataset.placement = placeAbove ? 'above' : 'below'
    menu.style.setProperty('--scope-menu-max-height', `${placeAbove ? availableAbove : availableBelow}px`)
  }
  private closeScopeMenu(returnFocus = false): void {
    if (!this.scopeMenuPolicyID) return
    const id = this.scopeMenuPolicyID
    this.scopeMenuPolicyID = ''
    this.scopeDraft = { scope: '', resourceIDs: [] }
    this.resourceSearch = ''
    if (returnFocus) void this.updateComplete.then(() => Array.from(this.querySelectorAll<HTMLElement>('.permission-policy')).find((policy) => policy.dataset.permission === id)?.querySelector<HTMLButtonElement>('.permission-scope-trigger')?.focus())
  }
  private requestToggle = (): void => {
    this.closeScopeMenu()
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
  private reconcileLocalState(): void {
    const valid = new Set(this.policies.map((policy) => policy.id))
    const scopes = Object.fromEntries(Object.entries(this.policyScopes).filter(([id]) => valid.has(id)))
    if (Object.keys(scopes).length !== Object.keys(this.policyScopes).length) this.policyScopes = scopes
    const selected = this.selectedResourcePolicyIDs.filter((id) => valid.has(id))
    if (selected.length !== this.selectedResourcePolicyIDs.length) this.selectedResourcePolicyIDs = selected
    if (this.scopeMenuPolicyID && !valid.has(this.scopeMenuPolicyID)) this.closeScopeMenu()
    else if (this.scopeMenuPolicyID && this.scopeDraft.scope === 'specific') {
      const policy = this.policies.find((candidate) => candidate.id === this.scopeMenuPolicyID)
      const available = new Set(policy?.options.map((option) => option.id))
      const resourceIDs = this.scopeDraft.resourceIDs.filter((id) => available.has(id))
      if (resourceIDs.length !== this.scopeDraft.resourceIDs.length) this.scopeDraft = { scope: 'specific', resourceIDs }
    }
  }
  private reportIncompletePolicyChange(): void {
    const hasIncompletePolicy = this.selectedResourcePolicyIDs.some((id) => {
      const policy = this.policies.find((candidate) => candidate.id === id)
      return policy && !this.policyConfigured(policy)
    })
    if (this.lastReportedIncomplete === hasIncompletePolicy) return
    this.lastReportedIncomplete = hasIncompletePolicy
    this.dispatchEvent(new CustomEvent('lv-personal-token-permission-incomplete-change', { detail: { hasIncompletePolicy }, bubbles: true, composed: true }))
  }
  private handleDocumentPointerDown = (event: PointerEvent): void => {
    if (!this.scopeMenuPolicyID) return
    const policy = Array.from(this.querySelectorAll<HTMLElement>('.permission-policy')).find((element) => element.dataset.permission === this.scopeMenuPolicyID)
    const control = policy?.querySelector('.permission-scope-control')
    if (control && !event.composedPath().includes(control)) this.closeScopeMenu()
  }
  private handleWindowKeydown = (event: KeyboardEvent): void => {
    if (event.key !== 'Escape' || !this.scopeMenuPolicyID) return
    event.preventDefault()
    this.closeScopeMenu(true)
  }
}

if (!customElements.get('lv-personal-token-permission-picker')) customElements.define('lv-personal-token-permission-picker', PersonalSettingsTokenPermissionPicker)
