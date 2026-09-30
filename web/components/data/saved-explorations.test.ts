import { expect, test } from 'bun:test'
import { loadSavedExplorations } from './saved-explorations'

test('saved exploration list uses the authenticated same-origin read route and canonical internal links', async () => {
  let requestedURL = ''
  let requestInit: RequestInit | undefined
  const items = await loadSavedExplorations(async (input, init) => {
    requestedURL = String(input)
    requestInit = init
    return new Response(JSON.stringify({ items: [
      { id: 'explore_1', title: 'Orders by month', href: '/explore?saved=explore_1', updatedAt: '2026-09-27T10:30:00Z' },
      { id: 'explore_2', title: 'External link', href: 'https://example.test/phishing', updatedAt: '2026-09-27T10:30:00Z' },
      { id: '', title: 'Invalid item', href: '/explore?saved=', updatedAt: '2026-09-27T10:30:00Z' },
    ] }), { status: 200, headers: { 'content-type': 'application/json' } })
  })

  expect(requestedURL).toBe('/explore/saved')
  expect(requestInit).toMatchObject({ credentials: 'same-origin', headers: { Accept: 'application/json' } })
  expect(items).toEqual([
    { id: 'explore_1', title: 'Orders by month', href: '/explore?saved=explore_1', updatedAt: '2026-09-27T10:30:00Z' },
    { id: 'explore_2', title: 'External link', href: '/explore?saved=explore_2', updatedAt: '2026-09-27T10:30:00Z' },
  ])
})

test('saved exploration list reports failed and malformed responses', async () => {
  await expect(loadSavedExplorations(async () => new Response('', { status: 503 }))).rejects.toThrow('503')
  await expect(loadSavedExplorations(async () => new Response(JSON.stringify({ saved: [] }), { status: 200 }))).rejects.toThrow('invalid response')
})
