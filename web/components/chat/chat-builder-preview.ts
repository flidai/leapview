import { uuidv7 } from '../shared/command'
import { addChatVisualToDashboard, saveChatDashboardDraft, type ChatDashboardResult } from './chat-dashboard-api'

export type ChatBuilderPreviewSource = { conversationId: string; title: string } & (
  { artifactId: string; revision?: never } | { revision: string; artifactId?: never }
)

type Attempt = { identity: string; key: string; created: number }
const storageKey = 'leapview-chat-builder-previews-v1'
const maxAge = 24 * 60 * 60 * 1000
const attempts = new Map<string, Attempt>()
const completed = new Map<string, ChatDashboardResult>()
const pending = new Map<string, Promise<ChatDashboardResult>>()

function retryKey(identity: string): string {
  const remembered = attempts.get(identity)
  if (remembered) return remembered.key
  let stored: Attempt[] = []
  try {
    const raw = sessionStorage.getItem(storageKey) ?? '[]'
    const values: unknown = raw.length <= 100_000 ? JSON.parse(raw) : []
    if (Array.isArray(values)) stored = values.slice(-32).filter((entry): entry is Attempt =>
      typeof entry?.identity === 'string' && entry.identity.length <= 4096
      && typeof entry?.key === 'string' && /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(entry.key)
      && typeof entry?.created === 'number' && entry.created > Date.now() - maxAge && entry.created <= Date.now())
  } catch { /* Retry remains stable in memory when browser storage is unavailable. */ }
  const attempt = stored.find(entry => entry.identity === identity) ?? { identity, key: uuidv7(), created: Date.now() }
  attempts.set(identity, attempt)
  if (attempts.size > 32) attempts.delete(attempts.keys().next().value!)
  try { sessionStorage.setItem(storageKey, JSON.stringify([...stored.filter(entry => entry.identity !== identity).slice(-31), attempt])) } catch { /* Storage is optional. */ }
  return attempt.key
}

/** Called only by an explicit Preview action. Source definitions remain server-owned. */
export function previewChatBuilder(source: ChatBuilderPreviewSource): Promise<ChatDashboardResult> {
  const conversationId = source.conversationId.trim()
  const title = Array.from(source.title.trim()).slice(0, 255).join('')
  const artifactId = source.artifactId?.trim()
  const revision = source.revision?.trim()
  if (!conversationId || conversationId.length > 256 || !title || Boolean(artifactId) === Boolean(revision)
    || (artifactId?.length ?? 0) > 256 || (revision?.length ?? 0) > 256) {
    return Promise.reject(new Error('This preview is unavailable. Reload the chat and try again.'))
  }
  const identity = JSON.stringify({ conversationId, title, ...(artifactId ? { artifactId } : { revision }) })
  const result = completed.get(identity)
  if (result) return Promise.resolve(result)
  const existing = pending.get(identity)
  if (existing) return existing
  const key = retryKey(identity)
  const request = (artifactId
    ? addChatVisualToDashboard(conversationId, artifactId, { title }, key)
    : saveChatDashboardDraft(conversationId, revision!, title, key))
    .then(result => {
      const href = result.href
      if (typeof href !== 'string' || !/^\/dashboards\/[^/?#]+\/edit(?:\?[^#]*)?$/.test(href) || /[\\\x00-\x20]/.test(href)) {
        throw new Error('The dashboard builder address is unavailable. Please try again.')
      }
      completed.set(identity, result)
      if (completed.size > 32) completed.delete(completed.keys().next().value!)
      return result
    })
    .finally(() => { pending.delete(identity) })
  pending.set(identity, request)
  return request
}
