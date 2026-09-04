package configspec

import (
	"sort"
	"strconv"
	"testing"
	"time"
)

func TestCatalogIsCompleteAndDeterministic(t *testing.T) {
	settings := Settings()
	if len(settings) < 40 {
		t.Fatalf("Settings() returned %d entries, want the complete application and tooling catalog", len(settings))
	}

	orderedNames := make([]string, 0, len(settings))
	knownNames := map[string]string{}
	fields := map[string]string{}
	for _, setting := range settings {
		if setting.Name == "" || setting.Description == "" || setting.Category == "" || setting.Scope == "" || setting.Lifecycle == "" {
			t.Fatalf("setting is missing required metadata: %#v", setting)
		}
		if previous, ok := knownNames[setting.Name]; ok {
			t.Fatalf("duplicate environment variable %s on %s and %s", setting.Name, previous, setting.Field)
		}
		knownNames[setting.Name] = setting.Field
		if setting.Runtime && setting.Field == "" {
			t.Fatalf("runtime setting %s has no generated Config field", setting.Name)
		}
		if setting.Runtime {
			if previous, ok := fields[setting.Field]; ok {
				t.Fatalf("generated Config field %s is shared by %s and %s", setting.Field, previous, setting.Name)
			}
			fields[setting.Field] = setting.Name
		}
		if setting.Secret && setting.Example != "" && setting.Example != SecretPlaceholder {
			t.Fatalf("secret setting %s exposes a non-placeholder example", setting.Name)
		}
		if setting.Default != "" {
			switch setting.Type {
			case TypeBool:
				if _, err := strconv.ParseBool(setting.Default); err != nil {
					t.Fatalf("setting %s has invalid boolean default %q", setting.Name, setting.Default)
				}
			case TypeInt:
				if _, err := strconv.Atoi(setting.Default); err != nil {
					t.Fatalf("setting %s has invalid integer default %q", setting.Name, setting.Default)
				}
			case TypeInt64:
				if _, err := strconv.ParseInt(setting.Default, 10, 64); err != nil {
					t.Fatalf("setting %s has invalid 64-bit integer default %q", setting.Name, setting.Default)
				}
			case TypeDuration:
				if _, err := time.ParseDuration(setting.Default); err != nil {
					t.Fatalf("setting %s has invalid duration default %q", setting.Name, setting.Default)
				}
			}
		}
		orderedNames = append(orderedNames, setting.Name)
	}

	want := append([]string(nil), orderedNames...)
	sort.Strings(want)
	for index := range orderedNames {
		if orderedNames[index] != want[index] {
			t.Fatalf("catalog order is not stable at index %d: got %s, want %s", index, orderedNames[index], want[index])
		}
	}
}

func TestCatalogExcludesRemovedLegacySettings(t *testing.T) {
	removed := map[string]struct{}{
		"ADDR":                           {},
		"PORT":                           {},
		"LEAPVIEW_CATALOG_PATH":          {},
		"LEAPVIEW_DATA_DIR":              {},
		"LEAPVIEW_DUCKDB_PATH":           {},
		"LEAPVIEW_DUCKLAKE_CATALOG_PATH": {},
	}
	for _, setting := range Settings() {
		if _, ok := removed[setting.Name]; ok {
			t.Errorf("Settings() still contains removed setting %s", setting.Name)
		}
	}
}

func TestRulesOnlyReferenceCatalogSettings(t *testing.T) {
	known := map[string]struct{}{}
	for _, setting := range Settings() {
		known[setting.Name] = struct{}{}
	}
	for _, rule := range Rules() {
		if rule.ID == "" || rule.Description == "" || rule.Message == "" {
			t.Fatalf("rule is missing required metadata: %#v", rule)
		}
		for _, name := range rule.References() {
			if _, ok := known[name]; !ok {
				t.Fatalf("rule %s references unknown setting %s", rule.ID, name)
			}
		}
	}
}

