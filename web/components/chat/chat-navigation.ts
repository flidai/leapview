import type { AgentReferenceSignal } from '../../generated/signals'

export type DrawerReturnState = {
  conversationId: string
  draft: string
  references: AgentReferenceSignal[]
  editMessageId: string
  selectedVisualId: string
  selectedExplorerHref: string
  selectedVisualTitle: string
  scroll: { top: number; follow: boolean }
}
type ReturnEntry = { href: string; created: number; state: DrawerReturnState; fullChatPresented?: boolean }
const storageKey = 'leapview-chat-returns-v1'
const builderHandoffKey = 'leapview-builder-chat-handoffs-v1'
const maxAge = 24 * 60 * 60 * 1000

export function safeChatReturnURL(value: string, origin: string): string | undefined {
  if (!value.startsWith('/') || value.startsWith('//') || value.includes('\\')) return undefined
  try {
    const url = new URL(value, origin)
    if (url.origin !== origin || !(/^\/dashboards(?:\/|$)/.test(url.pathname) || url.pathname === '/explore')) return undefined
    return url.pathname + url.search + url.hash
  } catch { return undefined }
}

function record(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}
function text(value: unknown, limit = 8192): value is string {
  return typeof value === 'string' && value.length <= limit
}
function textList(value: unknown): boolean {
  return Array.isArray(value) && value.length <= 64 && value.every(item => text(item))
}
function reference(value: unknown): boolean {
  return record(value) && record(value.reference) && text(value.reference.kind, 256) && text(value.reference.id, 256)
    && text(value.name) && text(value.href) && textList(value.hierarchy) && textList(value.context)
    && ['description', 'visualType'].every(key => value[key] === undefined || text(value[key]))
    && Array.isArray(value.locations) && value.locations.length <= 64 && value.locations.every(location =>
      record(location) && text(location.href)
      && ['dashboardId', 'dashboardName', 'pageId', 'pageName'].every(key => location[key] === undefined || text(location[key])))
}
function validState(value: unknown): value is DrawerReturnState {
  return record(value) && text(value.conversationId, 256) && text(value.draft, 100_000)
    && ['editMessageId', 'selectedVisualId', 'selectedExplorerHref', 'selectedVisualTitle'].every(key => text(value[key]))
    && Array.isArray(value.references) && value.references.length <= 64 && value.references.every(reference)
    && record(value.scroll) && typeof value.scroll.top === 'number' && Number.isFinite(value.scroll.top)
    && value.scroll.top >= 0 && value.scroll.top <= 100_000_000 && typeof value.scroll.follow === 'boolean'
}

function entries(key = storageKey): Record<string, ReturnEntry> {
  try {
    const encoded = sessionStorage.getItem(key) ?? '{}'
    if (encoded.length > 2_000_000) return {}
    const raw: unknown = JSON.parse(encoded)
    if (!record(raw)) return {}
    return Object.fromEntries(Object.entries(raw).slice(-16).filter(([, entry]) =>
      record(entry) && typeof entry.created === 'number' && entry.created > Date.now() - maxAge && entry.created <= Date.now()
      && validState(entry.state) && text(entry.href) && safeChatReturnURL(entry.href, location.origin))) as Record<string, ReturnEntry>
  } catch { return {} }
}

export function rememberChatReturn(state: DrawerReturnState): string {
  const href = safeChatReturnURL(location.pathname + location.search + location.hash, location.origin)
  if (!href || !validState(state)) return ''
  const token = crypto.randomUUID()
  try {
    const saved = entries()
    for (const [key, entry] of Object.entries(saved)) if (entry.href === href) delete saved[key]
    const bounded = Object.fromEntries(Object.entries(saved).slice(-15))
    bounded[token] = { href, created: Date.now(), state }
    sessionStorage.setItem(storageKey, JSON.stringify(bounded))
    return token
  } catch { return '' }
}

export function readDrawerReturn(): DrawerReturnState | undefined {
  if (typeof location === 'undefined') return undefined
  const href = location.pathname + location.search + location.hash
  return Object.values(entries(builderHandoffKey)).find(entry => entry.href === href)?.state
    ?? Object.values(entries()).find(entry => entry.href === href)?.state
}

