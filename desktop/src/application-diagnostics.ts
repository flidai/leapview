import {
  DiagnosticJournal,
  type DiagnosticEnvironment,
  type DiagnosticEvent,
} from "./diagnostics.js";

export class DesktopDiagnosticsCoordinator {
  private flushTimer: NodeJS.Timeout | null = null;

  private constructor(
    private readonly journal: DiagnosticJournal,
    private readonly flushDelayMs: number,
  ) {}

  public static async open(
    path: string,
    enabled: boolean,
    flushDelayMs = 500,
  ): Promise<DesktopDiagnosticsCoordinator> {
    const journal = await DiagnosticJournal.open(path, { enabled });
    return new DesktopDiagnosticsCoordinator(journal, flushDelayMs);
  }

  public record(event: DiagnosticEvent): void {
    try {
      this.journal.record(event);
    } catch {
      console.warn("LeapView Desktop rejected an internal diagnostic event.");
      return;
    }
    if (this.flushTimer !== null) return;
    this.flushTimer = setTimeout(() => {
      this.flushTimer = null;
      void this.flush();
    }, this.flushDelayMs);
  }

  public async flush(): Promise<void> {
    if (this.flushTimer !== null) {
      clearTimeout(this.flushTimer);
      this.flushTimer = null;
    }
    try {
      await this.journal.flush();
    } catch {
      console.warn("LeapView Desktop could not save diagnostic events.");
    }
  }

  public report(environment: DiagnosticEnvironment) {
    return this.journal.report(environment);
  }
}
