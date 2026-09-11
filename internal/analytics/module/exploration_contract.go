package module

import (
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// SemanticModel and CompiledSemanticModel are the analytics-owned contract
// types used by process-composed capabilities that need an activation-paired
// model. Keeping these aliases on the module surface prevents composition
// code from importing analytics implementation packages directly.
type SemanticModel = semanticmodel.Model
type CompiledSemanticModel = semanticquery.CompiledModel
