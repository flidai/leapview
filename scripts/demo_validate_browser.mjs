import { chromium } from '@playwright/test';

const browser = await chromium.launch({ headless: true, args: ['--no-proxy-server'] });
try {
  const page = await browser.newPage();
  const response = await page.goto('https://demo.leapview.dev/login');
  if (response.status() !== 200) throw new Error('Public login unavailable');
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
