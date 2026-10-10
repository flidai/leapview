import { expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { parse } from 'yaml'

test('cold Nix development prepares pinned SQLC modules before source generation', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  const prepare = workflow.jobs.development.steps.find((step: any) =>
    step.name === 'Prepare generated and embedded assets for the fresh checkout')
  expect(prepare.run).toContain('nix develop --no-update-lock-file -c bash scripts/prepare_sqlc_modules.sh')
  expect(prepare.run.indexOf('prepare_sqlc_modules.sh')).toBeLessThan(prepare.run.indexOf('task ci:prepare'))
  for (const input of ['scripts/prepare_sqlc_modules.sh', 'scripts/tests/test_prepare_sqlc_modules.py']) {
    expect(workflow.on.pull_request.paths).toContain(input)
  }
  expect(readFileSync('Taskfile.yml', 'utf8')).toContain(
    'python3 -m unittest discover -s scripts/tests -p test_prepare_sqlc_modules.py')
})

test('external conformance prepares pinned fixture images before container work', () => {
  const workflow = parse(readFileSync('.github/workflows/nix-development.yml', 'utf8'))
  for (const input of ['scripts/prepare_ci_fixture_images.sh', 'scripts/tests/test_prepare_ci_fixture_images.py',
    'scripts/postgres-conformance-tests.sh', 'scripts/postgres-app-shards.sh',
    'internal/platform/postgres/postgrestest/harness.go',
    'internal/platform/postgres/postgrestest/cmd/packageexec/main.go',
    'internal/platform/testminio/Dockerfile']) {
    expect(workflow.on.pull_request.paths.some((pattern: string) => new Bun.Glob(pattern).match(input))).toBe(true)
  }
  expect(readFileSync('Taskfile.yml', 'utf8')).toContain(
    'python3 -m unittest discover -s scripts/tests -p test_prepare_ci_fixture_images.py')
  const runner = readFileSync('scripts/postgres-conformance-tests.sh', 'utf8')
  expect(runner).toContain('bash "$root/scripts/prepare_ci_fixture_images.sh" postgres')
  expect(runner.indexOf('bash "$root/scripts/prepare_ci_fixture_images.sh"')).toBeLessThan(
    runner.indexOf('bash "$root/scripts/postgres-app-shards.sh"'))
  expect(runner.indexOf('bash "$root/scripts/prepare_ci_fixture_images.sh"')).toBeLessThan(
    runner.indexOf('go test -exec'))
  const external = parse(readFileSync('Taskfile.yml', 'utf8')).tasks['test:go:external'].cmds
  expect(external).toContain('bash scripts/prepare_ci_fixture_images.sh minio')
  expect(external.indexOf('bash scripts/prepare_ci_fixture_images.sh minio')).toBeLessThan(
    external.indexOf('go test -tags integration ./internal/platform/testminio -count=1'))
})
