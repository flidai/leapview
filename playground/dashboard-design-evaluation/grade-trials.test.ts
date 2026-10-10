import { describe, test, expect } from 'bun:test'
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync, rmSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { stringify, parse } from 'yaml'
import { digest } from './controller'
import { encodeSourceFiles, lowerSourceFiles, candidateSchemas, candidateFragmentSchemas, type Candidate } from './prototypes'
import { gradeTrials, commandIssues, auditTranscript, rowPreservation, qualificationIssues, inventory } from './grade-trials'

const candidates: Candidate[] = ['A', 'B', 'C']
const seed = stringify({ apiVersion: 'leapview.dev/v1', kind: 'Dashboard', metadata: { id: 'dashboard:evaluation', name: 'evaluation', displayName: 'Evaluation' }, spec: {
  semanticModel: 'sales', layout: { columns: 12, rowHeight: 48, gap: 16, padding: 16 }, filters: [],
  visuals: [{ id: 'total-revenue', title: 'Revenue', type: 'kpi', query: { type: 'aggregate', dimensions: [], metrics: ['revenue'] }, presentation: { type: 'kpi', displayUnits: 'auto' } }],
  pages: [{ id: 'overview', title: 'Overview', components: [{ id: 'revenue-kpi', type: 'visual', visual: 'total-revenue', placement: { column: 1, row: 1, columnSpan: 3, rowSpan: 3 } }] }],
} })
const py = (v: any): string => Array.isArray(v) ? '[' + v.map(py).join(', ') + ']' : v && typeof v === 'object' ? '{' + Object.keys(v).sort().map(k => JSON.stringify(k) + ': ' + py(v[k])).join(', ') + '}' : JSON.stringify(v)
const put = (path: string, content: string | Buffer) => { mkdirSync(dirname(path), { recursive: true }); writeFileSync(path, content); return path }

