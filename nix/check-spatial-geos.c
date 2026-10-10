#include <geos_c.h>
#include <math.h>
#include <stdio.h>
#include <string.h>

int main(void) {
  if (strncmp(GEOSversion(), "3.14.1-", 7) != 0) return 1;
  GEOSContextHandle_t ctx = GEOS_init_r();
  if (!ctx) return 2;
  GEOSWKTReader *reader = GEOSWKTReader_create_r(ctx);
  GEOSGeometry *polygon = GEOSWKTReader_read_r(ctx, reader, "POLYGON ((0 0, 4 0, 4 4, 0 4, 0 0))");
  GEOSGeometry *point = GEOSWKTReader_read_r(ctx, reader, "POINT (2 2)");
  double area = 0;
  if (!polygon || !point || !GEOSArea_r(ctx, polygon, &area) || area != 16 || GEOSContains_r(ctx, polygon, point) != 1) return 3;
  GEOSGeometry *buffer = GEOSBuffer_r(ctx, point, 1, 32);
  if (!buffer || !GEOSArea_r(ctx, buffer, &area) || fabs(area - 3.141592653589793) > 0.002) return 4;
  GEOSGeom_destroy_r(ctx, buffer);
  GEOSGeom_destroy_r(ctx, point);
  GEOSGeom_destroy_r(ctx, polygon);
  GEOSWKTReader_destroy_r(ctx, reader);
  GEOS_finish_r(ctx);
  puts("selected static GEOS geometry, containment and buffer checks passed");
  return 0;
}
