package contractprojection

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	contracts "github.com/flidai/leapview/internal/project/contracts"
	graph "github.com/flidai/leapview/internal/project/graph"
)

func TestSourceDeprecationContext(t *testing.T) {
	tests := []struct {
		name    string
		version string
		fields  string
		wantErr string
	}{
		{
			name:    "missing replacement",
			version: "1.0.0",
			fields:  `"legacy":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use current","replacement":"current"}}`,
			wantErr: `field "legacy" replacement "current" does not exist`,
		},
		{
			name:    "self replacement",
			version: "1.0.0",
			fields:  `"legacy":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Do not use","replacement":"legacy"}}`,
			wantErr: `field "legacy" cannot replace itself`,
		},
		{
			name:    "two node cycle",
			version: "1.0.0",
			fields:  `"a":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use b","replacement":"b"}},"b":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use a","replacement":"a"}}`,
			wantErr: `replacement cycle: a -> b -> a`,
		},
		{
			name:    "multi node cycle",
			version: "1.0.0",
			fields:  `"a":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use b","replacement":"b"}},"b":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use c","replacement":"c"}},"c":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use a","replacement":"a"}}`,
			wantErr: `replacement cycle: a -> b -> c -> a`,
		},
		{
			name:    "deprecation after contract version",
			version: "1.9.0",
			fields:  `"legacy":{"datatype":"String","deprecation":{"since":"2.0.0","reason":"Use current","replacement":"current"}},"current":{"datatype":"String"}`,
			wantErr: `field "legacy" deprecation since "2.0.0" is later than contract version "1.9.0"`,
		},
		{
			name:    "valid replacement chain",
			version: "1.2.0",
			fields:  `"a":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use b","replacement":"b"}},"b":{"datatype":"String","deprecation":{"since":"1.1.0","reason":"Use c","replacement":"c"}},"c":{"datatype":"String"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection, err := projectDeprecationSource(t, test.version, test.fields)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("ProjectSource() error = %v, want containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ProjectSource() error = %v", err)
			}
			canonical, err := CanonicalBytes(projection)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSourcePublication(canonical); err != nil {
				t.Fatalf("DecodeSourcePublication() error = %v", err)
			}
		})
	}
}

func TestSourcePublicationReplayRejectsInvalidDeprecationContext(t *testing.T) {
	projection, err := projectDeprecationSource(t, "1.2.0", `"legacy":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use current","replacement":"current"}},"current":{"datatype":"String"}`)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CanonicalBytes(projection)
	if err != nil {
		t.Fatal(err)
	}
	invalid := bytes.Replace(canonical, []byte(`"replacement":"current"`), []byte(`"replacement":"missing"`), 1)
	if _, err := DecodeSourcePublication(invalid); err == nil || !strings.Contains(err.Error(), `replacement "missing" does not exist`) {
		t.Fatalf("DecodeSourcePublication() error = %v, want missing replacement", err)
	}
}

func TestModelDeprecationUsesContextualValidation(t *testing.T) {
	projectGraph, err := graph.NewProjectGraph([]graph.Resource{{ID: "source:orders", Name: "orders", Kind: graph.KindSource}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	context, err := NewReferenceContext(projectGraph)
	if err != nil {
		t.Fatal(err)
	}
	var input contracts.Model
	raw := `{"apiVersion":"leapview.dev/v1","kind":"Model","metadata":{"id":"model:orders","name":"orders","contract":{"version":"1.0.0","compatibility":"backward"}},"spec":{"definition":{"type":"direct","source":"orders"},"entities":{"row":{"type":"primary","fields":["legacy"]}},"grain":{"entity":"row"},"fields":{"legacy":{"datatype":"String","deprecation":{"since":"1.0.0","reason":"Use current","replacement":"current"}}}}}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectModel(input, Contract{}, context); err == nil || !strings.Contains(err.Error(), `field "legacy" replacement "current" does not exist`) {
		t.Fatalf("ProjectModel() error = %v, want missing replacement", err)
	}
}

func projectDeprecationSource(t *testing.T, version, fields string) (Source, error) {
	t.Helper()
	var input contracts.Source
	raw := `{"apiVersion":"leapview.dev/v1","kind":"Source","metadata":{"id":"source:orders","name":"orders","contract":{"version":"` + version + `","compatibility":"backward"}},"spec":{"connection":"warehouse","location":{"type":"path","path":"orders.csv","format":"csv"},"schema":{"mode":"strict","fields":{` + fields + `}}}}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	return ProjectSource(input, Contract{})
}
