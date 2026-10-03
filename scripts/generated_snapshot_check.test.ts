import { expect, test } from 'bun:test'
import { execFileSync, spawnSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { parse } from 'yaml'

const helper = resolve('scripts/check_generated_snapshots.sh')
const tasks = parse(readFileSync('Taskfile.yml', 'utf8')).tasks

function runSnapshotCheck(root: string, ...generator: string[]) {
  return runSnapshotCheckPaths(root, ['generated'], ...generator)
}

function runSnapshotCheckPaths(root: string, paths: string[], ...generator: string[]) {
  return spawnSync('bash', [helper, ...paths, '--', ...generator], {
    cwd: root,
    encoding: 'utf8',
  })
}

function initGitRepo(root: string) {
  execFileSync('git', ['init', '-q'], { cwd: root })
  execFileSync('git', ['config', 'user.name', 'Generated Snapshot Test'], { cwd: root })
  execFileSync('git', ['config', 'user.email', 'generated-snapshot@example.invalid'], { cwd: root })
}

function commitFixture(root: string) {
  execFileSync('git', ['add', '-A'], { cwd: root })
  execFileSync('git', ['commit', '-qm', 'fixture'], { cwd: root })
}

test('generated checks compare against staged and untracked worktree snapshots', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-generated-snapshot-'))
  try {
    initGitRepo(root)
    mkdirSync(join(root, 'generated'), { recursive: true })
    writeFileSync(join(root, 'generated', 'tracked.json'), '{"version":1}\n')
    commitFixture(root)

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
    initGitRepo(root)
    mkdirSync(join(root, 'generated'), { recursive: true })
    writeFileSync(join(root, 'generated', 'existing.json'), '{"version":1}\n')
    writeFileSync(join(root, 'generated', 'removed.json'), '{}\n')
    commitFixture(root)

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

test('generated checks ignore absent gitignored build outputs in a cold worktree', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-generated-cold-worktree-'))
  try {
    initGitRepo(root)
    writeFileSync(join(root, '.gitignore'), 'api/gen/\ninternal/agent/contracts/gen/\nweb/generated/\n')
    mkdirSync(join(root, 'internal', 'agent', 'contracts'), { recursive: true })
    writeFileSync(join(root, 'internal', 'agent', 'contracts', 'models.gen.go'), '// generated public model\n')
    commitFixture(root)

    const generator = [
      'bash',
      '-c',
      'mkdir -p api/gen internal/agent/contracts/gen web/generated/data-resources; printf \'{}\\n\' > api/gen/data-resources-ir.json; printf \'{}\\n\' > internal/agent/contracts/gen/agent-tools-ir.json; printf \'export {}\\n\' > web/generated/data-resources/index.ts',
    ]
    const result = runSnapshotCheckPaths(
      root,
      [
        'api/gen/data-resources-ir.json',
        'internal/agent/contracts',
        'web/generated/data-resources',
      ],
      ...generator,
    )
    expect(result.status).toBe(0)
    expect(result.stderr).toBe('')

    const publicDrift = runSnapshotCheckPaths(
      root,
      ['api/gen/data-resources-ir.json', 'internal/agent/contracts', 'web/generated/data-resources'],
      'bash',
      '-c',
      'printf \'{"buildOnly":true}\\n\' > api/gen/second-ir.json; printf \'// stale public model\\n\' > internal/agent/contracts/models.gen.go',
    )
    expect(publicDrift.status).toBe(1)
    expect(publicDrift.stdout + publicDrift.stderr).toContain('models.gen.go')
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('generated checks detect drift in ignored outputs already present in a warm worktree', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-generated-warm-worktree-'))
  try {
    initGitRepo(root)
    writeFileSync(join(root, '.gitignore'), 'api/gen/\n')
    commitFixture(root)
    mkdirSync(join(root, 'api', 'gen'), { recursive: true })
    writeFileSync(join(root, 'api', 'gen', 'data-resources-ir.json'), '{"version":1}\n')

    const result = runSnapshotCheckPaths(
      root,
      ['api/gen/data-resources-ir.json'],
      'bash',
      '-c',
      'printf \'{"version":2}\\n\' > api/gen/data-resources-ir.json',
    )
    expect(result.status).toBe(1)
    expect(result.stdout + result.stderr).toContain('data-resources-ir.json')
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})

test('generated checks repeat generation when cold ignored outputs first appear', () => {
  const root = mkdtempSync(join(tmpdir(), 'leapview-generated-nondeterministic-'))
  try {
    initGitRepo(root)
    writeFileSync(join(root, '.gitignore'), 'api/gen/\n')
    commitFixture(root)

    const result = runSnapshotCheckPaths(
      root,
      ['api/gen/data-resources-ir.json'],
      'bash',
      '-c',
      'mkdir -p api/gen; count=0; [[ ! -f .generator-runs ]] || count=$(cat .generator-runs); count=$((count + 1)); printf \'%s\\n\' "$count" > .generator-runs; printf \'{"run":%s}\\n\' "$count" > api/gen/data-resources-ir.json',
    )
    expect(result.status).toBe(1)
    expect(result.stderr).toContain('nondeterministic')
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

  const docsSiteCommands = tasks['ci:test:docs'].cmds as Array<string | { task?: string }>
  expect(docsSiteCommands[0]).toContain('check_generated_snapshots.sh')
  expect(docsSiteCommands[0]).toContain('-- task --force docs:generate')
  expect(docsSiteCommands.some((command) => typeof command === 'string' && command.includes('git status --porcelain')))
    .toBe(false)
})
