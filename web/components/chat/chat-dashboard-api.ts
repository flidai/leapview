import { postUIJSON } from '../shared/command'

export type ChatDashboardDestination = { id: string; title: string; createsCopy?: boolean; pages: Array<{ id: string; title: string }> }
export type ChatDashboardResult = { componentId?: string; dashboardId: string; title: string; href: string; pageId: string }
export type ChatDashboardChoice = { dashboardId: string; pageId: string } | { title: string }
export type ChatDashboardOptions = { dashboards: ChatDashboardDestination[]; canCreate: boolean }

function endpoint(conversationId: string, artifactId: string): string {
  return `/chats/${encodeURIComponent(conversationId)}/visuals/${encodeURIComponent(artifactId)}/dashboards`
}

export async function listChatDashboards(conversationId: string, artifactId: string, signal: AbortSignal): Promise<ChatDashboardOptions> {
  const response = await fetch(endpoint(conversationId, artifactId), { credentials: 'same-origin', signal })
  if (!response.ok) throw new Error('Could not load editable dashboards. Please try again.')
  return response.json()
}

export async function addChatVisualToDashboard(conversationId: string, artifactId: string, choice: ChatDashboardChoice, idempotencyKey: string): Promise<ChatDashboardResult> {
  const response = await postUIJSON(endpoint(conversationId, artifactId), 'addChatVisualToDashboard', choice, idempotencyKey)
  if (!response.ok) throw new Error(response.status === 409
    ? 'This dashboard changed while you were adding the visual. Reopen the picker and try again.'
    : 'Could not add this visual. Please try again.')
  return response.json()
}
