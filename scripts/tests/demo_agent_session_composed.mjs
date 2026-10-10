// Runs the production transport against the real composed Go HTTP router.
// All session/provider credentials in this fixture are synthetic and stdin-only.
import assert from 'node:assert/strict';
import { pathToFileURL } from 'node:url';
let raw = '';
for await (const chunk of process.stdin) raw += chunk;
const fixture = JSON.parse(raw);
const { browserClient, performAgentCredentialTransition } = await import(pathToFileURL(fixture.driver).href);
const nativeFetch = globalThis.fetch;
const mutations = [];
globalThis.document = { querySelector: () => ({ content: fixture.csrf }) };
globalThis.fetch = async (path, options) => {
  const headers = new Headers(options.headers);
  headers.set('Cookie', fixture.cookies);
  headers.set('Origin', fixture.origin);
  if (fixture.failure === 'csrf') headers.delete('X-CSRF-Token');
  if (fixture.failure === 'claim') headers.delete('X-LeapView-Operation-ID');
  if (fixture.failure === 'etag') headers.set('If-Match', '"stale"');
  const response = await nativeFetch(new URL(path, fixture.origin), { ...options, headers });
  if (options.method === 'PATCH') mutations.push({ path, status: response.status });
  return response;
};
const page = { evaluate: (callback, args) => callback(args) };
try {
  const publicMe = await nativeFetch(`${fixture.origin}/api/v1/me`, { headers: { Cookie: fixture.cookies } });
  assert.equal(publicMe.status, 401);
  assert.equal((await publicMe.json()).code, 'BEARER_REQUIRED');
  const client = browserClient(page);
  if (fixture.failure === 'auth') {
    const denied = await nativeFetch(`${fixture.origin}/updates?route=admin&section=profile`,
      { headers: { Cookie: fixture.cookies, Accept: 'text/event-stream' }, redirect: 'manual' });
    assert.equal(denied.status, 401);
    await denied.body?.cancel();
  }
  if (fixture.mode === 'profile') {
    const me = await client.get('/api/v1/me');
    if (me.status === 401 && me.body?.code === 'BEARER_REQUIRED') process.stderr.write('COOKIE_PROFILE_BEARER_REQUIRED\n');
    assert.equal(me.status, 200);
    assert.equal(me.body.id, fixture.input.intent.actorId);
    assert.equal(me.body.sidebarPrincipalId, fixture.input.intent.actorId);
    assert.equal(me.body.identitySource, 'local');
    assert.equal(me.body.hasLocalPassword, true);
  } else {
    let tests = 0;
    const operation = performAgentCredentialTransition({ input: fixture.input, client,
      emit: marker => { assert.equal(marker, 'TEST_PASSED'); tests++; },
      waitForSave: async () => ({ action: 'save', operationDigest: fixture.input.operationDigest }) });
    if (fixture.failure) {
      await assert.rejects(operation);
      assert.equal(tests, 0);
      // VerifyClaim is wrapped as the generated INVALID_AGENT_CONFIG failure,
      // whose canonical contract is 422; CSRF and concurrency have own guards.
      const status = { csrf: 403, claim: 422, etag: 412 }[fixture.failure];
      assert.deepEqual(mutations, status ? [{ path: '/admin/agent/config', status }] : []);
    } else {
      await operation; assert.equal(tests, 1);
      assert.deepEqual(mutations, [{ path: '/admin/agent/config', status: 200 }, { path: '/admin/agent/config', status: 200 }]);
    }
  }
  process.stdout.write('COMPOSED_SESSION_PASSED\n');
} catch {
  // Never print captured responses, cookies, input, or dependency exceptions.
  process.stderr.write('COMPOSED_SESSION_FAILED\n');
  process.exitCode = 1;
}
