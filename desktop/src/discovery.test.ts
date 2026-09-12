import { describe, expect, test } from "bun:test";

import {
  DesktopDiscoveryError,
  discoverInstance,
  validateDiscoveryDocument,
  type DiscoveryDocument,
} from "./discovery.js";
import type { DesktopDiscoveryFailureKind } from "./generated/desktop-discovery.js";

const origin = "https://analytics.company.com";
const validDocument: DiscoveryDocument = {
  schemaVersion: 1,
  canonicalOrigin: origin,
  instanceId: "instance_0123456789abcdef0123456789abcdef",
  displayName: "Company Analytics",
  serverVersion: "v1.4.2",
  desktopProtocolMin: 1,
  desktopProtocolMax: 1,
  authenticationModes: ["browser-session", "system-browser-pkce"],
  capabilities: ["remote-web"],
};

function expectDiscoveryFailure(
  operation: () => unknown,
  kind: DesktopDiscoveryFailureKind,
): void {
  try {
    operation();
    throw new Error(`expected desktop discovery failure ${kind}`);
  } catch (error) {
    expect(error).toBeInstanceOf(DesktopDiscoveryError);
    expect((error as DesktopDiscoveryError).kind).toBe(kind);
  }
}

describe("validateDiscoveryDocument", () => {
  test("accepts the supported bounded contract", () => {
    expect(validateDiscoveryDocument(validDocument, origin)).toEqual(validDocument);
  });

  test("rejects origin substitution, instance spoofing, and incompatible protocols", () => {
    expectDiscoveryFailure(
      () =>
      validateDiscoveryDocument(
        { ...validDocument, canonicalOrigin: "https://attacker.example" },
        origin,
      ),
      "canonical_origin_mismatch",
    );
    expectDiscoveryFailure(
      () =>
      validateDiscoveryDocument({ ...validDocument, instanceId: origin }, origin),
      "malformed_response",
    );
    expectDiscoveryFailure(
      () =>
      validateDiscoveryDocument(
        { ...validDocument, desktopProtocolMin: 2, desktopProtocolMax: 4 },
        origin,
      ),
      "protocol_incompatible",
    );
    expectDiscoveryFailure(
      () =>
      validateDiscoveryDocument(
        { ...validDocument, authenticationModes: ["browser-session"] },
        origin,
      ),
      "authentication_incompatible",
    );
  });

  test("rejects excessive nesting, strings, and arrays", () => {
    expect(() =>
      validateDiscoveryDocument(
        { ...validDocument, displayName: "a".repeat(121) },
        origin,
      ),
    ).toThrow("display name");
    expect(() =>
      validateDiscoveryDocument(
        { ...validDocument, capabilities: Array.from({ length: 17 }, () => "remote-web") },
        origin,
      ),
    ).toThrow("array");
    expect(() =>
      validateDiscoveryDocument(
        { ...validDocument, extension: { a: { b: { c: { d: { e: { f: {} } } } } } } },
        origin,
      ),
    ).toThrow("nested");
  });
});

describe("discoverInstance", () => {
  test("fetches without credentials, redirects, or cache", async () => {
    let requestURL = "";
    let requestInit: RequestInit | undefined;
    const document = await discoverInstance(origin, async (url, init) => {
      requestURL = url;
      requestInit = init;
      return Response.json(validDocument, {
        headers: { "content-type": "application/json" },
      });
    });

    expect(document).toEqual(validDocument);
    expect(requestURL).toBe(`${origin}/.well-known/leapview`);
    expect(requestInit).toMatchObject({
      cache: "no-store",
      credentials: "omit",
      redirect: "error",
      referrerPolicy: "no-referrer",
    });
  });

  test("rejects non-JSON and oversized responses", async () => {
    for (const fetcher of [
      async () =>
        new Response("not json", { headers: { "content-type": "text/plain" } }),
      async () =>
        new Response("{}", {
          headers: { "content-type": "application/jsonp" },
        }),
      async () =>
        new Response("x".repeat(65_537), {
          headers: { "content-type": "application/json" },
        }),
      async () =>
        new Response("{", {
          headers: { "content-type": "application/json" },
        }),
    ]) {
      try {
        await discoverInstance(origin, fetcher);
        throw new Error("expected malformed discovery response");
      } catch (error) {
        expect(error).toBeInstanceOf(DesktopDiscoveryError);
        expect((error as DesktopDiscoveryError).kind).toBe(
          "malformed_response",
        );
      }
    }
  });

  test("classifies bounded timeout, redirect, DNS, TLS, proxy, and network failures", async () => {
    const cases = [
      ["redirect", "net::ERR_UNSAFE_REDIRECT"],
      ["dns", "net::ERR_NAME_NOT_RESOLVED"],
      ["tls", "net::ERR_CERT_AUTHORITY_INVALID"],
      ["proxy", "net::ERR_PROXY_CONNECTION_FAILED"],
      ["proxy", "net::ERR_TUNNEL_CONNECTION_FAILED"],
      ["network", "net::ERR_CONNECTION_REFUSED"],
    ] as const;
    for (const [kind, message] of cases) {
      try {
        await discoverInstance(origin, async () => {
          throw new TypeError(`Failed to fetch: ${message}`);
        });
        throw new Error(`expected ${kind} failure`);
      } catch (error) {
        expect(error).toBeInstanceOf(DesktopDiscoveryError);
        expect((error as DesktopDiscoveryError).kind).toBe(kind);
        expect(error instanceof Error ? error.message : "").not.toContain(
          message,
        );
      }
    }

    try {
      await discoverInstance(
        origin,
        async (_input, init) =>
          new Promise<Response>((_resolve, reject) => {
            init.signal?.addEventListener(
              "abort",
              () => reject(new DOMException("aborted", "AbortError")),
              { once: true },
            );
          }),
        { timeoutMs: 5 },
      );
      throw new Error("expected timeout");
    } catch (error) {
      expect(error).toBeInstanceOf(DesktopDiscoveryError);
      expect((error as DesktopDiscoveryError).kind).toBe("timeout");
    }
  });

  test("serializes only the generated, non-secret failure contract", () => {
    const error = new DesktopDiscoveryError(
      "dns",
      "failed to resolve secret.internal",
      { cause: new Error("resolver=10.0.0.53") },
    );

    expect(error.toFailure()).toEqual({
      schemaVersion: 1,
      kind: "dns",
    });
    expect(JSON.stringify(error.toFailure())).not.toContain("secret.internal");
    expect(
      new DesktopDiscoveryError("tls", "certificate rejected").toFailure(),
    ).toEqual({
      schemaVersion: 1,
      kind: "tls",
    });
  });
});
