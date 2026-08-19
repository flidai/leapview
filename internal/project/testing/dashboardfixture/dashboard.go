package dashboardfixture

import (
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
)

// Compile crosses the same authoring-to-serving boundary as production. Test
// doubles use it so they cannot accidentally preserve the removed authoring
// dashboard runtime interface.
func Compile(report dashboarddocument.DashboardDocument, model *semanticmodel.Model) dashboarddefinition.Definition {
	result, err := dashboardcompiler.CompileDocument(report, map[string]*semanticmodel.Model{model.Name: model})
	if err != nil {
		panic(fmt.Sprintf("compile dashboard fixture: %v", err))
	}
	return result.Definition
}
