# Common password blocklist provenance

`common_passwords.hmac-sha256-128` is derived from the first one million entries in
SecLists' `xato-net-10-million-passwords-1000000.txt` snapshot at commit
`0e0329aa77f0f3d2ff5035e989ad320a2ac4a35d`.

- Source Git blob: `e5e73232c8f4f53edfe9c0fd699f415da8a336e3`
- Downloaded source SHA-256: `424a3e03a17df0a2bc2b3ca749d81b04e79d59cb7aeec8876a5a3f308d0caf51`
- Derivation: keep valid UTF-8 lines from 12 Unicode characters through 1024 UTF-8 bytes, fingerprint the exact line with HMAC-SHA-256 using the public domain-separation key `leapview/local-password-blocklist/v1`, retain the first 128 bits, deduplicate, and sort lexicographically.
- Derived entries: 46,296
- Derived file SHA-256: `5381c457e7f2d881787473062703a371068bce3243a373d3ce18a5003c74ae93`

Only the derived hash prefixes are distributed. The plaintext corpus is not
part of LeapView, and password validation performs no network requests.

## SecLists license

MIT License

Copyright (c) 2018 Daniel Miessler

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
