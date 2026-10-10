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
  let handler, websocket, registered;
  const registration = new Promise(resolve => { registered = resolve; });
  const context = { async route(pattern, callback) { assert.equal(pattern, '**/*'); handler = callback; },
    async routeWebSocket(pattern, callback) { assert.equal(pattern, '**/*'); websocket = callback; await registration; } };
  const browser = { async newContext(options) { assert.deepEqual(options, { serviceWorkers: 'block' }); return context; } };
  let ready = false;
  const pending = createPrivateBrowserContext(browser).then(value => { ready = true; return value; });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(ready, false);
  registered();
  assert.equal(await pending, context);
  for (const url of ['wss://demo.leapview.dev/socket', 'wss://other.example/socket', 'ws://127.0.0.1/socket']) {
    let closed = false;
    await websocket({ url: () => url, close: () => { closed = true; }, connectToServer: () => assert.fail('private websocket connected upstream') });
    assert.equal(closed, true);
  }
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
  candidateRevision: 'd'.repeat(40),
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
      if (path === '/api/v1/me') return { status: 200, body: { id: options.actor ?? privateInput.intent.actorId, email: privateInput.loginEmail,
        sidebarPrincipalId: options.sidebarActor ?? privateInput.intent.actorId, identitySource: options.identitySource ?? 'local', hasLocalPassword: options.hasLocalPassword ?? true } };
      if (path === '/api/v1/instance/system') return { status: 200, body: { instanceId: options.instance ?? privateInput.intent.instanceId, canonicalOrigin: 'https://demo.leapview.dev',
        build: { revision: options.revision ?? privateInput.candidateRevision, dirty: options.dirty ?? false, development: options.development ?? false } } };
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

