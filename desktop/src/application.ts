// Desktop application lifecycle and feature composition. The executable
// entrypoint intentionally imports only this owned application module.
import { spawn } from "node:child_process";
import { release as operatingSystemRelease } from "node:os";
import {
  dirname,
  join,
  resolve,
} from "node:path";

import {
  app,
  autoUpdater,
  BrowserWindow,
  dialog,
  Menu,
  net,
  powerMonitor,
  protocol,
  screen,
  session,
  shell,
  type BrowserWindowConstructorOptions,
  type Session,
} from "electron";

import {
  clearDesktopProfileState,
  disconnectDesktopProfile,
  DesktopProfileRemovedLocallyError,
  prepareDesktopSession,
  removeDesktopProfileState,
} from "./auth.js";
import { DesktopAuthenticationCoordinator } from "./application-auth.js";
import { DesktopShutdownCoordinator } from "./application-shutdown.js";
import {
  isRetryableRecoveryFailure,
  nonRetryableRecoveryMessage,
} from "./application-recovery.js";
import {
  canonicalExternalURL,
  exactProfileURL,
  pathIsInside,
  profilePartition,
} from "./application-navigation.js";
import {
  DeepLinkDispatcher,
  DESKTOP_DEEP_LINK_SCHEME,
  routeDesktopDeepLink,
  type DeepLinkRejection,
  type DesktopDeepLink,
} from "./deep-link.js";
import {
  DesktopDiscoveryError,
  discoverInstance,
  type DiscoveryDocument,
} from "./discovery.js";
import {
  normalizeChildProcessType,
  normalizeProcessGoneReason,
  writeDiagnosticReport,
  type DiagnosticEnvironment,
  type DiagnosticEvent,
} from "./diagnostics.js";
import { DesktopDiagnosticsCoordinator } from "./application-diagnostics.js";
import { resolveDesktopDistribution } from "./distribution.js";
import { runPackagedSecurityProofIfRequested } from "./packaged-security-proof.js";
import { buildNativeMenuTemplate } from "./native-menu.js";
import {
  loadDesktopPolicy,
  policyAllowsOrigin,
  policyAllowsProfile,
  policyManagesOrigin,
  probeWindowsDesktopPolicy,
  resolveDesktopPolicySource,
  type DesktopPolicy,
} from "./managed-policy.js";
import {
  profileDisplayName,
  DesktopProfileReplacementCancelledError,
  ProfileStore,
  type Profile,
} from "./profiles.js";
import {
  installRemoteLifecyclePolicy,
  type RemoteLifecycleFailure,
} from "./remote-lifecycle.js";
import {
  createRemoteWindow,
  REMOTE_WINDOW_SIZE,
} from "./security/remote-window.mjs";
import {
  BoundedRecoveryCoordinator,
  RollingRecoveryBudget,
  type RecoveryAttemptResult,
} from "./recovery.js";
import {
  configureRemoteSession,
  parseConfiguredOrigin,
} from "./security/remote-policy.mjs";
import {
  handleSquirrelLifecycle,
  SQUIRREL_APP_USER_MODEL_ID,
} from "./squirrel-lifecycle.js";
import { loadTrustedUIAssets } from "./trusted-assets.js";
import { TrustedUI } from "./trusted-ui.js";
import { DesktopUpdateCoordinator } from "./update-coordinator.js";
import type { DesktopAutoUpdater } from "./updater.js";
import {
  type PersistedWindowState,
} from "./window-state.js";
import { DesktopWindowStateCoordinator } from "./application-window-state.js";

const TRUSTED_SCHEME = "leapview";
const TRUSTED_PARTITION = "leapview-shell";
const DISCOVERY_PARTITION = "leapview-discovery";
const WINDOW_STATE_FLUSH_DELAY_MS = 300;
const DIAGNOSTIC_FLUSH_DELAY_MS = 500;
const NETWORK_STATUS_POLL_MS = 5_000;
const RENDERER_CRASH_RECOVERY_WINDOW_MS = 60_000;
const RENDERER_CRASH_RECOVERY_LIMIT = 2;
const SHELL_WINDOW_SIZE = {
  width: 780,
  height: 760,
  minimumWidth: 620,
  minimumHeight: 620,
};
interface RemotePolicyDecision {
  kind: string;
  allowed: boolean;
}

protocol.registerSchemesAsPrivileged([
  {
    scheme: TRUSTED_SCHEME,
    privileges: {
      standard: true,
      secure: true,
      supportFetchAPI: false,
      corsEnabled: false,
      allowServiceWorkers: false,
      codeCache: true,
    },
  },
]);
app.enableSandbox();

let shellWindow: BrowserWindow | null = null;
let trustedUI: TrustedUI | null = null;
const remoteWindows = new Map<string, BrowserWindow>();
const authenticationCoordinator = new DesktopAuthenticationCoordinator(
  (authorizationURL) => shell.openExternal(authorizationURL, { activate: true }),
  recordDiagnostic,
);
const configuredSessions = new WeakSet<Session>();
const configuredSessionOrigins = new WeakMap<Session, string>();
const externalApprovals = new Set<string>();
const remoteRecoveries = new Map<string, BoundedRecoveryCoordinator>();
const rendererCrashBudgets = new Map<string, RollingRecoveryBudget>();
const allowLoopbackHTTP = !app.isPackaged;
const deepLinks = new DeepLinkDispatcher({
  allowLoopbackHTTP,
  onRejected: reportDeepLinkRejection,
});
let pendingDeepLinkNotice: {
  state: "error";
  kind: "error";
  message: string;
} | undefined;
let profiles: ProfileStore;
let desktopPolicy: DesktopPolicy = {
  mode: "locked",
  allowUserAddedInstances: false,
  diagnosticsEnabled: false,
  preconfiguredOrigins: [],
  revision: "desktop-policy-v1-invalid",
};
let windowStateCoordinator: DesktopWindowStateCoordinator | null = null;
const shutdownCoordinator = new DesktopShutdownCoordinator();
let diagnostics: DesktopDiagnosticsCoordinator | null = null;
let diagnosticExportActive = false;
let systemSuspended = false;
let networkAvailable = true;
let networkStatusTimer: NodeJS.Timeout | null = null;
let desktopUpdates: DesktopUpdateCoordinator | null = null;

