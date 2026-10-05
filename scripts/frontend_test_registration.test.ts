import { expect, test } from 'bun:test'
import {
  auditFrontendTestRegistration,
  bunRunTestScripts,
  globMatches,
  registrationFailures,
  taskCommandTargets,
  testPathPatterns,
} from './frontend_test_registration.mjs'

const frontendShards = ['core', 'reports', 'chat', 'data', 'site']

function parameterizedTaskfile() {
  const tasks: Record<string, unknown> = {
    'ci:lane:frontend': {
      cmds: frontendShards.map((shard) => ({ task: 'ci:lane:frontend:shard', vars: { SHARD: shard } })),
    },
    'ci:lane:frontend:shard': {
      requires: { vars: [{ name: 'SHARD', enum: frontendShards }] },
      cmds: ['node scripts/ci_watchdog.mjs --timeout-seconds {{if eq .SHARD "reports"}}300{{else}}180{{end}} -- task ci:test:frontend:{{.SHARD}}'],
    },
  }
  for (const shard of frontendShards) tasks[`ci:test:frontend:${shard}`] = { cmds: [`bun run test:${shard}`] }
  return { tasks }
}

test('frontend registration resolves each parameterized Task shard and its scripts', () => {
  const result = auditFrontendTestRegistration({
    taskfile: parameterizedTaskfile(),
    packageJson: {
      scripts: {
        'test:core': 'bun test web/components/app/core.test.ts',
        'test:reports': 'bun test web/components/dashboard/report.test.ts',
        'test:chat': 'bun test web/components/chat/chat.test.ts',
        'test:data': 'bun test web/components/data/data.test.ts',
        'test:site': 'bun test web/components/site/site.test.ts',
      },
    },
    componentTestFiles: [
      'web/components/app/core.test.ts',
      'web/components/dashboard/report.test.ts',
      'web/components/chat/chat.test.ts',
      'web/components/data/data.test.ts',
      'web/components/site/site.test.ts',
    ],
    taskRoots: ['ci:lane:frontend'],
  })

  expect(result.shardValues).toEqual([...frontendShards].sort())
  expect(result.missing).toEqual([])
  expect(result.outsideFrontendGraph).toEqual([])
  expect(result.reachableScripts).toEqual(frontendShards.map((shard) => `test:${shard}`).sort())
  expect(result.reachableTaskInvocations.filter(({ task }) => task === 'ci:lane:frontend:shard').map(({ vars }) => vars.SHARD).sort()).toEqual([...frontendShards].sort())
})

test('frontend registration expands the shard enum when the hosted matrix enters at the shard task', () => {
  const result = auditFrontendTestRegistration({
    taskfile: parameterizedTaskfile(),
    packageJson: { scripts: Object.fromEntries(frontendShards.map((shard) => [`test:${shard}`, `bun test web/components/${shard}/one.test.ts`])) },
    componentTestFiles: frontendShards.map((shard) => `web/components/${shard}/one.test.ts`),
    taskRoots: ['ci:lane:frontend:shard'],
  })

  expect(result.shardValues).toEqual([...frontendShards].sort())
  expect(result.missing).toEqual([])
})

test('dead package scripts are reported outside CI and do not count as registered', () => {
  const result = auditFrontendTestRegistration({
    taskfile: { tasks: { 'ci:lane:frontend': { cmds: ['bun run test:live'] } } },
    packageJson: {
      scripts: {
        'test:live': 'bun test web/components/live/live.test.ts',
        'test:dead': 'bun test web/components/dead/dead.test.ts',
      },
    },
    componentTestFiles: [
      'web/components/live/live.test.ts',
      'web/components/dead/dead.test.ts',
      'web/components/orphan/orphan.test.ts',
    ],
    taskRoots: ['ci:lane:frontend'],
  })

  expect(result.missing).toEqual(['web/components/orphan/orphan.test.ts'])
  expect(result.outsideFrontendGraph).toEqual(['web/components/dead/dead.test.ts'])
  expect(registrationFailures(result).join('\n')).toContain('web/components/dead/dead.test.ts')
})

test('frontend registration follows nested Task dependencies, package scripts, and globs', () => {
  const result = auditFrontendTestRegistration({
    taskfile: {
      tasks: {
        'ci:lane:frontend': { deps: [{ task: 'ci:test:shared', vars: { GROUP: 'shared' } }] },
        'ci:test:shared': { cmds: ['bun run test:shared'] },
      },
    },
    packageJson: {
      scripts: {
        'test:shared': 'bun run test:shared:prepared',
        'test:shared:prepared': 'bun test web/components/shared/**/*.test.ts',
      },
    },
    componentTestFiles: [
      'web/components/shared/config-viewer.test.ts',
      'web/components/shared/nested/table.test.ts',
    ],
    taskRoots: ['ci:lane:frontend'],
  })

  expect(result.missing).toEqual([])
  expect(result.reachableScripts).toEqual(['test:shared', 'test:shared:prepared'])
})

