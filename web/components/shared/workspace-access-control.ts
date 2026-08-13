import { LitElement, css, html, nothing } from 'lit'
import { property, state } from 'lit/decorators.js'
import { Trash2, UserRound, Users } from 'lucide'
import { lucideIcon } from './lucide-icons'
import './drawer'
import './entity-multi-select'
import type { EntityMultiSelectItem } from './entity-multi-select'

type Workspace = {
  id?: string
  title?: string
}

type Role = {
  name: string
}

type Binding = {
  id?: string
  subjectType?: string
  subjectId?: string
  principalId: string
  email: string
  displayName?: string
  groupName?: string
  role: string
}

type AccessCandidate = {
  subjectType: 'principal' | 'group'
  subjectId: string
  label: string
  detail: string
}

type AccessStatus = {
  loading?: boolean
  error?: string
  message?: string
}

type SearchStatus = {
  loading?: boolean
  error?: string
}

type WorkspaceAccess = {
  workspace?: Workspace
  objectType?: string
  objectId?: string
  objectTitle?: string
  mode?: string
  roles?: Role[]
  bindings?: Binding[]
  candidates?: AccessCandidate[]
  canManage?: boolean
  search?: string
  searchStatus?: SearchStatus
  status?: AccessStatus
}

type WorkspaceAccessInput = {
  workspace?: unknown
  roles?: unknown
  bindings?: unknown
  candidates?: unknown
  canManage?: unknown
  search?: unknown
  searchStatus?: unknown
  status?: unknown
}

type AccessCommand = {
  email?: string
  role?: string
  privilege?: string
  principalId?: string
  bindingId?: string
  subjectType?: string
  subjectId?: string
  subjects?: Array<{ subjectType: string; subjectId: string }>
}

const emptyAccess: WorkspaceAccess = {
  roles: [],
  bindings: [],
  candidates: [],
  canManage: false,
  search: '',
  searchStatus: {},
  status: {},
}

class WorkspaceAccessControl extends LitElement {
  @property({ attribute: false }) access: WorkspaceAccessInput | null = null
  @property({ attribute: 'access' }) accessAttribute = ''
  @property({ attribute: 'search' }) searchAttribute = ''

  @state() private open = false
  @state() private selectedRole = ''
  @state() private query = ''
  @state() private selectedSubjectKeys: string[] = []

  private selectedSubjects = new Map<string, AccessCandidate>()

  private previousFocus: HTMLElement | null = null

