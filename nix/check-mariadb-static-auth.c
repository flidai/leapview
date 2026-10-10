#include <ma_global.h>
#include <stdio.h>
#include <mysql.h>
#include <mysql/client_plugin.h>

/* Link the produced archive, then prohibit plugin fallback. A successful build
 * alone can hide missing Ed25519 object members or a dynamic SHA2 plugin. */
int main(void) {
  const char *names[] = {"mysql_native_password", "caching_sha2_password",
                        "sha256_password", "dialog", "mysql_clear_password",
                        "client_ed25519"};
  MYSQL *db = mysql_init(NULL);
  if (!db || mysql_options(db, MYSQL_PLUGIN_DIR, "/nonexistent-static-auth"))
    return 1;
  for (unsigned int i = 0; i < sizeof(names) / sizeof(*names); i++) {
    if (!mysql_client_find_plugin(db, names[i], MYSQL_CLIENT_AUTHENTICATION_PLUGIN)) {
      fprintf(stderr, "missing static authentication plugin %s\n", names[i]);
      mysql_close(db);
      mysql_library_end();
      return 2;
    }
    printf("static authentication plugin %s available\n", names[i]);
  }
  mysql_close(db);
  mysql_library_end();
  return 0;
}
