import { describe, expect, test } from "bun:test";
import { readFile } from "node:fs/promises";

import { createRemoteWindow } from "./security/remote-window.mjs";

describe("createRemoteWindow", () => {
  test("installs the production session and contents invariants before navigation", () => {
    const calls: string[] = [];
    const handlers = new Map<string, (...arguments_: unknown[]) => void>();
    const contents = {
      on: (name: string, handler: (...arguments_: unknown[]) => void) => {
        if (name === "will-navigate") {
          calls.push("contents-policy");
        }
        handlers.set(name, handler);
      },
      setWindowOpenHandler: () => {},
      loadURL: async () => {},
    };
    const remote = {
      webContents: contents,
      once: (name: string, handler: (...arguments_: unknown[]) => void) => {
        handlers.set(name, handler);
      },
      maximize: () => calls.push("maximize"),
      setTitle: () => calls.push("title"),
      show: () => calls.push("show"),
    };

    const created = createRemoteWindow({
      partition: "persist:leapview-profile-0123456789abcdef",
      canonicalOrigin: "https://analytics.example.com",
      displayName: "Production",
      restoredState: {
        bounds: { x: 10, y: 20, width: 1200, height: 800 },
        maximized: true,
      },
      createWindow: (options) => {
        calls.push("create");
        expect(options.webPreferences).toMatchObject({
          partition: "persist:leapview-profile-0123456789abcdef",
          nodeIntegration: false,
          contextIsolation: true,
          sandbox: true,
          webviewTag: false,
        });
        return remote as never;
      },
      installLifecyclePolicy: () => calls.push("lifecycle-policy"),
      onDecision: () => {},
      requestExternalOpen: async () => {},
      onFailure: () => {},
      onSafeRoute: async () => {},
      onClosed: () => calls.push("closed"),
    });

    expect(created).toBe(remote);
    expect(calls.slice(0, 4)).toEqual([
      "create",
      "contents-policy",
      "lifecycle-policy",
      "maximize",
    ]);
    expect(handlers.has("page-title-updated")).toBe(true);
    expect(handlers.has("ready-to-show")).toBe(true);
    expect(handlers.has("closed")).toBe(true);
  });

  test("keeps the executable entrypoint as a small application composition root", async () => {
    const entrypoint = await readFile(
      new URL("./main.ts", import.meta.url),
      "utf8",
    );
    expect(entrypoint.replace(/\r\n?/gu, "\n")).toBe(
      'import "./application.js";\n',
    );
  });
});
