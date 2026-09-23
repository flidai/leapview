import { html, nothing } from 'lit'
import { Monitor, Terminal } from 'lucide'
import type { PersonalAuthoringSessionSignal, PersonalSessionSignal } from '../../generated/signals'
import { lucideIcon } from '../shared/lucide-icons'
import { formatRelativeActivity, formatSessionDate, humanizeSessionKind } from './personal-settings-format'

export type PendingSessionRevocation = {
  id: string
  label: string
  kind: 'browser' | 'authoring'
  current?: boolean
}

export type SelectedSession = {
  id: string
  kind: 'browser' | 'authoring'
}

export function renderBrowserSessionRow(
  session: PersonalSessionSignal,
  onSelect: (session: SelectedSession) => void,
  onRevoke: (session: PendingSessionRevocation) => void,
) {
  const label = session.clientLabel || humanizeSessionKind(session.kind)
  return html`<tr class=${session.current ? 'is-current' : ''}>
    <td><button class="security-session-device" type="button" aria-label=${`View details for ${label}`} @click=${() => onSelect({ id: session.id, kind: 'browser' })}>
      <span class="security-session-icon" aria-hidden="true">${lucideIcon(Monitor, { size: 16, strokeWidth: 1.75 })}</span>
      <span class="security-session-device-copy"><span class="security-session-title"><strong>${label}</strong>${session.current ? html`<span class="security-badge">Current</span>` : nothing}</span><span class="security-session-kind">${humanizeSessionKind(session.kind)}</span></span>
    </button></td>
    <td class="security-session-access">Account</td>
    <td><time>${formatSessionDate(session.createdAt)}</time></td>
    <td><time>${session.current ? 'Active now' : formatRelativeActivity(session.lastSeenAt)}</time></td>
    <td class="security-session-action-cell"><button class="session-action" type="button" @click=${() => onRevoke({ id: session.id, label, kind: 'browser', current: session.current })}>${session.current ? 'Sign out' : 'Revoke'}</button></td>
  </tr>`
}

export function renderAuthoringSessionRow(
  session: PersonalAuthoringSessionSignal,
  onSelect: (session: SelectedSession) => void,
  onRevoke: (session: PendingSessionRevocation) => void,
) {
  const label = session.clientId || humanizeSessionKind(session.kind)
  const access = [session.projectId || 'All projects', session.permissions.map((permission) => permission.action.replaceAll('.', ' ')).join(', ') || 'Scoped access'].join(' · ')
  return html`<tr>
    <td><button class="security-session-device" type="button" aria-label=${`View details for ${label}`} @click=${() => onSelect({ id: session.id, kind: 'authoring' })}>
      <span class="security-session-icon" aria-hidden="true">${lucideIcon(Terminal, { size: 16, strokeWidth: 1.75 })}</span>
      <span class="security-session-device-copy"><span class="security-session-title"><strong>${label}</strong></span><span class="security-session-kind">${humanizeSessionKind(session.kind)}</span></span>
    </button></td>
    <td class="security-session-access">${access}</td>
    <td><time>${formatSessionDate(session.createdAt)}</time></td>
    <td><time>${session.lastUsedAt ? formatRelativeActivity(session.lastUsedAt) : 'Never used'}</time></td>
    <td class="security-session-action-cell"><button class="session-action" type="button" @click=${() => onRevoke({ id: session.id, label, kind: 'authoring' })}>Revoke</button></td>
  </tr>`
}