if (process.platform === "win32") {
  app.setAppUserModelId(SQUIRREL_APP_USER_MODEL_ID);
}
const squirrelLifecycleHandled = handleSquirrelLifecycle({
  argv: process.argv,
  packaged: app.isPackaged,
  platform: process.platform,
  registerProtocol: () =>
    app.setAsDefaultProtocolClient(DESKTOP_DEEP_LINK_SCHEME),
  removeProtocol: () =>
    app.removeAsDefaultProtocolClient(DESKTOP_DEEP_LINK_SCHEME),
  runUpdate: (arguments_) => {
    const updater = resolve(
      dirname(process.execPath),
      "..",
      "Update.exe",
    );
    const child = spawn(updater, [...arguments_], {
      detached: true,
      stdio: "ignore",
      windowsHide: true,
    });
    child.on("error", () => undefined);
    child.unref();
  },
  scheduleQuit: () => {
    setTimeout(() => app.quit(), 1_000);
  },
});
const primaryInstance =
  !squirrelLifecycleHandled && app.requestSingleInstanceLock();
if (squirrelLifecycleHandled) {
  // Squirrel lifecycle launches must not initialize profiles or remote content.
} else if (!primaryInstance) {
  app.quit();
} else {
  registerDeepLinkProtocolClient();
  deepLinks.acceptArguments(process.argv, "cold-start");
  app.on("open-url", (event, url) => {
    event.preventDefault();
    if (!deepLinks.acceptURL(url, "open-url")) {
      focusTrustedShell();
    }
  });
  app.on("second-instance", (_event, arguments_) => {
    if (!deepLinks.acceptArguments(arguments_, "second-instance")) {
      focusTrustedShell();
    }
  });
  app.on(
    "certificate-error",
    (event, _contents, _url, _error, _certificate, callback) => {
      event.preventDefault();
      callback(false);
    },
  );
  app.on("render-process-gone", (_event, contents, details) => {
    recordDiagnostic({
      kind: "render-process-gone",
      surface: diagnosticSurface(contents),
      reason: normalizeProcessGoneReason(details.reason),
    });
  });
  app.on("child-process-gone", (_event, details) => {
    recordDiagnostic({
      kind: "child-process-gone",
      processType: normalizeChildProcessType(details.type),
      reason: normalizeProcessGoneReason(details.reason),
    });
  });
  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) {
      createShellWindow();
    }
  });
  app.on("window-all-closed", () => {
    cancelAllAuthenticationTransactions();
    cancelAllRemoteRecoveries();
    if (process.platform !== "darwin") {
      app.quit();
    }
  });
  app.on("before-quit", (event) => {
    cancelAllAuthenticationTransactions();
    cancelAllRemoteRecoveries();
    if (networkStatusTimer !== null) {
      clearInterval(networkStatusTimer);
      networkStatusTimer = null;
    }
    desktopUpdates?.stop();
    if (shutdownCoordinator.isReady || windowStateCoordinator === null) {
      return;
    }
    shutdownCoordinator.begin(
      () => event.preventDefault(),
      captureAllWindowStates,
      () => Promise.all([flushWindowStates(), flushDiagnostics()]).then(() => undefined),
      () => app.quit(),
    );
  });
  void app.whenReady().then(start).catch(() => {
    console.error("LeapView Desktop failed to start safely.");
    app.exit(1);
  });
}