  static styles = css`
    :host {
      display: inline-block;
      color: var(--lv-fg-default);
      font-family: var(--fontStack-system);
    }

    button,
    input,
    select {
      font: inherit;
    }

    .trigger {
      display: inline-flex;
      min-height: var(--lv-button-height);
      align-items: center;
      justify-content: center;
      gap: var(--base-size-6);
      border: var(--borderWidth-default) solid var(--lv-button-border-rest);
      border-radius: var(--lv-button-radius);
      background: var(--lv-button-bg-rest);
      color: var(--lv-button-fg-rest);
      cursor: pointer;
      font: var(--lv-type-body-compact);
      font-weight: var(--base-text-weight-medium);
      padding: 0 var(--lv-button-padding-inline);
      transition:
        color var(--lv-transition-fast),
        background-color var(--lv-transition-fast),
        border-color var(--lv-transition-fast);
    }

    .trigger:hover,
    .trigger:focus-visible {
      border-color: var(--lv-button-border-hover);
      background: var(--lv-button-bg-hover);
      color: var(--lv-fg-default);
      outline: var(--focus-outline, var(--lv-border-default));
      outline-color: var(--borderColor-accent-emphasis, var(--lv-line-accent));
      outline-offset: var(--focus-outline-offset, var(--base-size-2));
    }

    .trigger:disabled {
      cursor: not-allowed;
      opacity: var(--opacity-disabled);
    }

    .candidate-batch-add {
      justify-self: end;
    }

    .icon {
      display: inline-flex;
      width: var(--lv-icon-sm);
      height: var(--lv-icon-sm);
      align-items: center;
      justify-content: center;
      color: currentColor;
    }

    .title {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-section-title);
    }

    .subtitle {
      margin: var(--base-size-4) 0 0;
      color: var(--lv-fg-muted);
      font: var(--lv-type-body-snug);
    }

    .drawer-body {
      display: grid;
      gap: var(--base-size-24);
      min-height: 0;
    }

    .card {
      display: grid;
      gap: var(--lv-space-control);
    }

    .section-title {
      margin: 0;
      color: var(--lv-fg-default);
      font: var(--lv-type-body-snug);
      font-weight: var(--base-text-weight-semibold);
    }

    .label {
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .field-shell {
      display: flex;
      min-height: var(--lv-control-medium);
      min-width: 0;
      align-items: center;
      gap: var(--base-size-8);
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
      padding: 0 var(--lv-space-control);
      transition:
        background-color var(--lv-transition-fast),
        border-color var(--lv-transition-fast);
    }

    .field-shell:focus-within,
    .field-shell:hover {
      border-color: var(--lv-line-accent);
      background: var(--lv-bg-control-hover);
    }

    input,
    select {
      min-height: var(--lv-control-medium);
      min-width: 0;
      border: var(--lv-border-default);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-control);
      color: var(--lv-fg-default);
      font: var(--lv-type-body-snug);
      padding: 0 var(--base-size-8);
    }

    .field-shell input {
      min-height: auto;
      flex: 1 1 auto;
      border: 0;
      border-radius: 0;
      background: transparent;
      padding: 0;
      outline: 0;
    }

    input::placeholder {
      color: var(--lv-fg-muted);
    }

    input:focus,
    select:focus {
      border-color: var(--lv-line-accent);
      outline: var(--lv-border-width-focus) solid var(--lv-line-accent-muted);
      outline-offset: 0;
    }

    .access-search {
      min-height: var(--lv-control-large);
    }

    .access-search input:disabled {
      cursor: not-allowed;
      opacity: var(--opacity-disabled);
    }

    .role-field {
      display: grid;
      gap: var(--base-size-6);
    }

    .assignment-role {
      width: 100%;
    }

    .candidate-list {
      display: grid;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
    }

    .candidate {
      display: grid;
      min-width: 0;
      grid-template-columns: var(--base-size-32) minmax(0, 1fr) auto;
      align-items: center;
      gap: var(--lv-space-control);
      border-bottom: var(--lv-border-muted);
      background: transparent;
      color: var(--lv-fg-default);
      padding: var(--lv-space-control) var(--base-size-12);
    }

    .candidate:last-child {
      border-bottom: 0;
    }

    .candidate:hover {
      background: var(--lv-bg-control-hover);
    }

    .subject-icon {
      display: inline-flex;
      width: var(--base-size-32);
      height: var(--base-size-32);
      align-items: center;
      justify-content: center;
      border-radius: var(--lv-radius-full);
      background: var(--lv-bg-control);
      color: var(--lv-fg-muted);
    }

    .subject-icon-group {
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-accent-muted);
      color: var(--lv-fg-accent);
    }

    .subject-copy {
      display: grid;
      min-width: 0;
    }

    .subject-label,
    .subject-detail {
      overflow: hidden;
      margin: 0;
      text-overflow: ellipsis;
      white-space: nowrap;
    }

    .subject-label {
      color: var(--lv-fg-default);
      font: var(--lv-type-body-snug);
      font-weight: var(--base-text-weight-semibold);
    }

    .subject-detail {
      margin-top: var(--base-size-2);
      color: var(--lv-fg-muted);
      font: var(--lv-type-caption);
    }

    .search-state {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-default);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
      padding: var(--base-size-16);
      text-align: center;
    }

    .search-state-error {
      color: var(--lv-fg-danger);
    }

    .row-action {
      display: inline-flex;
      width: var(--lv-control-medium);
      height: var(--lv-control-medium);
      flex: 0 0 auto;
      align-items: center;
      justify-content: center;
      border: var(--lv-border-transparent);
      border-radius: var(--lv-radius-default);
      background: transparent;
      color: var(--lv-fg-muted);
      cursor: pointer;
      padding: 0;
      transition:
        color var(--lv-transition-fast),
        background-color var(--lv-transition-fast),
        border-color var(--lv-transition-fast);
    }

    .row-action:hover,
    .row-action:focus-visible {
      border-color: var(--lv-line-muted);
      background: var(--lv-bg-control-hover);
      color: var(--lv-fg-default);
      outline: 0;
    }

    .candidate-add {
      color: var(--lv-fg-accent);
    }

    .row-action:disabled {
      cursor: not-allowed;
      opacity: var(--opacity-disabled);
    }

    .status {
      border-radius: var(--lv-radius-default);
      font: var(--lv-type-body-snug);
      padding: var(--base-size-8) var(--base-size-12);
    }

    .status-error {
      border: var(--lv-border-danger);
      background: var(--lv-bg-danger-muted);
      color: var(--lv-fg-danger);
    }

    .status-message {
      border: var(--borderWidth-default) solid var(--lv-line-success-muted);
      background: var(--lv-bg-success-muted);
      color: var(--lv-fg-success);
    }

    .list {
      display: grid;
      overflow: hidden;
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-tight);
      background: var(--lv-bg-panel);
    }

    .row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) minmax(8rem, 10rem) auto;
      align-items: center;
      gap: var(--base-size-12);
      border-bottom: var(--lv-border-muted);
      padding: var(--lv-space-control) var(--base-size-12);
    }

    .row:last-child {
      border-bottom: 0;
    }

    .person {
      display: grid;
      min-width: 0;
      grid-template-columns: var(--base-size-32) minmax(0, 1fr);
      align-items: center;
      gap: var(--base-size-8);
    }

    .empty {
      border: var(--lv-border-muted);
      border-radius: var(--lv-radius-tight);
      background: var(--lv-bg-panel);
      color: var(--lv-fg-muted);
      font: var(--lv-type-body);
      padding: var(--base-size-20) var(--base-size-16);
      text-align: center;
    }

    @media (max-width: 44rem) {
      .row {
        grid-template-columns: minmax(0, 1fr);
      }
    }
  `

