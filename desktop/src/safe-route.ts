const MAXIMUM_ROUTE_BYTES = 1_024;
const routeSegment = "[A-Za-z0-9][A-Za-z0-9._:-]{0,127}";
const safeRoutePattern = new RegExp(
  `^/(?:explore|data|models|semantic-models|pipelines|connections|dashboards/${routeSegment}(?:/pages/${routeSegment})?)?$`,
  "u",
);

export function isSafeDesktopRoute(path: string): boolean {
  return (
    typeof path === "string" &&
    new TextEncoder().encode(path).byteLength <= MAXIMUM_ROUTE_BYTES &&
    !path.includes("%") &&
    !path.includes("\\") &&
    !path.includes("?") &&
    !path.includes("#") &&
    !path.includes("//") &&
    safeRoutePattern.test(path)
  );
}
