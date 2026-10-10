// Private, two-phase authenticated recovery. Credentials and validation receipts
// stay in process memory; stdout contains only fixed phase acknowledgments.
import { randomBytes } from 'node:crypto';
import { isIP } from 'node:net';
import { pathToFileURL } from 'node:url';

const origin = 'https://demo.leapview.dev';
const digest = /^sha256:[0-9a-f]{64}$/;
const failure = () => new Error('Agent credential transition failed.');
const require = condition => { if (!condition) throw failure(); };
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
const exactFields = (value, fields) => object(value) && Object.keys(value).sort().join(',') === [...fields].sort().join(',');
const bounded = (value, maximum) => typeof value === 'string' && value.length > 0 && Buffer.byteLength(value) <= maximum && value.trim() === value && !/[\x00\r\n\t]/.test(value);

export function validatePrivateInput(input) {
  require(exactFields(input, ['intent', 'operationDigest', 'candidateRevision', 'loginEmail', 'adminPassword', 'apiKey']));
  require(typeof input.candidateRevision === 'string' && /^[0-9a-f]{40}$/.test(input.candidateRevision));
  const intent = input.intent;
  require(exactFields(intent, ['reference', 'transitionVersion', 'fileDigest', 'installationId', 'instanceId', 'customerOwnerId', 'expectedRevision', 'actorId', 'provider', 'providerAddress']));
  require(digest.test(input.operationDigest) && digest.test(intent.fileDigest));
  require(/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(intent.reference));
  require(/^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(intent.transitionVersion));
  for (const name of ['installationId', 'instanceId', 'customerOwnerId', 'actorId']) require(bounded(intent[name], 255));
  require(Number.isSafeInteger(intent.expectedRevision) && intent.expectedRevision > 0 && intent.expectedRevision < Number.MAX_SAFE_INTEGER);
  require(isIP(intent.providerAddress) === 4);
  require(bounded(input.loginEmail, 255) && typeof input.adminPassword === 'string' &&
    input.adminPassword.length > 0 && Buffer.byteLength(input.adminPassword) <= 1024 && bounded(input.apiKey, 16384));
  const settings = intent.provider;
  require(exactFields(settings, ['enabled', 'model', 'baseUrl', 'apiMode', 'reasoningEffort']));
  require(settings.enabled === true && bounded(settings.model, 256) && bounded(settings.baseUrl, 2048));
  require(['responses', 'chat-completions'].includes(settings.apiMode));
  require(typeof settings.reasoningEffort === 'string' && ['', 'none', 'low', 'medium', 'high', 'xhigh', 'max'].includes(settings.reasoningEffort));
  if (settings.apiMode === 'chat-completions') require(settings.model.toLowerCase().startsWith('deepseek-v4') ? settings.reasoningEffort === 'none' : settings.reasoningEffort === '');
  let endpoint;
  try { endpoint = new URL(settings.baseUrl); } catch { throw failure(); }
  require(endpoint.protocol === 'https:' && !endpoint.username && !endpoint.password && !endpoint.search && !endpoint.hash && (!endpoint.port || endpoint.port === '443') && !isIP(endpoint.hostname));
  return input;
}

function settingsMatch(details, settings) {
  return details.enabled === settings.enabled && details.model === settings.model && details.baseUrl === settings.baseUrl &&
    details.apiMode === settings.apiMode && (details.reasoningEffort ?? '') === settings.reasoningEffort;
}

async function identity(client, input) {
  const me = await client.get('/api/v1/me');
  const instance = await client.get('/api/v1/instance/system');
  require(me.status === 200 && me.body?.id === input.intent.actorId && me.body?.sidebarPrincipalId === input.intent.actorId &&
    me.body?.identitySource === 'local' && me.body?.hasLocalPassword === true &&
    me.body?.email?.toLowerCase() === input.loginEmail.toLowerCase());
  require(instance.status === 200 && instance.body?.instanceId === input.intent.instanceId && instance.body?.canonicalOrigin === origin &&
    instance.body?.build?.revision === input.candidateRevision && instance.body?.build?.dirty === false);
}

async function current(client, input, allowCommitted = false) {
  const result = await client.get('/api/v1/agent/config');
  const details = result.body;
  require(result.status === 200 && bounded(result.etag, 1024) && object(details) &&
    details.adminManaged === true && details.configurationAvailable === true && settingsMatch(details, input.intent.provider));
  const expected = input.intent.expectedRevision;
  require(details.configurationRevision === expected || (allowCommitted && details.configurationRevision === expected + 1));
  if (details.configurationRevision === expected) require(details.configured === false && details.credentialConfigured === false && details.status === 'degraded' && !details.credentialVersionId);
  return result;
}

