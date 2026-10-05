const interactiveRoles = new Set([
  "button",
  "checkbox",
  "combobox",
  "link",
  "listbox",
  "menuitem",
  "radio",
  "slider",
  "spinbutton",
  "switch",
  "textbox",
]);

export function verifyTrustedShellAccessibility(input) {
  if (!Array.isArray(input) || input.length === 0) {
    throw new Error("trusted shell accessibility tree is missing");
  }
  const nodes = input.filter(
    (node) =>
      node !== null &&
      typeof node === "object" &&
      node.ignored !== true,
  );
  const nodesByID = new Map(
    nodes
      .filter((node) => typeof node.nodeId === "string")
      .map((node) => [node.nodeId, node]),
  );
  const entries = nodes.map((node) => ({
    node,
    role: axValue(node.role),
    name: axValue(node.name).trim(),
    focused: axProperty(node, "focused") === true,
    live: axProperty(node, "live"),
    required: axProperty(node, "required") === true,
  }));
  if (!entries.some((entry) => entry.role === "main")) {
    throw new Error("trusted shell accessibility main landmark is missing");
  }
  if (
    !entries.some(
      (entry) =>
        entry.role === "heading" && entry.name === "Connect to LeapView",
    )
  ) {
    throw new Error("trusted shell accessibility heading is missing");
  }

  const controls = entries.filter((entry) =>
    interactiveRoles.has(entry.role),
  );
  for (const control of controls) {
    if (control.name === "") {
      throw new Error(
        "trusted shell interactive control has no accessible name",
      );
    }
  }
  const regions = entries
    .filter((entry) => entry.role === "region")
    .map((entry) => entry.name);
  if (regions.some((name) => name === "")) {
    throw new Error("trusted shell accessibility region has no name");
  }

  const origin = controls.find(
    (entry) =>
      entry.role === "textbox" && entry.name === "LeapView URL",
  );
  if (origin !== undefined) {
    const missing = [];
    if (!regions.includes("Connect an instance")) {
      missing.push("named connection region");
    }
    if (
      !controls.some(
        (entry) =>
          entry.role === "button" && entry.name === "Verify & open",
      )
    ) {
      missing.push("named submit button");
    }
    if (origin.required !== true) {
      missing.push("required input state");
    }
    if (origin.focused !== true) {
      missing.push("initial input focus");
    }
    if (missing.length > 0) {
      throw new Error(
        `trusted shell open-mode accessibility contract is incomplete: ${missing.join(", ")}`,
      );
    }
    if (
      controls.filter((entry) => entry.focused).length !== 1
    ) {
      throw new Error(
        "trusted shell accessibility focus is not deterministic",
      );
    }
    return {
      mode: "open",
      announcement: "none",
      controls: controls.length,
      focusedControl: origin.name,
      regions,
    };
  }

  const policyAlert = entries.find(
    (entry) =>
      entry.role === "alert" &&
      accessibleText(entry.node, nodesByID).includes(
        "managed desktop configuration is invalid",
      ),
  );
  const applicationDocumentFocused = entries.some(
    (entry) =>
      entry.focused &&
      (entry.role === "RootWebArea" || entry.role === "WebArea"),
  );
  const focusedControl =
    policyAlert?.focused === true
      ? "Managed configuration error"
      : applicationDocumentFocused
        ? "Application document"
        : undefined;
  const missing = [];
  if (policyAlert === undefined) {
    missing.push("managed configuration alert");
  } else if (policyAlert.live !== "assertive") {
    missing.push("assertive announcement");
  }
  if (controls.length !== 0) {
    missing.push("locked action suppression");
  }
  if (focusedControl === undefined) {
    missing.push("document or alert focus");
  }
  if (missing.length > 0) {
    throw new Error(
      `trusted shell locked-mode accessibility contract is incomplete: ${missing.join(", ")}`,
    );
  }
  return {
    mode: "locked",
    announcement: "assertive",
    controls: 0,
    focusedControl,
    regions,
  };
}

