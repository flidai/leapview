// Probe the installed archive through its real trusted UI and durable profile store.
// No credential, remote server, renderer privilege or packaged-code modification.
import { spawn } from 'node:child_process';
import { readFile, writeFile } from 'node:fs/promises';
import { createServer } from 'node:net';
import { join } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { debugCommandError, inspectTrustedShell } from './lifecycle-probe-cdp.mjs';
import { verifyRetainedProfile, verifyTermination } from './lifecycle-probe-policy.mjs';

const [executable, profile, expectedLabel, nextLabel, termination, output] = process.argv.slice(2);
if (!output || !['graceful', 'crash'].includes(termination) || process.platform !== 'linux' || process.getuid() === 0) {
  throw new Error('requires a nonroot Linux process and exact lifecycle probe arguments');
}
const expectedID = 'profile_' + '1'.repeat(32);
const server = createServer();
await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve); });
const port = server.address().port;
await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
const child = spawn(executable, ['--headless', '--disable-gpu', `--remote-debugging-port=${port}`,
  '--remote-debugging-address=127.0.0.1', `--user-data-dir=${profile}`],
{ detached: true, stdio: 'ignore', cwd: profile });
let exit;
const exited = new Promise((resolve, reject) => {
  child.once('error', reject);
  child.once('exit', (code, signal) => { exit = { code, signal }; resolve(); });
});
// Attach immediately so a startup error cannot become an unhandled rejection.
exited.catch(() => {});
const sockets = [];
let sequence = 0;
async function connect(url) {
  const address = new URL(url);
  if (address.protocol !== 'ws:' || address.hostname !== '127.0.0.1' || Number(address.port) !== port) {
    throw new Error('debug target escaped the disposable loopback process');
  }
  const socket = new WebSocket(url);
  sockets.push(socket);
  await Promise.race([new Promise((resolve, reject) => {
    socket.addEventListener('open', resolve, { once: true });
    socket.addEventListener('error', () => reject(new Error('debug transport failed')), { once: true });
  }), delay(3000).then(() => { throw new Error('debug transport timed out'); })]);
  return socket;
}
async function call(socket, method, params = {}) {
  const id = ++sequence;
  return new Promise((resolve, reject) => {
    const timeout = setTimeout(() => finish(new Error('debug command timed out')), 3000);
    function finish(error, result) {
      clearTimeout(timeout);
      socket.removeEventListener('message', message);
      socket.removeEventListener('close', closed);
      error ? reject(error) : resolve(result);
    }
    function closed() { finish(new Error('debug connection closed')); }
    function message(event) {
      let value;
      try { value = JSON.parse(event.data); } catch { finish(new Error('invalid debug response')); return; }
      if (value.id === id) finish(value.error ? debugCommandError(method, value.error) : null, value.result);
    }
    socket.addEventListener('message', message);
    socket.addEventListener('close', closed);
    socket.send(JSON.stringify({ id, method, params }));
  });
}
async function until(run, description) {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (exit) throw new Error('packaged application exited during probe');
    try { if (await run()) return; } catch {}
    await delay(100);
  }
  throw new Error(description);
}
async function json(path) {
  const response = await fetch(`http://127.0.0.1:${port}${path}`, { signal: AbortSignal.timeout(1000) });
  if (!response.ok) throw new Error('debug endpoint unavailable');
  return response.json();
}
function stopGroup(signal) {
  if (!child.pid) return;
  try { process.kill(-child.pid, signal); } catch (error) { if (error.code !== 'ESRCH') throw error; }
}
let receipt;
try {
  let target;
  await until(async () => {
    target = (await json('/json/list')).find(value => value.type === 'page' && value.url === 'leapview://app/');
    return Boolean(target?.webSocketDebuggerUrl);
  }, 'installed archive did not open its trusted shell');
  const socket = await connect(target.webSocketDebuggerUrl);
  const inspect = (action, values) => inspectTrustedShell((method, params) => call(socket, method, params), action, values);
  await until(async () => await inspect('read', [expectedID, expectedLabel]),
    'installed archive did not read the retained profile');
  if (nextLabel !== '-') {
    // Submit the same form a user submits. The renderer never writes profiles.json.
    await inspect('rename', [nextLabel]);
    await until(async () => await inspect('acknowledged', [nextLabel]),
      'trusted UI did not acknowledge its durable profile write');
  }
  const document = JSON.parse(await readFile(join(profile, 'profiles.json'), 'utf8'));
  verifyRetainedProfile(document, nextLabel === '-' ? expectedLabel : nextLabel);
  if (termination === 'crash') {
    stopGroup('SIGKILL');
  } else {
    const browser = await connect((await json('/json/version')).webSocketDebuggerUrl);
    // Browser.close may close the socket before its response; process exit is authority.
    await call(browser, 'Browser.close').catch(() => {});
  }
  await Promise.race([exited, delay(5000).then(() => { throw new Error('application did not stop'); })]);
  verifyTermination(exit, termination);
  receipt = { schemaVersion: 1, trustedShellReadback: true, acknowledgedWrite: nextLabel !== '-',
    durableReadback: true, termination, mainProcessExited: true };
} finally {
  for (const socket of sockets) socket.close();
  stopGroup('SIGKILL');
  await exited;
  let remaining = true;
  for (let attempt = 0; attempt < 50; attempt++) {
    try { process.kill(-child.pid, 0); } catch (error) {
      if (error.code !== 'ESRCH') throw error;
      remaining = false;
      break;
    }
    await delay(100);
  }
  if (remaining) throw new Error('Desktop process group did not finish cleanup');
}
receipt.processGroupStopped = true;
await writeFile(output, JSON.stringify(receipt) + '\n', { flag: 'wx', mode: 0o600 });
