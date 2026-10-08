import type { ChatTranscriptItemSignal } from '../../generated/signals'
import { dashboardActionIdentity, dashboardActionLinks } from './dashboard-action-links'

// A completed full-dashboard edit or creation in this live run can open its
// validated preview. Merely reopening history or inspecting a draft cannot.
export function generatedDashboardHref(transcript: ChatTranscriptItemSignal[], runId: string, conversationId: string): string {
  if (!runId || !conversationId) return ''
  const tools = transcript.filter(item => item.kind === 'tool' && item.runId === runId)
  const created = [...tools].reverse().find(item => ['create_dashboard_draft', 'fork_dashboard'].includes(item.name ?? '') && item.status === 'complete' && !item.error && item.toolCallId)
  const edited = [...tools].reverse().find(item => item.name === 'edit_dashboard_source' && item.status === 'complete' && !item.error && item.toolCallId)
  const authored = created ?? edited
  if (!authored) return ''
  const dashboardID = dashboardActionIdentity(authored)
  if (!created && !dashboardID) return ''
  // A preceding preview may belong to a different dashboard inspected in the
  // same run. Require a preview after the latest edit to that dashboard.
  const lastEdit = [...tools].reverse().find(item => ['edit_dashboard_source', 'add_dashboard_page', 'add_dashboard_visual', 'assign_dashboard_field'].includes(item.name ?? '')
    && (!dashboardID || dashboardActionIdentity(item) === dashboardID))
  const after = Math.max(tools.indexOf(authored), lastEdit ? tools.indexOf(lastEdit) : -1)
  const preview = tools.slice(after + 1).reverse().find(item => item.name === 'preview_dashboard_draft'
    && (created ? (!dashboardID || !dashboardActionIdentity(item) || dashboardActionIdentity(item) === dashboardID) : dashboardActionIdentity(item) === dashboardID))
  if (!preview) return ''
  try {
    const result = JSON.parse(preview.resultJson || '{}')
    if (result.visualErrors && Object.keys(result.visualErrors).length) return ''
  } catch { /* Retained non-JSON results are resolved by the authorized open route. */ }
  const href = dashboardActionLinks(preview, conversationId)[0]?.href
  if (!href) return ''
  const url = new URL(href, 'http://local.invalid')
  url.searchParams.set('embed', 'chat')
  // Retained receipts may omit chart errors from their browser summary.
  // Automatic navigation must validate the full receipt, including recovery.
  url.searchParams.set('mode', 'preview')
  // The retained route verifies that this preview belongs to this creation.
  if (created) url.searchParams.set('createdBy', created.toolCallId!)
  return url.pathname + url.search
}
