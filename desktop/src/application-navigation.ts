import { isAbsolute, relative, resolve, sep } from "node:path";

import {
  profilePartitionName,
  type Profile,
} from "./profiles.js";
import { isSafeDesktopRoute } from "./safe-route.js";

export function exactProfileURL(profile: Profile, path: string): string {
  const target = new URL(path, profile.canonicalOrigin);
  if (
    !isSafeDesktopRoute(path) ||
    target.origin !== profile.canonicalOrigin ||
    target.hash !== ""
  ) {
    throw new Error("LeapView route is not safe for the saved instance.");
  }
  return target.toString();
}

export function profilePartition(profile: Profile): string {
  return profilePartitionName(profile);
}

export function pathIsInside(candidate: string, parent: string): boolean {
  const relationship = relative(resolve(parent), resolve(candidate));
  return (
    relationship === "" ||
    (relationship !== ".." &&
      !relationship.startsWith(".." + sep) &&
      !isAbsolute(relationship))
  );
}

export function canonicalExternalURL(
  candidate: string,
  configuredOrigin: string,
): string | null {
  if (new TextEncoder().encode(candidate).byteLength > 2_048) {
    return null;
  }
  let parsed: URL;
  try {
    parsed = new URL(candidate);
  } catch {
    return null;
  }
  if (
    parsed.protocol === "https:" &&
    parsed.origin !== configuredOrigin &&
    parsed.username === "" &&
    parsed.password === ""
  ) {
    return parsed.toString();
  }
  if (
    parsed.protocol === "mailto:" &&
    parsed.search === "" &&
    parsed.hash === "" &&
    /^[A-Za-z0-9.!#$%&'*+\/?^_\x60{|}~-]{1,64}@[A-Za-z0-9.-]{1,189}$/u.test(
      parsed.pathname,
    )
  ) {
    return parsed.toString();
  }
  return null;
}
