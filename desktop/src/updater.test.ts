import { describe, expect, test } from "bun:test";

import {
  DESKTOP_UPDATE_CHANNEL,
  DESKTOP_UPDATE_ELECTRON_MAJOR,
  DESKTOP_UPDATE_ORIGIN,
  DesktopUpdater,
  desktopUpdateFeedURL,
  type DesktopAutoUpdater,
  type DesktopUpdateEvent,
} from "./updater.js";
import releasePolicy from "../release-policy.json";

class FakeAutoUpdater implements DesktopAutoUpdater {
  feedURL = "";
  checks = 0;
  installs = 0;
  readonly listeners = new Map<string, Array<(...args: never[]) => void>>();

  setFeedURL(options: { url: string }): void {
    this.feedURL = options.url;
  }

  checkForUpdates(): void {
    this.checks += 1;
  }

  quitAndInstall(): void {
    this.installs += 1;
  }

  on(event: string, listener: (...args: never[]) => void): this {
    const listeners = this.listeners.get(event) ?? [];
    listeners.push(listener);
    this.listeners.set(event, listeners);
    return this;
  }

  emit(event: string, ...args: unknown[]): void {
    for (const listener of this.listeners.get(event) ?? []) {
      listener(...(args as never[]));
    }
  }
}

const supportedRuntime = {
  platform: "win32" as const,
  architecture: "x64",
  applicationVersion: "1.2.3",
  electronVersion: "44.0.0",
  packaged: true,
  releaseChannel: "stable" as const,
};

describe("desktopUpdateFeedURL", () => {
  test("matches the reviewable release policy contract", () => {
    expect(releasePolicy.updates).toEqual({
      origin: DESKTOP_UPDATE_ORIGIN,
      pathVersion: "v1",
      channel: DESKTOP_UPDATE_CHANNEL,
      productName: "LeapView",
      applicationId: "dev.leapview.desktop",
      electronMajor: DESKTOP_UPDATE_ELECTRON_MAJOR,
      windowsPackageId: "leapview",
    });
  });

  test("binds the vendor stable channel to platform, architecture, and version", () => {
    expect(desktopUpdateFeedURL(supportedRuntime)).toBe(
      "https://releases.leapview.dev/desktop/v1/stable/win32/x64/1.2.3",
    );
    expect(
      desktopUpdateFeedURL({
        ...supportedRuntime,
        platform: "darwin",
        architecture: "arm64",
      }),
    ).toBe(
      "https://releases.leapview.dev/desktop/v1/stable/darwin/arm64/1.2.3",
    );
  });

  test("fails closed outside qualified packaged runtimes", () => {
    expect(
      desktopUpdateFeedURL({ ...supportedRuntime, packaged: false }),
    ).toBeNull();
    expect(
      desktopUpdateFeedURL({ ...supportedRuntime, platform: "linux" }),
    ).toBeNull();
    expect(
      desktopUpdateFeedURL({
        ...supportedRuntime,
        architecture: "ia32",
      }),
    ).toBeNull();
    expect(
      desktopUpdateFeedURL({
        ...supportedRuntime,
        electronVersion: "43.2.0",
      }),
    ).toBeNull();
    expect(
      desktopUpdateFeedURL({
        ...supportedRuntime,
        applicationVersion: "../latest",
      }),
    ).toBeNull();
    expect(
      desktopUpdateFeedURL({
        ...supportedRuntime,
        applicationVersion: "1.2.3-alpha.1",
      }),
    ).toBeNull();
    expect(
      desktopUpdateFeedURL({
        ...supportedRuntime,
        releaseChannel: "preview",
      }),
    ).toBeNull();
  });
});

