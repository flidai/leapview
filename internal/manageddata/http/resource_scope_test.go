package http

import "testing"

func TestValidScopeUsesCanonicalProjectResourceIDs(t *testing.T) {
	for _, test := range []struct {
		name       string
		project    string
		connection string
		want       bool
	}{
		{name: "namespaced project", project: "project:leapview-showcase", connection: "olist", want: true},
		{name: "namespaced connection", project: "project_demo", connection: "connection:olist", want: true},
		{name: "whitespace denied", project: "project demo", connection: "olist", want: false},
		{name: "path denied", project: "project/demo", connection: "olist", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validScope(test.project, test.connection); got != test.want {
				t.Fatalf("validScope(%q, %q) = %t, want %t", test.project, test.connection, got, test.want)
			}
		})
	}
}
