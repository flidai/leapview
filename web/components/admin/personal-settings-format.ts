import { html } from 'lit'

export function humanizeCapability(value: string): string {
  return value.toLocaleLowerCase().split('_').filter(Boolean).map((part, index) => index === 0 ? `${part.charAt(0).toLocaleUpperCase()}${part.slice(1)}` : part).join(' ')
}

export function sessionFact(label: string, value: string, code = false) {
  const display = value || 'Unknown'
  return html`<div class="session-drawer-fact"><dt>${label}</dt> <dd>${code ? html`<code>${display}</code>` : display}</dd></div>`
}

export function formatDate(value: string): string {
  if (!value) return 'unknown'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toLocaleString()
}

export function formatRelativeActivity(value: string): string {
  if (!value) return 'unknown'
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return value
  const elapsed = Date.now() - date.valueOf()
  if (elapsed < 0) return formatDate(value)
  const minute = 60_000
  const hour = 60 * minute
  const day = 24 * hour
  if (elapsed < minute) return 'just now'
  if (elapsed < hour) {
    const minutes = Math.max(1, Math.floor(elapsed / minute))
    return `${minutes} ${minutes === 1 ? 'minute' : 'minutes'} ago`
  }
  if (elapsed < day) {
    const hours = Math.max(1, Math.floor(elapsed / hour))
    return `${hours} ${hours === 1 ? 'hour' : 'hours'} ago`
  }
  if (elapsed < 2 * day) return 'yesterday'
  if (elapsed < 7 * day) return `${Math.floor(elapsed / day)} days ago`
  return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: date.getFullYear() === new Date().getFullYear() ? undefined : 'numeric' })
}

export function humanizeSessionKind(value: string): string {
  const normalized = value.trim().replace(/[._-]+/g, ' ')
  if (!normalized) return 'Session'
  return normalized.charAt(0).toUpperCase() + normalized.slice(1)
}
