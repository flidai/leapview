import assert from 'node:assert/strict';
import test from 'node:test';
import { PassThrough } from 'node:stream';
import { execFileSync, spawnSync } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { browserClient, createPrivateBrowserContext, fillAdminLogin, finishPrivateDriver, openPrivateLogin, performAgentCredentialTransition, privateInputChannel, validatePrivateInput } from '../demo_agent_credential_transition.mjs';

const root = dirname(dirname(dirname(fileURLToPath(import.meta.url))));

test('private context blocks service workers and requests outside the canonical demo origin', async () => {
  let handler;
  const context = { async route(pattern, callback) { assert.equal(pattern, '**/*'); handler = callback; } };
  const browser = { async newContext(options) { assert.deepEqual(options, { serviceWorkers: 'block' }); return context; } };
  assert.equal(await createPrivateBrowserContext(browser), context);
  for (const [url, allowed] of [
    ['https://demo.leapview.dev/login', true], ['https://demo.leapview.dev/static/app.js', true],
    ['https://demo.leapview.dev:443/admin/agent', true], ['https://other.example/login', false],
    ['http://127.0.0.1:8000/login', false], ['http://[::1]/login', false],
    ['http://demo.leapview.dev/login', false], ['https://demo.leapview.dev:8443/login', false],
    ['https://demo.leapview.dev.other.example/login', false], ['https://user@demo.leapview.dev/login', false],
    ['not-a-url', false],
  ]) {
    let result;
    await handler({ request: () => ({ url: () => url }),
      continue: async () => { result = 'continue'; }, abort: async () => { result = 'abort'; } });
    assert.equal(result, allowed ? 'continue' : 'abort', url);
  }
});

test('private login attests final origin and path before filling any credentials', async () => {
  for (const url of ['https://other.example/login', 'http://127.0.0.1/login',
    'https://demo.leapview.dev/admin/agent', 'https://demo.leapview.dev/login/']) {
    const page = { goto: async () => ({ status: () => 200 }), url: () => url,
      getByLabel: () => assert.fail('credentials filled before login URL attestation') };
    await assert.rejects(openPrivateLogin(page, input()));
  }
  for (const status of [302, 403, 500, null]) {
    const page = { goto: async () => status === null ? null : { status: () => status },
      url: () => 'https://demo.leapview.dev/login',
      getByLabel: () => assert.fail('credentials filled after failed navigation') };
    await assert.rejects(openPrivateLogin(page, input()));
  }
  const fills = [];
  await openPrivateLogin({ goto: async url => { assert.equal(url, 'https://demo.leapview.dev/login'); return { status: () => 200 }; },
    url: () => 'https://demo.leapview.dev/login',
    getByLabel: label => ({ fill: async value => fills.push([label, value]) }) }, input());
  assert.deepEqual(fills, [['Email', input().loginEmail], ['Password', input().adminPassword]]);
});

const input = () => ({
  operationDigest: 'sha256:' + 'a'.repeat(64),
  intent: { reference: 'demo-migration', transitionVersion: '8a22935d-54ab-4b99-90ef-72cc4130be2a',
    fileDigest: 'sha256:' + 'b'.repeat(64), installationId: 'app-leapview-demo-02',
    instanceId: 'instance_demo', customerOwnerId: 'customer_demo', expectedRevision: 2,
    actorId: 'principal_admin', providerAddress: '93.184.215.14',
    provider: { enabled: true, model: 'model', baseUrl: 'https://provider.example/v1', apiMode: 'responses', reasoningEffort: 'high' } },
  loginEmail: 'admin@example.test', adminPassword: 'synthetic-password', apiKey: 'synthetic-provider-key',
});

