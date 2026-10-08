import { isDeepStrictEqual } from 'node:util';

export function verifyRetainedProfile(document, label) {
  const expected = { schemaVersion: 2, profiles: [{
    id: 'profile_' + '1'.repeat(32), canonicalOrigin: 'https://qualification.invalid',
    instanceId: 'instance_' + '2'.repeat(32), displayName: 'Disposable offline fixture',
    lastSafePath: '/dashboards/sales', partitionVersion: 1, label,
  }] };
  if (!isDeepStrictEqual(document, expected)) {
    throw new Error('retained profile identity, route, partition or acknowledged label changed');
  }
}

export function verifyTermination(exit, termination) {
  if (!['graceful', 'crash'].includes(termination) || !exit ||
      (termination === 'graceful' && (exit.code !== 0 || exit.signal !== null)) ||
      (termination === 'crash' && exit.signal !== 'SIGKILL')) {
    throw new Error('application termination differs');
  }
}