async function start(): Promise<void> {
  const distribution = resolveDesktopDistribution({
    packaged: app.isPackaged,
    resourcesPath: process.resourcesPath,
  });
  if (await runPackagedSecurityProofIfRequested(distribution)) {
    return;
  }
  networkAvailable = net.isOnline();
  powerMonitor.on("suspend", () => {
    systemSuspended = true;
    refreshRecoveryAvailability();
  });
  powerMonitor.on("resume", () => {
    systemSuspended = false;
    updateNetworkAvailability();
    refreshRecoveryAvailability();
  });
  networkStatusTimer = setInterval(
    updateNetworkAvailability,
    NETWORK_STATUS_POLL_MS,
  );
  const windowsProbe =
    process.platform === "win32" && app.isPackaged
      ? await probeWindowsDesktopPolicy(process.resourcesPath)
      : undefined;
  desktopPolicy = await loadDesktopPolicy(
    resolveDesktopPolicySource({
      platform: process.platform,
      packaged: app.isPackaged,
      ...(windowsProbe === null || windowsProbe === undefined
        ? {}
        : { windowsProbe }),
    }),
    { allowLoopbackHTTP },
  );
  profiles = new ProfileStore(join(app.getPath("userData"), "profiles.json"));
  diagnostics = await DesktopDiagnosticsCoordinator.open(
    join(app.getPath("userData"), "diagnostics.json"),
    desktopPolicy.diagnosticsEnabled,
    DIAGNOSTIC_FLUSH_DELAY_MS,
  );
  recordDiagnostic({ kind: "startup", packaged: app.isPackaged });
  recordDiagnostic({
    kind: "policy",
    mode: desktopPolicy.mode,
    userInstances: desktopPolicy.allowUserAddedInstances
      ? "allowed"
      : "restricted",
    diagnostics: desktopPolicy.diagnosticsEnabled
      ? "enabled"
      : "disabled",
  });
  windowStateCoordinator = await DesktopWindowStateCoordinator.open(
    join(app.getPath("userData"), "window-state.json"),
    WINDOW_STATE_FLUSH_DELAY_MS,
  );
  initializeDesktopUpdater(distribution);
  Menu.setApplicationMenu(
    Menu.buildFromTemplate(
      buildNativeMenuTemplate(process.platform, app.name, {
        showInstances: focusTrustedShell,
        saveDiagnosticReport: () => {
          void saveDiagnosticReport();
        },
        checkForUpdates: () => {
          void desktopUpdates?.checkManually();
        },
      }),
    ),
  );
  screen.on("display-removed", keepAllWindowsVisible);
  screen.on("display-metrics-changed", keepAllWindowsVisible);
  const trustedAssets = await loadTrustedUIAssets();
  trustedUI = new TrustedUI(
    {
      allowLoopbackHTTP,
      policy: desktopPolicy,
      listProfiles: listAllowedProfiles,
      connectOrigin,
      connectProfile,
      disconnectProfile,
      removeProfile,
      renameProfile,
    },
    trustedAssets,
  );
  const trustedSession = session.fromPartition(TRUSTED_PARTITION, {
    cache: false,
  });
  configureSessionOnce(trustedSession);
  await trustedSession.protocol.handle(TRUSTED_SCHEME, (request) =>
    trustedUI?.handle(request) ?? new Response(null, { status: 503 }),
  );
  if (pendingDeepLinkNotice !== undefined) {
    trustedUI.reportNotice(pendingDeepLinkNotice);
    pendingDeepLinkNotice = undefined;
  }
  createShellWindow();
  deepLinks.attach((request, source) =>
    routeDesktopDeepLink(request, source, {
      listProfiles: listAllowedProfiles,
      openKnown: (profile, path) => connectProfileAtPath(profile.id, path),
      confirmUnknown: confirmUnknownDeepLink,
      connectUnknown: (candidate) =>
        connectOriginAtPath(candidate.origin, candidate.path),
      rejectUnknown: reportUnknownSecondaryDeepLink,
    }),
  );
  desktopUpdates?.startAutomaticChecks();
}

async function connectOrigin(rawOrigin: string): Promise<void> {
  await connectOriginAtPath(rawOrigin, "/");
}

async function connectOriginAtPath(
  rawOrigin: string,
  path: string,
): Promise<void> {
  const origin = configuredOrigin(rawOrigin);
  requirePolicyOrigin(origin);
  const discovery = await discover(origin);
  const profile = await resolveVerifiedProfile(discovery);
  await openRemoteWindow(profile, path);
}

async function connectProfile(profileID: string): Promise<void> {
  const profile = await savedProfile(profileID);
  await connectProfileAtPath(profile.id, profile.lastSafePath);
}

async function connectProfileAtPath(
  profileID: string,
  path: string,
): Promise<void> {
  cancelRemoteRecovery(profileID);
  const profile = await savedProfile(profileID);
  const origin = configuredOrigin(profile.canonicalOrigin);
  const discovery = await discover(origin);
  const verifiedProfile = await resolveVerifiedProfile(discovery);
  await openRemoteWindow(
    verifiedProfile,
    verifiedProfile.id === profile.id ? path : "/",
  );
}

async function resolveVerifiedProfile(
  discovery: DiscoveryDocument,
): Promise<Profile> {
  try {
    return await profiles.upsertFromDiscovery(discovery);
  } catch (error) {
    if (
      !(error instanceof DesktopDiscoveryError) ||
      ![
        "canonical_origin_mismatch",
        "instance_identity_mismatch",
      ].includes(error.kind)
    ) {
      throw error;
    }
  }
  const current = (await profiles.list()).find(
    (profile) =>
      profile.canonicalOrigin === discovery.canonicalOrigin ||
      profile.instanceId === discovery.instanceId,
  );
  if (current === undefined) {
    throw new Error("Saved profile replacement candidate was not found.");
  }
  const options = {
    type: "warning" as const,
    buttons: ["Cancel", "Replace saved instance"],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    title: "Replace saved LeapView instance?",
    message:
      "The verified server identity or canonical address changed.",
    detail: [
      `Saved origin: ${current.canonicalOrigin}`,
      `Saved identity: ${current.instanceId}`,
      `Reported origin: ${discovery.canonicalOrigin}`,
      `Reported identity: ${discovery.instanceId}`,
      "",
      "Replacing creates a new isolated profile and removes the old profile's local browser state.",
    ].join("\n"),
  };
  const confirmation =
    shellWindow !== null && !shellWindow.isDestroyed()
      ? await dialog.showMessageBox(shellWindow, options)
      : await dialog.showMessageBox(options);
  if (confirmation.response !== 1) {
    throw new DesktopProfileReplacementCancelledError();
  }
  cancelRemoteRecovery(current.id);
  rendererCrashBudgets.delete(current.id);
  await cancelAuthenticationTransaction(current.id);
  const remote = remoteWindows.get(current.id);
  if (remote !== undefined && !remote.isDestroyed()) {
    remote.destroy();
  }
  const oldSession = session.fromPartition(profilePartition(current));
  configureSessionOnce(oldSession, current);
  await disconnectDesktopProfile(
    current,
    (input, init) => oldSession.fetch(input, init),
  ).catch(() => undefined);
  const replacement = await profiles.replaceFromDiscovery(
    current.id,
    discovery,
  );
  await clearDesktopProfileState(oldSession);
  windowStateCoordinator?.remove(current.id);
  scheduleWindowStateFlush();
  recordDiagnostic({
    kind: "profile",
    action: "replaced",
    outcome: "success",
  });
  return replacement;
}

