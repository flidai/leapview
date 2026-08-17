import { describe, expect, test } from "bun:test";
import { EventEmitter } from "node:events";

import { installRemoteLifecyclePolicy } from "./remote-lifecycle.js";

describe("installRemoteLifecyclePolicy", () => {
  test("reports exact-origin main-frame network failures once", () => {
    const contents = new EventEmitter();
    const failures: unknown[] = [];
    installRemoteLifecyclePolicy(
      contents,
      {
        origin: "https://analytics.company.com",
        displayName: "Company Analytics",
      },
      (failure) => failures.push(failure),
    );

    contents.emit(
      "did-fail-load",
      {},
      -105,
      "NAME_NOT_RESOLVED",
      "https://analytics.company.com/dashboards/sales",
      true,
    );
    contents.emit(
      "did-fail-load",
      {},
      -105,
      "NAME_NOT_RESOLVED",
      "https://analytics.company.com/dashboards/sales",
      true,
    );

    expect(failures).toEqual([
      {
        state: "offline",
        message:
          "Company Analytics could not be reached. Check the network or server.",
      },
    ]);
  });

  test("ignores aborted, subframe, and foreign-origin loads", () => {
    const contents = new EventEmitter();
    const failures: unknown[] = [];
    installRemoteLifecyclePolicy(
      contents,
      {
        origin: "https://analytics.company.com",
        displayName: "Company Analytics",
      },
      (failure) => failures.push(failure),
    );

    contents.emit("did-fail-load", {}, -3, "ABORTED", "https://analytics.company.com/", true);
    contents.emit("did-fail-load", {}, -105, "FAILED", "https://analytics.company.com/frame", false);
    contents.emit("did-fail-load", {}, -105, "FAILED", "https://attacker.example/", true);

    expect(failures).toEqual([]);
  });

  test("reports an unexpected renderer exit as a crash", () => {
    const contents = new EventEmitter();
    const failures: unknown[] = [];
    installRemoteLifecyclePolicy(
      contents,
      {
        origin: "https://analytics.company.com",
        displayName: "Company Analytics",
      },
      (failure) => failures.push(failure),
    );

    contents.emit("render-process-gone", {}, {
      reason: "crashed",
      exitCode: 1,
    });

    expect(failures).toEqual([
      {
        state: "crashed",
        message:
          "Company Analytics stopped unexpectedly.",
      },
    ]);
  });

  test("remembers only same-origin fragment-free main-frame routes", () => {
    const contents = new EventEmitter();
    const routes: string[] = [];
    installRemoteLifecyclePolicy(
      contents,
      {
        origin: "https://analytics.company.com",
        displayName: "Company Analytics",
      },
      () => undefined,
      (route) => routes.push(route),
    );

    contents.emit(
      "did-navigate",
      {},
      "https://analytics.company.com/dashboards/sales?period=q1#visual",
      200,
      "OK",
    );
    contents.emit(
      "did-navigate-in-page",
      {},
      "https://analytics.company.com/dashboards/finance?period=q2",
      true,
    );
    contents.emit(
      "did-navigate-in-page",
      {},
      "https://analytics.company.com/dashboards/ignored",
      false,
    );
    contents.emit(
      "did-navigate",
      {},
      "https://attacker.example/dashboards/stolen",
      200,
      "OK",
    );
    contents.emit(
      "did-navigate",
      {},
      "https://user@analytics.company.com/dashboards/credentialed",
      200,
      "OK",
    );
    contents.emit(
      "did-navigate",
      {},
      "https://analytics.company.com/admin",
      200,
      "OK",
    );

    expect(routes).toEqual([
      "/dashboards/sales",
      "/dashboards/finance",
    ]);
  });
});
