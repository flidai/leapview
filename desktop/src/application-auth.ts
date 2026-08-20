import {
  authenticateDesktopProfile,
  prepareDesktopSession,
} from "./auth.js";
import type { DiagnosticEvent } from "./diagnostics.js";
import type { Profile } from "./profiles.js";
import type { Session } from "electron";

export type AuthenticationReporter = (event: DiagnosticEvent) => void;
export type AuthorizationOpener = (url: string) => Promise<void>;

interface AuthenticationTransaction {
  controller: AbortController;
  promise: Promise<void>;
}

/**
 * Owns the bounded browser authentication lifecycle for the application.
 *
 * The coordinator deliberately has no window or policy state.  Callers keep
 * authorization and profile decisions in the application coordinator while
 * this unit makes transaction ownership and exactly-once cancellation explicit.
 */
export class DesktopAuthenticationCoordinator {
  private readonly transactions = new Map<string, AuthenticationTransaction>();

  public constructor(
    private readonly openAuthorization: AuthorizationOpener,
    private readonly report: AuthenticationReporter,
    private readonly maximumTransactions = 3,
  ) {}

  public async ensure(
    profile: Profile,
    profileSession: Session,
  ): Promise<void> {
    const fetcher = (input: string, init: RequestInit) =>
      profileSession.fetch(input, init);
    if (await prepareDesktopSession(profile, fetcher, profileSession)) {
      this.report({ kind: "authentication", phase: "session-valid" });
      return;
    }
    this.report({ kind: "authentication", phase: "required" });
    const existing = this.transactions.get(profile.id);
    if (existing !== undefined) {
      await existing.promise;
      return;
    }
    if (this.transactions.size >= this.maximumTransactions) {
      throw new Error(
        "Too many LeapView authentication requests are already active.",
      );
    }
    const controller = new AbortController();
    const promise = authenticateDesktopProfile(
      profile,
      fetcher,
      async (authorizationURL) => {
        const parsed = new URL(authorizationURL);
        if (
          parsed.origin !== profile.canonicalOrigin ||
          parsed.pathname !== "/auth/desktop/authorize" ||
          parsed.hash !== ""
        ) {
          throw new Error("LeapView produced an unsafe authorization URL.");
        }
        await this.openAuthorization(parsed.toString());
      },
      { signal: controller.signal },
    );
    this.report({ kind: "authentication", phase: "started" });
    const transaction = { controller, promise };
    this.transactions.set(profile.id, transaction);
    try {
      await promise;
      this.report({ kind: "authentication", phase: "completed" });
    } catch (error) {
      this.report({ kind: "authentication", phase: "failed" });
      throw error;
    } finally {
      if (this.transactions.get(profile.id) === transaction) {
        this.transactions.delete(profile.id);
      }
    }
  }

  public async cancel(profileID: string): Promise<void> {
    const transaction = this.transactions.get(profileID);
    if (transaction === undefined) return;
    transaction.controller.abort();
    await transaction.promise.catch(() => undefined);
  }

  public cancelAll(): void {
    for (const transaction of this.transactions.values()) {
      transaction.controller.abort();
    }
  }

  public get size(): number {
    return this.transactions.size;
  }
}
