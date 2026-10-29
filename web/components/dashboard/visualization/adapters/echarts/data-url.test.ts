import { expect, test } from 'bun:test'
import { dataURLToBlob } from './data-url'

test('decodes a base64 PNG snapshot without fetching it', async () => {
  const bytes = new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])
  const blob = dataURLToBlob(`data:image/png;base64,${btoa(String.fromCharCode(...bytes))}`)
  expect(blob.type).toBe('image/png')
  expect(new Uint8Array(await blob.arrayBuffer())).toEqual(bytes)
})

test('preserves the SVG media type the renderer produced', async () => {
  const svg = '<svg xmlns="http://www.w3.org/2000/svg"><text>Revenue</text></svg>'
  const encoded = dataURLToBlob(`data:image/svg+xml;base64,${btoa(svg)}`)
  expect(encoded.type).toBe('image/svg+xml')
  expect(await encoded.text()).toBe(svg)
  const plain = dataURLToBlob(`data:image/svg+xml,${encodeURIComponent(svg)}`)
  expect(plain.type).toBe('image/svg+xml')
  expect(await plain.text()).toBe(svg)
})

test('rejects values that are not data URLs', () => {
  expect(() => dataURLToBlob('blob:https://leapview.invalid/1234')).toThrow('invalid data URL')
  expect(() => dataURLToBlob('data:image/png;base64')).toThrow('invalid data URL')
})