function fixture(options = {}) {
  const privateInput = input();
  let current = 2;
  let saves = 0;
  const calls = [];
  const details = () => ({ ...privateInput.intent.provider, configurationRevision: current,
    adminManaged: true, configurationAvailable: true, credentialConfigured: current === 3,
    configured: current === 3, status: current === 3 ? 'enabled' : 'degraded',
    ...(current === 3 ? { credentialVersionId: 'immutable-version' } : {}) });
  const client = {
    async get(path) {
      calls.push(['GET', path]);
      if (path === '/api/v1/me') return { status: 200, body: { id: options.actor ?? privateInput.intent.actorId, kind: 'user', email: privateInput.loginEmail } };
      if (path === '/api/v1/instance/system') return { status: 200, body: { instanceId: options.instance ?? privateInput.intent.instanceId, canonicalOrigin: 'https://demo.leapview.dev' } };
      if (path === '/api/v1/agent/config') return { status: 200, etag: `revision-${current}`, body: details() };
      throw new Error('unsupported path');
    },
    async patch(path, body, etag) {
      calls.push(['PATCH', path, structuredClone(body), etag]);
      if (body.action === 'test') return { status: options.testStatus ?? 200, etag: 'revision-2', body: { ...details(), testToken: 'private-validation-receipt' } };
      saves++;
      if (options.retryStatus && saves > 1) return { status: options.retryStatus, body: {} };
      if (options.saveStatus) return { status: options.saveStatus, body: {} };
      if (options.ackLost && saves === 1) {
        if (options.committed) current = 3;
        throw new Error(privateInput.apiKey);
      }
      current = 3;
      return { status: 200, etag: 'revision-3', body: details() };
    },
  };
  const markers = [];
  let cleanup = false;
  const waitForSave = async () => { cleanup = true; return { action: 'save', operationDigest: privateInput.operationDigest }; };
  const originalPatch = client.patch;
  client.patch = async (path, body, etag) => { if (body.action === 'save') assert.equal(cleanup, true); return originalPatch(path, body, etag); };
  return { privateInput, client, calls, markers, waitForSave,
    run: (overrides = {}) => performAgentCredentialTransition({ input: privateInput, client,
      waitForSave, emit: marker => markers.push(marker), ...overrides }) };
}

test('real receipt stays private and Save follows the cleanup acknowledgment', async () => {
  const f = fixture();
  await f.run();
  assert.deepEqual(f.markers, ['TEST_PASSED']);
  const patches = f.calls.filter(call => call[0] === 'PATCH');
  assert.deepEqual(patches.map(call => call[2].action), ['test', 'save']);
  assert.equal(patches[1][2].testToken, 'private-validation-receipt');
  assert.equal(patches[1][2].provider.apiKey, f.privateInput.apiKey);
  assert.equal(patches[1][2].expectedRevision, 2);
});

for (const committed of [false, true]) test(`lost Save acknowledgment retries exact receipt and input (committed=${committed})`, async () => {
  const f = fixture({ ackLost: true, committed });
  await f.run();
  const patches = f.calls.filter(call => call[0] === 'PATCH');
  assert.equal(patches.length, 3);
  assert.deepEqual(patches[1][2], patches[2][2]);
  assert.equal(patches[2][3], committed ? 'revision-3' : 'revision-2');
  assert.equal(patches.filter(call => call[2].action === 'test').length, 1);
});

for (const options of [{ actor: 'other' }, { instance: 'other' }, { testStatus: 403 }]) test(`wrong identity or denied Test cannot Save ${JSON.stringify(options)}`, async () => {
  const f = fixture(options);
  await assert.rejects(f.run());
  assert.equal(f.calls.filter(call => call[0] === 'PATCH' && call[2].action === 'save').length, 0);
});

for (const saveStatus of [401, 403, 422]) test(`expired/revoked receipt or authorization failure is terminal (${saveStatus})`, async () => {
  const f = fixture({ saveStatus });
  await assert.rejects(f.run());
  assert.equal(f.calls.filter(call => call[0] === 'PATCH' && call[2].action === 'save').length, 1);
});

test('wrong digest, wrong phase and EOF cannot cross the cleanup barrier', async () => {
  for (const command of [null, { action: 'test', operationDigest: input().operationDigest }, { action: 'save', operationDigest: 'sha256:' + 'c'.repeat(64) }]) {
    const f = fixture();
    await assert.rejects(f.run({ waitForSave: async () => command }));
    assert.equal(f.calls.filter(call => call[0] === 'PATCH' && call[2].action === 'save').length, 0);
  }
});

test('secret-bearing malformed input is rejected without exposing its contents', () => {
  const malformed = input();
  malformed.apiKey = 'invalid\nprivate-key';
  assert.throws(() => validatePrivateInput(malformed), error => !String(error).includes(malformed.apiKey));
  const oversized = input(); oversized.apiKey = 'x'.repeat(16385);
  assert.throws(() => validatePrivateInput(oversized));
});

