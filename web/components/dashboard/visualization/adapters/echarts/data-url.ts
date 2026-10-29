/**
 * Decode a data URL into a Blob without fetch().
 *
 * The product Content-Security-Policy allows connect-src 'self', so fetching a
 * data: URL is rejected even though the image already lives in the document.
 * Decoding the payload locally keeps snapshots working under that policy and
 * preserves the media type the renderer actually produced (PNG or SVG).
 */
export function dataURLToBlob(dataURL: string): Blob {
  if (!dataURL.startsWith('data:')) throw new Error('invalid data URL')
  const comma = dataURL.indexOf(',')
  if (comma < 0) throw new Error('invalid data URL')
  const header = dataURL.slice(5, comma)
  const payload = dataURL.slice(comma + 1)
  const [mediaType = 'text/plain', ...parameters] = header.split(';')
  const base64 = parameters.some((parameter) => parameter.trim().toLowerCase() === 'base64')
  const bytes = base64 ? decodeBase64(payload) : decodeURIComponent(payload)
  return new Blob([bytes], { type: mediaType || 'text/plain' })
}

function decodeBase64(payload: string): Uint8Array<ArrayBuffer> {
  const binary = atob(payload)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index)
  return bytes
}
