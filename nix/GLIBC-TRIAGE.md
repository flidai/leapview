# glibc runtime finding review — 2026-09-29

## Result

Of the eleven HIGH/CRITICAL matches in the original candidate scan, **eight have
fixes in the pinned Nixpkgs patch bundle, two are disputed/non-security issues,
and one was a confirmed defect in the shipped library**. The Nix candidate now
adds the upstream stable fix for that defect. No scanner suppressions or release
exceptions have been activated. Production promotion remains blocked.

This assessment applies only to:

- Candidate source: `000eac132fd6a2802d86d4ed4e14fad3f01dd8c7`.
- Nixpkgs: `7a0f122f5090cf4c2ade2a13a0e229d4e19ba71f`.
- Runtime: `/nix/store/lm3pknxi0ipypy3lxh1wmm8wvvavdwrn-glibc-2.42-84`.
- Deriver: `/nix/store/sayi2sas6kn7q181jy7ic8h8hy2jiqn7-glibc-2.42-84.drv`.
- Backport bundle: `/nix/store/f3xrdrys50y52ij1vg5lk6qdqpvqmf9r-2.42-master.patch`.
- Bundle SHA-256: `a46c4ddb46b674ea5590cd9e07ba55d0fa0db84dda3f86e20b1f0e38729d3a65`.