export async function performAgentCredentialTransition({ input, client, waitForSave, emit, checkCancelled = () => {} }) {
  validatePrivateInput(input);
  checkCancelled();
  await identity(client, input);
  let before = await current(client, input);
  const provider = { ...input.intent.provider, apiKey: input.apiKey, removeKey: false };
  const expectedRevision = input.intent.expectedRevision;
  checkCancelled();
  const tested = await client.patch('/api/v1/agent/config', { action: 'test', provider, expectedRevision }, before.etag);
  require(tested.status === 200 && bounded(tested.body?.testToken, 65536) &&
    tested.body?.configurationRevision === expectedRevision && tested.body?.configured === false &&
    tested.body?.credentialConfigured === false && settingsMatch(tested.body, input.intent.provider));
  before = await current(client, input);
  require(before.etag === tested.etag);
  checkCancelled();
  const saveCommand = Promise.resolve(waitForSave());
  void saveCommand.catch(() => {});
  emit('TEST_PASSED');
  const command = await saveCommand;
  require(exactFields(command, ['action', 'operationDigest']) && command.action === 'save' && command.operationDigest === input.operationDigest);
  const save = { action: 'save', provider, expectedRevision, testToken: tested.body.testToken };
  // Each retry asks the product to recover this same immutable activation. A
  // matching revision or settings never substitutes for a successful Save.
  for (let attempt = 0; attempt < 3; attempt++) {
    checkCancelled();
    await identity(client, input);
    before = await current(client, input, attempt > 0);
    let saved;
    checkCancelled();
    try { saved = await client.patch('/api/v1/agent/config', save, before.etag); }
    catch { if (attempt === 2) throw failure(); continue; }
    if ([412, 500, 502, 503, 504].includes(saved.status) && attempt < 2) continue;
    require(saved.status === 200 && saved.body?.configurationRevision === expectedRevision + 1 &&
      saved.body?.configured === true && saved.body?.credentialConfigured === true && saved.body?.status === 'enabled' &&
      bounded(saved.body?.credentialVersionId, 255) && settingsMatch(saved.body, input.intent.provider));
    const after = await current(client, input, true);
    require(after.body.configurationRevision === expectedRevision + 1 && after.body.configured === true &&
      after.body.credentialConfigured === true && after.body.status === 'enabled' &&
      after.body.credentialVersionId === saved.body.credentialVersionId && after.etag === saved.etag);
    return;
  }
  throw failure();
}

function requestID() {
  const bytes = randomBytes(16);
  const timestamp = BigInt(Date.now());
  for (let index = 5; index >= 0; index--) bytes[index] = Number((timestamp >> BigInt((5 - index) * 8)) & 255n);
  bytes[6] = (bytes[6] & 15) | 112;
  bytes[8] = (bytes[8] & 63) | 128;
  const value = bytes.toString('hex');
  return `${value.slice(0, 8)}-${value.slice(8, 12)}-${value.slice(12, 16)}-${value.slice(16, 20)}-${value.slice(20)}`;
}

