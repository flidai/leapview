package manageddata

import "testing"

func TestLifecycleTransitionsRejectRegressions(t *testing.T) {
	tests := []struct {
		name string
		from interface{ CanTransitionTo(string) bool }
		to   string
		want bool
	}{
		{name: "upload open to committing", from: UploadStatusOpen, to: string(UploadStatusCommitting), want: true},
		{name: "upload complete is terminal", from: UploadStatusComplete, to: string(UploadStatusOpen), want: false},
		{name: "revision pending to ready", from: RevisionStatusPending, to: string(RevisionStatusReady), want: true},
		{name: "revision ready is immutable", from: RevisionStatusReady, to: string(RevisionStatusFailed), want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.from.CanTransitionTo(test.to); got != test.want {
				t.Fatalf("CanTransitionTo(%q) = %v, want %v", test.to, got, test.want)
			}
		})
	}
}

func TestNormalizeEnvironmentRequiresRouteSafeGlobalName(t *testing.T) {
	for _, value := range []string{"dev", "production", "eu-west-1"} {
		got, err := NormalizeEnvironment(value)
		if err != nil {
			t.Fatalf("NormalizeEnvironment(%q): %v", value, err)
		}
		if got != Environment(value) {
			t.Fatalf("NormalizeEnvironment(%q) = %q", value, got)
		}
	}
	for _, value := range []string{"", "Production", "../prod", "prod space", " prod", "prod "} {
		if _, err := NormalizeEnvironment(value); err == nil {
			t.Fatalf("NormalizeEnvironment(%q) unexpectedly succeeded", value)
		}
	}
}
