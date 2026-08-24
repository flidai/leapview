"use strict";

const base = require("brace-expansion-next");
const expand = base.expand ?? base;

function braceExpansion(pattern, options) {
  return expand(pattern, options);
}

module.exports = braceExpansion;
module.exports.expand = expand;
module.exports.EXPANSION_MAX = base.EXPANSION_MAX;
module.exports.EXPANSION_MAX_LENGTH = base.EXPANSION_MAX_LENGTH;