async function disconnectProfile(profileID: string): Promise<void> {
  const profile = await savedProfile(profileID);
  cancelRemoteRecovery(profile.id);
  rendererCrashBudgets.delete(profile.id);
  await cancelAuthenticationTransaction(profile.id);
  const remote = remoteWindows.get(profile.id);
  if (remote !== undefined && !remote.isDestroyed()) {
    remote.destroy();
  }
  const partition = profilePartition(profile);
  const profileSession = session.fromPartition(partition);
  configureSessionOnce(profileSession, profile);
  try {
    await disconnectDesktopProfile(
      profile,
      (input, init) => profileSession.fetch(input, init),
    );
    await clearDesktopProfileState(profileSession);
    recordDiagnostic({ kind: "authentication", phase: "disconnected" });
    recordDiagnostic({
      kind: "profile",
      action: "disconnected",
      outcome: "success",
    });
  } catch (error) {
    recordDiagnostic({
      kind: "profile",
      action: "disconnected",
      outcome: "failed",
    });
    throw error;
  }
}

async function removeProfile(profileID: string): Promise<void> {
  const profile = await savedProfile(profileID);
  if (policyManagesOrigin(desktopPolicy, profile.canonicalOrigin)) {
    throw new Error(
      "This LeapView instance is managed by your organization and cannot be removed.",
    );
  }
  cancelRemoteRecovery(profile.id);
  rendererCrashBudgets.delete(profile.id);
  await cancelAuthenticationTransaction(profile.id);
  const remote = remoteWindows.get(profile.id);
  if (remote !== undefined && !remote.isDestroyed()) {
    remote.destroy();
  }
  const profileSession = session.fromPartition(profilePartition(profile));
  configureSessionOnce(profileSession, profile);
  let removalError: unknown;
  try {
    await removeDesktopProfileState(
      profile,
      (input, init) => profileSession.fetch(input, init),
      profileSession,
      () => profiles.remove(profileID),
    );
  } catch (error) {
    removalError = error;
  }
  if (
    removalError === undefined ||
    removalError instanceof DesktopProfileRemovedLocallyError
  ) {
    windowStateCoordinator?.remove(profileID);
    scheduleWindowStateFlush();
    recordDiagnostic({
      kind: "profile",
      action: "removed",
      outcome: "success",
    });
  } else {
    recordDiagnostic({
      kind: "profile",
      action: "removed",
      outcome: "failed",
    });
  }
  if (removalError !== undefined) {
    throw removalError;
  }
}

async function renameProfile(
  profileID: string,
  label: string | null,
): Promise<void> {
  const profile = await profiles.setLabel(profileID, label);
  const remote = remoteWindows.get(profile.id);
  if (remote !== undefined && !remote.isDestroyed()) {
    remote.setTitle(
      `${profileDisplayName(profile)} — ${profile.canonicalOrigin}`,
    );
  }
  recordDiagnostic({
    kind: "profile",
    action: "renamed",
    outcome: "success",
  });
}

async function savedProfile(profileID: string): Promise<Profile> {
  if (!/^profile_[0-9a-f]{32}$/u.test(profileID)) {
    throw new Error("Saved profile identifier is invalid.");
  }
  const profile = (await listAllowedProfiles()).find(
    (candidate) => candidate.id === profileID,
  );
  if (profile === undefined) {
    throw new Error("Saved LeapView instance was not found.");
  }
  return profile;
}

async function listAllowedProfiles(): Promise<Profile[]> {
  return (await profiles.list()).filter((profile) =>
    policyAllowsProfile(desktopPolicy, profile),
  );
}

function requirePolicyOrigin(canonicalOrigin: string): void {
  if (!policyAllowsOrigin(desktopPolicy, canonicalOrigin)) {
    throw new Error(
      "This desktop is managed by your organization. Choose an approved instance.",
    );
  }
}

async function discover(origin: string) {
  const discoverySession = session.fromPartition(DISCOVERY_PARTITION, {
    cache: false,
  });
  configureSessionOnce(discoverySession);
  try {
    const document = await discoverInstance(origin, (input, init) =>
      discoverySession.fetch(input, init),
    );
    recordDiagnostic({ kind: "discovery", outcome: "success" });
    return document;
  } catch (error) {
    recordDiagnostic({
      kind: "discovery",
      outcome: diagnosticDiscoveryOutcome(error),
    });
    throw error;
  } finally {
    await discoverySession.clearStorageData();
    await discoverySession.clearCache();
  }
}

async function openRemoteWindow(
  profile: Profile,
  path: string = profile.lastSafePath,
  preparedSession?: Session,
): Promise<void> {
  const target = exactProfileURL(profile, path);
  const existing = remoteWindows.get(profile.id);
  if (existing !== undefined && !existing.isDestroyed()) {
    try {
      const profileSession = session.fromPartition(profilePartition(profile));
      configureSessionOnce(profileSession, profile);
      if (preparedSession === undefined) {
        await ensureAuthenticated(profile, profileSession);
      }
      await existing.loadURL(target);
      existing.show();
      existing.focus();
      recordDiagnostic({
        kind: "profile",
        action: "opened",
        outcome: "success",
      });
    } catch (error) {
      recordDiagnostic({
        kind: "profile",
        action: "opened",
        outcome: "failed",
      });
      throw error;
    }
    return;
  }
  const partition = profilePartition(profile);
  const profileSession =
    preparedSession ?? session.fromPartition(partition);
  configureSessionOnce(profileSession, profile);
  if (preparedSession === undefined) {
    await ensureAuthenticated(profile, profileSession);
  }
  const restoredState = restoreWindowState(
    profile.id,
    REMOTE_WINDOW_SIZE.minimumWidth,
    REMOTE_WINDOW_SIZE.minimumHeight,
  );
  let remote: BrowserWindow;
  remote = createRemoteWindow({
    partition,
    canonicalOrigin: profile.canonicalOrigin,
    displayName: profileDisplayName(profile),
    ...(restoredState === undefined ? {} : { restoredState }),
    createWindow: (options: BrowserWindowConstructorOptions) =>
      new BrowserWindow(options),
    onDecision: recordRemotePolicyDecision,
    requestExternalOpen: (request: { url: string }) =>
      confirmExternalOpen(profile, remote, request.url),
    onFailure: (failure: RemoteLifecycleFailure) =>
      handleRemoteFailure(profile, remote, failure),
    onSafeRoute: (route: string) =>
      profiles.setLastSafePath(profile.id, route).then(() => undefined),
    onClosed: () => remoteWindows.delete(profile.id),
    installLifecyclePolicy: installRemoteLifecyclePolicy,
  });
  remoteWindows.set(profile.id, remote);
  trackWindowState(
    remote,
    profile.id,
    REMOTE_WINDOW_SIZE.minimumWidth,
    REMOTE_WINDOW_SIZE.minimumHeight,
  );
  try {
    await remote.loadURL(target);
    if (preparedSession === undefined) {
      rendererCrashBudgets.delete(profile.id);
    }
    cancelRemoteRecovery(profile.id);
    recordDiagnostic({
      kind: "profile",
      action: "opened",
      outcome: "success",
    });
  } catch (error) {
    recordDiagnostic({
      kind: "profile",
      action: "opened",
      outcome: "failed",
    });
    if (!remote.isDestroyed()) {
      remote.destroy();
    }
    throw new Error("LeapView could not load after successful discovery.", {
      cause: error,
    });
  }
}


