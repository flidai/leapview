import { readdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { parse } from 'yaml'

const componentRoot = 'web/components'
const frontendTaskRoots = ['ci:lane:frontend', 'ci:lane:frontend:shard']

export function auditFrontendTestRegistration({ taskfile, packageJson, componentTestFiles, taskRoots = frontendTaskRoots }) {
  const tasks = taskfile.tasks ?? {}
  const taskNames = new Set(Object.keys(tasks))
  const reachableInvocations = new Map()
  const taskCommands = []
  const missingRootTasks = taskRoots.filter((name) => !taskNames.has(name))

  function visitTask(name, suppliedVariables = {}) {
    const task = tasks[name]
    if (!task) return
    const variables = { ...suppliedVariables }
    const missingRequired = (task.requires?.vars ?? []).filter(({ name: key }) => variables[key] === undefined)
    if (missingRequired.length) {
      const [required, ...rest] = missingRequired
      if (!Array.isArray(required.enum) || required.enum.length === 0) return
      for (const value of required.enum) visitTask(name, { ...variables, [required.name]: value })
      return
    }

    const key = `${name}\u0000${JSON.stringify(Object.fromEntries(Object.entries(variables).sort(([a], [b]) => a.localeCompare(b))))}`
    if (reachableInvocations.has(key)) return
    reachableInvocations.set(key, { task: name, vars: variables })

    visitDependencies(task.deps, variables)
    inspectTaskValue(task.cmds, variables)
  }

  function visitDependencies(value, variables) {
    if (Array.isArray(value)) {
      for (const child of value) visitDependencies(child, variables)
    } else if (typeof value === 'string') {
      visitTask(value, variables)
    } else if (value && typeof value === 'object') {
      if (typeof value.task === 'string') {
        visitTask(value.task, mergeVariables(variables, value.vars))
      } else {
        for (const child of Object.values(value)) visitDependencies(child, variables)
      }
    }
  }

  function inspectTaskValue(value, variables) {
    if (Array.isArray(value)) {
      for (const child of value) inspectTaskValue(child, variables)
      return
    }
    if (value && typeof value === 'object') {
      if (typeof value.task === 'string') visitTask(value.task, mergeVariables(variables, value.vars))
      for (const [key, child] of Object.entries(value)) {
        if (key !== 'task' && key !== 'vars') inspectTaskValue(child, variables)
      }
      return
    }
    if (typeof value !== 'string') return
    const command = renderTaskTemplate(value, variables)
    taskCommands.push(command)
    for (const invocation of taskInvocations(command, taskNames, variables)) {
      visitTask(invocation.task, invocation.vars)
    }
  }

  for (const root of taskRoots) visitTask(root)

  const reachableScripts = new Map()
  const missingScripts = new Set()
  const scriptsToVisit = new Set()
  for (const command of taskCommands) {
    for (const name of bunRunTestScripts(command)) scriptsToVisit.add(name)
  }
  while (scriptsToVisit.size > 0) {
    const name = scriptsToVisit.values().next().value
    scriptsToVisit.delete(name)
    if (reachableScripts.has(name) || missingScripts.has(name)) continue
    const command = packageJson.scripts?.[name]
    if (typeof command !== 'string') {
      missingScripts.add(name)
      continue
    }
    reachableScripts.set(name, command)
    for (const nested of bunRunTestScripts(command)) {
      if (!reachableScripts.has(nested)) scriptsToVisit.add(nested)
    }
  }

  const reachablePatterns = [
    ...taskCommands.flatMap(testPathPatterns),
    ...[...reachableScripts.values()].flatMap(testPathPatterns),
  ]
  const packagePatterns = Object.values(packageJson.scripts ?? {}).flatMap(testPathPatterns)
  const reachable = (file) => reachablePatterns.some((pattern) => globMatches(pattern, file))
  const referencedByPackageScript = (file) => packagePatterns.some((pattern) => globMatches(pattern, file))
  const missing = componentTestFiles.filter((file) => !reachable(file) && !referencedByPackageScript(file))
  const outsideFrontendGraph = componentTestFiles.filter((file) => !reachable(file) && referencedByPackageScript(file))

  return {
    componentTestCount: componentTestFiles.length,
    reachableTasks: [...new Set([...reachableInvocations.values()].map(({ task }) => task))].sort(),
    reachableTaskInvocations: [...reachableInvocations.values()].sort((a, b) =>
      a.task.localeCompare(b.task) || JSON.stringify(a.vars).localeCompare(JSON.stringify(b.vars))),
    reachableScripts: [...reachableScripts.keys()].sort(),
    missing,
    outsideFrontendGraph,
    missingScripts: [...missingScripts].sort(),
    missingRootTasks,
    shardValues: [...new Set([...reachableInvocations.values()]
      .filter(({ task }) => task.endsWith(':shard'))
      .map(({ vars }) => vars.SHARD)
      .filter((value) => value !== undefined))].sort(),
  }
}

export function registrationFailures(result) {
  const failures = []
  if (result.missingRootTasks.length) failures.push(`missing Taskfile roots: ${result.missingRootTasks.join(', ')}`)
  if (result.missingScripts.length) failures.push(`frontend tasks reference missing package scripts: ${result.missingScripts.join(', ')}`)
  if (result.missing.length) failures.push(`component tests absent from package/Taskfile commands:\n  ${result.missing.join('\n  ')}`)
  if (result.outsideFrontendGraph.length) failures.push(`component tests registered outside the frontend CI task graph:\n  ${result.outsideFrontendGraph.join('\n  ')}`)
  return failures
}

export function taskCommandTargets(command, knownTaskNames, variables = {}) {
  return [...new Set(taskInvocations(command, knownTaskNames, variables).map(({ task }) => task))]
}

export function bunRunTestScripts(command) {
  const scripts = []
  for (const segment of shellCommandSegments(renderTaskTemplate(command, {}))) {
    const invocation = stripEnvironment(segment).match(/^bun\s+run\s+['"]?(test:[\w:.-]+)['"]?(?:\s|$)/)
    if (invocation) scripts.push(invocation[1])
  }
  return scripts
}

export function testPathPatterns(command) {
  const patterns = []
  for (const segment of shellCommandSegments(renderTaskTemplate(command, {}))) {
    const invocation = stripEnvironment(segment).match(/^bun\s+test\b([\s\S]*)$/)
    if (!invocation) continue
    patterns.push(...[...invocation[1].matchAll(/web\/components\/[A-Za-z0-9_./*?{}-]+\.test\.ts/g)].map((path) => path[0]))
  }
  return patterns
}

export function globMatches(pattern, value) {
  let expression = '^'
  for (let index = 0; index < pattern.length; index += 1) {
    const char = pattern[index]
    if (char === '*' && pattern[index + 1] === '*') {
      if (pattern[index + 2] === '/') {
        expression += '(?:.*/)?'
        index += 2
      } else {
        expression += '.*'
        index += 1
      }
    } else if (char === '*') {
      expression += '[^/]*'
    } else if (char === '?') {
      expression += '[^/]'
    } else {
      expression += char.replace(/[|\\{}()[\]^$+?.]/g, '\\$&')
    }
  }
  expression += '$'
  return new RegExp(expression).test(value)
}

function taskInvocations(command, knownTaskNames, variables) {
  const invocations = []
  for (const segment of shellCommandSegments(renderTaskTemplate(command, variables))) {
    const stripped = stripEnvironment(segment)
    const invocation = stripped.match(/^(?:task\b([\s\S]*)|node\s+scripts\/ci_watchdog\.mjs\b[\s\S]*?\s--\s+task\b([\s\S]*))$/)
    if (!invocation) continue
    const words = (invocation[1] ?? invocation[2]).trim().split(/\s+/).filter(Boolean).map((word) => word.replace(/^['"]|['"]$/g, ''))
    const assignments = Object.fromEntries(words.flatMap((word) => {
      const match = word.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.+)$/)
      return match ? [[match[1], match[2]]] : []
    }))
    for (const word of words) {
      if (knownTaskNames.has(word)) invocations.push({ task: word, vars: { ...variables, ...assignments } })
    }
  }
  return invocations
}

function shellCommandSegments(command) {
  const segments = []
  let current = ''
  let quote = ''
  let escaped = false
  let wordStart = true

  const finishSegment = () => {
    const segment = current.trim()
    if (segment) segments.push(segment)
    current = ''
    wordStart = true
  }

  for (let index = 0; index < command.length; index += 1) {
    const char = command[index]
    if (escaped) {
      current += char
      escaped = false
      if (char !== '\n') wordStart = false
      continue
    }
    if (char === '\\' && quote !== "'") {
      current += char
      escaped = true
      continue
    }
    if (quote) {
      current += char
      if (char === quote) quote = ''
      continue
    }
    if (char === '"' || char === "'") {
      current += char
      quote = char
      wordStart = false
      continue
    }
    if (char === '#' && wordStart) {
      const newline = command.indexOf('\n', index)
      if (newline < 0) break
      index = newline - 1
      continue
    }
    if (char === '\n' || char === ';') {
      finishSegment()
      continue
    }
    if ((char === '&' || char === '|') && command[index + 1] === char) {
      finishSegment()
      index += 1
      continue
    }
    current += char
    wordStart = /\s/.test(char)
  }

  finishSegment()
  return segments
}

function stripEnvironment(segment) {
  return segment.replace(/^(?:[A-Za-z_][A-Za-z0-9_]*=(?:"[^"]*"|'[^']*'|[^\s]+)\s+)+/, '').trim()
}

function mergeVariables(parent, child = {}) {
  const resolved = {}
  for (const [key, value] of Object.entries(child ?? {})) {
    resolved[key] = typeof value === 'string' ? renderTaskTemplate(value, parent) : value
  }
  return { ...parent, ...resolved }
}

function renderTaskTemplate(command, variables) {
  const conditional = command.replace(/\{\{if\s+eq\s+\.([A-Za-z_][A-Za-z0-9_]*)\s+["']([^"']+)["']\}\}([\s\S]*?)\{\{else\}\}([\s\S]*?)\{\{end\}\}/g,
    (_match, name, expected, yes, no) => String(variables[name]) === expected ? yes : no)
  return conditional
    .replace(/\{\{\s*\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}/g, (_match, name) => String(variables[name] ?? ''))
    .replace(/\{\{(?:if|else|end)[^}]*\}\}/g, '')
}

async function listComponentTests(directory, prefix = '') {
  const entries = await readdir(directory, { withFileTypes: true })
  const files = []
  for (const entry of entries) {
    const path = join(directory, entry.name)
    const relative = prefix ? `${prefix}/${entry.name}` : `${componentRoot}/${entry.name}`
    if (entry.isDirectory()) {
      files.push(...await listComponentTests(path, relative))
    } else if (entry.isFile() && entry.name.endsWith('.test.ts')) {
      files.push(relative)
    }
  }
  return files
}

async function main() {
  const [taskfileText, packageText, componentTestFiles] = await Promise.all([
    readFile('Taskfile.yml', 'utf8'),
    readFile('package.json', 'utf8'),
    listComponentTests(componentRoot),
  ])
  const result = auditFrontendTestRegistration({
    taskfile: parse(taskfileText),
    packageJson: JSON.parse(packageText),
    componentTestFiles: componentTestFiles.sort(),
  })
  const failures = registrationFailures(result)
  if (failures.length) {
    console.error(failures.join('\n'))
    process.exitCode = 1
    return
  }
  console.log(`Frontend test registration covers all ${result.componentTestCount} web component test files across shards: ${result.shardValues.join(', ')}.`)
}

if (import.meta.main) await main()
