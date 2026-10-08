import assert from 'node:assert/strict';
import test from 'node:test';
import { verifyRetainedProfile, verifyTermination } from './lifecycle-probe-policy.mjs';

const profile = () => ({ schemaVersion: 2, profiles: [{
  id: 'profile_' + '1'.repeat(32), canonicalOrigin: 'https://qualification.invalid',
  instanceId: 'instance_' + '2'.repeat(32), displayName: 'Disposable offline fixture',
  lastSafePath: '/dashboards/sales', partitionVersion: 1, label: 'acknowledged',
}] });

test('readback preserves exact saved instance, partition, route and acknowledged name', () => {
  verifyRetainedProfile(profile(), 'acknowledged');
  for (const [field, value] of Object.entries({ id: 'different', canonicalOrigin: 'https://elsewhere.invalid',
    instanceId: 'different', displayName: 'different', lastSafePath: '/', partitionVersion: 2, label: 'old' })) {
    const valueWithDrift = profile();
    valueWithDrift.profiles[0][field] = value;
    assert.throws(() => verifyRetainedProfile(valueWithDrift, 'acknowledged'), undefined, field);
  }
  const changed = profile();
  changed.profiles.push(structuredClone(changed.profiles[0]));
  assert.throws(() => verifyRetainedProfile(changed, 'acknowledged'));
});

test('forced cleanup cannot masquerade as graceful shutdown or induced SIGKILL', () => {
  verifyTermination({ code: 0, signal: null }, 'graceful');
  verifyTermination({ code: null, signal: 'SIGKILL' }, 'crash');
  for (const mode of ['graceful', 'crash']) {
    assert.throws(() => verifyTermination({ code: 1, signal: null }, mode));
    assert.throws(() => verifyTermination({ code: null, signal: 'SIGTERM' }, mode));
  }
  assert.throws(() => verifyTermination({ code: null, signal: 'SIGKILL' }, 'graceful'));
});
