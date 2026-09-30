import { postUIJSON } from '../shared/command'

/** Saves only the query URL and title; the server derives ownership and validates the query. */
export async function saveChatVisual(title: string, explorerUrl: string): Promise<void> {
  const response = await postUIJSON('/explore/saved', 'saveExploration', { title, explorerUrl })
  if (!response.ok) throw new Error('Could not save this visual.')
}