test('private protocol rejects early/duplicate commands, EOF and cancellation', async () => {
  for (const action of ['early', 'duplicate', 'eof', 'cancel']) {
    const stream = new PassThrough();
    let aborted = false;
    const channel = privateInputChannel(stream, () => { aborted = true; });
    if (action === 'early') {
      stream.write('{}\n');
      await assert.rejects(channel.next());
    } else if (action === 'duplicate') {
      const first = channel.next();
      stream.write('{}\n{}\n');
      await first;
      await assert.rejects(channel.next());
    } else {
      const pending = channel.next();
      if (action === 'eof') stream.end(); else channel.cancel();
      await assert.rejects(pending);
    }
    assert.equal(aborted, true);
    stream.destroy();
  }
});

test('browser API transport preserves CSRF, ETag, session and operation claims', async () => {
  let parameters;
  const page = { async evaluate(callback, args) {
    parameters = args;
    const document = globalThis.document, fetch = globalThis.fetch;
    try {
      globalThis.document = { querySelector: () => ({ content: 'csrf-session-token' }) };
      globalThis.fetch = async (path, options) => {
        assert.equal(path, '/api/v1/agent/config');
        assert.equal(options.credentials, 'same-origin');
        assert.equal(options.redirect, 'error');
        assert.equal(options.headers['X-CSRF-Token'], 'csrf-session-token');
        assert.equal(options.headers['If-Match'], 'exact-etag');
        assert.equal(options.headers['X-LeapView-Operation-ID'], 'updateAgentConfig');
        assert.match(options.headers['X-Request-ID'], /^[0-9a-f-]{14}7[0-9a-f-]{21}$/);
        return new Response('{}', { status: 200, headers: { ETag: 'response-etag' } });
      };
      return await callback(args);
    } finally { globalThis.document = document; globalThis.fetch = fetch; }
  } };
  const body = { action: 'test', provider: { apiKey: 'synthetic-private-key' } };
  const result = await browserClient(page).patch('/api/v1/agent/config', body, 'exact-etag');
  assert.deepEqual(parameters.data, body);
  assert.equal(result.etag, 'response-etag');
});

test('matching postcommit metadata cannot replace exact activation receipt recovery', async () => {
  const f = fixture({ ackLost: true, committed: true, retryStatus: 422 });
  await assert.rejects(f.run());
  const saves = f.calls.filter(call => call[0] === 'PATCH' && call[2].action === 'save');
  assert.equal(saves.length, 2);
  assert.deepEqual(saves[0][2], saves[1][2]);
  assert.equal(saves[1][3], 'revision-3');
});

test('actor change between Test and Save blocks activation', async () => {
  const options = {};
  const f = fixture(options);
  await assert.rejects(f.run({ waitForSave: async () => {
    options.actor = 'changed-admin';
    return { action: 'save', operationDigest: f.privateInput.operationDigest };
  } }));
  assert.equal(f.calls.filter(call => call[0] === 'PATCH' && call[2].action === 'save').length, 0);
});

test('missing CSRF token blocks the browser mutation before fetch', async () => {
  const page = { async evaluate(callback, args) {
    const document = globalThis.document, fetch = globalThis.fetch;
    try {
      globalThis.document = { querySelector: () => null };
      globalThis.fetch = () => assert.fail('mutation reached fetch without CSRF');
      return await callback(args);
    } finally { globalThis.document = document; globalThis.fetch = fetch; }
  } };
  await assert.rejects(browserClient(page).patch('/api/v1/agent/config', { action: 'test' }, 'etag'));
});

test('opaque password admission preserves spaces and enforces product byte limit', () => {
  const value = input();
  value.adminPassword = ' synthetic password with surrounding spaces ';
  assert.equal(validatePrivateInput(value).adminPassword, value.adminPassword);
  value.adminPassword = 'é'.repeat(513);
  assert.throws(() => validatePrivateInput(value));
});