  updated(changed: Map<string, unknown>): void {
    if (changed.has('access') || changed.has('accessAttribute')) {
      this.ensureRole()
    }
    if (changed.has('searchAttribute') && this.searchAttribute !== this.query) {
      this.query = this.searchAttribute
    }
  }

  render() {
    const access = this.resolvedAccess
    if (!access.canManage) return nothing

    return html`
      <button class="trigger" type="button" aria-haspopup="dialog" aria-expanded=${String(this.open)} @click=${this.openDialog}>
        ${usersIcon()}
        <span>Manage access</span>
      </button>
      ${this.open ? this.renderDrawer(access) : nothing}
    `
  }

  private renderDrawer(access: WorkspaceAccess) {
    const status = access.status ?? {}
    return html`
      <lv-drawer open label="Manage access" @lv-drawer-close=${this.closeDialog}>
        <h2 class="title" slot="title" id="workspace-access-title">Manage access</h2>
        <p class="subtitle" slot="subtitle">${this.drawerSubtitle(access)}</p>
        <div class="drawer-body">
          <section class="card" aria-label="Add workspace access">
            <div class="label">Add people or groups</div>
            ${status.error ? html`<div class="status status-error" role="alert">${status.error}</div>` : nothing}
            ${status.message && !status.error ? html`<div class="status status-message" role="status">${status.message}</div>` : nothing}
            <label class="role-field">
              <span class="label">${this.modeIsObject(access) ? 'Privilege' : 'Role'}</span>
              <select
                class="assignment-role"
                aria-label=${this.modeIsObject(access) ? 'Privilege to grant' : 'Role to assign'}
                .value=${this.selectedRole}
                ?disabled=${status.loading}
                @change=${this.handleRoleChange}
              >
                <option value="">${this.modeIsObject(access) ? 'Select a privilege' : 'Select a role'}</option>
                ${this.roles.map((role) => html`<option value=${role.name}>${roleLabel(role.name)}</option>`)}
              </select>
            </label>
            ${this.renderCandidates(access, status)}
          </section>
          <section class="card" aria-label="Current workspace access">
            <h3 class="section-title">${this.modeIsObject(access) ? 'Direct grants' : 'People with access'}</h3>
            ${this.renderBindings(access)}
          </section>
        </div>
      </lv-drawer>
    `
  }

