/**
 * Coordinates the asynchronous before-quit barrier.
 *
 * Electron may deliver before-quit more than once while the flush promise is
 * pending.  This small state machine makes the barrier idempotent and keeps
 * shutdown ordering testable without importing Electron.
 */
export class DesktopShutdownCoordinator {
  private pending = false;
  private ready = false;

  public get isReady(): boolean {
    return this.ready;
  }

  public get isPending(): boolean {
    return this.pending;
  }

  public begin(
    preventDefault: () => void,
    capture: () => void,
    flush: () => Promise<void>,
    quit: () => void,
  ): void {
    if (this.ready) return;
    preventDefault();
    if (this.pending) return;
    this.pending = true;
    capture();
    void flush().finally(() => {
      this.ready = true;
      quit();
    });
  }

  public markReady(): void {
    this.ready = true;
    this.pending = false;
  }
}
