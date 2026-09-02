export const DESKTOP_UPDATE_ELECTRON_MAJOR = 44;
export const DESKTOP_UPDATE_ORIGIN = "https://releases.leapview.dev";
export const DESKTOP_UPDATE_CHANNEL = "stable";

const stableVersionPattern =
  /^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/u;

export interface DesktopUpdateRuntime {
  platform: NodeJS.Platform;
  architecture: string;
  applicationVersion: string;
  electronVersion: string;
  packaged: boolean;
  releaseChannel: "development" | "preview" | "stable" | "invalid";
}

export interface DesktopAutoUpdater {
  setFeedURL(options: { url: string }): void;
  checkForUpdates(): void;
  quitAndInstall(): void;
  on(event: string, listener: (...args: never[]) => void): unknown;
}

export type DesktopUpdateEvent =
  | { phase: "checking" }
  | { phase: "available" }
  | { phase: "not-available" }
  | { phase: "downloaded"; version: string }
  | { phase: "deferred" }
  | { phase: "restart-requested" }
  | {
      phase: "failed";
      reason: "native" | "invalid-target" | "restart-preparation";
    };

export type DesktopUpdateState =
  | { phase: "disabled" }
  | { phase: "idle" }
  | { phase: "checking" }
  | { phase: "downloading" }
  | { phase: "failed" }
  | {
      phase: "ready";
      version: string;
      deferred: boolean;
    }
  | { phase: "restarting"; version: string };

export interface DesktopUpdaterOptions {
  native: DesktopAutoUpdater;
  runtime: DesktopUpdateRuntime;
  onEvent: (event: DesktopUpdateEvent) => void;
  beforeRestart?: () => Promise<void>;
}

export function desktopUpdateFeedURL(
  runtime: DesktopUpdateRuntime,
): string | null {
  if (!runtime.packaged || runtime.releaseChannel !== "stable") {
    return null;
  }
  if (
    runtime.platform !== "darwin" &&
    runtime.platform !== "win32"
  ) {
    return null;
  }
  if (
    (runtime.platform === "darwin" &&
      runtime.architecture !== "arm64" &&
      runtime.architecture !== "x64") ||
    (runtime.platform === "win32" && runtime.architecture !== "x64")
  ) {
    return null;
  }
  if (
    majorVersion(runtime.electronVersion) !==
      DESKTOP_UPDATE_ELECTRON_MAJOR ||
    parseStableVersion(runtime.applicationVersion) === null
  ) {
    return null;
  }
  return [
    DESKTOP_UPDATE_ORIGIN,
    "desktop",
    "v1",
    DESKTOP_UPDATE_CHANNEL,
    runtime.platform,
    runtime.architecture,
    runtime.applicationVersion,
  ].join("/");
}

export class DesktopUpdater {
  readonly #native: DesktopAutoUpdater;
  readonly #runtime: DesktopUpdateRuntime;
  readonly #onEvent: (event: DesktopUpdateEvent) => void;
  readonly #beforeRestart: () => Promise<void>;
  #state: DesktopUpdateState = { phase: "disabled" };
  #initialized = false;
  #restartInFlight = false;

  constructor(options: DesktopUpdaterOptions) {
    this.#native = options.native;
    this.#runtime = structuredClone(options.runtime);
    this.#onEvent = options.onEvent;
    this.#beforeRestart =
      options.beforeRestart ?? (() => Promise.resolve());
  }

