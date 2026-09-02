package capabilities

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform/buildinfo"
)

func TestWriteReportsRuntimeBuildIdentity(t *testing.T) {
	recorder := httptest.NewRecorder()
	identity := buildinfo.Identity{
		Version: "0.2.0-rc.1", Revision: strings.Repeat("d", 40),
		BuildTime: "2026-07-27T09:00:00Z",
	}
	Write(recorder, Config{Environment: "prod", BuildIdentity: identity})

	var response struct {
		BuildVersion     string `json:"buildVersion"`
		BuildRevision    string `json:"buildRevision"`
		BuildTime        string `json:"buildTime"`
		BuildDirty       bool   `json:"buildDirty"`
		BuildDevelopment bool   `json:"buildDevelopment"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.BuildVersion != identity.Version ||
		response.BuildRevision != identity.Revision ||
		response.BuildTime != identity.BuildTime ||
		response.BuildDirty != identity.Dirty ||
		response.BuildDevelopment != identity.Development {
		t.Fatalf("capabilities build identity = %#v", response)
	}
}

func TestWriteReportsOnlyRuntimeQueryFormats(t *testing.T) {
	for _, tc := range []struct {
		name  string
		arrow bool
		want  int
	}{
		{name: "json-only runtime", want: 1},
		{name: "native arrow runtime", arrow: true, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Write(recorder, Config{Arrow: tc.arrow})
			var response struct {
				QueryFormats []string `json:"queryFormats"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.QueryFormats) != tc.want {
				t.Fatalf("queryFormats=%v, want %d entries", response.QueryFormats, tc.want)
			}
			for _, format := range response.QueryFormats {
				if format == "application/vnd.apache.arrow.stream" && !tc.arrow {
					t.Fatal("JSON-only runtime advertised native Arrow")
				}
			}
		})
	}
}

func TestWriteReportsComposedDeliveryMode(t *testing.T) {
	for _, test := range []struct {
		name   string
		native bool
		want   string
	}{
		{name: "legacy sqlite"},
		{name: "native postgres", native: true, want: "native_postgres"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Write(recorder, Config{NativeDeliveryMutations: test.native})
			var response struct {
				DeliveryMode string `json:"deliveryMode"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			want := test.want
			if want == "" {
				want = "legacy_sqlite"
			}
			if response.DeliveryMode != want {
				t.Fatalf("deliveryMode=%q, want %q", response.DeliveryMode, want)
			}
		})
	}
}
