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

const browserBrandNames = new Map<string, string>([
  ['brave', 'Brave'],
  ['microsoft edge', 'Edge'],
  ['opera', 'Opera'],
  ['vivaldi', 'Vivaldi'],
  ['samsung internet', 'Samsung Internet'],
  ['yandex', 'Yandex Browser'],
  ['duckduckgo', 'DuckDuckGo'],
  ['silk', 'Silk'],
  ['uc browser', 'UC Browser'],
  ['huawei browser', 'Huawei Browser'],
  ['miui browser', 'Xiaomi Browser'],
  ['xiaomi browser', 'Xiaomi Browser'],
  ['firefox', 'Firefox'],
  ['google chrome', 'Chrome'],
])

export function browserNameFromClientHints(userAgent: string, brands: readonly string[] = []): string | null {
  const hintedBrowser = brands
    .map((brand) => browserBrandNames.get(brand.trim().toLowerCase()))
    .find((name) => name !== undefined)

  const lower = userAgent.toLowerCase()
  const userAgentBrowser = [
    { name: 'Edge', match: /(?:edg|edgios|edga)\// },
    { name: 'Opera', match: /(?:opr|opera)\// },
    { name: 'Vivaldi', match: /vivaldi\// },
    { name: 'Samsung Internet', match: /samsungbrowser\// },
    { name: 'Yandex Browser', match: /yabrowser\// },
    { name: 'DuckDuckGo', match: /duckduckgo\// },
    { name: 'Silk', match: /silk\// },
    { name: 'UC Browser', match: /ucbrowser\// },
    { name: 'Huawei Browser', match: /huaweibrowser\// },
    { name: 'Xiaomi Browser', match: /(?:miuibrowser|xiaomibrowser)\// },
    { name: 'Brave', match: /brave(?:\/|$)/ },
    { name: 'Firefox', match: /(?:firefox|fxios)\// },
    { name: 'Chrome', match: /(?:crios|chrome)\// },
    { name: 'Safari', match: /safari\// },
  ].find((candidate) => candidate.match.test(lower))?.name

  // A branded UA token is more specific than the generic Chromium brand that
  // Chrome-based browsers expose through User-Agent Client Hints.
  if (userAgentBrowser && userAgentBrowser !== 'Chrome' && userAgentBrowser !== 'Safari') return userAgentBrowser
  return hintedBrowser ?? userAgentBrowser ?? null
}

export function detectCurrentBrowser(browserNavigator: Navigator & {
  brave?: { isBrave?: () => Promise<boolean> }
  userAgentData?: { brands?: readonly { brand: string }[] }
}, onBrave: () => void): string | null {
  void browserNavigator.brave?.isBrave?.().then((isBrave) => {
    if (isBrave) onBrave()
  }).catch(() => { /* Browser detection is optional. */ })
  return browserNameFromClientHints(browserNavigator.userAgent, browserNavigator.userAgentData?.brands?.map(({ brand }) => brand))
}

export function browserSessionLabel(session: PersonalSessionSignal, currentBrowserName: string | null): string {
  const label = session.clientLabel || humanizeSessionKind(session.kind)
  if (!session.current || session.kind !== 'browser' || !currentBrowserName) return label

  const splitAt = label.indexOf(' on ')
  const storedBrowserName = splitAt < 0 ? label : label.slice(0, splitAt)
  if (storedBrowserName === 'Browser' || storedBrowserName === 'Chrome' || storedBrowserName === 'Chromium') {
    return `${currentBrowserName}${splitAt < 0 ? '' : label.slice(splitAt)}`
  }
  return label
}

export function renderBrowserSessionRow(
  session: PersonalSessionSignal,
  onSelect: (session: SelectedSession) => void,
  onRevoke: (session: PendingSessionRevocation) => void,
  currentBrowserName: string | null = null,
) {
  const label = browserSessionLabel(session, currentBrowserName)
  return html`<tr class=${session.current ? 'is-current' : ''}>
    <td><button class="security-session-device" type="button" aria-label=${`View details for ${label}${session.current ? ', this device, current session' : ''}`} @click=${() => onSelect({ id: session.id, kind: 'browser' })}>
      <span class="security-session-icon" aria-hidden="true">${lucideIcon(Monitor, { size: 16, strokeWidth: 1.75 })}</span>
      <span class="security-session-device-copy"><span class="security-session-title"><strong>${label}</strong>${session.current ? html`<span class="security-badge">This device</span>` : nothing}</span><span class="security-session-kind">${session.current ? 'Current session · ' : ''}${humanizeSessionKind(session.kind)}</span></span>
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
