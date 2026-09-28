import { headers as commandHeaders } from '../shared/command'

/** Saves only the query URL and title; the server derives ownership and validates the query. */
export async function saveChatVisual(title: string, explorerUrl: string): Promise<void> {
  const response = await fetch('/explore/saved', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { ...commandHeaders('saveExploration'), 'Content-Type': 'application/json' },
    body: JSON.stringify({ title, explorerUrl }),
  })
  if (!response.ok) throw new Error('Could not save this visual.')
}