async function ensureAuthenticated(
  profile: Profile,
  profileSession: Session,
): Promise<void> {
  await authenticationCoordinator.ensure(profile, profileSession);
}

async function cancelAuthenticationTransaction(profileID: string): Promise<void> {
  await authenticationCoordinator.cancel(profileID);
}

function cancelAllAuthenticationTransactions(): void {
  authenticationCoordinator.cancelAll();
}

function registerDeepLinkProtocolClient(): void {
  if (app.isPackaged) {
    if (!app.isDefaultProtocolClient(DESKTOP_DEEP_LINK_SCHEME)) {
      console.warn(
        "LeapView Desktop installer protocol registration is unavailable.",
      );
    }
    return;
  }
  const registered =
    process.defaultApp && process.argv[1] !== undefined
      ? app.setAsDefaultProtocolClient(
          DESKTOP_DEEP_LINK_SCHEME,
          process.execPath,
          [resolve(process.argv[1])],
        )
      : false;
  if (!registered) {
    console.warn(
      "LeapView Desktop development protocol registration was unavailable.",
    );
  }
}

async function confirmUnknownDeepLink(
  request: DesktopDeepLink,
): Promise<boolean> {
  focusTrustedShell();
  if (!policyAllowsOrigin(desktopPolicy, request.origin)) {
    reportTrustedShellNotice(
      "This desktop is managed by your organization. The link targets an unapproved instance.",
    );
    return false;
  }
  const options: Electron.MessageBoxOptions = {
    type: "question",
    buttons: ["Cancel", "Add instance"],
    defaultId: 0,
    cancelId: 0,
    noLink: true,
    title: "Add LeapView instance",
    message: "This link targets an instance not saved on this device.",
    detail: `${request.origin}\n\nOpen ${request.path} after verifying and adding this instance?`,
  };
  const result =
    shellWindow !== null && !shellWindow.isDestroyed()
      ? await dialog.showMessageBox(shellWindow, options)
      : await dialog.showMessageBox(options);
  return result.response === 1;
}

function reportUnknownSecondaryDeepLink(): void {
  reportTrustedShellNotice(
    "This link targets an instance that is not saved on this device. Add the instance in LeapView before opening the link again.",
  );
}

function reportDeepLinkRejection(rejection: DeepLinkRejection): void {
  const message =
    rejection === "overloaded"
      ? "Too many LeapView links are waiting. Finish the current action and try again."
      : rejection === "handling-failed"
        ? "LeapView could not open the link safely. Open the saved instance and try the route again."
        : "LeapView rejected an invalid or ambiguous desktop link.";
  reportTrustedShellNotice(message);
}

function reportTrustedShellNotice(message: string): void {
  const notice = {
    kind: "error" as const,
    state: "error" as const,
    message,
  };
  if (trustedUI === null) {
    pendingDeepLinkNotice = notice;
    return;
  }
  trustedUI.reportNotice(notice);
  focusTrustedShell(true);
}

function initializeDesktopUpdater(releaseChannel: ReturnType<typeof resolveDesktopDistribution>): void {
  desktopUpdates = new DesktopUpdateCoordinator({
    native: autoUpdater as unknown as DesktopAutoUpdater,
    runtime: {
      platform: process.platform,
      architecture: process.arch,
      applicationVersion: app.getVersion(),
      electronVersion: process.versions.electron ?? "",
      packaged: app.isPackaged,
      releaseChannel,
    },
    showMessageBox: (options) =>
      shellWindow !== null && !shellWindow.isDestroyed()
        ? dialog.showMessageBox(shellWindow, options)
        : dialog.showMessageBox(options),
    recordEvent: (event) => {
      recordDiagnostic({ kind: "update", phase: event.phase });
    },
    beforeRestart: prepareForUpdateRestart,
  });
  desktopUpdates.initialize();
}

async function prepareForUpdateRestart(): Promise<void> {
  desktopUpdates?.stop();
  cancelAllAuthenticationTransactions();
  cancelAllRemoteRecoveries();
  captureAllWindowStates();
  await Promise.all([flushWindowStates(), flushDiagnostics()]);
  shutdownCoordinator.markReady();
}

function focusTrustedShell(reload = false): void {
  if (!app.isReady()) {
    return;
  }
  createShellWindow();
  if (
    reload &&
    shellWindow !== null &&
    !shellWindow.isDestroyed()
  ) {
    void shellWindow.loadURL("leapview://app/");
  }
}

