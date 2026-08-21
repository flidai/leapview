import { describe, expect, test } from "bun:test";
import { join } from "node:path";

import {
  PREVIEW_DISTRIBUTION_MARKER,
  STABLE_DISTRIBUTION_MARKER,
  resolveDesktopDistribution,
} from "./distribution.js";

describe("resolveDesktopDistribution", () => {
  test("keeps unpackaged development builds off release channels", () => {
    expect(
      resolveDesktopDistribution({
        packaged: false,
        resourcesPath: "/unused",
        readFile: () => {
          throw new Error("development must not inspect packaged resources");
        },
      }),
    ).toBe("development");
  });

  test("recognizes only the exact packaged preview marker", () => {
    expect(
      resolveDesktopDistribution({
        packaged: true,
        resourcesPath: "/application/resources",
        readFile: (path) => {
          if (
            path === join(
              "/application/resources",
              PREVIEW_DISTRIBUTION_MARKER,
            )
          ) {
            return '{"schemaVersion":1,"channel":"preview","updates":false}\n';
          }
          const error = new Error("missing") as NodeJS.ErrnoException;
          error.code = "ENOENT";
          throw error;
        },
      }),
    ).toBe("preview");
  });

  test("recognizes packaged markers checked out with Windows line endings", () => {
    expect(
      resolveDesktopDistribution({
        packaged: true,
        resourcesPath: "/application/resources",
        readFile: (path) => {
          if (path.endsWith(PREVIEW_DISTRIBUTION_MARKER)) {
            return '{"schemaVersion":1,"channel":"preview","updates":false}\r\n';
          }
          const error = new Error("missing") as NodeJS.ErrnoException;
          error.code = "ENOENT";
          throw error;
        },
      }),
    ).toBe("preview");
  });

  test("recognizes only the exact packaged stable marker", () => {
    expect(
      resolveDesktopDistribution({
        packaged: true,
        resourcesPath: "/application/resources",
        readFile: (path) => {
          if (
            path === join(
              "/application/resources",
              STABLE_DISTRIBUTION_MARKER,
            )
          ) {
            return '{"schemaVersion":1,"channel":"stable","updates":true}\n';
          }
          const error = new Error("missing") as NodeJS.ErrnoException;
          error.code = "ENOENT";
          throw error;
        },
      }),
    ).toBe("stable");
  });

  test("fails closed when both packaged markers are absent", () => {
    expect(
      resolveDesktopDistribution({
        packaged: true,
        resourcesPath: "/application/resources",
        readFile: () => {
          const error = new Error("missing") as NodeJS.ErrnoException;
          error.code = "ENOENT";
          throw error;
        },
      }),
    ).toBe("invalid");
  });

  test("fails closed when a packaged marker is malformed", () => {
    expect(
      resolveDesktopDistribution({
        packaged: true,
        resourcesPath: "/application/resources",
        readFile: (path) =>
          path.endsWith(PREVIEW_DISTRIBUTION_MARKER)
            ? '{"schemaVersion":1,"channel":"preview"}'
            : (() => {
                const error = new Error("missing") as NodeJS.ErrnoException;
                error.code = "ENOENT";
                throw error;
              })(),
      }),
    ).toBe("invalid");
  });

  test("fails closed when both packaged markers are present", () => {
    expect(
      resolveDesktopDistribution({
        packaged: true,
        resourcesPath: "/application/resources",
        readFile: (path) =>
          path.endsWith(PREVIEW_DISTRIBUTION_MARKER)
            ? '{"schemaVersion":1,"channel":"preview","updates":false}\n'
            : '{"schemaVersion":1,"channel":"stable","updates":true}\n',
      }),
    ).toBe("invalid");
  });
});
