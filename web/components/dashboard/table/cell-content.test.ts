import { expect, test } from 'bun:test'

// The URL helpers share a module with a registered Lit element. Import without
// a browser, then immediately restore the global registry before any test runs.
const registry = Object.getOwnPropertyDescriptor(globalThis, 'customElements')
if (!registry) Object.defineProperty(globalThis, 'customElements', {
  configurable: true, value: { get: () => undefined, define: () => {} },
})
const { safeCellURL, safeImageURL } = await (async () => {
  try { return await import('./cell-content') }
  finally {
    if (registry) Object.defineProperty(globalThis, 'customElements', registry)
    else Reflect.deleteProperty(globalThis, 'customElements')
  }
})()

const origin = 'https://analytics.example'

test('links accept web URLs and keep normalized relative paths on the original origin', () => {
  expect(safeCellURL('https://photos.example/image?q=1#credit', origin)).toBe('https://photos.example/image?q=1#credit')
  expect(safeCellURL('http://sources.example/credit', origin)).toBe('http://sources.example/credit')
  expect(safeCellURL('/images/a/../photo.png', origin)).toBe(`${origin}/images/photo.png`)
  expect(safeCellURL('/a/..//other.example/photo.png', origin)).toBe(`${origin}//other.example/photo.png`)
  expect(safeCellURL('/a/%2e%2e//other.example/photo.png', origin)).toBe(`${origin}//other.example/photo.png`)
  expect(new URL(safeCellURL('/a/..//other.example/photo.png', origin)!).origin).toBe(origin)
})

test('links and images reject executable URLs, implicit authorities, and ambiguous characters', () => {
  for (const value of [
    null, 42, {}, '', 'javascript:alert(1)', 'data:image/png;base64,x', 'blob:https://analytics.example/id',
    'file:///etc/passwd', 'mailto:user@example.com', 'https:photos.example/x', 'images/photo.png',
    '//photos.example/image.png', '///photos.example/image.png', '\\photos.example\\image.png',
    'https://photos.example\\@analytics.example/image.png', ' https://photos.example/image.png',
    'https://photos.example/a b', 'https://photos.example/\timage.png', 'https://photos.example/\nimage.png',
    'https://photos.example/\u0000image.png', 'https://photos.example/\u007fimage.png', 'https://photos.example/\u0085image.png',
  ]) {
    expect(safeCellURL(value, origin)).toBeUndefined()
    expect(safeImageURL(value, origin)).toBeUndefined()
  }
})

test('images allow HTTPS and same-origin HTTP while rejecting remote HTTP', () => {
  expect(safeImageURL('https://photos.example/image.png', origin)).toBe('https://photos.example/image.png')
  expect(safeImageURL('http://photos.example/image.png', origin)).toBeUndefined()
  expect(safeImageURL('/photo.png', 'http://localhost:8166')).toBe('http://localhost:8166/photo.png')
  expect(safeImageURL('http://localhost:8166/photo.png', 'http://localhost:8166')).toBe('http://localhost:8166/photo.png')
  expect(safeImageURL('http://localhost:8167/photo.png', 'http://localhost:8166')).toBeUndefined()
})