function createShellWindow(): void {
  if (shellWindow !== null && !shellWindow.isDestroyed()) {
    shellWindow.show();
    shellWindow.focus();
    refreshRecoveryAvailability();
    return;
  }
  const restoredState = restoreWindowState(
    "shell",
    SHELL_WINDOW_SIZE.minimumWidth,
    SHELL_WINDOW_SIZE.minimumHeight,
  );
  const window = new BrowserWindow({
    width: SHELL_WINDOW_SIZE.width,
    height: SHELL_WINDOW_SIZE.height,
    minWidth: SHELL_WINDOW_SIZE.minimumWidth,
    minHeight: SHELL_WINDOW_SIZE.minimumHeight,
    ...(restoredState?.bounds ?? {}),
    show: false,
    title: "LeapView",
    backgroundColor: "#f4f7f5",
    webPreferences: {
      partition: TRUSTED_PARTITION,
      nodeIntegration: false,
      nodeIntegrationInWorker: false,
      nodeIntegrationInSubFrames: false,
      contextIsolation: true,
      sandbox: true,
      webSecurity: true,
      allowRunningInsecureContent: false,
      experimentalFeatures: false,
      webviewTag: false,
      devTools: false,
      disableDialogs: true,
      navigateOnDragDrop: false,
      autoplayPolicy: "document-user-activation-required",
      enableWebSQL: false,
      plugins: false,
    },
  });
  shellWindow = window;
  trackWindowState(
    window,
    "shell",
    SHELL_WINDOW_SIZE.minimumWidth,
    SHELL_WINDOW_SIZE.minimumHeight,
  );
  if (restoredState?.maximized === true) {
    window.maximize();
  }
  installTrustedContentsPolicy(window.webContents);
  window.once("ready-to-show", () => {
    if (!window.isDestroyed()) {
      window.show();
      refreshRecoveryAvailability();
    }
  });
  window.on("show", refreshRecoveryAvailability);
  window.on("restore", refreshRecoveryAvailability);
  window.on("minimize", refreshRecoveryAvailability);
  window.on("hide", refreshRecoveryAvailability);
  window.once("closed", () => {
    if (shellWindow === window) {
      shellWindow = null;
    }
    refreshRecoveryAvailability();
  });
  void window.loadURL("leapview://app/");
}

function restoreWindowState(
  key: string,
  minimumWidth: number,
  minimumHeight: number,
): PersistedWindowState | undefined {
  return windowStateCoordinator?.restore(key, minimumWidth, minimumHeight);
}

function trackWindowState(
  window: BrowserWindow,
  key: string,
  minimumWidth: number,
  minimumHeight: number,
): void {
  windowStateCoordinator?.track(window, key, minimumWidth, minimumHeight);
}

function scheduleWindowStateFlush(): void {
  windowStateCoordinator?.scheduleFlush();
}

async function flushWindowStates(): Promise<void> {
  await windowStateCoordinator?.flush();
}

function captureAllWindowStates(): void {
  windowStateCoordinator?.captureAll(shellWindow, remoteWindows);
}

function keepAllWindowsVisible(): void {
  windowStateCoordinator?.keepAllVisible(
    shellWindow,
    remoteWindows,
    SHELL_WINDOW_SIZE,
    REMOTE_WINDOW_SIZE,
  );
}

async function saveDiagnosticReport(): Promise<void> {
  if (diagnosticExportActive || diagnostics === null) {
    return;
  }
  diagnosticExportActive = true;
  const parent = BrowserWindow.getFocusedWindow();
  try {
    const confirmationOptions: Electron.MessageBoxOptions = {
      type: "question",
      buttons: ["Cancel", "Choose location"],
      defaultId: 0,
      cancelId: 0,
      noLink: true,
      title: "Save diagnostic report",
      message: "Create a privacy-safe LeapView diagnostic report?",
      detail:
        "Includes: app/runtime/OS versions, desktop policy revision, and recent allowlisted lifecycle outcomes.\n\nExcludes: instance URLs and names, dashboard data, credentials, cookies, tokens, authorization values, renderer console output, filenames, and crash dumps.\n\nThe JSON report is saved locally and is never uploaded automatically.",
    };
    const confirmation =
      parent === null
        ? await dialog.showMessageBox(confirmationOptions)
        : await dialog.showMessageBox(parent, confirmationOptions);
    if (confirmation.response !== 1) {
      return;
    }
    const saveOptions: Electron.SaveDialogOptions = {
      title: "Save LeapView diagnostic report",
      defaultPath: join(
        app.getPath("downloads"),
        "leapview-diagnostic-report.json",
      ),
      buttonLabel: "Save report",
      filters: [{ name: "JSON", extensions: ["json"] }],
      properties: [
        "showOverwriteConfirmation",
        "createDirectory",
        "dontAddToRecent",
      ],
    };
    const destination =
      parent === null
        ? await dialog.showSaveDialog(saveOptions)
        : await dialog.showSaveDialog(parent, saveOptions);
    if (destination.canceled || destination.filePath === "") {
      return;
    }
    if (pathIsInside(destination.filePath, app.getPath("userData"))) {
      throw new Error("diagnostic destination overlaps application state");
    }
    await writeDiagnosticReport(
      destination.filePath,
      diagnostics.report(diagnosticEnvironment()),
    );
    const successOptions: Electron.MessageBoxOptions = {
      type: "info",
      buttons: ["OK"],
      defaultId: 0,
      cancelId: 0,
      noLink: true,
      title: "Diagnostic report saved",
      message: "The privacy-safe diagnostic report was saved locally.",
      detail: "Review the JSON file before choosing whether to share it.",
    };
    if (parent === null || parent.isDestroyed()) {
      await dialog.showMessageBox(successOptions);
    } else {
      await dialog.showMessageBox(parent, successOptions);
    }
  } catch {
    const failureOptions: Electron.MessageBoxOptions = {
      type: "error",
      buttons: ["OK"],
      defaultId: 0,
      cancelId: 0,
      noLink: true,
      title: "Could not save diagnostic report",
      message: "LeapView could not save the diagnostic report safely.",
      detail: "Choose another location and try again.",
    };
    if (parent === null || parent.isDestroyed()) {
      await dialog.showMessageBox(failureOptions);
    } else {
      await dialog.showMessageBox(parent, failureOptions);
    }
  } finally {
    diagnosticExportActive = false;
  }
}

