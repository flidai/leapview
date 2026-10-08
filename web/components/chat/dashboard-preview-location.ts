import type { DashboardChatComponent } from './dashboard-preview-contract'

// Owns same-document preview navigation. Chat renders the selected surface;
// this controller retains the conversation URL and history entry independently.
export function setChatPreviewLocation(mode: 'builder' | 'dashboard' | null, replace = false): void {
  const url = new URL(window.location.href)
  if (mode) url.searchParams.set('preview', mode)
  else url.searchParams.delete('preview')
  if (replace) window.history.replaceState(window.history.state, '', url)
  else window.history.pushState(window.history.state, '', url)
}

export function clearChatDashboardLocation(): void {
  const url = new URL(window.location.href)
  url.searchParams.delete('dashboard')
  window.history.replaceState(window.history.state, '', url)
}

export function rememberChatDashboardLocation(href: string, links: Record<string, DashboardChatComponent[]>, signature: string): void {
  const url = new URL(window.location.href)
  url.searchParams.set('dashboard', href)
  // Only identities belong in history; the server owns the current draft.
  window.history.replaceState({ ...window.history.state, chatDashboard: { href, links, signature } }, '', url)
}
