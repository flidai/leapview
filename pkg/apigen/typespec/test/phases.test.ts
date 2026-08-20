import { describe, expect, it } from "vitest";
import { normalizeDocument } from "../src/phase-normalization.js";
import { renderDocumentJSON } from "../src/phase-rendering.js";

describe("APIGen generation phases", () => {
  it("normalizes optional fields without changing security requirements", () => {
    const input = {
      optional: undefined,
      empty: [],
      failures: [],
      security: [{ BearerAuth: [] }],
      nested: { present: true, omitted: undefined },
    };

    expect(normalizeDocument(input)).toEqual({
      failures: [],
      security: [{ BearerAuth: [] }],
      nested: { present: true },
    });
  });

  it("renders a byte-stable JSON document with one trailing newline", () => {
    const document = { schema_version: "v4", info: { title: "API" } };
    expect(renderDocumentJSON(document)).toBe(
      '{\n  "schema_version": "v4",\n  "info": {\n    "title": "API"\n  }\n}\n',
    );
  });
});
