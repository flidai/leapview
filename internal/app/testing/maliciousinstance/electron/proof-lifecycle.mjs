export async function runProofLifecycle({ app, result, runProof, writeResult }) {
  // The proof owns application termination. Electron's default Linux/Windows
  // behavior would otherwise quit as soon as runProof destroys its last window.
  app.on("window-all-closed", () => {});

  await writeResult();
  try {
    await app.whenReady();
    result.phase = "running";
    await runProof();
    result.passed = true;
    result.phase = "complete";
    await writeResult();
    app.quit();
  } catch (error) {
    result.error = error instanceof Error ? error.message : String(error);
    result.phase = "failed";
    await writeResult();
    app.exit(1);
  }
}
