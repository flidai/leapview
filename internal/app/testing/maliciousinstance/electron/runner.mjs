import assert from "node:assert/strict";

import { app, BrowserWindow, session } from "electron";

import {
  createObservationRecorder,
  fetchBoundedJSON,
  validateManifest,
} from "./manifest-proof.mjs";
import {
  configureRemoteSession,
  parseConfiguredOrigin,
} from "./policy.mjs";
import {
  createRemoteWindow,
} from "../../../../../desktop/src/security/remote-window.mjs";
import { writeJSONAtomic } from "./result-file.mjs";

app.enableSandbox();

const proofOrigin = parseConfiguredOrigin(process.env.LEAPVIEW_PROOF_ORIGIN, {
  allowLoopbackHTTP: true,
});
const resultPath = process.env.LEAPVIEW_PROOF_RESULT;
if (!resultPath) {
  throw new Error("LEAPVIEW_PROOF_RESULT is required");
}

const result = {
  passed: false,
  framework: `Electron ${process.versions.electron}`,
  chromium: process.versions.chrome,
  phase: "bootstrap",
  manifestVersion: null,
  observations: [],
  checks: [],
  decisions: [],
};

await writeResult();
app.whenReady().then(async () => {
  try {
    result.phase = "running";
    await withTimeout(runProof(), 40_000, "policy integration");
    result.passed = true;
    result.phase = "complete";
    await writeResult();
    app.quit();
  } catch (error) {
    await fail(error);
  }
}).catch(fail);

async function fail(error) {
  result.error = error instanceof Error ? error.message : String(error);
  result.phase = "failed";
  await writeResult();
  app.exit(1);
}

async function runProof() {
  result.currentCheck = "manifest";
  const manifest = validateManifest(
    await fetchBoundedJSON(`${proofOrigin}/__harness/manifest.json`),
  );
  result.manifestVersion = manifest.version;
  const observations = createObservationRecorder(manifest);

  const first = createProofRemoteWindow("leapview-profile-proof-a");
  const second = createProofRemoteWindow("leapview-profile-proof-b");

  try {
    await assertRendererBoundary(first.window);
    await observeNativeAttacks(first.window, observations);
    await observeNavigationAttacks(first.window, observations);
    await observePopupAttack(first.window, observations);
    await observeCrossOriginFrame(first.window, observations);
    await observePermissionAttacks(first.window, observations);
    await observeDownloadAttack(first.window, observations);
    await observeStorageIsolation(first, second, observations);
    await observeDiscoveryAttacks(observations);
    await observeRendererAvailability(first.window, observations);

    result.observations = observations.finalize();
    result.checks.push("manifest-complete");
  } finally {
    if (!first.window.isDestroyed()) {
      first.window.destroy();
    }
    if (!second.window.isDestroyed()) {
      second.window.destroy();
    }
  }
}

async function assertRendererBoundary(window) {
  result.currentCheck = "renderer-preferences";
  await load(window, `${proofOrigin}/`);
  const preferences = window.webContents.getLastWebPreferences();
  result.preferences = {
    nodeIntegration: preferences.nodeIntegration,
    nodeIntegrationInWorker: preferences.nodeIntegrationInWorker,
    nodeIntegrationInSubFrames: preferences.nodeIntegrationInSubFrames,
    contextIsolation: preferences.contextIsolation,
    sandbox: preferences.sandbox,
    webSecurity: preferences.webSecurity,
    webviewTag: preferences.webviewTag,
    hasPreload: Boolean(preferences.preload),
  };
  assert.equal(preferences.nodeIntegration, false, "nodeIntegration");
  assert.equal(preferences.nodeIntegrationInWorker ?? false, false, "nodeIntegrationInWorker");
  assert.equal(preferences.nodeIntegrationInSubFrames ?? false, false, "nodeIntegrationInSubFrames");
  assert.equal(preferences.contextIsolation, true, "contextIsolation");
  assert.equal(preferences.sandbox, true, "sandbox");
  assert.equal(preferences.webSecurity, true, "webSecurity");
  assert.equal(preferences.webviewTag ?? false, false, "webviewTag");
  assert.ok(!preferences.preload, "remote content must not receive a preload");
  result.checks.push("sandboxed-renderer-without-preload");
}