function fixture(fragmentTask = false, explicitRowSpans = false) {
  const root = mkdtempSync('/tmp/dashboard-grade-fake-'), results = join(root, 'results'), frozen: Record<string, string> = {}
  const write = (path: string, content: string) => { put(path, content); frozen[path] = digest(content); return path }
  const fragmentDocument = parse(seed), fragmentVisuals = fragmentDocument.spec.visuals, fragmentPages = fragmentDocument.spec.pages
  fragmentDocument.spec.visuals = []; fragmentDocument.spec.pages = []
  fragmentDocument.spec.includes = { visuals: ['visuals.yaml'], pages: ['pages.yaml'] }
  const fragmentFiles = { 'dashboards/evaluation.yaml': stringify(fragmentDocument), 'dashboards/visuals.yaml': stringify({ visuals: fragmentVisuals }), 'dashboards/pages.yaml': stringify({ pages: fragmentPages }) }
  function encoded(files: Record<string, string>, arm: Candidate) {
    const result = encodeSourceFiles(files, arm)
    if (explicitRowSpans && arm === 'C') for (const [name, text] of Object.entries(result)) {
      const document = parse(text), content = document.kind === 'Dashboard' ? document.spec : document
      for (const page of content.pages || []) for (const row of page.rows) for (const item of row.items) item.rowSpan = row.height
      result[name] = stringify(document)
    }
    return result
  }
  const tasks = Array.from({ length: 10 }, (_, index) => ({ id: fragmentTask && index === 0 ? 'second-page-reuse' : 'task-' + index,
    prompt_file: write(join(root, 'inputs/prompts', index + '.md'), 'Only the fixture task.\n'),
    seed_files_by_arm: Object.fromEntries(candidates.map(arm => [arm, Object.entries(encoded(fragmentTask && index === 0 ? fragmentFiles : { 'dashboards/evaluation.yaml': seed }, arm)).map(([name, text]) => ({ source: write(join(root, 'inputs/seeds', arm, String(index), name), text), destination: 'project/' + name }))])),
    allowed_yaml_files: fragmentTask && index === 0 ? ['project/dashboards/pages.yaml'] : ['project/dashboards/evaluation.yaml'] }))
  const arms = candidates.map(id => ({ id, reference_files: [
    { source: write(join(root, 'inputs/reference', id, 'schema.json'), JSON.stringify(candidateSchemas[id])), destination: 'reference/schema.json' },
    { source: write(join(root, 'inputs/reference', id, 'fragments.json'), JSON.stringify(candidateFragmentSchemas[id])), destination: 'reference/fragments.schema.json' },
    { source: write(join(root, 'inputs/reference', id, 'AUTHORING.md'), 'Synthetic fixture reference\n'), destination: 'reference/AUTHORING.md' },
  ] }))
  const corpus = { tasks: tasks.map((task, index) => ({ id: task.id, seedCompilerValid: index !== 9,
    negatives: [{ id: 'wrong-intent' }, ...(index === 0 ? [{ id: 'extra-negative' }] : [])] })), frozenFiles: {} }
  const corpusPath = put(join(root, 'corpus/manifest.json'), JSON.stringify(corpus))
  const binary = put(join(root, 'fake-binary'), 'not an executable; canonical grader is injected\n')
  const records = corpus.tasks.flatMap(task => candidates.flatMap(candidate => [
    { task: task.id, candidate, fixture: 'seed', id: task.id, prototypeAccepted: true, parseAndSchema: true, compiler: task.seedCompilerValid, intent: false },
    { task: task.id, candidate, fixture: 'oracle', id: task.id, prototypeAccepted: true, parseAndSchema: true, compiler: true, intent: true },
    ...task.negatives.map(n => ({ task: task.id, candidate, fixture: 'negative:' + n.id, id: task.id, prototypeAccepted: true, parseAndSchema: true, compiler: true, intent: false })),
  ]))
  const qualification = { kind: 'deterministic-prototype-qualification', qualified: true, agentTrials: 0, records,
    binarySHA256: digest(readFileSync(binary)), corpusSHA256: digest(readFileSync(corpusPath)),
    implementationSHA256: Object.fromEntries(['controller.ts', 'prototypes.ts', 'prototypes-cli.ts'].map(path => [path, digest(readFileSync(join(import.meta.dir, path)))])) }
  const qualificationPath = write(join(root, 'qualification.json'), JSON.stringify(qualification))
  const manifest: any = { version: 1, experiment_id: 'fake', shuffle_seed: 93820261009, tasks, arms, frozen_files: frozen,
    trials: tasks.flatMap(task => candidates.flatMap(arm => [1, 2, 3].map(replicate => ({ id: `${task.id}-${arm}-${replicate}`, task_id: task.id, arm_id: arm, replicate })))),
    protocol: { qualified: true, qualification: qualificationPath, qualification_sha256: frozen[qualificationPath], frozenCorpusManifest: corpusPath,
      corpusSHA256: qualification.corpusSHA256, firstAttemptOnly: true, repairBudget: 0, wallSeconds: 240, toolCalls: 12,
      preservation: 'Retain preexisting local row IDs except move-resize permits regrouping.' } }
  const identity = { cli_path: '/fake/codex', config_sha256: 'x', model_settings: { model: 'configured-model', model_reasoning_effort: 'configured-effort' } }
  const manifestPath = join(root, 'experiment.json')
  function commitManifest() {
    const raw = JSON.stringify(manifest, null, 2) + '\n'
    put(manifestPath, raw); put(join(results, 'manifest.json'), raw)
    const normalized = structuredClone(manifest)
    for (const task of normalized.tasks) {
      task.prompt_sha256 = frozen[task.prompt_file]
      for (const list of Object.values(task.seed_files_by_arm) as any[]) for (const spec of list) spec.sha256 = frozen[spec.source]
    }
    for (const arm of normalized.arms) for (const spec of arm.reference_files) spec.sha256 = frozen[spec.source]
    put(join(results, 'experiment.json'), JSON.stringify({ manifest: normalized, manifest_sha256: digest(raw), identity }))
  }
  commitManifest()
  for (const trial of manifest.trials) {
    const base = join(results, trial.id), snapshot = join(base, 'frozen'), task = tasks.find(t => t.id === trial.task_id)!, arm = arms.find(a => a.id === trial.arm_id)!
    for (const spec of [...task.seed_files_by_arm[trial.arm_id as Candidate], ...arm.reference_files]) put(join(snapshot, spec.destination), readFileSync(spec.source))
    put(join(snapshot, 'TASK.md'), readFileSync(task.prompt_file))
    const before = inventory(snapshot), usage = { input_tokens: 50, cached_input_tokens: 20, output_tokens: 3, reasoning_output_tokens: 0 }
    put(join(base, 'input-inventory.json'), JSON.stringify(before))
    put(join(base, 'reservation.json'), JSON.stringify({ trial, identity }))
    put(join(base, 'events.jsonl'), [
      { type: 'thread.started', thread_id: 'thread-' + trial.id }, { type: 'turn.started' },
      { type: 'item.completed', item: { id: 'message', type: 'agent_message', text: 'DONE' } }, { type: 'turn.completed', usage },
    ].map(v => JSON.stringify(v)).join('\n') + '\n')
    put(join(base, 'last-message.txt'), 'DONE'); put(join(base, 'stderr.txt'), '')
    put(join(base, 'result.json'), JSON.stringify({ trial, identity, complete: true, failure: null, cli_exit_code: -15,
      first_terminal_freeze: true, controlled_terminal_shutdown: true, frozen_at: 2, freeze_completed_at: 2.01,
      elapsed_seconds: 1, tool_calls: 0, thread_id: 'thread-' + trial.id, usage,
      input_inventory_sha256: digest(py(before)), stdin_sha256: digest(readFileSync(task.prompt_file)),
      output_inventory: before, changed_files: [], unauthorized_changes: [],
      artifact_sha256: Object.fromEntries(['events.jsonl', 'stderr.txt', 'last-message.txt'].map(name => [name, digest(readFileSync(join(base, name)))])),
      argv: ['/fake/codex', 'exec', '--ephemeral', '--skip-git-repo-check', '--sandbox', 'workspace-write', '--json', '-C', join(root, 'work', trial.id), '-o', join(base, 'last-message.txt'), '-'] }))
  }
  const options = { manifest: manifestPath, resultsRoot: results, binary,
    canonicalGrade: (task: string) => ({ id: task, parseAndSchema: true, compiler: true, intent: true }) }
  function update(id: string, fields: Record<string, any> = {}) {
    const base = join(results, id), result = JSON.parse(readFileSync(join(base, 'result.json'), 'utf8'))
    const after = inventory(join(base, 'frozen')), before = JSON.parse(readFileSync(join(base, 'input-inventory.json'), 'utf8'))
    result.output_inventory = after
    result.changed_files = Object.keys(after).filter(path => after[path].sha256 !== before[path]?.sha256).sort()
    result.artifact_sha256 = Object.fromEntries(['events.jsonl', 'stderr.txt', 'last-message.txt'].map(name => [name, digest(readFileSync(join(base, name)))]))
    put(join(base, 'result.json'), JSON.stringify({ ...result, ...fields }))
  }
  return { root, results, manifest, qualification, corpus, identity, options, update, commitManifest,
    close: () => rmSync(root, { recursive: true, force: true }) }
}

