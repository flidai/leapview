/** Shared offline qualification matrix check; never imported by product runtime. */
type Issue = { type: string, detail: string }
const arms = ['A', 'B', 'C']
const issue = (type: string, detail: string): Issue => ({ type, detail })

export function qualificationIssues(report: any, corpus: any): Issue[] {
  const expected = new Map<string, { compiler: boolean, intent: boolean }>()
  for (const task of corpus.tasks || []) for (const candidate of arms) {
    expected.set([task.id, candidate, 'seed'].join('|'), { compiler: task.seedCompilerValid, intent: false })
    expected.set([task.id, candidate, 'oracle'].join('|'), { compiler: true, intent: true })
    for (const negative of task.negatives || []) expected.set([task.id, candidate, 'negative:' + negative.id].join('|'), { compiler: true, intent: false })
  }
  const records = report.records || [], seen = new Set<string>()
  if (report.kind !== 'deterministic-prototype-qualification' || report.qualified !== true || report.agentTrials !== 0 || expected.size !== 93 || records.length !== 93) return [issue('integrity_qualification', 'Expected complete 31-fixture × 3-candidate offline qualification')]
  for (const record of records) {
    const key = [record.task, record.candidate, record.fixture].join('|'), want = expected.get(key)
    if (!want || seen.has(key) || record.prototypeAccepted !== true || record.id !== record.task || record.parseAndSchema !== true || record.compiler !== want.compiler || record.intent !== want.intent) return [issue('integrity_qualification', 'Qualification identity or expected outcome mismatch: ' + key)]
    seen.add(key)
  }
  return seen.size === expected.size ? [] : [issue('integrity_qualification', 'Qualification matrix has missing fixture identities')]
}
