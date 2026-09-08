// Keep production output semantically unchanged while removing transport-only
// formatting. In particular, do not rename identifiers or apply syntax
// transforms: the bundle gate should measure the code that product code emits,
// with only unnecessary whitespace and comments removed.
export const productionMinify = {
  whitespace: true,
  syntax: false,
  identifiers: false,
} as const
