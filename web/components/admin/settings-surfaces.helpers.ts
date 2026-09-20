import type { AccessActivitySignal, AccessPrincipalSignal } from '../../generated/signals'

export function identitySourceLabel(principal: AccessPrincipalSignal): string {
  if (principal.identitySource === 'local') return 'Local'
  if (principal.identityProvider) return principal.identityProvider.toUpperCase()
  return principal.identitySource || 'System'
}

export function principalInitials(principal: AccessPrincipalSignal): string {
  return initialsForValue(principal.displayName || principal.email || principal.id)
}

export function currentPrincipalAvatarUrl(chrome: { sidebar?: { userAvatarUrl?: string } }, principalId: string): string {
  const avatarUrl = chrome.sidebar?.userAvatarUrl?.trim() ?? ''
  if (!avatarUrl) return ''
  try {
    const parts = new URL(avatarUrl, window.location.origin).pathname.split('/')
    return parts[1] === 'profile' && parts[2] === 'avatars' && decodeURIComponent(parts[3] ?? '') === principalId ? avatarUrl : ''
  } catch {
    return ''
  }
}

export function initialsForValue(value: string): string {
  const words = value.trim().split(/\s+/).filter(Boolean)
  return (words.length > 1 ? `${words[0][0]}${words[1][0]}` : value.slice(0, 2)).toUpperCase()
}

export function memberActionLabel(count: number): string {
  if (count === 0) return 'Add members'
  return `Add ${count} ${count === 1 ? 'member' : 'members'}`
}

export function formatAccessDate(value?: string): string {
  if (!value) return 'Never'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(parsed)
}

export function humanizeAccessValue(value: string): string {
  const normalized = value.replace(/[._-]+/g, ' ').trim()
  return normalized ? normalized[0].toUpperCase() + normalized.slice(1) : '—'
}

export function principalActivityLabel(activity: AccessActivitySignal): string {
  const actor = activity.actorName || activity.actorId || 'System'
  const labels: Record<string, string> = {
    'principal.local_user.created': 'created the local user',
    'principal.updated': 'updated the user profile',
    'principal.local_password.reset': 'reset the local password',
    'principal.blocked': 'blocked access',
    'principal.unblocked': 'unblocked access',
    'principal.sessions.revoked': 'revoked all sessions',
    'principal.credentials.revoked_all': 'revoked all credentials',
  }
  return `${actor} ${labels[activity.action] || humanizeAccessValue(activity.action).toLowerCase()}`
}

export function formValue(form: HTMLFormElement, name: string): string {
  return (form.elements.namedItem(name) as HTMLInputElement | HTMLSelectElement | null)?.value.trim() || ''
}
