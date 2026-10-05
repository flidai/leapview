package composectl

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQualificationDeliveryEvidenceTokenIsDedicatedFromOtherCredentials(t *testing.T) {
	credentials := qualificationCredentials{
		PublisherToken: "publisher-secret",
		WorkloadToken:  "release-operator-secret",
	}
	if _, err := credentials.deliveryEvidenceToken(); err == nil {
		t.Fatal("deliveryEvidenceToken() error = nil, want a dedicated read credential")
	}
	credentials.DeliveryEvidenceToken = "delivery-read-secret"
	got, err := credentials.deliveryEvidenceToken()
	require.NoError(t, err)
	if string(got) != credentials.DeliveryEvidenceToken || string(got) == credentials.PublisherToken || string(got) == credentials.WorkloadToken {
		t.Fatalf("delivery evidence token = %q, want its dedicated read-only credential", got)
	}
}

func TestQualificationConnectionEvidenceTokenIsDedicatedFromOtherCredentials(t *testing.T) {
	credentials := qualificationCredentials{
		PublisherToken: "publisher-secret",
		WorkloadToken:  "release-operator-secret",
	}
	if _, err := credentials.connectionEvidenceToken(); err == nil {
		t.Fatal("connectionEvidenceToken() error = nil, want a dedicated read credential")
	}
	credentials.ConnectionEvidenceToken = "connection-read-secret"
	got, err := credentials.connectionEvidenceToken()
	require.NoError(t, err)
	if string(got) != credentials.ConnectionEvidenceToken || string(got) == credentials.PublisherToken || string(got) == credentials.WorkloadToken {
		t.Fatalf("connection evidence token = %q, want its dedicated read-only credential", got)
	}
}

func TestQualificationRecoveryUploadTokenIsDedicatedFromOtherCredentials(t *testing.T) {
	credentials := qualificationCredentials{
		PublisherToken: "publisher-secret",
		WorkloadToken:  "release-operator-secret",
	}
	if _, err := credentials.recoveryUploadToken(); err == nil {
		t.Fatal("recoveryUploadToken() error = nil, want a dedicated mutation credential")
	}
	credentials.RecoveryUploadToken = "recovery-upload-secret"
	got, err := credentials.recoveryUploadToken()
	require.NoError(t, err)
	if string(got) != credentials.RecoveryUploadToken || string(got) == credentials.PublisherToken || string(got) == credentials.WorkloadToken {
		t.Fatalf("recovery upload token = %q, want its dedicated exact-resource credential", got)
	}
}

func TestQualificationInstalledTokensRequireEvidenceCredentials(t *testing.T) {
	base := qualificationCredentials{
		PublisherToken:          "publisher-secret",
		WorkloadToken:           "release-operator-secret",
		DeliveryEvidenceToken:   "delivery-read-secret",
		ConnectionEvidenceToken: "connection-read-secret",
		RecoveryUploadToken:     "recovery-upload-secret",
		RecoveryControlToken:    "recovery-control-secret",
	}
	for _, test := range []struct {
		name    string
		missing string
		mutate  func(*qualificationCredentials)
	}{
		{name: "delivery", missing: "delivery-evidence", mutate: func(credentials *qualificationCredentials) {
			credentials.DeliveryEvidenceToken = ""
		}},
		{name: "connection", missing: "connection-evidence", mutate: func(credentials *qualificationCredentials) {
			credentials.ConnectionEvidenceToken = ""
		}},
		{name: "upload", missing: "recovery-upload", mutate: func(credentials *qualificationCredentials) {
			credentials.RecoveryUploadToken = ""
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			credentials := base
			test.mutate(&credentials)
			if _, err := credentials.installedTokens(); err == nil || !strings.Contains(err.Error(), test.missing) {
				t.Fatalf("installedTokens() error = %v, want missing dedicated %s token", err, test.missing)
			}
		})
	}
}

func TestQualificationDeliveryEvidenceActionsOnlyGrantDeliveryRead(t *testing.T) {
	got := qualificationActionNames(qualificationDeliveryEvidenceActions())
	if !slices.Equal(got, []string{"delivery.read"}) {
		t.Fatalf("delivery evidence actions = %v, want only delivery.read", got)
	}
}

func TestQualificationConnectionEvidenceActionsOnlyGrantConnectionRead(t *testing.T) {
	got := qualificationActionNames(qualificationConnectionEvidenceActions())
	if !slices.Equal(got, []string{"connection.read"}) {
		t.Fatalf("connection evidence actions = %v, want only connection.read", got)
	}
}

func TestQualificationActiveRevisionUsesDedicatedConnectionReadCredential(t *testing.T) {
	credentials := qualificationCredentials{ConnectionEvidenceToken: "connection-read-secret"}
	token, err := credentials.connectionEvidenceToken()
	require.NoError(t, err)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != qualificationManagedConnectionPath("project:leapview-evaluation")+"/active-revision" {
			t.Errorf("request = %s %s, want active-revision GET", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer connection-read-secret" {
			t.Errorf("Authorization = %q, want dedicated connection evidence bearer", got)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"revision":{"id":"revision-7"}}`)
	}))
	defer server.Close()
	for snapshot := 0; snapshot < 2; snapshot++ {
		revision, err := qualificationActiveManagedRevision(
			t.Context(), server.Client(), server.URL, "project:leapview-evaluation", token,
		)
		if err != nil {
			t.Fatalf("active revision snapshot %d: %v", snapshot, err)
		}
		if revision != "revision-7" {
			t.Fatalf("active revision snapshot %d = %q", snapshot, revision)
		}
	}
	if requests != 2 {
		t.Fatalf("active revision reads = %d, want baseline and post-interruption reads", requests)
	}
}

func TestQualificationActiveRevisionRejectsWrongActorWithForbidden(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer connection-read-secret" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		http.Error(w, "expected wrong actor", http.StatusBadRequest)
	}))
	defer server.Close()
	_, err := qualificationActiveManagedRevision(
		t.Context(), server.Client(), server.URL, "project:leapview-evaluation",
		qualificationConnectionEvidenceToken("project-data-token-from-previous-run"),
	)
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("wrong-actor active revision error = %v, want HTTP 403", err)
	}
}
