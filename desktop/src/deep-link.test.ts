import { describe, expect, test } from "bun:test";

import {
  DeepLinkDispatcher,
  desktopDeepLinkFromArguments,
  parseDesktopDeepLink,
  routeDesktopDeepLink,
  type DesktopDeepLink,
} from "./deep-link.js";

const dashboardLink =
  "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fdashboards%2Frevenue%2Fpages%2Foverview";

describe("parseDesktopDeepLink", () => {
  test("accepts only canonical insights, develop, and dashboard routes", () => {
    for (const path of [
      "/",
      "/explore",
      "/data",
      "/models",
      "/semantic-models",
      "/pipelines",
      "/connections",
      "/dashboards/revenue",
      "/dashboards/revenue/pages/overview",
    ]) {
      const candidate = new URL("leapview-desktop://open");
      candidate.searchParams.set("origin", "https://analytics.company.com");
      candidate.searchParams.set("path", path);
      expect(parseDesktopDeepLink(candidate.toString())).toEqual({
        origin: "https://analytics.company.com",
        path,
      });
    }
  });

  test("allows loopback HTTP only when explicitly enabled for development", () => {
    const candidate =
      "leapview-desktop://open?origin=http%3A%2F%2F127.0.0.1%3A8149&path=%2Fexplore";

    expect(() => parseDesktopDeepLink(candidate)).toThrow("invalid");
    expect(
      parseDesktopDeepLink(candidate, { allowLoopbackHTTP: true }),
    ).toEqual({
      origin: "http://127.0.0.1:8149",
      path: "/explore",
    });
  });

  test("rejects ambiguous authority, unsafe routes, encoding tricks, and excess input", () => {
    const candidates = [
      "leapview://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fexplore",
      "LEAPVIEW-DESKTOP://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fexplore",
      "leapview-desktop://close?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fexplore",
      "leapview-desktop://user:secret@open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fexplore",
      "leapview-desktop://open:80?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fexplore",
      "leapview-desktop://open/path?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fexplore",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fexplore#fragment",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com",
      "leapview-desktop://open?path=%2Fexplore",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&origin=https%3A%2F%2Fattacker.example&path=%2Fexplore",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fexplore&next=https%3A%2F%2Fattacker.example",
      "leapview-desktop://open?origin=https%3A%2F%2Fuser%3Asecret%40analytics.company.com&path=%2Fexplore",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com%2Fpath&path=%2Fexplore",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fadmin",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fprojects%2Fsales",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fdashboards%2Frevenue%3Ffilter%3Dsecret",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fdashboards%2F..%2Fadmin",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fdashboards%2F%252e%252e%2Fadmin",
      "leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fdashboards%255cadmin",
      `leapview-desktop://open?origin=https%3A%2F%2Fanalytics.company.com&path=%2Fdashboards%2F${"a".repeat(2_048)}`,
    ];

    for (const candidate of candidates) {
      expect(() => parseDesktopDeepLink(candidate)).toThrow(
        "invalid",
        candidate,
      );
    }
  });
});

describe("desktopDeepLinkFromArguments", () => {
  test("extracts one exact protocol argument without trusting argument order", () => {
    expect(
      desktopDeepLinkFromArguments([
        "/Applications/LeapView",
        "--original-process-start-time=123",
        dashboardLink,
        "--flag",
      ]),
    ).toBe(dashboardLink);
    expect(desktopDeepLinkFromArguments(["/Applications/LeapView"])).toBeNull();
  });

  test("rejects multiple protocol arguments instead of choosing one", () => {
    expect(() =>
      desktopDeepLinkFromArguments([dashboardLink, dashboardLink]),
    ).toThrow("multiple");
  });
});

