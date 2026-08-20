/**
 * Removes undefined and empty optional fields while retaining security
 * requirement objects verbatim. This is the canonical JSON-IR normalization
 * boundary; keeping it separate makes byte stability explicit at emission.
 */
export declare function normalizeDocument<T>(value: T): T;
