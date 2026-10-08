export type ChatFileAttachment = { id: string; name: string; text: string; size: number }
export const maxAttachmentCharacters = 60000
export const maxAttachmentCount = 5
const marker = '\n\n<attached_files>\n'
const ending = '\n</attached_files>'

// Extracted text follows the existing user-message persistence and agent path.
// JSON keeps file names and contents separate from the user's prompt.
export function attachedMessage(input: string, files: ChatFileAttachment[]): string {
  if (!files.length) return input
  const contents = files.map(({ name, text, size }) => ({ name, text, size }))
  return `${input || 'Please review the attached files.'}${marker}${JSON.stringify(contents)}${ending}`
}

export function readAttachedMessage(input: string): { text: string; files: ChatFileAttachment[] } {
  const index = input.lastIndexOf(marker)
  if (index < 0 || !input.endsWith(ending)) return { text: input, files: [] }
  try {
    const value = JSON.parse(input.slice(index + marker.length, -ending.length))
    if (!Array.isArray(value) || value.length > maxAttachmentCount || !value.every(file =>
      file && typeof file.name === 'string' && typeof file.text === 'string' && typeof file.size === 'number',
    )) return { text: input, files: [] }
    return { text: input.slice(0, index), files: value.map(file => ({ ...file, id: crypto.randomUUID() })) }
  } catch {
    return { text: input, files: [] }
  }
}

export async function readChatFile(file: File): Promise<ChatFileAttachment> {
  if (file.size > 10 * 1024 * 1024) throw new Error(`${file.name}: choose a file smaller than 10 MB.`)
  const extension = file.name.split('.').pop()?.toLowerCase()
  let text = ''
  if (extension === 'pdf') {
    const pdfjs = await import('pdfjs-dist')
    pdfjs.GlobalWorkerOptions.workerSrc = '/static/pdf-worker.js'
    const task = pdfjs.getDocument({ data: new Uint8Array(await file.arrayBuffer()), useSystemFonts: true })
    try {
      const pdf = await task.promise
      if (pdf.numPages > 100) throw new Error('Choose a PDF with no more than 100 pages.')
      for (let number = 1; number <= pdf.numPages; number++) {
        const page = await pdf.getPage(number)
        const content = await page.getTextContent()
        const pageText = content.items.map(item => 'str' in item ? item.str + (item.hasEOL ? '\n' : ' ') : '').join('').trim()
        if (pageText) text += `Page ${number}\n${pageText}\n\n`
        if (text.length > maxAttachmentCharacters) throw new Error('This file has too much text. Attach a shorter excerpt.')
        page.cleanup()
      }
      if (!text.trim()) throw new Error('This PDF has no readable text. Attach a text-based PDF or a text file.')
    } catch (error) {
      if (error instanceof Error && error.name === 'PasswordException') {
        throw new Error(`${file.name}: password-protected PDFs are not supported.`)
      }
      throw error
    } finally {
      await task.destroy()
    }
  } else if (['txt', 'md', 'csv', 'json', 'log'].includes(extension ?? '')) {
    try {
      text = new TextDecoder('utf-8', { fatal: true }).decode(await file.arrayBuffer())
    } catch {
      throw new Error(`${file.name}: save this file as UTF-8 text first.`)
    }
    if (text.includes('\0')) throw new Error(`${file.name}: this does not appear to be a text file.`)
  } else {
    throw new Error(`${file.name}: supported files are PDF, TXT, MD, CSV, JSON and LOG.`)
  }
  if (!text.trim()) throw new Error(`${file.name} is empty.`)
  if (text.length > maxAttachmentCharacters) {
    throw new Error(`${file.name}: attach a shorter excerpt (up to ${maxAttachmentCharacters.toLocaleString()} characters).`)
  }
  return { id: crypto.randomUUID(), name: file.name, text: text.trim(), size: file.size }
}