function diagnosticEnvironment(): DiagnosticEnvironment {
  return {
    applicationVersion: app.getVersion(),
    electronVersion: process.versions.electron ?? "unknown",
    chromiumVersion: process.versions.chrome ?? "unknown",
    nodeVersion: process.versions.node,
    platform: process.platform,
    osRelease: operatingSystemRelease(),
    architecture: process.arch,
    packaged: app.isPackaged,
    policyRevision: desktopPolicy.revision,
  };
}

function recordDiagnostic(event: DiagnosticEvent): void {
  diagnostics?.record(event);
}

async function flushDiagnostics(): Promise<void> {
  await diagnostics?.flush();
}

function recordRemotePolicyDecision(decision: RemotePolicyDecision): void {
  let action: Extract<
    DiagnosticEvent,
    { kind: "navigation" }
  >["action"] | undefined;
  if (decision.kind === "main-frame-navigation" && !decision.allowed) {
    action = "blocked-main-frame";
  } else if (decision.kind === "popup" && !decision.allowed) {
    action = "blocked-popup";
  } else if (decision.kind === "webview-attachment" && !decision.allowed) {
    action = "blocked-webview";
  } else if (
    decision.kind === "same-origin-window-open" &&
    decision.allowed
  ) {
    action = "allowed-same-origin-window";
  } else if (
    decision.kind === "external-open-request" &&
    decision.allowed
  ) {
    action = "requested-external";
  } else if (decision.kind === "download") {
    action = decision.allowed
      ? "allowed-csv-export"
      : "blocked-download";
  }
  if (action !== undefined) {
    recordDiagnostic({ kind: "navigation", action });
  }
}

function diagnosticDiscoveryOutcome(
  error: unknown,
): Extract<DiagnosticEvent, { kind: "discovery" }>["outcome"] {
  if (
    error instanceof DesktopDiscoveryError &&
    ["dns", "network", "proxy", "timeout", "tls"].includes(error.kind)
  ) {
    return "unavailable";
  }
  return "rejected";
}

function configuredOrigin(rawOrigin: string): string {
  try {
    return parseConfiguredOrigin(rawOrigin, { allowLoopbackHTTP });
  } catch (error) {
    throw new DesktopDiscoveryError(
      "invalid_origin",
      "instance URL must be a canonical HTTPS origin",
      { cause: error },
    );
  }
}

function diagnosticSurface(
  contents: Electron.WebContents,
): Extract<DiagnosticEvent, { kind: "render-process-gone" }>["surface"] {
  if (
    shellWindow !== null &&
    !shellWindow.isDestroyed() &&
    shellWindow.webContents === contents
  ) {
    return "trusted-shell";
  }
  for (const remote of remoteWindows.values()) {
    if (!remote.isDestroyed() && remote.webContents === contents) {
      return "remote";
    }
  }
  return "unknown";
}


function configureSessionOnce(target: Session, profile?: Profile): void {
  if (configuredSessions.has(target)) {
    if (
      profile !== undefined &&
      configuredSessionOrigins.get(target) !== profile.canonicalOrigin
    ) {
      throw new Error("Desktop profile session origin binding changed.");
    }
    return;
  }
  configuredSessions.add(target);
  if (profile !== undefined) {
    configuredSessionOrigins.set(target, profile.canonicalOrigin);
  }
  configureRemoteSession(
    target,
    recordRemotePolicyDecision,
    profile === undefined
      ? undefined
      : {
          configuredOrigin: profile.canonicalOrigin,
          displayName: profileDisplayName(profile),
          downloadsDirectory: app.getPath("downloads"),
        },
  );
}

async function confirmExternalOpen(
  profile: Profile,
  remote: BrowserWindow,
  candidate: string,
): Promise<void> {
  if (
    externalApprovals.has(profile.id) ||
    remote.isDestroyed() ||
    remoteWindows.get(profile.id) !== remote
  ) {
    return;
  }
  const url = canonicalExternalURL(candidate, profile.canonicalOrigin);
  if (url === null) {
    return;
  }
  externalApprovals.add(profile.id);
  try {
    const result = await dialog.showMessageBox(remote, {
      type: "question",
      buttons: ["Cancel", "Open in browser"],
      defaultId: 0,
      cancelId: 0,
      noLink: true,
      title: "Open external link",
      message: `Open a link from ${profileDisplayName(profile)}?`,
      detail: `${profile.canonicalOrigin}\n\n${url}`,
    });
    if (
      result.response === 1 &&
      !remote.isDestroyed() &&
      remoteWindows.get(profile.id) === remote
    ) {
      await shell.openExternal(url, { activate: true });
    }
  } finally {
    externalApprovals.delete(profile.id);
  }
}