async function observeNativeAttacks(window, observations) {
  result.currentCheck = "native-globals";
  await load(window, `${proofOrigin}/attack/native.renderer-authority`);
  const globals = await inspectNativeGlobals(window.webContents);
  assert.deepEqual(globals, {
    nodeProcess: false,
    nodeRequire: false,
    nodeModule: false,
    electron: false,
  });
  observations.record("native.renderer-authority", "isolated");
}

async function observeNavigationAttacks(window, observations) {
  result.currentCheck = "navigation.cross-origin";
  await load(window, `${proofOrigin}/`);
  await load(window, `${proofOrigin}/attack/navigation.cross-origin`, {
    allowBlocked: true,
  });
  await delay(150);
  assert.equal(new URL(window.webContents.getURL()).origin, proofOrigin);
  assertDecision("main-frame-navigation");
  observations.record("navigation.cross-origin", "denied");

  for (const attackID of [
    "navigation.javascript",
    "navigation.data",
    "navigation.blob",
    "navigation.file",
    "scheme.custom",
    "scheme.deep-link-injection",
  ]) {
    result.currentCheck = attackID;
    await load(window, `${proofOrigin}/attack/${attackID}`);
    const before = window.webContents.getURL();
    await execute(window, `document.getElementById("trigger").click()`, true);
    await delay(150);
    assert.equal(window.webContents.getURL(), before, attackID);
    assert.equal(
      await execute(
        window,
        `sessionStorage.getItem("leapview.desktop.harness.triggered")`,
      ),
      attackID,
      `${attackID} trigger did not execute`,
    );
    if (attackID === "navigation.javascript") {
      assertNoNativeGlobals(await inspectNativeGlobals(window.webContents));
    }
    observations.record(attackID, "denied");
  }
}

async function observePopupAttack(window, observations) {
  result.currentCheck = "popup.cross-origin";
  await load(window, `${proofOrigin}/attack/popup.cross-origin`);
  const decisionsBefore = result.decisions.length;
  await execute(window, `document.getElementById("trigger").click()`, true);
  assert.ok(
    result.decisions.slice(decisionsBefore).some((decision) => decision.kind === "popup"),
    "popup did not reach the popup boundary",
  );
  observations.record("popup.cross-origin", "denied");
}

async function observeCrossOriginFrame(window, observations) {
  result.currentCheck = "frame.cross-origin";
  await load(window, `${proofOrigin}/attack/frame.cross-origin`);
  const frame = await waitFor(
    () => window.webContents.mainFrame.frames.find(
      (candidate) =>
        candidate.url !== "" &&
        new URL(candidate.url).origin !== proofOrigin,
    ),
    5_000,
    "cross-origin child frame",
  );
  const globals = await withTimeout(
    inspectNativeGlobals(frame),
    5_000,
    "cross-origin frame inspection",
  );
  assertNoNativeGlobals(globals);
  observations.record("frame.cross-origin", "isolated");
}

async function observePermissionAttacks(window, observations) {
  const permissionScripts = new Map([
    ["permission.camera", `navigator.mediaDevices?.getUserMedia
      ? navigator.mediaDevices.getUserMedia({video:true}).then(() => "granted", () => "denied")
      : Promise.resolve("unavailable")`],
    ["permission.microphone", `navigator.mediaDevices?.getUserMedia
      ? navigator.mediaDevices.getUserMedia({audio:true}).then(() => "granted", () => "denied")
      : Promise.resolve("unavailable")`],
    ["permission.geolocation", `navigator.geolocation
      ? Promise.race([
          new Promise((resolve) => navigator.geolocation.getCurrentPosition(
            () => resolve("granted"),
            () => resolve("denied"),
          )),
          new Promise((resolve) => setTimeout(() => resolve("timeout"), 1500)),
        ])
      : Promise.resolve("unavailable")`],
    ["permission.notifications", `typeof Notification !== "undefined"
      ? Notification.requestPermission()
      : Promise.resolve("unavailable")`],
    ["permission.clipboard-read", `navigator.clipboard?.readText
      ? navigator.clipboard.readText().then(() => "granted", () => "denied")
      : Promise.resolve("unavailable")`],
  ]);

  for (const [attackID, script] of permissionScripts) {
    result.currentCheck = attackID;
    await load(window, `${proofOrigin}/attack/${attackID}`);
    const outcome = await execute(window, script, true);
    assert.notEqual(outcome, "granted", `${attackID} was granted`);
    observations.record(attackID, "denied");
  }
  assert.ok(
    result.decisions.some(
      (decision) =>
        decision.kind === "permission-request" ||
        decision.kind === "permission-check",
    ),
    "no permission request reached the deny-by-default session boundary",
  );
}

