import {
  DesktopDiscoveryError,
} from "./discovery.js";

/** Pure recovery classification shared by the application coordinator. */
export function isRetryableRecoveryFailure(error: unknown): boolean {
  if (error instanceof DesktopDiscoveryError) {
    return ["dns", "network", "proxy", "timeout", "http"].includes(
      error.kind,
    );
  }
  const message = error instanceof Error ? error.message.toLowerCase() : "";
  return (
    message.includes("could not load after successful discovery") ||
    message.includes("failed to fetch") ||
    message.includes("network") ||
    message.includes("timed out")
  );
}

export function nonRetryableRecoveryMessage(error: unknown): string {
  if (error instanceof DesktopDiscoveryError) {
    if (error.kind === "tls") {
      return "LeapView stopped automatic recovery because the server certificate could not be verified. Check the operating-system trust store, then reopen the instance.";
    }
    if (
      [
        "schema_incompatible",
        "protocol_incompatible",
        "authentication_incompatible",
        "capability_incompatible",
        "canonical_origin_mismatch",
        "instance_identity_mismatch",
      ].includes(error.kind)
    ) {
      return "LeapView stopped automatic recovery because the instance identity or desktop compatibility contract changed. Reopen the instance to review it.";
    }
  }
  return "LeapView stopped automatic recovery after a non-network failure. Reopen the saved instance to continue safely.";
}