func TestHTTPSURLRejectsAmbiguousIdentityEndpoints(t *testing.T) {
	predicate := HTTPSURL("IDENTITY_URL")
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "origin", value: "https://identity.example.com", want: true},
		{name: "issuer path", value: "https://identity.example.com/tenant/v2.0", want: true},
		{name: "userinfo", value: "https://client:secret@identity.example.com", want: false},
		{name: "query", value: "https://identity.example.com/issuer?tenant=prod", want: false},
		{name: "fragment", value: "https://identity.example.com/issuer#metadata", want: false},
		{name: "plain http", value: "http://identity.example.com", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := predicate.Evaluate(map[string]any{"IDENTITY_URL": test.value}); got != test.want {
				t.Fatalf("HTTPSURL(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestManagedDataStorageCatalogAndRelationships(t *testing.T) {
	known := map[string]Setting{}
	for _, setting := range Settings() {
		known[setting.Name] = setting
	}
	for _, name := range []string{
		"LEAPVIEW_MANAGED_DATA_BACKEND",
		"LEAPVIEW_MANAGED_DATA_DIR",
		"LEAPVIEW_MANAGED_DATA_S3_BUCKET",
		"LEAPVIEW_MANAGED_DATA_MAX_FILE_BYTES",
		"LEAPVIEW_MANAGED_DATA_UPLOAD_SESSION_TTL",
		"LEAPVIEW_MANAGED_DATA_GC_INTERVAL",
		"LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES",
	} {
		if _, ok := known[name]; !ok {
			t.Fatalf("managed data setting %s is missing", name)
		}
	}

	valid := map[string]any{
		"LEAPVIEW_MANAGED_DATA_BACKEND":            "local",
		"LEAPVIEW_MANAGED_DATA_DIR":                "/var/lib/leapview/managed-data",
		"LEAPVIEW_MANAGED_DATA_MAX_FILES":          100,
		"LEAPVIEW_MANAGED_DATA_MAX_FILE_BYTES":     1024,
		"LEAPVIEW_MANAGED_DATA_MAX_REVISION_BYTES": 4096,
		"LEAPVIEW_MANAGED_DATA_UPLOAD_SESSION_TTL": time.Hour,
		"LEAPVIEW_MANAGED_DATA_GC_INTERVAL":        time.Minute,
		"LEAPVIEW_MANAGED_DATA_GC_GRACE_PERIOD":    time.Hour,
		"LEAPVIEW_MANAGED_DATA_MIN_FREE_BYTES":     2048,
	}
	if err := Validate(valid); err != nil {
		t.Fatalf("valid local managed data config: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown backend", mutate: func(values map[string]any) { values["LEAPVIEW_MANAGED_DATA_BACKEND"] = "database" }},
		{name: "file exceeds revision", mutate: func(values map[string]any) { values["LEAPVIEW_MANAGED_DATA_MAX_FILE_BYTES"] = 8192 }},
		{name: "zero session ttl", mutate: func(values map[string]any) { values["LEAPVIEW_MANAGED_DATA_UPLOAD_SESSION_TTL"] = time.Duration(0) }},
		{name: "s3 incomplete", mutate: func(values map[string]any) { values["LEAPVIEW_MANAGED_DATA_BACKEND"] = "s3" }},
		{name: "s3 missing runtime cache", mutate: func(values map[string]any) {
			values["LEAPVIEW_MANAGED_DATA_BACKEND"] = "s3"
			values["LEAPVIEW_MANAGED_DATA_S3_BUCKET"] = "bucket"
			values["LEAPVIEW_MANAGED_DATA_S3_REGION"] = "eu-west-1"
			delete(values, "LEAPVIEW_MANAGED_DATA_DIR")
		}},
		{name: "partial s3 credentials", mutate: func(values map[string]any) {
			values["LEAPVIEW_MANAGED_DATA_BACKEND"] = "s3"
			values["LEAPVIEW_MANAGED_DATA_S3_BUCKET"] = "bucket"
			values["LEAPVIEW_MANAGED_DATA_S3_REGION"] = "eu-west-1"
			values["LEAPVIEW_MANAGED_DATA_S3_ACCESS_KEY_ID"] = "key"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := make(map[string]any, len(valid))
			for key, value := range valid {
				values[key] = value
			}
			test.mutate(values)
			if err := Validate(values); err == nil {
				t.Fatal("Validate() unexpectedly succeeded")
			}
		})
	}

	s3 := make(map[string]any, len(valid)+5)
	for key, value := range valid {
		s3[key] = value
	}
	s3["LEAPVIEW_MANAGED_DATA_BACKEND"] = "s3"
	s3["LEAPVIEW_MANAGED_DATA_S3_BUCKET"] = "bucket"
	s3["LEAPVIEW_MANAGED_DATA_S3_REGION"] = "eu-west-1"
	s3["LEAPVIEW_MANAGED_DATA_S3_ACCESS_KEY_ID"] = "key"
	s3["LEAPVIEW_MANAGED_DATA_S3_SECRET_ACCESS_KEY"] = "secret"
	if err := Validate(s3); err != nil {
		t.Fatalf("valid S3 managed data config: %v", err)
	}
}
