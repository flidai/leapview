import { chromium } from '@playwright/test';
import { validateCFODemo } from './demo_browser_assertions.mjs';

const proxy = process.env.DEMO_BROWSER_PROXY;
if (proxy && !/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(proxy)) throw new Error('Invalid private validation proxy');
// Keep the real HTTPS origin, certificate checks, cookies and Host/SNI. The
// loopback CONNECT proxy can reach only the private demo SSH tunnel.
const browser = await chromium.launch({ headless: true,
  ...(proxy ? { proxy: { server: proxy } } : { args: ['--no-proxy-server'] }),
});
try {
  await validateCFODemo(browser, {
    email: process.env.DEMO_VIEWER_EMAIL,
    password: process.env.DEMO_VIEWER_PASSWORD,
    privateProxy: Boolean(proxy),
  });
} finally {
  await browser.close();
}
