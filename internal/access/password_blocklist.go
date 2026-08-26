package access

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"sort"
	"strings"
)

const (
	commonPasswordHashPrefixBytes = 16
	commonPasswordHashEntries     = 46296
	commonPasswordHashDomain      = "leapview/local-password-blocklist/v1"
)

var commonPasswordBlocklistSHA256 = [sha256.Size]byte{
	0x53, 0x81, 0xc4, 0x57, 0xe7, 0xf2, 0xd8, 0x81,
	0x78, 0x74, 0x73, 0x06, 0x27, 0x03, 0xa3, 0x71,
	0x06, 0x8b, 0xce, 0x32, 0x43, 0xa3, 0x73, 0xd3,
	0xce, 0x18, 0xa5, 0x00, 0x3c, 0x74, 0xae, 0x93,
}

// commonPasswordHashPrefixes contains sorted 128-bit HMAC-SHA-256 prefixes
// derived from a pinned offline corpus. The fixed HMAC key is a public domain
// separator, not a secret; it distinguishes lookup fingerprints from stored
// password verifiers. Keeping the corpus local avoids disclosing password
// candidates to a remote breach-lookup service.
//
//go:embed common_passwords.hmac-sha256-128
var commonPasswordHashPrefixes []byte

func init() {
	got := sha256.Sum256(commonPasswordHashPrefixes)
	if len(commonPasswordHashPrefixes) != commonPasswordHashEntries*commonPasswordHashPrefixBytes || got != commonPasswordBlocklistSHA256 {
		panic("common password blocklist failed integrity validation")
	}
}

func isCommonOrBreachedLocalPassword(password string) bool {
	if commonPasswordBlocklistContains(password) {
		return true
	}
	lower := strings.ToLower(password)
	return lower != password && commonPasswordBlocklistContains(lower)
}

func commonPasswordBlocklistContains(password string) bool {
	mac := hmac.New(sha256.New, []byte(commonPasswordHashDomain))
	_, _ = mac.Write([]byte(password))
	want := mac.Sum(nil)[:commonPasswordHashPrefixBytes]
	entries := len(commonPasswordHashPrefixes) / commonPasswordHashPrefixBytes
	index := sort.Search(entries, func(index int) bool {
		start := index * commonPasswordHashPrefixBytes
		return bytes.Compare(commonPasswordHashPrefixes[start:start+commonPasswordHashPrefixBytes], want) >= 0
	})
	if index == entries {
		return false
	}
	start := index * commonPasswordHashPrefixBytes
	return bytes.Equal(commonPasswordHashPrefixes[start:start+commonPasswordHashPrefixBytes], want)
}
