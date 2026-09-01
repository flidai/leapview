import { describe, expect, test } from "bun:test";
import type {
  MessageBoxOptions,
  MessageBoxReturnValue,
} from "electron";

import { DesktopUpdateCoordinator } from "./update-coordinator.js";
import type {
  DesktopAutoUpdater,
  DesktopUpdateEvent,
} from "./updater.js";

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

function messageResult(response: number): MessageBoxReturnValue {
  return { response, checkboxChecked: false };
}

function coordinator(options: {
  native?: FakeAutoUpdater;
  platform?: NodeJS.Platform;
  responses?: number[];
  messages?: MessageBoxOptions[];
  events?: DesktopUpdateEvent[];
  beforeRestart?: () => Promise<void>;
}) {
  const native = options.native ?? new FakeAutoUpdater();
  const messages = options.messages ?? [];
  const events = options.events ?? [];
  const responses = [...(options.responses ?? [0])];
  const updates = new DesktopUpdateCoordinator({
    native,
    runtime: {
      platform: options.platform ?? "win32",
      architecture: "x64",
      applicationVersion: "1.2.3",
      electronVersion: "44.0.0",
      packaged: true,
      releaseChannel: "stable",
    },
    showMessageBox: async (message) => {
      messages.push(message);
      return messageResult(responses.shift() ?? 0);
    },
    recordEvent: (event) => events.push(event),
    beforeRestart:
      options.beforeRestart ?? (() => Promise.resolve()),
  });
  updates.initialize();
  return { updates, native, messages, events };
}

describe("DesktopUpdateCoordinator", () => {
  test("explains the package-manager path without invoking Electron on Linux", async () => {
    const context = coordinator({ platform: "linux" });

    await context.updates.checkManually();

    expect(context.native.checks).toBe(0);
    expect(context.messages).toHaveLength(1);
    expect(context.messages[0]?.message).toContain("system package manager");
    expect(JSON.stringify(context.messages)).not.toContain("http");
  });

  test("reports a completed manual check using only trusted copy", async () => {
    const context = coordinator({});

    await context.updates.checkManually();
    context.native.emit("update-not-available");
    await Promise.resolve();

    expect(context.native.checks).toBe(1);
    expect(context.messages.at(-1)?.message).toBe(
      "LeapView is up to date.",
    );
    expect(context.events).toEqual([
      { phase: "checking" },
      { phase: "not-available" },
    ]);
  });

  test("uses an explicit accessible defer-first restart decision", async () => {
    const context = coordinator({ responses: [0] });

    await context.updates.checkManually();
    context.native.emit("update-available");
    context.native.emit(
      "update-downloaded",
      {},
      "<script>untrusted notes</script>",
      "1.3.0",
      new Date(),
      "https://attacker.example/update",
    );
    await Promise.resolve();

    const message = context.messages.at(-1);
    expect(message).toMatchObject({
      buttons: ["Later", "Restart now"],
      defaultId: 0,
      cancelId: 0,
      noLink: true,
      title: "LeapView update ready",
      message: "LeapView 1.3.0 is ready to install.",
    });
    expect(context.native.installs).toBe(0);
    expect(context.events.at(-1)).toEqual({ phase: "deferred" });
    expect(JSON.stringify(context.messages)).not.toContain("attacker");
    expect(JSON.stringify(context.messages)).not.toContain("untrusted");
  });

  test("flushes trusted state before requesting the native restart", async () => {
    const order: string[] = [];
    const native = new FakeAutoUpdater();
    native.quitAndInstall = () => {
      order.push("install");
      native.installs += 1;
    };
    const context = coordinator({
      native,
      responses: [1],
      beforeRestart: async () => {
        order.push("flush");
      },
    });

    await context.updates.checkManually();
    native.emit("update-available");
    native.emit("update-downloaded", {}, "", "1.3.0", new Date(), "");
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(order).toEqual(["flush", "install"]);
    expect(context.events.at(-1)).toEqual({
      phase: "restart-requested",
    });
  });

  test("does not expose a native updater error in the manual failure UI", async () => {
    const context = coordinator({});

    await context.updates.checkManually();
    context.native.emit(
      "error",
      new Error("token=secret https://private.example/update"),
    );
    await Promise.resolve();

    expect(context.messages.at(-1)?.message).toBe(
      "LeapView could not complete the update check safely.",
    );
    expect(JSON.stringify(context.messages)).not.toContain("secret");
    expect(JSON.stringify(context.messages)).not.toContain("private");
  });
});