The runtime's actual derivation references that bundle. Its bytes match the
[locked Nixpkgs patch](https://github.com/NixOS/nixpkgs/blob/7a0f122f5090cf4c2ade2a13a0e229d4e19ba71f/pkgs/development/libraries/glibc/2.42-master.patch).
The [recipe](https://github.com/NixOS/nixpkgs/blob/7a0f122f5090cf4c2ade2a13a0e229d4e19ba71f/pkgs/development/libraries/glibc/common.nix)
uses the 2.42 release tarball plus this patch and Nix integration patches. The
recipe's maintenance comment mentions an older commit count; the bundle contents
and hash, not that comment or the `-84` version suffix alone, establish the fixes.

## Finding-by-finding assessment

For the eight backports below, the bundle contains the listed stable-branch
commit and its code changes. These are source/derivation assessments, not a claim
that all eight upstream regression tests were rerun on the installed binary.

| Finding | Assessment | Evidence / action |
|---|---|---|
| CVE-2026-0861 | Fixed in installed backport bundle | `b0ec8fb689df862171f0f78994a3bdeb51313545`: restores the alignment overflow check in `malloc/malloc.c`. |
| CVE-2026-0915 | Fixed in installed backport bundle | `453e6b8dbab935257eb0802b0c97bca6b67ba30e`: handles the zero network address in the NSS DNS backend. |
| CVE-2025-15281 | Fixed in installed backport bundle | `cbf39c26b25801e9bc88499b4fd361ac172d4125`: resets reused `wordexp_t` fields; subsequent test scheduling correction is also present. |
| CVE-2026-4437 | Fixed in installed backport bundle | `8e863fb1c92360520704a69dc948be6bb4a17cb3`: counts DNS answer records correctly. |
| CVE-2026-4046 | Fixed in installed backport bundle | `f13c1bb0f97fbc12a6ba1ab5669ce561ea32b80a`: fixes pending character handling for IBM1390/IBM1399 conversions. |
| CVE-2026-5928 | Fixed in installed backport bundle | `b4bca35ab9e76890504c4dbdd5eaf15a93514580`: uses the wide-stream read pointer for `ungetwc`. |
| CVE-2026-5450 | Fixed in installed backport bundle | `4ebd33dd77eabe8d4c45232bed4b42a31d2f9edc`: fixes the allocated buffer size for `scanf` `%mc`/`%mC`. |
| CVE-2026-5435 | Fixed in installed backport bundle | `299e1d25c32c5f9ef78ddd6cbfd0c6a09a1f4227`: removes the problematic TSIG printer handling. |
| CVE-2019-1010022 | Disputed/non-security; eligible for reviewed classification | [Debian's security assessment](https://security-tracker.debian.org/tracker/CVE-2019-1010022) marks this unimportant and records upstream's non-security position. It is not a patched-version claim. |
| CVE-2019-1010023 | Disputed/non-security; eligible for reviewed classification | [Debian's security assessment](https://security-tracker.debian.org/tracker/CVE-2019-1010023) records the same position. The scenario involves running `ldd` on an attacker-supplied executable; do not turn this into permission to inspect arbitrary executables. |
| CVE-2026-19499 | Fixed in the new Nix candidate | The pinned bundle lacks the fix. The candidate applies upstream stable commit `6ad255db1dad9f2761935d3125b5bc7fa0e6128f` and its controlled boundary probe passes against the patched image. |

The two disputed records remain described as unfixed in Debian's package table;
its non-security classification does not mean they have been patched. Sourceware
Bugzilla was inaccessible during this review, so the assessment uses Debian's
published security position and the CVE descriptions, not a claimed direct reading
of those Bugzilla discussions.

## Confirmed strfmon defect

The [glibc CNA record](https://github.com/CVEProject/cvelistV5/blob/main/cves/2026/19xxx/CVE-2026-19499.json)
identifies right-justification padding as the affected operation. The current
2.42 maintenance branch contains
[stable fix `6ad255db1dad9f2761935d3125b5bc7fa0e6128f`](https://sourceware.org/git/?p=glibc.git;a=commit;h=6ad255db1dad9f2761935d3125b5bc7fa0e6128f),
backported from `b090cf226ff65b913e41536f1f573f500855615c`. It saves the field's
written length before padding changes the buffer pointer, then uses that original
length for `memmove`. Our pinned bundle lacks this change; [the additional patch](glibc-CVE-2026-19499.patch)
is the unchanged upstream stable commit exported with `git format-patch`. Its
SHA-256 is `21b4138b0c7cdbda4405d776592bab40d92fee4a50e9a73484feef598d797fcd`.

A disposable, read-only, network-disabled container running the existing candidate
executed this probe against its own Nix glibc:

```c
#include <monetary.h>
#include <stdio.h>
#include <string.h>
#include <gnu/libc-version.h>
int main(void) {
  unsigned char buf[64];
  memset(buf, 0x5a, sizeof buf);
  ssize_t n = strfmon((char *)buf, 11, "%10n", 1.0);
  unsigned changed = 0;
  for (unsigned i = 11; i < sizeof buf; ++i)
    if (buf[i] != 0x5a) ++changed;
  printf("glibc=%s result=%zd bytes_changed_past_declared_size=%u\n",
         gnu_get_libc_version(), n, changed);
  return changed ? 1 : 0;
}
```

Observed: `glibc=2.42 result=10 bytes_changed_past_declared_size=5`, exit 1.
The backing allocation is intentionally larger than the declared API size so the
probe observes the boundary violation within reserved memory. This confirms the
library bug, not application-level remote exploitability. Reachability through
LeapView, DuckDB and its native extensions has not been established; absence of a
direct application call is not enough to exempt transitive native code.

The patched glibc output is
`/nix/store/kj7ia0isvb6xh74qavgcshmb7fcskj4l-glibc-2.42-84`.
Running the same probe through its dynamic loader returned
`glibc=2.42 result=10 bytes_changed_past_declared_size=0`, exit 0. The rebuilt
clean image at revision `81d56a2a1099` returned the same passing result and
passed the full production image qualifier, including enterprise authoring. Its
fresh Syft inventory contained exactly this glibc output and no copy of the old
one. All six matching controls passed. Grype still emitted the same eleven raw
version-based HIGH/CRITICAL matches. The Nix image qualification script now runs
this probe for every candidate image. These observations establish the boundary
fix in the tested library; they do not grant release clearance.

The loaded Docker image ID was
`sha256:7b6566d639efc96e7aaa256d70629c3219751cd96961465de91a6a95b9ef9147`;
its revision label matched the candidate above. This is the daemon's loaded image
identity, distinct from Syft's archive identity in the prior scanner report.

## Remaining release work

1. Record the eight backport dispositions, the fixed `strfmon` defect and the two
   disputed classifications as
   reviewed, narrowly bound assessments in protected admission. Retain raw matches
   and require reassessment when the package/store identity changes. Do not install
   a package-wide or unfixed-CVE ignore rule.
2. Require no unassessed HIGH/CRITICAL findings before production promotion, then
   complete digest binding and the other release gates.

The review supports continuing with Nix: most matches reflect missing backport
knowledge in NVD matching. It also demonstrates why inventory and matching alone
cannot grant clearance. Maintaining bounded package assessments is real ongoing
work; the confirmed defect was fixed in the candidate rather than waived.