describe('frozen grade controls', () => {
  test('exact 90 denominators and controlled terminal shutdown succeed only with valid evidence', () => {
    const f = fixture()
    try {
      const report = gradeTrials(f.options)
      expect(report.globalIssues).toEqual([])
      for (const arm of candidates) { expect(report.summary[arm].denominator).toBe(30); expect(report.summary[arm].successes).toBe(30) }
      expect(report.pairedTaskMatrix).toHaveLength(30)
      expect(report.records[0].prototypeAccepted).toBe(true)
      expect(report.queryExecutionScored).toBe(false)
      expect(report.summary.A.resources.inputTokens).toBe(1500)
    } finally { f.close() }
  })
  test('missing output never reduces arm denominator', () => {
    const f = fixture()
    try {
      rmSync(join(f.results, 'task-0-A-1'), { recursive: true })
      const report = gradeTrials(f.options)
      expect(report.records).toHaveLength(90)
      expect(report.summary.A).toMatchObject({ denominator: 30, successes: 29, failures: 1 })
      expect(report.summary.A.failureTypes.missing_output).toBe(1)
    } finally { f.close() }
  })
  test('snapshot, raw manifest and artifact tampering fail before canonical grading', () => {
    const f = fixture()
    try {
      put(join(f.results, 'task-0-A-1/frozen/project/dashboards/evaluation.yaml'), seed + '\n# tampered\n')
      put(join(f.results, 'task-0-B-1/last-message.txt'), 'replaced')
      const report = gradeTrials(f.options)
      expect(report.records.find((r: any) => r.id === 'task-0-A-1').failures.some((x: any) => x.type === 'integrity_output')).toBe(true)
      expect(report.records.find((r: any) => r.id === 'task-0-B-1').failures.some((x: any) => x.type === 'integrity_artifact')).toBe(true)
      put(join(f.results, 'manifest.json'), '{}')
      const broken = gradeTrials(f.options)
      expect(broken.summary.A.successes).toBe(0)
      expect(broken.globalIssues.some((x: any) => x.type === 'integrity_manifest')).toBe(true)
    } finally { f.close() }
  })
  test('unauthorized changes and extra source files fail even with recomputed snapshot hashes', () => {
    const f = fixture()
    try {
      put(join(f.results, 'task-0-A-1/frozen/reference/AUTHORING.md'), 'changed reference')
      put(join(f.results, 'task-0-B-1/frozen/project/dashboards/new.yaml'), 'pages: []\n')
      f.update('task-0-A-1'); f.update('task-0-B-1')
      const report = gradeTrials(f.options)
      for (const id of ['task-0-A-1', 'task-0-B-1']) expect(report.records.find((r: any) => r.id === id).failures.some((x: any) => x.type === 'preservation_unauthorized')).toBe(true)
    } finally { f.close() }
  })
  test('row-ID preservation is independent of canonical placement; move-resize exception is explicit', () => {
    const f = fixture()
    try {
      const path = join(f.results, 'task-0-C-1/frozen/project/dashboards/evaluation.yaml'), doc = parse(readFileSync(path, 'utf8'))
      doc.spec.pages[0].rows[0].id = 'new-id'
      put(path, stringify(doc)); f.update('task-0-C-1')
      const report = gradeTrials(f.options), record = report.records.find((r: any) => r.id === 'task-0-C-1')
      expect(record.prototypeAccepted).toBe(true)
      expect(record.preservation).toBe(false)
      expect(record.failures.some((x: any) => x.type === 'preservation_row_identity')).toBe(true)
      const before = encodeSourceFiles({ 'dashboards/evaluation.yaml': seed }, 'C')
      expect(rowPreservation(before, { 'dashboards/evaluation.yaml': stringify(doc) }, 'move-resize')).toEqual([])
    } finally { f.close() }
  })
  test('preexisting row height is preserved even when explicit rowSpan keeps current canonical placements unchanged', () => {
    const f = fixture(false, true)
    try {
      const path = join(f.results, 'task-0-C-1/frozen/project/dashboards/evaluation.yaml')
      const before = readFileSync(path, 'utf8'), document = parse(before)
      expect(document.spec.pages[0].rows[0].items[0].rowSpan).toBe(3)
      document.spec.pages[0].rows[0].height = 4
      const actual = stringify(document)
      expect(parse(lowerSourceFiles({ 'dashboards/evaluation.yaml': actual }, 'C')['dashboards/evaluation.yaml'])).toEqual(parse(lowerSourceFiles({ 'dashboards/evaluation.yaml': before }, 'C')['dashboards/evaluation.yaml']))
      put(path, actual); f.update('task-0-C-1')
      const record = gradeTrials(f.options).records.find((r: any) => r.id === 'task-0-C-1')
      expect(record.prototypeAccepted).toBe(true)
      expect(record.preservation).toBe(false)
      expect(record.success).toBe(false)
      expect(record.failures.some((x: any) => x.type === 'preservation_row_fields')).toBe(true)
      expect(rowPreservation({ 'dashboards/evaluation.yaml': before }, { 'dashboards/evaluation.yaml': actual }, 'move-resize')).toEqual([])
    } finally { f.close() }
  })
  test('fragment task requires only pages.yaml to change; semantically neutral neighbor rewrites fail', () => {
    const f = fixture(true)
    try {
      const pages = parse(seed).spec.pages
      pages.push({ ...structuredClone(pages[0]), id: 'detail', title: 'Detail' })
      const source = parse(seed)
      source.spec.visuals = []; source.spec.pages = []; source.spec.includes = { visuals: ['visuals.yaml'], pages: ['pages.yaml'] }
      const encoded = encodeSourceFiles({ 'dashboards/evaluation.yaml': stringify(source), 'dashboards/visuals.yaml': stringify({ visuals: parse(seed).spec.visuals }), 'dashboards/pages.yaml': stringify({ pages }) }, 'A')
      for (const rep of [1, 2]) {
        const id = 'second-page-reuse-A-' + rep
        put(join(f.results, id, 'frozen/project/dashboards/pages.yaml'), encoded['dashboards/pages.yaml'])
        f.update(id)
      }
      const neighbor = join(f.results, 'second-page-reuse-A-2/frozen/project/dashboards/visuals.yaml')
      put(neighbor, readFileSync(neighbor, 'utf8') + '\n# formatting-only rewrite\n'); f.update('second-page-reuse-A-2')
      const report = gradeTrials(f.options)
      expect(report.records.find((r: any) => r.id === 'second-page-reuse-A-1').success).toBe(true)
      const rejected = report.records.find((r: any) => r.id === 'second-page-reuse-A-2')
      expect(rejected.failures.some((x: any) => x.type === 'preservation_fragment_bytes')).toBe(true)
    } finally { f.close() }
  })
  test('stdin mismatch and unsupported negative exits cannot masquerade as controlled success', () => {
    const f = fixture()
    try {
      f.update('task-0-A-1', { stdin_sha256: '0'.repeat(64) })
      f.update('task-0-B-1', { cli_exit_code: -15, controlled_terminal_shutdown: false })
      const report = gradeTrials(f.options)
      expect(report.records.find((r: any) => r.id === 'task-0-A-1').failures.some((x: any) => x.type === 'integrity_stdin')).toBe(true)
      expect(report.records.find((r: any) => r.id === 'task-0-B-1').failures.some((x: any) => x.type === 'process_incomplete')).toBe(true)
    } finally { f.close() }
  })
  test('schema, compiler and intent failures remain separate from prototype acceptance', () => {
    const f = fixture()
    try {
      const report = gradeTrials({ ...f.options, canonicalGrade: task => ({ id: task, parseAndSchema: true, compiler: true, intent: false }) })
      expect(report.records[0]).toMatchObject({ prototypeAccepted: true, parseAndSchema: true, compiler: true, intent: false, success: false })
      expect(report.summary.A.failureTypes.intent).toBe(30)
    } finally { f.close() }
  })
  test('thread reuse and missing started turn are invalid first attempts', () => {
    const f = fixture()
    try {
      for (const [id, replacement] of [['task-0-A-1', 'shared'], ['task-0-B-1', 'shared']]) {
        const path = join(f.results, id, 'events.jsonl'), events = readFileSync(path, 'utf8').trim().split('\n').map(line => JSON.parse(line))
        events[0].thread_id = replacement
        put(path, events.map(e => JSON.stringify(e)).join('\n') + '\n'); f.update(id, { thread_id: replacement })
      }
      const report = gradeTrials(f.options)
      expect(report.summary.A.failureTypes.integrity_reused_context).toBe(1)
      expect(report.summary.B.failureTypes.integrity_reused_context).toBe(1)
      const text = [ { type: 'thread.started', thread_id: 'fresh' }, { type: 'item.completed', item: { type: 'agent_message', text: 'done' } }, { type: 'turn.completed', usage: {} } ].map(e => JSON.stringify(e)).join('\n')
      expect(auditTranscript(text, '/tmp/work', []).issues.some(x => x.type === 'transcript_incomplete')).toBe(true)
    } finally { f.close() }
  })
  test('qualification rejects duplicate fixtures and invalid repair-seed outcomes', () => {
    const f = fixture()
    try {
      expect(qualificationIssues(f.qualification, f.corpus)).toEqual([])
      const duplicate = structuredClone(f.qualification); duplicate.records[92] = duplicate.records[0]
      expect(qualificationIssues(duplicate, f.corpus)).not.toEqual([])
      const wrong = structuredClone(f.qualification); wrong.records.find(r => r.task === 'task-9' && r.fixture === 'seed')!.compiler = true
      expect(qualificationIssues(wrong, f.corpus)).not.toEqual([])
    } finally { f.close() }
  })
})

