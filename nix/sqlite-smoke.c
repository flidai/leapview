#include "sqlite3.h"
#include <stdio.h>
#include <string.h>

static int readback(void *unused, int count, char **values, char **names) {
  (void)unused;
  (void)names;
  return count != 1 || !values[0] || strcmp(values[0], "30") != 0;
}

int main(int argc, char **argv) {
  sqlite3 *db = NULL;
  char *error = NULL;
  if (argc != 2 || strcmp(sqlite3_libversion(), "3.53.4") != 0 ||
      strcmp(sqlite3_sourceid(), "2026-07-24 19:02:57 bf7c7f30031888f4e796e429ab3978879485813aaca6f641c7b33e4e09459bcc") != 0)
    return 1;
  if (sqlite3_open(argv[1], &db) != SQLITE_OK)
    return 2;
  const char *write = "BEGIN; CREATE TABLE sample(id INTEGER); INSERT INTO sample VALUES(10),(20);"
                      "CREATE VIRTUAL TABLE search USING fts5(content); INSERT INTO search VALUES('ordinary smoke'); COMMIT;";
  if (sqlite3_exec(db, write, NULL, NULL, &error) != SQLITE_OK) {
    fprintf(stderr, "%s\n", error);
    sqlite3_free(error);
    sqlite3_close(db);
    return 3;
  }
  if (sqlite3_close(db) != SQLITE_OK || sqlite3_open(argv[1], &db) != SQLITE_OK)
    return 4;
  if (sqlite3_exec(db, "SELECT sum(id) FROM sample", readback, NULL, &error) != SQLITE_OK) {
    fprintf(stderr, "%s\n", error ? error : "unexpected readback");
    sqlite3_free(error);
    sqlite3_close(db);
    return 5;
  }
  puts(sqlite3_libversion());
  puts(sqlite3_sourceid());
  return sqlite3_close(db) == SQLITE_OK ? 0 : 6;
}
