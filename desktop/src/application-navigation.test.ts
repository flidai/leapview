import { describe, expect, test } from "bun:test";

import {
  canonicalExternalURL,
  exactProfileURL,
  pathIsInside,
  profilePartition,
} from "./application-navigation.js";
import type { Profile } from "./profiles.js";

const profile: Profile = {
  id: "profile_0123456789abcdef0123456789abcdef",
  canonicalOrigin: "https://analytics.company.com",
  instanceId: "instance_0123456789abcdef0123456789abcdef",
  displayName: "Analytics",
  lastSafePath: "/explore",
  partitionVersion: 1,
};

describe("desktop application navigation helpers", () => {
  test("keeps profile routes and partitions bound to the saved identity", () => {
    expect(exactProfileURL(profile, "/dashboards/sales")).toBe(
      "https://analytics.company.com/dashboards/sales",
    );
    expect(profilePartition(profile)).toBe(
      "persist:leapview-profile-0123456789abcdef0123456789abcdef",
    );
    expect(() => exactProfileURL(profile, "https://evil.example/")).toThrow(
      "route is not safe",
    );
  });

  test("accepts only bounded external destinations and containment", () => {
    expect(canonicalExternalURL("https://example.com/help", profile.canonicalOrigin)).toBe(
      "https://example.com/help",
    );
    expect(canonicalExternalURL("https://analytics.company.com/private", profile.canonicalOrigin)).toBeNull();
    expect(canonicalExternalURL("mailto:user@example.com", profile.canonicalOrigin)).toBe(
      "mailto:user@example.com",
    );
    expect(pathIsInside("/tmp/state/report.json", "/tmp/state")).toBe(true);
    expect(pathIsInside("/tmp/stateful/report.json", "/tmp/state")).toBe(false);
  });
});
