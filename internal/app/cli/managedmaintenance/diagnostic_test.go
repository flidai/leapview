package managedmaintenance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestManagedDiagnosticChild(t *testing.T) {
	if os.Getenv("LEAPVIEW_DIAGNOSTIC_CHILD") != "1" {
		return
	}
	fmt.Fprint(os.Stderr, strings.Repeat("private-secret", 10000))
	fmt.Fprintln(os.Stderr, " Net::SSH::AuthenticationFailed private-password")
	os.Exit(23)
}

func TestManagedCommandDiagnosticPreservesExitWithoutPrivateOutput(t *testing.T) {
	_, err := runBoundedCommand(context.Background(), os.Args[0], []string{"-test.run=^TestManagedDiagnosticChild$"}, []string{"LEAPVIEW_DIAGNOSTIC_CHILD=1"}, "")
	var child *exec.ExitError
	if !errors.As(err, &child) || child.ExitCode() != 23 {
		t.Fatalf("lost child exit: %v", err)
	}
	var out bytes.Buffer
	writeFailureDiagnostic(&out, &ingressFailure{stage: "proxy-reboot", err: err})
	if !strings.Contains(out.String(), `"reason":"ssh-authentication"`) || !strings.Contains(out.String(), `"exitCode":23`) || !strings.Contains(out.String(), `"stage":"proxy-reboot"`) {
		t.Fatalf("missing bounded diagnostic: %s", out.String())
	}
	if strings.Contains(out.String(), "private") {
		t.Fatal("private output retained")
	}
}

func TestManagedDiagnosticTailBoundAndUnknown(t *testing.T) {
	var tail diagnosticTail
	for range 100 {
		_, _ = tail.Write(bytes.Repeat([]byte("private"), 10000))
	}
	if len(tail.data) != diagnosticLimit {
		t.Fatalf("unbounded capture: %d", len(tail.data))
	}
	if reason := classifyCommandFailure(tail.data); reason != "unknown" {
		t.Fatalf("unexpected reason: %s", reason)
	}
	_, _ = tail.Write([]byte(" Net::SSH::HostKeyMismatch secret"))
	if classifyCommandFailure(tail.data) != "ssh-host-key" {
		t.Fatal("lost final error")
	}
}

func TestManagedIngressDiagnosticsPreserveBoundaryAndCause(t *testing.T) {
	sentinel := errors.New("private injected failure")
	for _, stage := range []string{"gate-write", "proxy-reboot", "inventory", "proxy-running"} {
		err := &ingressFailure{stage: stage, err: sentinel}
		if !errors.Is(err, sentinel) {
			t.Fatal("lost typed cause")
		}
		var out bytes.Buffer
		writeFailureDiagnostic(&out, err)
		if strings.Contains(out.String(), "private") || !strings.Contains(out.String(), `"stage":"`+stage+`"`) {
			t.Fatal("unsafe or missing stage")
		}
	}
}

func TestKamalCloseIngressDiagnosticBoundaries(t *testing.T) {
	for _, stage := range []string{"gate-write", "proxy-reboot", "inventory", "proxy-running"} {
		t.Run(stage, func(t *testing.T) {
			k, _, _ := adapterFixture(t)
			sentinel := errors.New("private boundary failure")
			if stage == "gate-write" {
				k.Profile.StateRoot = k.Profile.Root + "/environment.json"
			}
			k.run = func(_ context.Context, bin string, _ []string, _ []string, _ string) ([]byte, error) {
				if (bin == "bundle" && stage == "proxy-reboot") || (bin == "docker" && stage == "inventory") {
					return nil, sentinel
				}
				return nil, nil
			}
			err := k.CloseIngress(t.Context())
			var failure *ingressFailure
			if !errors.As(err, &failure) || failure.stage != stage {
				t.Fatalf("wrong boundary: %v", err)
			}
			if (stage == "proxy-reboot" || stage == "inventory") && !errors.Is(err, sentinel) {
				t.Fatal("lost injected error")
			}
		})
	}
}

func TestManagedCommandFailureCategories(t *testing.T) {
	for input, want := range map[string]string{
		"Bundler::GemNotFound private": "bundler-dependency", "cannot load such file private": "ruby-load",
		"Cannot connect to the Docker daemon private": "docker-daemon", "No such image private": "docker-image",
		"network kamal not found": "docker-network", "managed controller lock identity mismatch": "controller-lock",
		"managed mutation requires a live bounded deadline": "controller-deadline", "private unknown detail": "unknown",
	} {
		if got := classifyCommandFailure([]byte(input)); got != want {
			t.Fatalf("classification %s != %s", got, want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newCommandFailure(ctx, "bundle", context.Canceled, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	var out bytes.Buffer
	writeFailureDiagnostic(&out, err)
	if !strings.Contains(out.String(), `"reason":"canceled"`) {
		t.Fatal("cancellation classification lost")
	}
}

func TestManagedDiagnosticDoesNotMisattributeJoinedCleanup(t *testing.T) {
	primary := &commandFailure{command: "docker", reason: "docker-image", exitCode: 9, err: errors.New("private primary")}
	cleanup := &ingressFailure{stage: "proxy-reboot", err: &commandFailure{command: "bundle", reason: "ssh-authentication", exitCode: 1, err: errors.New("private cleanup")}}
	var out bytes.Buffer
	writeFailureDiagnostic(&out, errors.Join(primary, cleanup))
	if !strings.Contains(out.String(), `"stage":"command"`) || !strings.Contains(out.String(), `"exitCode":9`) || strings.Contains(out.String(), "private") {
		t.Fatal("primary failure misattributed to cleanup")
	}
}
