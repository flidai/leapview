import { dataExplorerReturnContextFromSearch } from './data-explorer-url'

export type ExploreReturnLink = {
  href: string
  label: string
}

/**
 * Reads the closed return context emitted by an authored handoff. It accepts
 * only chat, dashboard, or model route IDs in the same route grammar as the server;
 * raw return URLs and foreign origins are never followed.
 */
export function exploreReturnLink(search = typeof window === 'undefined' ? '' : window.location.search): ExploreReturnLink | undefined {
  const context = dataExplorerReturnContextFromSearch(search)
  if (!context) return undefined
  if (context.surface === 'chat') {
    return { href: `/chats/${encodeURIComponent(context.conversationId)}`, label: 'Back to chat' }
  }
  if (context.surface === 'model') {
    return {
      href: `/models/${encodeURIComponent(context.asset)}/${encodeURIComponent(context.section)}`,
      label: 'Back to model',
    }
  }
  return {
    href: `/dashboards/${encodeURIComponent(context.dashboardId)}/pages/${encodeURIComponent(context.pageId)}`,
    label: 'Back to dashboard',
  }
}
