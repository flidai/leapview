export async function startProofLifecycle({ app, result, runProof, writeResult }) {
  // The proof owns application termination. Electron's default Linux/Windows
  // behavior would otherwise quit as soon as runProof destroys its last window.
  app.on("window-all-closed", () => {});

  await writeResult();
  const completion = app.whenReady()
    .then(async () => {
      result.phase = "running";
      await runProof();
      result.passed = true;
      result.phase = "complete";
      await writeResult();
      app.quit();
    })
    .catch(async (error) => {
      result.error = error instanceof Error ? error.message : String(error);
      result.phase = "failed";
      await writeResult();
      app.exit(1);
    });
  return { completion };
}
