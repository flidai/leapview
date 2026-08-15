package dashboardfixture

import (
	"fmt"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardcompiler "github.com/flidai/leapview/internal/dashboard/compiler"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
)

// Compile crosses the same authoring-to-serving boundary as production. Test
// doubles use it so they cannot accidentally preserve the removed authoring
// dashboard runtime interface.
func Compile(report dashboardauthoring.Dashboard, model *semanticmodel.Model) dashboarddefinition.Definition {
	result, err := dashboardcompiler.Compile(report, map[string]*semanticmodel.Model{model.Name: model})
	if err != nil {
		panic(fmt.Sprintf("compile dashboard fixture: %v", err))
	}
	return result.Definition
}