export async function readTrustedShellAccessibility(
  webSocketDebuggerURL,
) {
  const url = new URL(webSocketDebuggerURL);
  if (
    url.protocol !== "ws:" ||
    !["127.0.0.1", "[::1]", "localhost"].includes(url.hostname) ||
    url.username !== "" ||
    url.password !== ""
  ) {
    throw new Error(
      "trusted shell accessibility debugger URL is not loopback",
    );
  }
  const socket = new WebSocket(url);
  const pending = new Map();
  let nextID = 1;
  let terminalError;
  const fail = (error) => {
    if (terminalError !== undefined) {
      return;
    }
    terminalError = error;
    for (const call of pending.values()) {
      call.reject(terminalError);
    }
    pending.clear();
    socket.close();
  };
  socket.addEventListener("close", () => {
    fail(new Error("trusted shell accessibility debugger closed"));
  });
  socket.addEventListener("error", () => {
    fail(new Error("trusted shell accessibility debugger failed"));
  });
  socket.addEventListener("message", (event) => {
    if (terminalError !== undefined) {
      return;
    }
    if (typeof event.data !== "string" || event.data.length > 4 * 1024 * 1024) {
      fail(new Error(
        "trusted shell accessibility response is invalid",
      ));
      return;
    }
    let message;
    try {
      message = JSON.parse(event.data);
    } catch {
      fail(new Error(
        "trusted shell accessibility response is malformed",
      ));
      return;
    }
    // The debugger is a protocol boundary, even on loopback. JSON.parse can
    // return null or a primitive, neither of which is a response envelope.
    if (message === null || typeof message !== "object" || Array.isArray(message)) {
      fail(new Error("trusted shell accessibility response is invalid"));
      return;
    }
    if (!Object.hasOwn(message, "id")) {
      // CDP sends method notifications alongside command responses.
      if (typeof message.method !== "string") {
        fail(new Error("trusted shell accessibility response is invalid"));
      }
      return;
    }
    if (!Number.isSafeInteger(message.id) || message.id <= 0) {
      fail(new Error("trusted shell accessibility response id is invalid"));
      return;
    }
    const call = pending.get(message.id);
    if (typeof call?.resolve === "function") {
      pending.delete(message.id);
      call.resolve(message);
    }
  });
  try {
    await waitForSocket(socket);
    const call = async (method) => {
      if (terminalError !== undefined) {
        throw terminalError;
      }
      const id = nextID;
      nextID += 1;
      const response = new Promise((resolve, reject) => {
        const timeout = setTimeout(() => {
          pending.delete(id);
          reject(
            new Error(`trusted shell accessibility ${method} timed out`),
          );
        }, 3_000);
        pending.set(id, {
          reject(error) {
            clearTimeout(timeout);
            reject(error);
          },
          resolve(message) {
            clearTimeout(timeout);
            if (message.error !== undefined) {
              reject(new Error(`trusted shell accessibility ${method} failed`));
              return;
            }
            resolve(message.result);
          },
        });
      });
      try {
        socket.send(JSON.stringify({ id, method }));
      } catch {
        fail(new Error("trusted shell accessibility debugger send failed"));
      }
      return response;
    };
    await call("Accessibility.enable");
    const deadline = Date.now() + 3_000;
    let lastError;
    while (Date.now() < deadline) {
      const result = await call("Accessibility.getFullAXTree");
      try {
        return verifyTrustedShellAccessibility(result?.nodes);
      } catch (error) {
        lastError = error;
      }
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    throw new Error(
      `packaged trusted shell accessibility did not stabilize: ${
        lastError instanceof Error ? lastError.message : String(lastError)
      }`,
    );
  } finally {
    fail(new Error("trusted shell accessibility debugger closed"));
  }
}

function axValue(value) {
  return typeof value?.value === "string" ? value.value : "";
}

function axProperty(node, name) {
  const property = Array.isArray(node.properties)
    ? node.properties.find((candidate) => candidate?.name === name)
    : undefined;
  return property?.value?.value;
}

function accessibleText(node, nodesByID, visited = new Set()) {
  if (
    typeof node.nodeId === "string" &&
    visited.has(node.nodeId)
  ) {
    return "";
  }
  const nextVisited = new Set(visited);
  if (typeof node.nodeId === "string") {
    nextVisited.add(node.nodeId);
  }
  const ownName = axValue(node.name).trim();
  const childText = Array.isArray(node.childIds)
    ? node.childIds
        .map((id) =>
          typeof id === "string" ? nodesByID.get(id) : undefined,
        )
        .filter((child) => child !== undefined)
        .map((child) =>
          accessibleText(child, nodesByID, nextVisited),
        )
        .filter((text) => text !== "")
    : [];
  return [ownName, ...childText]
    .filter((text) => text !== "")
    .join(" ")
    .toLocaleLowerCase("en-US");
}

async function waitForSocket(socket) {
  if (socket.readyState === WebSocket.OPEN) {
    return;
  }
  await new Promise((resolve, reject) => {
    const cleanup = () => {
      clearTimeout(timeout);
      socket.removeEventListener("open", opened);
      socket.removeEventListener("error", failed);
      socket.removeEventListener("close", failed);
    };
    const opened = () => {
      cleanup();
      resolve();
    };
    const failed = () => {
      cleanup();
      reject(new Error("trusted shell accessibility debugger failed to open"));
    };
    const timeout = setTimeout(() => {
      cleanup();
      reject(new Error("trusted shell accessibility debugger did not open"));
    }, 3_000);
    socket.addEventListener("open", opened);
    socket.addEventListener("error", failed);
    socket.addEventListener("close", failed);
  });
}
