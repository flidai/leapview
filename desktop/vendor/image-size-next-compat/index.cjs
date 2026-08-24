"use strict";

const safe = require("image-size-next");

function imageSize(input, callback) {
  return safe.imageSize(input, callback);
}

module.exports = imageSize;
module.exports.default = imageSize;
module.exports.imageSize = imageSize;
module.exports.disableFS = safe.disableFS;
module.exports.disableTypes = safe.disableTypes;
module.exports.setConcurrency = safe.setConcurrency;
module.exports.types = safe.types;