describe("routeDesktopDeepLink", () => {
  const request: DesktopDeepLink = {
    origin: "https://analytics.company.com",
    path: "/dashboards/revenue",
  };
  const profile = {
    id: "profile_0123456789abcdef0123456789abcdef",
    canonicalOrigin: request.origin,
  };

  test("opens an exact known instance without another trust prompt", async () => {
    const opened: unknown[] = [];
    let confirmations = 0;

    await routeDesktopDeepLink(request, "second-instance", {
      listProfiles: async () => [profile],
      openKnown: async (...arguments_) => opened.push(arguments_),
      confirmUnknown: async () => {
        confirmations += 1;
        return true;
      },
      connectUnknown: async () => undefined,
      rejectUnknown: () => undefined,
    });

    expect(opened).toEqual([[profile, request.path]]);
    expect(confirmations).toBe(0);
  });

  test("never onboards an unknown instance from a secondary process", async () => {
    let confirmed = false;
    let connected = false;
    let rejected = false;

    await routeDesktopDeepLink(request, "second-instance", {
      listProfiles: async () => [],
      openKnown: async () => undefined,
      confirmUnknown: async () => {
        confirmed = true;
        return true;
      },
      connectUnknown: async () => {
        connected = true;
      },
      rejectUnknown: () => {
        rejected = true;
      },
    });

    expect({ confirmed, connected, rejected }).toEqual({
      confirmed: false,
      connected: false,
      rejected: true,
    });
  });

  test("requires explicit confirmation before cold-start onboarding", async () => {
    const connected: DesktopDeepLink[] = [];

    await routeDesktopDeepLink(request, "cold-start", {
      listProfiles: async () => [],
      openKnown: async () => undefined,
      confirmUnknown: async (candidate) => candidate === request,
      connectUnknown: async (candidate) => {
        connected.push(candidate);
      },
      rejectUnknown: () => undefined,
    });

    expect(connected).toEqual([request]);
  });
});

describe("DeepLinkDispatcher", () => {
  test("bounds pre-ready work and serializes validated requests after attach", async () => {
    const rejected: string[] = [];
    const dispatcher = new DeepLinkDispatcher({
      onRejected: (reason) => rejected.push(reason),
    });
    const first = parseDesktopDeepLink(dashboardLink);
    const secondURL = new URL(dashboardLink);
    secondURL.searchParams.set("path", "/explore");
    const second = parseDesktopDeepLink(secondURL.toString());
    let releaseFirst: () => void = () => undefined;
    const firstGate = new Promise<void>((resolve) => {
      releaseFirst = resolve;
    });
    let active = 0;
    let maximumActive = 0;
    const handled: DesktopDeepLink[] = [];

    expect(dispatcher.acceptURL(dashboardLink, "cold-start")).toBe(true);
    dispatcher.attach(async (request) => {
      active += 1;
      maximumActive = Math.max(maximumActive, active);
      handled.push(request);
      if (handled.length === 1) {
        await firstGate;
      }
      active -= 1;
    });
    expect(
      dispatcher.acceptURL(secondURL.toString(), "open-url"),
    ).toBe(true);
    expect(
      dispatcher.acceptURL("leapview-desktop://attacker", "open-url"),
    ).toBe(false);
    releaseFirst();
    await dispatcher.idle();

    expect(handled).toEqual([first, second]);
    expect(maximumActive).toBe(1);
    expect(rejected).toEqual(["invalid"]);
  });

  test("rejects a fifth outstanding request and multiple argv links", () => {
    const rejected: string[] = [];
    const dispatcher = new DeepLinkDispatcher({
      onRejected: (reason) => rejected.push(reason),
    });
    for (let index = 0; index < 4; index += 1) {
      expect(dispatcher.acceptURL(dashboardLink, "open-url")).toBe(true);
    }
    expect(dispatcher.acceptURL(dashboardLink, "open-url")).toBe(false);
    expect(
      dispatcher.acceptArguments(
        [dashboardLink, dashboardLink],
        "second-instance",
      ),
    ).toBe(false);
    expect(rejected).toEqual(["overloaded", "multiple"]);
  });
});
