import { startPlayground } from '../server'
import { reviewPort } from './settings'

// One production playground build per review run. No watchers or reload events.
const server = await startPlayground(reviewPort, { watch: false })
console.log(`Playground browser review: ${server.url}`)

for (const signal of ['SIGINT', 'SIGTERM'] as const) {
  process.once(signal, () => { void server.stop(true) })
}