export function browserClient(page) {
  const sections = { '/api/v1/me': 'profile', '/api/v1/instance/system': 'system', '/api/v1/agent/config': 'agent' };
  async function request(method, path, data, etag) {
    require(method === 'GET' ? Object.hasOwn(sections, path) : path === '/api/v1/agent/config');
    const section = method === 'GET' ? sections[path] : undefined;
    const result = await page.evaluate(async ({ method, section, data, etag, requestID }) => {
      const reject = () => { throw new Error('Private response rejected.'); };
      const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);
      const text = value => typeof value === 'string' && value.length > 0;
      const token = document.querySelector('meta[name="csrf-token"]')?.content?.trim();
      if (method !== 'GET' && !token) throw new Error('Private request rejected.');
      const controller = new AbortController();
      let expire;
      const expired = new Promise(resolve => { expire = resolve; });
      const timer = setTimeout(() => { controller.abort(); expire(); }, method === 'GET' ? 15000 : 120000);
      let reader;
      try {
        const response = await fetch(method === 'GET' ? `/updates?route=admin&section=${section}` : '/admin/agent/config',
          { method, credentials: 'same-origin', redirect: 'error', cache: 'no-store',
            headers: { Accept: method === 'GET' ? 'text/event-stream' : 'application/json',
              ...(method === 'GET' ? {} : { 'Content-Type': 'application/json', 'Cache-Control': 'no-store, no-transform', 'X-CSRF-Token': token,
                'If-Match': etag, 'X-LeapView-Operation-ID': 'updateAgentConfig', 'X-Request-ID': requestID }) },
            ...(method === 'GET' ? {} : { body: JSON.stringify({ adminAgentCommand: data }) }), signal: controller.signal });
        reader = response.body?.getReader();
        if (!reader) reject();
        if (method === 'GET' && response.status !== 200) return { status: response.status, body: {} };
        if (method === 'GET' && response.headers.get('Content-Type')?.split(';')[0].trim() !== 'text/event-stream') reject();
        let size = 0;
        const decoder = new TextDecoder('utf-8', { fatal: true });
        let raw = '';
        function project(event) {
          const lines = event.split('\n');
          const events = lines.filter(line => line.startsWith('event:')).map(line => line.slice(6).trim());
          if (events.length === 0) return;
          if (events.length !== 1) reject();
          if (events[0] !== 'datastar-patch-signals') return;
          const signals = lines.filter(line => line.startsWith('data: signals '));
          if (signals.length !== 1) reject();
          const patch = JSON.parse(signals[0].slice(14));
          if (!object(patch) || patch.page?.kind !== 'admin' || patch.page?.active !== section || patch.runtime?.kind !== 'admin') reject();
          if (section === 'profile') {
            const profile = patch.personalSettings?.profile;
            const sidebarPrincipalId = patch.chrome?.sidebar?.principalId;
            if (patch.personalSettings?.active !== 'profile' || !object(profile) || !text(profile.id) || !text(profile.email) ||
                !text(sidebarPrincipalId) || !text(profile.identitySource) || typeof profile.hasLocalPassword !== 'boolean') reject();
            return { status: 200, body: { id: profile.id, email: profile.email, sidebarPrincipalId,
              identitySource: profile.identitySource, hasLocalPassword: profile.hasLocalPassword } };
          }
          if (section === 'system') {
            const system = patch.productSettings?.system;
            if (patch.productSettings?.active !== 'system' || !object(system) || !text(system.instanceId) || !text(system.canonicalOrigin) ||
                !object(system.build) || !text(system.build.revision) || typeof system.build.dirty !== 'boolean' || typeof system.build.development !== 'boolean') reject();
            return { status: 200, body: { instanceId: system.instanceId, canonicalOrigin: system.canonicalOrigin,
              build: { revision: system.build.revision, dirty: system.build.dirty, development: system.build.development } } };
          }
          const agent = patch.page.agent;
          if (!object(agent) || !text(agent.revision) || agent.revision.length > 1024 ||
              !Number.isSafeInteger(agent.configurationRevision) || typeof agent.configurationAvailable !== 'boolean' ||
              typeof agent.adminManaged !== 'boolean' || typeof agent.configured !== 'boolean' ||
              typeof agent.credentialConfigured !== 'boolean' || typeof agent.enabled !== 'boolean' || !text(agent.status)) reject();
          return { status: 200, etag: agent.revision, body: {
            enabled: agent.enabled, model: agent.model, baseUrl: agent.baseUrl, apiMode: agent.apiMode,
            reasoningEffort: agent.reasoningEffort, revision: agent.revision, configurationRevision: agent.configurationRevision,
            adminManaged: agent.adminManaged, configurationAvailable: agent.configurationAvailable,
            configured: agent.configured, credentialConfigured: agent.credentialConfigured,
            status: agent.status, credentialVersionId: agent.credentialVersionId,
          } };
        }
        while (true) {
          const chunk = await reader.read();
          if (chunk.done) {
            if (method === 'GET') reject();
            break;
          }
          size += chunk.value.byteLength;
          if (size > 1048576) reject();
          raw += decoder.decode(chunk.value, { stream: true });
          if (method === 'GET') {
            // Normalize complete CRLF pairs only; a split trailing CR stays
            // buffered until the next chunk arrives.
            raw = raw.replace(/\r\n/g, '\n');
            let boundary;
            while ((boundary = raw.indexOf('\n\n')) >= 0) {
              const event = raw.slice(0, boundary); raw = raw.slice(boundary + 2);
              const result = project(event);
              if (result) return result;
            }
          }
        }
        raw += decoder.decode();
        return { status: response.status, etag: response.headers.get('ETag'), body: JSON.parse(raw) };
      } finally {
        controller.abort();
        try {
          if (reader) {
            const cancelled = reader.cancel().then(() => true, error => {
              if (error?.name !== 'AbortError') reject();
              return true;
            });
            if (!await Promise.race([cancelled, expired.then(() => false)])) reject();
          }
        } finally { reader?.releaseLock(); clearTimeout(timer); }
      }
    }, { method, section, data, etag, requestID: requestID() });
    return result;
  }
  return { get: path => request('GET', path), patch: (path, body, etag) => request('PATCH', path, body, etag) };
}

