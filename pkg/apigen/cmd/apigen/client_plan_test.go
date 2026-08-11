package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveGoPackageOutput_EnablesTypedClientExplicitly(t *testing.T) {
	t.Helper()

	output, err := resolveGoPackageOutput("go_out", goPackageOutputSpec{
		Dir:               "internal/widget/api/gen",
		Package:           "widgetapi",
		ServerFile:        "server.gen.go",
		RequestModelsFile: "models.gen.go",
		ClientFile:        "client.gen.go",
	}, false)

	require.NoError(t, err)
	require.Equal(t, "client.gen.go", output.ClientFile)
}

func TestResolveGoPackageOutput_LeavesTypedClientDisabledWhenOmitted(t *testing.T) {
	t.Helper()

	output, err := resolveGoPackageOutput("go_out", goPackageOutputSpec{
		Dir:     "internal/widget/api/gen",
		Package: "widgetapi",
	}, false)

	require.NoError(t, err)
	require.Empty(t, output.ClientFile)
}

func TestResolveGoPackageOutput_RejectsClientFileCollision(t *testing.T) {
	t.Helper()

	_, err := resolveGoPackageOutput("go_out", goPackageOutputSpec{
		Dir:        "internal/widget/api/gen",
		Package:    "widgetapi",
		ServerFile: "generated.go",
		ClientFile: "generated.go",
	}, false)

	require.ErrorContains(t, err, "server_file and client_file must be different")
}

func TestNormalizeGoPackagePlan_RejectsClientOnAggregatePackage(t *testing.T) {
	t.Helper()

	_, err := normalizeGoPackagePlan(goOutputSpec{
		Unmatched: "error",
		Aggregate: &goPackageOutputSpec{
			Dir:        "internal/app/api/gen",
			Package:    "aggregate",
			ClientFile: "client.apigen.gen.go",
		},
		Packages: map[string]goPackageOutputSpec{
			"ExampleAPI.Widget": {
				Dir:        "internal/widget/api/gen",
				Package:    "widgetapi",
				ImportPath: "example.com/project/internal/widget/api/gen",
			},
		},
	})

	require.ErrorContains(t, err, "go_out.aggregate.client_file is not supported")
}
