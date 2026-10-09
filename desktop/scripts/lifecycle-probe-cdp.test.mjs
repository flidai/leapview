import assert from 'node:assert/strict';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import { inspectTrustedShell } from './lifecycle-probe-cdp.mjs';

test('labels travel as typed CDP arguments, never executable source', async () => {
  const hostile = '\"); globalThis.injected = true; //\n${danger}\u2028';
  const requests = [];
  const scope = { input: { value: '' }, submitted: false, injected: false };
  scope.document = { querySelector: () => ({ querySelector: () => scope.input, requestSubmit: () => { scope.submitted = true; } }) };
  const call = async (method, params) => {
    requests.push({ method, params });
    if (method === 'Runtime.evaluate') return { result: { objectId: 'trusted-window' } };
    if (method === 'Runtime.releaseObject') return {};
    assert.equal(params.objectId, 'trusted-window');
    assert.ok(!params.functionDeclaration.includes(hostile));
    scope.args = params.arguments.map(arg => arg.value);
    return { result: { value: runInNewContext(`(${params.functionDeclaration})(...args)`, scope) } };
  };
  assert.equal(await inspectTrustedShell(call, 'rename', [hostile]), true);
  assert.equal(scope.input.value, hostile);
  assert.equal(scope.submitted, true);
  assert.equal(scope.injected, false);
  assert.equal(requests[0].params.expression, 'globalThis');
  assert.deepEqual(requests[1].params.arguments, [{ value: hostile }]);
  assert.deepEqual(requests.at(-1), { method: 'Runtime.releaseObject', params: { objectId: 'trusted-window' } });
});

test('remote failures release the same session object and cannot report a successful probe', async () => {
  const methods = [];
  await assert.rejects(inspectTrustedShell(async (method) => {
    methods.push(method);
    if (method === 'Runtime.evaluate') return { result: { objectId: 'same-session' } };
    if (method === 'Runtime.callFunctionOn') return { exceptionDetails: {} };
    return {};
  }, 'read', ['profile', 'label']));
  assert.deepEqual(methods, ['Runtime.evaluate', 'Runtime.callFunctionOn', 'Runtime.releaseObject']);
  await assert.rejects(inspectTrustedShell(async () => ({ result: {} }), 'read', ['profile', 'label']));
  await assert.rejects(inspectTrustedShell(async () => { throw new Error('must not call'); }, 'unknown', []));
});
