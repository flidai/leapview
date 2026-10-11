// Trusted functions stay constant; values cross CDP only as protocol arguments.
const functions = {
  read: { count: 2, declaration: `function(expectedID, expectedLabel) {
    return document.querySelector('form.rename input[name="profileId"]')?.value === expectedID &&
      document.querySelector('form.rename input[name="label"]')?.value === expectedLabel;
  }` },
  rename: { count: 1, declaration: `function(label) {
    const form = document.querySelector('form.rename');
    form.querySelector('input[name="label"]').value = label;
    form.requestSubmit();
    return true;
  }` },
  acknowledged: { count: 1, declaration: `function(label) {
    return document.querySelector('[data-state="success"]')?.textContent === 'Saved instance name updated.' &&
      document.querySelector('form.rename input[name="label"]')?.value === label;
  }` },
};

export function debugCommandError(method, error) {
  const knownMethod = ['Runtime.evaluate', 'Runtime.callFunctionOn', 'Runtime.releaseObject', 'Browser.close'].includes(method)
    ? method : 'unknown';
  const code = Number.isInteger(error?.code) && error.code >= -(2 ** 31) && error.code < 2 ** 31
    ? error.code : 'unknown';
  // Protocol messages/data can contain profile values or URLs. Keep only the
  // fixed method vocabulary and numeric protocol code, without the raw cause.
  return new Error(`debug command rejected (${knownMethod}; code ${code})`);
}

export async function inspectTrustedShell(call, action, values) {
  const fn = Object.hasOwn(functions, action) ? functions[action] : null;
  if (!fn || !Array.isArray(values) || values.length !== fn.count || values.some(value => typeof value !== 'string')) {
    throw new Error('invalid trusted UI probe arguments');
  }
  // Resolve and use this object through the same page-target CDP connection.
  const object = await call('Runtime.evaluate', { expression: 'globalThis', returnByValue: false });
  const objectId = object.result?.objectId;
  if (object.exceptionDetails || typeof objectId !== 'string' || !objectId) {
    throw new Error('trusted UI context unavailable');
  }
  try {
    const result = await call('Runtime.callFunctionOn', {
      objectId, functionDeclaration: fn.declaration,
      arguments: values.map(value => ({ value })), returnByValue: true,
    });
    if (result.exceptionDetails) throw new Error('trusted UI evaluation failed');
    return result.result?.value;
  } finally {
    // Real form submission navigates to the operation page and destroys the
    // old context. Releasing that obsolete object can fail after a successful
    // submission; cleanup must not replace the probe result or its real error.
    // The caller still requires UI acknowledgement, durable readback, and full
    // process-group termination before it can issue a lifecycle receipt.
    await call('Runtime.releaseObject', { objectId }).catch(() => {});
  }
}
