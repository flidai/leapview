import type { SavedVisualLibrarySignal } from '../../generated/signals'
import { uuidv7 } from '../shared/command'

export const savedVisualDragType = 'application/x-leapview-saved-visual'
export type SavedVisualLibraryMessage =
  | { type: 'lv-saved-visual-library'; library: SavedVisualLibrarySignal }
  | { type: 'lv-add-saved-visual'; id: string; requestId?: string }
  | { type: 'lv-remove-dashboard-visual'; pageId: string; componentId: string }
  | { type: 'lv-refresh-saved-visuals' }

const pendingForms = new WeakMap<HTMLIFrameElement, () => void>()

export function submitVisualForm(action: string, target: string | HTMLIFrameElement | null, values: Record<string, string>): void {
  if (!target) throw new Error('The visual panel is not ready. Please try again.')
  if (new URL(action, window.location.href).origin !== window.location.origin) throw new Error('Visual actions must stay on this site.')
  const frame = typeof target === 'string' ? null : target
  const form = document.createElement('form')
  form.method = 'post'
  form.action = action
  form.target = typeof target === 'string' ? target : target.name
  form.hidden = true
  const fields = {
    ...values,
    idempotencyKey: values.idempotencyKey || uuidv7(),
    'gorilla.csrf.Token': document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')?.content ?? '',
  }
  for (const [name, value] of Object.entries(fields)) {
    const input = document.createElement('input')
    input.type = 'hidden'
    input.name = name
    input.value = value
    form.append(input)
  }
  const submit = () => {
    document.body.append(form)
    form.submit()
    form.remove()
  }
  if (frame) {
    pendingForms.get(frame)?.()
    const cleanup = () => {
      frame.removeEventListener('load', loaded, true)
      window.clearTimeout(timer)
      pendingForms.delete(frame)
    }
    const loaded = (event: Event) => {
      let response: Document | null
      try {
        if (frame.contentWindow?.location.href === 'about:blank') return
        response = frame.contentDocument
      } catch {
        cleanup()
        return
      }
      const token = response?.querySelector<HTMLMetaElement>('meta[name="csrf-retry-token"]')?.content
      cleanup()
      if (!token) return
      // Only the CSRF middleware emits this marker, before any mutation.
      // Retry once with the same idempotency key and keep normal error
      // handling for any second failure.
      event.stopImmediatePropagation()
      const meta = document.querySelector<HTMLMetaElement>('meta[name="csrf-token"]')
      if (meta) meta.content = token
      const input = form.elements.namedItem('gorilla.csrf.Token') as HTMLInputElement
      input.value = token
      submit()
    }
    const timer = window.setTimeout(cleanup, 45000)
    pendingForms.set(frame, cleanup)
    frame.addEventListener('load', loaded, true)
  }
  submit()
}
