import { expect, test } from 'bun:test'
import { rm } from 'node:fs/promises'
import { chromium, expect as browserExpect } from '@playwright/test'
import { startTestPlayground } from './test-server'

async function liveBuildID(url: URL) {
  const abort = new AbortController()
  const response = await fetch(new URL('/__playground/events', url), { signal: abort.signal })
  const reader = response.body!.getReader()
  const decoder = new TextDecoder()
  let frames = ''
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) throw new Error('Live reload closed before a successful build')
      frames += decoder.decode(value, { stream: true })
      const rebuilt = frames.match(/event: rebuilt\ndata: ([^\n]+)/)
      if (rebuilt) return (JSON.parse(rebuilt[1]) as { buildID: string }).buildID
      if (frames.includes('event: error')) throw new Error(frames)
    }
  } finally { abort.abort() }
}

async function startPreviewPlayground(watch: boolean) {
  const command = `import { startPlayground } from './playground/server';
    const server = await startPlayground(0, { watch: ${watch} });
    console.log('LeapView playground: ' + server.url);
    process.once('SIGTERM', () => { void server.stop(true) });`
  const child = Bun.spawn([process.execPath, '-e', command], {
    cwd: import.meta.dir + '/..', env: process.env,
    stdout: 'pipe', stderr: 'pipe',
  })
  const reader = child.stdout.getReader()
  const decoder = new TextDecoder()
  let output = ''
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) throw new Error(`Playground failed: ${await new Response(child.stderr).text()}`)
      output += decoder.decode(value, { stream: true })
      const address = output.match(/LeapView playground: (http:\/\/[^\s]+)/)
      if (address) return { process: child, url: new URL(address[1]) }
    }
  } catch (error) { child.kill(); throw error }
  finally { reader.releaseLock() }
}

test('test and review builds preserve a watching server’s bundle, reload identity and editor state', async () => {
  const productCSS = Bun.file(import.meta.dir + '/../static/app.css')
  const readProductCSS = async () => await productCSS.exists() ? await productCSS.text() : undefined
  const originalCSS = await readProductCSS()
  const generatedOutput = new URL(`../static/playground-generated-${crypto.randomUUID()}.js`, import.meta.url)
  const watching = await startPreviewPlayground(true)
  let other: Awaited<ReturnType<typeof startTestPlayground>> | undefined
  let review: Awaited<ReturnType<typeof startPreviewPlayground>> | undefined
  let browser: Awaited<ReturnType<typeof chromium.launch>> | undefined
  try {
    const buildID = await liveBuildID(watching.url)
    const bundle = await fetch(new URL('/assets/app.js', watching.url)).then(response => response.text())
    expect(bundle).toContain(JSON.stringify(buildID))
    other = await startTestPlayground()
    const remaining = await fetch(new URL('/assets/app.js', watching.url)).then(response => response.text())
    expect(remaining).toBe(bundle)
    expect(await liveBuildID(watching.url)).toBe(buildID)
    expect(await fetch(new URL('/__playground/events', other.url)).then(response => response.status)).toBe(204)
    expect(await readProductCSS()).toBe(originalCSS)

    browser = await chromium.launch()
    const page = await browser.newPage()
    const errors: string[] = []
    page.on('pageerror', error => errors.push(error.message))
    let navigations = 0
    page.on('framenavigated', frame => { if (frame === page.mainFrame()) navigations++ })
    await page.goto(`${watching.url}?theme=light#recipes/dashboard-contract`)
    const source = page.getByRole('textbox', { name: 'Dashboard YAML source', exact: true })
    await browserExpect(source).toBeVisible()
    await source.focus()
    await source.press('ControlOrMeta+End')
    await page.keyboard.insertText('\n# preserved editor draft\n')
    const example = page.locator('playground-dashboard-contract')
    await browserExpect.poll(() => example.evaluate((element: any) => element.getExampleCode())).toContain('# preserved editor draft')
    const draft = await example.evaluate((element: any) => element.getExampleCode())
    const initialNavigations = navigations
    await other.stop(true)
    other = undefined
    // Ignored product-build output is not an authored source change.
    await Bun.write(generatedOutput, '// generated product output\n')
    review = await startPreviewPlayground(false)
    expect(await fetch(new URL('/__playground/events', review.url)).then(response => response.status)).toBe(204)
    expect(await fetch(new URL('/assets/app.js', review.url)).then(response => response.text())).not.toBe(bundle)
    // Wait through the reload client's EventSource reconnect interval as well as
    // rebuilding the other server, so an identity collision cannot go unnoticed.
    await page.waitForTimeout(3500)
    expect(navigations).toBe(initialNavigations)
    expect(await example.evaluate((element: any) => element.getExampleCode())).toBe(draft)
    expect(await fetch(new URL('/assets/app.js', watching.url)).then(response => response.text())).toBe(bundle)
    expect(await readProductCSS()).toBe(originalCSS)
    expect(errors).toEqual([])
  } finally {
    await browser?.close()
    await other?.stop(true)
    if (review) {
      review.process.kill('SIGTERM')
      await review.process.exited
    }
    watching.process.kill('SIGTERM')
    await watching.process.exited
    await rm(generatedOutput, { force: true })
  }
}, 120000)