async function observeDownloadAttack(window, observations) {
  result.currentCheck = "download.hostile-filename";
  const decisionsBefore = result.decisions.length;
  await load(window, `${proofOrigin}/attack/download.hostile-filename`, {
    allowBlocked: true,
  });
  await delay(150);
  assert.ok(
    result.decisions.slice(decisionsBefore).some((decision) => decision.kind === "download"),
    "download did not reach the session boundary",
  );
  observations.record("download.hostile-filename", "denied");
}

async function observeStorageIsolation(first, second, observations) {
  result.currentCheck = "storage.cross-profile";
  const key = "leapview.desktop.harness.cross-profile";
  const databaseName = "leapview-desktop-proof";
  const cacheName = "leapview-desktop-proof";
  const cookieName = "leapview-desktop-proof";
  await load(first.window, `${proofOrigin}/attack/storage.cross-profile`);
  assert.equal(await execute(first.window, `localStorage.getItem(${JSON.stringify(key)})`), "present");
  await seedPartitionState(first, "first", {
    databaseName,
    cacheName,
    cookieName,
  });

  await load(second.window, `${proofOrigin}/`);
  assert.equal(await execute(second.window, `localStorage.getItem(${JSON.stringify(key)})`), null);
  await assertPartitionStateMissing(second, {
    databaseName,
    cacheName,
    cookieName,
  });
  await seedPartitionState(second, "second", {
    databaseName,
    cacheName,
    cookieName,
  });

  await first.remoteSession.clearStorageData();
  await first.remoteSession.clearCache();
  await first.remoteSession.clearAuthCache();
  first.remoteSession.flushStorageData();
  await load(first.window, `${proofOrigin}/`);
  assert.equal(await execute(first.window, `localStorage.getItem(${JSON.stringify(key)})`), null);
  await assertPartitionStateMissing(first, {
    databaseName,
    cacheName,
    cookieName,
  });
  await assertPartitionState(second, "second", {
    databaseName,
    cacheName,
    cookieName,
  });
  observations.record("storage.cross-profile", "isolated");
}

async function seedPartitionState(
  target,
  value,
  { databaseName, cacheName, cookieName },
) {
  await target.remoteSession.cookies.set({
    url: proofOrigin,
    name: cookieName,
    value,
    sameSite: "lax",
  });
  await execute(target.window, `(async () => {
    localStorage.setItem(${JSON.stringify("leapview.desktop.partition-value")}, ${JSON.stringify(value)});
    await new Promise((resolve, reject) => {
      const request = indexedDB.open(${JSON.stringify(databaseName)}, 1);
      request.onupgradeneeded = () => request.result.createObjectStore("proof");
      request.onerror = () => reject(request.error);
      request.onsuccess = () => {
        const transaction = request.result.transaction("proof", "readwrite");
        transaction.objectStore("proof").put(${JSON.stringify(value)}, "value");
        transaction.oncomplete = () => {
          request.result.close();
          resolve();
        };
        transaction.onerror = () => reject(transaction.error);
      };
    });
    const cache = await caches.open(${JSON.stringify(cacheName)});
    await cache.put("/__harness/cache-proof", new Response(${JSON.stringify(value)}));
    await navigator.serviceWorker.register("/__harness/service-worker.js");
  })()`, true);
}

async function assertPartitionState(
  target,
  expected,
  { databaseName, cacheName, cookieName },
) {
  const cookies = await target.remoteSession.cookies.get({
    url: proofOrigin,
    name: cookieName,
  });
  assert.equal(cookies[0]?.value ?? null, expected);
  const state = await execute(target.window, `(async () => {
    const local = localStorage.getItem(${JSON.stringify("leapview.desktop.partition-value")});
    const databases = await indexedDB.databases();
    const cache = await caches.open(${JSON.stringify(cacheName)});
    const response = await cache.match("/__harness/cache-proof");
    const registrations = await navigator.serviceWorker.getRegistrations();
    return {
      local,
      database: databases.some((database) => database.name === ${JSON.stringify(databaseName)}),
      cache: response === undefined ? null : await response.text(),
      serviceWorkers: registrations.length,
    };
  })()`, true);
  assert.equal(state.local, expected);
  assert.equal(state.database, expected !== null);
  assert.equal(state.cache, expected);
  assert.equal(state.serviceWorkers, expected === null ? 0 : 1);
}

