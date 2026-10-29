/** Save a generated Blob through a temporary object URL. */
export function downloadBlob(blob: Blob, filename: string): void {
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = filename
  link.rel = 'noopener'
  document.body.append(link)
  link.click()
  link.remove()
  // Revoke after the browser has claimed the download; revoking synchronously
  // can cancel it in Safari and Firefox.
  window.setTimeout(() => URL.revokeObjectURL(url), 0)
}

/** Lowercase, hyphenated file name stem with a stable fallback. */
export function fileSlug(value: string, fallback = 'visual'): string {
  const normalized = value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
  return normalized || fallback
}

/** Image file extension for a Blob media type, or null when it is not an image. */
export function imageExtension(type: string): string | null {
  switch (type.split(';')[0].trim().toLowerCase()) {
    case 'image/png': return 'png'
    case 'image/jpeg': return 'jpg'
    case 'image/webp': return 'webp'
    case 'image/svg+xml': return 'svg'
    default: return null
  }
}
