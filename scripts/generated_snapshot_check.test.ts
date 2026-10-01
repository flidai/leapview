import { expect, test } from 'bun:test'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parse } from 'yaml'

const helper = resolve('scripts/check_generated_snapshots.sh')
const tasks = parse(readFileSync('Taskfile.yml', 'utf8')).tasks

function runSnapshotCheck(root: string, ...generator: string[]) {
  return spawnSync('bash', [helper, 'generated', '--', ...generator], {
    cwd: root,
    encoding: 'utf8',
  })
}

test('generated checks compare against staged and untracked worktree snapshots', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-generated-snapshot-'))
  try {
    execFileSync('git', ['init', '-q'], { cwd: root })
    execFileSync('git', ['config', 'user.name', 'Generated Snapshot Test'], { cwd: root })
    execFileSync('git', ['config', 'user.email', 'generated-snapshot@example.invalid'], { cwd: root })
    mkdirSync(join(root, 'generated'), { recursive: true })
    writeFileSync(join(root, 'generated', 'tracked.json'), '{"version":1}\n')
    execFileSync('git', ['add', 'generated/tracked.json'], { cwd: root })
    execFileSync('git', ['commit', '-qm', 'fixture'], { cwd: root })

    writeFileSync(join(root, 'generated', 'tracked.json'), '{"version":2}\n')
    execFileSync('git', ['add', 'generated/tracked.json'], { cwd: root })
    writeFileSync(join(root, 'generated', 'untracked.json'), '{"intentional":true}\n')

    const result = runSnapshotCheck(root, 'bash', '-c', 'true')
    expect(result.status).toBe(0)
    expect(result.stderr).toBe('')
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('generated checks report files changed, removed, or added by the generator', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-generated-drift-'))
  try {
    mkdirSync(join(root, 'generated'), { recursive: true })
    writeFileSync(join(root, 'generated', 'existing.json'), '{"version":1}\n')
    writeFileSync(join(root, 'generated', 'removed.json'), '{}\n')

    const generator = [
      'bash',
      '-c',
      'printf \'{"version":2}\\n\' > generated/existing.json; rm generated/removed.json; printf \'{}\\n\' > generated/added.json',
    ]
    const result = runSnapshotCheck(root, ...generator)
    expect(result.status).toBe(1)
    expect(result.stdout + result.stderr).toContain('existing.json')
    expect(result.stdout + result.stderr).toContain('removed.json')
    expect(result.stdout + result.stderr).toContain('added.json')
    expect(result.stderr).toContain('generated snapshots changed during generation')
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('ci:pr checks snapshots before preparation and after validation lanes', () => {
  const commands = tasks['ci:pr'].cmds as Array<{ task?: string }>
  expect(commands[0]).toEqual({ task: 'generated:check' })
  expect(commands[1]).toEqual({ task: 'ci:prepare' })
  expect(commands.at(-1)).toEqual({ task: 'generated:check' })

  const generatedCheck = tasks['generated:check']
  expect(generatedCheck.deps).toBeUndefined()
  expect(generatedCheck.cmds[0]).toContain('check_generated_snapshots.sh')
  expect(generatedCheck.cmds[0]).toContain('-- task --force generate')

  const docsSiteCommands = tasks['ci:test:docs-site'].cmds as Array<string | { task?: string }>
  expect(docsSiteCommands[0]).toContain('check_generated_snapshots.sh')
  expect(docsSiteCommands[0]).toContain('-- task --force docs:generate')
  expect(docsSiteCommands.some((command) => typeof command === 'string' && command.includes('git status --porcelain')))
    .toBe(false)
})