test('browser session transport preserves CSRF, ETag, session and operation claims', async () => {
  let parameters;
  const page = { async evaluate(callback, args) {
    parameters = args;
    const document = globalThis.document, fetch = globalThis.fetch;
    try {
      globalThis.document = { querySelector: () => ({ content: 'csrf-session-token' }) };
      globalThis.fetch = async (path, options) => {
        assert.equal(path, '/admin/agent/config');
        assert.deepEqual(JSON.parse(options.body), { adminAgentCommand: body });
        assert.equal(options.credentials, 'same-origin');
        assert.equal(options.redirect, 'error');
        assert.equal(options.headers['X-CSRF-Token'], 'csrf-session-token');
        assert.equal(options.headers['Cache-Control'], 'no-store, no-transform');
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

for (const development of [true, false]) {
  test(`clean immutable candidate permits release development stamp ${development}`, async () => {
    const f = fixture({ development });
    await f.run();
    assert.deepEqual(f.markers, ['TEST_PASSED']);
    assert.deepEqual(f.calls.filter(call => call[0] === 'PATCH').map(call => call[2].action), ['test', 'save']);
  });
}

for (const options of [{ sidebarActor: 'other' }, { identitySource: 'oidc' }, { hasLocalPassword: false },
  { revision: 'e'.repeat(40) }, { dirty: true }]) {
  test(`session identity and immutable source reject before Test ${JSON.stringify(options)}`, async () => {
    const f = fixture(options);
    await assert.rejects(f.run());
    assert.equal(f.calls.filter(call => call[0] === 'PATCH').length, 0);
  });
}

test('private candidate source is mandatory and immutable', () => {
  for (const value of [undefined, '', 'main', 'd'.repeat(39), 'D'.repeat(40)]) {
    const malformed = input(); malformed.candidateRevision = value;
    assert.throws(() => validatePrivateInput(malformed));
  }
});

function signalEvent(patch) {
  return `event: datastar-patch-signals\ndata: signals ${JSON.stringify(patch)}\n\n`;
}

function bootstrap(section) {
  return { page: { kind: 'admin', active: section, ...(section === 'agent' ? { agent: {
    configured: false, enabled: true, status: 'degraded', configurationRevision: 2,
    configurationAvailable: true, adminManaged: true, credentialConfigured: false, revision: '"exact-config"',
  } } : {}) }, runtime: { kind: 'admin' }, chrome: { sidebar: { principalId: 'principal_admin' } },
  ...(section === 'profile' ? { personalSettings: { active: 'profile', profile: { id: 'principal_admin', email: 'admin@example.test', identitySource: 'local', hasLocalPassword: true, displayName: 'ignored' } } } : {}),
  ...(section === 'system' ? { productSettings: { active: 'system', system: { instanceId: 'instance_demo', canonicalOrigin: 'https://demo.leapview.dev', build: { revision: 'd'.repeat(40), dirty: false, development: false } } } } : {}) };
}

function evaluationPage(fetchResponse) {
  return { async evaluate(callback, args) {
    const document = globalThis.document, fetch = globalThis.fetch;
    try {
      globalThis.document = { querySelector: () => ({ content: 'csrf-session-token' }) };
      globalThis.fetch = fetchResponse;
      return await callback(args);
    } finally { globalThis.document = document; globalThis.fetch = fetch; }
  } };
}

for (const [logical, section] of [['/api/v1/me', 'profile'], ['/api/v1/instance/system', 'system'], ['/api/v1/agent/config', 'agent']]) {
  test(`session bootstrap ${section} uses fixed authorized route and projected metadata`, async () => {
    const patch = bootstrap(section);
    const page = evaluationPage(async (path, options) => {
      assert.equal(path, `/updates?route=admin&section=${section}`);
      assert.equal(options.headers.Accept, 'text/event-stream');
      assert.equal(options.credentials, 'same-origin');
      assert.equal(options.redirect, 'error');
      return new Response(signalEvent(patch), { headers: { 'Content-Type': 'text/event-stream' } });
    });
    const result = await browserClient(page).get(logical);
    assert.equal(result.status, 200);
    if (section === 'profile') assert.deepEqual(result.body, { id: 'principal_admin', email: 'admin@example.test', sidebarPrincipalId: 'principal_admin', identitySource: 'local', hasLocalPassword: true });
    if (section === 'system') assert.deepEqual(result.body, patch.productSettings.system);
    if (section === 'agent') assert.equal(result.etag, '"exact-config"');
  });
}

test('bootstrap reads fragmented CRLF UTF-8 and joins cancellation without waiting for stream EOF', async () => {
  const patch = bootstrap('profile'); patch.personalSettings.profile.email = 'admín@example.test';
  const encoded = new TextEncoder().encode((': keepalive\n\n' + signalEvent(patch)).replaceAll('\n', '\r\n'));
  let cancelled = false;
  const response = new Response(new ReadableStream({
    start(controller) { for (const byte of encoded) controller.enqueue(new Uint8Array([byte])); },
    async cancel() { await new Promise(resolve => setTimeout(resolve, 5)); cancelled = true; },
  }), { headers: { 'Content-Type': 'text/event-stream; charset=utf-8' } });
  const result = await browserClient(evaluationPage(async () => response)).get('/api/v1/me');
  assert.equal(result.body.email, 'admín@example.test');
  assert.equal(cancelled, true);
  assert.equal(response.body.locked, false);
});

test('session projections exclude instruction, tool, token and unrelated system payloads', async () => {
  for (const [section, path] of [['system', '/api/v1/instance/system'], ['agent', '/api/v1/agent/config']]) {
    const patch = bootstrap(section);
    if (section === 'system') {
      patch.productSettings.system.runtime = { privateExtra: 'excluded-runtime' };
      patch.productSettings.system.build.extra = 'excluded-build';
    } else Object.assign(patch.page.agent, { systemPrompt: 'excluded-instruction', tools: [{ extra: 'excluded-tool' }], testToken: 'excluded-token' });
    const response = new Response(signalEvent(patch), { headers: { 'Content-Type': 'text/event-stream' } });
    const result = await browserClient(evaluationPage(async () => response)).get(path);
    assert.equal(JSON.stringify(result).includes('excluded-'), false);
  }
});

for (const behavior of ['pending', 'rejected', 'abort-error']) test(`reader cleanup immediately aborts and bounds ${behavior} cancellation`, async () => {
  const original = globalThis.setTimeout;
  let deadline, signal, cancels = 0;
  globalThis.setTimeout = (callback, milliseconds) => { deadline = milliseconds; return original(callback, 5); };
  const response = new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode(signalEvent(bootstrap('profile')))); },
    cancel() {
      cancels++;
      assert.equal(signal.aborted, true);
      if (behavior === 'pending') return new Promise(() => {});
      const error = new Error('synthetic cancellation failure');
      if (behavior === 'abort-error') error.name = 'AbortError';
      return Promise.reject(error);
    },
  }), { headers: { 'Content-Type': 'text/event-stream' } });
  try {
    const operation = browserClient(evaluationPage(async (path, options) => { signal = options.signal; return response; })).get('/api/v1/me');
    if (behavior === 'abort-error') assert.equal((await operation).status, 200);
    else await assert.rejects(operation, error => error.message === 'Private response rejected.');
    assert.equal(deadline, 15000);
    assert.equal(cancels, 1);
    assert.equal(signal.aborted, true);
    assert.equal(response.body.locked, false);
  } finally { globalThis.setTimeout = original; }
});

for (const [name, change] of [
  ['wrong route', patch => { patch.page.kind = 'dashboard'; }],
  ['wrong section', patch => { patch.page.active = 'system'; }],
  ['wrong runtime', patch => { patch.runtime.kind = 'dashboard'; }],
  ['missing runtime', patch => { delete patch.runtime; }],
  ['missing identity', patch => { delete patch.personalSettings.profile.id; }],
  ['missing sidebar identity', patch => { delete patch.chrome.sidebar.principalId; }],
  ['missing password admission', patch => { delete patch.personalSettings.profile.hasLocalPassword; }],
]) test(`session bootstrap rejects ${name} and cancels the owned reader`, async () => {
  const patch = bootstrap('profile'); change(patch);
  let cancelled = false;
  const response = new Response(new ReadableStream({
    start(controller) { controller.enqueue(new TextEncoder().encode(signalEvent(patch))); },
    cancel() { cancelled = true; },
  }), { headers: { 'Content-Type': 'text/event-stream' } });
  await assert.rejects(browserClient(evaluationPage(async () => response)).get('/api/v1/me'));
  assert.equal(cancelled, true);
});

for (const [name, raw, type] of [
  ['malformed JSON', 'event: datastar-patch-signals\ndata: signals {\n\n', 'text/event-stream'],
  ['missing signal payload', 'event: datastar-patch-signals\ndata: {}\n\n', 'text/event-stream'],
  ['incomplete event', signalEvent(bootstrap('profile')).slice(0, -1), 'text/event-stream'],
  ['non-stream content type', signalEvent(bootstrap('profile')), 'application/json'],
  ['oversized stream', ':' + 'x'.repeat(1048576), 'text/event-stream'],
]) test(`bounded bootstrap rejects ${name}`, async () => {
  const response = new Response(raw, { headers: { 'Content-Type': type } });
  await assert.rejects(browserClient(evaluationPage(async () => response)).get('/api/v1/me'));
});

test('bootstrap deadline remains fifteen seconds and aborts/join-cleans a stalled reader', async () => {
  const original = globalThis.setTimeout;
  let deadline, aborted = false;
  globalThis.setTimeout = (callback, milliseconds) => { deadline = milliseconds; return original(callback, 5); };
  try {
    const page = evaluationPage(async (path, options) => new Response(new ReadableStream({
      start(controller) { options.signal.addEventListener('abort', () => { aborted = true; controller.error(new Error('synthetic abort')); }, { once: true }); },
    }), { headers: { 'Content-Type': 'text/event-stream' } }));
    await assert.rejects(browserClient(page).get('/api/v1/me'));
    assert.equal(deadline, 15000);
    assert.equal(aborted, true);
  } finally { globalThis.setTimeout = original; }
});

test('session transport rejects paths outside its fixed logical interface before browser evaluation', async () => {
  const client = browserClient({ evaluate: () => assert.fail('unknown path reached browser') });
  for (const path of ['/api/v1/me?other=1', '/updates?route=admin&section=principals', 'https://other.example/', '/api/v1/agent/config/']) {
    await assert.rejects(client.get(path));
    await assert.rejects(client.patch(path, {}, 'etag'));
  }
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
