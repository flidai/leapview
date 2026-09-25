import { chromium } from '@playwright/test';

const proxy = process.env.DEMO_BROWSER_PROXY;
if (proxy && !/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(proxy)) throw new Error('Invalid private validation proxy');
// Keep the real HTTPS origin, certificate checks, cookies and Host/SNI. The
// loopback CONNECT proxy can reach only the private demo SSH tunnel.
const browser = await chromium.launch({ headless: true,
  ...(proxy ? { proxy: { server: proxy } } : { args: ['--no-proxy-server'] }),
});
try {
  const page = await browser.newPage();
  let response;
  for (let attempt = 0; attempt < 10; attempt++) {
    try {
      response = await page.goto('https://demo.leapview.dev/login', { timeout: 10000 });
      if (response?.status() === 200) break;
    } catch (error) {
      if (!proxy || attempt === 9) throw error;
    }
    if (!proxy) break;
    await page.waitForTimeout(1000);
  }
  if (response?.status() !== 200) throw new Error('Public login unavailable');
  await page.getByRole('textbox', { name: 'Email', exact: true }).fill(process.env.DEMO_VIEWER_EMAIL);
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill(process.env.DEMO_VIEWER_PASSWORD);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.waitForURL(url => url.pathname !== '/login');
  for (const [name, expected] of [['overview', 7], ['statement', 4], ['liquidity', 8], ['drivers', 8]]) {
    const response = await page.goto(`https://demo.leapview.dev/dashboards/dashboard:cfo-command-center/pages/${name}`);
    if (response.status() !== 200) throw new Error(`CFO ${name}: HTTP ${response.status()}`);
    let ready = 0;
    for (let attempt = 0; attempt < 90; attempt++) {
      const snapshot = await page.locator('body').ariaSnapshot();
      ready = (snapshot.match(/\. Ready\./g) || []).length;
      if (ready === expected && !snapshot.includes('. Loading.')) break;
      await page.waitForTimeout(500);
    }
    if (ready !== expected) throw new Error(`CFO ${name}: ${ready}/${expected} visuals ready`);
    console.log(`CFO ${name}: ${ready} visuals ready`);
  }
} finally {
  await browser.close();
}
