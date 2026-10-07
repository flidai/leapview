import { pathToFileURL } from 'node:url';
import { validateCFODemo } from '../../../../../scripts/demo_browser_assertions.mjs';

// Only this synthetic fixture trusts a generated certificate. The real release
// validator retains normal certificate verification. A single exact SPKI pin
// grants no wildcard or general ignore-HTTPS behavior.
export function historicalBrowserOptions(env) {
  const proxy = env.DEMO_BROWSER_PROXY;
  const pin = env.DEMO_HISTORICAL_BROWSER_SPKI;
  if (env.DEMO_CLONE_ONLY !== '1' || !proxy || proxy !== env.DEMO_CLONE_PROXY ||
      !/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(proxy)) {
    throw new Error('Historical browser requires the fixed private clone proxy');
  }
  const port = Number(new URL(proxy).port);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    throw new Error('Invalid historical browser proxy port');
  }
  if (!pin || !/^[A-Za-z0-9+/]{43}=$/.test(pin) ||
      Buffer.from(pin, 'base64').length !== 32 || Buffer.from(pin, 'base64').toString('base64') !== pin) {
    throw new Error('Historical browser requires one canonical SHA-256 SPKI pin');
  }
  return { headless: true, proxy: { server: proxy },
    args: ['--ignore-certificate-errors-spki-list=' + pin] };
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const options = historicalBrowserOptions(process.env);
  const { chromium } = await import('@playwright/test');
  const browser = await chromium.launch(options);
  let observedPage;
  try {
    await validateCFODemo(browser, { email: process.env.DEMO_VIEWER_EMAIL,
      password: process.env.DEMO_VIEWER_PASSWORD, privateProxy: true,
      observePage(page) {
        observedPage = page;
        let errors = 0;
        page.on('pageerror', error => {
          if (errors++ < 10) console.error('Historical browser page error:', error.message);
        });
        page.on('crash', () => console.error('Historical browser renderer crashed'));
      } });
  } catch (error) {
    if (observedPage && !observedPage.isClosed()) {
      const { mkdtemp, chmod } = await import('node:fs/promises');
      const { tmpdir } = await import('node:os');
      const { join } = await import('node:path');
      const directory = await mkdtemp(join(tmpdir(), 'leapview-historical-browser-'));
      await chmod(directory, 0o700);
      try {
        await observedPage.screenshot({ path: join(directory, 'failure.png'), timeout: 5000 });
        console.error('Historical browser failure screenshot:', join(directory, 'failure.png'));
      } catch (captureError) {
        console.error('Historical browser screenshot unavailable:', captureError.message);
      }
    }
    throw error;
  } finally {
    await browser.close();
  }
}
