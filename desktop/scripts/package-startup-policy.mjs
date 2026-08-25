const startupTimeouts = {
  darwin: 15_000,
  linux: 15_000,
  // An unsigned Windows package may be held briefly by cold antivirus
  // scanning before Electron can create the trusted shell.
  win32: 45_000,
};

export function packagedStartupTimeoutMilliseconds(platform) {
  const timeout = startupTimeouts[platform];
  if (timeout === undefined) {
    throw new Error(`unsupported packaged startup platform ${platform}`);
  }
  return timeout;
}
