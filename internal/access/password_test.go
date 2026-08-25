package access

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestValidateLocalPassword(t *testing.T) {
	knownBreached := strings.Join([]string{"q1w2", "e3r4", "t5y6"}, "")
	for _, test := range []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "minimum", password: "safe-pass-12"},
		{name: "unicode characters", password: strings.Repeat("密码", MinimumLocalPasswordCharacters/2)},
		{name: "opaque whitespace", password: "  exact password  "},
		{name: "known breached password", password: knownBreached, wantErr: true},
		{name: "case variant of breached password", password: strings.ToUpper(knownBreached), wantErr: true},
		{name: "short", password: strings.Repeat("a", MinimumLocalPasswordCharacters-1), wantErr: true},
		{name: "too many bytes", password: strings.Repeat("a", MaximumLocalPasswordBytes+1), wantErr: true},
		{name: "invalid utf8", password: strings.Repeat("\xff", MinimumLocalPasswordCharacters), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateLocalPassword(test.password)
			if test.wantErr != errors.Is(err, ErrLocalPasswordPolicy) {
				t.Fatalf("ValidateLocalPassword() error = %v, want policy error %v", err, test.wantErr)
			}
		})
	}
}

func TestCommonPasswordBlocklistIsStrictlySorted(t *testing.T) {
	if got, want := len(commonPasswordHashPrefixes), commonPasswordHashEntries*commonPasswordHashPrefixBytes; got != want {
		t.Fatalf("blocklist bytes = %d, want %d", got, want)
	}
	for index := 1; index < commonPasswordHashEntries; index++ {
		previous := commonPasswordHashPrefixes[(index-1)*commonPasswordHashPrefixBytes : index*commonPasswordHashPrefixBytes]
		current := commonPasswordHashPrefixes[index*commonPasswordHashPrefixBytes : (index+1)*commonPasswordHashPrefixBytes]
		if bytes.Compare(previous, current) >= 0 {
			t.Fatalf("blocklist entries %d and %d are not strictly sorted", index-1, index)
		}
	}
}
