import { spawn } from "node:child_process";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const desktopRoot = resolve(import.meta.dirname, "..");
const capturedOutputLimit = 128 * 1024;
const detachCommand = "hdiutil detach /Volumes/LeapView";
const alreadyDetachedError = "hdiutil: detach failed - No such file or directory";
const maximumSignatureSeparation = 512;

export function isAlreadyDetachedDMGFailure(output) {
  const commandIndex = output.indexOf(detachCommand);
  if (commandIndex < 0) return false;

  const errorIndex = output.indexOf(
    alreadyDetachedError,
    commandIndex + detachCommand.length,
  );
  return errorIndex >= 0 && errorIndex - commandIndex <= maximumSignatureSeparation;
}

export async function runWithDMGDetachRetry(
  runMake,
  {
    platform = process.platform,
    diagnostic = (message) => console.error(message),
  } = {},
) {
  const first = await runMake();
  if (
    first.code === 0 ||
    platform !== "darwin" ||
    !isAlreadyDetachedDMGFailure(first.output)
  ) {
    return first.code;
  }

  diagnostic(
    "macOS DMG cleanup found an already-detached LeapView volume; retrying Electron Forge make once",
  );
  const retry = await runMake();
  return retry.code;
}

async function runElectronMake() {
  const child = spawn("bun", ["scripts/run-electron.mjs", "make"], {
    cwd: desktopRoot,
    env: process.env,
    stdio: ["inherit", "pipe", "pipe"],
  });
  let output = "";

  const forward = (stream, destination) => {
    stream.on("data", (chunk) => {
      destination.write(chunk);
      output = `${output}${chunk}`.slice(-capturedOutputLimit);
    });
  };
  forward(child.stdout, process.stdout);
  forward(child.stderr, process.stderr);

  return await new Promise((resolveResult, reject) => {
    child.once("error", reject);
    child.once("close", (code) => {
      resolveResult({ code: code ?? 1, output });
    });
  });
}

const invokedPath = process.argv[1]
  ? pathToFileURL(resolve(process.argv[1])).href
  : "";
if (invokedPath === import.meta.url) {
  process.exitCode = await runWithDMGDetachRetry(runElectronMake);
}