  private renderCandidates(access: WorkspaceAccess, status: AccessStatus) {
    const searchStatus = access.searchStatus ?? {}
    const candidates = this.query.trim() ? access.candidates ?? [] : []
    const pickerItems: EntityMultiSelectItem[] = candidates.map((candidate) => ({
      id: candidateKey(candidate), label: candidate.label, detail: candidate.detail, kind: candidate.subjectType,
    }))
    const emptyMessage = !this.selectedRole
      ? `Select a ${this.modeIsObject(access) ? 'privilege' : 'role'} to search people and groups.`
      : this.query.trim()
        ? 'No people or groups found.'
        : 'Search by name or email.'
    return html`
      <lv-entity-multi-select
        label="People and groups"
        searchPlaceholder="Search people and groups..."
        .items=${pickerItems}
        .selectedIds=${this.selectedSubjectKeys}
        .emptyMessage=${emptyMessage}
        noResultsMessage="No people or groups found."
        remoteSearch
        ?disabled=${!this.selectedRole || status.loading}
        @lv-entity-search=${this.handleEntitySearch}
        @lv-entity-selection-change=${(event: CustomEvent<{ selectedIds: string[] }>) => this.updateSelectedSubjects(event, candidates)}
      ></lv-entity-multi-select>
      ${searchStatus.error ? html`<div class="search-state search-state-error" role="alert">${searchStatus.error}</div>` : nothing}
      <button
        class="trigger candidate-batch-add"
        type="button"
        ?disabled=${status.loading || this.selectedSubjectKeys.length === 0}
        @click=${this.addSelectedSubjects}
      >${assignmentActionLabel(this.selectedSubjectKeys.length)}</button>
    `
  }

  private renderBindings(access: WorkspaceAccess) {
    const rows = access.bindings ?? []
    if (rows.length === 0) {
      return html`<div class="empty">No role bindings yet.</div>`
    }
    return html`
      <div class="list">
        ${rows.map((binding) => {
          const subjectType = binding.subjectType === 'group' ? 'group' : 'principal'
          const detail = subjectType === 'group' ? 'Group' : binding.email
          return html`
            <div class="row">
              <div class="person">
                ${subjectIcon(subjectType)}
                <span class="subject-copy">
                  <p class="name subject-label">${displayLabel(binding)}</p>
                  <p class="email subject-detail">${detail}</p>
                </span>
              </div>
              <select
                aria-label=${`${this.modeIsObject(access) ? 'Privilege' : 'Role'} for ${displayLabel(binding)}`}
                .value=${binding.role}
                ?disabled=${access.status?.loading}
                @change=${(event: Event) => this.updateBindingRole(binding, (event.currentTarget as HTMLSelectElement).value)}
              >
                ${this.roles.map((role) => html`<option value=${role.name}>${roleLabel(role.name)}</option>`)}
              </select>
              <button
                class="row-action"
                type="button"
                aria-label=${`Remove ${displayLabel(binding)}`}
                ?disabled=${access.status?.loading}
                @click=${() => this.removeBinding(binding)}
              >
                ${trashIcon()}
              </button>
            </div>
          `
        })}
      </div>
    `
  }

  private get resolvedAccess(): WorkspaceAccess {
    if (this.access) return normalizeAccess(this.access)
    if (this.accessAttribute) {
      try {
        return normalizeAccess(JSON.parse(this.accessAttribute) as WorkspaceAccessInput)
      } catch {
        return emptyAccess
      }
    }
    return emptyAccess
  }

  private get roles(): Role[] {
    return this.resolvedAccess.roles ?? []
  }

  private ensureRole(): void {
    const roles = this.roles
    if (roles.some((role) => role.name === this.selectedRole)) return
    this.selectedRole = ''
  }

  private modeIsObject(access = this.resolvedAccess): boolean {
    return access.mode === 'object'
  }

  private drawerSubtitle(access: WorkspaceAccess): string {
    if (this.modeIsObject(access)) {
      const title = access.objectTitle || access.objectId || 'This asset'
      return `${title} grants apply only to this asset.`
    }
    return `${access.workspace?.title ?? 'Workspace'} roles apply to every published asset in this workspace.`
  }

  private readonly handleEntitySearch = (event: CustomEvent<{ query: string }>): void => {
    this.query = event.detail.query
    this.dispatchEvent(new CustomEvent('lv-workspace-access-search', {
      bubbles: true,
      composed: true,
      detail: { search: this.query },
    }))
  }

  private readonly handleRoleChange = (event: Event): void => {
    this.selectedRole = (event.currentTarget as HTMLSelectElement).value
    this.selectedSubjectKeys = []
    this.selectedSubjects.clear()
  }

