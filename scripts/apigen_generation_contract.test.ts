import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { parse } from 'yaml'

type Task = {
  cmds?: unknown[]
  deps?: unknown[]
}

const taskfile = parse(readFileSync('Taskfile.yml', 'utf8')) as {
  env: Record<string, unknown>
  tasks: Record<string, Task>
}

const typeSpecGenerationTasks = [
  'api:generate',
  'agent-contracts:generate',
  'data-resource-contracts:generate',
  'desktop-discovery:generate',
  'ui-signals:generate',
  'visualization-ir:generate',
  'dashboard-contracts:generate',
  'exploration-contracts:generate',
  'pipeline-contracts:generate',
]

test('Task-managed TypeSpec generation uses the prepared checkout-local emitter', () => {
  expect(taskfile.env.APIGEN_TYPESPEC_PACKAGE_DIR).toBe('{{.ROOT_DIR}}/pkg/apigen/typespec')

  for (const taskName of typeSpecGenerationTasks) {
    const task = taskfile.tasks[taskName]
    expect(task, `${taskName} must remain declared`).toBeDefined()
    expect(task.deps, `${taskName} must wait for the emitter build`).toContain('apigen:build')
    expect(task.cmds?.some((command) => typeof command === 'string' && command.includes('go -C pkg/apigen run ./cmd/apigen')))
      .toBe(true)
  }
})