export function clearDrawerReturn(): void {
  try {
    const href = location.pathname + location.search + location.hash
    const handoffs = entries(builderHandoffKey)
    const handoff = Object.entries(handoffs).find(([, entry]) => entry.href === href)
    if (handoff) {
      delete handoffs[handoff[0]]
      sessionStorage.setItem(builderHandoffKey, JSON.stringify(handoffs))
      return
    }
    const saved = entries()
    for (const [token, entry] of Object.entries(saved)) if (entry.href === href) delete saved[token]
    sessionStorage.setItem(storageKey, JSON.stringify(saved))
  } catch { /* Browser storage can be unavailable. */ }
}

export function fullChatHref(conversationId: string, search = typeof location === 'undefined' ? '' : location.search): string {
  const path = conversationId ? `/chats/${encodeURIComponent(conversationId)}` : '/chats/new'
  const token = new URLSearchParams(search).get('return')
  return token && /^[\w-]{1,100}$/.test(token) ? `${path}?return=${encodeURIComponent(token)}` : path
}

/** Read expansion composer state without consuming the origin's return state. */
export function readFullChatReturn(): DrawerReturnState | undefined {
  if (typeof location === 'undefined') return undefined
  const token = new URLSearchParams(location.search).get('return') ?? ''
  return entries()[token]?.state
}

/** Present the expansion draft once, while retaining the origin's snapshot. */
export function takeFullChatReturn(): DrawerReturnState | undefined {
  if (typeof location === 'undefined') return undefined
  try {
    const token = new URLSearchParams(location.search).get('return') ?? ''
    const saved = entries()
    const entry = saved[token]
    if (!entry || entry.fullChatPresented === true) return undefined
    entry.fullChatPresented = true
    sessionStorage.setItem(storageKey, JSON.stringify(saved))
    return entry.state
  } catch { return undefined }
}

export function chatReturnHref(): string | undefined {
  if (typeof location === 'undefined') return undefined
  const token = new URLSearchParams(location.search).get('return') ?? ''
  return entries()[token]?.href
}

export function returnFromFullChat(): void {
  location.assign(chatReturnHref() ?? '/chats')
}

/** A new full chat acquires its ID after the first accepted turn. */
export function updateChatReturnConversation(conversationId: string): void {
  if (!conversationId || typeof location === 'undefined') return
  try {
    const token = new URLSearchParams(location.search).get('return') ?? ''
    const saved = entries()
    if (!saved[token] || saved[token].state.conversationId) return
    saved[token].state.conversationId = conversationId
    sessionStorage.setItem(storageKey, JSON.stringify(saved))
  } catch { /* Browser storage can be unavailable. */ }
}

/** Stage local composer state; the builder authorizes and reloads the transcript. */
export function handoffBuilderConversation(targetHref: string, state: DrawerReturnState): string {
  if (!text(targetHref) || targetHref.startsWith('//') || targetHref.includes('\\')) throw new Error('Invalid dashboard builder destination.')
  const target = new URL(targetHref, location.origin)
  const match = /^\/dashboards\/([^/]+)\/edit$/.exec(target.pathname)
  let dashboardId = ''
  try { dashboardId = match ? decodeURIComponent(match[1]).trim() : '' } catch { /* Rejected below. */ }
  if (target.origin !== location.origin || !dashboardId || dashboardId.includes('/') || dashboardId.includes('\\')) {
    throw new Error('Invalid dashboard builder destination.')
  }
  if (!validState(state) || !state.conversationId.trim()) throw new Error('A conversation is required to open the dashboard builder.')
  const href = target.pathname + target.search + target.hash
  const saved = entries(builderHandoffKey)
  for (const [token, entry] of Object.entries(saved)) if (entry.href === href) delete saved[token]
  const bounded = Object.fromEntries(Object.entries(saved).slice(-15))
  bounded[crypto.randomUUID()] = {
    href, created: Date.now(),
    state: { ...state, selectedVisualId: '', selectedExplorerHref: '', selectedVisualTitle: '' },
  }
  try { sessionStorage.setItem(builderHandoffKey, JSON.stringify(bounded)) }
  catch { throw new Error('Could not preserve the conversation for the dashboard builder. Please try again.') }
  return href
}