  private readonly openDialog = (): void => {
    this.previousFocus = document.activeElement as HTMLElement | null
    this.open = true
    window.setTimeout(() => {
      this.renderRoot.querySelector('lv-drawer')?.focusFirst()
    }, 0)
  }

  private readonly closeDialog = (): void => {
    this.open = false
    window.setTimeout(() => {
      if (this.previousFocus?.isConnected) this.previousFocus.focus()
      this.previousFocus = null
    }, 0)
  }

  private updateSelectedSubjects(event: CustomEvent<{ selectedIds: string[] }>, candidates: AccessCandidate[]): void {
    const selected = new Set(event.detail.selectedIds)
    for (const candidate of candidates) {
      const key = candidateKey(candidate)
      if (selected.has(key)) this.selectedSubjects.set(key, candidate)
    }
    for (const key of this.selectedSubjects.keys()) {
      if (!selected.has(key)) this.selectedSubjects.delete(key)
    }
    this.selectedSubjectKeys = [...event.detail.selectedIds]
  }

  private readonly addSelectedSubjects = (): void => {
    const role = (this.renderRoot.querySelector('.assignment-role') as HTMLSelectElement | null)?.value.trim() ?? ''
    const subjects = this.selectedSubjectKeys
      .map((key) => this.selectedSubjects.get(key))
      .filter((candidate): candidate is AccessCandidate => Boolean(candidate))
      .map((candidate) => ({ subjectType: candidate.subjectType, subjectId: candidate.subjectId }))
    if (!role || subjects.length === 0) return
    const command: AccessCommand = {
      email: '',
      bindingId: '',
      principalId: '',
      role: this.modeIsObject() ? '' : role,
      privilege: this.modeIsObject() ? role : '',
      subjectType: '',
      subjectId: '',
      subjects,
    }
    this.dispatchEvent(new CustomEvent('lv-workspace-access-upsert', {
      bubbles: true,
      composed: true,
      detail: command,
    }))
    this.query = ''
    this.selectedSubjectKeys = []
    this.selectedSubjects.clear()
    this.renderRoot.querySelector('lv-entity-multi-select')?.clear()
  }

  private updateBindingRole(binding: Binding, role: string): void {
    if (!role || role === binding.role) return
    this.dispatchEvent(new CustomEvent('lv-workspace-access-upsert', {
      bubbles: true,
      composed: true,
      detail: {
        email: binding.email,
        role: this.modeIsObject() ? '' : role,
        privilege: this.modeIsObject() ? role : '',
        bindingId: binding.id,
        subjectType: binding.subjectType || 'principal',
        subjectId: binding.subjectId || binding.principalId,
      },
    }))
  }

  private removeBinding(binding: Binding): void {
    if (!binding.principalId && !binding.id) return
    this.dispatchEvent(new CustomEvent('lv-workspace-access-remove', {
      bubbles: true,
      composed: true,
      detail: {
        principalId: binding.principalId,
        bindingId: binding.id,
        subjectType: binding.subjectType,
        subjectId: binding.subjectId,
      },
    }))
  }
}

function normalizeAccess(access: WorkspaceAccessInput): WorkspaceAccess {
  const raw = recordValue(access)
  return {
    workspace: normalizeWorkspace(access.workspace),
    objectType: stringValue(raw.objectType ?? raw.ObjectType),
    objectId: stringValue(raw.objectId ?? raw.ObjectID),
    objectTitle: stringValue(raw.objectTitle ?? raw.ObjectTitle),
    mode: stringValue(raw.mode ?? raw.Mode),
    roles: Array.isArray(access.roles) ? access.roles.map(normalizeRole).filter(isRole) : [],
    bindings: Array.isArray(access.bindings) ? access.bindings.map(normalizeBinding).filter(isBinding) : [],
    candidates: Array.isArray(access.candidates) ? access.candidates.map(normalizeCandidate).filter(isCandidate) : [],
    canManage: Boolean(access.canManage ?? raw.CanManage),
    search: stringValue(access.search ?? raw.Search),
    searchStatus: normalizeSearchStatus(access.searchStatus ?? raw.SearchStatus),
    status: normalizeStatus(access.status ?? raw.Status),
  }
}

