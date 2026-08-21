import { describe, expect, test } from "bun:test";

import { DesktopShutdownCoordinator } from "./application-shutdown.js";

describe("DesktopShutdownCoordinator", () => {
  test("coalesces before-quit callbacks and flushes exactly once", async () => {
    const coordinator = new DesktopShutdownCoordinator();
    let prevented = 0;
    let captures = 0;
    let flushes = 0;
    let quits = 0;
    let resolveFlush!: () => void;
    const flushed = new Promise<void>((resolve) => {
      resolveFlush = resolve;
    });
    const begin = () =>
      coordinator.begin(
        () => prevented++,
        () => captures++,
        async () => {
          flushes++;
          await flushed;
        },
        () => quits++,
      );

    begin();
    begin();
    expect({ prevented, captures, flushes, quits }).toEqual({
      prevented: 2,
      captures: 1,
      flushes: 1,
      quits: 0,
    });

    resolveFlush();
    await Promise.resolve();
    await Promise.resolve();
    expect(coordinator.isReady).toBe(true);
    expect(quits).toBe(1);

    begin();
    expect({ captures, flushes, quits }).toEqual({
      captures: 1,
      flushes: 1,
      quits: 1,
    });
  });
});