test('Task shell parsing ignores echoed commands and preserves explicit task targets', () => {
  const names = new Set(['ci:lane:go', 'ci:lane:frontend'])
  expect(taskCommandTargets('task --parallel --concurrency 2 ci:lane:go ci:lane:frontend', names)).toEqual(['ci:lane:go', 'ci:lane:frontend'])
  expect(taskCommandTargets('echo "task ci:orphan"', new Set(['ci:orphan']))).toEqual([])
  expect(taskCommandTargets('TASK_TMP=/tmp node scripts/ci_watchdog.mjs -- task ci:lane:frontend SHARD=reports', names)).toEqual(['ci:lane:frontend'])
  expect(taskCommandTargets('echo "node scripts/ci_watchdog.mjs -- task ci:lane:frontend"', names)).toEqual([])
  expect(taskCommandTargets('node scripts/ci_watchdog.mjs --timeout-seconds 180 -- task ci:lane:frontend SHARD=reports', names)).toEqual(['ci:lane:frontend'])
})

test('single-star stays within one directory while double-star crosses directories', () => {
  expect(globMatches('web/components/dashboard/adapters/*.test.ts', 'web/components/dashboard/adapters/map.test.ts')).toBe(true)
  expect(globMatches('web/components/dashboard/adapters/*.test.ts', 'web/components/dashboard/adapters/map/nested.test.ts')).toBe(false)
  expect(globMatches('web/components/shared/**/*.test.ts', 'web/components/shared/config.test.ts')).toBe(true)
  expect(globMatches('web/components/shared/**/*.test.ts', 'web/components/shared/nested/table.test.ts')).toBe(true)
})

test('only Bun test invocations create test path patterns', () => {
  expect(testPathPatterns('echo web/components/app/echoed.test.ts && bun scripts/build_test_assets.ts')).toEqual([])
  expect(testPathPatterns('echo "bun test web/components/app/quoted.test.ts"')).toEqual([])
  expect(testPathPatterns('bun scripts/build_test_assets.ts web/components/app/built-only.test.ts')).toEqual([])
  expect(testPathPatterns('bun scripts/build_test_assets.ts app-shell && bun test web/components/app/registered.test.ts')).toEqual([
    'web/components/app/registered.test.ts',
  ])
  expect(bunRunTestScripts('echo "bun run test:orphan"')).toEqual([])
  expect(bunRunTestScripts('TEST_TMP=/tmp bun run test:registered && echo "bun run test:orphan"')).toEqual(['test:registered'])
})

test('quoted and escaped shell separators do not create reachable commands', () => {
  const orphan = 'web/components/orphan/orphan.test.ts'
  expect(testPathPatterns(`echo "ignored && bun test ${orphan}"`)).toEqual([])
  expect(testPathPatterns(`echo 'ignored ; bun test ${orphan}'`)).toEqual([])
  expect(testPathPatterns(String.raw`echo "ignored \" && bun test ${orphan}"`)).toEqual([])
  expect(testPathPatterns(String.raw`echo ignored \; bun test ${orphan}`)).toEqual([])
  expect(testPathPatterns(`echo "quoted && text" && bun test ${orphan}`)).toEqual([orphan])

  const result = auditFrontendTestRegistration({
    taskfile: { tasks: { 'ci:lane:frontend': { cmds: [`echo "ignored && bun test ${orphan}"`] } } },
    packageJson: { scripts: {} },
    componentTestFiles: [orphan],
    taskRoots: ['ci:lane:frontend'],
  })
  expect(result.missing).toEqual([orphan])
})

test('shell comments hide trailing test paths but quoted and escaped hashes stay literal', () => {
  const live = 'web/components/live/live.test.ts'
  const orphan = 'web/components/orphan/orphan.test.ts'
  expect(testPathPatterns(`bun test ${live} # ${orphan}`)).toEqual([live])
  expect(testPathPatterns(`echo '#' && bun test ${live}`)).toEqual([live])
  expect(testPathPatterns(String.raw`echo \# && bun test ${live}`)).toEqual([live])

  const result = auditFrontendTestRegistration({
    taskfile: { tasks: { 'ci:lane:frontend': { cmds: [`bun test ${live} # ${orphan}`] } } },
    packageJson: { scripts: {} },
    componentTestFiles: [live, orphan],
    taskRoots: ['ci:lane:frontend'],
  })
  expect(result.missing).toEqual([orphan])
})
