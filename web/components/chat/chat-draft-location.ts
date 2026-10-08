// The composer owns draft text; this service consumes the same-document
// Search handoff without replacing unrelated location or history state.
export function readChatPromptDraft(): string | null {
  return new URLSearchParams(window.location.hash.slice(1)).get('prompt')
}

export function consumeChatPromptDraft(): void {
  const url = new URL(window.location.href)
  const parameters = new URLSearchParams(url.hash.slice(1))
  if (!parameters.has('prompt')) return
  parameters.delete('prompt')
  url.hash = parameters.toString()
  window.history.replaceState(window.history.state, '', url)
}
