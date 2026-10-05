// Integration check: run inside the repository's Nix shell after task node:deps.
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { access, readFile } from 'node:fs/promises'
import { createRequire } from 'node:module'

const require = createRequire(import.meta.url)
const expectedPlaywright = process.env.LEAPVIEW_TEST_NIX_PLAYWRIGHT_VERSION
assert.ok(expectedPlaywright, 'Run this check with nix develop -c task nix:smoke')
assert.equal(require('playwright-core/package.json').version, expectedPlaywright,
  'Update the locked Nix Playwright input alongside bun.lock; browser revisions must match')
assert.ok(process.env.FONTCONFIG_FILE?.startsWith('/nix/store/'), 'Headless Chromium needs the pinned font configuration')
await access(process.env.FONTCONFIG_FILE)
const fontConfiguration = await readFile(process.env.FONTCONFIG_FILE, 'utf8')
for (const entry of fontConfiguration.matchAll(/<(?:dir|include)\b[^>]*>([^<]+)<\/(?:dir|include)>/g)) {
  assert.ok(entry[1].startsWith('/nix/store/'),
    `Browser fonts and configuration must not depend on the host: ${entry[1]}`)
}
const manifest = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'))
const goMod = await readFile(new URL('../go.mod', import.meta.url), 'utf8')
const goVersion = goMod.match(/^go (\S+)$/m)?.[1]
const run = (command, args, options = {}) => execFileSync(command, args, {
  encoding: 'utf8', timeout: 30_000, ...options,
}).trim()
assert.equal(run('go', ['env', 'GOVERSION']), `go${goVersion}`)
assert.equal(run('bun', ['--version']), manifest.packageManager.replace(/^bun@/, ''))
assert.match(run('node', ['--version']), /^v24\./)
assert.match(run('go', ['version'], { env: { ...process.env, GOTOOLCHAIN: 'go1.26.7' } }), /go1\.26\.7 /)
const { chromium } = await import('@playwright/test')
assert.ok(chromium.executablePath().startsWith('/nix/store/'), 'Chromium must come from the Nix store')
const browser = await chromium.launch({ headless: true })
try {
  const page = await browser.newPage()
  const interFont = await readFile(new URL('../static/files/inter-latin-wght-normal.woff2', import.meta.url))
  await page.setContent(`<style>@font-face{font-family:"Inter Variable";src:url(data:font/woff2;base64,${interFont.toString('base64')});font-weight:100 900}</style><button onclick="this.textContent=42">Run</button>`)
  await page.getByRole('button', { name: 'Run' }).click()
  assert.equal(await page.getByRole('button').textContent(), '42')
  // ZRender uses this CJK glyph's width as its line height, including in Latin
  // charts. A missing fallback changes axis and bar geometry without data changes.
  const lineHeights = await page.evaluate(async () => {
    await document.fonts.load('12px "Inter Variable"')
    await document.fonts.ready
    const context = document.createElement('canvas').getContext('2d')
    return ['sans-serif', '"Inter Variable", system-ui, sans-serif'].flatMap((family) =>
      [12, 24].map((size) => {
        context.font = `${size}px ${family}`
        return context.measureText('国').width
      }))
  })
  assert.deepEqual(lineHeights, [12, 24, 12, 24], 'Pinned Chromium needs a CJK fallback for stable ECharts text metrics')
  console.log(`Nix development check passed: Go ${goVersion}, Bun ${run('bun', ['--version'])}, Playwright ${expectedPlaywright}, Chromium ${browser.version()}`)
} finally {
  await browser.close()
}