async function assertPartitionStateMissing(
  target,
  names,
) {
  await assertPartitionState(target, null, names);
}

async function observeDiscoveryAttacks(observations) {
  result.currentCheck = "discovery.malformed";
  await assert.rejects(
    fetchBoundedJSON(`${proofOrigin}/attack/discovery.malformed`),
  );
  observations.record("discovery.malformed", "denied");

  result.currentCheck = "discovery.oversized";
  await assert.rejects(
    fetchBoundedJSON(`${proofOrigin}/attack/discovery.oversized`),
    /exceeds 65536 bytes/,
  );
  observations.record("discovery.oversized", "denied");
}

async function observeRendererAvailability(window, observations) {
  result.currentCheck = "renderer.resource-exhaustion";
  await load(window, `${proofOrigin}/attack/renderer.resource-exhaustion`);
  const rendererWork = execute(
    window,
    `document.getElementById("trigger").click()`,
    true,
  );
  const firstCompletion = await Promise.race([
    rendererWork.then(() => "renderer"),
    delay(75).then(() => "main"),
  ]);
  assert.equal(firstCompletion, "main", "renderer work blocked the Electron main process");
  await rendererWork;
  observations.record("renderer.resource-exhaustion", "responsive");
}

function createProofRemoteWindow(partition) {
  const remoteSession = session.fromPartition(partition, { cache: false });
  configureRemoteSession(remoteSession, recordDecision);
  const window = createRemoteWindow({
    partition,
    canonicalOrigin: proofOrigin,
    displayName: "Malicious LeapView Instance",
    createWindow: (options) => new BrowserWindow(options),
    onDecision: recordDecision,
    requestExternalOpen: async () => {},
    onFailure: () => {},
    onSafeRoute: () => {},
    onClosed: () => {},
    installLifecyclePolicy: () => {},
  });
  return { window, remoteSession };
}

function inspectNativeGlobals(frame) {
  return frame.executeJavaScript(`({
    nodeProcess: typeof window.process !== "undefined",
    nodeRequire: typeof window.require !== "undefined",
    nodeModule: typeof window.module !== "undefined",
    electron: Boolean(window.electron || window.electronAPI),
  })`);
}

function assertNoNativeGlobals(globals) {
  assert.deepEqual(globals, {
    nodeProcess: false,
    nodeRequire: false,
    nodeModule: false,
    electron: false,
  });
}

function assertDecision(kind) {
  assert.ok(
    result.decisions.some((decision) => decision.kind === kind),
    `no ${kind} decision was recorded`,
  );
}

function recordDecision(decision) {
  result.decisions.push(decision);
}

async function writeResult() {
  await writeJSONAtomic(resultPath, result);
}

function delay(milliseconds) {
  return new Promise((resolve) => setTimeout(resolve, milliseconds));
}

async function waitFor(operation, milliseconds, label) {
  const deadline = Date.now() + milliseconds;
  while (Date.now() < deadline) {
    const value = operation();
    if (value) {
      return value;
    }
    await delay(25);
  }
  throw new Error(`${label} exceeded ${milliseconds}ms`);
}

async function load(window, url, options = {}) {
  const operation = window.loadURL(url);
  if (options.allowBlocked === true) {
    await Promise.race([operation.catch(() => {}), delay(1_500)]);
    return;
  }
  await withTimeout(operation, 5_000, `load ${new URL(url).pathname}`);
}

async function execute(window, script, userGesture = false) {
  return withTimeout(
    window.webContents.executeJavaScript(script, userGesture),
    5_000,
    "renderer script",
  );
}

async function withTimeout(operation, milliseconds, label) {
  let timeout;
  try {
    return await Promise.race([
      operation,
      new Promise((_, reject) => {
        timeout = setTimeout(
          () => reject(new Error(`${label} exceeded ${milliseconds}ms`)),
          milliseconds,
        );
      }),
    ]);
  } finally {
    clearTimeout(timeout);
  }
}