test('cancellation during browser teardown never acknowledges Save', async () => {
  for (const during of ['context', 'browser']) {
    const controller = new AbortController();
    const acknowledged = [];
    const context = { async close() { if (during === 'context') controller.abort(); } };
    const browser = { async close() { if (during === 'browser') controller.abort(); } };
    await assert.rejects(finishPrivateDriver({ context, browser, signal: controller.signal,
      channel: { finish: () => assert.fail('aborted private protocol finished') }, emit: value => acknowledged.push(value) }));
    assert.deepEqual(acknowledged, []);
  }
});

test('cancellation after cleanup acknowledgment fences the Save mutation', async () => {
  const f = fixture();
  const controller = new AbortController();
  await assert.rejects(f.run({ waitForSave: async () => {
    controller.abort();
    return { action: 'save', operationDigest: f.privateInput.operationDigest };
  }, checkCancelled: () => { if (controller.signal.aborted) throw new Error('canceled'); } }));
  assert.equal(f.calls.filter(call => call[0] === 'PATCH' && call[2].action === 'save').length, 0);
});

test('CLI malformed private input reports only a fixed failure without secrets', () => {
  const value = input(); value.intent.unexpected = value.apiKey;
  const child = spawnSync(process.execPath, [join(root, 'scripts/demo_agent_credential_transition.mjs'),
    '--base-url', 'https://demo.leapview.dev', '--proxy-url', 'http://127.0.0.1:12345'],
  { input: JSON.stringify(value) + '\n', encoding: 'utf8', timeout: 10000,
    env: { ...process.env, DEBUG: '', PWDEBUG: '', NODE_DEBUG: '', NODE_OPTIONS: '' } });
  assert.equal(child.status, 1);
  assert.equal(child.stdout, '');
  assert.equal(child.stderr, 'Agent credential transition failed.\n');
});

test('private login driver locates the actual Lit login fields and preserves password bytes', { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), 'leapview-agent-login-'));
  let browser, server;
  try {
    execFileSync('bun', ['build', 'web/components/login/login-page.ts', '--target', 'browser', '--format', 'esm',
      '--external', '/static/vendor/datastar-1.0.2.js', '--outfile', join(directory, 'login.js')],
    { cwd: root, stdio: 'pipe', timeout: 15000 });
    server = createServer(async (request, response) => {
      try {
        const pathname = new URL(request.url, 'http://127.0.0.1').pathname;
        if (pathname === '/') {
          const signals = JSON.stringify({ page: { kind: 'login', title: 'LeapView', localAuth: true,
            developmentLogin: false, ssoAuth: false, mustChangePassword: false, providerLabel: 'Sign in' },
          status: { error: '' } }).replaceAll('"', '&quot;');
          response.setHeader('Content-Type', 'text/html');
          response.end(`<!doctype html><main data-signals="${signals}"><lv-login-page></lv-login-page></main><script type="module" src="/login.js"></script>`);
        } else if (pathname === '/login.js' || pathname === '/static/vendor/datastar-1.0.2.js') {
          response.setHeader('Content-Type', 'text/javascript');
          response.end(await readFile(pathname === '/login.js' ? join(directory, 'login.js') : join(root, pathname)));
        } else { response.writeHead(404); response.end(); }
      } catch { response.writeHead(500); response.end(); }
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    const { chromium } = await import('@playwright/test');
    browser = await chromium.launch({ headless: true, args: ['--no-proxy-server'] });
    const page = await browser.newPage();
    page.setDefaultTimeout(1500);
    await page.goto(`http://127.0.0.1:${server.address().port}`);
    await page.waitForFunction(() => customElements.get('lv-login-page'));
    const value = input(); value.adminPassword = ' synthetic opaque password ';
    await fillAdminLogin(page, value);
    assert.equal(await page.getByLabel('Password', { exact: true }).inputValue(), value.adminPassword);
    assert.equal(await page.getByLabel('Email', { exact: true }).inputValue(), value.loginEmail);
    assert.equal(await page.getByLabel('Password', { exact: true }).getAttribute('type'), 'password');
    assert.equal(await page.getByRole('button', { name: 'Sign in', exact: true }).count(), 1);
    await page.close();
  } finally {
    await browser?.close();
    if (server) await new Promise(resolve => server.close(resolve));
    await rm(directory, { recursive: true, force: true });
  }
});
