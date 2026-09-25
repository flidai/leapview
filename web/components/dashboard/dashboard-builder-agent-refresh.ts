type DashboardBuilderAgentTranscriptItem = {
  error?: string
  kind?: string
  name?: string
  runId?: string
  status?: string
}

type DashboardBuilderAgentSignal = {
  status?: { running?: boolean; runId?: string }
  transcript?: DashboardBuilderAgentTranscriptItem[]
}

const mutationTools = new Set([
  'edit_dashboard_source',
  'add_dashboard_page',
  'add_dashboard_visual',
  'assign_dashboard_field',
])

/** Tracks successful Builder writes until the corresponding agent run settles. */
export class DashboardBuilderAgentMutationTracker {
  private observed = false
  private runId = ''
  private mutationObserved = false

  observe(agent: DashboardBuilderAgentSignal): boolean {
    const running = Boolean(agent.status?.running)
    const runId = agent.status?.runId?.trim() ?? ''
    let refresh = false
    if (running) {
      if (this.observed && runId && this.runId && runId !== this.runId) {
        // A replacement run ID means the prior run settled between signal updates.
        refresh = this.mutationObserved
        this.mutationObserved = false
        this.runId = runId
      } else if (!this.observed) {
        this.runId = runId
        this.mutationObserved = false
      } else if (!this.runId && runId) {
        this.runId = runId
      }
      this.observed = true
      this.mutationObserved ||= this.hasSuccessfulMutation(agent.transcript, this.runId)
    } else if (this.observed) {
      // The final tool result can arrive in the same patch as the status change.
      this.mutationObserved ||= this.hasSuccessfulMutation(agent.transcript, this.runId)
      refresh = this.mutationObserved
      this.observed = false
      this.runId = ''
      this.mutationObserved = false
    }
    return refresh
  }

  private hasSuccessfulMutation(transcript: DashboardBuilderAgentTranscriptItem[] | undefined, runId: string): boolean {
    if (!runId || !Array.isArray(transcript)) return false
    return transcript.some(item =>
      item.runId?.trim() === runId &&
      item.kind === 'tool' &&
      item.status === 'complete' &&
      !item.error &&
      mutationTools.has(item.name ?? ''),
    )
  }
}
