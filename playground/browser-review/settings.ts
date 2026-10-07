import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

export const reviewDirectory = dirname(fileURLToPath(import.meta.url))
export const repositoryRoot = resolve(reviewDirectory, '../..')
export const reviewOutput = resolve(repositoryRoot, '.tmp/playground-browser-review')
export const reviewPort = Number(process.env.PLAYGROUND_REVIEW_PORT || 4410)

if (!Number.isInteger(reviewPort) || reviewPort < 1 || reviewPort > 65535) {
  throw new Error('PLAYGROUND_REVIEW_PORT must be an integer from 1 to 65535')
}

export const reviewBaseURL = `http://127.0.0.1:${reviewPort}`
