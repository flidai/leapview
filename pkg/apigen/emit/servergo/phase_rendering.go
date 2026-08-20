package servergo

import "github.com/Yacobolo/toolbelt/apigen/ir"

// render is the rendering seam for generated Go. The large endpoint/template
// translator remains cohesive in renderGeneratedServer, while emission keeps
// plan discovery and validation independent of formatting details.
func render(doc ir.Document, opts Options, plan emissionPlan) ([]byte, error) {
	return renderGeneratedServer(doc, opts, plan)
}