export async function fillAdminLogin(page, input) {
  await page.getByLabel('Email', { exact: true }).fill(input.loginEmail);
  await page.getByLabel('Password', { exact: true }).fill(input.adminPassword);
}

function canonicalURL(value) {
  try {
    const url = new URL(value);
    return url.origin === origin && !url.username && !url.password ? url : undefined;
  } catch { return undefined; }
}

export async function createPrivateBrowserContext(browser) {
  const context = await browser.newContext({ serviceWorkers: 'block' });
  await context.route('**/*', route => canonicalURL(route.request().url()) ? route.continue() : route.abort());
  await context.routeWebSocket('**/*', socket => socket.close());
  return context;
}

export async function openPrivateLogin(page, input) {
  const login = await page.goto(`${origin}/login`);
  require(login?.status() === 200 && canonicalURL(page.url())?.pathname === '/login');
  await fillAdminLogin(page, input);
}

export async function finishPrivateDriver({ context, browser, signal, channel, emit }) {
  await context.close();
  await browser.close();
  require(!signal.aborted);
  channel.finish();
  emit('SAVE_PASSED');
}

export function privateInputChannel(stream, abort) {
  let buffer = '';
  let awaiting;
  let failed;
  let complete = false;
  function reject() {
    if (complete || failed) return;
    failed = failure();
    awaiting?.reject(failed); awaiting = undefined;
    abort();
  }
  stream.setEncoding('utf8');
  stream.on('data', chunk => {
    buffer += chunk;
    if (Buffer.byteLength(buffer) > 131072) { reject(); return; }
    let newline;
    while ((newline = buffer.indexOf('\n')) >= 0) {
      const raw = buffer.slice(0, newline); buffer = buffer.slice(newline + 1);
      if (!awaiting) { reject(); return; }
      const waiting = awaiting; awaiting = undefined;
      try { waiting.resolve(JSON.parse(raw)); } catch { waiting.reject(failure()); reject(); }
    }
  });
  stream.on('end', reject); stream.on('error', reject);
  return {
    next() {
      if (failed || awaiting || complete) return Promise.reject(failure());
      return new Promise((resolve, reject) => { awaiting = { resolve, reject }; });
    },
    cancel: reject,
    finish() { complete = true; buffer = ''; stream.destroy(); },
  };
}

async function main() {
  // Strip Playwright debug hooks before importing/launching it. No tracing,
  // screenshots, persistent browser profile or credential environment is used.
  delete process.env.DEBUG; delete process.env.PWDEBUG;
  const args = process.argv.slice(2);
  require(args.length === 4 && args[0] === '--base-url' && args[1] === origin && args[2] === '--proxy-url' && /^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/.test(args[3]));
  require(Number(new URL(args[3]).port) <= 65535);
  let browser;
  let context;
  const abort = new AbortController();
  const channel = privateInputChannel(process.stdin, () => abort.abort());
  const cancel = () => channel.cancel();
  abort.signal.addEventListener('abort', () => { void browser?.close().catch(() => {}); }, { once: true });
  process.once('SIGTERM', cancel); process.once('SIGINT', cancel);
  const timer = setTimeout(cancel, 180000);
  try {
    const input = validatePrivateInput(await channel.next());
    require(!abort.signal.aborted);
    const { chromium } = await import('@playwright/test');
    require(!abort.signal.aborted);
    browser = await chromium.launch({ headless: true, proxy: { server: args[3], bypass: '<-loopback>' } });
    require(!abort.signal.aborted);
    context = await createPrivateBrowserContext(browser);
    const page = await context.newPage();
    page.setDefaultTimeout(30000);
    await openPrivateLogin(page, input);
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.waitForURL(url => url.origin === origin && url.pathname !== '/login');
    const settings = await page.goto(`${origin}/admin/agent`);
    require(settings?.status() === 200 && canonicalURL(page.url())?.pathname === '/admin/agent');
    await performAgentCredentialTransition({ input, client: browserClient(page),
      waitForSave: () => channel.next(), checkCancelled: () => require(!abort.signal.aborted), emit: marker => {
        require(!abort.signal.aborted);
        // The save waiter was installed before advertising Test completion.
        process.stdout.write(`${marker}\n`);
      } });
    require(!abort.signal.aborted);
    await finishPrivateDriver({ context, browser, signal: abort.signal, channel,
      emit: marker => process.stdout.write(`${marker}\n`) });
    context = undefined; browser = undefined;
  } finally {
    clearTimeout(timer);
    process.removeListener('SIGTERM', cancel); process.removeListener('SIGINT', cancel);
    await context?.close().catch(() => {});
    await browser?.close().catch(() => {});
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(() => { process.stderr.write('Agent credential transition failed.\n'); process.exitCode = 1; process.stdin.destroy(); });
}
