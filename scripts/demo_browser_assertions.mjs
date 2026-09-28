// Shared by the live release gate and the isolated historical fixture.
// Browser launch and certificate trust remain owned by each caller.
export async function validateCFODemo(browser, { email, password, privateProxy = false, observePage }) {
  if (!email || !password) throw new Error('Shared viewer credentials are required');
  const page = await browser.newPage();
  if (observePage) observePage(page);
  let response;
  for (let attempt = 0; attempt < 10; attempt++) {
    try {
      response = await page.goto('https://demo.leapview.dev/login', { timeout: 10000 });
      if (response?.status() === 200) break;
    } catch (error) {
      if (!privateProxy || attempt === 9) throw error;
    }
    if (!privateProxy) break;
    await page.waitForTimeout(1000);
  }
  if (response?.status() !== 200) throw new Error('Public login unavailable');
  await page.getByRole('textbox', { name: 'Email', exact: true }).fill(email);
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.waitForURL(url => url.pathname !== '/login');
  for (const [name, expected] of [['overview', 7], ['statement', 4], ['liquidity', 8], ['drivers', 8]]) {
    const response = await page.goto(`https://demo.leapview.dev/dashboards/dashboard:cfo-command-center/pages/${name}`);
    if (response.status() !== 200) throw new Error(`CFO ${name}: HTTP ${response.status()}`);
    let ready = 0;
    for (let attempt = 0; attempt < 90; attempt++) {
      let snapshot;
      try {
        snapshot = await page.locator('body').ariaSnapshot();
      } catch (error) {
        throw new Error(`CFO ${name}: cannot inspect visual readiness at ${page.url()} (page closed: ${page.isClosed()})`, { cause: error });
      }
      ready = (snapshot.match(/\. Ready\./g) || []).length;
      if (ready === expected && !snapshot.includes('. Loading.')) break;
      await page.waitForTimeout(500);
    }
    if (ready !== expected) throw new Error(`CFO ${name}: ${ready}/${expected} visuals ready`);
    console.log(`CFO ${name}: ${ready} visuals ready`);
  }
}
