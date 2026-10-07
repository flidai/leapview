import type { ChatTranscriptItemSignal } from '../../generated/signals'

const builderTools = new Set([
  'create_dashboard_draft',
  'fork_dashboard',
  'get_dashboard_draft',
  'read_dashboard_source',
  'edit_dashboard_source',
  'set_dashboard_visibility',
  'add_dashboard_page',
  'add_dashboard_visual',
  'assign_dashboard_field',
  'preview_dashboard_draft',
  'execute_dashboard_command',
])

// Use the recorded operation's identities, never whichever dashboard happens
// to be open now. These links also work when reopening a saved conversation.
export function dashboardActionLinks(item: ChatTranscriptItemSignal, conversationID = ''): Array<{ label: string; href: string }> {
  if (item.status !== 'complete' || item.error || !builderTools.has(item.name ?? '')) return []

  const result = parseRecord(item.resultJson)
  if (result.error) return []
  const inputEnvelope = parseRecord(item.inputJson)
  const input = item.argumentsJson
    ? parseRecord(item.argumentsJson)
    : typeof inputEnvelope.arguments === 'string'
      ? parseRecord(inputEnvelope.arguments)
      : record(inputEnvelope.arguments) ?? inputEnvelope
  const lifecycle = record(result.lifecycle)
  // Archived dashboards have no usable Builder surface.
  if (lifecycle?.status === 'archived' || input.archive) return []

  // Durable results can be TOON or bounded previews. Resolve the full stored
  // operation server-side after checking ownership of the conversation.
  if (conversationID && item.toolCallId) return [{
    label: 'Open in Builder',
    href: `/chats/${encodeURIComponent(conversationID)}/actions/${encodeURIComponent(item.toolCallId)}/open${item.runId ? `?run=${encodeURIComponent(item.runId)}` : ''}`,
  }]

  const revision = record(result.revision)
  const draft = record(lifecycle?.draft)
  const createsDashboard = item.name === 'create_dashboard_draft' || item.name === 'fork_dashboard'
  // Fork inputs identify the source, so only a returned identity can reopen
  // the newly created dashboard.
  const dashboardID = text(lifecycle?.id) || text(revision?.dashboardId) || text(result.dashboardId)
    || (createsDashboard ? '' : text(input.dashboardId))
  const draftID = text(draft?.id) || text(result.draftId) || (createsDashboard ? '' : text(input.draftId))
  if (!dashboardID) return []
  if (lifecycle && !draftID) return []

  const query = new URLSearchParams()
  if (draftID) query.set('draft', draftID)
  const page = text(input.pageId) || text(input.page) || resultPageID(revision, item.name === 'add_dashboard_page')
  if (page) query.set('page', page)
  const spec = record(record(revision?.document)?.spec)
  const selectedPage = Array.isArray(spec?.pages) ? spec.pages.map(record).find(candidate => text(candidate?.id) === page) : undefined
  const components = Array.isArray(selectedPage?.components) ? selectedPage.components.map(record) : []
  const visualID = text(input.visualId)
  const component = components.find(candidate => text(candidate?.id) === visualID)
    ?? components.find(candidate => text(candidate?.visual) === visualID)
  const visual = text(input.componentId) || text(component?.id) || visualID
    || (item.name === 'add_dashboard_visual' ? text(components.filter(candidate => text(candidate?.visual)).at(-1)?.id) : '')
  if (visual) query.set('visual', visual)
  const suffix = query.size ? `?${query}` : ''
  return [{ label: 'Open in Builder', href: `/dashboards/${encodeURIComponent(dashboardID)}/edit${suffix}` }]
}

function resultPageID(revision: Record<string, unknown> | undefined, addedPage: boolean): string {
  const spec = record(record(revision?.document)?.spec)
  return Array.isArray(spec?.pages) ? text(record(addedPage ? spec.pages.at(-1) : spec.pages[0])?.id) : ''
}

function parseRecord(raw: string | undefined): Record<string, unknown> {
  if (!raw) return {}
  try { return record(JSON.parse(raw)) ?? {} } catch { return {} }
}

function record(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}
