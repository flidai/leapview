import type { ChatDashboardMessage, SavedVisualImportMessage } from './dashboard-preview-contract'

// A tiny native-form response: no chart runtime, builder, or additional stream.
const receipt = document.getElementById('chat-dashboard-receipt')?.dataset.receipt
if (receipt && window.parent !== window) {
  const message = JSON.parse(receipt) as ChatDashboardMessage | SavedVisualImportMessage
  if (message.type === 'lv-dashboard-mutation' || message.type === 'lv-builder-imported') window.parent.postMessage(message, window.location.origin)
}
