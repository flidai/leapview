import { readFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'
import { indexGoEvents } from './go_receipt_index.mjs'

// Compare executions, including skip reasons; cached package success is not execution.
export function ciExecutionRows(text) {
  const index = indexGoEvents(text)
  if (index.errors.length || index.builds.some(row => row.outcome === 'failed') ||
      index.packages.length !== 1 || index.packages[0].outcome !== 'passed' ||
      index.packages[0].cached || !index.tests.length ||
      index.tests.some(row => !row.fresh || !['passed', 'skipped'].includes(row.outcome))) {
    throw new Error('incomplete, cached, failed or malformed test execution')
  }
  const output = new Map()
  for (const line of text.split('\n').filter(line => line.trim())) {
    const event = JSON.parse(line)
    if (event.Test && event.Action === 'output') {
      const lines = (event.Output ?? '').split('\n').filter(line => line.trim() &&
        !/^\s*(?:===|---) (?:RUN|PAUSE|CONT|PASS|SKIP)\b/.test(line))
      output.set(event.Test, [...(output.get(event.Test) ?? []), ...lines])
    }
  }
  return index.tests.map(row => ({ test: row.test, outcome: row.outcome,
    skipOutput: row.outcome === 'skipped' ? (output.get(row.test) ?? []) : [] }))
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (process.argv.length !== 3) throw new Error('usage: performance_ci_events.mjs JSONL')
  console.log(JSON.stringify(ciExecutionRows(readFileSync(process.argv[2], 'utf8'))))
}
