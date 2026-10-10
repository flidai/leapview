/** Offline grading of frozen first attempts. No query-data execution. */
import { readFileSync, readdirSync, lstatSync, existsSync, mkdirSync, writeFileSync, mkdtempSync, rmSync } from 'node:fs'
import { join, resolve, dirname, relative, isAbsolute, basename } from 'node:path'
import { isDeepStrictEqual } from 'node:util'
import { spawnSync } from 'node:child_process'
import { parse } from 'yaml'
import { sourceFiles, digest } from './controller'
import { qualificationIssues } from './qualification'
export { qualificationIssues } from './qualification'
import { lowerSourceFiles, candidateSchemas, candidateFragmentSchemas, type Candidate, type SourceFiles } from './prototypes'

type Issue = { type: string, detail: string, command?: string }
type Inventory = Record<string, { kind: string, bytes: number, sha256: string }>
export type GradeOptions = { manifest: string, resultsRoot: string, binary: string,
  canonicalGrade?: (task: string, root: string, corpus: string) => any }
const arms: Candidate[] = ['A', 'B', 'C']
const wallSeconds = 240, toolLimit = 12
const issue = (type: string, detail: string, command?: string): Issue => ({ type, detail, ...(command ? { command } : {}) })
const bytes = (value: string) => Buffer.byteLength(value, 'utf8')

function safeRelative(value: string): string {
  if (typeof value !== 'string' || !value || isAbsolute(value) || value.includes('\\') || value.split('/').some(p => !p || p === '.' || p === '..')) throw new Error('Unsafe relative path')
  return value
}
function safeRead(path: string): Buffer {
  for (let part = resolve(path); ; part = dirname(part)) {
    if (lstatSync(part).isSymbolicLink()) throw new Error('Symlink in artifact/input path')
    if (part === dirname(part)) break
  }
  if (!lstatSync(path).isFile()) throw new Error('Expected regular file')
  return readFileSync(path)
}
const json = (path: string) => JSON.parse(safeRead(path).toString('utf8'))