function handleRemoteFailure(
  profile: Profile,
  remote: BrowserWindow,
  failure: RemoteLifecycleFailure,
): void {
  if (
    remote.isDestroyed() ||
    remoteWindows.get(profile.id) !== remote
  ) {
    return;
  }
  recordDiagnostic({
    kind: "remote-lifecycle",
    state: failure.state,
  });
  remote.destroy();
  if (
    failure.state === "crashed" &&
    !consumeRendererCrashRecoveryBudget(profile.id)
  ) {
    trustedUI?.reportNotice({
      kind: "error",
      state: "crashed",
      message:
        `${failure.message} Automatic recovery stopped after repeated renderer failures. Reopen the instance explicitly to try again.`,
    });
    createShellWindow();
    if (shellWindow !== null && !shellWindow.isDestroyed()) {
      void shellWindow.loadURL("leapview://app/");
    }
    return;
  }
  trustedUI?.reportNotice({
    kind: "error",
    state: failure.state,
    message:
      `${failure.message} LeapView will make a few bounded recovery attempts while this window remains visible.`,
  });
  createShellWindow();
  if (shellWindow !== null && !shellWindow.isDestroyed()) {
    void shellWindow.loadURL("leapview://app/");
  }
  const recovery = remoteRecovery(profile.id);
  recovery.setAvailable(recoveryIsAvailable());
  recovery.request();
}

function remoteRecovery(profileID: string): BoundedRecoveryCoordinator {
  const existing = remoteRecoveries.get(profileID);
  if (existing !== undefined) {
    return existing;
  }
  const recovery = new BoundedRecoveryCoordinator(
    (signal) => attemptRemoteRecovery(profileID, signal),
    {
      onExhausted: () => {
        reportTrustedShellNotice(
          "LeapView could not reconnect automatically. Reopen the saved instance when the network or server is ready.",
        );
      },
    },
  );
  remoteRecoveries.set(profileID, recovery);
  return recovery;
}

async function attemptRemoteRecovery(
  profileID: string,
  signal: AbortSignal,
): Promise<RecoveryAttemptResult> {
  if (signal.aborted || !recoveryIsAvailable()) {
    return "retry";
  }
  let profile: Profile;
  try {
    profile = await savedProfile(profileID);
  } catch {
    return "stop";
  }
  if (signal.aborted) {
    return "retry";
  }
  const existing = remoteWindows.get(profile.id);
  if (existing !== undefined && !existing.isDestroyed()) {
    return "success";
  }
  try {
    const discovery = await discover(configuredOrigin(profile.canonicalOrigin));
    if (signal.aborted) {
      return "retry";
    }
    if (
      discovery.canonicalOrigin !== profile.canonicalOrigin ||
      discovery.instanceId !== profile.instanceId
    ) {
      reportTrustedShellNotice(
        "LeapView stopped automatic recovery because the saved server identity changed. Reopen the instance to review the change.",
      );
      return "stop";
    }
    const profileSession = session.fromPartition(profilePartition(profile));
    configureSessionOnce(profileSession, profile);
    const authenticated = await prepareDesktopSession(
      profile,
      (input, init) => profileSession.fetch(input, init),
      profileSession,
    );
    if (signal.aborted) {
      return "retry";
    }
    if (!authenticated) {
      reportTrustedShellNotice(
        "The LeapView session is no longer valid. Reopen the saved instance to authenticate in the system browser.",
      );
      return "stop";
    }
    const refreshedProfile = await savedProfile(profile.id);
    if (signal.aborted) {
      return "retry";
    }
    await openRemoteWindow(
      refreshedProfile,
      refreshedProfile.lastSafePath,
      profileSession,
    );
    trustedUI?.reportNotice({
      kind: "success",
      state: "success",
      message: "LeapView reconnected using the last validated route.",
    });
    return "success";
  } catch (error) {
    if (signal.aborted) {
      return "retry";
    }
    if (isRetryableRecoveryFailure(error)) {
      return "retry";
    }
    reportTrustedShellNotice(nonRetryableRecoveryMessage(error));
    return "stop";
  }
}


function cancelRemoteRecovery(profileID: string): void {
  remoteRecoveries.get(profileID)?.cancel();
  remoteRecoveries.delete(profileID);
}

function consumeRendererCrashRecoveryBudget(profileID: string): boolean {
  let budget = rendererCrashBudgets.get(profileID);
  if (budget === undefined) {
    budget = new RollingRecoveryBudget(
      RENDERER_CRASH_RECOVERY_LIMIT,
      RENDERER_CRASH_RECOVERY_WINDOW_MS,
    );
    rendererCrashBudgets.set(profileID, budget);
  }
  return budget.consume();
}

function cancelAllRemoteRecoveries(): void {
  for (const recovery of remoteRecoveries.values()) {
    recovery.cancel();
  }
  remoteRecoveries.clear();
  rendererCrashBudgets.clear();
}

function updateNetworkAvailability(): void {
  const available = net.isOnline();
  if (networkAvailable === available) {
    return;
  }
  networkAvailable = available;
  refreshRecoveryAvailability();
}

function refreshRecoveryAvailability(): void {
  const available = recoveryIsAvailable();
  for (const recovery of remoteRecoveries.values()) {
    recovery.setAvailable(available);
  }
}

function recoveryIsAvailable(): boolean {
  return (
    !systemSuspended &&
    networkAvailable &&
    shellWindow !== null &&
    !shellWindow.isDestroyed() &&
    shellWindow.isVisible() &&
    !shellWindow.isMinimized()
  );
}

function installTrustedContentsPolicy(contents: Electron.WebContents): void {
  const guardNavigation = (
    details: Electron.Event<{ url: string; isMainFrame: boolean }>,
  ) => {
    if (
      details.isMainFrame &&
      !details.url.startsWith("leapview://app/")
    ) {
      details.preventDefault();
    }
  };
  contents.on("will-navigate", (details) => guardNavigation(details));
  contents.on("will-frame-navigate", (details) => guardNavigation(details));
  contents.on("will-redirect", (details) => guardNavigation(details));
  contents.on("will-attach-webview", (event) => event.preventDefault());
  contents.setWindowOpenHandler(() => ({ action: "deny" }));
}
