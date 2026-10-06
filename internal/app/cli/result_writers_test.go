package cli

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/app/cli/localdocker"
	"github.com/flidai/leapview/internal/app/cli/localruntime"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/spf13/cobra"
)

type resultErrorWriter struct{ err error }

func (writer resultErrorWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestLocalLifecycleCommandsPropagateResultWriterFailures(t *testing.T) {
	failure := errors.New("result stream closed")
	for _, arguments := range [][]string{{"status"}, {"stop"}, {"reset"}, {"reset", "--confirm", "exact"}} {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			parent := &cobra.Command{Use: "dev", SilenceErrors: true, SilenceUsage: true}
			runtime := &fakeLocalRuntimeLifecycle{resetPlan: localruntime.ResetPlan{Confirmation: "exact"}}
			addLocalDevLifecycleCommands(t.Context(), parent, localLifecycleTestResolver, func(localdocker.Endpoint, *cobra.Command) (localRuntimeLifecycle, error) { return runtime, nil }, nil)
			parent.SetOut(resultErrorWriter{failure})
			parent.SetErr(io.Discard)
			parent.SetArgs(arguments)
			if err := parent.Execute(); !errors.Is(err, failure) {
				t.Fatalf("error = %v, want result writer failure", err)
			}
		})
	}
}

func TestHealthcheckPropagatesResultWriterFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	failure := errors.New("result stream closed")
	if err := runHealthcheck(t.Context(), &rootOptions{healthcheckURL: server.URL}, resultErrorWriter{failure}); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want result writer failure", err)
	}
}

func TestPublicationPropagatesResultWriterFailure(t *testing.T) {
	failure := errors.New("result stream closed")
	operations := projectPublishOperations{client: fixedTransportClient{transport: &publishTransportStub{}}}
	checkpoint := projectcli.CandidateCheckpoint{ProjectID: "finance", CandidateID: "cand_1", PlanID: "plan_1", PlanDigest: "sha256:" + strings.Repeat("b", 64)}
	if err := operations.Publish(t.Context(), projectcli.PublishOptions{
		Credentials: cliapi.Credentials{Target: "https://target.example", Token: "token"}, Checkpoint: checkpoint,
	}, resultErrorWriter{failure}); !errors.Is(err, failure) {
		t.Fatalf("error = %v, want result writer failure", err)
	}
}