describe("DesktopUpdater", () => {
  test("configures only the compiled vendor feed and serializes checks", () => {
    const native = new FakeAutoUpdater();
    const events: DesktopUpdateEvent[] = [];
    const updater = new DesktopUpdater({
      native,
      runtime: supportedRuntime,
      onEvent: (event) => events.push(event),
    });

    expect(updater.initialize()).toBe(true);
    expect(native.feedURL).toBe(
      "https://releases.leapview.dev/desktop/v1/stable/win32/x64/1.2.3",
    );
    expect(updater.check()).toBe("started");
    expect(updater.check()).toBe("busy");
    expect(native.checks).toBe(1);

    native.emit("update-available");
    expect(updater.snapshot()).toEqual({ phase: "downloading" });
    expect(updater.check()).toBe("busy");
    expect(events.map((event) => event.phase)).toEqual([
      "checking",
      "available",
    ]);
  });

  test("rejects stale, downgraded, and malformed downloaded targets", () => {
    for (const target of ["1.2.3", "1.2.2", "v1.2.2", "latest", "1.3.0-beta.1"]) {
      const native = new FakeAutoUpdater();
      const events: DesktopUpdateEvent[] = [];
      const updater = new DesktopUpdater({
        native,
        runtime: supportedRuntime,
        onEvent: (event) => events.push(event),
      });
      updater.initialize();
      updater.check();
      native.emit("update-available");
      native.emit(
        "update-downloaded",
        {},
        "<b>untrusted release notes</b>",
        target,
        new Date(),
        "https://attacker.example/update",
      );

      expect(updater.snapshot()).toEqual({ phase: "failed" });
      expect(updater.restart()).resolves.toBe(false);
      expect(native.installs).toBe(0);
      expect(events.at(-1)).toEqual({
        phase: "failed",
        reason: "invalid-target",
      });
    }
  });

  test("offers an explicit restart without exposing feed notes or URLs", async () => {
    const native = new FakeAutoUpdater();
    const events: DesktopUpdateEvent[] = [];
    let prepared = 0;
    const updater = new DesktopUpdater({
      native,
      runtime: supportedRuntime,
      onEvent: (event) => events.push(event),
      beforeRestart: async () => {
        prepared += 1;
      },
    });
    updater.initialize();
    updater.check();
    native.emit("update-available");
    native.emit(
      "update-downloaded",
      {},
      "<script>remote notes</script>",
      "1.3.0",
      new Date(),
      "https://attacker.example/update",
    );

    expect(updater.snapshot()).toEqual({
      phase: "ready",
      version: "1.3.0",
      deferred: false,
    });
    expect(updater.defer()).toBe(true);
    expect(updater.snapshot()).toEqual({
      phase: "ready",
      version: "1.3.0",
      deferred: true,
    });
    expect(await updater.restart()).toBe(true);
    expect(await updater.restart()).toBe(false);
    expect(prepared).toBe(1);
    expect(native.installs).toBe(1);
    expect(events).toEqual([
      { phase: "checking" },
      { phase: "available" },
      { phase: "downloaded", version: "1.3.0" },
      { phase: "deferred" },
      { phase: "restart-requested" },
    ]);
    expect(JSON.stringify(events)).not.toContain("attacker");
    expect(JSON.stringify(events)).not.toContain("remote notes");
  });

  test("recovers from native failures and restart-preparation failures", async () => {
    const native = new FakeAutoUpdater();
    const events: DesktopUpdateEvent[] = [];
    let preparationFails = true;
    const updater = new DesktopUpdater({
      native,
      runtime: supportedRuntime,
      onEvent: (event) => events.push(event),
      beforeRestart: async () => {
        if (preparationFails) {
          throw new Error("private path or profile detail");
        }
      },
    });
    updater.initialize();
    updater.check();
    native.emit("error", new Error("https://secret.example/feed failed"));
    expect(updater.snapshot()).toEqual({ phase: "failed" });
    expect(updater.check()).toBe("started");

    native.emit("update-available");
    native.emit("update-downloaded", {}, "", "1.3.0", new Date(), "");
    expect(await updater.restart()).toBe(false);
    expect(native.installs).toBe(0);
    expect(updater.snapshot()).toEqual({
      phase: "ready",
      version: "1.3.0",
      deferred: false,
    });

    preparationFails = false;
    expect(await updater.restart()).toBe(true);
    expect(native.installs).toBe(1);
    expect(events.filter((event) => event.phase === "failed")).toEqual([
      { phase: "failed", reason: "native" },
      { phase: "failed", reason: "restart-preparation" },
    ]);
  });

  test("does not configure or call the native updater when unsupported", () => {
    const native = new FakeAutoUpdater();
    const updater = new DesktopUpdater({
      native,
      runtime: { ...supportedRuntime, platform: "linux" },
      onEvent: () => undefined,
    });

    expect(updater.initialize()).toBe(false);
    expect(updater.snapshot()).toEqual({ phase: "disabled" });
    expect(updater.check()).toBe("unsupported");
    expect(native.feedURL).toBe("");
    expect(native.checks).toBe(0);
  });

  test("fails closed when the packaged runtime cannot initialize Squirrel", () => {
    const native = new FakeAutoUpdater();
    native.setFeedURL = () => {
      throw new Error("application is not signed");
    };
    const events: DesktopUpdateEvent[] = [];
    const updater = new DesktopUpdater({
      native,
      runtime: { ...supportedRuntime, platform: "darwin" },
      onEvent: (event) => events.push(event),
    });

    expect(updater.initialize()).toBe(false);
    expect(updater.snapshot()).toEqual({ phase: "disabled" });
    expect(updater.check()).toBe("unsupported");
    expect(events).toEqual([{ phase: "failed", reason: "native" }]);
  });

  test("keeps a staged update recoverable when native restart throws", async () => {
    const native = new FakeAutoUpdater();
    native.quitAndInstall = () => {
      throw new Error("native restart unavailable");
    };
    const events: DesktopUpdateEvent[] = [];
    const updater = new DesktopUpdater({
      native,
      runtime: supportedRuntime,
      onEvent: (event) => events.push(event),
    });
    updater.initialize();
    updater.check();
    native.emit("update-available");
    native.emit("update-downloaded", {}, "", "1.3.0", new Date(), "");

    expect(await updater.restart()).toBe(false);
    expect(updater.snapshot()).toEqual({
      phase: "ready",
      version: "1.3.0",
      deferred: false,
    });
    expect(events.at(-1)).toEqual({ phase: "failed", reason: "native" });
  });
});
