import { expect, test } from 'bun:test'
import { spawnSync } from 'node:child_process'

const script = 'scripts/agent_e2e.sh'

function resolveProvider(environment: Record<string, string>) {
  return spawnSync('bash', [script, '--check-config'], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      LEAPVIEW_AGENT_API_KEY: '',
      LEAPVIEW_AGENT_BASE_URL: '',
      LEAPVIEW_AGENT_MODEL: '',
      DEEPSEEK_API_TOKEN: '',
      DEEPSEEK_API_KEY: '',
      ...environment,
    },
    encoding: 'utf8',
    timeout: 2_000,
  })
}

test('agent E2E accepts a generic provider without inheriting DeepSeek defaults', () => {
  const result = resolveProvider({
    LEAPVIEW_AGENT_API_KEY: 'openrouter-test-key',
    LEAPVIEW_AGENT_BASE_URL: 'https://openrouter.ai/api/v1',
    LEAPVIEW_AGENT_MODEL: 'openai/gpt-4o-mini',
    DEEPSEEK_API_TOKEN: 'legacy-test-key',
  })

  expect(result.status).toBe(0)
  expect(result.stdout).toContain('api_key_configured=true')
  expect(result.stdout).toContain('base_url=https://openrouter.ai/api/v1')
  expect(result.stdout).toContain('model=openai/gpt-4o-mini')
  expect(result.stdout).not.toContain('deepseek')
})

test('agent E2E requires an explicit model for a generic provider key', () => {
  const result = resolveProvider({
    LEAPVIEW_AGENT_API_KEY: 'openrouter-test-key',
    DEEPSEEK_API_TOKEN: 'legacy-test-key',
  })

  expect(result.status).toBe(1)
  expect(result.stderr).toContain('LEAPVIEW_AGENT_MODEL is required')
})

test('agent E2E retains explicit settings when using the legacy DeepSeek key fallback', () => {
  const result = resolveProvider({
    LEAPVIEW_AGENT_BASE_URL: 'https://openrouter.ai/api/v1',
    LEAPVIEW_AGENT_MODEL: 'openai/gpt-4o-mini',
    DEEPSEEK_API_TOKEN: 'legacy-test-key',
  })

  expect(result.status).toBe(0)
  expect(result.stdout).toContain('api_key_configured=true')
  expect(result.stdout).toContain('base_url=https://openrouter.ai/api/v1')
  expect(result.stdout).toContain('model=openai/gpt-4o-mini')
})

test('agent E2E keeps the legacy DeepSeek defaults when no generic key is set', () => {
  const result = resolveProvider({
    DEEPSEEK_API_TOKEN: 'legacy-test-key',
  })

  expect(result.status).toBe(0)
  expect(result.stdout).toContain('api_key_configured=true')
  expect(result.stdout).toContain('base_url=https://api.deepseek.com')
  expect(result.stdout).toContain('model=deepseek-v4-flash')
})

test('agent E2E fails when neither generic nor legacy credentials are set', () => {
  const result = resolveProvider({})

  expect(result.status).toBe(1)
  expect(result.stderr).toContain('LEAPVIEW_AGENT_API_KEY or DEEPSEEK_API_TOKEN/DEEPSEEK_API_KEY is required')
})
