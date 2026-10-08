// The source library ID lives in the authored component ID, so membership
// survives reload, Undo, and opening the same draft in another browser.
export function savedVisualComponentId(savedId: string, requestId: string): string {
  return `saved_${savedId.replaceAll('-', '')}_${requestId.replaceAll('-', '')}`
}

export function savedVisualSourceId(componentId: string): string | undefined {
  const match = /^saved_([a-f0-9]{32})_[a-f0-9]{32}$/.exec(componentId)
  const id = match?.[1]
  return id ? `${id.slice(0, 8)}-${id.slice(8, 12)}-${id.slice(12, 16)}-${id.slice(16, 20)}-${id.slice(20)}` : undefined
}
