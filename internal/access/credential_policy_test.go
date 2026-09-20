package access

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCredentialExpiryPolicy(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		resolve    func(time.Time, time.Time) (time.Time, error)
		input      time.Time
		want       time.Time
		wantErr    error
		wantErrSub string
	}{
		{
			name:    "api token omitted",
			resolve: ResolveAPITokenExpiry,
			want:    now.Add(APITokenDefaultLifetime),
		},
		{
			name:    "service principal secret omitted",
			resolve: ResolveServicePrincipalSecretExpiry,
			want:    now.Add(ServicePrincipalSecretDefaultLifetime),
		},
		{
			name:       "api token past",
			resolve:    ResolveAPITokenExpiry,
			input:      now.Add(-time.Second),
			wantErr:    ErrCredentialExpiryInPast,
			wantErrSub: "api token expiry must be in the future",
		},
		{
			name:       "service principal secret past",
			resolve:    ResolveServicePrincipalSecretExpiry,
			input:      now.Add(-time.Second),
			wantErr:    ErrCredentialExpiryInPast,
			wantErrSub: "service principal secret expiry must be in the future",
		},
		{
			name:    "api token maximum",
			resolve: ResolveAPITokenExpiry,
			input:   now.Add(APITokenMaxLifetime),
			want:    now.Add(APITokenMaxLifetime),
		},
		{
			name:    "service principal secret maximum",
			resolve: ResolveServicePrincipalSecretExpiry,
			input:   now.Add(ServicePrincipalSecretMaxLifetime),
			want:    now.Add(ServicePrincipalSecretMaxLifetime),
		},
		{
			name:       "api token over maximum",
			resolve:    ResolveAPITokenExpiry,
			input:      now.Add(APITokenMaxLifetime + time.Nanosecond),
			wantErr:    ErrCredentialExpiryTooFar,
			wantErrSub: "api token expiry exceeds maximum lifetime",
		},
		{
			name:       "service principal secret over maximum",
			resolve:    ResolveServicePrincipalSecretExpiry,
			input:      now.Add(ServicePrincipalSecretMaxLifetime + time.Nanosecond),
			wantErr:    ErrCredentialExpiryTooFar,
			wantErrSub: "service principal secret expiry exceeds maximum lifetime",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.resolve(test.input, now)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.wantErr)
				}
				if test.wantErrSub != "" && !strings.Contains(err.Error(), test.wantErrSub) {
					t.Fatalf("error = %q, want substring %q", err, test.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve expiry: %v", err)
			}
			if !got.Equal(test.want) {
				t.Fatalf("expiry = %s, want %s", got, test.want)
			}
		})
	}
}
