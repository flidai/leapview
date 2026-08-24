"use strict";

const assert = require("node:assert/strict");
const { test } = require("node:test");

const braceExpansion = require("brace-expansion");
const minimatch = require("minimatch");
const modernMinimatch = require("minimatch-modern");

test("preserves brace-expansion callable and named APIs", () => {
  assert.equal(typeof braceExpansion, "function");
  assert.deepEqual(braceExpansion("{a,b}"), ["a", "b"]);
  assert.deepEqual(braceExpansion.expand("{a,b}"), ["a", "b"]);
  assert.equal(minimatch("a", "{a,b}"), true);
  assert.equal(modernMinimatch.minimatch("a", "{a,b}"), true);
});