describe('command audit distinguishes scope from literals', () => {
  test('normal shell wrappers and project file walks are confined', () => {
    for (const command of ["/bin/bash -lc 'cat TASK.md'", "python3 - <<'PY'\nfrom pathlib import Path\nfor p in Path('project').rglob('*.yaml'): print(p.read_text())\ntarget=Path('project/dashboards/x.yaml'); target.parent.mkdir(exist_ok=True)\nPY", "rg --files project reference", "cat /tmp/work/project/dashboards/evaluation.yaml"])
      expect(commandIssues(command, '/tmp/work')).toEqual([])
  })
  test('actual slash-delimited sed address ranges are expressions, while outside input paths remain rejected', () => {
    for (const command of ["/bin/bash -lc \"sed -n '/\\\"PrototypeAggregate\\\": {/,/\\\"required\\\": \\\\[/p' reference/schema.json; cat project/dashboards/evaluation.yaml\"", "/bin/bash -lc \"cat project/dashboards/evaluation.yaml project/models/sales_orders.yaml project/semantic-models/sales.yaml; sed -n '/\\\"RecordsDashboardQuery\\\": {/,/\\\"RelativeDateDashboardFilterExpression\\\": {/p' reference/schema.json\""]) expect(commandIssues(command, '/tmp/work')).toEqual([])
    expect(commandIssues("sed -n '/foo/,/bar/p' /etc/passwd", '/tmp/work').some(f => f.type === 'contamination_outside_path')).toBe(true)
    expect(commandIssues("sed -n -f /etc/script reference/schema.json", '/tmp/work').some(f => f.type === 'contamination_outside_path')).toBe(true)
  })
  test('outside paths, parent reads, validators, delegation and external tools report concrete evidence', () => {
    for (const [command, type] of [["cat ../other/TASK.md", 'contamination_outside_path'], ["cat /tmp/grader-corpus/oracle/a.yaml", 'contamination_outside_path'], ['go test ./...', 'contamination_validator'], ['codex exec foo', 'contamination_delegation'], ['curl https://example.com', 'contamination_external'], ["python3 -c \"Path.cwd().parent.iterdir()\"", 'contamination_parent_read']]) {
      const findings = commandIssues(command, '/tmp/work')
      expect(findings.some(f => f.type === type && f.command === command)).toBe(true)
    }
    const mcp = [{ type: 'item.started', item: { id: 'external', type: 'mcp_tool_call', server: 'x', tool: 'y' } }].map(x => JSON.stringify(x)).join('\n')
    expect(auditTranscript(mcp, '/tmp/work', []).issues.some(f => f.type === 'contamination_external_tool')).toBe(true)
  })
})