  initialize(): boolean {
    if (this.#initialized) {
      return this.#state.phase !== "disabled";
    }
    this.#initialized = true;
    const feedURL = desktopUpdateFeedURL(this.#runtime);
    if (feedURL === null) {
      return false;
    }
    try {
      this.#native.setFeedURL({ url: feedURL });
      this.#bindEvents();
    } catch {
      this.#onEvent({ phase: "failed", reason: "native" });
      return false;
    }
    this.#state = { phase: "idle" };
    return true;
  }

  snapshot(): DesktopUpdateState {
    return structuredClone(this.#state);
  }

  check(): "started" | "busy" | "unsupported" {
    if (this.#state.phase === "disabled") {
      return "unsupported";
    }
    if (
      this.#state.phase === "checking" ||
      this.#state.phase === "downloading" ||
      this.#state.phase === "ready" ||
      this.#state.phase === "restarting"
    ) {
      return "busy";
    }
    this.#state = { phase: "checking" };
    this.#onEvent({ phase: "checking" });
    try {
      this.#native.checkForUpdates();
    } catch {
      this.#fail("native");
    }
    return "started";
  }

  defer(): boolean {
    if (this.#state.phase !== "ready" || this.#state.deferred) {
      return false;
    }
    this.#state = { ...this.#state, deferred: true };
    this.#onEvent({ phase: "deferred" });
    return true;
  }

  async restart(): Promise<boolean> {
    if (this.#state.phase !== "ready" || this.#restartInFlight) {
      return false;
    }
    const ready = this.#state;
    this.#restartInFlight = true;
    this.#onEvent({ phase: "restart-requested" });
    try {
      await this.#beforeRestart();
    } catch {
      this.#restartInFlight = false;
      this.#onEvent({
        phase: "failed",
        reason: "restart-preparation",
      });
      return false;
    }
    this.#state = { phase: "restarting", version: ready.version };
    try {
      this.#native.quitAndInstall();
    } catch {
      this.#state = ready;
      this.#restartInFlight = false;
      this.#onEvent({ phase: "failed", reason: "native" });
      return false;
    }
    return true;
  }

  #bindEvents(): void {
    this.#native.on("checking-for-update", () => {
      // check() records the transition before invoking Electron so a
      // synchronous native event cannot create a duplicate state change.
    });
    this.#native.on("update-available", () => {
      if (this.#state.phase !== "checking") {
        return;
      }
      this.#state = { phase: "downloading" };
      this.#onEvent({ phase: "available" });
    });
    this.#native.on("update-not-available", () => {
      if (
        this.#state.phase !== "checking" &&
        this.#state.phase !== "downloading"
      ) {
        return;
      }
      this.#state = { phase: "idle" };
      this.#onEvent({ phase: "not-available" });
    });
    this.#native.on("error", () => {
      if (
        this.#state.phase === "checking" ||
        this.#state.phase === "downloading"
      ) {
        this.#fail("native");
      }
    });
    this.#native.on(
      "update-downloaded",
      (
        _event: unknown,
        _releaseNotes: unknown,
        releaseName: unknown,
      ) => {
        if (
          this.#state.phase !== "checking" &&
          this.#state.phase !== "downloading"
        ) {
          return;
        }
        if (
          typeof releaseName !== "string" ||
          !isNewerStableVersion(
            releaseName,
            this.#runtime.applicationVersion,
          )
        ) {
          this.#fail("invalid-target");
          return;
        }
        this.#state = {
          phase: "ready",
          version: releaseName,
          deferred: false,
        };
        this.#onEvent({
          phase: "downloaded",
          version: releaseName,
        });
      },
    );
  }

  #fail(
    reason: Extract<DesktopUpdateEvent, { phase: "failed" }>["reason"],
  ): void {
    this.#state = { phase: "failed" };
    this.#onEvent({ phase: "failed", reason });
  }
}

function majorVersion(version: string): number | null {
  const match = /^(0|[1-9][0-9]*)\./u.exec(version);
  if (match?.[1] === undefined) {
    return null;
  }
  const value = Number(match[1]);
  return Number.isSafeInteger(value) ? value : null;
}

function parseStableVersion(
  version: string,
): readonly [number, number, number] | null {
  const match = stableVersionPattern.exec(version);
  if (match === null) {
    return null;
  }
  const values = match
    .slice(1)
    .map((component) => Number(component)) as [number, number, number];
  return values.every(Number.isSafeInteger) ? values : null;
}

function isNewerStableVersion(
  candidate: string,
  current: string,
): boolean {
  const candidateVersion = parseStableVersion(candidate);
  const currentVersion = parseStableVersion(current);
  if (candidateVersion === null || currentVersion === null) {
    return false;
  }
  for (let index = 0; index < candidateVersion.length; index += 1) {
    const candidatePart = candidateVersion[index] ?? 0;
    const currentPart = currentVersion[index] ?? 0;
    if (candidatePart !== currentPart) {
      return candidatePart > currentPart;
    }
  }
  return false;
}