/** Match the runner's json.dumps(sort_keys=True), rather than its pretty file. */
function pythonJSON(value: any): string {
  if (Array.isArray(value)) return '[' + value.map(pythonJSON).join(', ') + ']'
  if (value && typeof value === 'object') return '{' + Object.keys(value).sort().map(k => pythonJSON(k) + ': ' + pythonJSON(value[k])).join(', ') + '}'
  return JSON.stringify(value).replace(/[\u007f-\uffff]/g, c => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'))
}
export function inventory(root: string): Inventory {
  const files: Inventory = {}
  function walk(path: string, prefix: string) {
    for (const entry of readdirSync(path, { withFileTypes: true })) {
      if (entry.isSymbolicLink()) throw new Error('Frozen output contains a symlink')
      const name = prefix + entry.name, target = join(path, entry.name)
      if (entry.isDirectory()) walk(target, name + '/')
      else {
        if (!entry.isFile()) throw new Error('Frozen output contains a nonregular file')
        const content = safeRead(target)
        files[name] = { kind: 'file', bytes: content.length, sha256: digest(content) }
      }
    }
  }
  safeReadDirectory(root)
  walk(root, '')
  return files
}
function safeReadDirectory(path: string) {
  if (!lstatSync(path).isDirectory() || lstatSync(path).isSymbolicLink()) throw new Error('Expected regular directory')
}

function inside(path: string, root: string): boolean {
  const rel = relative(resolve(root), resolve(path))
  return rel === '' || (!rel.startsWith('../') && rel !== '..' && !isAbsolute(rel))
}

/** Decode shell quoting for inspection only; this never executes or expands input. */
function shellTokens(script: string): { value: string, start: number, end: number }[] {
  const tokens: { value: string, start: number, end: number }[] = []
  let index = 0
  while (index < script.length) {
    if (/\s/.test(script[index])) { index++; continue }
    const start = index
    if (/[;|&]/.test(script[index])) {
      let value = script[index++]
      if (script[index] === value && value !== ';') value += script[index++]
      tokens.push({ value, start, end: index }); continue
    }
    let value = '', quote = ''
    while (index < script.length) {
      const char = script[index]
      if (!quote && (/\s/.test(char) || /[;|&]/.test(char))) break
      if ((char === "'" || char === '"') && (!quote || quote === char)) { quote = quote ? '' : char; index++; continue }
      if (char === '\\' && quote !== "'" && index + 1 < script.length && (!quote || /[$`"\\\n]/.test(script[index + 1]))) {
        value += script[index + 1]; index += 2; continue
      }
      value += char; index++
    }
    tokens.push({ value, start, end: index })
  }
  return tokens
}

function maskSedReadExpressions(command: string): string {
  const outer = shellTokens(command)
  const isShell = outer.length >= 3 && /^(?:\/(?:usr\/)?bin\/)?(?:bash|sh|dash|zsh)$/.test(outer[0].value) && /^-[a-z]*c[a-z]*$/.test(outer[1].value)
  const script = isShell ? outer[2].value : command
  const tokens = shellTokens(script), masked = script.split('')
  // Only address-range print scripts are masked. sed -f and actual input paths remain visible.
  const address = String.raw`(?:\/(?:\\.|[^\/])*\/|\d+|\$)`
  const printRange = new RegExp('^' + address + '(?:,' + address + ')?\\s*p$')
  for (let index = 0; index < tokens.length; index++) {
    if (tokens[index].value !== 'sed' || (index > 0 && ![';', '|', '||', '&&', '&'].includes(tokens[index - 1].value))) continue
    let cursor = index + 1
    while (cursor < tokens.length && /^-[nEr]+$/.test(tokens[cursor].value)) cursor++
    if (tokens[cursor]?.value === '-e') cursor++
    const expression = tokens[cursor]
    if (expression && printRange.test(expression.value)) {
      for (let position = expression.start; position < expression.end; position++) masked[position] = ' '
    }
  }
  // Preserve any positional shell arguments too, so outside paths cannot hide there.
  return masked.join('') + (isShell && outer.length > 3 ? ' ' + outer.slice(3).map(t => t.value).join(' ') : '')
}

/** Audit only executable tool items, never assistant prose or YAML label text. */
export function commandIssues(command: string, workspace: string): Issue[] {
  const problems: Issue[] = []
  const body = maskSedReadExpressions(command).replace(/^\/(?:usr\/)?bin\/(?:bash|sh|dash|zsh|python3?|node|bun|cat|rg|sed|ls|find|head|tail|awk)\b\s*/, '')
  if (/\b(?:leapview\s+(?:validate|plan)|task\s+(?:ci|generate|validate)|go\s+(?:test|run|build)|bun\s+(?:test|controller\.ts)|pytest|jsonschema|ajv\s+(?:validate|compile)|yamllint)\b|\.(?:validate|validate_schema)\s*\(/i.test(command)) problems.push(issue('contamination_validator', 'Compiler, validation or test execution before freeze', command))
  if (/\b(?:codex\s+(?:exec|resume|fork)|spawn_agent|followup_task|subagent|claude)\b|collaboration\./i.test(command)) problems.push(issue('contamination_delegation', 'Delegation or another model session', command))
  if (/\b(?:curl|wget|ssh|scp|git)\b|(?:requests|urllib\.request)\.(?:get|post|urlopen)|\bfetch\s*\(/i.test(body)) problems.push(issue('contamination_external', 'Network or repository tool execution', command))
  if (/\b(?:printenv|os\.environ|process\.env)\b|(?:^|[\s;])env(?:\s|$)|\/proc\//.test(command)) problems.push(issue('contamination_environment', 'Environment or process inspection', command))
  if (/(?:Path\s*\(\s*['"]\.['"]\s*\)|Path\.cwd\s*\(\s*\))\.(?:parent|parents)\b|os\.chdir\s*\(\s*['"]\.\./.test(command)) problems.push(issue('contamination_parent_read', 'Explicit traversal outside the current workspace', command))
  // Extract path-like literals/tokens; project/* and target.parent.mkdir stay legal.
  const candidates = [...body.matchAll(/(?:["'`]|\s|^)(\/[^\s"'`<>;,()]+|\.\.?(?:\/[^\s"'`<>;,()]+)?)(?=[\s"'`<>;,()]|$)/g)].map(m => m[1])
  for (const path of candidates) {
    if (['/dev/null', '/dev/stdin', '/dev/stdout', '/dev/stderr'].includes(path)) continue
    if (!inside(resolve(workspace, path), workspace)) problems.push(issue('contamination_outside_path', 'Concrete path outside assigned workspace: ' + path, command))
  }
  if (/(?:["'`]|\s|^)(?:[^\s"'`]*\/)?(?:oracles?|grader-corpus|other-trials?|\.git|\.codex)(?:\/|["'`]|\s|$)/i.test(command)) problems.push(issue('contamination_oracle_or_host', 'Explicit oracle, grader, repository or host state path', command))
  return problems
}

export function auditTranscript(text: string, workspace: string, allowedWrites: string[]) {
  const problems: Issue[] = [], tools = new Set<string>()
  let threadId: string | null = null, usage: any = null, terminal = false, turns = 0, threadStarts = 0, messages = 0, finalMessage: string | null = null
  const events: any[] = []
  for (const [index, line] of text.split('\n').entries()) {
    if (!line.trim()) continue
    let event: any
    try { event = JSON.parse(line) } catch { problems.push(issue('transcript_invalid', 'Invalid JSONL line ' + (index + 1))); continue }
    events.push(event)
    if (event.type === 'thread.started') {
      threadStarts++
      if (threadStarts > 1) problems.push(issue('transcript_multiple_threads', 'Multiple thread.started events'))
      threadId = event.thread_id
    }
    if (event.type === 'turn.started') turns++
    if (event.type === 'turn.failed' || event.type === 'error') problems.push(issue('transcript_error', 'CLI reported an error event'))
    if (terminal && event.type !== 'turn.completed') problems.push(issue('transcript_after_terminal', 'Activity after first turn.completed'))
    if (event.type === 'turn.completed') {
      if (terminal) problems.push(issue('transcript_multiple_turns', 'Multiple completed turns'))
      terminal = true; usage = event.usage || {}
    }
    const item = event.item
    if (!item) continue
    if (event.type === 'item.completed' && item.type === 'agent_message') { messages++; if (!terminal) finalMessage = item.text }
    if (!['item.started', 'item.completed'].includes(event.type) || ['agent_message', 'reasoning', 'plan', 'todo_list'].includes(item.type)) continue
    tools.add(item.id === undefined ? 'event:' + index : item.type + ':' + item.id)
    if (item.type === 'command_execution') {
      if (typeof item.command !== 'string') problems.push(issue('transcript_command_invalid', 'Command item lacks exact executable command text'))
      else if (event.type === 'item.started' || !events.some(e => e !== event && e.type === 'item.started' && e.item?.id === item.id)) problems.push(...commandIssues(item.command, workspace))
    } else if (item.type === 'file_change') {
      for (const change of item.changes || []) {
        const path = isAbsolute(change.path) ? relative(workspace, change.path) : change.path
        if (!allowedWrites.includes(path)) problems.push(issue('contamination_unauthorized_write', 'Tool changed an unassigned path: ' + change.path))
      }
    } else problems.push(issue('contamination_external_tool', 'Disallowed tool item: ' + item.type))
  }
  if (turns > 1) problems.push(issue('transcript_multiple_turns', 'Multiple started turns'))
  if (threadStarts !== 1 || turns !== 1 || typeof threadId !== 'string' || !threadId.trim() || !terminal || !messages) problems.push(issue('transcript_incomplete', 'Require exactly one thread/start/completed turn and assistant submission'))
  return { issues: problems, toolCalls: tools.size, threadId, usage, terminal, messages, finalMessage }
}

function inputBytes(manifest: any, trial: any): Record<string, Buffer> {
  const task = manifest.tasks.find((t: any) => t.id === trial.task_id)
  const arm = manifest.arms.find((a: any) => a.id === trial.arm_id)
  const result: Record<string, Buffer> = {}
  for (const spec of [...task.seed_files_by_arm[trial.arm_id], ...arm.reference_files]) {
    safeRelative(spec.destination)
    if (result[spec.destination]) throw new Error('Duplicate input destination')
    result[spec.destination] = safeRead(spec.source)
  }
  result['TASK.md'] = safeRead(task.prompt_file)
  return result
}
function asInventory(files: Record<string, Buffer>): Inventory {
  return Object.fromEntries(Object.entries(files).map(([path, content]) => [path, { kind: 'file', bytes: content.length, sha256: digest(content) }]))
}
function changedNames(before: Inventory, after: Inventory): string[] {
  return [...new Set([...Object.keys(before), ...Object.keys(after)])].filter(p => !isDeepStrictEqual(before[p], after[p])).sort()
}
function dashboardFiles(files: SourceFiles): SourceFiles {
  return Object.fromEntries(Object.entries(files).filter(([path]) => path.startsWith('dashboards/')))
}
function rowObjects(files: SourceFiles): Map<string, Map<string, unknown>> {
  const pages = new Map<string, Map<string, unknown>>()
  for (const text of Object.values(files)) {
    const document = parse(text), content = document?.kind === 'Dashboard' ? document.spec : document
    for (const page of content?.pages || []) {
      if (pages.has(page.id)) throw new Error('Duplicate page identity')
      pages.set(page.id, new Map((page.rows || []).map((row: any) => [row.id, row])))
    }
  }
  return pages
}
export function rowPreservation(seed: SourceFiles, actual: SourceFiles, taskId: string): Issue[] {
  if (taskId === 'move-resize') return []
  const before = rowObjects(seed), after = rowObjects(actual), problems: Issue[] = []
  for (const [page, rows] of before) for (const [id, row] of rows) {
    const actualRows = after.get(page)
    if (!actualRows?.has(id)) problems.push(issue('preservation_row_identity', 'Missing preexisting row ID ' + id + ' on page ' + page))
    else if (!isDeepStrictEqual(row, actualRows.get(id))) problems.push(issue('preservation_row_fields', 'Changed preexisting row fields for ' + id + ' on page ' + page))
  }
  return problems
}
function editedSpan(before: Buffer, after: Buffer): number {
  let start = 0, oldEnd = before.length, newEnd = after.length
  while (start < oldEnd && start < newEnd && before[start] === after[start]) start++
  while (oldEnd > start && newEnd > start && before[oldEnd - 1] === after[newEnd - 1]) { oldEnd--; newEnd-- }
  return (oldEnd - start) + (newEnd - start)
}

function normalizedManifest(manifest: any): any {
  const result = structuredClone(manifest)
  for (const task of result.tasks) {
    task.prompt_sha256 = result.frozen_files[task.prompt_file]
    for (const files of Object.values(task.seed_files_by_arm) as any[]) for (const file of files) file.sha256 = result.frozen_files[file.source]
  }
  for (const arm of result.arms) for (const file of arm.reference_files) file.sha256 = result.frozen_files[file.source]
  return result
}


function canonicalGrade(options: GradeOptions, task: string, root: string, corpus: string) {
  if (options.canonicalGrade) return options.canonicalGrade(task, root, corpus)
  const child = spawnSync(options.binary, ['-design-manifest', corpus, '-design-task', task, '-design-source', root], { timeout: 60000, maxBuffer: 10 * 1024 * 1024, encoding: 'utf8', shell: false })
  if (child.error || child.status !== 0) throw new Error(child.error?.message || child.stderr)
  return JSON.parse(child.stdout)
}

function gradeOne(manifest: any, experiment: any, trial: any, options: GradeOptions, globalIssues: Issue[]) {
  const base = join(options.resultsRoot, trial.id), freeze = join(base, 'frozen')
  const record: any = { ...trial, success: false, prototypeAccepted: false, prototypeAssessed: false, canonicalAssessed: false, parseAndSchema: false,
    compiler: false, intent: false, preservation: false, integrity: false, process: false,
    failures: [...globalIssues], sourceBytes: null, editedBytes: null, elapsedSeconds: null,
    toolCalls: null, usage: null, queryExecutionScored: false }
  let output: any
  try { output = json(join(base, 'result.json')) }
  catch { record.failures.push(issue('missing_output', 'Missing or unreadable frozen result')); return record }
  record.elapsedSeconds = output.elapsed_seconds ?? null
  record.toolCalls = output.tool_calls ?? 0
  record.usage = output.usage || {}
  if (!isDeepStrictEqual(output.trial, trial)) record.failures.push(issue('integrity_trial', 'Result trial identity differs from fixed manifest cell'))
  const controlledExit = output.controlled_terminal_shutdown === true && output.first_terminal_freeze === true && [-15, -9].includes(output.cli_exit_code)
  if (!output.complete || output.failure || (output.cli_exit_code !== 0 && !controlledExit) || existsSync(join(base, 'INCOMPLETE'))) record.failures.push(issue('process_incomplete', 'Incomplete process: ' + (output.failure || String(output.cli_exit_code))))
  if (output.first_terminal_freeze !== true || output.freeze_completed_at < output.frozen_at) record.failures.push(issue('process_freeze_boundary', 'Missing first-terminal source snapshot evidence'))
  record.controlledTerminalShutdown = output.controlled_terminal_shutdown === true
  record.cliExitCode = output.cli_exit_code
  if (output.attempted === false) { record.failures.push(issue('not_dispatched', 'Cell was not dispatched')); return record }
  if (!Number.isFinite(output.elapsed_seconds) || output.elapsed_seconds > wallSeconds || output.tool_calls > toolLimit) record.failures.push(issue('process_budget', 'Recorded time/tool budget exceeded or absent'))
  record.process = !record.failures.some((f: Issue) => f.type.startsWith('process_'))
  try {
    const inputs = inputBytes(manifest, trial), expectedInventory = asInventory(inputs)
    const before = json(join(base, 'input-inventory.json')), after = inventory(freeze)
    if (!isDeepStrictEqual(before, expectedInventory) || output.input_inventory_sha256 !== digest(pythonJSON(before))) record.failures.push(issue('integrity_inputs', 'Input inventory differs from exact frozen assigned inputs'))
    if (output.stdin_sha256 !== digest(inputs['TASK.md'])) record.failures.push(issue('integrity_stdin', 'Stdin prompt hash differs from frozen task'))
    if (!isDeepStrictEqual(after, output.output_inventory)) record.failures.push(issue('integrity_output', 'Frozen snapshot differs from recorded output inventory'))
    const changed = changedNames(before, after)
    if (!isDeepStrictEqual(changed, output.changed_files)) record.failures.push(issue('integrity_changes', 'Recorded changed file set differs from actual snapshots'))
    const task = manifest.tasks.find((t: any) => t.id === trial.task_id)
    const unauthorized = changed.filter(p => !task.allowed_yaml_files.includes(p))
    if (unauthorized.length || output.unauthorized_changes?.length) record.failures.push(issue('preservation_unauthorized', 'Unassigned files changed: ' + [...new Set([...unauthorized, ...(output.unauthorized_changes || [])])].join(', ')))
    if (!isDeepStrictEqual(Object.keys(after).sort(), Object.keys(expectedInventory).sort())) record.failures.push(issue('preservation_file_set', 'Frozen file set differs from exact expected source/input set'))
    for (const name of ['events.jsonl', 'stderr.txt', 'last-message.txt']) {
      if (!output.artifact_sha256?.[name] || digest(safeRead(join(base, name))) !== output.artifact_sha256[name]) record.failures.push(issue('integrity_artifact', 'Artifact hash mismatch: ' + name))
    }
    const reservation = json(join(base, 'reservation.json'))
    if (!isDeepStrictEqual(output.identity, experiment.identity) || !isDeepStrictEqual(reservation.identity, experiment.identity) || !isDeepStrictEqual(reservation.trial, trial)) record.failures.push(issue('integrity_identity', 'Runner reservation or CLI/config identity drift'))
    const argv = output.argv
    const workspace = Array.isArray(argv) ? argv[argv.indexOf('-C') + 1] : ''
    if (!isAbsolute(workspace) || basename(workspace) !== trial.id || !isDeepStrictEqual(argv, [experiment.identity.cli_path, 'exec', '--ephemeral', '--skip-git-repo-check', '--sandbox', 'workspace-write', '--json', '-C', workspace, '-o', join(base, 'last-message.txt'), '-'])) record.failures.push(issue('integrity_cli_command', 'CLI argv differs from preregistered fresh stdin-only invocation'))
    const transcript = auditTranscript(safeRead(join(base, 'events.jsonl')).toString('utf8'), workspace, task.allowed_yaml_files)
    record.failures.push(...transcript.issues)
    if (transcript.toolCalls !== output.tool_calls || transcript.toolCalls > toolLimit || transcript.threadId !== output.thread_id || !isDeepStrictEqual(transcript.usage, output.usage)) record.failures.push(issue('integrity_transcript_metadata', 'Transcript identity/usage/tool count differs from frozen result'))
    record.threadId = transcript.threadId
    record.toolCalls = transcript.toolCalls
    record.usage = transcript.usage
    if (safeRead(join(base, 'last-message.txt')).toString('utf8') !== transcript.finalMessage) record.failures.push(issue('integrity_final_message', 'Last message differs from final pre-terminal JSONL assistant message'))
    record.integrity = !record.failures.some((f: Issue) => f.type.startsWith('integrity_') || f.type.startsWith('transcript_'))
    const sources = sourceFiles(join(freeze, 'project'))
    const candidateFiles = dashboardFiles(sources)
    const seed = Object.fromEntries(Object.entries(inputs).filter(([path]) => path.startsWith('project/dashboards/')).map(([path, content]) => [path.slice('project/'.length), content.toString('utf8')]))
    record.sourceBytes = Object.values(candidateFiles).reduce((n, text) => n + bytes(text), 0)
    record.editedBytes = Object.keys(seed).reduce((n, path) => n + editedSpan(Buffer.from(seed[path]), Buffer.from(candidateFiles[path] || '')), 0)
    record.changedSourceFiles = changed.filter(path => path.startsWith('project/dashboards/'))
    if (trial.task_id === 'second-page-reuse') {
      if (!isDeepStrictEqual(record.changedSourceFiles, ['project/dashboards/pages.yaml'])) record.failures.push(issue('preservation_fragment_only', 'Second-page task must change only pages.yaml'))
      for (const [path, content] of Object.entries(inputs)) if (path !== 'project/dashboards/pages.yaml' && after[path]?.sha256 !== digest(content)) record.failures.push(issue('preservation_fragment_bytes', 'Untouched fragment/support/reference bytes changed: ' + path))
    }
    record.preservation = !record.failures.some((f: Issue) => f.type.startsWith('preservation_'))
    // No compiler/lowerer is invoked for contaminated, incomplete or unverified evidence.
    if (record.failures.length) return record
    let lowered: SourceFiles
    record.prototypeAssessed = true
    try { lowered = lowerSourceFiles(candidateFiles, trial.arm_id); record.prototypeAccepted = true }
    catch (error) { record.failures.push(issue('prototype_rejected', String(error))); return record }
    if (trial.arm_id === 'C') record.failures.push(...rowPreservation(seed, candidateFiles, trial.task_id))
    record.preservation = !record.failures.some((f: Issue) => f.type.startsWith('preservation_'))
    if (record.failures.length) return record
    const temporary = mkdtempSync('/tmp/dashboard-design-grade-')
    try {
      for (const [path, text] of Object.entries(lowered)) {
        safeRelative(path)
        mkdirSync(dirname(join(temporary, path)), { recursive: true })
        writeFileSync(join(temporary, path), text, { flag: 'wx' })
      }
      record.canonicalAssessed = true
      const canonical = canonicalGrade(options, trial.task_id, temporary, manifest.protocol.frozenCorpusManifest)
      record.canonical = canonical
      for (const key of ['parseAndSchema', 'compiler', 'intent']) record[key] = canonical[key] === true
      if (!record.parseAndSchema) record.failures.push(issue('canonical_schema', canonical.diagnostic || 'Canonical decoding/schema rejected'))
      else if (!record.compiler) record.failures.push(issue('compiler', canonical.diagnostic || 'Go project compiler rejected'))
      else if (!record.intent) record.failures.push(issue('intent', canonical.diagnostic || 'Authored behavior differs from frozen task oracle'))
    } catch (error) { record.failures.push(issue('grader_error', String(error))) }
    finally { rmSync(temporary, { recursive: true, force: true }) }
    record.success = record.failures.length === 0
  } catch (error) { record.failures.push(issue('integrity_unreadable', String(error))) }
  return record
}

export function gradeTrials(options: GradeOptions): any {
  options = { ...options, manifest: resolve(options.manifest), resultsRoot: resolve(options.resultsRoot), binary: resolve(options.binary) }
  const raw = safeRead(options.manifest), manifest = JSON.parse(raw.toString('utf8'))
  const globalIssues: Issue[] = []
  const preparationProvenance: any[] = []
  const trialCells = manifest.tasks.flatMap((task: any) => arms.flatMap(arm => [1, 2, 3].map(replicate => ({ id: `${task.id}-${arm}-${replicate}`, task_id: task.id, arm_id: arm, replicate }))))
  if (manifest.tasks.length !== 10 || trialCells.length !== 90 || new Set(trialCells.map((t: any) => t.id)).size !== 90 || !isDeepStrictEqual([...manifest.trials].sort((a: any, b: any) => a.id.localeCompare(b.id)), [...trialCells].sort((a: any, b: any) => a.id.localeCompare(b.id)))) throw new Error('Invalid preregistered 90-cell manifest')
  let experiment: any = { identity: {} }, qualification: any = {}, corpus: any = {}
  try {
    if (!safeRead(join(options.resultsRoot, 'manifest.json')).equals(raw)) globalIssues.push(issue('integrity_manifest', 'Raw manifest copy differs from preregistration'))
    experiment = json(join(options.resultsRoot, 'experiment.json'))
    if (experiment.manifest_sha256 !== digest(raw) || !isDeepStrictEqual(experiment.manifest, normalizedManifest(manifest))) globalIssues.push(issue('integrity_experiment', 'Output experiment hash or normalized manifest differs'))
    for (const [path, hash] of Object.entries(manifest.frozen_files)) if (digest(safeRead(path)) !== hash) globalIssues.push(issue('integrity_frozen_input', 'Frozen input changed: ' + path))
    qualification = json(manifest.protocol.qualification)
    if (manifest.protocol.qualified !== true || manifest.protocol.qualification_sha256 !== manifest.frozen_files[manifest.protocol.qualification] || qualification.qualified !== true || qualification.agentTrials !== 0 || qualification.records?.length !== 93 || qualification.records.some((r: any) => r.prototypeAccepted !== true)) globalIssues.push(issue('integrity_qualification', 'Qualification record does not establish 93 accepted offline prototypes'))
    if (digest(safeRead(options.binary)) !== qualification.binarySHA256) globalIssues.push(issue('integrity_binary', 'Go grader binary differs from qualification'))
    if (digest(safeRead(manifest.protocol.frozenCorpusManifest)) !== manifest.protocol.corpusSHA256 || manifest.protocol.corpusSHA256 !== qualification.corpusSHA256) globalIssues.push(issue('integrity_corpus', 'Frozen corpus manifest differs from qualification'))
    corpus = json(manifest.protocol.frozenCorpusManifest)
    globalIssues.push(...qualificationIssues(qualification, corpus))
    for (const [path, hash] of Object.entries(corpus.frozenFiles || {})) if (digest(safeRead(join(dirname(manifest.protocol.frozenCorpusManifest), safeRelative(path)))) !== hash) globalIssues.push(issue('integrity_corpus_input', 'Frozen corpus input changed: ' + path))
    if (manifest.protocol.wallSeconds !== wallSeconds || manifest.protocol.toolCalls !== toolLimit || manifest.protocol.firstAttemptOnly !== true || manifest.protocol.repairBudget !== 0 || !String(manifest.protocol.preservation).includes('preexisting local row IDs') || !String(manifest.protocol.preservation).includes('move-resize')) globalIssues.push(issue('integrity_protocol', 'Fixed attempt/budget/row-preservation policy is absent'))
    for (const path of ['controller.ts', 'prototypes.ts', 'prototypes-cli.ts']) {
      const qualified = qualification.implementationSHA256?.[path], current = digest(safeRead(join(import.meta.dir, path)))
      if (!qualified) globalIssues.push(issue('integrity_lowerer', 'Missing qualified implementation hash: ' + path))
      else if (path === 'controller.ts') preparationProvenance.push({ path, qualifiedSHA256: qualified, currentSHA256: current, mismatch: current !== qualified,
        disclosure: 'Root disclosed predispatch preparation-only corpus/prompt policy changes; raw preregistered manifest and exact frozen inputs govern, qualification remains unchanged.' })
      else if (current !== qualified) globalIssues.push(issue('integrity_lowerer', 'Qualified lowerer implementation changed: ' + path))
    }
    for (const [name, expected] of [
      ['controller-at-qualification.ts', qualification.implementationSHA256?.['controller.ts']],
      ['runner-at-dispatch.py', manifest.protocol.runnerSHA256],
    ] as const) {
      const archive = join(dirname(options.manifest), name)
      if (expected && (existsSync(archive) || name === 'runner-at-dispatch.py')) {
        const actual = digest(safeRead(archive))
        preparationProvenance.push({ archive, expectedSHA256: expected, actualSHA256: actual, matches: actual === expected })
        if (actual !== expected) globalIssues.push(issue('integrity_preparation_archive', 'Recovered qualification/dispatch archive hash mismatch: ' + name))
      }
    }
    const dispatchController = join(dirname(options.manifest), 'controller-at-dispatch.ts')
    if (existsSync(dispatchController)) preparationProvenance.push({ archive: dispatchController, SHA256: digest(safeRead(dispatchController)), role: 'disclosed controller preparation source at dispatch' })
    for (const arm of manifest.arms) for (const spec of arm.reference_files) {
      const expected = spec.destination === 'reference/schema.json' ? candidateSchemas[arm.id as Candidate]
        : spec.destination === 'reference/fragments.schema.json' ? candidateFragmentSchemas[arm.id as Candidate] : null
      if (expected && !isDeepStrictEqual(json(spec.source), expected)) globalIssues.push(issue('integrity_schema', 'Runtime prototype schema differs from frozen assigned schema: ' + arm.id + '/' + spec.destination))
    }
  } catch (error) { globalIssues.push(issue('integrity_global_unreadable', String(error))) }
  const records = trialCells.map((trial: any) => gradeOne(manifest, experiment, trial, options, globalIssues))
  const threadCounts = new Map<string, number>()
  for (const r of records) if (r.threadId) threadCounts.set(r.threadId, (threadCounts.get(r.threadId) || 0) + 1)
  for (const r of records) if (r.threadId && threadCounts.get(r.threadId)! > 1) { r.failures.push(issue('integrity_reused_context', 'Thread identity reused across trials')); r.success = false; r.integrity = false }
  const summary = Object.fromEntries(arms.map(arm => {
    const chosen = records.filter((r: any) => r.arm_id === arm), failureTypes: Record<string, number> = {}
    for (const r of chosen) for (const type of new Set(r.failures.map((f: Issue) => f.type)) as Set<string>) failureTypes[type] = (failureTypes[type] || 0) + 1
    const resources = { inputTokens: 0, cachedInputTokens: 0, outputTokens: 0, reasoningOutputTokens: 0, elapsedSeconds: 0, toolCalls: 0, sourceBytes: 0, editedBytes: 0 }
    for (const r of chosen) {
      resources.inputTokens += r.usage?.input_tokens || 0
      resources.cachedInputTokens += r.usage?.cached_input_tokens || 0
      resources.outputTokens += r.usage?.output_tokens || 0
      resources.reasoningOutputTokens += r.usage?.reasoning_output_tokens || 0
      for (const key of ['elapsedSeconds', 'toolCalls', 'sourceBytes', 'editedBytes'] as const) resources[key] += r[key] || 0
    }
    return [arm, { denominator: 30, successes: chosen.filter((r: any) => r.success).length, failures: chosen.filter((r: any) => !r.success).length, failureTypes, resources }]
  }))
  const pairedTaskMatrix = manifest.tasks.flatMap((task: any) => [1, 2, 3].map(replicate => ({ task: task.id, replicate,
    arms: Object.fromEntries(arms.map(arm => { const r = records.find((r: any) => r.task_id === task.id && r.replicate === replicate && r.arm_id === arm)!; return [arm, { success: r.success, failures: r.failures.map((f: Issue) => f.type) }] })) })))
  return { kind: 'frozen-dashboard-design-trial-grades', denominator: 90, manifestSHA256: digest(raw), binarySHA256: digest(safeRead(options.binary)),
    qualification: { qualified: qualification.qualified, agentTrials: qualification.agentTrials, records: qualification.records?.length },
    corpusSHA256: manifest.protocol.corpusSHA256, graderImplementationSHA256: digest(safeRead(join(import.meta.dir, 'grade-trials.ts'))), globalIssues, preparationProvenance, summary, pairedTaskMatrix, records,
    resourceMetric: 'editedBytes is replaced middle-span bytes after common prefix/suffix removal, not keystrokes',
    queryExecutionScored: false, userdataUsed: false, physicalBlindnessClaimed: false }
}

if (import.meta.main) {
  const args = process.argv.slice(2), values: Record<string, string> = {}
  for (let i = 0; i < args.length; i += 2) {
    if (!['--manifest', '--results-root', '--binary', '--output'].includes(args[i]) || !args[i + 1] || values[args[i]]) throw new Error('Usage: bun grade-trials.ts --manifest FILE --results-root DIR --binary FILE --output NEWFILE')
    values[args[i]] = args[i + 1]
  }
  if (Object.keys(values).length !== 4) throw new Error('All four grader arguments are required')
  const report = gradeTrials({ manifest: values['--manifest'], resultsRoot: values['--results-root'], binary: values['--binary'] })
  writeFileSync(resolve(values['--output']), JSON.stringify(report, null, 2) + '\n', { flag: 'wx' })
}
