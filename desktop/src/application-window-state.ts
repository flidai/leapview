import { screen, type BrowserWindow as BrowserWindowType } from "electron";

import {
  fitWindowStateToWorkArea,
  WindowStateStore,
  type PersistedWindowState,
} from "./window-state.js";

type WindowRecord = {
  window: BrowserWindowType;
  key: string;
  minimumWidth: number;
  minimumHeight: number;
};

export class DesktopWindowStateCoordinator {
  private flushTimer: NodeJS.Timeout | null = null;

  private constructor(
    private readonly store: WindowStateStore,
    private readonly flushDelayMs: number,
  ) {}

  public static async open(
    path: string,
    flushDelayMs = 300,
  ): Promise<DesktopWindowStateCoordinator> {
    return new DesktopWindowStateCoordinator(
      await WindowStateStore.open(path),
      flushDelayMs,
    );
  }

  public restore(
    key: string,
    minimumWidth: number,
    minimumHeight: number,
  ): PersistedWindowState | undefined {
    const saved = this.store.get(key);
    if (saved === undefined) return undefined;
    const display = screen.getDisplayMatching(saved.bounds);
    return fitWindowStateToWorkArea(saved, display.workArea, {
      width: minimumWidth,
      height: minimumHeight,
    });
  }

  public track(
    window: BrowserWindowType,
    key: string,
    minimumWidth: number,
    minimumHeight: number,
  ): void {
    const capture = (): void => {
      this.capture(window, key);
      this.scheduleFlush();
    };
    window.on("move", capture);
    window.on("resize", capture);
    window.on("maximize", capture);
    window.on("unmaximize", capture);
    window.on("closed", () => this.scheduleFlush());
    this.keepVisible(window, key, minimumWidth, minimumHeight);
    this.capture(window, key);
  }

  public capture(window: BrowserWindowType, key: string): void {
    if (window.isDestroyed()) return;
    this.store.record(key, {
      bounds: window.getNormalBounds(),
      maximized: window.isMaximized(),
    });
  }

  public scheduleFlush(): void {
    if (this.flushTimer !== null) clearTimeout(this.flushTimer);
    this.flushTimer = setTimeout(() => {
      this.flushTimer = null;
      void this.flush();
    }, this.flushDelayMs);
  }

  public async flush(): Promise<void> {
    if (this.flushTimer !== null) {
      clearTimeout(this.flushTimer);
      this.flushTimer = null;
    }
    try {
      await this.store.flush();
    } catch {
      console.warn("LeapView Desktop could not save window placement.");
    }
  }

  public remove(key: string): void {
    this.store.remove(key);
  }

  public captureAll(
    shellWindow: BrowserWindowType | null,
    remotes: ReadonlyMap<string, BrowserWindowType>,
  ): void {
    if (shellWindow !== null && !shellWindow.isDestroyed()) {
      this.capture(shellWindow, "shell");
    }
    for (const [profileID, remote] of remotes) {
      this.capture(remote, profileID);
    }
  }

  public keepAllVisible(
    shellWindow: BrowserWindowType | null,
    remotes: ReadonlyMap<string, BrowserWindowType>,
    shellMinimum: { width: number; height: number },
    remoteMinimum: { width: number; height: number },
  ): void {
    if (shellWindow !== null && !shellWindow.isDestroyed()) {
      this.keepVisible(
        shellWindow,
        "shell",
        shellMinimum.width,
        shellMinimum.height,
      );
    }
    for (const [profileID, remote] of remotes) {
      this.keepVisible(
        remote,
        profileID,
        remoteMinimum.width,
        remoteMinimum.height,
      );
    }
  }

  private keepVisible(
    window: BrowserWindowType,
    key: string,
    minimumWidth: number,
    minimumHeight: number,
  ): void {
    if (
      window.isDestroyed() ||
      window.isMaximized() ||
      window.isMinimized() ||
      window.isFullScreen()
    ) {
      return;
    }
    const current = window.getNormalBounds();
    const display = screen.getDisplayMatching(current);
    const fitted = fitWindowStateToWorkArea(
      { bounds: current, maximized: false },
      display.workArea,
      { width: minimumWidth, height: minimumHeight },
    );
    if (
      current.x !== fitted.bounds.x ||
      current.y !== fitted.bounds.y ||
      current.width !== fitted.bounds.width ||
      current.height !== fitted.bounds.height
    ) {
      window.setBounds(fitted.bounds);
    }
    this.store.record(key, fitted);
  }
}
