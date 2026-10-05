#include <gnu/libc-version.h>
#include <monetary.h>
#include <stdio.h>
#include <string.h>

int main(void) {
  unsigned char buffer[64];
  memset(buffer, 0x5a, sizeof buffer);
  ssize_t result = strfmon((char *)buffer, 11, "%10n", 1.0);
  unsigned changed = 0;
  for (unsigned i = 11; i < sizeof buffer; ++i) {
    if (buffer[i] != 0x5a) {
      ++changed;
    }
  }
  printf("glibc=%s result=%zd bytes_changed_past_declared_size=%u\n",
         gnu_get_libc_version(), result, changed);
  return result != 10 || changed != 0;
}
