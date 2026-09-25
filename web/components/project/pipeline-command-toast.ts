import { showToast } from '../shared/toast'

export function showPipelineCommandToast(action: string): void {
  const message = action === 'cancel' ? 'Cancellation requested' : action === 'cancel-intent' ? 'Queued request cancelled' : action === 'retry' ? 'Retry queued' : 'Run queued'
  showToast({ message, tone: 'success' })
}