function normalizeWorkspace(workspace: unknown): Workspace {
  const raw = recordValue(workspace)
  return {
    id: stringValue(raw.id ?? raw.ID),
    title: stringValue(raw.title ?? raw.Title),
  }
}

function normalizeRole(role: unknown): Role | null {
  if (typeof role === 'string') {
    const name = role.trim()
    return name ? { name } : null
  }
  const raw = recordValue(role)
  const name = stringValue(raw.name ?? raw.Name).trim()
  return name ? { name } : null
}

function normalizeBinding(binding: unknown): Binding | null {
  const raw = recordValue(binding)
  const email = stringValue(raw.email ?? raw.Email)
  const principalId = stringValue(raw.principalId ?? raw.PrincipalID)
  const subjectId = stringValue(raw.subjectId ?? raw.SubjectID)
  const role = stringValue(raw.role ?? raw.Role)
  if (!email && !principalId && !subjectId) return null
  return {
    id: stringValue(raw.id ?? raw.ID),
    subjectType: stringValue(raw.subjectType ?? raw.SubjectType),
    subjectId,
    principalId,
    email,
    displayName: stringValue(raw.displayName ?? raw.DisplayName),
    groupName: stringValue(raw.groupName ?? raw.GroupName),
    role,
  }
}

function normalizeCandidate(candidate: unknown): AccessCandidate | null {
  const raw = recordValue(candidate)
  const subjectType = stringValue(raw.subjectType ?? raw.SubjectType)
  const subjectId = stringValue(raw.subjectId ?? raw.SubjectID)
  const label = stringValue(raw.label ?? raw.Label)
  if ((subjectType !== 'principal' && subjectType !== 'group') || !subjectId || !label) return null
  return {
    subjectType,
    subjectId,
    label,
    detail: stringValue(raw.detail ?? raw.Detail),
  }
}

function normalizeStatus(status: unknown): AccessStatus {
  const raw = recordValue(status)
  return {
    loading: Boolean(raw.loading ?? raw.Loading),
    error: stringValue(raw.error ?? raw.Error),
    message: stringValue(raw.message ?? raw.Message),
  }
}

function normalizeSearchStatus(status: unknown): SearchStatus {
  const raw = recordValue(status)
  return {
    loading: Boolean(raw.loading ?? raw.Loading),
    error: stringValue(raw.error ?? raw.Error),
  }
}

function isRole(role: Role | null): role is Role {
  return role !== null
}

function isBinding(binding: Binding | null): binding is Binding {
  return binding !== null
}

function isCandidate(candidate: AccessCandidate | null): candidate is AccessCandidate {
  return candidate !== null
}

function recordValue(value: unknown): Record<string, unknown> {
  return value && typeof value === 'object' ? value as Record<string, unknown> : {}
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function displayLabel(binding: Binding): string {
  return binding.displayName || binding.groupName || binding.email || binding.subjectId || 'Principal'
}

function candidateKey(candidate: AccessCandidate): string {
  return `${candidate.subjectType}:${candidate.subjectId}`
}

function assignmentActionLabel(count: number): string {
  if (count === 0) return 'Grant access'
  return `Grant access to ${count}`
}

function roleLabel(role: string): string {
  return role.replaceAll('_', ' ').replace(/\b\w/g, (letter) => letter.toUpperCase())
}

function subjectCopy(label: string, detail: string) {
  return html`
    <span class="subject-copy">
      <span class="subject-label">${label}</span>
      <span class="subject-detail">${detail}</span>
    </span>
  `
}

function subjectIcon(subjectType: 'principal' | 'group', extraClass = '') {
  const className = `${extraClass} subject-icon subject-icon-${subjectType}`.trim()
  const icon = subjectType === 'group' ? Users : UserRound
  return html`<span class=${className} aria-hidden="true">${lucideIcon(icon, { size: 16 })}</span>`
}

function trashIcon() {
  return html`<span class="icon" aria-hidden="true">${lucideIcon(Trash2, { size: 16 })}</span>`
}

function usersIcon() {
  return html`<span class="icon" aria-hidden="true">${lucideIcon(Users, { size: 16 })}</span>`
}

if (!customElements.get('lv-workspace-access-control')) customElements.define('lv-workspace-access-control', WorkspaceAccessControl)

declare global {
  interface HTMLElementTagNameMap {
    'lv-workspace-access-control': WorkspaceAccessControl
  }
}
