import assert from 'node:assert/strict';
import test from 'node:test';
import { runInNewContext } from 'node:vm';
import { inspectTrustedShell } from './lifecycle-probe-cdp.mjs';
import * as cdp from './lifecycle-probe-cdp.mjs';

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

test('rename navigation retires the old context without replacing successful submission', async () => {
  let context = 0;
  const released = [];
  const call = async (method, params) => {
    if (method === 'Runtime.evaluate') return { result: { objectId: `window-${context}` } };
    if (method === 'Runtime.releaseObject') {
      released.push(params.objectId);
      if (params.objectId !== `window-${context}`) throw new Error('retired context');
      return {};
    }
    assert.equal(params.objectId, `window-${context}`);
    if (params.functionDeclaration.includes('requestSubmit()')) context++;
    return { result: { value: true } };
  };
  assert.equal(await inspectTrustedShell(call, 'rename', ['renamed']), true);
  assert.equal(await inspectTrustedShell(call, 'acknowledged', ['renamed']), true);
  assert.deepEqual(released, ['window-0', 'window-1']);
});

test('object cleanup cannot mask the primary command failure or turn false into success', async () => {
  const primary = new Error('primary probe failure');
  const call = async method => {
    if (method === 'Runtime.evaluate') return { result: { objectId: 'window' } };
    if (method === 'Runtime.callFunctionOn') throw primary;
    throw new Error('cleanup failure');
  };
  await assert.rejects(inspectTrustedShell(call, 'rename', ['renamed']), error => error === primary);
  assert.equal(await inspectTrustedShell(async (method, params) => {
    if (method === 'Runtime.callFunctionOn') return { result: { value: false } };
    return call(method, params);
  }, 'acknowledged', ['renamed']), false);
});

test('rejected commands expose only a known method and bounded numeric code', () => {
  const error = cdp.debugCommandError('Runtime.releaseObject', {
    code: -32000, message: 'private URL and profile', data: { password: 'private credential' },
  });
  assert.equal(error.message, 'debug command rejected (Runtime.releaseObject; code -32000)');
  assert.equal(error.cause, undefined);
  assert.equal(JSON.stringify(error), '{}');
  for (const code of ['private credential', -Infinity, 1.5, 2 ** 32, null]) {
    assert.equal(cdp.debugCommandError('private method payload', { code }).message,
      'debug command rejected (unknown; code unknown)');
  }
});
